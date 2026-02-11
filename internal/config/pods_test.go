package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePodName(t *testing.T) {
	valid := []string{
		"backend",
		"my-pod",
		"pod-123",
		"a",
		"123",
		"a-b-c",
	}
	for _, name := range valid {
		if err := ValidatePodName(name); err != nil {
			t.Errorf("ValidatePodName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"My-Pod",
		"UPPER",
		"has space",
		"under_score",
		"has.dot",
		"has/slash",
		"café",
	}
	for _, name := range invalid {
		if err := ValidatePodName(name); err == nil {
			t.Errorf("ValidatePodName(%q) = nil, want error", name)
		}
	}
}

// setupTestH2Dir creates a temp h2 directory and sets H2_DIR + resets the resolve cache.
// Returns the h2 dir path.
func setupTestH2Dir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	WriteMarker(dir)
	os.MkdirAll(filepath.Join(dir, "roles"), 0o755)
	os.MkdirAll(filepath.Join(dir, "pods", "roles"), 0o755)
	t.Setenv("H2_DIR", dir)
	ResetResolveCache()
	t.Cleanup(func() { ResetResolveCache() })
	return dir
}

func writeRole(t *testing.T, dir, name string) {
	t.Helper()
	content := "name: " + name + "\ninstructions: |\n  Test role\n"
	os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0o644)
}

func TestLoadPodRole_PodRoleOverGlobal(t *testing.T) {
	h2Dir := setupTestH2Dir(t)
	// Create both a global and a pod role with same name but different descriptions.
	globalContent := "name: builder\ndescription: global\ninstructions: |\n  global\n"
	podContent := "name: builder\ndescription: pod-override\ninstructions: |\n  pod\n"
	os.WriteFile(filepath.Join(h2Dir, "roles", "builder.yaml"), []byte(globalContent), 0o644)
	os.WriteFile(filepath.Join(h2Dir, "pods", "roles", "builder.yaml"), []byte(podContent), 0o644)

	role, err := LoadPodRole("builder")
	if err != nil {
		t.Fatalf("LoadPodRole failed: %v", err)
	}
	if role.Description != "pod-override" {
		t.Errorf("expected pod-override description, got %q", role.Description)
	}
}

func TestLoadPodRole_FallbackToGlobal(t *testing.T) {
	h2Dir := setupTestH2Dir(t)
	// Only global role, no pod role.
	globalContent := "name: builder\ndescription: global-only\ninstructions: |\n  global\n"
	os.WriteFile(filepath.Join(h2Dir, "roles", "builder.yaml"), []byte(globalContent), 0o644)

	role, err := LoadPodRole("builder")
	if err != nil {
		t.Fatalf("LoadPodRole failed: %v", err)
	}
	if role.Description != "global-only" {
		t.Errorf("expected global-only description, got %q", role.Description)
	}
}

func TestLoadPodRole_NoRoleAnywhere(t *testing.T) {
	setupTestH2Dir(t)
	_, err := LoadPodRole("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent role, got nil")
	}
}

func TestListPodRoles_ReturnsOnlyPodRoles(t *testing.T) {
	h2Dir := setupTestH2Dir(t)
	// Create global and pod roles.
	writeRole(t, filepath.Join(h2Dir, "roles"), "global-role")
	writeRole(t, filepath.Join(h2Dir, "pods", "roles"), "pod-role-a")
	writeRole(t, filepath.Join(h2Dir, "pods", "roles"), "pod-role-b")

	podRoles, err := ListPodRoles()
	if err != nil {
		t.Fatalf("ListPodRoles failed: %v", err)
	}
	if len(podRoles) != 2 {
		t.Fatalf("expected 2 pod roles, got %d", len(podRoles))
	}

	// Should not include global role.
	for _, r := range podRoles {
		if r.Name == "global-role" {
			t.Error("ListPodRoles should not include global roles")
		}
	}
}

func TestListPodRoles_NoPodDir(t *testing.T) {
	// No pods/roles/ directory at all.
	dir := t.TempDir()
	WriteMarker(dir)
	t.Setenv("H2_DIR", dir)
	ResetResolveCache()
	t.Cleanup(func() { ResetResolveCache() })

	roles, err := ListPodRoles()
	if err != nil {
		t.Fatalf("ListPodRoles failed: %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("expected 0 pod roles, got %d", len(roles))
	}
}

func TestPodRolesDir(t *testing.T) {
	h2Dir := setupTestH2Dir(t)
	expected := filepath.Join(h2Dir, "pods", "roles")
	if got := PodRolesDir(); got != expected {
		t.Errorf("PodRolesDir() = %q, want %q", got, expected)
	}
}

func TestPodTemplatesDir(t *testing.T) {
	h2Dir := setupTestH2Dir(t)
	expected := filepath.Join(h2Dir, "pods", "templates")
	if got := PodTemplatesDir(); got != expected {
		t.Errorf("PodTemplatesDir() = %q, want %q", got, expected)
	}
}
