# Design: `h2 hook` Command & Hook Collector

## Problem

h2 has limited visibility into what Claude Code is doing between OTEL events. OTEL gives us token counts and API request activity, but Claude Code's hook system exposes much richer lifecycle events — tool use, notifications, session start/end, subagent activity, idle prompts, etc. We want to capture these as a parallel data source to OTEL.

## Concept: Collectors

OTEL is one "collector" — it runs an HTTP server, sets env vars on the child process, and accumulates metrics. The hook collector is another: it provides a CLI command (`h2 hook`) that Claude Code invokes as a hook handler, and it reports events back to the h2 session over the existing Unix socket.

Different agent types or setups enable different collectors. A Claude Code agent gets both OTEL and hooks. A generic shell agent might get neither. Future agents might have their own collectors.

```
Agent (Session)
├── OTEL Collector     — env vars, HTTP server, parses OTLP
├── Hook Collector     — `h2 hook` CLI command, receives hook JSON on stdin
└── (future)           — file watcher, log tailer, etc.
```

## Data Model

### Current state of the world

Status data lives across several layers today:

```
Session (internal/session/session.go)
├── state: State (Active|Idle|Exited)    — derived from child output + OTEL activity
├── stateChangedAt: time.Time
├── StartTime: time.Time
├── Queue.PendingCount(): int
│
├── Agent (internal/session/agent/otel.go)
│   ├── helper: AgentHelper              — agent-type-specific config
│   ├── metrics: *OtelMetrics            — token counts, cost, API request counts
│   ├── port: int                        — OTEL collector HTTP port
│   └── otelNotify: chan struct{}         — activity signal for state machine
│
└── Daemon.AgentInfo() → AgentInfo       — snapshot for status protocol & h2 list
```

`Session.state` (Active/Idle/Exited) is the only "derived" status. It's computed
by `watchState()` which listens for child output and OTEL events and runs an
idle timer. Everything else is raw data that gets formatted for display.

`AgentInfo` is the external representation — it's what the socket protocol returns
for `"status"` requests and what `h2 list` and the status bar consume.

### Proposed: Collector-based model

The `Agent` struct becomes the home for all collectors. Each collector is optional
and provides its own snapshot type. The `Agent` unifies them into a single
`StatusSnapshot` that the session and protocol layer consume.

```go
// Agent wraps all collectors for a session.
type Agent struct {
    helper      AgentHelper
    otel        *OtelCollector       // token counts, cost (may be nil/inactive)
    hooks       *HookCollector       // tool use, lifecycle events (may be nil/inactive)
    // future: fileWatcher, logTailer, etc.

    // Unified activity signal — any collector can trigger this.
    activityNotify chan struct{}
    stopCh         chan struct{}
}
```

Each collector has its own state and snapshot:

```go
// OtelCollector — what we have today, renamed from the current Agent OTEL fields.
type OtelCollector struct {
    metrics  *OtelMetrics
    listener net.Listener
    server   *http.Server
    port     int
    notify   chan struct{}
}

type OtelSnapshot struct {
    TotalTokens  int64
    TotalCostUSD float64
    Connected    bool     // true after first event received
    Port         int
}

// HookCollector — new, receives events from `h2 hook` via socket.
type HookCollector struct {
    mu             sync.RWMutex
    lastEvent      string          // "PreToolUse", "PostToolUse", etc.
    lastEventTime  time.Time
    lastToolName   string          // from PreToolUse/PostToolUse tool_name
    toolUseCount   int64           // total tool invocations seen
    subagentCount  int             // increment on SubagentStart, decrement on SubagentStop
    notify         chan struct{}
}

type HookSnapshot struct {
    LastEvent     string
    LastEventTime time.Time
    LastToolName  string
    ToolUseCount  int64
    SubagentCount int
}
```

The `Agent` provides a unified snapshot and activity channel:

```go
// StatusSnapshot combines all available collector data.
// Fields are zero-valued when their collector is absent or hasn't received data.
type StatusSnapshot struct {
    Otel  *OtelSnapshot   // nil if OTEL collector not active
    Hooks *HookSnapshot   // nil if hook collector not active
}

// ActivityNotify returns a channel signaled when any collector sees activity.
func (a *Agent) ActivityNotify() <-chan struct{} {
    return a.activityNotify
}

// Status returns a unified snapshot from all active collectors.
func (a *Agent) Status() StatusSnapshot {
    snap := StatusSnapshot{}
    if a.otel != nil {
        s := a.otel.Snapshot()
        snap.Otel = &s
    }
    if a.hooks != nil {
        s := a.hooks.Snapshot()
        snap.Hooks = &s
    }
    return snap
}
```

### Graceful degradation

The key design constraint: everything is optional. If OTEL isn't set up (e.g.
non-Claude agent, or OTEL env vars not configured), `snap.Otel` is nil.
If hooks aren't configured, `snap.Hooks` is nil. Consumers check for nil:

```go
// In status bar rendering:
snap := s.Agent.Status()
if snap.Otel != nil && snap.Otel.Connected {
    label += " | " + formatTokens(snap.Otel.TotalTokens) + " " + formatCost(snap.Otel.TotalCostUSD)
}
if snap.Hooks != nil && snap.Hooks.LastToolName != "" {
    label += " | " + snap.Hooks.LastToolName
}

// In AgentInfo for protocol:
if snap.Otel != nil { info.TotalTokens = snap.Otel.TotalTokens; ... }
if snap.Hooks != nil { info.LastToolUse = snap.Hooks.LastToolName; ... }
```

`h2 list` does the same — only shows fields that have data:

```
# Full data (OTEL + hooks active):
  ● concierge claude — active 3s, up 12m, 45k $1.23 [550e8400] (Edit session.go)

# OTEL only (no hooks configured):
  ● concierge claude — active 3s, up 12m, 45k $1.23 [550e8400]

# Hooks only (OTEL not connected yet):
  ● concierge claude — active 3s, up 12m [550e8400] (Edit session.go)

# Neither (generic agent):
  ● my-shell bash — idle 5s, up 2m
```

### Idle/active status and collector authority

The idle/active determination uses a **committed authority** model. Once a
higher-fidelity collector proves it's working (by firing at least one event),
it becomes the sole authority for idle/active status. We don't bounce between
data sources mid-session.

**Priority order** (highest to lowest):
1. **Hook collector** — most granular (knows about individual tool use, subagents)
2. **OTEL collector** — knows about API requests and token activity
3. **Output timer** — fallback, watches child PTY output timing (current behavior)

**Commitment rules:**
- On first hook event → hook collector becomes the idle authority for this session
- On first OTEL event (if no hooks have fired) → OTEL becomes the idle authority
- If neither has fired → output timer is the authority (current behavior)
- Child exit always overrides everything → Exited

Once committed, that source stays authoritative. We trust that if one hook event
fired, the hook setup is working and more will come. We don't wait for each
individual hook type to fire before trusting the data source.

**Why not mix sources?** Mixing creates confusing edge cases. If the hook
collector says "last event 10s ago" but the output timer says "output 1s ago",
which one is right? The output timer would keep resetting idle even when the
agent is genuinely waiting for the next turn. By committing to one authority,
the status is consistent and predictable.

The idle state lives on the Agent (not the Session), since it's derived from
collector data. The Session asks the Agent for status rather than computing it
independently.

```go
// Agent tracks which collector is authoritative for idle/active.
type IdleAuthority int
const (
    AuthorityOutputTimer IdleAuthority = iota  // fallback
    AuthorityOtel                               // OTEL events drive idle
    AuthorityHooks                              // hook events drive idle
)

type Agent struct {
    // ...collectors...
    idleAuthority IdleAuthority
    idleTimer     *time.Timer        // fallback timer, ticks when no collector is authoritative
}

// NoteActivity is called when any collector fires. It promotes the authority
// if a higher-priority collector just activated for the first time.
func (a *Agent) NoteActivity(source IdleAuthority) {
    if source > a.idleAuthority {
        a.idleAuthority = source  // promote (e.g. timer → otel → hooks)
    }
    // Reset idle state based on the current authority
    if source >= a.idleAuthority {
        a.setState(AgentActive)
        a.resetIdleTimer()
    }
    // Lower-priority sources are ignored once a higher one is committed
}
```

The Session's `watchState()` simplifies — it listens on one unified channel and
delegates to the Agent:

```go
func (s *Session) watchState(stop <-chan struct{}) {
    for {
        select {
        case <-s.outputNotify:
            s.Agent.NoteActivity(AuthorityOutputTimer)
        case <-s.Agent.ActivityNotify():
            // Agent already handled the state change internally
        case <-s.Agent.IdleNotify():
            // Agent's idle timer fired — transition to idle
        case <-s.exitNotify:
            s.Agent.SetState(AgentExited)
        case <-stop:
            return
        }
    }
}
```

The Agent fans in all collector notify channels into the single `activityNotify`:

```go
func (a *Agent) startActivityFanIn() {
    go func() {
        for {
            select {
            case <-a.otel.Notify():
                a.NoteActivity(AuthorityOtel)
            case <-a.hooks.Notify():
                a.NoteActivity(AuthorityHooks)
            case <-a.stopCh:
                return
            }
            select {
            case a.activityNotify <- struct{}{}:
            default:
            }
        }
    }()
}
```

### AgentHelper controls which collectors are active

```go
type AgentHelper interface {
    OtelParser() OtelParser
    OtelEnv(otelPort int) map[string]string
    Collectors() CollectorSet   // NEW
}

type CollectorSet struct {
    Otel  bool   // start OTEL HTTP collector
    Hooks bool   // accept hook events on socket
}
```

`ClaudeCodeHelper` returns `{Otel: true, Hooks: true}`.
`GenericAgentHelper` returns `{Otel: false, Hooks: false}`.

Agent initialization checks these flags:

```go
func (a *Agent) Init() {
    cfg := a.helper.Collectors()
    if cfg.Otel {
        a.otel = NewOtelCollector(a.helper)
    }
    if cfg.Hooks {
        a.hooks = NewHookCollector()
    }
    a.startActivityFanIn()
}
```

### Protocol changes

`AgentInfo` gains optional fields from each collector:

```go
type AgentInfo struct {
    // Existing
    Name          string `json:"name"`
    Command       string `json:"command"`
    SessionID     string `json:"session_id,omitempty"`
    Uptime        string `json:"uptime"`
    State         string `json:"state"`
    StateDuration string `json:"state_duration"`
    QueuedCount   int    `json:"queued_count"`

    // From OTEL collector (omitted if not active)
    TotalTokens  int64   `json:"total_tokens,omitempty"`
    TotalCostUSD float64 `json:"total_cost_usd,omitempty"`

    // From Hook collector (omitted if not active)
    LastToolUse  string `json:"last_tool_use,omitempty"`
    ToolUseCount int64  `json:"tool_use_count,omitempty"`
}
```

New request type for receiving hook events:

```json
{
  "type": "hook_event",
  "event_name": "PreToolUse",
  "payload": { ... full hook JSON from stdin ... }
}
```

## The `h2 hook` Command

A single CLI command that handles all Claude Code hook events:

```
h2 hook [--agent <agent-name>]
```

- Reads the hook JSON payload from **stdin** (Claude Code pipes it)
- Extracts `hook_event_name` from the JSON
- Reports the event to the agent's h2 session via the existing Unix socket
- Exits with code 0 and empty JSON `{}` (no blocking, no decisions)

The `--agent` flag identifies which h2 session to report to. In practice this
comes from the `H2_ACTOR` env var that h2 already injects into child processes,
so the hook config just uses `$H2_ACTOR`. The command defaults to `$H2_ACTOR`
if `--agent` is not explicitly provided.

### Hook Event Flow

```
Claude Code fires hook
  → runs: h2 hook
  → stdin: {"session_id": "...", "hook_event_name": "PreToolUse", "tool_name": "Bash", ...}
  → h2 hook reads H2_ACTOR env var → "concierge"
  → connects to ~/.h2/sockets/agent.concierge.sock
  → sends: {"type": "hook_event", "event_name": "PreToolUse", "payload": {...}}
  → daemon receives event, calls hooks.ProcessEvent()
  → h2 hook exits 0, stdout: {}
```

### Hook Configuration

Users configure Claude Code to call `h2 hook` for desired events:

```json
{
  "hooks": {
    "PreToolUse": [{"matcher": "", "hooks": [{"type": "command", "command": "h2 hook", "timeout": 5}]}],
    "PostToolUse": [{"matcher": "", "hooks": [{"type": "command", "command": "h2 hook", "timeout": 5}]}],
    "Notification": [{"matcher": "", "hooks": [{"type": "command", "command": "h2 hook", "timeout": 5}]}],
    "Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "h2 hook", "timeout": 5}]}],
    "SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "h2 hook", "timeout": 5}]}],
    "SessionEnd": [{"matcher": "", "hooks": [{"type": "command", "command": "h2 hook", "timeout": 5}]}]
  }
}
```

## Implementation Order

### Phase 1: Core plumbing
1. Add `HookCollector` struct to `internal/session/agent/hook_collector.go`
2. Refactor `Agent` to hold collectors with unified `ActivityNotify()`
3. Add `hook_event` request type to socket protocol and listener
4. Add `h2 hook` CLI command
5. Wire `ActivityNotify()` into session `watchState` (replacing `OtelNotify`)

### Phase 2: Status exposure
6. Add collector-derived fields to `AgentInfo`
7. Update `h2 list` to show current tool/activity (graceful degradation)
8. Update status bar rendering to use `Agent.Status()` snapshot

## Files to Modify/Create

**New files:**
- `internal/session/agent/hook_collector.go` — HookCollector struct and event processing
- `internal/cmd/hook.go` — `h2 hook` CLI command

**Modified files:**
- `internal/session/agent/agent_helper.go` — add Collectors() to AgentHelper interface
- `internal/session/agent/otel.go` — refactor Agent to hold collectors, add ActivityNotify fan-in
- `internal/session/message/protocol.go` — add hook_event request type, AgentInfo fields
- `internal/session/listener.go` — handle hook_event requests
- `internal/session/session.go` — use Agent.ActivityNotify() in watchState
- `internal/cmd/ls.go` — display collector-derived info with graceful degradation
- `internal/cmd/root.go` — register hook command

## Open Questions

1. **Auto-configure hooks?** Should `h2 run` automatically write Claude Code hook config, or require manual setup? Auto-config is more convenient but modifying the user's settings is invasive.

2. **Async hooks?** Claude Code supports `"async": true` on hooks, which means they run in the background without blocking. This would be ideal for our use case (we never want to block Claude), but we need to verify async hooks still get stdin.

3. **Event filtering.** Do we want all 14 hook events, or start with a subset? Suggested minimal set: PreToolUse, PostToolUse, Notification, Stop, SessionStart, SessionEnd.
