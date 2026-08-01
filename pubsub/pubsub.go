// Package pubsub contains the 'pubsub' command with all its sub commands
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/pubsub"
	"github.com/alis-build/protog/diff"
	"github.com/alis-build/protog/fds"
	"github.com/spf13/cobra"
	"go.alis.build/alog"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
			client, eventTopics, err := clientAndTopics(cmd.Context(), args[0])
			if err != nil {
				alog.Fatalf(cmd.Context(), "view pubsub topics for %s: %v", args[0], err)
			}
			defer client.Close()
			for topic := range eventTopics {
				println(topic)
			}
		},
	}
	return cmd
}

func viewTopics(ctx context.Context, client *pubsub.Client) (map[string]struct{}, error) {
	println("Fetching current topics...")
	eventTopics := map[string]struct{}{}
	it := client.Topics(ctx)
	for {
		topic, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing pubsub topics: %w", err)
		}
		eventTopics[topic.ID()] = struct{}{}
	}
	return eventTopics, nil
}

type topicsView struct {
	client *pubsub.Client
	topics map[string]struct{}
}

func clientAndTopics(ctx context.Context, projectID string) (*pubsub.Client, map[string]struct{}, error) {
	view, err := retryUnauthenticated(func() (*topicsView, error) {
		client, err := pubsub.NewClient(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("instantiate pubsub client: %w", err)
		}
		topics, err := viewTopics(ctx, client)
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		return &topicsView{client: client, topics: topics}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return view.client, view.topics, nil
}

// retryUnauthenticated retries once so a rejected cached credential is
// discarded and the caller can obtain a fresh one.
func retryUnauthenticated[T any](fn func() (T, error)) (T, error) {
	result, err := fn()
	if status.Code(err) != codes.Unauthenticated {
		return result, err
	}
	return fn()
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview topic changes without deploying them",
		Args:  planOrDeployArgValidation,
		Run: func(cmd *cobra.Command, args []string) {
			plan := NewPlan(cmd.Context(), args)
			defer plan.Client.Close()
			plan.Print(&diff.PrintOptions{PrintIgnored: true, NoUpdates: true})
		},
	}
	return cmd
}

func planOrDeployArgValidation(cmd *cobra.Command, args []string) error {
	if len(args) < 2 {
		return errors.New("expecting {projectID} {fdsFilePath} (optional list of package IDs)")
	}
	return nil
}

func NewPlan(ctx context.Context, args []string) *Plan {
	client, eventTopics, err := clientAndTopics(ctx, args[0])
	if err != nil {
		alog.Fatalf(ctx, "view pubsub topics for %s: %v", args[0], err)
	}
	plan := &Plan{Client: client}
	fdsEvents, _ := fds.ParseEvents(args[1])
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	plan.Diff = diff.New(eventTopics, fdsEvents, packageIDs)
	return plan
}

type Plan struct {
	Client *pubsub.Client
	*diff.Diff
}

func (p *Plan) Deploy(ctx context.Context) {
	if len(p.Create) > 0 {
		println("Creating topics...")
	}
	hadError := atomic.Bool{}
	wg := sync.WaitGroup{}
	for i, topic := range p.Create {
		wg.Go(func() {
			_, err := p.Client.CreateTopic(ctx, topic)
			if err != nil {
				hadError.Store(true)
				alog.Errorf(ctx, "creating topic %s: %v", topic, err)
			}
		})
		if i%10 == 0 {
			wg.Wait()
			if hadError.Load() {
				alog.Fatalf(ctx, "deploying topic changes failed")
			}
		}
	}
	if len(p.Delete) > 0 {
		println("Deleting topics...")
	}
	for i, topic := range p.Delete {
		wg.Go(func() {
			err := p.Client.Topic(topic).Delete(ctx)
			if err != nil {
				hadError.Store(true)
				alog.Errorf(ctx, "deleting topic %s: %v", topic, err)
			}
		})
		if i%10 == 0 {
			wg.Wait()
			if hadError.Load() {
				alog.Fatalf(ctx, "deploying topic changes failed")
			}
		}
	}
	wg.Wait()
	if hadError.Load() {
		alog.Fatalf(ctx, "deploying topic changes failed")
	}
}

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy topic changes to a Google Project",
		Args:  planOrDeployArgValidation,
		Run: func(cmd *cobra.Command, args []string) {
			plan := NewPlan(cmd.Context(), args)
			defer plan.Client.Close()
			plan.Print(&diff.PrintOptions{PrintIgnored: false, NoUpdates: false})
			plan.Deploy(cmd.Context())
		},
	}
	return cmd
}
