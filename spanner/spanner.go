// Package spanner contains the 'spanner' command with all its sub commands
package spanner

import "github.com/spf13/cobra"

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
	}
	return cmd
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview protobundle changes without deploying them",
	}
	return cmd
}

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy protobundle changes to a Spanner database",
	}
	return cmd
}
