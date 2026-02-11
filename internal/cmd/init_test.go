package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
)

// expectedDirs returns the subdirectories that h2 init should create.
func expectedDirs() []string {
	return []string{
		"roles",
		"sessions",
		"sockets",
		filepath.Join("claude-config", "default"),
		"projects",
		"worktrees",
		filepath.Join("pods", "roles"),
		filepath.Join("pods", "templates"),
	}
}

func TestInitCmd_CreatesStructure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myh2")

	cmd := newInitCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	// Marker file should exist.
	if !config.IsH2Dir(dir) {
		t.Error("expected .h2-dir.txt marker to exist")
	}

	// config.yaml should exist.
	configPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("expected config.yaml to exist: %v", err)
	}

	// All expected directories should exist.
	for _, sub := range expectedDirs() {
		path := filepath.Join(dir, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", sub)
		}
	}

	// Output should mention the path.
	abs, _ := filepath.Abs(dir)
	if !strings.Contains(buf.String(), abs) {
		t.Errorf("output = %q, want it to contain %q", buf.String(), abs)
	}
}

func TestInitCmd_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()

	// Pre-create marker so it's already an h2 dir.
	if err := config.WriteMarker(dir); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}

	cmd := newInitCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{dir})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when h2 dir already exists")
	}
	if !strings.Contains(err.Error(), "already an h2 directory") {
		t.Errorf("error = %q, want it to contain 'already an h2 directory'", err.Error())
	}
}

func TestInitCmd_Global(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	cmd := newInitCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--global"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init --global failed: %v", err)
	}

	h2Dir := filepath.Join(fakeHome, ".h2")
	if !config.IsH2Dir(h2Dir) {
		t.Error("expected ~/.h2 to be an h2 directory")
	}

	// Verify subdirectories.
	for _, sub := range expectedDirs() {
		path := filepath.Join(h2Dir, sub)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
		}
	}
}

func TestInitCmd_DefaultCurrentDir(t *testing.T) {
	dir := t.TempDir()

	// Change to the temp dir so default "." resolves there.
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	cmd := newInitCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init (default dir) failed: %v", err)
	}

	// Resolve symlinks for macOS /var -> /private/var.
	resolved, _ := filepath.EvalSymlinks(dir)
	if !config.IsH2Dir(resolved) {
		t.Error("expected current dir to be an h2 directory")
	}
}

func TestInitCmd_CreatesParentDirs(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")

	cmd := newInitCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{nested})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init with nested path failed: %v", err)
	}

	if !config.IsH2Dir(nested) {
		t.Error("expected nested dir to be an h2 directory")
	}
}
