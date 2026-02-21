# Agent Telemetry Module

> `internal/session/agent/` and `internal/session/agent/collector/` -- OTEL collection, metrics aggregation, and agent state detection.

## Overview

The agent module tracks what the child process (Claude Code, etc.) is doing in real time. It provides three independent state detection mechanisms with a priority hierarchy, aggregates OTEL metrics (tokens, cost, tool usage), and exposes a state machine consumed by the message delivery system, UI rendering, and external status queries.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Claude Code (child process)                  │
│                                                                  │
│  OTEL Exporter ──POST /v1/logs──►  ┌──────────────────────────┐ │
│                ──POST /v1/metrics─► │ Agent HTTP Server        │ │
│                                     │ (127.0.0.1:random port)  │ │
│  Hook Commands ──h2 hook collect──► └──────────┬───────────────┘ │
│                ──h2 permission-request──►       │                │
└─────────────────────────────────────────────────┼────────────────┘
                                                  │
         ┌────────────────────────────────────────┘
         ▼
┌─────────────────────────────────────────────────────────────────┐
│                          Agent                                   │
│                                                                  │
│  ┌────────────────┐  ┌────────────────┐  ┌────────────────────┐ │
│  │ OutputCollector │  │  OtelCollector  │  │   HookCollector    │ │
│  │ (PTY timing)   │  │  (log events)   │  │   (hook events)    │ │
│  │ Priority: LOW   │  │  Priority: MED  │  │   Priority: HIGH   │ │
│  └───────┬────────┘  └───────┬────────┘  └────────┬───────────┘ │
│          │                   │                      │            │
│          └──────────┬────────┘──────────────────────┘            │
│                     ▼                                            │
│             watchState goroutine                                 │
│             (forwards primary collector → Agent state)           │
│                     │                                            │
│                     ▼                                            │
│  ┌──────────────────────────────────────┐                       │
│  │          Agent State Machine          │                       │
│  │  State: Active | Idle | Exited       │                       │
│  │  SubState: Thinking | ToolUse |      │                       │
│  │           WaitingForPermission |     │                       │
│  │           Compacting | None         │                       │
│  └──────────────────────────────────────┘                       │
│                                                                  │
│  ┌──────────────────────────────────────┐                       │
│  │          OtelMetrics Store            │                       │
│  │  Tokens, Cost, ToolCounts, Models    │                       │
│  └──────────────────────────────────────┘                       │
│                                                                  │
│  ┌──────────────────────────────────────┐                       │
│  │          ActivityLog (JSONL)          │                       │
│  └──────────────────────────────────────┘                       │
└─────────────────────────────────────────────────────────────────┘
```

## Collector Priority Hierarchy

The system uses a **committed-authority model**: once a higher-fidelity source fires, it becomes the sole authority.

| Priority | Collector | Trigger | Accuracy |
|----------|-----------|---------|----------|
| **HIGH** | HookCollector | Claude Code hook events via `h2 hook collect` | Precise event-driven state transitions with sub-state tracking |
| **MED** | OtelCollector | OTEL log events via `POST /v1/logs` | Coarser: active on any event, idle after 2s silence |
| **LOW** | OutputCollector | PTY output activity | Coarsest: active on write, idle after 2s silence |

Only the `primaryCollector` feeds the Agent's state machine. For Claude Code, this is always the HookCollector (if hooks are configured). For generic agents, it falls back to OTEL then Output.

## Agent Type Abstraction

```go
type AgentType interface {
    Name() string
    Command() string
    PrependArgs(sessionID string) []string
    ChildEnv(cp *CollectorPorts) map[string]string
    Collectors() CollectorSet
    OtelParser() OtelParser
    DisplayCommand() string
}
```

| Type | Collectors | Special Behavior |
|------|-----------|-----------------|
| `ClaudeCodeType` | OTEL + Hooks + Output | Injects OTEL env vars, session ID args, `ClaudeCodeParser` for metrics |
| `GenericType` | Output only | No special args or env |

`ResolveAgentType(command)` matches `filepath.Base(command)` against `"claude"`, falls back to `GenericType`.

## State Machine

```
                    ┌─────────────┐
                    │ Initialized │
                    └──────┬──────┘
                           │ (first collector event)
                           ▼
              ┌────► ┌──────────┐ ◄───── activity signal
              │      │  Active  │        (from authoritative collector)
              │      └────┬─────┘
              │           │ idle timer expires (2s no activity)
              │           ▼
              │      ┌──────────┐
              └──────│   Idle   │
                     └────┬─────┘
                          │ SessionEnd hook / child exit
                          ▼
                     ┌──────────┐
                     │  Exited  │  (terminal state)
                     └──────────┘
```

Sub-states (within Active):

| SubState | Condition |
|----------|-----------|
| `Thinking` | After `UserPromptSubmit` or `PostToolUse` |
| `ToolUse` | After `PreToolUse` (with tool name) |
| `WaitingForPermission` | After `PermissionRequest` or `permission_decision{ask_user}` |
| `Compacting` | After `PreCompact` |
| `None` | Default |

## HookCollector -- Precise State Detection

The HookCollector receives events from Claude Code via `h2 hook collect` (a Claude Code hook command):

| Hook Event | State Effect | Resets Idle Timer |
|-----------|-------------|-------------------|
| `SessionStart` | Commits hook authority, idle | No |
| `UserPromptSubmit` | Active (Thinking) | Yes |
| `PreToolUse` | Active (ToolUse) | Yes |
| `PostToolUse` | Active (Thinking) | Yes |
| `PermissionRequest` | Active (WaitingForPermission) | Yes |
| `Stop` | Idle | No |
| `SessionEnd` | Exited | No |
| `permission_decision` | Updates blocked tracking | No |
| `PreCompact` | Active (Compacting) | Yes |

**Permission blocking**: When `permission_decision` has `decision == "ask_user"`, sets `BlockedOnPermission=true`. Any other decision clears blocked state. The delivery loop checks blocked state to hold back normal-priority messages while the agent waits for a permission prompt response.

**`NoteInterrupt()`**: Treated like `Stop` -- clears blocked state and transitions to Idle. Called when Ctrl+C is sent to the PTY.

### HookState Snapshot

```go
type HookState struct {
    LastEvent, LastToolName string
    LastEventTime          time.Time
    ToolUseCount           int64
    BlockedOnPermission    bool
    BlockedToolName        string
    Compacting             bool
}
```

## OutputCollector and OtelCollector

Both use the same timer-based pattern:

```go
func (c *OutputCollector) run() {
    for {
        select {
        case <-c.notifyCh:       // NoteOutput() called
            emit(StateActive)
            resetTimer(2s)
        case <-timer.C:          // 2s without activity
            emit(StateIdle)
        case <-c.stopCh:
            return
        }
    }
}
```

The drain-and-replace pattern on `stateCh` (buffered 1) ensures consumers always see the latest state:
```go
select { case <-c.stateCh: default: }  // drain stale
c.stateCh <- su                         // write latest
```

## OTEL HTTP Server

The Agent starts an HTTP server on `127.0.0.1:0` (random port) with two endpoints:

### `POST /v1/logs`

1. Read body, append raw JSON to `otel-logs.jsonl`
2. Parse OTLP `OtelLogsPayload` structure
3. For each `logRecord`:
   - Extract `event.name` attribute
   - Call `otelCollector.NoteEvent()` (marks active)
   - Call `OtelParser.ParseLogRecord()` → `OtelMetricsDelta`
   - Call `metrics.Update(delta)`

### `POST /v1/metrics`

1. Read body, append raw JSON to `otel-metrics.jsonl`
2. Parse `ParseOtelMetricsPayload()` → per-model costs, tokens, lines added/removed, active time
3. Call `metrics.UpdateFromMetricsEndpoint(parsed)`

The child process (Claude Code) is configured to export OTEL to this server via environment variables:
```
CLAUDE_CODE_ENABLE_TELEMETRY=1
OTEL_METRICS_EXPORTER=otlp
OTEL_LOGS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http/json
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<port>
OTEL_METRIC_EXPORT_INTERVAL=5000
OTEL_LOGS_EXPORT_INTERVAL=1000
```

## Metrics Store

`OtelMetrics` is a cumulative store protected by `sync.RWMutex`:

```go
type OtelMetrics struct {
    InputTokens, OutputTokens, TotalTokens int64
    CostUSD        float64
    APIRequests    int64
    ToolResultCount int64
    ToolCounts     map[string]int64  // per-tool call counts
    LinesAdded, LinesRemoved int64
    ActiveTimeHrs  float64
    ModelCosts     map[string]float64  // per-model cost
    ModelTokens    map[string]ModelTokenCount  // per-model in/out/cache tokens
    Connected      bool
}
```

- `Update(delta)` -- increments from log record parsing
- `UpdateFromMetricsEndpoint(parsed)` -- overwrites monotonic totals from metrics endpoint
- `Snapshot()` -- deep copy (copies all maps) under read lock

## Claude Code Log Record Parser

`ClaudeCodeParser.ParseLogRecord` extracts metrics from specific OTEL events:

| Event | Extracted Fields |
|-------|-----------------|
| `api_request` | `input_tokens`, `output_tokens`, `cost_usd`, `IsAPIRequest=true` |
| `tool_result` | `tool_name`, token counts, `IsToolResult=true` |
| Other events | `nil` (still counted by `noteOtelEvent`) |

Handles OTLP's dual-encoding (intValue as raw JSON or quoted string, stringValue as fallback).

## Activity Logging

The `ActivityLog` receives events from all collectors and the state machine, writing JSONL to the session directory:

| Event Type | Source |
|-----------|--------|
| `hook` | HookCollector |
| `permission_decision` | HookCollector |
| `otel_connected` | OTEL HTTP server first connection |
| `state_change` | Agent state machine transitions |
| `session_summary` | Session shutdown (tokens, cost, git stats, uptime) |
