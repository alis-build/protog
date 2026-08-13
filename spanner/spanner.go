// Package spanner contains the 'spanner' command with all its sub commands
package spanner

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	spannerAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	spannerPb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"

	"github.com/alis-build/protog/diff"
	"github.com/alis-build/protog/fds"
	"github.com/alis-build/protog/output"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spanner",
		Short: "View, plan or deploy protobundles to a Spanner database",
	}
	cmd.AddCommand(viewCmd())
	cmd.AddCommand(planCmd())
	cmd.AddCommand(deployCmd())
	return cmd
}

type viewResult struct {
	Database string   `json:"database"`
	Types    []string `json:"types"`
}

func (r viewResult) Lines() []string { return r.Types }

func viewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view",
		Short: "View current protobundles in a Spanner database",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("expecting one argument in format '{projectID}/{spannerInstance}/{database}")
			}
			parts := strings.Split(args[0], "/")
			if len(parts) != 3 {
				return fmt.Errorf("expecting one argument in format '{projectID}/{spannerInstance}/{database}")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			parts := strings.Split(args[0], "/")
			database := fmt.Sprintf("projects/%s/instances/%s/databases/%s", parts[0], parts[1], parts[2])
			client, bundles, err := clientAndProtobundles(cmd.Context(), database)
			if err != nil {
				return fmt.Errorf("viewing proto bundles: %w", err)
			}
			defer client.Close()
			output.Result(viewResult{Database: database, Types: slices.Sorted(maps.Keys(bundles))})
			return nil
		},
	}
	return cmd
}

func SpannerAdmin(ctx context.Context) (*spannerAdmin.DatabaseAdminClient, error) {
	client, err := spannerAdmin.NewDatabaseAdminClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("spanner.NewDatabaseAdminClient: %w", err)
	}
	return client, nil
}

func clientAndProtobundles(ctx context.Context, database string) (*spannerAdmin.DatabaseAdminClient, map[string]struct{}, error) {
	client, err := SpannerAdmin(ctx)
	if err != nil {
		return nil, nil, err
	}
	bundles, err := retryUnauthenticated(
		func() (map[string]struct{}, error) {
			return viewProtobundles(ctx, client, database)
		},
		func() error {
			return refreshSpannerAdmin(ctx, &client)
		},
	)
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		return nil, nil, err
	}
	return client, bundles, nil
}

func refreshSpannerAdmin(ctx context.Context, client **spannerAdmin.DatabaseAdminClient) error {
	output.AuthRetry("cached credential rejected", 2)
	if *client != nil {
		_ = (*client).Close()
		*client = nil
	}
	refreshed, err := spannerAdmin.NewDatabaseAdminClient(ctx)
	if err != nil {
		return fmt.Errorf("spanner.NewDatabaseAdminClient: %w", err)
	}
	*client = refreshed
	return nil
}

// retryUnauthenticated retries once after replacing the client that supplied
// the rejected cached credential.
func retryUnauthenticated[T any](fn func() (T, error), refresh func() error) (T, error) {
	result, err := fn()
	if status.Code(err) != codes.Unauthenticated {
		return result, err
	}
	if err := refresh(); err != nil {
		var zero T
		return zero, fmt.Errorf("refreshing credentials: %w", err)
	}
	return fn()
}

func viewProtobundles(ctx context.Context, client *spannerAdmin.DatabaseAdminClient, database string) (map[string]struct{}, error) {
	target := output.SpannerDatabase(database)
	output.RefreshStart(target)
	bundles := map[string]struct{}{}
	getDatabaseDdlRes, err := client.GetDatabaseDdl(ctx, &spannerPb.GetDatabaseDdlRequest{
		Database: database,
	})
	if err != nil {
		return nil, fmt.Errorf("spanner.GetDatabaseDdl: %w", err)
	}
	for _, ddl := range getDatabaseDdlRes.GetStatements() {
		if strings.HasPrefix(ddl, "CREATE PROTO BUNDLE") {
			commaSepTypes := strings.TrimPrefix(ddl, "CREATE PROTO BUNDLE (\n")
			for t := range strings.SplitSeq(commaSepTypes, ",\n") {
				t = strings.Trim(t, " ")
				t = strings.Trim(t, "`")
				if t == "" || t == ")" {
					continue
				}
				bundles[t] = struct{}{}
			}
		}
	}
	output.RefreshComplete(target, len(bundles))
	return bundles, nil
}

type planResult struct {
	Operation string          `json:"operation"`
	Target    output.Resource `json:"target"`
	Create    []string        `json:"create"`
	Update    []string        `json:"update"`
	Delete    []string        `json:"delete"`
	Ignore    []string        `json:"ignore"`
	Changes   output.Summary  `json:"changes"`
	Statement string          `json:"statement,omitempty"`
}

type deployResult struct {
	planResult
	Lro     *LroInfo `json:"lro,omitempty"`
	Success bool     `json:"success"`
}

// LroInfo reports the Spanner UpdateDatabaseDdl long-running operation.
type LroInfo struct {
	Name             string   `json:"name"`
	Done             bool     `json:"done"`
	Throttled        bool     `json:"throttled"`
	CommitTimestamps []string `json:"commit_timestamps,omitempty"`
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview protobundle changes without deploying them",
		Args:  planOrDeployArgValidation,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := NewPlan(cmd, args)
			if err != nil {
				return err
			}
			defer plan.Close()
			plan.Emit(output.Get(), output.SpannerType(plan.Database), "plan", &diff.PrintOptions{PrintIgnored: true})
			output.Result(plan.result("plan"))
			return nil
		},
	}
	return cmd
}

func planOrDeployArgValidation(cmd *cobra.Command, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("expecting '{projectID}/{spannerInstance}/{database}' {pathToFdsFile} (optional list of packages)")
	}
	if len(strings.Split(args[0], "/")) != 3 {
		return fmt.Errorf("expecting '{projectID}/{spannerInstance}/{database}' as the first argument")
	}
	return nil
}

func NewPlan(cmd *cobra.Command, args []string) (*Plan, error) {
	dbParts := strings.Split(args[0], "/")
	fdsFilePath := args[1]
	fdsTypes, fdsBytes, err := fds.ParseFdsTypes(fdsFilePath)
	if err != nil {
		return nil, err
	}
	database := fmt.Sprintf("projects/%s/instances/%s/databases/%s", dbParts[0], dbParts[1], dbParts[2])
	client, bundles, err := clientAndProtobundles(cmd.Context(), database)
	if err != nil {
		return nil, fmt.Errorf("viewing proto bundles: %w", err)
	}
	plan := &Plan{
		Client:   client,
		Database: database,
		FdsBytes: fdsBytes,
	}
	plan.NoExistingTypes = len(bundles) == 0
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	plan.Diff = diff.New(bundles, fdsTypes, packageIDs)
	return plan, nil
}

type Plan struct {
	Client          *spannerAdmin.DatabaseAdminClient
	Database        string
	FdsBytes        []byte
	NoExistingTypes bool
	*diff.Diff
}

func (p *Plan) Close() {
	if p.Client != nil {
		_ = p.Client.Close()
	}
}

func (p *Plan) result(operation string) planResult {
	return planResult{
		Operation: operation,
		Target:    output.SpannerDatabase(p.Database),
		Create:    p.Create,
		Update:    p.Update,
		Delete:    p.Delete,
		Ignore:    p.Ignore,
		Changes:   p.Diff.Summary(""),
		Statement: p.Statement(),
	}
}

func (p *Plan) action() output.Action {
	if p.NoExistingTypes {
		return output.ActionCreate
	}
	return output.ActionUpdate
}

// Deploy submits the proto bundle DDL and waits for the long-running
// operation, emitting progress from its metadata. The returned LroInfo is nil
// only when the DDL submission itself failed.
func (p *Plan) Deploy(ctx context.Context) (*LroInfo, error) {
	target := output.SpannerDatabase(p.Database)
	statement := p.Statement()
	output.ApplyStart(p.action(), target, statement)
	start := time.Now()
	request := &spannerPb.UpdateDatabaseDdlRequest{
		Database:         p.Database,
		Statements:       []string{statement},
		ProtoDescriptors: p.FdsBytes,
	}
	op, err := retryUnauthenticated(
		func() (*spannerAdmin.UpdateDatabaseDdlOperation, error) {
			return p.Client.UpdateDatabaseDdl(ctx, request)
		},
		func() error {
			return refreshSpannerAdmin(ctx, &p.Client)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("updating Spanner Database DDL: %w", err)
	}
	operationName := op.Name()
	info := &LroInfo{Name: operationName}
	lastPercent := int32(-1)
	_, err = retryUnauthenticated(
		func() (struct{}, error) {
			return struct{}{}, p.waitWithProgress(ctx, op, target, start, info, &lastPercent)
		},
		func() error {
			if err := refreshSpannerAdmin(ctx, &p.Client); err != nil {
				return err
			}
			op = p.Client.UpdateDatabaseDdlOperation(operationName)
			return nil
		},
	)
	if err != nil {
		output.ApplyErrored(p.action(), target, err, time.Since(start))
		return info, fmt.Errorf("waiting for Spanner Database DDL update to complete: %w", err)
	}
	info.Done = true
	output.ApplyComplete(p.action(), target, operationName, time.Since(start))
	return info, nil
}

// waitWithProgress polls the operation until done, emitting an apply_progress
// event whenever the reported percentage changes. It replaces op.Wait, which
// blocks without exposing the operation metadata.
func (p *Plan) waitWithProgress(ctx context.Context, op *spannerAdmin.UpdateDatabaseDdlOperation, target output.Resource, start time.Time, info *LroInfo, lastPercent *int32) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if md, mdErr := op.Metadata(); mdErr == nil && md != nil {
			info.Throttled = md.GetThrottled()
			info.CommitTimestamps = commitTimestamps(md)
			if progress := md.GetProgress(); len(progress) > 0 {
				if pct := progress[0].GetProgressPercent(); pct != *lastPercent {
					*lastPercent = pct
					output.ApplyProgress(target, op.Name(), pct, md.GetThrottled(), time.Since(start))
				}
			}
		}
		if op.Done() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if err := op.Poll(ctx); err != nil {
			return err
		}
	}
}

func commitTimestamps(md *spannerPb.UpdateDatabaseDdlMetadata) []string {
	var timestamps []string
	for _, ts := range md.GetCommitTimestamps() {
		timestamps = append(timestamps, ts.AsTime().UTC().Format(time.RFC3339Nano))
	}
	return timestamps
}

func (p *Plan) Statement() string {
	if p.NoExistingTypes {
		return fmt.Sprintf("CREATE PROTO BUNDLE (`%s`)", strings.Join(p.Create, "`,`"))
	}
	statement := "ALTER PROTO BUNDLE"
	if len(p.Create) > 0 {
		statement += " " + fmt.Sprintf("INSERT(`%s`)", strings.Join(p.Create, "`,`"))
	}
	if len(p.Update) > 0 {
		statement += " " + fmt.Sprintf("UPDATE(`%s`)", strings.Join(p.Update, "`,`"))
	}
	if len(p.Delete) > 0 {
		statement += " " + fmt.Sprintf("DELETE(`%s`)", strings.Join(p.Delete, "`,`"))
	}
	return statement
}

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy protobundle changes to a Spanner database",
		Args:  planOrDeployArgValidation,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := NewPlan(cmd, args)
			if err != nil {
				return err
			}
			defer plan.Close()
			plan.Emit(output.Get(), output.SpannerType(plan.Database), "deploy", &diff.PrintOptions{})
			lro, err := plan.Deploy(cmd.Context())
			if lro == nil {
				return err
			}
			output.Result(deployResult{
				planResult: plan.result("deploy"),
				Lro:        lro,
				Success:    err == nil,
			})
			return err
		},
	}
	return cmd
}
