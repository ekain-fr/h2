# Focus: Agent Runner

> How agents are launched, controlled, and managed throughout their lifecycle.

## Overview

The agent runner is h2's core capability -- it manages AI coding agents as background daemon processes, each running in its own PTY with full telemetry collection, message queuing, and multi-client terminal access. The runner handles the complete lifecycle: configuration resolution, process spawning, state tracking, message delivery, and graceful shutdown.

## Launch Flow

The full sequence from `h2 run` to a running agent:

```
h2 run --role coding --name coder-1 --detach
  │
  │  Phase 1: Configuration
  ├── ResolveDir()                    → find h2 directory
  ├── GenerateName()                  → random name if not provided
  ├── LoadRoleRendered(name, ctx)     → template-render + parse role YAML
  ├── ApplyOverrides(role, overrides) → reflection-based field patching
  │
  │  Phase 2: Setup
  ├── SetupSessionDir(name, role)     → create session dir, metadata, permissions
  ├── EnsureClaudeConfigDir(dir)      → bootstrap Claude config with h2 hooks
  ├── git.CreateWorktree(cfg)         → create git worktree branch (if configured)
  │
  │  Phase 3: Fork
  ├── ForkDaemon(opts)                → re-exec h2 as background daemon
  │     ├── Build args: h2 _daemon --name=coder-1 --command=claude ...
  │     ├── Detach: Setsid=true, stdin/stdout/stderr=/dev/null
  │     ├── Start subprocess
  │     └── Poll for socket (50x100ms, 5s timeout)
  │
  │  Phase 4: Attach (unless --detach)
  └── doAttach(name)                  → connect terminal to daemon
```

### Safety Guard

If `CLAUDECODE` env var is set (meaning we're inside a Claude Code session), `h2 run` refuses to start in interactive mode. This prevents agents from accidentally spawning nested interactive sessions.

## The Daemon Process

When `h2 _daemon` starts (the forked subprocess), it runs the full session setup:

```
h2 _daemon --name coder-1 --command claude --role coding ...
  │
  ├── session.New(name, command, args)
  │     └── ResolveAgentType("claude") → ClaudeCodeType
  │
  ├── Create Unix socket: agent.coder-1.sock
  │
  ├── Write session metadata
  │
  ├── initVT(24, 80)                  ← default size until client attaches
  │
  ├── Agent.StartCollectors()
  │     ├── OutputCollector            ← PTY output timing
  │     ├── OTEL HTTP server           ← binds 127.0.0.1:random-port
  │     ├── OtelCollector              ← OTEL event timing
  │     ├── HookCollector              ← precise hook-driven state
  │     └── watchState goroutine       ← forwards primary collector to state machine
  │
  ├── Build environment:
  │     ├── H2_DIR, H2_ACTOR, H2_ROLE, H2_SESSION_DIR
  │     ├── CLAUDE_CONFIG_DIR
  │     ├── OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<port>
  │     └── CLAUDE_CODE_ENABLE_TELEMETRY=1
  │
  ├── VT.StartPTY("claude", args, childRows, cols, extraEnv)
  │     └── creack/pty.StartWithSize() → child process in PTY
  │
  ├── goroutine: message.RunDelivery()  ← delivery loop
  ├── goroutine: RunHeartbeat()         ← idle nudge (if configured)
  ├── goroutine: TickStatus()           ← 1s status bar refresh
  ├── goroutine: VT.PipeOutput()        ← read child output
  ├── goroutine: Daemon.acceptLoop()    ← accept socket connections
  │
  └── lifecycleLoop()                   ← blocks, handles exit/relaunch
```

### Child Process Arguments

For Claude Code, the actual command executed is:

```bash
claude --session-id <uuid> \
       --append-system-prompt "..." \
       --model <model> \
       --permission-mode <mode> \
       --allowedTools '["Read","Bash(git *)"]' \
       --disallowedTools '["Write"]'
```

The `--append-system-prompt` injects the role's instructions plus the h2 messaging protocol (how to handle `[h2 message from: X]` prefixes).

## Process Isolation

Each daemon is fully isolated:

| Aspect | Mechanism |
|--------|-----------|
| **Process group** | `Setsid=true` -- new session leader, survives parent exit |
| **Terminal** | Own PTY via `creack/pty` -- no shared terminal state |
| **Stdin/stdout/stderr** | All point to `/dev/null` -- daemon is headless |
| **Environment** | Filtered: `CLAUDECODE` stripped to prevent nested detection |
| **Working directory** | Role-defined: absolute path, relative to h2 dir, or git worktree |
| **Claude config** | Separate `CLAUDE_CONFIG_DIR` per role -- isolated settings, hooks, credentials |
| **IPC** | Own Unix socket in `~/.h2/sockets/` |
| **Session state** | Own directory in `~/.h2/sessions/` |

## Agent Control Operations

### `h2 list` -- Monitor All Agents

Queries every agent socket in `~/.h2/sockets/` with `{type: "status"}`:

```
Agents
  ● coder-1 (coding) claude — Active (tool use: Bash) 30s, up 2h, 45k $3.20
  ● coder-2 (coding) claude — Active (thinking) 5s, up 1h, 30k $2.10
  ○ reviewer (reviewer) claude — Idle 10m, up 3h, 20k $1.50

  Pod: dev-team
  ● concierge (concierge) claude — Active 2s, up 4h, 15k $1.00
```

State indicators:
- `●` green = active (or recently idle <2min)
- `○` yellow = idle
- `●` red = exited
- `✗` red = unresponsive (socket exists but no response)

### `h2 attach <name>` -- Connect Terminal

```
1. Dial agent.<name>.sock
2. Send {type: "attach", rows: 40, cols: 120}
3. Switch local terminal to raw mode
4. Start SIGWINCH handler → send resize control frames
5. Bidirectional I/O:
   stdin  → FrameTypeData  → daemon → client input handler
   daemon → FrameTypeData → stdout  (terminal output)
6. On disconnect: restore terminal, disable mouse
```

Multiple clients can attach simultaneously. All see the same terminal output. Only one can hold passthrough mode at a time.

### `h2 send <name> <message>` -- Deliver Message

```
1. Resolve sender identity (H2_ACTOR / git user / $USER)
2. Clean LLM escape artifacts from message body
3. Dial agent.<name>.sock
4. Send {type: "send", priority: "normal", from: "sender", body: "..."}
5. Daemon: PrepareMessage → write to ~/.h2/messages/<name>/<ts>-<id>.md
6. Daemon: Queue.Enqueue(msg) → delivery loop picks it up
7. Response includes MessageID for tracking
```

### `h2 peek <name>` -- View Recent Activity

Reads the Claude Code session log (JSONL) from the claude config directory and displays recent messages and tool uses as a quick activity summary without needing to attach.

### `h2 stop <name>` -- Graceful Shutdown

```
1. Dial agent.<name>.sock
2. Send {type: "stop"}
3. Daemon:
   a. Set Quit = true
   b. VT.KillChild() → SIGTERM to child process group
   c. Signal quitCh → unblocks lifecycleLoop
4. lifecycleLoop returns → cleanup:
   a. Build session summary (metrics, git stats, uptime)
   b. Log summary to activity log
   c. Stop Agent (collectors, HTTP server)
   d. Close socket, remove socket file
```

### `h2 status <name>` -- Detailed Status

Returns the full `AgentInfo` JSON: state, sub-state, tool counts, per-model metrics, git stats, queue depth, uptime, hook collector data.

## Lifecycle Management

### Child Exit and Relaunch

When the child process exits (normally or with error):

```
lifecycleLoop:
  VT.Cmd.Wait() returns
  │
  ├── Render exit message to all clients
  ├── Pause message queue
  │
  └── Wait for signal:
        │
        ├── relaunchCh (from client menu → "restart")
        │     ├── Re-exec same command with fresh PTY
        │     ├── Reset VT state
        │     ├── Resume message queue
        │     └── Continue loop
        │
        └── quitCh (from client menu → "quit" or h2 stop)
              └── Return → cleanup and exit daemon
```

### Heartbeat (Idle Nudge)

For agents configured with heartbeat:

```yaml
heartbeat:
  idle_timeout: "5m"
  message: "Check the beads board for new tasks"
  condition: "bd list --mine --status open | grep -q ."
```

Loop:
1. Wait for agent to become idle
2. Start idle timer (5 minutes)
3. If agent goes active during timer, restart
4. On timeout: run condition command
5. If condition succeeds: send idle-priority message
6. Repeat

### Permission Management

Two approaches configured per-role:

**Pattern matching** (via Claude Code's built-in system):
```yaml
permissions:
  allow: ["Read", "Bash(git *)", "Bash(make *)"]
  deny: ["Bash(rm -rf *)"]
```

**AI reviewer** (via h2's `permission-request` hook):
```yaml
permissions:
  agent:
    enabled: true
    instructions: |
      ALLOW standard dev commands. DENY destructive operations.
      ASK_USER if unsure about file modifications outside the project.
```

The AI reviewer runs Claude Haiku to evaluate each permission request, returning ALLOW, DENY, or ASK_USER. This happens within the `h2 permission-request` hook command, with decisions reported back to the daemon for state tracking.

## Pod Management

Pods launch coordinated groups of agents:

```bash
h2 pod launch dev-team --var project=myapp
```

Flow:
1. Load and render pod template YAML
2. Expand count groups (e.g., `count: 3` → 3 agents)
3. Check for already-running agents (skip with warning)
4. For each expanded agent:
   - Load role (pod-scoped, then global fallback)
   - Merge variables (template vars → CLI vars)
   - `setupAndForkAgent()` → daemon + socket
5. All agents start in parallel

```bash
h2 pod stop dev-team
```

Queries all agent sockets, filters by pod membership, sends stop to each.

## Environment Variable Contract

Every agent process runs with:

| Variable | Value | Purpose |
|----------|-------|---------|
| `H2_DIR` | h2 root path | Configuration directory |
| `H2_ACTOR` | agent name | Identity for messaging |
| `H2_ROLE` | role name | Role identity |
| `H2_POD` | pod name | Pod membership |
| `H2_SESSION_DIR` | session dir path | Permission reviewer, activity log |
| `CLAUDE_CONFIG_DIR` | config dir path | Claude Code config isolation |
| `CLAUDE_CODE_ENABLE_TELEMETRY` | `1` | Enable OTEL export |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://127.0.0.1:<port>` | h2's OTEL collector |

## Dry-Run Mode

`h2 run --dry-run` mirrors the full launch sequence without any side effects:

```
$ h2 run --role coding --name test-agent --dry-run

Agent Configuration (dry run)
  Name:             test-agent
  Role:             coding
  Command:          claude
  Model:            opus
  Working Dir:      /Users/me/project
  Claude Config:    ~/.h2/claude-config/default
  Session Dir:      ~/.h2/sessions/test-agent
  Permission Mode:  default

  Environment:
    H2_DIR=/Users/me/.h2
    H2_ACTOR=test-agent
    H2_ROLE=coding
    CLAUDE_CONFIG_DIR=/Users/me/.h2/claude-config/default

  Child Args:
    claude --session-id <uuid> --append-system-prompt "..." --model opus

  Instructions (first 10 lines):
    You are a coding agent...
```

Also works for pods: `h2 pod launch dev-team --dry-run` shows all expanded agents.

## QA Testing Framework

h2 includes a built-in QA system for automated testing of agent behavior:

```bash
h2 qa setup                    # Build Docker image
h2 qa auth                     # Authenticate Claude in container
h2 qa run integration-lifecycle # Run a test plan
h2 qa report                   # View results
```

The orchestrator runs Claude Code with `--permission-mode bypassPermissions` inside a Docker container (or sandboxed temp dir with `--no-docker`), executing a markdown test plan and producing structured pass/fail results.
