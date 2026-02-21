# Client UI Module

> `internal/session/client/` -- Per-client UI state, input handling, and terminal rendering.

## Overview

Each connected terminal (interactive or remote-attached) gets its own `Client` instance. The client manages input modes, cursor position, input history, scrollback navigation, and rendering -- all while sharing the same underlying VT (virtual terminal) buffer. The design uses callback injection so the `client` package never imports the parent `session` package.

## The Client Struct

```go
type Client struct {
    VT            *virtualterminal.VT  // shared terminal state
    Output        io.Writer            // per-client output destination
    Input         []byte               // current input buffer
    CursorPos     int                  // byte offset in Input
    History       []string             // session input history
    HistIdx       int
    Mode          InputMode            // current interaction mode
    ScrollOffset  int                  // scrollback offset
    InputPriority message.Priority     // current message priority
    TermRows, TermCols int             // client terminal dimensions

    // Callbacks wired by Session.NewClient():
    OnModeChange       func(old, new InputMode)
    OnSubmit           func(text string, priority Priority)
    OnInterrupt        func()
    OnDetach, OnRelaunch, OnQuit func()
    TryPassthrough     func() bool
    ReleasePassthrough func()
    TakePassthrough    func()
    IsPassthroughLocked func() bool
    QueueStatus        func() (count int, paused bool)
    OtelMetrics        func() OtelMetricsSnapshot
    AgentState         func() (state, subState, toolName string)
    HookState          func() *HookState
}
```

## Input Modes

```
                  ┌─────────────┐
         Ctrl+\   │  ModeMenu   │  Ctrl+\ or ESC
        ┌────────►│  (actions)  │◄────────┐
        │         └──┬──┬───┬───┘         │
        │         p  │  │t  │d/q          │
        │            ▼  ▼   ▼             │
  ┌─────┴──────┐  ┌──────────────┐        │
  │ ModeNormal │  │ModePassthrough│───────┘
  │  (default) │  │(direct PTY)  │
  └─────┬──────┘  └──────┬───────┘
        │                │
  mouse scroll     mouse scroll
        ▼                ▼
  ┌──────────┐  ┌────────────────────┐
  │ModeScroll│  │ModePassthroughScroll│
  └──────────┘  └────────────────────┘
```

| Mode | Value | Behavior |
|------|-------|----------|
| **ModeNormal** | 0 | h2 intercepts all input. Printable chars fill the input buffer. Enter submits to PTY (normal priority) or queue (other priorities). Control sequences passed through to child. |
| **ModePassthrough** | 1 | All input forwarded directly to PTY. Queue is paused. Only one client can hold passthrough at a time. |
| **ModeMenu** | 2 | Action menu overlay. Keys: `p` passthrough, `t` take passthrough, `c` clear input, `r` redraw, `d` detach, `q` quit. |
| **ModeScroll** | 3 | Scrollback navigation. Arrow keys scroll. ESC exits. |
| **ModePassthroughScroll** | 4 | Scroll while preserving passthrough ownership. |

## Input Handling by Mode

### ModeNormal (`HandleDefaultBytes`)

| Key | Action |
|-----|--------|
| Printable bytes | `InsertByte()` at cursor position |
| Enter (0x0D/0x0A) | Normal priority: write directly to PTY. Other: call `OnSubmit` for queue. |
| Backspace/Delete | `DeleteBackward()` |
| Tab (0x09) | Cycle input priority (normal → interrupt → idle-first → idle → normal) |
| Ctrl+\ (0x1C) | Open menu |
| Ctrl+A/E | Cursor to start/end (pass through if input empty) |
| Ctrl+K/U | Kill to end/start (pass through if input empty) |
| Arrow keys | Cursor movement or history navigation |
| Alt+Left/Right | Word-wise cursor movement |
| Other control bytes | Pass through to PTY |

### ModePassthrough (`HandlePassthroughBytes`)

All bytes forwarded to PTY via `writePTYOrHang`. ESC sequences accumulated in `PassthroughEsc`, flushed when complete. SGR mouse sequences intercepted for scroll support. Ctrl+\ exits passthrough.

### WritePTY Timeout Protection

```go
func writePTYOrHang(p []byte) bool {
    // Runs VT.WritePTY with 3s timeout
    // On timeout: sets VT.ChildHung=true, kills child, re-renders bar
}
```

This prevents deadlocks when the child's PTY buffer fills (e.g., during compaction).

## Rendering System

### Screen Rendering (`RenderScreen`)

Two rendering paths:

**Live view** (`renderLiveView`):
- Cursor-anchored window: computes `startRow = VT.Vt.Cursor.Y - ChildRows + 1`
- Renders `ChildRows` rows from the live terminal buffer
- Each row: position cursor → clear line → render content with ANSI attributes

**Scroll view** (`renderScrollView`):
- Renders from `VT.Scrollback` at `bottom - ChildRows + 1 - ScrollOffset`
- Draws `(scrolling)` indicator in inverse video at top-right

### Line Rendering (`RenderLineFrom`)

- Iterates `vt.Format.Regions(row)` for attribute spans from midterm
- Outputs `ESC[0m` + format rendering on attribute changes
- Writes UTF-8 content, pads with spaces to region end
- Handles terminal width correctly for multi-byte characters

### Status Bar (`RenderBar`)

The bar is rendered at the bottom of the terminal (2 rows, or 3 in debug mode):

```
┌──────────────────────────────────────────────────────────┐
│ Normal │ Active (tool use: Bash) 30s │ 12k/3k $0.42 │ [3 queued] │ agent-name │
├──────────────────────────────────────────────────────────┤
│ > your input text here█                                  │
└──────────────────────────────────────────────────────────┘
```

Contents:
- Mode label (color-coded by mode)
- Agent state from `AgentState()` callback
- OTEL metrics: input/output tokens and cost
- Queue indicator: `[N queued]` or `[N paused]`
- Agent name (right-aligned)

### Input Line Rendering

- Colored prompt prefix showing priority (e.g., `interrupt > `)
- Input buffer with cursor windowing (scrolls when input exceeds terminal width)
- Cursor positioned at correct column accounting for UTF-8

## Cursor Operations

UTF-8-aware cursor operations on `Client.Input []byte`:

| Operation | Behavior |
|-----------|----------|
| `CursorLeft/Right` | Decode single rune forward/backward |
| `CursorToStart/End` | Jump to byte 0 / len(Input) |
| `CursorForwardWord/BackwardWord` | Skip non-word then word chars (word = letter/digit/`_`) |
| `KillToEnd/KillToStart` | Slice operations on Input |
| `DeleteBackward` | Decode last rune, shift bytes |
| `InsertByte` | Grow slice, shift, insert at CursorPos |

## History

In-memory session history (not persisted):
- `HistoryUp()` saves current input on first navigation, walks backward
- `HistoryDown()` walks forward, restores saved input at bottom

## Kitty Keyboard Protocol

The client auto-detects kitty keyboard support:
1. Sends `CSI ? u` (query keyboard mode)
2. Waits 100ms for response
3. If detected: sends `CSI > 1 u` (enable disambiguate mode)
4. Adjusts help text (`Ctrl+Enter menu` vs `Ctrl+\ menu`)

## Terminal Setup (`SetupInteractiveTerminal`)

1. Detect terminal colors via termenv
2. Enter raw mode (`term.MakeRaw`)
3. Detect kitty keyboard support
4. Enable SGR mouse reporting
5. Register SIGWINCH handler for terminal resize
6. Start status ticker (1s refresh)
7. Start `ReadInput` goroutine (256-byte reads dispatched to mode handler under `VT.Mu`)
