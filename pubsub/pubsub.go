// Package pubsub contains the 'pubsub' command with all its sub commands
package pubsub

import "github.com/spf13/cobra"

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pubsub",
		Short: "View, plan or deploy pubsub topics from `message {Topic}Event` messages",
	}
	cmd.AddCommand(viewCmd())
	cmd.AddCommand(planCmd())
	cmd.AddCommand(deployCmd())
	return cmd
}

func viewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view",
		Short: "View current pubsub topics in a Google Project",
	}
	return cmd
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview topic changes without deploying them",
	}
	return cmd
}

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy topic changes to a Google Project",
	}
	return cmd
}
