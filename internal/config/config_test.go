package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFrom_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	yaml := `users:
  dcosson:
    bridges:
      telegram:
        bot_token: "123456:ABC-DEF"
        chat_id: 789
      macos_notify:
        enabled: true
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	u, ok := cfg.Users["dcosson"]
	if !ok {
		t.Fatal("expected user dcosson")
	}

	if u.Bridges.Telegram == nil {
		t.Fatal("expected telegram config")
	}
	if u.Bridges.Telegram.BotToken != "123456:ABC-DEF" {
		t.Errorf("bot_token = %q, want %q", u.Bridges.Telegram.BotToken, "123456:ABC-DEF")
	}
	if u.Bridges.Telegram.ChatID != 789 {
		t.Errorf("chat_id = %d, want 789", u.Bridges.Telegram.ChatID)
	}

	if u.Bridges.MacOSNotify == nil {
		t.Fatal("expected macos_notify config")
	}
	if !u.Bridges.MacOSNotify.Enabled {
		t.Error("expected macos_notify.enabled = true")
	}
}

func TestLoadFrom_MissingFile(t *testing.T) {
	cfg, err := LoadFrom("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Users != nil {
		t.Errorf("expected nil Users, got %v", cfg.Users)
	}
}

func TestLoadFrom_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("{{invalid yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadFrom_NoBridges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	yaml := `users:
  alice: {}
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	u := cfg.Users["alice"]
	if u == nil {
		t.Fatal("expected user alice")
	}
	if u.Bridges.Telegram != nil {
		t.Error("expected nil telegram config")
	}
	if u.Bridges.MacOSNotify != nil {
		t.Error("expected nil macos_notify config")
	}
}
