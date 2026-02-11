package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"h2/internal/config"

	"gopkg.in/yaml.v3"
)

func TestRoleTemplate_UsesResolvedClaudeConfigDir(t *testing.T) {
	// Role templates must set claude_config_dir to the dynamically resolved
	// default, not a hardcoded ~/.h2/ path.
	claudeConfigDir := config.DefaultClaudeConfigDir()

	for _, name := range []string{"default", "concierge", "custom"} {
		tmpl := roleTemplate(name, claudeConfigDir)

		var parsed map[string]interface{}
		if err := yaml.Unmarshal([]byte(tmpl), &parsed); err != nil {
			t.Fatalf("roleTemplate(%q): failed to parse YAML: %v", name, err)
		}

		got, ok := parsed["claude_config_dir"]
		if !ok {
			t.Errorf("roleTemplate(%q): claude_config_dir should be set", name)
			continue
		}
		if got != claudeConfigDir {
			t.Errorf("roleTemplate(%q): claude_config_dir = %q, want %q", name, got, claudeConfigDir)
		}
	}
}

// setupRoleTestH2Dir creates a temp h2 directory, sets H2_DIR to point at it,
// and resets the resolve cache so ConfigDir() picks it up.
func setupRoleTestH2Dir(t *testing.T) string {
	t.Helper()

	config.ResetResolveCache()
	t.Cleanup(config.ResetResolveCache)

	h2Dir := filepath.Join(t.TempDir(), "myh2")
	for _, sub := range []string{"roles", "sessions", "sockets", "claude-config/default"} {
		if err := os.MkdirAll(filepath.Join(h2Dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.WriteMarker(h2Dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("H2_DIR", h2Dir)

	return h2Dir
}

func TestRoleInitCmd_UsesCurrentH2Dir(t *testing.T) {
	h2Dir := setupRoleTestH2Dir(t)

	// Run "h2 role init default" via the cobra command.
	cmd := newRoleInitCmd()
	cmd.SetArgs([]string{"default"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("role init failed: %v", err)
	}

	// Read the generated file.
	path := filepath.Join(h2Dir, "roles", "default.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated role: %v", err)
	}

	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse generated role: %v", err)
	}

	// claude_config_dir must point to the current h2 dir, not ~/.h2/.
	want := filepath.Join(h2Dir, "claude-config", "default")
	got, ok := parsed["claude_config_dir"]
	if !ok {
		t.Fatal("generated role should set claude_config_dir")
	}
	if got != want {
		t.Errorf("claude_config_dir = %q, want %q", got, want)
	}
}

func TestRoleInitCmd_ConciergeUsesCurrentH2Dir(t *testing.T) {
	h2Dir := setupRoleTestH2Dir(t)

	cmd := newRoleInitCmd()
	cmd.SetArgs([]string{"concierge"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("role init concierge failed: %v", err)
	}

	path := filepath.Join(h2Dir, "roles", "concierge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated role: %v", err)
	}

	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse generated role: %v", err)
	}

	want := filepath.Join(h2Dir, "claude-config", "default")
	got, ok := parsed["claude_config_dir"]
	if !ok {
		t.Fatal("generated concierge role should set claude_config_dir")
	}
	if got != want {
		t.Errorf("claude_config_dir = %q, want %q", got, want)
	}
}

func TestRoleInitCmd_RefusesOverwrite(t *testing.T) {
	h2Dir := setupRoleTestH2Dir(t)

	// Create a role file first.
	rolePath := filepath.Join(h2Dir, "roles", "default.yaml")
	if err := os.WriteFile(rolePath, []byte("name: default\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRoleInitCmd()
	cmd.SetArgs([]string{"default"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when role already exists")
	}
}
