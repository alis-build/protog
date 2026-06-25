package main

import (
	"context"
	"os"

	"github.com/alis-build/protog/fds"
	"github.com/alis-build/protog/pubsub"
	"github.com/alis-build/protog/spanner"
	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
)

func main() {
	cmd := &cobra.Command{
		Use:   "protog",
		Short: "Protocol buffers integrated with Google Cloud",
	}
	cmd.AddCommand(spanner.Command())
	cmd.AddCommand(pubsub.Command())
	cmd.AddCommand(fds.Command())
	if err := fang.Execute(context.Background(), cmd); err != nil {
		os.Exit(1)
	}
}
