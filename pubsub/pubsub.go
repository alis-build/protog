// Package pubsub contains the 'pubsub' command with all its sub commands
package pubsub

import (
	"context"
	"errors"
	"strings"

	"cloud.google.com/go/pubsub"
	"github.com/spf13/cobra"
	"go.alis.build/alog"
	"google.golang.org/api/iterator"
)

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
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("expecting exactly one argument for the project ID")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			eventTopics := viewEventTopics(cmd.Context(), args[0])
			for topic := range eventTopics {
				println(topic)
			}
		},
	}
	return cmd
}

func viewEventTopics(ctx context.Context, projectID string) map[string]struct{} {
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		alog.Fatalf(ctx, "instantiate pubsub client for %s: %v", projectID, err)
	}
	eventTopics := map[string]struct{}{}
	it := client.Topics(ctx)
	for {
		topic, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			alog.Fatalf(ctx, "listing pubsub topics: %v", err)
		}
		// Only append a topic representing an event.
		if strings.HasSuffix(topic.ID(), "Event") {
			eventTopics[topic.ID()] = struct{}{}
		}
	}
	return eventTopics
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
