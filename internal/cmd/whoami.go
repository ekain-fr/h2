package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the resolved actor identity",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(resolveActor())
		},
	}
}
