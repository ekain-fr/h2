# Configuration and Template System

> `internal/config/` and `internal/tmpl/` -- Directory resolution, role/pod loading, template rendering, session setup, and runtime overrides.

## Overview

The config module resolves the h2 working directory, loads and validates role and pod definitions, manages multi-directory routing, applies runtime overrides, and bootstraps session directories with Claude Code hook wiring. The template engine provides Go `text/template` rendering with a two-phase parse strategy that separates variable definitions from template body.

## h2 Directory Resolution

`config.ResolveDir()` is the canonical entry point, cached via `sync.Once`:

```
1. H2_DIR environment variable (must contain .h2-dir.txt marker)
2. Walk up from CWD looking for .h2-dir.txt marker
3. Fall back to ~/.h2/ if it has the marker
4. Auto-migrate: if ~/.h2/ has roles/, sessions/, sockets/ but no marker, write it
5. Error → "run h2 init"
```

The `.h2-dir.txt` marker file is the canonical indicator that a directory is an h2 root. This allows multiple h2 directories (one per project) to coexist, with automatic discovery when running commands from within subdirectories.

## Directory Structure

```
~/.h2/ (or any h2-initialized directory)
├── .h2-dir.txt              ← marker file
├── config.yaml              ← top-level config (bridges, users)
├── roles/                   ← role YAML files
├── sessions/                ← per-agent session metadata
├── sockets/                 ← Unix domain sockets for IPC
├── messages/                ← persisted inter-agent messages
├── claude-config/
│   └── default/             ← default Claude config
│       ├── .claude.json     ← auth credentials
│       ├── settings.json    ← hooks, permissions
│       └── CLAUDE.md        ← agent instructions
├── pods/
│   ├── roles/               ← pod-scoped role overrides
│   └── templates/           ← pod launch templates
├── projects/                ← code checkouts
├── worktrees/               ← git worktrees for isolation
└── logs/                    ← bridge daemon logs
```

## Roles

Roles are the primary configuration bundle for agents. They define behavior, permissions, working directory, and integration settings.

```yaml
# ~/.h2/roles/coding.yaml
name: coding
description: "A coding agent that writes and debugs code"
agent_type: claude                    # "claude" (default) or generic
model: opus
claude_config_dir: ~/.h2/claude-config/default
working_dir: "."                      # resolved relative to h2 dir
instructions: |
  You are a coding agent. You write, edit, and debug code.
  Your name is {{ .AgentName }} and your role is {{ .RoleName }}.
system_prompt: ""                     # alternative to instructions
permission_mode: default
permissions:
  allow:
    - "Read"
    - "Bash(git *)"
  deny: []
  agent:
    enabled: true
    instructions: |
      ALLOW standard dev commands. DENY destructive system ops.
heartbeat:
  idle_timeout: "5m"
  message: "What should I work on next?"
  condition: "bd list --mine --status open | grep -q ."
worktree:
  project_dir: ~/projects/myapp
  branch_from: main
  branch_name: "{{ .AgentName }}"
hooks: {}                             # passed through to settings.json
settings: {}                          # passed through to settings.json
variables:
  team:
    description: "Team name for multi-team setups"
    default: "default"
  task_id:
    description: "Task ID to work on"
    # no default = required
```

### Role Loading

Two load paths:

| Function | Template Rendering | Use Case |
|----------|-------------------|----------|
| `LoadRole(name)` | No | Inspection, listing |
| `LoadRoleRendered(name, ctx)` | Yes | Agent launch |

`LoadRoleRendered` flow:
1. Read raw YAML text
2. `tmpl.ParseVarDefs()` → extract `variables:` block before template rendering
3. Merge provided vars with defaults, validate required vars
4. `tmpl.Render()` → execute Go template on remaining text
5. `yaml.Unmarshal()` → parse rendered YAML into `Role` struct
6. `role.Validate()` → enforce structural rules

### Claude Config Dir Resolution

```go
func (r *Role) GetClaudeConfigDir() string {
    // "~/"        → "" (skip CLAUDE_CONFIG_DIR override, use default)
    // "~/..."     → expand to home directory
    // ""          → <h2-dir>/claude-config/default/
    // other       → literal value
}
```

## Pod Templates

Pods launch groups of agents together from a template:

```yaml
# ~/.h2/pods/templates/dev-team.yaml
pod_name: dev-team
variables:
  project:
    description: "Project to work on"
agents:
  - name: concierge
    role: concierge
  - name: coder
    role: coding
    count: 3                          # creates coder-1, coder-2, coder-3
    vars:
      task_prefix: "code"
  - name: reviewer
    role: reviewer
```

### Pod Agent Expansion

`ExpandPodAgents(template)` converts the compact agent list into a flat slice:

| `count` | Result |
|---------|--------|
| nil | 1 agent, Index=0, Count=0 |
| 0 | skip (disabled) |
| 1 with `{{ }}` in name | 1 agent with template-rendered name |
| N | N agents: `name-1`, `name-2`, ... (or template-rendered) |

### Pod Role Resolution

`LoadPodRole` checks `<h2-dir>/pods/roles/<name>.yaml` first, falls back to global `<h2-dir>/roles/<name>.yaml`. This allows pod-specific role overrides without modifying shared roles.

## Template Engine

### Context

```go
type Context struct {
    AgentName string            // .AgentName
    RoleName  string            // .RoleName
    PodName   string            // .PodName
    Index     int               // .Index (1..N for counted agents)
    Count     int               // .Count (total in group)
    H2Dir     string            // .H2Dir
    Var       map[string]string // .Var.mykey
}
```

### Custom Functions

| Function | Signature | Example |
|----------|-----------|---------|
| `seq` | `seq(start, end) []int` | `{{ range seq 1 .Count }}` |
| `split` | `split(s, sep) []string` | `{{ split "a,b,c" "," }}` |
| `join` | `join(elems, sep) string` | `{{ join .Items ", " }}` |
| `default` | `default(val, fallback) string` | `{{ default .Var.x "none" }}` |
| `upper` / `lower` | string case | `{{ upper .RoleName }}` |
| `contains` | `contains(s, substr) bool` | `{{ if contains .AgentName "coder" }}` |
| `trimSpace` | trim whitespace | `{{ trimSpace .Var.name }}` |
| `quote` | Go %q escaping | `{{ quote .Var.path }}` |

### Two-Phase Parse Strategy

The key design challenge: role YAML files contain both YAML structure *and* Go template expressions. Template expressions like `{{ if }}` would break YAML parsing. Solution:

1. **Phase 1**: Extract the `variables:` section via string manipulation (line-by-line, not YAML-parsed)
2. **Phase 2**: Render the remaining text as a Go template, then parse the result as YAML

This is why `ParseVarDefs()` returns both the variable definitions and the remaining text with the variables block removed.

## Runtime Overrides

`ApplyOverrides(role, ["worktree.branch_from=develop", "model=haiku"])` uses reflection to patch a loaded `Role` struct:

- Dot notation for nested structs (e.g., `worktree.branch_from`)
- Auto-initializes nil pointer-to-struct fields
- Type coercion: string, bool, int, *bool
- Non-overridable fields: `name`, `instructions`, `permissions`, `hooks`, `settings`

## Session Directory Setup

`SetupSessionDir(agentName, role)` creates:

```
~/.h2/sessions/<agentName>/
├── session.metadata.json     ← SessionMetadata (name, ID, config paths, timestamps)
├── permission-reviewer.md    ← AI reviewer instructions (if agent permissions enabled)
├── activity.jsonl            ← structured event log
├── otel-logs.jsonl           ← raw OTEL log payloads
└── otel-metrics.jsonl        ← raw OTEL metrics payloads
```

`EnsureClaudeConfigDir(configDir)` bootstraps the Claude config with h2's standard hooks:

```json
{
  "hooks": {
    "PreToolUse": [{ "type": "command", "command": "h2 hook collect --event PreToolUse", "timeout": 5000 }],
    "PostToolUse": [{ "type": "command", "command": "h2 hook collect --event PostToolUse", "timeout": 5000 }],
    "SessionStart": [{ "type": "command", "command": "h2 hook collect --event SessionStart", "timeout": 5000 }],
    "Stop": [{ "type": "command", "command": "h2 hook collect --event Stop", "timeout": 5000 }],
    "UserPromptSubmit": [{ "type": "command", "command": "h2 hook collect --event UserPromptSubmit", "timeout": 5000 }],
    "PermissionRequest": [{ "type": "command", "command": "h2 permission-request", "timeout": 60000 }]
  }
}
```

## Routes Registry (Multi-Directory)

The routes system enables `h2 list --all` to discover agents across multiple h2 directories:

```
~/.h2/routes.jsonl    ← append-only, file-locked
```

Each line: `{"prefix":"myproject","path":"/Users/me/myproject/.h2"}`

`RegisterRouteWithAutoPrefix` is atomic (one exclusive lock, reads existing routes, checks conflicts, writes). Prefixes auto-increment on collision (`myproject`, `myproject-2`, etc.).

## Configuration Flow Summary

```
h2 run --role coding --var task_id=42 --override model=haiku
  │
  ├── ResolveDir()                              → ~/.h2/
  ├── LoadRoleRendered("coding", ctx)
  │     ├── ParseVarDefs()                      → extract variables block
  │     ├── ValidateVars()                      → check task_id provided
  │     ├── tmpl.Render()                       → expand {{ .AgentName }}, {{ .Var.task_id }}
  │     ├── yaml.Unmarshal()                    → parse rendered YAML
  │     └── Validate()                          → structural checks
  ├── ApplyOverrides(role, ["model=haiku"])      → reflection-based patch
  ├── SetupSessionDir(name, role)               → create session dir + metadata
  ├── EnsureClaudeConfigDir(configDir)          → bootstrap hooks
  ├── git.CreateWorktree(cfg)                   → create branch (if worktree configured)
  └── ForkDaemon(opts)                          → start h2 _daemon subprocess
```
