# Bridge and Telegram Integration

> `internal/bridge/`, `internal/bridgeservice/` -- External messaging bridges, the bridge daemon, and Telegram Bot API integration.

## Overview

The bridge subsystem connects h2's internal messaging to external platforms. It defines a capability-based interface hierarchy, implements Telegram long-polling and macOS native notifications, and runs as a separate daemon process that routes messages between agents and external services.

## Interface Hierarchy

```go
// Base -- every bridge must implement
type Bridge interface {
    Name() string
    Close() error
}

// Outbound capability
type Sender interface {
    Send(ctx context.Context, text string) error
}

// Inbound capability
type InboundHandler func(targetAgent string, body string)
type Receiver interface {
    Start(ctx context.Context, handler InboundHandler) error
    Stop()
}

// Optional enhancement
type TypingIndicator interface {
    SendTyping(ctx context.Context) error
}
```

| Bridge | Implements |
|--------|-----------|
| Telegram | Bridge + Sender + Receiver + TypingIndicator |
| macOS Notify | Bridge + Sender |

The capability interfaces allow runtime type assertion (`b.(bridge.Sender)`) without requiring bridges to implement capabilities they don't have.

## Bridge Service Daemon

The `BridgeService` runs as a separate daemon process (`h2 _bridge-service`) managing message routing.

```go
type Service struct {
    bridges    []bridge.Bridge
    concierge  string          // default message target
    socketDir  string
    user       string          // socket name + "from" field
    lastSender string          // reply routing
    // counters: messagesSent, messagesReceived, lastActivityTime
}
```

### Lifecycle (`Run`)

```
1. Create socket directory
2. Start all bridge.Receiver implementations
3. Create Unix socket: bridge.<user>.sock
4. Start acceptLoop goroutine
5. Start runTypingLoop goroutine
6. Block on ctx.Done()
7. Cleanup: stop receivers, close bridges, remove socket
```

### Message Routing

**Inbound (External → Agent):**

```
Telegram user message
  → telegram.poll() goroutine
  → ParseSlashCommand() → ExecCommand → reply       (if /command)
  → ParseAgentPrefix("coder-1: review the PR")      (if agent-prefixed)
  → ParseAgentTag("[coder-1] ...")                   (if replying to tagged msg)
  → handler(targetAgent, body)
  → Service.handleInbound()
  → resolveDefaultTarget()                           (if no explicit target)
  → sendToAgent() → dial agent.<name>.sock → {type: "send"}
```

**Outbound (Agent → External):**

```
Agent session → dial bridge.<user>.sock → {type: "send", from: "coder-1", body: "..."}
  → Service.handleOutbound()
  → FormatAgentTag("[coder-1] ...")                  (if not concierge)
  → telegram.Send() → SplitMessage(4096, 3 pages) → Telegram API
  → macos_notify.Send() → osascript                  (if enabled)
```

**Default Target Resolution:**

```
1. concierge (if configured)
2. lastSender (most recent outbound agent -- for reply context)
3. First agent socket found in directory
```

### Typing Indicator Loop

Every 4 seconds:
1. Resolve default target agent
2. Query agent state via `{type: "status"}` socket request
3. If state is `"active"`: send typing indicator to all `TypingIndicator` bridges

## Telegram Integration

### Long-Polling Architecture

```
telegram.Start(ctx, handler)
  └── go poll(ctx, handler)
        │
        ├── getUpdates(offset, timeout=30s)   ← Telegram long-poll
        │     └── POST /bot<token>/getUpdates
        │
        ├── Filter by ChatID (reject other senders)
        │
        ├── ParseSlashCommand → execAndReply  (if /command)
        │     └── ExecCommand → LookPath + 30s timeout
        │
        ├── ParseAgentPrefix / ParseAgentTag  (routing)
        │
        └── handler(agent, body)              → BridgeService.handleInbound
```

Key behaviors:
- **Offset tracking**: Each processed update advances the offset to prevent redelivery
- **Backoff**: On error, exponential backoff from 1s to 60s; resets on success
- **Chat ID filtering**: Only processes messages from the configured chat ID (security)
- **Reply routing**: When replying to a message tagged with `[agent-name]`, routes to that agent

### Message Sending

```go
func (t *Telegram) Send(ctx context.Context, text string) error {
    chunks := bridge.SplitMessage(text, 4096, 3)  // Telegram's 4096 char limit
    for _, chunk := range chunks {
        t.sendChunk(ctx, chunk)  // POST /bot<token>/sendMessage
    }
}
```

Message paging splits on newline boundaries when possible, hard-cuts at 4096 chars otherwise. Maximum 3 pages per message (truncates with `"... (truncated)"` on the last page).

### Slash Commands

Users can execute h2 commands from Telegram:

```
/h2 list              → runs "h2 list" on the host machine
/bd list              → runs "bd list" (beads-lite)
```

Allowed commands are configured per-user in `config.yaml` and validated against `[a-zA-Z0-9_-]+` to prevent injection. Commands are executed via `bridge.ExecCommand` which uses `exec.LookPath` + `shlex.Split` + 30-second timeout.

## macOS Notifications

```go
func (m *MacOSNotify) Send(ctx context.Context, text string) error {
    // osascript -e 'display notification "text" with title "h2"'
}
```

Send-only bridge. Text sanitized via `escapeAppleScript` (newlines → spaces). The `ExecCommand` field allows test injection.

## Configuration

```yaml
# config.yaml
users:
  myuser:
    bridges:
      telegram:
        bot_token: "123456:ABC..."
        chat_id: 987654321
        allowed_commands:
          - h2
          - bd
      macos_notify:
        enabled: true
```

`bridgeservice.FromConfig(cfg)` instantiates concrete bridge implementations from config types. This is the only coupling point between config and bridge implementations.

## Process Model

```
h2 bridge
  ├── bridgeservice.ForkBridge(user, concierge)
  │     └── exec: h2 _bridge-service --for myuser --concierge concierge
  │           ├── Setsid=true (detached)
  │           ├── stdout/stderr → ~/.h2/logs/bridge.log
  │           └── Polls for bridge.myuser.sock (5s timeout)
  │
  └── setupAndForkAgent(concierge)  (unless --no-concierge)
        └── h2 _daemon --name concierge --role concierge
```

## Socket Naming

```
~/.h2/sockets/
├── agent.concierge.sock
├── agent.coder-1.sock
├── agent.coder-2.sock
└── bridge.myuser.sock
```

On macOS, if path length exceeds 100 chars, a symlink from `/tmp/h2-<sha256[:8]>/` is created transparently.

## Data Flow Summary

```
                     ┌─────────────────────┐
                     │   Telegram Bot API    │
                     │   (long-poll 30s)     │
                     └─────────┬─────────────┘
                               │
                     ┌─────────▼─────────────┐
                     │  h2 _bridge-service    │
                     │                        │
                     │  bridge.myuser.sock    │
                     │                        │
              ┌──────┤  Routes messages:      ├──────┐
              │      │  inbound → agent sock  │      │
              │      │  outbound → Telegram   │      │
              │      │  typing → Telegram     │      │
              │      └────────────────────────┘      │
              │                                      │
    ┌─────────▼──────────┐             ┌─────────────▼──────────┐
    │  agent.concierge   │             │  agent.coder-1         │
    │       .sock        │◄───────────►│       .sock            │
    │                    │  h2 send    │                        │
    │  h2 _daemon        │             │  h2 _daemon            │
    │  └── claude ...    │             │  └── claude ...        │
    └────────────────────┘             └────────────────────────┘
```
