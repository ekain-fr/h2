package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"h2/internal/session"
)

func newDaemonCmd() *cobra.Command {
	var name string
	var sessionID string
	var roleName string
	var sessionDir string
	var claudeConfigDir string
	var heartbeatIdleTimeout string
	var heartbeatMessage string
	var heartbeatCondition string

	cmd := &cobra.Command{
		Use:    "_daemon --name=<name> -- <command> [args...]",
		Short:  "Run as a daemon (internal)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			var heartbeat session.DaemonHeartbeat
			if heartbeatIdleTimeout != "" {
				d, err := time.ParseDuration(heartbeatIdleTimeout)
				if err != nil {
					return fmt.Errorf("invalid --heartbeat-idle-timeout: %w", err)
				}
				heartbeat = session.DaemonHeartbeat{
					IdleTimeout: d,
					Message:     heartbeatMessage,
					Condition:   heartbeatCondition,
				}
			}

			err := session.RunDaemon(session.RunDaemonOpts{
				Name:            name,
				SessionID:       sessionID,
				Command:         args[0],
				Args:            args[1:],
				RoleName:        roleName,
				SessionDir:      sessionDir,
				ClaudeConfigDir: claudeConfigDir,
				Heartbeat:       heartbeat,
			})
			if err != nil {
				if _, ok := err.(*exec.ExitError); ok {
					os.Exit(1)
				}
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Agent name")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "Claude Code session ID")
	cmd.Flags().StringVar(&roleName, "role", "", "Role name")
	cmd.Flags().StringVar(&sessionDir, "session-dir", "", "Session directory path")
	cmd.Flags().StringVar(&claudeConfigDir, "claude-config-dir", "", "Claude config directory")
	cmd.Flags().StringVar(&heartbeatIdleTimeout, "heartbeat-idle-timeout", "", "Heartbeat idle timeout duration")
	cmd.Flags().StringVar(&heartbeatMessage, "heartbeat-message", "", "Heartbeat nudge message")
	cmd.Flags().StringVar(&heartbeatCondition, "heartbeat-condition", "", "Heartbeat condition command")

	return cmd
}
