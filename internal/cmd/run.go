package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"h2/internal/daemon"
)

func newRunCmd() *cobra.Command {
	var name string
	var detach bool

	cmd := &cobra.Command{
		Use:   "run --name=<name> [--detach] -- <command> [args...]",
		Short: "Start a new agent",
		Long:  "Fork a daemon process running the given command, then attach to it.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			// Fork a daemon process.
			if err := daemon.ForkDaemon(name, args[0], args[1:]); err != nil {
				return err
			}

			if detach {
				fmt.Fprintf(os.Stderr, "Agent %q started (detached). Use 'h2 attach %s' to connect.\n", name, name)
				return nil
			}

			// Auto-attach.
			return doAttach(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	cmd.Flags().BoolVar(&detach, "detach", false, "Don't auto-attach after starting")

	return cmd
}
