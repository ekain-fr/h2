package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/spf13/cobra"

	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Hook integration commands",
	}

	cmd.AddCommand(newHookCollectCmd())
	return cmd
}

func newHookCollectCmd() *cobra.Command {
	var agentName string

	cmd := &cobra.Command{
		Use:   "collect",
		Short: "Collect a hook event from stdin and report it to an agent",
		Long: `Reads a Claude Code hook JSON payload from stdin, extracts the event name,
and sends it to the specified agent's h2 session via Unix socket.

Designed to be used as a Claude Code hook command. Exits 0 with {} on stdout.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentName == "" {
				agentName = os.Getenv("H2_ACTOR")
			}
			if agentName == "" {
				return fmt.Errorf("--agent is required (or set H2_ACTOR)")
			}

			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}

			// Extract hook_event_name from the JSON payload.
			var envelope struct {
				HookEventName string `json:"hook_event_name"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				return fmt.Errorf("parse hook JSON: %w", err)
			}
			if envelope.HookEventName == "" {
				return fmt.Errorf("hook_event_name not found in payload")
			}

			sockPath, findErr := socketdir.Find(agentName)
			if findErr != nil {
				return agentConnError(agentName, findErr)
			}
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return agentConnError(agentName, err)
			}
			defer conn.Close()

			if err := message.SendRequest(conn, &message.Request{
				Type:      "hook_event",
				EventName: envelope.HookEventName,
				Payload:   json.RawMessage(data),
			}); err != nil {
				return fmt.Errorf("send hook event: %w", err)
			}

			resp, err := message.ReadResponse(conn)
			if err != nil {
				return fmt.Errorf("read response: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("hook event failed: %s", resp.Error)
			}

			// Claude Code expects JSON on stdout from hook commands.
			fmt.Fprintln(cmd.OutOrStdout(), "{}")
			return nil
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "Agent name to report to (defaults to $H2_ACTOR)")

	return cmd
}
