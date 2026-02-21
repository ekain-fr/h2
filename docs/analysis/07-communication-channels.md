# Focus: Communication Channels

> How bidirectional communications with agents are managed.

## Overview

h2 manages bidirectional communication with agents through multiple channels, each optimized for a different purpose. The fundamental constraint is that agents are TUI applications running in a PTY -- they have no native IPC mechanism. h2 bridges this gap by wrapping PTY I/O, injecting Unix socket infrastructure, and registering hook commands that let the agent process report events back to h2.

## Channel Map

```
                    ┌──────────────────────────────────────────┐
                    │            Agent Process (Claude Code)    │
                    │                                          │
  h2 → Agent:      │  PTY stdin ◄────── h2 writes messages    │
                    │                    (typed, queued, raw)   │
                    │                                          │
  Agent → h2:      │  PTY stdout ──────► h2 reads output      │
                    │                    (rendered in VT)       │
                    │                                          │
  Agent → h2:      │  OTEL exporter ───► POST /v1/logs        │
  (telemetry)      │                     POST /v1/metrics      │
                    │                                          │
  Agent → h2:      │  Hook commands ───► h2 hook collect       │
  (events)         │                     h2 permission-request │
                    │                                          │
  h2 ↔ h2:        │  Unix sockets ────► agent.<name>.sock     │
  (IPC)            │                     bridge.<user>.sock    │
                    │                                          │
  External ↔ h2:   │  Telegram long-poll / HTTP POST          │
                    └──────────────────────────────────────────┘
```

## Channel 1: PTY (h2 → Agent, Agent → h2)

The PTY (pseudoterminal) is the primary communication channel. It's the same interface a human uses when interacting with a terminal application.

### Writing to the Agent (h2 → Agent)

All messages ultimately reach the agent by writing bytes to `VT.Ptm` (the PTY master file descriptor):

| Source | Path | Format |
|--------|------|--------|
| User typing (normal mode) | `Client.HandleDefaultBytes` → `VT.WritePTY` | Raw keystrokes + `\r` |
| User typing (passthrough) | `Client.HandlePassthroughBytes` → `VT.WritePTY` | All bytes forwarded |
| Queued messages | `message.RunDelivery` → `VT.Ptm.Write` | `[h2 message from: X] body\r` |
| Interrupt messages | `message.deliver` → `0x03` + wait + message | Ctrl+C then formatted message |
| Raw messages | `message.deliver` → direct write | Unformatted bytes |
| Permission responses | `message.EnqueueRaw` → direct write | Permission prompt text |

**Write timeout protection**: `WritePTY` runs the actual `Ptm.Write` in a goroutine with a 3-second timeout. If the child's stdin buffer is full (e.g., during compaction), the write times out and the child is killed rather than deadlocking.

### Reading from the Agent (Agent → h2)

`VT.PipeOutput` reads 4096-byte chunks from the PTY master in a dedicated goroutine:

```
PTY master fd → Read(4096) → RespondOSCColors → VT.Vt.Write + VT.Scrollback.Write → callback
```

The callback triggers:
1. `Agent.NoteOutput()` → feeds OutputCollector for state detection
2. `RenderScreen()` + `RenderBar()` for all connected clients

**OSC color proxying**: When Claude Code queries terminal colors (`OSC 10;?` / `OSC 11;?`), h2 intercepts these and responds with cached values from the parent terminal. This allows Claude Code's color auto-detection to work correctly inside the multiplexer.

## Channel 2: OTEL Telemetry (Agent → h2)

Claude Code has built-in OpenTelemetry instrumentation. h2 redirects this to a local HTTP server:

```
Claude Code OTEL Exporter
  │
  ├── POST /v1/logs    →  Agent.handleOtelLogs()
  │                         ├── Parse log records
  │                         ├── Extract metrics (tokens, cost, tool names)
  │                         ├── Update OtelMetrics store
  │                         ├── Notify OtelCollector (state detection)
  │                         └── Write raw payloads to otel-logs.jsonl
  │
  └── POST /v1/metrics →  Agent.handleOtelMetrics()
                            ├── Parse metrics payload
                            ├── Update per-model costs and tokens
                            └── Write raw payloads to otel-metrics.jsonl
```

**Configuration**: The child process receives environment variables directing OTEL to h2's server:
```
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<random-port>
OTEL_LOGS_EXPORT_INTERVAL=1000   (1 second)
OTEL_METRIC_EXPORT_INTERVAL=5000 (5 seconds)
```

**What's extracted from OTEL logs**:
- `api_request` events → input/output tokens, cost, API request count
- `tool_result` events → tool name, tool call count
- Any event with `event.name` → activity signal for state detection

**What's extracted from OTEL metrics**:
- `claude_code.lines_of_code.count` (added/removed)
- `claude_code.active_time.total`
- `claude_code.cost.usage` (per-model breakdown)
- `claude_code.token.usage` (per-model, per-type breakdown)

## Channel 3: Hook Events (Agent → h2)

Claude Code hooks are shell commands executed at specific lifecycle points. h2 registers hooks that report events back via Unix socket:

### Hook Registration (in settings.json)

```json
{
  "PreToolUse":        "h2 hook collect --event PreToolUse",
  "PostToolUse":       "h2 hook collect --event PostToolUse",
  "SessionStart":      "h2 hook collect --event SessionStart",
  "Stop":              "h2 hook collect --event Stop",
  "UserPromptSubmit":  "h2 hook collect --event UserPromptSubmit",
  "PermissionRequest": "h2 permission-request"
}
```

### `h2 hook collect` Flow

```
Claude Code fires hook → executes "h2 hook collect --event PreToolUse"
  → Read hook payload from stdin (JSON)
  → Resolve agent name from H2_ACTOR env var
  → Dial agent.<name>.sock
  → Send {type: "hook_event", eventName: "PreToolUse", payload: <json>}
  → Output {} to stdout (Claude Code expects this)
  → Exit 0
```

### `h2 permission-request` Flow

```
Claude Code fires PermissionRequest hook
  → h2 permission-request reads payload from stdin
  → Fast-pass check (non-risky tools auto-allowed)
  → If AI reviewer configured:
      → Launch claude --print --model haiku with reviewer instructions
      → Parse response: ALLOW / DENY / ASK_USER + reason
  → Report decision to agent socket as permission_decision event
  → Output JSON response to stdout
```

### Hook Event → State Machine

```
h2 hook collect → agent socket → Daemon.handleHookEvent()
  → HookCollector.ProcessEvent(eventName, payload)
  → HookCollector.runStateLoop():
      PreToolUse      → State: Active, SubState: ToolUse
      PostToolUse     → State: Active, SubState: Thinking
      UserPromptSubmit → State: Active, SubState: Thinking
      Stop            → State: Idle
      SessionEnd      → State: Exited
      permission_decision(ask_user) → BlockedOnPermission = true
  → Agent.watchState() forwards to Agent state machine
  → Delivery loop reads state for hold-back decisions
  → UI reads state for status bar rendering
```

## Channel 4: Unix Socket IPC (h2 ↔ h2)

All h2 processes communicate via Unix domain sockets using a JSON request/response protocol:

### Agent Sockets (`agent.<name>.sock`)

| Request | Sender | Purpose |
|---------|--------|---------|
| `send` | `h2 send`, bridge, other agents | Deliver message |
| `status` | `h2 list`, `h2 status`, bridge | Query state/metrics |
| `attach` | `h2 attach` | Connect terminal (switches to binary framing) |
| `hook_event` | `h2 hook collect` | Forward hook event from child |
| `show` | `h2 show` | Look up message by ID |
| `stop` | `h2 stop` | Graceful shutdown |

### Bridge Socket (`bridge.<user>.sock`)

| Request | Sender | Purpose |
|---------|--------|---------|
| `send` | Agent session daemon | Outbound message to external platforms |
| `status` | `h2 list` | Bridge status info |
| `stop` | `h2 stop` | Shutdown bridge daemon |

### Attach Protocol (Binary Framing)

After the initial JSON handshake, the attach connection switches to binary framing:

```
[1 byte type][4 bytes big-endian length][payload]

Type 0x00: Data  (keyboard bytes or terminal output)
Type 0x01: Control (JSON: resize events)
```

This multiplexes real-time terminal data and control messages on a single connection.

## Channel 5: External Bridges (User ↔ h2)

### Telegram

```
User's phone/app
  ↕ Telegram Bot API (HTTPS)
    ↕ h2 _bridge-service (long-poll every 30s)
      ↕ Unix socket → agent daemon
        ↕ PTY → Claude Code
```

Inbound: Telegram message → `poll()` → `handleInbound()` → agent socket → message queue → PTY

Outbound: Agent session daemon → bridge socket → `handleOutbound()` → `telegram.Send()` → Telegram API

### macOS Notifications

Outbound only: Agent session daemon → bridge socket → `handleOutbound()` → `osascript` → Notification Center

## State-Aware Delivery

The message delivery system uses agent state to determine when and how to deliver:

```
Message Queue
  │
  ├── Agent state: Active, not blocked
  │     → Deliver normal, interrupt
  │
  ├── Agent state: Active, blocked on permission
  │     → Only deliver interrupt (normal held back)
  │
  ├── Agent state: Idle
  │     → Deliver all priorities including idle/idle-first
  │
  ├── Queue paused (passthrough mode)
  │     → Only deliver interrupt
  │
  └── Interrupt delivery:
        1. Write 0x03 (Ctrl+C) to PTY
        2. Wait up to 5s for idle
        3. Retry up to 3 times
        4. Deliver formatted message
```

## Summary: All Communication Paths

| Direction | Channel | Protocol | Latency | Used For |
|-----------|---------|----------|---------|----------|
| h2 → Agent | PTY stdin | Raw bytes | Immediate | User input, messages, permission responses |
| Agent → h2 | PTY stdout | Raw bytes (VT100) | Immediate | Terminal output, rendering |
| Agent → h2 | OTEL HTTP | JSON/OTLP | 1-5s export interval | Tokens, cost, tool metrics |
| Agent → h2 | Hook exec | Unix socket JSON | Per-event (~100ms) | State transitions, permission events |
| h2 ↔ h2 | Unix socket | JSON req/resp | <1ms | Send, status, attach, stop |
| h2 ↔ h2 | Unix socket | Binary frames | <1ms | Attach terminal streaming |
| External ↔ h2 | Telegram API | HTTPS long-poll | 0-30s poll + network | Telegram messages |
| h2 → External | osascript | Process exec | ~100ms | macOS notifications |
