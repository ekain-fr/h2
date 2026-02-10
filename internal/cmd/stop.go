package cmd

import (
	"fmt"
	"net"

	"github.com/spf13/cobra"

	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a running agent or bridge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			sockPath, err := socketdir.Find(name)
			if err != nil {
				return fmt.Errorf("cannot find %q: %w", name, err)
			}

			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return fmt.Errorf("cannot connect to %q: %w", name, err)
			}
			defer conn.Close()

			if err := message.SendRequest(conn, &message.Request{Type: "stop"}); err != nil {
				return fmt.Errorf("send stop request: %w", err)
			}

			resp, err := message.ReadResponse(conn)
			if err != nil {
				return fmt.Errorf("read response: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("stop failed: %s", resp.Error)
			}

			fmt.Printf("Stopped %s.\n", name)
			return nil
		},
	}
}
