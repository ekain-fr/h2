package cmd

import (
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"h2/internal/config"
	"h2/internal/session"
)

func newRunCmd() *cobra.Command {
	var name string
	var detach bool
	var roleName string
	var agentType string
	var command string
	var pod string
	var overrides []string

	cmd := &cobra.Command{
		Use:   "run [flags]",
		Short: "Start a new agent",
		Long: `Start a new agent, optionally configured from a role.

By default, uses the "default" role from ~/.h2/roles/default.yaml.

  h2 run                        Use the default role
  h2 run --role concierge       Use a specific role
  h2 run --agent-type claude    Run an agent type without a role
  h2 run --command "vim"        Run an explicit command`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate pod name if provided.
			if pod != "" {
				if err := config.ValidatePodName(pod); err != nil {
					return err
				}
			}

			var cmdCommand string
			var cmdArgs []string
			var heartbeat session.DaemonHeartbeat

			// Check mutual exclusivity of mode flags.
			modeFlags := 0
			if cmd.Flags().Changed("role") {
				modeFlags++
			}
			if cmd.Flags().Changed("agent-type") {
				modeFlags++
			}
			if cmd.Flags().Changed("command") {
				modeFlags++
			}
			if modeFlags > 1 {
				return fmt.Errorf("--role, --agent-type, and --command are mutually exclusive")
			}

			if cmd.Flags().Changed("agent-type") {
				// Run agent type without a role.
				cmdCommand = agentType
			} else if cmd.Flags().Changed("command") {
				// Run explicit command without a role.
				cmdCommand = command
				cmdArgs = args
			} else {
				// Use a role (specified or default).
				if roleName == "" {
					roleName = "default"
				}
				// When --pod is specified, check pod roles first then global.
				var role *config.Role
				var err error
				if pod != "" {
					role, err = config.LoadPodRole(roleName)
				} else {
					role, err = config.LoadRole(roleName)
				}
				if err != nil {
					if roleName == "concierge" {
						return fmt.Errorf("concierge role not found; create one with: h2 role init concierge")
					}
					if roleName == "default" {
						return fmt.Errorf("no default role found; create one with 'h2 role init default' or specify --role, --agent-type, or --command")
					}
					return fmt.Errorf("load role %q: %w", roleName, err)
				}
				if len(overrides) > 0 {
					if err := config.ApplyOverrides(role, overrides); err != nil {
						return fmt.Errorf("apply overrides: %w", err)
					}
				}
				return setupAndForkAgent(name, role, detach, pod, overrides)
			}

			// Agent-type or command mode: fork without a role.
			if name == "" {
				name = session.GenerateName()
			}

			sessionID := uuid.New().String()

			// Fork a daemon process.
			if err := session.ForkDaemon(session.ForkDaemonOpts{
				Name:      name,
				SessionID: sessionID,
				Command:   cmdCommand,
				Args:      cmdArgs,
				Heartbeat: heartbeat,
				Pod:       pod,
			}); err != nil {
				return err
			}

			if detach {
				fmt.Fprintf(os.Stderr, "Agent %q started (detached). Use 'h2 attach %s' to connect.\n", name, name)
				return nil
			}

			fmt.Fprintf(os.Stderr, "Agent %q started. Attaching...\n", name)
			return doAttach(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Agent name (auto-generated if omitted)")
	cmd.Flags().BoolVar(&detach, "detach", false, "Don't auto-attach after starting")
	cmd.Flags().StringVar(&roleName, "role", "", "Role to use (defaults to 'default')")
	cmd.Flags().StringVar(&agentType, "agent-type", "", "Agent type to run without a role (e.g. claude)")
	cmd.Flags().StringVar(&command, "command", "", "Explicit command to run without a role")
	cmd.Flags().StringVar(&pod, "pod", "", "Pod name for the agent (sets H2_POD env var)")
	cmd.Flags().StringArrayVar(&overrides, "override", nil, "Override role field (key=value, e.g. worktree.enabled=true)")

	return cmd
}
