// Package spanner contains the 'spanner' command with all its sub commands
package spanner

import (
	"context"
	"fmt"
	"strings"

	spannerAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	spannerPb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"

	"github.com/alis-build/protog/diff"
	"github.com/alis-build/protog/fds"
	"github.com/spf13/cobra"
	"go.alis.build/alog"
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
		Run: func(cmd *cobra.Command, args []string) {
			parts := strings.Split(args[0], "/")
			spannerAdmin := SpannerAdmin(cmd.Context())
			database := fmt.Sprintf("projects/%s/instances/%s/databases/%s", parts[0], parts[1], parts[2])
			bundles, err := viewProtobundles(cmd.Context(), spannerAdmin, database)
			if err != nil {
				alog.Fatalf(cmd.Context(), "viewing proto bundles: %v", err)
			}
			for bundle := range bundles {
				println(bundle)
			}
		},
	}
	return cmd
}

func SpannerAdmin(ctx context.Context) *spannerAdmin.DatabaseAdminClient {
	spannerAdmin, err := spannerAdmin.NewDatabaseAdminClient(ctx)
	if err != nil {
		alog.Fatalf(ctx, "spanner.NewDatabaseAdminClient: %s", err.Error())
	}
	return spannerAdmin
}

func viewProtobundles(ctx context.Context, client *spannerAdmin.DatabaseAdminClient, database string) (map[string]struct{}, error) {
	println("Fetching current proto bundles...")
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
	return bundles, nil
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview protobundle changes without deploying them",
		Args:  planOrDeployArgValidation,
		Run: func(cmd *cobra.Command, args []string) {
			plan := NewPlan(cmd, args)
			plan.Print(&diff.PrintOptions{PrintIgnored: true})
		},
	}
	return cmd
}

func planOrDeployArgValidation(cmd *cobra.Command, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("expecting '{projectID}/{spannerInstance}/{database}' {pathToFdsFile} (optional list of packages)")
	}
	return nil
}

func NewPlan(cmd *cobra.Command, args []string) *Plan {
	dbParts := strings.Split(args[0], "/")
	fdsFilePath := args[1]
	fdsTypes, fdsBytes := fds.ParseFdsTypes(fdsFilePath)
	plan := &Plan{
		Client:   SpannerAdmin(cmd.Context()),
		Database: fmt.Sprintf("projects/%s/instances/%s/databases/%s", dbParts[0], dbParts[1], dbParts[2]),
		FdsBytes: fdsBytes,
	}
	bundles, err := viewProtobundles(cmd.Context(), plan.Client, plan.Database)
	if err != nil {
		alog.Fatalf(cmd.Context(), "viewing proto bundles: %v", err)
	}
	plan.NoExistingTypes = len(bundles) == 0
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	plan.Diff = diff.New(bundles, fdsTypes, packageIDs)
	return plan
}

type Plan struct {
	Client          *spannerAdmin.DatabaseAdminClient
	Database        string
	FdsBytes        []byte
	NoExistingTypes bool
	*diff.Diff
}

func (p *Plan) Deploy(ctx context.Context) {
	println("Deploying proto bundles...")
	op, err := p.Client.UpdateDatabaseDdl(ctx, &spannerPb.UpdateDatabaseDdlRequest{
		Database:         p.Database,
		Statements:       []string{p.Statement()},
		ProtoDescriptors: p.FdsBytes,
	})
	if err != nil {
		alog.Fatalf(ctx, "updating Spanner Database DDL: %v", err)
	}
	err = op.Wait(ctx)
	if err != nil {
		alog.Fatalf(ctx, "waiting for Spanner Database DDL update to complete: %v", err)
	}
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
		Run: func(cmd *cobra.Command, args []string) {
			plan := NewPlan(cmd, args)
			plan.Print(&diff.PrintOptions{})
			plan.Deploy(cmd.Context())
		},
	}
	return cmd
}
