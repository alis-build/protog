// Package pubsub contains the 'pubsub' command with all its sub commands
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/alis-build/protog/diff"
	"github.com/alis-build/protog/fds"
	"github.com/alis-build/protog/output"
	"github.com/spf13/cobra"
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

type viewResult struct {
	Project string   `json:"project"`
	Topics  []string `json:"topics"`
}

func (r viewResult) Lines() []string { return r.Topics }

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
		RunE: func(cmd *cobra.Command, args []string) error {
			client, eventTopics, err := clientAndTopics(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("view pubsub topics for %s: %w", args[0], err)
			}
			defer client.Close()
			output.Result(viewResult{Project: args[0], Topics: slices.Sorted(maps.Keys(eventTopics))})
			return nil
		},
	}
	return cmd
}

func viewTopics(ctx context.Context, client *pubsub.Client) (map[string]struct{}, error) {
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
	target := output.PubsubProject(projectID)
	attempt := 0
	view, err := retryUnauthenticated(func() (*topicsView, error) {
		attempt++
		if attempt > 1 {
			output.AuthRetry("cached credential rejected", attempt)
		}
		client, err := pubsub.NewClient(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("instantiate pubsub client: %w", err)
		}
		output.RefreshStart(target)
		topics, err := viewTopics(ctx, client)
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		output.RefreshComplete(target, len(topics))
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

type planResult struct {
	Operation string          `json:"operation"`
	Target    output.Resource `json:"target"`
	Create    []string        `json:"create"`
	Delete    []string        `json:"delete"`
	Ignore    []string        `json:"ignore"`
	Changes   output.Summary  `json:"changes"`
}

type deployResult struct {
	planResult
	Results []TopicResult `json:"results"`
	Success bool          `json:"success"`
}

// TopicResult reports the outcome of one topic create or delete.
type TopicResult struct {
	Name    string        `json:"name"`
	Action  output.Action `json:"action"`
	Success bool          `json:"success"`
	Error   string        `json:"error,omitempty"`
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview topic changes without deploying them",
		Args:  planOrDeployArgValidation,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := NewPlan(cmd.Context(), args)
			if err != nil {
				return err
			}
			defer plan.Client.Close()
			plan.Emit(output.Get(), output.PubsubTopic(plan.Project), "plan", &diff.PrintOptions{PrintIgnored: true, NoUpdates: true})
			output.Result(plan.result("plan"))
			return nil
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

func NewPlan(ctx context.Context, args []string) (*Plan, error) {
	client, eventTopics, err := clientAndTopics(ctx, args[0])
	if err != nil {
		return nil, fmt.Errorf("view pubsub topics for %s: %w", args[0], err)
	}
	plan := &Plan{Client: client, Project: args[0]}
	fdsEvents, _, err := fds.ParseEvents(args[1])
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	plan.Diff = diff.New(eventTopics, fdsEvents, packageIDs)
	return plan, nil
}

type Plan struct {
	Client  *pubsub.Client
	Project string
	*diff.Diff
}

func (p *Plan) result(operation string) planResult {
	s := p.Diff.Summary("")
	// Topic updates are never deployed; report them as unchanged.
	s.Change = 0
	return planResult{
		Operation: operation,
		Target:    output.PubsubProject(p.Project),
		Create:    p.Create,
		Delete:    p.Delete,
		Ignore:    p.Ignore,
		Changes:   s,
	}
}

// Deploy creates and deletes topics in batches, reporting each topic's
// outcome. It stops scheduling new batches once a batch reported an error.
func (p *Plan) Deploy(ctx context.Context) ([]TopicResult, error) {
	var (
		mu      sync.Mutex
		results = []TopicResult{}
		errs    []error
	)
	topicItem := output.PubsubTopic(p.Project)
	runTopic := func(action output.Action, name string, apply func() error) {
		start := time.Now()
		item := topicItem.Named(name)
		output.ApplyStart(action, item, "")
		err := apply()
		if err != nil {
			output.ApplyErrored(action, item, err, time.Since(start))
		} else {
			output.ApplyComplete(action, item, "", time.Since(start))
		}
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			verb := "creating"
			if action == output.ActionDelete {
				verb = "deleting"
			}
			errs = append(errs, fmt.Errorf("%s topic %s: %w", verb, name, err))
			results = append(results, TopicResult{Name: name, Action: action, Error: err.Error()})
			return
		}
		results = append(results, TopicResult{Name: name, Action: action, Success: true})
	}
	deployErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		if len(errs) == 0 {
			return nil
		}
		return fmt.Errorf("deploying topic changes: %w", errors.Join(errs...))
	}

	wg := sync.WaitGroup{}
	if len(p.Create) > 0 {
		output.Log("Creating topics...")
	}
	for i, topic := range p.Create {
		wg.Go(func() {
			runTopic(output.ActionCreate, topic, func() error {
				_, err := p.Client.CreateTopic(ctx, topic)
				return err
			})
		})
		if i%10 == 0 {
			wg.Wait()
			if err := deployErr(); err != nil {
				return results, err
			}
		}
	}
	if len(p.Delete) > 0 {
		output.Log("Deleting topics...")
	}
	for i, topic := range p.Delete {
		wg.Go(func() {
			runTopic(output.ActionDelete, topic, func() error {
				return p.Client.Topic(topic).Delete(ctx)
			})
		})
		if i%10 == 0 {
			wg.Wait()
			if err := deployErr(); err != nil {
				return results, err
			}
		}
	}
	wg.Wait()
	return results, deployErr()
}

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy topic changes to a Google Project",
		Args:  planOrDeployArgValidation,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := NewPlan(cmd.Context(), args)
			if err != nil {
				return err
			}
			defer plan.Client.Close()
			plan.Emit(output.Get(), output.PubsubTopic(plan.Project), "deploy", &diff.PrintOptions{PrintIgnored: false, NoUpdates: false})
			results, err := plan.Deploy(cmd.Context())
			output.Result(deployResult{
				planResult: plan.result("deploy"),
				Results:    results,
				Success:    err == nil,
			})
			return err
		},
	}
	return cmd
}
