package main

import (
	"context"
	"io"
	"os"

	"github.com/alis-build/protog/fds"
	"github.com/alis-build/protog/output"
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
	var jsonFlag bool
	cmd.PersistentFlags().BoolVar(&jsonFlag, "json", false,
		"emit NDJSON progress events on stderr and a single JSON result on stdout")
	cmd.PersistentPreRun = func(c *cobra.Command, _ []string) {
		// fang populates the root Version from build info before executing.
		output.Init(jsonFlag, c.Root().Version)
	}
	cmd.AddCommand(spanner.Command())
	cmd.AddCommand(pubsub.Command())
	cmd.AddCommand(fds.Command())
	err := fang.Execute(context.Background(), cmd,
		fang.WithErrorHandler(func(w io.Writer, styles fang.Styles, err error) {
			// Failures before PersistentPreRun (flag or arg validation) reach
			// here uninitialized; Init is a no-op once initialized. A parse
			// error on --json itself still renders human-style.
			if jsonFlag {
				output.Init(true, cmd.Version)
				output.Diagnostic("error", "command failed", err.Error())
				output.ResultError(err)
				return
			}
			fang.DefaultErrorHandler(w, styles, err)
		}))
	if err != nil {
		os.Exit(1)
	}
}
