package cmd

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/spf13/cobra"

	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show agent status",
		Long:  "Query a single agent's status and print it as JSON.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			sockPath, err := socketdir.Find(name)
			if err != nil {
				return agentConnError(name, err)
			}

			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return agentConnError(name, err)
			}
			defer conn.Close()

			if err := message.SendRequest(conn, &message.Request{Type: "status"}); err != nil {
				return fmt.Errorf("send request: %w", err)
			}

			resp, err := message.ReadResponse(conn)
			if err != nil {
				return fmt.Errorf("read response: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("status failed: %s", resp.Error)
			}
			if resp.Agent == nil {
				return fmt.Errorf("no agent info in response")
			}

			out, err := json.MarshalIndent(resp.Agent, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			fmt.Println(string(out))
			return nil
		},
	}
}
