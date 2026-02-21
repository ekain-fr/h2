# Orchestration Comparison: OpenClaw vs h2

## Executive Summary

This report provides a detailed comparison of two agent orchestration solutions:
**OpenClaw** and **h2**. It evaluates their architectures across communication
channels, agent runner design, queue management, and language choice, with scored
criteria and recommendations for building a hybrid orchestration system.

| Dimension | OpenClaw | h2 |
| ----------- | -------- | ----- |
| Language | TypeScript ESM (Node 22+, pnpm monorepo) | Go 1.24 (~36,800 LOC, ~85 files) |
| Scope | Multi-channel AI gateway + personal assistant | Agent runner + messaging + orchestration layer |
| Agent interface | In-process RPC via Pi Agent library | PTY harness wrapping any TUI agent |
| API keys | Required (Anthropic, OpenAI, Gemini, etc.) | Optional -- works via CLI subscription plans |
| Deployment | Local, Remote (SSH/WS), Cloud (Fly.io), Docker | Local daemon processes |
| Channels | 42+ messaging platforms | Telegram + macOS notifications |

---

## 1. Communication Channels

### 1.1 OpenClaw

OpenClaw manages bidirectional communication through a **plugin-based channel
architecture** with a central WebSocket+HTTP gateway (port 18789).

**Inbound path**: Platform SDK (Carbon/grammY/etc.) receives message ->
channel plugin normalizes -> gateway routes via session key -> followup queue ->
command queue -> agent runner.

**Outbound path**: Agent produces response -> event streaming (real-time) ->
reply delivery pipeline -> chunking
(Discord: 2000 chars, Telegram: 4096 chars) -> channel plugin delivers via
platform API.

**Key mechanisms**:

- **WebSocket protocol** (v1) with challenge-based auth handshake,
  30s heartbeat, 60s timeout
- **Session key system**: structured format
  `agent:{agentId}:{mainKey | channel:peerKind:peerId[:threadId]}`
- **Draft streaming**: live message preview editing
  (throttled 1000-1200ms per platform)
- **Follow-up routing**: "last route" stored per session to route responses
  back to originating channel/thread
- **ACP (Agent Client Protocol)**: bidirectional ndjson over stdio for
  IDE/external integration
- **Event system**: broadcast events (`tick`, `chat.event`, `channel.*`, etc.)
  with `dropIfSlow` flag

**Strengths**: Massive platform coverage (42+ channels), sophisticated
per-platform adapters with caching/rate-limiting/chunking, real-time streaming
previews, rich media support (voice, polls, stickers, embeds).

### 1.2 h2

h2 manages bidirectional communication through **5 distinct channels**, each
purpose-built for a specific communication need.

**Channel 1 -- PTY** (primary): Raw byte I/O on the pseudoterminal. h2 writes
formatted messages to stdin (`[h2 message from: X] body\r`), reads VT100 output
from stdout. Write timeout protection (3s) prevents deadlocks.

**Channel 2 -- OTEL**: Agent's built-in OpenTelemetry exporter redirected to
h2's local HTTP server (`/v1/logs`, `/v1/metrics`). Extracts tokens, cost,
tool names. 1-5s export interval.

**Channel 3 -- Hook Events**: Claude Code lifecycle hooks (`PreToolUse`,
`PostToolUse`, `Stop`, etc.) execute `h2 hook collect` which reports events via
Unix socket. Drives precise state detection.

**Channel 4 -- Unix Socket IPC**: JSON request/response for control operations
(`send`, `status`, `attach`, `stop`, `hook_event`). Binary framing for terminal
streaming (`[1 byte type][4 bytes length][payload]`).

**Channel 5 -- External Bridges**: Telegram long-polling + macOS notifications
via bridge daemon process.

**Strengths**: Harness-agnostic (wraps any TUI), multi-channel state
triangulation (hooks > OTEL > output timing), multi-client terminal attach
(shared VT buffer), extremely low-latency IPC via Unix sockets (<1ms).

### 1.3 Communication Channels Comparison

| Criterion | OpenClaw | h2 |
| ----------- | -------- | ----- |
| Platform coverage | 42+ channels | 2 (Telegram + macOS notif) |
| Agent interface | In-process RPC (tight coupling) | PTY stdin/stdout (zero coupling) |
| State detection | Explicit via Pi Agent events | Triangulated: hooks > OTEL > output |
| Real-time streaming | Draft message editing (1000-1200ms) | Raw VT100 terminal output |
| Multi-client | WebSocket broadcast to clients | Unix socket binary framing, N clients on shared VT |
| Latency | WebSocket + HTTP (~10-50ms) | Unix socket (<1ms for IPC), PTY (immediate) |
| Protocol complexity | High (WS handshake, auth, heartbeat) | Low (JSON req/resp, binary framing) |

### 1.4 Communication Channels Scores

| Criterion | OpenClaw (0-10) | h2 (0-10) | Notes |
| ----------- | :---------------: | :---------: | ------- |
| Platform breadth | **10** | 3 | OpenClaw: 42+ platforms. h2: Telegram + macOS notif |
| Agent decoupling | 4 | **10** | h2 wraps any TUI; OpenClaw requires Pi Agent library |
| Latency | 6 | **9** | h2: Unix sockets <1ms. OpenClaw: WebSocket overhead |
| State detection | 7 | **9** | h2: 3-source triangulation. OpenClaw: explicit events |
| Streaming UX | **9** | 6 | OpenClaw: draft editing per platform. h2: raw VT output |

---

## 2. Agent Runner

### 2.1 OpenClaw

Agents run as **in-process modules** via the Pi Agent library
(`@mariozechner/pi-agent-core`). The `runEmbeddedPiAgent()` function is
the main entry point.

**Lifecycle**:

1. **Launch**: Two-layer lane enqueue (session lane -> global lane)
2. **Init**: Resolve model, select auth profile, build system prompt
   (identity + skills + context + tools), load session file
3. **Execution**: Pi Agent RPC handles LLM API calls, tool routing,
   conversation management
4. **Streaming**: Real-time events (`agent_message_chunk`, `tool_call`,
   `tool_result`, `usage`, `message_end`)
5. **Completion**: Collect payloads, record messaging sends, track cron
   additions, clear active run

**Active Run Registry**: Global
`Map<sessionId, {queueMessage, isStreaming, isCompacting, abort}>`
for tracking.

**Process Supervisor**: Two modes for tool execution --
`child_process.spawn()` for non-interactive commands,
`node-pty` for interactive terminals.
Kill tree: SIGTERM -> wait -> SIGKILL (Unix), taskkill (Windows).

**Subagent System**: `spawnSubagentDirect()` with max depth tracking,
max concurrent limit, auto-announce pattern (child results delivered as
user messages to parent).

**Failover**: Model failover chain, auth profile rotation with cooldowns,
context window guard with auto-compaction.

**Sandbox**: Docker container per session with workspace mount control,
network isolation, tool policy enforcement.

### 2.2 h2

Agents run as **independent daemon processes**, each in its own PTY. The
`h2 _daemon` subprocess is the single source of truth for a session.

**Lifecycle**:

1. **Configuration**: `ResolveDir()` -> `LoadRoleRendered()` ->
   `ApplyOverrides()`
2. **Setup**: Create session dir, bootstrap Claude config with h2 hooks,
   create git worktree (optional)
3. **Fork**: `ForkDaemon()` -- re-exec `h2 _daemon` with `Setsid=true`,
   fully detached. Poll for socket (5s timeout)
4. **Daemon Init**: Create Unix socket, start collectors
   (Output + OTEL + Hook), start PTY, start delivery loop + heartbeat +
   status tick + output pipe + socket listener
5. **Lifecycle Loop**: Block on child exit, then wait for relaunch or
   quit signal

**Process Isolation**:

- Own process group (`Setsid=true`)
- Own PTY (`creack/pty`)
- Own stdin/stdout/stderr -> `/dev/null`
- Separate Claude config dir per role
- Own Unix socket, session state, environment

**Control Operations**: `h2 list` (monitor all), `h2 attach`
(connect terminal), `h2 send` (deliver message), `h2 peek`
(view activity), `h2 stop` (graceful shutdown), `h2 status`
(detailed info).

**Pod Management**: Launch coordinated groups of agents with templates,
variable expansion, count groups.

**Heartbeat**: Configurable idle nudge with condition checking
(shell command that must succeed before sending).

### 2.3 Agent Runner Comparison

| Criterion | OpenClaw | h2 |
| ----------- | -------- | ----- |
| Isolation model | In-process (shared Node.js event loop) | OS-level (separate daemon per agent) |
| Agent coupling | Tight (Pi Agent library) | Zero (any TUI via PTY) |
| Multi-model | Yes (Anthropic, OpenAI, Gemini, local) | Inherited from wrapped agent |
| Failover | Built-in model chain + key rotation | N/A (delegates to agent) |
| Sandbox | Docker per session | N/A (QA system uses Docker) |
| Subagents | Native (depth tracking, auto-announce) | Via `h2 send` inter-agent messaging |
| Group launch | N/A | Pod templates with count expansion |
| Process recovery | Lane reset via SIGUSR1 | Lifecycle loop with relaunch channel |
| Observability | Event streaming + active run registry | `h2 list/status/peek` + OTEL metrics |
| Dry-run | N/A | Full `--dry-run` mode showing config |

### 2.4 Agent Runner Scores

| Criterion | OpenClaw (0-10) | h2 (0-10) | Notes |
| ----------- | :---------------: | :---------: | ------- |
| Process isolation | 5 | **10** | h2: full OS-level isolation. OpenClaw: shared process |
| Multi-model | **9** | 5 | OpenClaw: native multi-provider + failover |
| Subagent orchestration | **8** | 6 | OpenClaw: native spawn + depth tracking |
| Group management | 4 | **9** | h2: pod templates with count expansion |
| Observability | 7 | **8** | h2: rich status/list/peek/OTEL |
| Recoverability | 7 | **8** | h2: lifecycle loop with relaunch |

---

## 3. Queue Management

### 3.1 OpenClaw

A **3-layer queue architecture**:

**Layer 1 -- Command Lanes** (`command-queue.ts`):

- Lane state: queue (FIFO), activeTaskIds, maxConcurrent, generation counter
- Predefined lanes: `main` (configurable, typically 4), `cron` (1),
  `subagent` (8), `session:{key}` (1)
- Two-layer nesting: session lane (1 concurrent) -> global lane (capped)
- Pump mechanism: drain while `activeTaskIds.size < maxConcurrent`
- Lane reset (SIGUSR1): increment generation to invalidate stale completions,
  preserve queued entries

**Layer 2 -- Followup Runs Queue** (`auto-reply/reply/queue/`):

- 6 modes: `steer` (inject into current run), `followup` (queue for next turn),
  `collect` (coalesce into single prompt), `steer-backlog` (steer + preserve),
  `interrupt` (abort previous), `queue` (legacy)
- Settings resolution: inline directive > session > per-channel > plugin >
  global > built-in defaults
- Message deduplication: by message-id, prompt, or none
- Overflow policy: `old` (drop oldest), `new` (reject newest),
  `summarize` (drop + inject summary)
- Collect mode: batch same-channel messages into
  `[Queued messages while agent was busy]` prompt
- User-facing `/queue` directives for runtime control

**Layer 3 -- Outbound Delivery Queue** (`delivery-queue.ts`):

- Persistent to disk before send attempts
- Retry: backoff [5s, 25s, 120s, 600s], max 5 retries
- Ensures no message loss even on crashes

**Rate limiting**: Control plane rate limit (3 req/60s per client).

### 3.2 h2

A **single priority queue** with state-aware delivery:

**MessageQueue**:

- 4 priority sub-queues: `interrupt` (FIFO), `normal` (FIFO),
  `idleFirst` (LIFO/prepend), `idle` (FIFO)
- `notify` channel (buffered 1) signaled on enqueue/unpause
- `paused` flag during passthrough mode

**Dequeue priority order**: interrupt (always, even when paused) ->
normal (skip when paused/blocked) -> idleFirst (only when idle) ->
idle (only when idle).

**Delivery loop**: select on `notify` channel, 1s ticker poll, or
stop signal. Per-message delivery:

- Interrupt: write `0x03` (Ctrl+C), wait up to 5s for idle, retry 3x,
  then deliver
- Raw: direct PTY write (permission responses)
- Inter-agent: formatted `[h2 message from: sender] body\r`, long messages
  delivered as file path references

**Message persistence**: Written to
`~/.h2/messages/<agentName>/<ts>-<id>.md` before enqueueing.

**State-aware delivery**: Delivery decisions based on agent state --
Active + not blocked delivers normal + interrupt;
Active + blocked on permission delivers only interrupt;
Idle delivers all including idle/idle-first;
Paused (passthrough) delivers only interrupt.

### 3.3 Queue Management Comparison

| Criterion | OpenClaw | h2 |
| ----------- | -------- | ----- |
| Queue complexity | 3-layer (followup + command lanes + outbound) | 1-layer with 4 priority sub-queues |
| Concurrency control | Lane-based: per-session (1) + global cap | Single agent per daemon (inherent isolation) |
| Message coalescing | Collect mode batches into single prompt | No coalescing (FIFO per priority) |
| Smart injection | Steer mode injects into active run | Interrupt mode sends Ctrl+C + wait |
| Overflow handling | Cap + 3 policies (old/new/summarize) | No cap (unbounded queue) |
| Deduplication | By message-id, prompt, or none | No deduplication |
| Outbound persistence | Disk-persisted with retry (5x backoff) | N/A (PTY write is immediate) |
| Runtime reconfiguration | `/queue` directives, per-channel overrides | Priority selection per message |
| State awareness | Streaming/compacting state drives queue | 4-state agent model drives delivery timing |
| Debouncing | Configurable per-channel debounce (1-2s) | No debouncing |

### 3.4 Queue Management Scores

| Criterion | OpenClaw (0-10) | h2 (0-10) | Notes |
| ----------- | :---------------: | :---------: | ------- |
| Sophistication | **10** | 5 | OpenClaw: 6 modes, coalescing, dedup, overflow |
| Reliability | **9** | 6 | OpenClaw: disk-persisted outbound + retry |
| Concurrency control | **9** | 7 | OpenClaw: configurable lane caps |
| Simplicity | 4 | **9** | h2: ~550 LOC, 4 priorities, clear semantics |

---

## 4. Language Choice: TypeScript vs Go

### 4.1 Concurrency Model

**Go (h2)** -- Goroutines + channels are a natural fit for this domain. Every
h2 daemon runs ~6 concurrent goroutines (delivery loop, heartbeat, status tick,
output pipe, socket listener, lifecycle loop) and the code reads linearly
despite heavy concurrency. The `select` statement in the delivery loop is
idiomatic:

```go
select {
case <-queue.Notify():    // new message
case <-ticker.C:          // 1s poll
case <-stop:              // shutdown
}
```

No callback nesting, no promise chains. The `MessageQueue.notify` channel
(buffered 1) is a zero-allocation signaling primitive that replaces what would
be an EventEmitter + listener pattern in Node.

**TypeScript (OpenClaw)** -- Node.js's single-threaded event loop means the
entire gateway, all 42 channel adapters, the agent runner, and all queue layers
share one thread. The Pi Agent runs in-process as an RPC module, so a
CPU-intensive operation (prompt building, token counting, context compaction)
blocks everything else. OpenClaw compensates with `child_process.spawn()` and
`node-pty` for tool execution, but the core orchestration loop is fundamentally
single-threaded. The async/await sugar hides this well in source code, but the
lane queue's pump mechanism and followup queue drain are doing cooperative
multitasking, not true parallelism.

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| True parallelism | Yes (goroutines on OS threads) | No (single event loop) |
| Concurrency primitives | Channels, select, sync.Mutex | Promises, EventEmitter, async/await |
| Blocking I/O risk | Isolated per goroutine | Blocks entire process |
| Mental model | CSP (communicating sequential processes) | Callback/continuation-based |

### 4.2 Process and OS Integration

**Go (h2)** -- Direct syscall access makes h2's daemon model possible with
minimal friction. `Setsid: true` for process detachment, `creack/pty` for PTY
allocation, signal handling (`SIGTERM`, `SIGWINCH`), Unix domain sockets --
all first-class. The `ForkDaemon()` implementation is ~30 lines. Writing a
binary frame protocol over Unix sockets is straightforward with `io.ReadFull`
and explicit byte slicing.

**TypeScript (OpenClaw)** -- Node.js requires wrappers for everything OS-level.
`node-pty` is a native addon (C++ binding to forkpty/openpty) that needs
compilation per platform. `child_process.spawn()` doesn't support `setsid`
directly. The WebSocket + HTTP server model works well but it's a higher-level
abstraction than Unix sockets. The `kill-tree.ts` implementation needs
platform-specific branches (Unix SIGTERM/-pid vs Windows taskkill) and can't
use process groups as cleanly as Go.

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| PTY management | Native (`creack/pty`, ~5 lines) | Native addon (`node-pty`, build step) |
| Daemon forking | `Setsid: true` in SysProcAttr | Not natively supported |
| Signal handling | `os/signal.Notify()` | `process.on('SIGTERM')` (limited) |
| Unix sockets | `net.Listen("unix", path)` | `net.createServer()` (works but verbose) |
| Binary protocols | `io.ReadFull`, byte slices | `Buffer`, manual framing |

### 4.3 Memory and Performance

**Go (h2)** -- Compiled to native binary. Each h2 daemon process uses
~15-30MB RSS. Goroutine stack starts at 8KB and grows on demand. The
4096-byte PTY read buffer is stack-allocated. No GC pauses visible at
this scale. The `midterm` VT100 emulator is pure Go with no allocations
on the hot path.

**TypeScript (OpenClaw)** -- V8 runtime overhead means the gateway process
likely uses 100-300MB+ RSS. Every channel adapter, queue state, session store,
and active run lives in the same heap. The 5,000-session ACP store with 24h
TTL represents significant memory pressure. V8's garbage collector can
introduce latency spikes during major collections, which matters for real-time
streaming (draft message editing at 1000ms throttle leaves little headroom).

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| Base memory per agent | ~15-30MB | N/A (shared process, 100-300MB+ total) |
| Startup time | ~50ms | ~500ms-2s (V8 init + module loading) |
| GC impact | Minimal at this scale | Potential latency spikes on large heaps |
| Binary size | ~15-20MB single binary | Entire `node_modules` tree |

### 4.4 Distribution and Deployment

**Go (h2)** -- Single static binary. `go build` produces one file that runs
on the target OS with zero dependencies. No runtime to install, no package
manager, no node_modules. Cross-compilation is trivial
(`GOOS=linux GOARCH=arm64 go build`). This matters enormously for an agent
runner that deploys on developer machines.

**TypeScript (OpenClaw)** -- Requires Node.js 22+, pnpm, native addon
compilation (node-pty, possibly others). The monorepo structure with `apps/`,
`extensions/`, `packages/`, `skills/`, `src/`, `ui/` means deployment involves
coordinating many packages. Docker and Fly.io deployment configs exist but
they bundle the full runtime. Native companion apps (Android, iOS, macOS) add
further build complexity.

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| Runtime dependency | None | Node.js 22+ |
| Distribution artifact | Single binary | Package tree + node_modules |
| Cross-compilation | Built-in (`GOOS/GOARCH`) | Platform-specific native addons |
| Install friction | Download and run | `pnpm install` + native builds |

### 4.5 Type System and Refactoring Safety

**Go (h2)** -- Structural typing with interfaces, but limited generics
(Go 1.18+). The `bridge.Bridge` / `bridge.Sender` / `bridge.Receiver`
interface hierarchy shows idiomatic Go: small interfaces, runtime type
assertion (`b.(bridge.Sender)`). The downside is that complex data structures
like `AgentInfo` (20+ fields) or `Request` (union-like struct with many
optional fields) lack discriminated union support -- you rely on conventions
(check `Type` field) rather than compiler enforcement.

**TypeScript (OpenClaw)** -- TypeScript's type system is significantly more
expressive for this domain. Discriminated unions
(`QueueMode = "steer" | "followup" | "collect" | ...`), intersection types,
mapped types, and generics make the complex queue state machine, lane
configuration, and multi-provider model system type-safe at compile time.
The `RunEmbeddedPiAgentParams` interface (30+ fields with precise typing)
and `EmbeddedPiRunResult` benefit from structural typing with optional fields.
The 6-level settings resolution chain for queue configuration would be much
harder to type correctly in Go.

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| Union types | None (conventions) | Discriminated unions, literal types |
| Generics | Limited (Go 1.18+) | Full generics + mapped types |
| Null safety | Zero values (footgun) | `strictNullChecks`, optional chaining |
| Refactoring confidence | Good (compiler catches most) | Better (richer type constraints) |
| Complex state modeling | Adequate but verbose | Excellent |

### 4.6 Error Handling

**Go (h2)** -- Explicit `error` returns force handling at every call site.
The h2 codebase shows this clearly: every socket operation, every PTY write,
every file persistence returns `error`. The downside is verbose boilerplate
(`if err != nil { return err }`), but it makes failure paths visible. No
hidden exceptions, no unhandled promise rejections.

**TypeScript (OpenClaw)** -- Exception-based with try/catch. OpenClaw's
channel adapters show sophisticated error handling: custom `DiscordApiError`
with `retryAfter`, grammY throttler with automatic retry, model failover
chains, context window guards. But exceptions can propagate silently through
async chains. The outbound delivery queue's retry logic and the followup
queue's drain process have complex error paths that are harder to trace than
Go's explicit returns.

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| Failure visibility | Every call site (explicit) | Hidden in async chains (implicit) |
| Error composition | `fmt.Errorf("context: %w", err)` | `new Error("msg", { cause })` |
| Recovery patterns | `defer` + explicit cleanup | try/catch/finally, `.catch()` |
| Unhandled failures | Compiler warns on unused errors | Silent promise rejections possible |

### 4.7 Ecosystem and Velocity

**Go (h2)** -- Smaller ecosystem but extremely stable dependencies. h2 uses
9 external deps, all mature. The standard library covers HTTP servers, JSON,
crypto, testing. Downside: fewer ready-made integrations for messaging
platforms -- h2 implements Telegram from scratch with raw HTTP calls to the
Bot API.

**TypeScript (OpenClaw)** -- npm's massive ecosystem is why OpenClaw can
support 42+ channels. Carbon (Discord), grammY (Telegram), and dozens of
other platform SDKs are available off-the-shelf. The Pi Agent library
provides the entire LLM interaction layer. The Lit framework powers the
control UI. Downside: deep dependency trees, native addon fragility, and
the constant churn of the JS ecosystem.

| Aspect | Go (h2) | TypeScript (OpenClaw) |
| -------- | --------- | ---------------------- |
| Platform SDK availability | Limited (manual API calls) | Extensive (npm packages) |
| Dependency stability | Very high (9 deps, all mature) | Moderate (deep trees, churn) |
| Feature velocity | Slower (build more yourself) | Faster (compose existing packages) |
| Supply chain risk | Low | Higher (larger attack surface) |

### 4.8 Language Choice Scores

| Criterion | Go (0-10) | TypeScript (0-10) | Notes |
| ----------- | :---------: | :-----------------: | ------- |
| Concurrency model | **9** | 5 | Goroutines + channels vs single event loop |
| Process and OS integration | **10** | 5 | Direct syscalls vs native addon wrappers |
| Memory and performance | **9** | 5 | ~15-30MB per agent vs 100-300MB+ shared |
| Distribution and deployment | **10** | 4 | Single binary vs Node.js + node_modules |
| Type system and refactoring | 6 | **9** | Limited unions vs discriminated unions |
| Error handling | **8** | 6 | Explicit returns vs exception propagation |
| Ecosystem and velocity | 6 | **8** | 9 stable deps vs npm ecosystem breadth |
| **Average** | **8.3** | **6.0** | |

---

## 5. Overall Scorecard

| Criterion | OpenClaw (0-10) | h2 (0-10) | Notes |
| ----------- | :---------------: | :---------: | ------- |
| Comm: Platform breadth | **10** | 3 | 42+ channels vs 2 |
| Comm: Agent decoupling | 4 | **10** | PTY harness vs in-process RPC |
| Comm: Latency | 6 | **9** | Unix sockets <1ms vs WebSocket |
| Comm: State detection | 7 | **9** | 3-source triangulation vs explicit events |
| Comm: Streaming UX | **9** | 6 | Draft editing vs raw VT output |
| Runner: Process isolation | 5 | **10** | OS-level daemon vs shared event loop |
| Runner: Multi-model | **9** | 5 | Native multi-provider + failover |
| Runner: Subagent orchestration | **8** | 6 | Native spawn vs inter-agent messaging |
| Runner: Group management | 4 | **9** | Pod templates with count expansion |
| Runner: Observability | 7 | **8** | Rich status/list/peek/OTEL |
| Runner: Recoverability | 7 | **8** | Lifecycle loop with relaunch |
| Queue: Sophistication | **10** | 5 | 6 modes, coalescing, dedup, overflow |
| Queue: Reliability | **9** | 6 | Disk-persisted outbound + retry |
| Queue: Concurrency control | **9** | 7 | Configurable lane caps |
| Queue: Simplicity | 4 | **9** | ~550 LOC, 4 priorities, clear semantics |
| Overall: Code complexity | 4 | **9** | 36K LOC Go vs 2,165-file TS monorepo |
| Overall: Extensibility | **9** | 5 | Plugin architecture, 42 extensions |
| Overall: Security model | **8** | 6 | Auth modes, pairing, TLS/mTLS |
| Language: Concurrency | 6 | **9** | Goroutines vs single thread |
| Language: OS integration | 5 | **10** | Direct syscalls vs wrappers |
| Language: Performance | 5 | **9** | Compiled binary vs V8 runtime |
| Language: Distribution | 4 | **10** | Single binary vs node_modules |
| Language: Type system | **9** | 6 | Discriminated unions vs conventions |
| Language: Ecosystem | **8** | 6 | npm breadth vs Go stability |

---

## 6. Alternative: tmux as Agent Harness

### 6.1 What tmux Gives You for Free

tmux already solves several problems that h2 implements from scratch:

| Capability | h2 (custom) | tmux (built-in) |
| ---------- | ----------- | --------------- |
| Session detach/attach | `ForkDaemon` + `Setsid` + socket handshake | `tmux new -d` / `tmux attach` |
| PTY management | `creack/pty` + `midterm` VT emulation | Native, battle-tested |
| Multi-client attach | Binary framing protocol over Unix socket | `tmux attach` from N terminals |
| Scrollback | `VT.Scrollback.Write` (custom buffer) | Built-in copy mode |
| Window resize | SIGWINCH handler + control frame | Automatic |
| Session listing | `h2 list` queries all sockets | `tmux list-sessions` |
| Message injection | `VT.WritePTY` with formatting | `tmux send-keys` |
| Process survival | Daemon with `/dev/null` stdio | tmux server persists |

A naive tmux-based agent runner would be approximately 200 lines:

```bash
# Launch
tmux new-session -d -s coder-1 "claude --session-id $UUID ..."

# Send message
tmux send-keys -t coder-1 "[h2 message from: user] Review auth" Enter

# Attach
tmux attach -t coder-1

# Read output
tmux capture-pane -t coder-1 -p

# Stop
tmux send-keys -t coder-1 C-c
```

### 6.2 What tmux Cannot Do

The critical insight is that h2 is not primarily a terminal multiplexer --
the PTY is just **channel 1 of 5**. tmux does not cover the following.

**Priority-aware message queue.** h2's queue has 4 priority levels with
state-aware delivery logic. Interrupt messages send Ctrl+C, wait up to
5s for idle, and retry 3x before delivering. Normal messages are held
back when the agent is blocked on permission. IdleFirst and Idle messages
are only delivered when the agent reaches idle state. `tmux send-keys` is
fire-and-forget -- no queuing, no priority, no hold-back, no retry. If
the agent is mid-tool-execution and you `send-keys`, the text lands in
the input buffer at a random point, possibly corrupting a tool call or
being swallowed entirely.

**State detection.** h2 triangulates agent state from 3 sources
(hooks > OTEL > output timing) to determine whether the agent is Active,
Idle, blocked on permission, or using a specific tool. This drives
message delivery timing, status bar rendering, Telegram typing
indicators, and heartbeat nudge triggers. tmux has no concept of what the
child process is doing. `capture-pane` gives static screen contents --
you would need to poll and parse VT100 escape sequences to infer state,
which is fragile and high-latency compared to h2's hook-driven approach
(~100ms per event).

**OTEL telemetry collection.** h2 redirects the agent's OpenTelemetry
exporter to a local HTTP server, extracting tokens, cost, tool names,
and per-model metrics in real time. This is an out-of-band channel that
operates independently of the PTY. tmux has no mechanism for this.

**Hook event forwarding.** h2 registers Claude Code hooks (`PreToolUse`,
`PostToolUse`, `PermissionRequest`, etc.) that execute `h2 hook collect`,
reporting events back to the daemon via Unix socket. This enables precise
state machine transitions, AI-powered permission review (launching Claude
Haiku to evaluate tool requests), and tool use counting. tmux cannot
intercept or inject hook infrastructure into the child process's
environment.

**Structured inter-agent messaging.** h2 persists messages to disk
(`~/.h2/messages/<agent>/<ts>-<id>.md`), assigns UUIDs, tracks delivery
status, and routes long messages as file references. `tmux send-keys`
has no persistence, no delivery confirmation, and no message ID for
tracking.

**Write timeout protection.** h2's `WritePTY` runs writes in a goroutine
with a 3-second timeout. If the child's stdin buffer is full (e.g.,
during context compaction), the write times out and the child is killed
rather than deadlocking the daemon. `tmux send-keys` will block
indefinitely if the PTY buffer is full.

### 6.3 Where tmux Has a Genuine Advantage

tmux excels at **crash resilience**. When you `h2 attach`, you use h2's
custom binary framing protocol. If h2's daemon crashes or has a bug in
its attach handler, you lose access to the agent. With tmux, the tmux
server is a separate, mature process -- even if your orchestration layer
crashes, the agent session survives and you can `tmux attach` directly.

A hybrid approach is possible:

```text
Your orchestration daemon
  |
  +-- tmux new-session -d -s agent-1 "claude ..."
  |     (tmux owns the PTY and session persistence)
  |
  +-- Unix socket IPC (your daemon)
  |     (message queue, state detection, OTEL, hooks)
  |
  +-- tmux send-keys (message delivery)
  |     (wrapped with state-aware queue logic)
  |
  +-- tmux attach (user access)
        (fallback always works, even if daemon dies)
```

However, this adds a layer rather than removing one. The daemon still
needs all the same queue, state, hook, and OTEL infrastructure. The only
thing tmux saves is `creack/pty` + `midterm` + the binary framing attach
protocol -- roughly 800 LOC out of h2's 36,800.

### 6.4 tmux vs h2 Verdict

| Criterion | tmux-based | h2 (current) |
| --------- | ---------- | ------------ |
| Session management | Simpler (mature, free) | More work but full control |
| Message delivery | Unreliable (`send-keys` is blind) | State-aware, priority-queued |
| State detection | Not possible (poll + parse) | Hook-driven, <100ms |
| Telemetry | Not possible | OTEL collector built-in |
| Permission control | Not possible | Hook + AI reviewer |
| Crash resilience | Better (tmux server survives) | Daemon crash = session loss |
| Multi-agent coordination | `send-keys` (blind) | Structured messaging + persistence |
| Implementation effort | ~200 LOC for basics | ~36,800 LOC for full system |

**tmux is a terminal multiplexer; h2 is an agent orchestration runtime
that happens to use a PTY.** Replacing h2 with tmux would be like
replacing a database with a file system -- the primitives are there, but
the semantics (transactions, queries, consistency) are not. h2's value
lives in the layers above the PTY: state-aware queuing, hook-driven
state detection, OTEL telemetry, structured messaging, and permission
control. tmux handles none of these.

The one defensible hybrid is using tmux as the PTY layer for crash
resilience while keeping h2's daemon for everything else. Whether the
added tmux dependency and coordination complexity is worth the crash
resilience gain is a judgment call -- h2's current approach of owning
the PTY directly is simpler and gives full control over write timeouts,
which tmux does not expose.

---

## 7. Recommendations

### 7.1 Components to Adopt from h2

1. **PTY-based agent harness**. The zero-coupling design is h2's strongest
   differentiator. By communicating through the terminal interface, you can
   orchestrate any TUI agent without custom integration code. This also means
   you can use subscription plans (Claude Max) instead of API keys. The
   3-second write timeout and Ctrl+C interrupt protocol are battle-tested.

2. **Daemon-per-agent process model with Unix socket IPC**. Full OS-level
   isolation eliminates entire classes of shared-state bugs. The
   JSON-over-Unix-socket protocol is simple and fast (<1ms). The binary
   framing for attach mode is elegant and efficient.

3. **Pod templates for group launch**. The role/pod YAML system with template
   rendering and count expansion is a clean way to define agent teams
   declaratively.

4. **3-source state triangulation**. The hooks > OTEL > output timing priority
   hierarchy with committed authority is more robust than relying on a single
   event source.

### 7.2 Components to Adopt from OpenClaw

1. **Followup runs queue with collect mode**. This is OpenClaw's most
   innovative queue feature. Batching messages that arrive during an active
   run into a single collected prompt produces much better agent responses
   than delivering them one-at-a-time. The overflow policies (especially
   `summarize`) are also worth adopting.

2. **Dual-layer lane concurrency**. The session lane (serialize per session) +
   global lane (cap total parallelism) pattern is elegant and prevents both
   per-session races and global resource exhaustion.

3. **Outbound delivery queue with disk persistence and retry**. For any
   external channel delivery (Telegram, Slack, etc.), OpenClaw's approach
   of persisting before sending + exponential backoff retry is
   production-grade. h2 has no outbound retry.

4. **Channel plugin architecture**. If you plan to support more than 2-3
   platforms, OpenClaw's extension-based plugin model with per-platform
   adapters (caching, rate limiting, chunking, draft streaming) is the way
   to scale.

### 7.3 Recommended Architecture

```text
                    Your Orchestration Layer
                    +--------------------------------------+
                    |  Agent Manager (from h2)              |
                    |  - Daemon per agent (PTY harness)     |
                    |  - Unix socket IPC                    |
                    |  - Pod/role templates                 |
                    |  - 3-source state detection           |
                    +--------------------------------------+
                    |  Queue System (hybrid)                |
                    |  - Priority sub-queues (from h2)      |
                    |  - Collect/coalesce mode (OpenClaw)   |
                    |  - Lane concurrency (OpenClaw)        |
                    |  - Overflow policies (OpenClaw)       |
                    +--------------------------------------+
                    |  Channel Layer (from OpenClaw)        |
                    |  - Plugin-based extensibility         |
                    |  - Outbound queue + retry             |
                    |  - Per-platform adapters              |
                    +--------------------------------------+
```

### 7.4 Language Recommendation

Write the runtime layer (daemon management, PTY harness, IPC, queue core) in
**Go**. If you need a rich channel/plugin layer or web UI, consider a
**TypeScript** service that communicates with the Go runtime via Unix sockets
or gRPC -- getting the benefits of both without the drawbacks of either.

**Bottom line**: Use h2's process model and agent harness as your foundation
(it's simpler, more robust, and agent-agnostic), but graft OpenClaw's queue
sophistication and channel extensibility on top. h2 gives you the right
runtime primitives; OpenClaw gives you the right application-layer
intelligence for message handling and platform integration.
