// Package pubsub contains the 'pubsub' command with all its sub commands
package pubsub

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/pubsub"
	"github.com/alis-build/protog/fds"
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
			client, err := pubsub.NewClient(cmd.Context(), args[0])
			if err != nil {
				alog.Fatalf(cmd.Context(), "instantiate pubsub client for %s: %v", args[0], err)
			}
			eventTopics := viewTopics(cmd.Context(), client)
			for topic := range eventTopics {
				println(topic)
			}
		},
	}
	return cmd
}

func viewTopics(ctx context.Context, client *pubsub.Client) map[string]struct{} {
	println("Fetching current topics...")
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
		eventTopics[topic.ID()] = struct{}{}
	}
	return eventTopics
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview topic changes without deploying them",
		Args:  planOrDeployArgValidation,
		Run: func(cmd *cobra.Command, args []string) {
			_ = plan(cmd.Context(), args, true)
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

func plan(ctx context.Context, args []string, printIgnored bool) *Plan {
	plan := &Plan{}
	var err error
	plan.Client, err = pubsub.NewClient(ctx, args[0])
	if err != nil {
		alog.Fatalf(ctx, "instantiate pubsub client for %s: %v", args[0], err)
	}
	eventTopics := viewTopics(ctx, plan.Client)
	fdsEvents, _ := fds.ParseEvents(args[1])
	for event := range fdsEvents {
		if _, ok := eventTopics[event]; !ok {
			plan.CreateTopics = append(plan.CreateTopics, event)
		}
	}
	var packageIDs []string
	if len(args) > 2 {
		packageIDs = args[2:]
	}
	ignores := []string{}
	for topic := range eventTopics {
		if !strings.HasSuffix(topic, "Event") {
			ignores = append(ignores, topic)
			continue
		}
		if _, ok := fdsEvents[topic]; !ok {
			var foundPackage bool
			for _, packageID := range packageIDs {
				if strings.HasPrefix(topic, packageID) {
					foundPackage = true
					break
				}
			}
			if !foundPackage {
				ignores = append(ignores, topic)
				continue
			}
			plan.DeleteTopics = append(plan.DeleteTopics, topic)
		}
	}
	if printIgnored {
		for _, topic := range ignores {
			println("\033[37mIGNORE " + topic + "\033[0m")
		}
	}
	for _, topic := range plan.CreateTopics {
		println("➕ CREATE " + topic)
	}
	for _, topic := range plan.DeleteTopics {
		println("🗑️ DELETE " + topic)
	}
	println()
	println("TOTAL TOPICS IGNORED: " + strconv.Itoa(len(ignores)))
	println("TOTAL TOPICS TO CREATE: " + strconv.Itoa(len(plan.CreateTopics)))
	println("TOTAL TOPICS TO DELETE: " + strconv.Itoa(len(plan.DeleteTopics)))
	return plan
}

type Plan struct {
	Client       *pubsub.Client
	CreateTopics []string
	DeleteTopics []string
}

func (p *Plan) Deploy(ctx context.Context) {
	if len(p.CreateTopics) > 0 {
		println("Creating topics...")
	}
	hadError := atomic.Bool{}
	wg := sync.WaitGroup{}
	for i, topic := range p.CreateTopics {
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
	if len(p.DeleteTopics) > 0 {
		println("Deleting topics...")
	}
	for i, topic := range p.DeleteTopics {
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
			plan := plan(cmd.Context(), args, false)
			plan.Deploy(cmd.Context())
		},
	}
	return cmd
}
