# Message System

> `internal/session/message/` -- Priority queue, delivery loop, wire protocol, and file-backed messages.

## Overview

The message system handles all communication to agents: user input from the h2 bar, inter-agent messages via `h2 send`, and bridge-forwarded messages from Telegram. Messages have four priority levels, are persisted to disk before delivery, and are written to the agent's PTY at appropriate moments based on agent state.

## Priority Levels

```
PriorityInterrupt (1) ─── highest: breaks through immediately
PriorityNormal    (2) ─── delivered at next natural pause
PriorityIdleFirst (3) ─── queued, delivered when idle (LIFO)
PriorityIdle      (4) ─── queued, delivered when idle (FIFO)
```

| Priority | Delivery Behavior | Queue Behavior |
|----------|-------------------|----------------|
| **Interrupt** | Sends Ctrl+C first, waits for idle, then delivers. Passes through even when paused or blocked. | FIFO |
| **Normal** | Delivered immediately if agent is idle or active (not blocked). Held back if blocked on permission. | FIFO |
| **IdleFirst** | Only delivered when agent state is Idle. Most recent message delivered first. | LIFO (prepend) |
| **Idle** | Only delivered when agent state is Idle. | FIFO |

## MessageQueue

```go
type MessageQueue struct {
    interrupt   []*Message  // FIFO
    normal      []*Message  // FIFO
    idleFirst   []*Message  // prepend (most recent first)
    idle        []*Message  // FIFO
    allMessages map[string]*Message  // by ID
    paused      bool
    notify      chan struct{}  // buffered(1), signaled on enqueue/unpause
}
```

### Operations

| Method | Behavior |
|--------|----------|
| `Enqueue(msg)` | Appends to correct sub-queue (idleFirst prepends). Signals `notify`. |
| `Dequeue(idle, blocked bool)` | Priority-ordered drain. Paused: only interrupt. Blocked: only interrupt. Idle: also drain idleFirst and idle. |
| `Pause()` / `Unpause()` | Controls delivery. Paused during passthrough mode. |
| `Notify()` | Returns the notification channel for the delivery loop. |
| `FindByID(id)` | Looks up any message by its UUID. |
| `Count()` | Total pending messages across all sub-queues. |

### Dequeue Priority Order

```
1. interrupt queue  (always drained, even when paused)
2. normal queue     (skipped when paused or blocked)
3. idleFirst queue  (only when idle=true and not paused)
4. idle queue       (only when idle=true and not paused)
```

## Message Struct

```go
type Message struct {
    ID        string           // UUID
    From      string           // sender name
    Priority  Priority
    Body      string           // message text
    FilePath  string           // path to persisted file
    Raw       bool             // skip formatting, write directly to PTY
    Status    MessageStatus    // StatusQueued | StatusDelivered
    CreatedAt time.Time
    DeliveredAt *time.Time
}
```

## Delivery Loop (`RunDelivery`)

```go
type DeliveryConfig struct {
    Queue     *MessageQueue
    Writer    io.Writer         // PTY master fd
    IsIdle    func() bool       // Agent.State() == Idle
    IsBlocked func() bool       // SubState == WaitingForPermission
    OnDeliver func()            // re-render status bars
    Stop      <-chan struct{}    // shutdown signal
}
```

The delivery goroutine runs a select loop:

```
for {
    select {
    case <-queue.Notify():    // new message or unpause
    case <-ticker.C:          // 1-second poll
    case <-stop:              // shutdown
        return
    }

    for msg := queue.Dequeue(idle, blocked); msg != nil; ... {
        deliver(cfg, msg)
    }
}
```

### Per-Message Delivery Logic

**Interrupt (non-raw):**
1. Write `0x03` (Ctrl+C) to PTY
2. Call `NoteInterrupt()` on the agent
3. Wait up to 5 seconds for agent to become idle
4. Retry up to 3 times if agent doesn't go idle
5. Then deliver the formatted message

**Raw messages:**
- Write body directly to PTY (no formatting, no Ctrl+C)
- Used for permission prompt responses

**Inter-agent messages:**
- Short body (<=300 chars): `[h2 message from: sender] body\r`
- Long body (>300 chars): `[h2 message from: sender] Read /path/to/file\r`
- 50ms delay before Enter to ensure the agent's input buffer is ready

On delivery: marks `StatusDelivered`, sets `DeliveredAt`, calls `OnDeliver` callback.

## Message Persistence (`PrepareMessage`)

Before enqueueing, `PrepareMessage` writes the message body to disk:

```
~/.h2/messages/<agentName>/<timestamp>-<id8>.md
```

Where `<id8>` is the first 8 characters of the UUID. This provides:
- An audit trail of all inter-agent messages
- A file path for long messages (agent reads the file instead of receiving the full text inline)
- A delivery receipt mechanism via `h2 show <message-id>`

## Wire Protocol

### JSON Protocol (control operations)

All inter-process communication uses JSON-encoded request/response over Unix sockets:

```go
type Request struct {
    Type      string  // "send" | "attach" | "show" | "status" | "hook_event" | "stop"
    Priority  string
    From, Body string
    Raw       bool
    Cols, Rows int
    MessageID  string
    EventName  string
    Payload    json.RawMessage
}

type Response struct {
    OK        bool
    Error     string
    MessageID string
    Message   *MessageInfo
    Agent     *AgentInfo
    Bridge    *BridgeInfo
}
```

### Binary Framing (attach mode)

After the initial JSON handshake, the attach protocol switches to binary framing:

```
[1 byte type][4 bytes big-endian length][payload bytes]
```

| Frame Type | Value | Purpose |
|-----------|-------|---------|
| `FrameTypeData` | 0x00 | Keyboard input or terminal output |
| `FrameTypeControl` | 0x01 | JSON control messages (e.g., `{"type":"resize","cols":120,"rows":40}`) |

Payload limit: 10MB per frame. `WriteFrame`/`ReadFrame` use `io.ReadFull` for guaranteed complete reads.

## Message Flow Diagrams

### User Input (Normal Priority)

```
User types in h2 bar → Enter
  → Client.HandleDefaultBytes
  → VT.WritePTY(input + "\r")  [direct to PTY, no queue]
```

### User Input (Non-Normal Priority)

```
User types in h2 bar → Tab (change priority) → Enter
  → Client.OnSubmit(text, priority)
  → Session.SubmitInput(text, priority)
  → message.Message{ID: uuid, Body: text}
  → Queue.Enqueue(msg)
  → Queue.notify channel signaled
  → RunDelivery picks it up
  → deliver(msg) → write to PTY
```

### Inter-Agent Message

```
h2 send coder-1 "Review the auth module"
  → dial agent.coder-1.sock
  → Request{Type: "send", Priority: "normal", From: "concierge", Body: "..."}
  → Daemon.handleSend()
  → PrepareMessage() → write to ~/.h2/messages/coder-1/ts-id.md
  → Queue.Enqueue(msg)
  → RunDelivery
  → deliver(msg) → write "[h2 message from: concierge] Review the auth module\r" to PTY
```

### Bridge-Forwarded Message (from Telegram)

```
Telegram user types: "How's the auth module?"
  → BridgeService.handleInbound("", body)
  → resolveDefaultTarget() → "concierge"
  → sendToAgent("concierge", "bridge-user", body)
  → dial agent.concierge.sock
  → Request{Type: "send", Priority: "normal", ...}
  → ... same delivery path as inter-agent
```
