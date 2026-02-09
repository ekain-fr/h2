package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SessionsDir returns the directory where agent session dirs are created (~/.h2/sessions/).
func SessionsDir() string {
	return filepath.Join(ConfigDir(), "sessions")
}

// SessionDir returns the session directory for a given agent name.
func SessionDir(agentName string) string {
	return filepath.Join(SessionsDir(), agentName)
}

// SetupSessionDir creates the session directory for an agent and writes
// per-agent files (e.g. permission-reviewer.md). Claude Code config
// (auth, hooks, settings) lives in the shared claude config dir, not here.
func SetupSessionDir(agentName string, role *Role) (string, error) {
	sessionDir := SessionDir(agentName)

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}

	// Write permission-reviewer.md if permissions.agent is configured.
	if role.Permissions.Agent != nil && role.Permissions.Agent.IsEnabled() {
		reviewerPath := filepath.Join(sessionDir, "permission-reviewer.md")
		if err := os.WriteFile(reviewerPath, []byte(role.Permissions.Agent.Instructions), 0o644); err != nil {
			return "", fmt.Errorf("write permission-reviewer.md: %w", err)
		}
	}

	return sessionDir, nil
}

// EnsureClaudeConfigDir creates the shared Claude config directory and writes
// the h2 standard settings.json (hooks + permissions) if it doesn't exist yet.
func EnsureClaudeConfigDir(configDir string) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	// Write settings.json with h2 hooks if it doesn't exist.
	settingsPath := filepath.Join(configDir, "settings.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		settings := buildH2Settings()
		settingsJSON, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal settings.json: %w", err)
		}
		if err := os.WriteFile(settingsPath, settingsJSON, 0o644); err != nil {
			return fmt.Errorf("write settings.json: %w", err)
		}
	}

	return nil
}

// hookEntry represents a single hook in the settings.json hooks array.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// hookMatcher represents a matcher + hooks pair in settings.json.
type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// buildH2Settings constructs the settings.json content with h2 standard hooks.
func buildH2Settings() map[string]any {
	settings := make(map[string]any)
	settings["hooks"] = buildH2Hooks()
	return settings
}

// buildH2Hooks creates the hooks section with h2 standard hooks.
func buildH2Hooks() map[string][]hookMatcher {
	collectHook := hookEntry{
		Type:    "command",
		Command: "h2 hook collect",
		Timeout: 5,
	}

	permissionHook := hookEntry{
		Type:    "command",
		Command: "h2 permission-request",
		Timeout: 60,
	}

	// Standard hook events that get the collect hook.
	standardEvents := []string{
		"PreToolUse",
		"PostToolUse",
		"SessionStart",
		"Stop",
		"UserPromptSubmit",
	}

	hooks := make(map[string][]hookMatcher)

	for _, event := range standardEvents {
		hooks[event] = []hookMatcher{{
			Matcher: "",
			Hooks:   []hookEntry{collectHook},
		}}
	}

	// PermissionRequest gets the permission handler + collect hook.
	hooks["PermissionRequest"] = []hookMatcher{{
		Matcher: "",
		Hooks:   []hookEntry{permissionHook, collectHook},
	}}

	return hooks
}

