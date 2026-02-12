# Role & Pod Template Templating System

## Overview

Add a full templating system to h2 roles and pod templates using Go's `text/template` engine. This enables parameterized roles, dynamic pod configurations, and agent multiplication patterns.

## Current State

- Role YAML files are static — loaded and used as-is after `h2 role init`
- Pod templates are simple agent lists with hardcoded names and role references
- No variable substitution, conditionals, or loops in either
- Only "templating" is one-time `fmt.Sprintf` substitution at `h2 role init` time

## Requirements

### 1. User-Defined Variables

Variables can be declared in role and pod template YAML files with optional defaults:

```yaml
# In a role file
variables:
  project_name:
    description: "The project this agent works on"
    default: "myapp"
  team:
    description: "Team name"
    # no default — required at launch time

instructions: |
  You work on {{ .Var.project_name }} for team {{ .Var.team }}.
```

**Validation rules:**
- If a variable has no `default`, it is **required** — launching an agent with a role that has unsatisfied required variables fails with a clear error listing all missing variables
- Variables can be provided via CLI: `h2 run --name coder --role coding --var team=backend`
- Pod templates can pass variables to roles (see pod template section)
- Variable values are always strings (consistent with Go templates)

### 2. Built-In Variables

Available automatically based on context — no declaration needed:

| Variable | Available In | Description |
|----------|-------------|-------------|
| `.AgentName` | Roles | Name of the agent being launched |
| `.RoleName` | Roles | Name of the role |
| `.PodName` | Roles, Pod templates | Pod name (empty string if not in pod) |
| `.Index` | Roles (when launched via pod `count`) | 1-based index of this agent within its count group |
| `.Count` | Roles (when launched via pod `count`) | Total count for this agent group |
| `.H2Dir` | Roles, Pod templates | Absolute path to the h2 directory |

### 3. Pod Template Agent Multiplication

Pod templates gain a `count` field that launches multiple copies of an agent with the same role:

```yaml
# Pod template: dev-team.yaml
pod_name: dev-team
agents:
  - role: concierge
    name: concierge
  - role: coding
    name: "coder-{{ .Index }}"
    count: 3
  - role: reviewer
    name: reviewer
```

This launches: `concierge`, `coder-1`, `coder-2`, `coder-3`, `reviewer`.

When `count` is set:
- The `name` field is rendered as a template for each copy, with `.Index` set to 1, 2, ..., N
- `.Count` is set to N
- If `name` doesn't contain `{{ .Index }}`, an index suffix is appended automatically (e.g., `coder` becomes `coder-1`, `coder-2`, ...)
- `count` defaults to 1 (no change from current behavior)

### 4. Variable Passing from Pod Templates to Roles

Pod templates can pass variables to the roles they reference:

```yaml
# Pod template
agents:
  - role: coding
    name: "coder-{{ .Index }}"
    count: 3
    vars:
      team: backend
      project_name: h2
```

The `vars` map is merged with any CLI `--var` flags (CLI takes precedence) and passed to the role's template rendering. This is how pod templates satisfy required role variables.

### 5. Conditionals and Loops in Templates

Full Go `text/template` syntax is available in **role instructions** and **pod template fields**:

**Conditionals in role instructions:**
```yaml
instructions: |
  You are {{ .AgentName }}.
  {{ if .PodName }}
  You are part of the {{ .PodName }} pod. Coordinate with teammates.
  {{ else }}
  You are running standalone.
  {{ end }}
```

**Conditionals in pod templates:**
```yaml
agents:
  - role: concierge
    name: concierge
  {{ if .Var.include_reviewer }}
  - role: reviewer
    name: reviewer
  {{ end }}
```

**Loops in pod templates (advanced):**
```yaml
# Pod template variables
variables:
  services:
    default: "api,web,worker"

agents:
  {{ range $svc := split .Var.services "," }}
  - role: coding
    name: "{{ $svc }}-coder"
    vars:
      service: "{{ $svc }}"
  {{ end }}
```

### 6. Custom Template Functions

Beyond Go's built-in template functions, provide:

| Function | Description | Example |
|----------|-------------|---------|
| `seq` | Generate integer sequence | `{{ range $i := seq 1 3 }}` |
| `split` | Split string by delimiter | `{{ range split .Var.list "," }}` |
| `join` | Join strings | `{{ join .Var.items "," }}` |
| `default` | Fallback value | `{{ default .Var.name "unnamed" }}` |
| `upper` / `lower` | Case conversion | `{{ upper .Var.env }}` |
| `contains` | String contains check | `{{ if contains .Var.mode "debug" }}` |

## Architecture

### Template Rendering Pipeline

```
1. Load raw YAML text from file
2. Parse variable declarations (pre-template, extract `variables:` section)
3. Validate all required variables are satisfied
4. Build template data context (merge built-ins + user vars)
5. Render YAML text through text/template
6. Parse rendered YAML into Go struct
```

**Why render before YAML parse:** The template may generate structural YAML (e.g., loop producing agent entries), so we must render first, then parse the result.

**Variable declaration extraction:** The `variables:` section is parsed with a minimal pre-pass (YAML unmarshal into a map) before full template rendering, so variable defaults are available during rendering.

### New Package: `internal/tmpl/`

```go
package tmpl

// VarDef defines a template variable with optional default.
type VarDef struct {
    Description string `yaml:"description"`
    Default     string `yaml:"default"`
    HasDefault  bool   // set during parsing (distinguishes "" default from no default)
}

// Context holds all template data available during rendering.
type Context struct {
    AgentName string
    RoleName  string
    PodName   string
    Index     int
    Count     int
    H2Dir     string
    Var       map[string]string
}

// Render processes a YAML template string with the given context.
// Returns the rendered string or an error (including missing variable errors).
func Render(templateText string, ctx *Context) (string, error)

// ParseVarDefs extracts variable definitions from raw YAML text.
// Returns the definitions and the YAML text with the variables section removed.
func ParseVarDefs(yamlText string) (map[string]VarDef, string, error)

// ValidateVars checks that all required variables (no default) are provided.
// Returns a descriptive error listing all missing variables.
func ValidateVars(defs map[string]VarDef, provided map[string]string) error
```

### Changes to Existing Code

**`internal/config/role.go`:**
- `LoadRole` / `LoadRoleFrom` gain an optional `tmpl.Context` parameter
- Raw YAML is rendered through `tmpl.Render()` before YAML unmarshal
- `Role` struct gets `Variables map[string]tmpl.VarDef` field (parsed but not rendered)

**`internal/config/pods.go`:**
- `PodTemplateAgent` gains `Count int` and `Vars map[string]string` fields
- `LoadPodTemplate` renders template text before parsing
- Pod template expansion logic handles `count` multiplication

**`internal/cmd/agent_setup.go`:**
- Builds `tmpl.Context` from agent name, role, pod, and CLI vars
- Passes context through to role loading

**`internal/cmd/pod.go`:**
- `h2 pod launch` expands `count` agents, setting `.Index` and `.Count`
- Merges `vars` from pod template with CLI `--var` flags
- Passes merged vars to each agent's role rendering

**`internal/cmd/run.go`:**
- Adds `--var key=value` flag (repeatable)
- Passes vars to role loading

### Error Messages

Missing required variables produce clear, actionable errors:

```
Error: role "coding" requires variables that were not provided:

  team         — Team name
  project_name — The project this agent works on

Provide them with: h2 run --role coding --var team=X --var project_name=Y
```

## Rendering Order & Precedence

1. **Pod template** rendered first (with pod-level vars + CLI vars)
2. **Expanded agents** — count produces multiple agent entries
3. **Role** rendered for each agent (with agent-level built-ins + pod template vars + CLI vars)

**Variable precedence** (highest to lowest):
1. CLI `--var` flags
2. Pod template `vars` per-agent
3. Variable `default` values in the role/template definition

## Examples

### Example 1: Parameterized Role

```yaml
# ~/.h2/roles/service-coder.yaml
name: service-coder
variables:
  service:
    description: "Which microservice to work on"
  language:
    description: "Primary language"
    default: "go"

instructions: |
  You are {{ .AgentName }}, working on the {{ .Var.service }} service ({{ .Var.language }}).
  {{ if eq .Var.language "go" }}
  Use `go test ./...` to run tests and `go build ./...` to build.
  {{ else if eq .Var.language "python" }}
  Use `pytest` to run tests and `pip install -e .` to install.
  {{ end }}
```

Launch: `h2 run --name api-coder --role service-coder --var service=api`

### Example 2: Scaled Pod

```yaml
# ~/.h2/pods/templates/backend.yaml
pod_name: backend
variables:
  num_coders:
    default: "2"

agents:
  - role: concierge
    name: concierge
  - role: coding
    name: "coder-{{ .Index }}"
    count: {{ .Var.num_coders }}
    vars:
      team: backend
  - role: reviewer
    name: reviewer
```

Launch with defaults: `h2 pod launch backend`
Launch with 5 coders: `h2 pod launch backend --var num_coders=5`

### Example 3: Role Aware of Pod Context

```yaml
# ~/.h2/roles/coding.yaml
instructions: |
  You are {{ .AgentName }}.
  {{ if .PodName }}
  You are agent {{ .Index }}/{{ .Count }} in the {{ .PodName }} pod.
  Use `h2 list` to see your teammates and coordinate work.
  {{ end }}
```

## Files to Modify

| File | Change |
|------|--------|
| `internal/tmpl/` (new) | Template engine, variable parsing, validation, custom functions |
| `internal/tmpl/tmpl_test.go` (new) | Comprehensive tests for rendering, validation, edge cases |
| `internal/config/role.go` | Add Variables field, render through template engine |
| `internal/config/pods.go` | Add Count/Vars to PodTemplateAgent, template rendering |
| `internal/cmd/run.go` | Add `--var` flag, pass to role loading |
| `internal/cmd/pod.go` | Expand count agents, merge vars, pass to role rendering |
| `internal/cmd/agent_setup.go` | Accept and thread template context |

## Open Questions

1. **Template delimiters:** Go's default `{{ }}` could conflict with content in role instructions (e.g., JSON examples, mustache references). Should we use alternative delimiters like `<< >>` or `{{% %}}`? Or is `{{ }}` fine since instructions are natural language/markdown?

2. **Pod template YAML + Go templates:** Generating structural YAML from Go template loops requires careful indentation. Should we lint/validate the rendered YAML and give helpful errors on template indentation issues?

3. **Variable types:** Currently all string. Should we support typed variables (int, bool, list) for cleaner conditionals and loops? Or keep everything as strings and let template functions handle conversion?

4. **Escaping:** If role instructions need literal `{{ }}` (e.g., documenting Go templates), users need `{{"{{"}}` or raw string blocks. Should we provide a `raw` block or alternative syntax?

## Verification

1. `make build` compiles
2. `make test` passes — including new `tmpl` package tests
3. Manual: Create a parameterized role, launch with `--var`, verify instructions are rendered
4. Manual: Create a pod template with `count`, launch, verify agents named correctly
5. Manual: Omit a required variable, verify clear error message
6. Manual: Use conditionals in instructions, verify correct branch rendered
