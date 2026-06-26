// Package spanner contains the 'spanner' command with all its sub commands
package spanner

import (
	"context"
	"fmt"
	"strings"

	spannerAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	spannerPb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"

	"github.com/alis-build/protog/fds"
	"github.com/spf13/cobra"
	"go.alis.build/alog"
)

var SpannerAdmin *spannerAdmin.DatabaseAdminClient

func init() {
	ctx := context.Background()
	var err error
	SpannerAdmin, err = spannerAdmin.NewDatabaseAdminClient(ctx)
	if err != nil {
		alog.Fatalf(ctx, "spanner.NewDatabaseAdminClient: %s", err.Error())
	}
}

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
			bundles, err := viewProtobundles(cmd.Context(), fmt.Sprintf("projects/%s/instances/%s/databases/%s", parts[0], parts[1], parts[2]))
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

func viewProtobundles(ctx context.Context, database string) (map[string]struct{}, error) {
	bundles := map[string]struct{}{}
	getDatabaseDdlRes, err := SpannerAdmin.GetDatabaseDdl(ctx, &spannerPb.GetDatabaseDdlRequest{
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
			_ = plan(cmd, args)
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

// Plan returns a spanner DDL statement and raw fds bytes to sync the desired types to the current types.
type Plan struct {
	Database  string
	Statement string
	FdsBytes  []byte
}

func plan(cmd *cobra.Command, args []string) *Plan {
	dbParts := strings.Split(args[0], "/")
	database := fmt.Sprintf("projects/%s/instances/%s/databases/%s", dbParts[0], dbParts[1], dbParts[2])
	bundles, err := viewProtobundles(cmd.Context(), database)
	if err != nil {
		alog.Fatalf(cmd.Context(), "viewing proto bundles: %v", err)
	}
	fdsFilePath := args[1]
	fdsTypes, fdsBytes := fds.ParseFdsTypes(fdsFilePath)
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	statement := buildProtobundleStatement(bundles, fdsTypes, packageIDs)
	println(statement)
	return &Plan{
		Database:  database,
		Statement: statement,
		FdsBytes:  fdsBytes,
	}
}

func buildProtobundleStatement(currentTypes map[string]struct{}, desiredTypes map[string]struct{}, packageIDs []string) string {
	return "TODO"
}

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy protobundle changes to a Spanner database",
		Args:  planOrDeployArgValidation,
		Run: func(cmd *cobra.Command, args []string) {
			plan := plan(cmd, args)
			op, err := SpannerAdmin.UpdateDatabaseDdl(cmd.Context(), &spannerPb.UpdateDatabaseDdlRequest{
				Database:         plan.Database,
				Statements:       []string{plan.Statement},
				ProtoDescriptors: plan.FdsBytes,
			})
			if err != nil {
				alog.Fatalf(cmd.Context(), "updating Spanner Database DDL: %v", err)
			}
			err = op.Wait(cmd.Context())
			if err != nil {
				alog.Fatalf(cmd.Context(), "waiting for Spanner Database DDL update to complete: %v", err)
			}
		},
	}
	return cmd
}
