package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/tmpl"

	"gopkg.in/yaml.v3"
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

// --- ExpandPodAgents tests ---

func TestExpandPodAgents_SingleAgentNoCount(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "concierge", Role: "concierge"},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	a := agents[0]
	if a.Name != "concierge" || a.Role != "concierge" || a.Index != 0 || a.Count != 0 {
		t.Errorf("got %+v, want Name=concierge Role=concierge Index=0 Count=0", a)
	}
}

func TestExpandPodAgents_CountGreaterThanOne(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "coder", Role: "coding", Count: 3},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
	for i, a := range agents {
		expectedName := []string{"coder-1", "coder-2", "coder-3"}[i]
		if a.Name != expectedName {
			t.Errorf("agent %d: name = %q, want %q", i, a.Name, expectedName)
		}
		if a.Index != i+1 {
			t.Errorf("agent %d: Index = %d, want %d", i, a.Index, i+1)
		}
		if a.Count != 3 {
			t.Errorf("agent %d: Count = %d, want 3", i, a.Count)
		}
		if a.Role != "coding" {
			t.Errorf("agent %d: Role = %q, want coding", i, a.Role)
		}
	}
}

func TestExpandPodAgents_CountWithIndexTemplate(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "coder-{{ .Index }}", Role: "coding", Count: 3},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
	for i, a := range agents {
		expected := []string{"coder-1", "coder-2", "coder-3"}[i]
		if a.Name != expected {
			t.Errorf("agent %d: name = %q, want %q", i, a.Name, expected)
		}
	}
}

func TestExpandPodAgents_CountOneWithIndexTemplate(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "worker-{{ .Index }}", Role: "worker", Count: 1},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	a := agents[0]
	if a.Name != "worker-1" {
		t.Errorf("name = %q, want worker-1", a.Name)
	}
	if a.Index != 1 || a.Count != 1 {
		t.Errorf("Index=%d Count=%d, want Index=1 Count=1", a.Index, a.Count)
	}
}

func TestExpandPodAgents_CountOneNoTemplate(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "worker", Role: "worker", Count: 1},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	a := agents[0]
	if a.Name != "worker" {
		t.Errorf("name = %q, want worker", a.Name)
	}
	if a.Index != 0 || a.Count != 0 {
		t.Errorf("Index=%d Count=%d, want Index=0 Count=0", a.Index, a.Count)
	}
}

func TestExpandPodAgents_VarsPassThrough(t *testing.T) {
	vars := map[string]string{"team": "backend", "project": "h2"}
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "coder", Role: "coding", Count: 2, Vars: vars},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, a := range agents {
		if a.Vars["team"] != "backend" || a.Vars["project"] != "h2" {
			t.Errorf("agent %d: vars = %v, want team=backend project=h2", i, a.Vars)
		}
	}
}

func TestExpandPodAgents_NameCollision(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "coder-2", Role: "coding"},
			{Name: "coder", Role: "coding", Count: 3},
		},
	}
	_, err := ExpandPodAgents(pt)
	if err == nil {
		t.Fatal("expected name collision error, got nil")
	}
	if !strings.Contains(err.Error(), "coder-2") {
		t.Errorf("error should mention colliding name 'coder-2': %v", err)
	}
}

func TestExpandPodAgents_MixedAgents(t *testing.T) {
	pt := &PodTemplate{
		PodName: "dev-team",
		Agents: []PodTemplateAgent{
			{Name: "concierge", Role: "concierge"},
			{Name: "coder-{{ .Index }}", Role: "coding", Count: 3},
			{Name: "reviewer", Role: "reviewer"},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 5 {
		t.Fatalf("expected 5 agents, got %d", len(agents))
	}

	expected := []struct {
		name  string
		role  string
		index int
		count int
	}{
		{"concierge", "concierge", 0, 0},
		{"coder-1", "coding", 1, 3},
		{"coder-2", "coding", 2, 3},
		{"coder-3", "coding", 3, 3},
		{"reviewer", "reviewer", 0, 0},
	}
	for i, e := range expected {
		a := agents[i]
		if a.Name != e.name || a.Role != e.role || a.Index != e.index || a.Count != e.count {
			t.Errorf("agent %d: got %+v, want name=%s role=%s index=%d count=%d",
				i, a, e.name, e.role, e.index, e.count)
		}
	}
}

func TestExpandPodAgents_DuplicateStaticNames(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "worker", Role: "coding"},
			{Name: "worker", Role: "reviewer"},
		},
	}
	_, err := ExpandPodAgents(pt)
	if err == nil {
		t.Fatal("expected name collision error, got nil")
	}
	if !strings.Contains(err.Error(), "worker") {
		t.Errorf("error should mention 'worker': %v", err)
	}
}

func TestExpandPodAgents_NegativeCount(t *testing.T) {
	pt := &PodTemplate{
		Agents: []PodTemplateAgent{
			{Name: "coder", Role: "coding", Count: -1},
		},
	}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Negative count treated as default (1 agent).
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
}

func TestExpandPodAgents_EmptyTemplate(t *testing.T) {
	pt := &PodTemplate{Agents: nil}
	agents, err := ExpandPodAgents(pt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

// --- PodTemplateAgent YAML parsing tests ---

func TestPodTemplateAgent_YAMLParsing(t *testing.T) {
	yamlText := `
pod_name: test
agents:
  - name: concierge
    role: concierge
  - name: coder
    role: coding
    count: 3
    vars:
      team: backend
      project: h2
`
	var pt PodTemplate
	if err := yaml.Unmarshal([]byte(yamlText), &pt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pt.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(pt.Agents))
	}

	// First agent: no count, no vars.
	a0 := pt.Agents[0]
	if a0.Count != 0 {
		t.Errorf("agent 0 Count = %d, want 0", a0.Count)
	}

	// Second agent: count=3, vars set.
	a1 := pt.Agents[1]
	if a1.Count != 3 {
		t.Errorf("agent 1 Count = %d, want 3", a1.Count)
	}
	if a1.Vars["team"] != "backend" {
		t.Errorf("agent 1 vars[team] = %q, want backend", a1.Vars["team"])
	}
	if a1.Vars["project"] != "h2" {
		t.Errorf("agent 1 vars[project] = %q, want h2", a1.Vars["project"])
	}
}

// --- ParsePodTemplateRendered tests ---

func TestParsePodTemplateRendered_Basic(t *testing.T) {
	yamlText := `pod_name: test
agents:
  - name: concierge
    role: concierge
  - name: coder
    role: coding
    count: 2
`
	ctx := &tmpl.Context{PodName: "test", H2Dir: "/tmp/h2"}
	pt, err := ParsePodTemplateRendered(yamlText, "test", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt.PodName != "test" {
		t.Errorf("PodName = %q, want test", pt.PodName)
	}
	if len(pt.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(pt.Agents))
	}
}

func TestParsePodTemplateRendered_WithVariables(t *testing.T) {
	yamlText := `variables:
  num_coders:
    default: "2"
  team:
    description: Team name

pod_name: backend
agents:
  - name: concierge
    role: concierge
  - name: coder
    role: coding
    count: {{ .Var.num_coders }}
    vars:
      team: {{ .Var.team }}
`
	ctx := &tmpl.Context{
		PodName: "backend",
		Var:     map[string]string{"team": "platform"},
	}
	pt, err := ParsePodTemplateRendered(yamlText, "backend", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pt.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(pt.Agents))
	}
	if pt.Agents[1].Count != 2 {
		t.Errorf("coder count = %d, want 2", pt.Agents[1].Count)
	}
	if pt.Agents[1].Vars["team"] != "platform" {
		t.Errorf("coder vars[team] = %q, want platform", pt.Agents[1].Vars["team"])
	}
}

func TestParsePodTemplateRendered_MissingRequiredVar(t *testing.T) {
	yamlText := `variables:
  team:
    description: Team name

pod_name: backend
agents:
  - name: coder
    role: coding
`
	ctx := &tmpl.Context{PodName: "backend", Var: map[string]string{}}
	_, err := ParsePodTemplateRendered(yamlText, "backend", ctx)
	if err == nil {
		t.Fatal("expected error for missing required var, got nil")
	}
	if !strings.Contains(err.Error(), "team") {
		t.Errorf("error should mention 'team': %v", err)
	}
}

func TestParsePodTemplateRendered_DefaultsApplied(t *testing.T) {
	yamlText := `variables:
  greeting:
    default: hello

pod_name: test
agents:
  - name: agent-{{ .Var.greeting }}
    role: greeter
`
	ctx := &tmpl.Context{PodName: "test"}
	pt, err := ParsePodTemplateRendered(yamlText, "test", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt.Agents[0].Name != "agent-hello" {
		t.Errorf("name = %q, want agent-hello", pt.Agents[0].Name)
	}
}

func TestParsePodTemplateRendered_CLIVarsOverrideDefaults(t *testing.T) {
	yamlText := `variables:
  greeting:
    default: hello

pod_name: test
agents:
  - name: agent-{{ .Var.greeting }}
    role: greeter
`
	ctx := &tmpl.Context{
		PodName: "test",
		Var:     map[string]string{"greeting": "hi"},
	}
	pt, err := ParsePodTemplateRendered(yamlText, "test", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt.Agents[0].Name != "agent-hi" {
		t.Errorf("name = %q, want agent-hi", pt.Agents[0].Name)
	}
}

func TestParsePodTemplateRendered_VariablesStoredOnStruct(t *testing.T) {
	yamlText := `variables:
  team:
    description: Team name
    default: backend

pod_name: test
agents:
  - name: worker
    role: coding
`
	ctx := &tmpl.Context{PodName: "test"}
	pt, err := ParsePodTemplateRendered(yamlText, "test", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt.Variables == nil {
		t.Fatal("Variables should not be nil")
	}
	def, ok := pt.Variables["team"]
	if !ok {
		t.Fatal("expected 'team' in Variables")
	}
	if def.Description != "Team name" {
		t.Errorf("team description = %q, want 'Team name'", def.Description)
	}
	if def.Default == nil || *def.Default != "backend" {
		t.Error("team default should be 'backend'")
	}
}

func TestParsePodTemplateRendered_NoVariablesSection(t *testing.T) {
	yamlText := `pod_name: simple
agents:
  - name: worker
    role: coding
`
	ctx := &tmpl.Context{PodName: "simple"}
	pt, err := ParsePodTemplateRendered(yamlText, "simple", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt.PodName != "simple" {
		t.Errorf("PodName = %q, want simple", pt.PodName)
	}
	if len(pt.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(pt.Agents))
	}
}

func TestLoadPodTemplateRendered_FromFile(t *testing.T) {
	h2Dir := setupTestH2Dir(t)
	tmplDir := filepath.Join(h2Dir, "pods", "templates")
	os.MkdirAll(tmplDir, 0o755)

	content := `pod_name: myteam
agents:
  - name: worker-{{ .Index }}
    role: coding
    count: 2
`
	os.WriteFile(filepath.Join(tmplDir, "myteam.yaml"), []byte(content), 0o644)

	ctx := &tmpl.Context{PodName: "myteam"}
	pt, err := LoadPodTemplateRendered("myteam", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt.PodName != "myteam" {
		t.Errorf("PodName = %q, want myteam", pt.PodName)
	}
	if len(pt.Agents) != 1 {
		t.Fatalf("expected 1 agent template, got %d", len(pt.Agents))
	}
	if pt.Agents[0].Count != 2 {
		t.Errorf("Count = %d, want 2", pt.Agents[0].Count)
	}
}
