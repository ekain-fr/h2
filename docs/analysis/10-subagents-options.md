# Focus: Subagent Options for Task-Scoped Execution

> Analysis of approaches for launching task-scoped agents (subagents) within the h2 architecture.

## Problem Statement

h2 agents are long-running daemon processes designed for persistent teams. However, many workflows need task-scoped execution: launch an agent, give it a specific job (implement a feature, review code), collect the result, and tear it down. This report analyzes the available approaches, their trade-offs, and what h2 would need for native subagent support.

## Background: h2 Agent Lifecycle

Each h2 agent follows a 4-phase lifecycle:

1. **Configuration** -- resolve h2 dir, load role YAML (template-rendered), apply overrides
2. **Setup** -- create session dir, bootstrap Claude config with hooks, optionally create git worktree
3. **Fork** -- `h2 run` re-execs itself as `h2 _daemon` with `Setsid=true`, fully detaching from the parent terminal
4. **Attach** (optional) -- a client connects to the daemon's Unix socket

Once running, the daemon spins up goroutines for message delivery, heartbeat, status tick, PTY output pipe, and socket accept loop. It then enters `lifecycleLoop()` which blocks until the child process exits.

Every agent is a **fully independent OS process** with its own PTY, Unix socket, OTEL HTTP server, hook collector, message queue, and session directory. This is fundamentally different from lightweight in-process subagents.

## How h2 Controls Claude Code

The daemon has full read/write access to the Claude Code PTY -- the same interface a human uses:

| Direction | Mechanism | Used For |
|-----------|-----------|----------|
| h2 → Agent | `VT.WritePTY()` -- raw bytes to PTY master fd | User input, queued messages, interrupts |
| Agent → h2 | `VT.PipeOutput()` -- 4KB chunks from PTY master | Terminal output, rendering |
| Agent → h2 | Hook events via `h2 hook collect` | State transitions (PreToolUse, PostToolUse, Stop) |
| Agent → h2 | OTEL HTTP server | Tokens, cost, tool metrics |

The daemon knows exactly when Claude Code is idle, active, thinking, using a tool, or blocked on permissions -- via the 3-collector priority hierarchy (hooks > OTEL > output timing). This state awareness is what makes programmatic orchestration possible.

## Approach 1: Claude Code's Built-In Task Tool

Claude Code has a native `Task` tool that spawns subagents within the same process. An h2 agent can use this tool directly -- no h2 changes needed.

### How It Works

```
Claude Code process (single Node.js process)
  |
  |-- Main agent loop
  |     |-- Send conversation to Claude API
  |     |-- Receive response (may include tool calls)
  |     |-- Execute tool calls (including Task)
  |     +-- Loop until done
  |
  +-- When Task tool is invoked:
        |-- Create a NEW agent loop (same process)
        |     |-- Fresh conversation history
        |     |-- Own system prompt (from subagent_type)
        |     |-- Scoped tool set (per agent type)
        |     |-- Send to Claude API (same auth)
        |     +-- Loop until max_turns or natural completion
        |
        |-- Parent loop is BLOCKED waiting for child loop
        |
        +-- Child's final text response becomes the
            Task tool's return value to the parent
```

### Characteristics

- **Same OS process** -- no fork, no PTY, no socket. A nested function call running another conversation loop.
- **Shared filesystem** -- subagent reads/writes the same files as the parent. No isolation unless `isolation: "worktree"` is specified.
- **Blocking** -- parent is suspended while subagent runs. Parent context is preserved but frozen.
- **Implicit completion** -- when the subagent's loop ends, the function returns. No polling or marker detection.
- **No inter-agent messaging** -- subagent can't talk to parent mid-execution. Runs to completion, returns a string.
- **Context isolation** -- separate conversation history, so subagent work doesn't consume parent's context window.
- **Opaque execution** -- no visibility into subagent progress from outside.

### Triggering from h2

An h2 agent can be instructed to use the Task tool via a normal message:

```bash
h2 send coder-1 "Use the Task tool to spawn a code-reviewer agent to check auth.ts"
```

h2 sees the parent agent as "active (tool use)" but has no visibility into the subagent itself.

## Approach 2: h2-Managed Subagent Daemons

h2 launches a full daemon per task, sends it a job, monitors for completion, collects the result, and tears it down.

### Flow

```bash
# 1. Launch
h2 run --role coding --name subtask-42 --detach

# 2. Send task
h2 send subtask-42 "Implement the auth module per the plan in docs/plan.md. \
  When done, run: h2 send orchestrator 'COMPLETED:subtask-42:auth module implemented'"

# 3. Monitor (orchestrator agent or external script polls status)
h2 status subtask-42  # → state: active / idle

# 4. Collect result (via message received, or peek)
h2 peek subtask-42

# 5. Teardown
h2 stop subtask-42
```

### Per-Agent Overhead

| Component | Startup Cost | Runtime Cost |
|-----------|-------------|--------------|
| Fork + daemon startup | ~1-2s | -- |
| Socket + session dir | ~100ms | Negligible |
| OTEL HTTP server | ~100ms | ~2MB memory |
| Goroutines (heartbeat, tick, accept) | -- | Near-zero CPU when idle |
| Teardown (`h2 stop`) | ~500ms | -- |

**For tasks taking 1+ minutes, this overhead is negligible** -- under 1% of total execution time. The overhead is architectural complexity, not runtime cost.

### Completion Detection Options

h2 already has all the primitives needed:

**Option A: `h2 send` back to orchestrator.** The subagent's instructions include a Bash command to report completion. This is the cleanest approach -- carries structured result data, uses existing first-class h2 messaging.

```
When you have completed all work, run this command:
  h2 send orchestrator "COMPLETED:subtask-42:<summary of what was done>"
```

**Option B: PTY output pattern matching.** h2 reads all PTY output through `VT.PipeOutput()`. A pattern matcher could watch for a marker like `**JOB COMPLETED**` in the output callback. The `OutputCollector` already does similar timing-based inference.

**Option C: Hook-based detection.** The `Stop` hook fires when Claude Code's conversation ends naturally. h2 captures this -- the agent transitions to `Idle` or `Exited`. Treat "child exited cleanly" as task completion.

**Option D: State polling.** External script polls `h2 status subtask-42` and interprets sustained idle (or exited) as completion. Least reliable but requires zero agent cooperation.

## Approach 3: Hybrid (h2 Daemon + Task Tool)

A long-running h2 orchestrator agent uses Claude Code's Task tool internally for subtask decomposition, while coordinating with other h2 agents via messaging.

```
h2 agents (long-lived daemons):
  orchestrator  -- receives tasks, decomposes, delegates
  coder-1       -- receives work via h2 send, uses Task tool internally
  coder-2       -- same
  reviewer      -- receives review requests via h2 send

Within each coder agent:
  Claude Code Task tool subagents (ephemeral, in-process):
    Explore agent -- find relevant files
    code-reviewer -- check implementation
    etc.
```

This plays to each layer's strengths: h2 handles persistent identity, inter-agent messaging, and observability; the Task tool handles lightweight in-process subtask dispatch.

## Comparison Matrix

| Dimension | Task Tool (Approach 1) | h2 Daemon (Approach 2) | Hybrid (Approach 3) |
|-----------|----------------------|----------------------|-------------------|
| Startup overhead | ~0 (function call) | ~2s (fork + daemon) | Mixed |
| Runtime overhead | None | Negligible for 1+ min tasks | Mixed |
| Parallelism | Multiple Tasks can run concurrently | Fully parallel processes | Both |
| Completion signal | Implicit (function returns) | Explicit (message, marker, hook) | Both |
| Result collection | Return value (string) | Message or PTY scraping | Both |
| Process isolation | None (shared filesystem) | Full (own PTY, socket, env) | Per-layer |
| Git worktree isolation | Optional (`isolation: "worktree"`) | Built-in per role config | Both |
| Orchestrator | Parent Claude Code agent | Another h2 agent or script | h2 agent |
| Live observability | None during execution | `h2 attach`, `h2 peek`, `h2 list` | h2 layer visible |
| Survivability | Dies if parent dies | Independent process | h2 agents survive |
| Intervention | Cannot interact mid-task | Can attach and type mid-task | h2 agents only |
| Context window | Consumes parent's API budget | Own API conversation | Own per layer |
| Implementation effort | Zero (already works) | Needs orchestration logic | Moderate |

## Recommendations

### For quick subtasks (< 1 min, focused scope)

**Use the Task tool.** Zero overhead, implicit completion, simple result collection. Examples: explore codebase, search for patterns, review a single file.

### For substantial tasks (1-10 min, multi-file changes)

**Either approach works.** Choose based on requirements:

- Need **live observability** or **human intervention**? → h2 daemon
- Need **process isolation** or **independent survival**? → h2 daemon
- Need **simplicity** and results flow back to a parent agent? → Task tool
- Need **parallel execution** across isolated worktrees? → h2 daemon (each gets its own worktree)

### For team-scale orchestration (multiple agents, ongoing work)

**Use the hybrid approach.** Long-running h2 agents coordinate via messaging. Each agent uses the Task tool internally for its own subtask decomposition. This is the natural architecture that plays to both layers' strengths.

## What h2 Would Need for Native Subagent Support

If h2 were to add first-class subagent primitives (beyond what's possible today with existing tools), the gaps are:

1. **`h2 run --ephemeral`** -- a launch mode that auto-stops the daemon when the child exits cleanly, skipping the relaunch prompt. Avoids manual `h2 stop`.

2. **`h2 run --on-complete "h2 send orchestrator DONE:{name}"`** -- a callback hook triggered on clean child exit. Removes the need to embed completion instructions in the agent's prompt.

3. **`h2 wait <name> [--timeout 10m]`** -- blocks until agent reaches idle/exited state, returns exit status. Enables simple scripting: `h2 run ... --detach && h2 wait subtask-42 && h2 peek subtask-42`.

4. **Result extraction** -- `h2 peek` already reads the Claude Code session JSONL log. A `--last-response` flag could extract just the final assistant message as a structured result.

5. **Lightweight daemon mode** -- skip OTEL server, heartbeat, and hook collector for ephemeral agents that don't need full telemetry. Reduces memory footprint and startup time.

None of these are fundamental architecture changes -- they're convenience wrappers around existing primitives.
