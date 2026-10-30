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
		Short: "View, plan, deploy or destroy protobundles in a Spanner database",
	}
	cmd.AddCommand(viewCmd())
	cmd.AddCommand(planCmd())
	cmd.AddCommand(deployCmd())
	cmd.AddCommand(planDestroyCmd())
	cmd.AddCommand(destroyCmd())
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
			output.Result(viewResult{Database: database, Types: slices.Sorted(maps.Keys(bundles.Types))})
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

func clientAndProtobundles(ctx context.Context, database string) (*spannerAdmin.DatabaseAdminClient, *bundleSnapshot, error) {
	client, err := SpannerAdmin(ctx)
	if err != nil {
		return nil, nil, err
	}
	bundles, err := retryUnauthenticated(
		func() (*bundleSnapshot, error) {
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

func viewProtobundles(ctx context.Context, client *spannerAdmin.DatabaseAdminClient, database string) (*bundleSnapshot, error) {
	target := output.SpannerDatabase(database)
	output.RefreshStart(target)
	bundles := &bundleSnapshot{Types: map[string]struct{}{}}
	getDatabaseDdlRes, err := client.GetDatabaseDdl(ctx, &spannerPb.GetDatabaseDdlRequest{
		Database: database,
	})
	if err != nil {
		return nil, fmt.Errorf("spanner.GetDatabaseDdl: %w", err)
	}
	bundles.Descriptors = getDatabaseDdlRes.GetProtoDescriptors()
	for _, ddl := range getDatabaseDdlRes.GetStatements() {
		if strings.HasPrefix(ddl, "CREATE PROTO BUNDLE") {
			commaSepTypes := strings.TrimSpace(strings.TrimPrefix(ddl, "CREATE PROTO BUNDLE"))
			commaSepTypes = strings.TrimSuffix(strings.TrimPrefix(commaSepTypes, "("), ")")
			for t := range strings.SplitSeq(commaSepTypes, ",") {
				t = strings.TrimSpace(t)
				t = strings.Trim(t, "`")
				if t == "" || t == ")" {
					continue
				}
				bundles.Types[t] = struct{}{}
			}
		}
	}
	output.RefreshComplete(target, len(bundles.Types))
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
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	plan := &Plan{
		Client:     client,
		Database:   database,
		FdsBytes:   fdsBytes,
		desired:    fdsTypes,
		packageIDs: packageIDs,
	}
	if err := plan.replan(bundles); err != nil {
		plan.Close()
		return nil, err
	}
	return plan, nil
}

type Plan struct {
	Client   *spannerAdmin.DatabaseAdminClient
	Database string
	FdsBytes []byte
	// ExistingTypes counts the types already in the database's proto bundle.
	ExistingTypes int
	// Destroy marks a plan whose desired state is empty: every existing type
	// in the requested packages is removed and no descriptors are uploaded.
	Destroy bool
	*diff.Diff
	// desired and packageIDs are the inputs the Diff was computed from, kept
	// so the plan can be recomputed against a fresh bundle snapshot when the
	// database changed between the read and the DDL (see Deploy).
	desired    map[string]struct{}
	packageIDs []string
	// snapshot and apply are the live Spanner calls, replaced in tests.
	snapshot func(ctx context.Context) (*bundleSnapshot, error)
	apply    func(ctx context.Context, statement string, start time.Time) (*LroInfo, error)
}

// replan recomputes the Diff against a bundle snapshot.
func (p *Plan) replan(bundles *bundleSnapshot) error {
	p.ExistingTypes = len(bundles.Types)
	p.Diff = diff.New(bundles.Types, p.desired, p.packageIDs)
	if p.Destroy {
		return nil
	}
	return removeUnchangedUpdates(p.Diff, bundles.Descriptors, p.FdsBytes)
}

// Refresh re-reads the database's proto bundle and recomputes the plan
// against it.
func (p *Plan) Refresh(ctx context.Context) error {
	snapshot := p.snapshot
	if snapshot == nil {
		snapshot = func(ctx context.Context) (*bundleSnapshot, error) {
			return retryUnauthenticated(
				func() (*bundleSnapshot, error) {
					return viewProtobundles(ctx, p.Client, p.Database)
				},
				func() error {
					return refreshSpannerAdmin(ctx, &p.Client)
				},
			)
		}
	}
	bundles, err := snapshot(ctx)
	if err != nil {
		return fmt.Errorf("viewing proto bundles: %w", err)
	}
	return p.replan(bundles)
}

// maxReplans bounds how many times Deploy re-reads the bundle and retries
// after Spanner rejected the DDL because the bundle changed under the plan.
const maxReplans = 3

// staleBundle reports whether Spanner rejected the DDL because the bundle's
// membership differs from the snapshot the plan was computed from: a type the
// plan inserts already exists, or one it updates or deletes is gone. That
// happens when several deploys share a database and run at the same time —
// each reads the bundle before the others' DDL lands, so all of them plan an
// INSERT for a shared imported type and only the first succeeds.
func staleBundle(err error) bool {
	switch status.Code(err) {
	case codes.AlreadyExists, codes.NotFound:
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "type already exists")
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
	switch {
	case p.Destroy:
		return output.ActionDelete
	case p.ExistingTypes == 0:
		return output.ActionCreate
	default:
		return output.ActionUpdate
	}
}

// Deploy submits the proto bundle DDL and waits for the long-running
// operation, emitting progress from its metadata. The returned LroInfo is nil
// only when the DDL submission itself failed.
//
// When Spanner rejects the DDL because the bundle changed since it was read
// (see staleBundle), the bundle is re-read, the plan recomputed and the DDL
// submitted again, up to maxReplans times. A type another deploy inserted in
// the meantime then plans as an update or no-op instead of a second insert.
func (p *Plan) Deploy(ctx context.Context) (*LroInfo, error) {
	target := output.SpannerDatabase(p.Database)
	for replans := 0; ; replans++ {
		statement := p.Statement()
		start := time.Now()
		if statement == "" {
			output.ApplyStart(p.action(), target, "No changes")
			output.ApplyComplete(p.action(), target, "No changes", time.Since(start))
			return &LroInfo{
				Name:             "operations/none",
				Done:             true,
				Throttled:        false,
				CommitTimestamps: []string{},
			}, nil
		}
		output.ApplyStart(p.action(), target, statement)
		apply := p.apply
		if apply == nil {
			apply = p.applyDdl
		}
		info, err := apply(ctx, statement, start)
		if err == nil {
			output.ApplyComplete(p.action(), target, info.Name, time.Since(start))
			return info, nil
		}
		if replans >= maxReplans || !staleBundle(err) {
			if info != nil {
				output.ApplyErrored(p.action(), target, err, time.Since(start))
			}
			return info, err
		}
		output.Diagnostic("warning", "proto bundle changed since it was read; re-planning",
			fmt.Sprintf("%s (attempt %d of %d)", status.Convert(err).Message(), replans+1, maxReplans))
		if refreshErr := p.Refresh(ctx); refreshErr != nil {
			if info != nil {
				output.ApplyErrored(p.action(), target, err, time.Since(start))
			}
			return info, fmt.Errorf("re-planning after %v: %w", err, refreshErr)
		}
		p.Emit(output.Get(), output.SpannerType(p.Database), "deploy", &diff.PrintOptions{NoUpdates: p.Destroy})
	}
}

// applyDdl submits one DDL statement and waits for its long-running
// operation. The returned LroInfo is nil when the submission itself failed;
// errors carry the gRPC status of the rejected DDL so callers can classify
// them.
func (p *Plan) applyDdl(ctx context.Context, statement string, start time.Time) (*LroInfo, error) {
	target := output.SpannerDatabase(p.Database)
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
		return info, fmt.Errorf("waiting for Spanner Database DDL update to complete: %w", err)
	}
	info.Done = true
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
					output.ApplyProgress(p.action(), target, op.Name(), pct, md.GetThrottled(), time.Since(start))
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

// Statement renders the DDL for the plan, or "" when there is nothing to do.
func (p *Plan) Statement() string {
	if p.Destroy {
		if len(p.Delete) == 0 {
			return ""
		}
		return fmt.Sprintf("ALTER PROTO BUNDLE DELETE(`%s`)", strings.Join(p.Delete, "`,`"))
	}
	if p.ExistingTypes == 0 {
		if len(p.Create) == 0 {
			return ""
		}
		return fmt.Sprintf("CREATE PROTO BUNDLE (`%s`)", strings.Join(p.Create, "`,`"))
	}
	if len(p.Create)+len(p.Update)+len(p.Delete) == 0 {
		return ""
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

func destroyArgValidation(cmd *cobra.Command, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("expecting '{projectID}/{spannerInstance}/{database}' {packageID} (optional additional package IDs)")
	}
	if len(strings.Split(args[0], "/")) != 3 {
		return fmt.Errorf("expecting '{projectID}/{spannerInstance}/{database}' as the first argument")
	}
	return nil
}

// NewDestroyPlan plans the removal of every existing proto bundle type owned
// by the requested packages. There is no fds file: the desired state is empty,
// so the diff reduces to deletes for the requested packages and ignores for
// everything else.
func NewDestroyPlan(cmd *cobra.Command, args []string) (*Plan, error) {
	dbParts := strings.Split(args[0], "/")
	database := fmt.Sprintf("projects/%s/instances/%s/databases/%s", dbParts[0], dbParts[1], dbParts[2])
	client, bundles, err := clientAndProtobundles(cmd.Context(), database)
	if err != nil {
		return nil, fmt.Errorf("viewing proto bundles: %w", err)
	}
	plan := &Plan{
		Client:     client,
		Database:   database,
		Destroy:    true,
		packageIDs: args[1:],
	}
	if err := plan.replan(bundles); err != nil {
		plan.Close()
		return nil, err
	}
	return plan, nil
}

func planDestroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plandestroy",
		Short: "Preview the protobundle types a destroy would remove",
		Args:  destroyArgValidation,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := NewDestroyPlan(cmd, args)
			if err != nil {
				return err
			}
			defer plan.Close()
			plan.Emit(output.Get(), output.SpannerType(plan.Database), "plandestroy", &diff.PrintOptions{PrintIgnored: true, NoUpdates: true})
			output.Result(plan.result("plandestroy"))
			return nil
		},
	}
	return cmd
}

func destroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Remove a package's protobundle types from a Spanner database",
		Args:  destroyArgValidation,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := NewDestroyPlan(cmd, args)
			if err != nil {
				return err
			}
			defer plan.Close()
			plan.Emit(output.Get(), output.SpannerType(plan.Database), "destroy", &diff.PrintOptions{NoUpdates: true})
			if len(plan.Delete) == 0 {
				output.Log("No proto bundle types to destroy")
				output.Result(deployResult{planResult: plan.result("destroy"), Success: true})
				return nil
			}
			lro, err := plan.Deploy(cmd.Context())
			if lro == nil {
				return err
			}
			output.Result(deployResult{
				planResult: plan.result("destroy"),
				Lro:        lro,
				Success:    err == nil,
			})
			return err
		},
	}
	return cmd
}
