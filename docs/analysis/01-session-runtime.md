# Session Runtime Module

> `internal/session/` -- The core runtime engine of h2.

## Overview

The session module manages the full lifecycle of an agent session: spawning a child process inside a PTY, routing I/O through a virtual terminal, collecting telemetry via OTEL and hooks, managing a priority-aware message queue, and serving multiple simultaneous UI clients over Unix sockets. It supports two execution modes: **interactive** (one terminal, no daemon) and **daemon** (background process accepting attach clients).

## Ownership Tree

```
Session (orchestrator, owns everything)
├── VT (PTY, midterm buffers, child process state)
├── Agent (OTEL collector, metrics, idle tracking)
├── Daemon (socket listener, attach/detach, process forking)
├── MessageQueue + Delivery goroutine
├── PassthroughOwner *Client
└── Clients []*Client
    └── Client
        ├── Output io.Writer (per-client: stdout or framed connection)
        ├── VT reference (reads shared terminal content)
        ├── Per-client state (mode, input, cursor, scroll, history, priority)
        └── Callbacks (OnSubmit, TryPassthrough, etc. -- wired by Session)
```

## File Map

| File | Responsibility |
|------|---------------|
| `session.go` | Central `Session` struct, lifecycle loops, client factory, goroutine wiring |
| `daemon.go` | `Daemon` struct, socket setup, `ForkDaemon`, `RunDaemon`, `AgentInfo` |
| `listener.go` | Socket request router (`send`, `attach`, `status`, `stop`, `hook_event`) |
| `attach.go` | Attach protocol: framing, resize, client lifecycle, passthrough cleanup |
| `heartbeat.go` | Idle-nudge system: timed messages when agent stays idle |
| `gitstats.go` | Git working tree diff stats (files changed, lines added/removed) |
| `names.go` | Random adjective-noun name generator (7,500 combinations) |
| `sysproc_unix.go` | `Setsid: true` for daemon process detachment |

## The Session Struct

```go
type Session struct {
    Name, Command string; Args []string
    SessionID, RoleName, SessionDir, ClaudeConfigDir string
    Instructions, SystemPrompt, Model, PermissionMode string
    AllowedTools, DisallowedTools []string

    Queue      *message.MessageQueue
    Agent      *agent.Agent
    VT         *virtualterminal.VT
    Client     *client.Client        // primary interactive client
    Clients    []*client.Client      // all connected clients
    PassthroughOwner *client.Client
    ExtraEnv   map[string]string
    Daemon     *Daemon
    StartTime  time.Time
    Quit       bool
    OnDeliver  func()
    // private: exitNotify, stopCh, relaunchCh, quitCh
}
```

## Lifecycle

### Daemon Mode (`RunDaemon`)

```
1. New(name, command, args)
   └── ResolveAgentType() → ClaudeCodeType | GenericType
2. initVT(24, 80)              -- default terminal size until first client attaches
3. NewClient() + AddClient()   -- primary client (used for daemon-only rendering)
4. Agent.StartCollectors()
   ├── OutputCollector
   ├── OtelCollector + HTTP server
   ├── HookCollector
   └── watchState goroutine
5. VT.StartPTY(command, args, childRows, cols, ExtraEnv)
6. goroutine: StartServices() → message.RunDelivery()
7. goroutine: RunHeartbeat()   (if configured)
8. goroutine: TickStatus()     (1s status bar refresh)
9. goroutine: VT.PipeOutput(callback)
10. goroutine: Daemon.acceptLoop()
11. lifecycleLoop()            -- blocks, handles child exit + relaunch
```

### Interactive Mode (`RunInteractive`)

Same as daemon except: uses real terminal dimensions, enters raw mode, registers SIGWINCH handler, enables mouse reporting, starts `ReadInput` goroutine. No socket listener.

### Lifecycle Loop

The `lifecycleLoop` is the core wait/relaunch loop:

```
┌──────────────────────────────────────────┐
│ VT.Cmd.Wait()  (blocks until child exit) │
└──────────────┬───────────────────────────┘
               ▼
         Render exit state
         Pause message queue
               │
     ┌─────────┴─────────┐
     ▼                    ▼
  relaunchCh           quitCh
  (re-exec cmd,        (cleanup
   reset VT,            and return)
   resume queue)
```

A client triggers `relaunchCh` from the menu (restart agent) or `quitCh` from quit/stop.

## Client Factory and Callback Wiring

`Session.NewClient()` creates clients with all reverse-dependency callbacks wired:

| Callback | Wired To |
|----------|----------|
| `OnRelaunch` | signals `relaunchCh` |
| `OnQuit` | sets `Quit=true`, signals `quitCh` |
| `OnModeChange` | releases passthrough lock on mode exit |
| `TryPassthrough` | acquires single-owner passthrough lock; pauses queue |
| `ReleasePassthrough` | releases lock; unpauses queue |
| `TakePassthrough` | force-kicks current owner to ModeNormal |
| `QueueStatus` | returns queue depth and pause state |
| `OtelMetrics` | returns OTEL metrics snapshot |
| `AgentState` | returns current state/sub-state labels |
| `HookState` | returns hook collector snapshot |
| `OnInterrupt` | calls `Agent.NoteInterrupt()` |
| `OnSubmit` | calls `SubmitInput()` for non-normal priority messages |

This callback pattern breaks the circular dependency between `session` and `client` packages -- `client` never imports `session`.

## Daemon and Socket Listener

The `Daemon` accepts connections on `~/.h2/sockets/agent.<name>.sock` and routes requests:

| Request Type | Handler | Purpose |
|-------------|---------|---------|
| `send` | `handleSend` | Enqueue message (raw or formatted) |
| `show` | `handleShow` | Look up message by ID |
| `status` | `handleStatus` | Return full `AgentInfo` JSON |
| `attach` | `handleAttach` | Switch to framed attach protocol |
| `hook_event` | `handleHookEvent` | Forward hook event to collector |
| `stop` | `handleStop` | Graceful shutdown (kill child, signal quit) |

### `AgentInfo` -- The Status Response

```go
type AgentInfo struct {
    Name, Command, Role, Pod string
    Uptime                    string
    State, SubState           string
    ToolName                  string
    QueueCount               int
    // OTEL metrics:
    InputTokens, OutputTokens, TotalTokens int64
    CostUSD                   float64
    APIRequests, ToolCalls    int64
    LinesAdded, LinesRemoved  int64
    ActiveTimeHrs             float64
    ToolCounts                map[string]int64
    ModelStats                []ModelStat
    // Git:
    GitFilesChanged, GitLinesAdded, GitLinesRemoved int
    // Hook collector:
    HookToolUseCount int64
    Blocked          bool
    BlockedToolName  string
}
```

## Attach Protocol

When `h2 attach <name>` connects:

```
1. Send {type: "attach", rows: 40, cols: 120}
2. Receive OK response (protocol switches to binary framing)
3. Daemon creates new Client with frameWriter{conn} as Output
4. Resize PTY to client dimensions (if changed)
5. Send mouse-enable escape sequences
6. Render current screen state
7. Enter bidirectional framing loop:

   Client → Daemon: [0x00][4-byte len][keyboard bytes]     (data frame)
   Client → Daemon: [0x01][4-byte len][resize JSON]        (control frame)
   Daemon → Client: [0x00][4-byte len][terminal output]    (data frame)

8. On disconnect:
   - Release passthrough if held
   - Remove client
   - Resize VT to min dimensions across remaining clients
   - Re-render all remaining clients
```

## Child Output Pipeline

```
Child process writes to PTY slave
  → VT.Ptm.Read() [PipeOutput goroutine, 4KB chunks]
  → RespondOSCColors() [proxy terminal color queries]
  → VT.Mu.Lock()
  → VT.Vt.Write()        [update live terminal buffer -- cursor-anchored]
  → VT.Scrollback.Write() [append to history -- grows indefinitely]
  → pipeOutputCallback()
       ├── Agent.NoteOutput() → OutputCollector.NoteOutput()
       └── for each Client:
             ├── RenderScreen() → client's Output writer
             └── RenderBar()    → client's Output writer
  → VT.Mu.Unlock()
```

## Heartbeat System

The heartbeat nudges idle agents with a configured message after a timeout:

```go
type HeartbeatConfig struct {
    IdleTimeout time.Duration  // e.g., 5m
    Message     string         // e.g., "What should I work on next?"
    Condition   string         // optional: shell command that must succeed
}
```

Loop: wait for idle → start timer → if agent goes active, restart → on timeout, check condition → send idle-priority message.

## Key Design Decisions

1. **Single-process daemon**: All clients share one VT buffer and one agent process. The daemon is the single source of truth.

2. **VT.Mu as central lock**: Every operation touching terminal state (render, resize, input, PipeOutput) holds this mutex. Prevents races between the PTY output goroutine and client input handlers.

3. **Fork-exec self-invocation**: `ForkDaemon` re-execs `h2 _daemon` with `Setsid=true`, fully detaching from the controlling terminal. Stdin/stdout/stderr point to `/dev/null`.

4. **PTY write timeouts**: `WritePTY` runs the write in a goroutine with a 3-second timeout, returning `ErrPTYWriteTimeout` to prevent the VT mutex from being held forever if the child stops reading.

5. **Passthrough single-owner**: Only one client at a time can own passthrough mode (direct PTY access). The queue is paused during passthrough. `TakePassthrough` allows force-kicking the current owner.
