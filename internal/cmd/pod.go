package cmd

import (
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"

	"h2/internal/config"
	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func newPodCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pod",
		Short: "Manage agent pods",
	}

	cmd.AddCommand(newPodLaunchCmd())
	cmd.AddCommand(newPodStopCmd())
	cmd.AddCommand(newPodListCmd())
	return cmd
}

func newPodLaunchCmd() *cobra.Command {
	var podName string

	cmd := &cobra.Command{
		Use:   "launch <template>",
		Short: "Launch a pod from a template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateName := args[0]

			tmpl, err := config.LoadPodTemplate(templateName)
			if err != nil {
				return fmt.Errorf("load template %q: %w", templateName, err)
			}

			// Use --pod flag, or template's pod_name, or template file name.
			pod := podName
			if pod == "" {
				pod = tmpl.PodName
			}
			if pod == "" {
				pod = templateName
			}

			if err := config.ValidatePodName(pod); err != nil {
				return err
			}

			if len(tmpl.Agents) == 0 {
				return fmt.Errorf("template %q has no agents", templateName)
			}

			for _, agent := range tmpl.Agents {
				roleName := agent.Role
				if roleName == "" {
					roleName = "default"
				}

				// Load role using pod role resolution.
				role, err := config.LoadPodRole(roleName)
				if err != nil {
					return fmt.Errorf("load role %q for agent %q: %w", roleName, agent.Name, err)
				}

				if err := setupAndForkAgent(agent.Name, role, true, pod, nil); err != nil {
					return fmt.Errorf("start agent %q: %w", agent.Name, err)
				}
				fmt.Fprintf(os.Stderr, "Started agent %q in pod %q\n", agent.Name, pod)
			}

			fmt.Fprintf(os.Stderr, "Pod %q launched with %d agents\n", pod, len(tmpl.Agents))
			return nil
		},
	}

	cmd.Flags().StringVar(&podName, "pod", "", "Override pod name (default: template's pod_name or template name)")

	return cmd
}

func newPodStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <pod-name>",
		Short: "Stop all agents in a pod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			podName := args[0]

			entries, err := socketdir.ListByType(socketdir.TypeAgent)
			if err != nil {
				return err
			}

			stopped := 0
			for _, e := range entries {
				info := queryAgent(e.Path)
				if info == nil || info.Pod != podName {
					continue
				}

				conn, err := net.Dial("unix", e.Path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: cannot connect to %q: %v\n", e.Name, err)
					continue
				}

				if err := message.SendRequest(conn, &message.Request{Type: "stop"}); err != nil {
					conn.Close()
					fmt.Fprintf(os.Stderr, "Warning: cannot stop %q: %v\n", e.Name, err)
					continue
				}

				resp, err := message.ReadResponse(conn)
				conn.Close()
				if err != nil || !resp.OK {
					fmt.Fprintf(os.Stderr, "Warning: stop failed for %q\n", e.Name)
					continue
				}

				fmt.Printf("Stopped %s\n", e.Name)
				stopped++
			}

			if stopped == 0 {
				fmt.Printf("No agents found in pod %q\n", podName)
			} else {
				fmt.Printf("Stopped %d agents in pod %q\n", stopped, podName)
			}
			return nil
		},
	}
}

func newPodListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available pod templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			templates, err := config.ListPodTemplates()
			if err != nil {
				return err
			}

			if len(templates) == 0 {
				fmt.Printf("No pod templates found in %s\n", config.PodTemplatesDir())
				return nil
			}

			for _, t := range templates {
				name := t.PodName
				if name == "" {
					name = "(unnamed)"
				}
				fmt.Printf("%-20s %d agents\n", name, len(t.Agents))
			}
			return nil
		},
	}
}
