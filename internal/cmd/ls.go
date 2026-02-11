package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"h2/internal/config"
	"h2/internal/session/agent"
	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func newLsCmd() *cobra.Command {
	var podFlag string
	var allFlag bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List running agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if allFlag && cmd.Flags().Changed("pod") {
				return fmt.Errorf("--all and --pod are mutually exclusive")
			}

			if allFlag {
				return listAll()
			}

			entries, err := socketdir.List()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("No running agents.")
				return nil
			}

			// Collect bridge entries and query agent info.
			var bridges []socketdir.Entry
			var agentInfos []*message.AgentInfo
			var unresponsive []string
			for _, e := range entries {
				switch e.Type {
				case socketdir.TypeBridge:
					bridges = append(bridges, e)
				case socketdir.TypeAgent:
					info := queryAgent(e.Path)
					if info != nil {
						agentInfos = append(agentInfos, info)
					} else {
						unresponsive = append(unresponsive, e.Name)
					}
				}
			}

			// Determine effective pod filter.
			podFilter := podFlag
			if !cmd.Flags().Changed("pod") {
				podFilter = os.Getenv("H2_POD")
			}

			groups := groupAgentsByPod(agentInfos, podFilter)
			printPodGroups(groups, unresponsive)

			// Bridges are always shown.
			if len(bridges) > 0 {
				fmt.Printf("\n\033[1mBridges\033[0m\n")
				for _, e := range bridges {
					fmt.Printf("  \033[32m●\033[0m %s\n", e.Name)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&podFlag, "pod", "", "Filter by pod name, or '*' to show all grouped by pod")
	cmd.Flags().BoolVar(&allFlag, "all", false, "List agents from all discovered h2 directories")

	return cmd
}

// podGroup represents a group of agents with the same pod name.
type podGroup struct {
	Pod    string // empty string means "no pod"
	Agents []*message.AgentInfo
}

// groupAgentsByPod groups agents according to the pod filter logic.
//
// podFilter semantics:
//   - "*": show all agents, grouped by pod
//   - "<name>": show only agents in that pod
//   - "": show all agents, grouped by pod if any pods exist
func groupAgentsByPod(agents []*message.AgentInfo, podFilter string) []podGroup {
	if len(agents) == 0 {
		return nil
	}

	// Collect agents into pod buckets.
	podMap := make(map[string][]*message.AgentInfo) // pod name -> agents ("" for no pod)
	for _, a := range agents {
		podMap[a.Pod] = append(podMap[a.Pod], a)
	}

	// Filter by specific pod name.
	if podFilter != "" && podFilter != "*" {
		filtered := podMap[podFilter]
		if len(filtered) == 0 {
			return nil
		}
		return []podGroup{{Pod: podFilter, Agents: filtered}}
	}

	// Check if any agents have pod membership.
	hasPods := false
	for pod := range podMap {
		if pod != "" {
			hasPods = true
			break
		}
	}

	// If showing all and no pods exist, return a single flat group.
	if !hasPods && podFilter != "*" {
		return []podGroup{{Pod: "", Agents: agents}}
	}

	// Build sorted groups: named pods first (alphabetical), then no-pod.
	var podNames []string
	for pod := range podMap {
		if pod != "" {
			podNames = append(podNames, pod)
		}
	}
	sort.Strings(podNames)

	var groups []podGroup
	for _, pod := range podNames {
		groups = append(groups, podGroup{Pod: pod, Agents: podMap[pod]})
	}
	if noPod := podMap[""]; len(noPod) > 0 {
		groups = append(groups, podGroup{Pod: "", Agents: noPod})
	}

	return groups
}

// printPodGroups renders grouped agent output.
func printPodGroups(groups []podGroup, unresponsive []string) {
	if len(groups) == 0 && len(unresponsive) == 0 {
		fmt.Println("No matching agents.")
		return
	}

	hasPods := false
	for _, g := range groups {
		if g.Pod != "" {
			hasPods = true
			break
		}
	}

	for i, g := range groups {
		if i > 0 {
			fmt.Println()
		}

		// Print group header.
		if hasPods || len(groups) > 1 {
			if g.Pod != "" {
				fmt.Printf("\033[1mAgents (pod: %s)\033[0m\n", g.Pod)
			} else {
				fmt.Printf("\033[1mAgents (no pod)\033[0m\n")
			}
		} else {
			fmt.Printf("\033[1mAgents\033[0m\n")
		}

		for _, info := range g.Agents {
			printAgentLine(info)
		}
	}

	for _, name := range unresponsive {
		fmt.Printf("  \033[31m✗\033[0m %s \033[2m(not responding)\033[0m\n", name)
	}
}

func printAgentLine(info *message.AgentInfo) {
	// Pick symbol and color based on state.
	var symbol, stateColor string
	switch info.State {
	case "active":
		symbol = "\033[32m●\033[0m" // green dot
		stateColor = "\033[32m"     // green
	case "idle":
		symbol = "\033[33m○\033[0m" // yellow circle
		stateColor = "\033[33m"     // yellow
	case "exited":
		symbol = "\033[31m●\033[0m" // red dot
		stateColor = "\033[31m"     // red
	default:
		symbol = "\033[37m○\033[0m"
		stateColor = "\033[37m"
	}

	// State label with duration.
	var stateLabel string
	if info.State != "" {
		label := info.StateDisplayText
		stateLabel = fmt.Sprintf("%s%s %s\033[0m", stateColor, label, info.StateDuration)
	} else {
		stateLabel = fmt.Sprintf("\033[2mup %s\033[0m", info.Uptime)
	}

	// Queued suffix — only show if there are queued messages.
	queued := ""
	if info.QueuedCount > 0 {
		queued = fmt.Sprintf(", \033[36m%d queued\033[0m", info.QueuedCount)
	}

	// OTEL metrics — tokens and cost (only if data received).
	metrics := ""
	if info.TotalTokens > 0 || info.TotalCostUSD > 0 {
		parts := []string{}
		if info.TotalTokens > 0 {
			parts = append(parts, agent.FormatTokens(info.TotalTokens))
		}
		if info.TotalCostUSD > 0 {
			parts = append(parts, agent.FormatCost(info.TotalCostUSD))
		}
		metrics = fmt.Sprintf(", %s", strings.Join(parts, " "))
	}

	// Hook collector — current tool use or blocked state.
	tool := ""
	if info.BlockedOnPermission {
		blocked := "permission"
		if info.BlockedToolName != "" {
			blocked = fmt.Sprintf("permission: %s", info.BlockedToolName)
		}
		tool = fmt.Sprintf(" \033[31m(blocked %s)\033[0m", blocked) // red
	} else if info.LastToolUse != "" {
		tool = fmt.Sprintf(" \033[2m(%s)\033[0m", info.LastToolUse)
	}

	// Role label.
	role := ""
	if info.RoleName != "" {
		role = fmt.Sprintf(" \033[35m(%s)\033[0m", info.RoleName) // magenta
	}

	// Session ID suffix — show truncated ID if present.
	sid := ""
	if info.SessionID != "" {
		short := info.SessionID
		if len(short) > 8 {
			short = short[:8]
		}
		sid = fmt.Sprintf(" \033[2m[%s]\033[0m", short)
	}

	if info.State != "" {
		fmt.Printf("  %s %s%s \033[2m%s\033[0m — %s, up %s%s%s%s%s\n",
			symbol, info.Name, role, info.Command, stateLabel, info.Uptime, metrics, queued, sid, tool)
	} else {
		fmt.Printf("  %s %s%s \033[2m%s\033[0m — %s%s%s%s%s\n",
			symbol, info.Name, role, info.Command, stateLabel, metrics, queued, sid, tool)
	}
}

// newLsAlias returns a hidden "ls" command that delegates to "list".
func newLsAlias(listCmd *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:    "ls",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listCmd.RunE(listCmd, args)
		},
	}
}

// listAll discovers all h2 directories and lists agents from each.
func listAll() error {
	h2Dirs := config.ResolveDirAll()
	if len(h2Dirs) == 0 {
		fmt.Println("No h2 directories found.")
		return nil
	}

	for i, h2Dir := range h2Dirs {
		if i > 0 {
			fmt.Println()
		}

		fmt.Printf("\033[1m%s\033[0m\n", shortenHome(h2Dir))

		sockDir := socketdir.ResolveSocketDir(h2Dir)
		entries, err := socketdir.ListIn(sockDir)
		if err != nil {
			fmt.Printf("  \033[2m(error reading sockets: %v)\033[0m\n", err)
			continue
		}

		if len(entries) == 0 {
			fmt.Println("  No running agents.")
			continue
		}

		var bridges []socketdir.Entry
		var agentInfos []*message.AgentInfo
		var unresponsive []string
		for _, e := range entries {
			switch e.Type {
			case socketdir.TypeBridge:
				bridges = append(bridges, e)
			case socketdir.TypeAgent:
				info := queryAgent(e.Path)
				if info != nil {
					agentInfos = append(agentInfos, info)
				} else {
					unresponsive = append(unresponsive, e.Name)
				}
			}
		}

		groups := groupAgentsByPod(agentInfos, "*")
		if len(groups) > 0 || len(unresponsive) > 0 {
			printPodGroupsIndented(groups, unresponsive)
		}

		if len(bridges) > 0 {
			fmt.Printf("  \033[1mBridges\033[0m\n")
			for _, e := range bridges {
				fmt.Printf("    \033[32m●\033[0m %s\n", e.Name)
			}
		}
	}

	return nil
}

// printPodGroupsIndented renders grouped agent output with extra indent for --all mode.
func printPodGroupsIndented(groups []podGroup, unresponsive []string) {
	if len(groups) == 0 && len(unresponsive) == 0 {
		return
	}

	hasPods := false
	for _, g := range groups {
		if g.Pod != "" {
			hasPods = true
			break
		}
	}

	for i, g := range groups {
		if i > 0 {
			fmt.Println()
		}

		if hasPods || len(groups) > 1 {
			if g.Pod != "" {
				fmt.Printf("  \033[1mAgents (pod: %s)\033[0m\n", g.Pod)
			} else {
				fmt.Printf("  \033[1mAgents (no pod)\033[0m\n")
			}
		} else {
			fmt.Printf("  \033[1mAgents\033[0m\n")
		}

		for _, info := range g.Agents {
			fmt.Print("  ")
			printAgentLine(info)
		}
	}

	for _, name := range unresponsive {
		fmt.Printf("    \033[31m✗\033[0m %s \033[2m(not responding)\033[0m\n", name)
	}
}

// shortenHome replaces the home directory prefix with ~ in a path.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// agentConnError returns an error for a failed agent connection that includes
// the list of available agents.
func agentConnError(name string, err error) error {
	agents, listErr := socketdir.ListByType(socketdir.TypeAgent)
	if listErr != nil || len(agents) == 0 {
		return fmt.Errorf("cannot connect to agent %q (no running agents)\n\nStart one with: h2 run --name <name> <command>", name)
	}
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return fmt.Errorf("cannot connect to agent %q\n\nAvailable agents: %s", name, strings.Join(names, ", "))
}

// queryAgent connects to a socket path and queries agent status.
func queryAgent(sockPath string) *message.AgentInfo {
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return nil
	}
	defer conn.Close()

	if err := message.SendRequest(conn, &message.Request{Type: "status"}); err != nil {
		return nil
	}

	resp, err := message.ReadResponse(conn)
	if err != nil || !resp.OK {
		return nil
	}
	return resp.Agent
}
