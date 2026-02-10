package cmd

import (
	"fmt"
	"os"

	"github.com/google/uuid"

	"h2/internal/config"
	"h2/internal/session"
)

// setupAndForkAgent loads a role, sets up the agent session, forks the daemon,
// and optionally attaches to it. This is shared by both 'h2 run' and 'h2 bridge'.
func setupAndForkAgent(name, roleName string, detach bool) error {
	role, err := config.LoadRole(roleName)
	if err != nil {
		if roleName == "concierge" {
			return fmt.Errorf("concierge role not found; create one with: h2 role init concierge")
		}
		if roleName == "default" {
			return fmt.Errorf("no default role found; create one with 'h2 role init default' or specify --role, --agent-type, or --command")
		}
		return fmt.Errorf("load role %q: %w", roleName, err)
	}

	if name == "" {
		name = session.GenerateName()
	}

	sessionDir, err := config.SetupSessionDir(name, role)
	if err != nil {
		return fmt.Errorf("setup session dir: %w", err)
	}

	claudeConfigDir := role.GetClaudeConfigDir()
	if claudeConfigDir != "" {
		if err := config.EnsureClaudeConfigDir(claudeConfigDir); err != nil {
			return fmt.Errorf("ensure claude config dir: %w", err)
		}
	}

	cmdCommand := role.GetAgentType()
	var heartbeat session.DaemonHeartbeat
	if role.Heartbeat != nil {
		d, err := role.Heartbeat.ParseIdleTimeout()
		if err != nil {
			return fmt.Errorf("invalid heartbeat idle_timeout: %w", err)
		}
		heartbeat = session.DaemonHeartbeat{
			IdleTimeout: d,
			Message:     role.Heartbeat.Message,
			Condition:   role.Heartbeat.Condition,
		}
	}

	sessionID := uuid.New().String()

	// Fork the daemon.
	if err := session.ForkDaemon(name, sessionID, cmdCommand, nil, roleName, sessionDir, claudeConfigDir, heartbeat); err != nil {
		return err
	}

	if detach {
		fmt.Fprintf(os.Stderr, "Agent %q started (detached). Use 'h2 attach %s' to connect.\n", name, name)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Agent %q started. Attaching...\n", name)
	return doAttach(name)
}
