# Telegram Command Execution

## Overview

Allow users to execute whitelisted CLI commands from Telegram and receive the output back. Messages starting with `/h2` or `/bd` are intercepted by the bridge service before agent routing, executed as shell commands, and the stdout/stderr is sent back to Telegram.

This is fundamentally shell access scoped to a whitelist, not agent interaction.

## User Experience

```
User sends in Telegram:     /h2 list
Bot replies:                 NAME        STATE    POD
                             concierge   active
                             coder-1     idle     dev

User sends:                  /bd list
Bot replies:                 h2-abc  open  Fix login bug
                             h2-def  open  Add caching

User sends:                  /h2 badcommand
Bot replies:                 ERROR (exit 1):
                             Error: unknown command "badcommand" for "h2"

User sends:                  /notwhitelisted foo
Bot replies:                 (routed to agent as normal — not a command)
```

## Design

### 1. Config: Whitelisted Commands

Add an `allowed_commands` field to the bridge config at the user level:

```yaml
# ~/.h2/config.yaml
users:
  dcosson:
    bridges:
      telegram:
        bot_token: "..."
        chat_id: 12345
    allowed_commands:
      - h2
      - bd
```

**Config changes:**

```go
// internal/config/config.go
type UserConfig struct {
    Bridges         BridgesConfig `yaml:"bridges"`
    AllowedCommands []string      `yaml:"allowed_commands,omitempty"`
}
```

The whitelist contains bare command names (not paths). Each entry must match `^[a-zA-Z0-9_-]+$` (same character class as agent names). Empty strings are rejected. The command is resolved via `exec.LookPath` at execution time.

### 2. Telegram Message Parsing

In `telegram.go`'s `poll()`, Telegram slash commands (`/h2 ...`, `/bd ...`) arrive as the full message text. Before calling the existing `ParseAgentPrefix` + `handler()` flow, check if the message starts with `/<whitelisted-command>`.

The parsing logic is added as a new function in the `bridge` package:

```go
// internal/bridge/bridge.go

// ParseSlashCommand checks if text starts with /<command> where command
// is in the allowed list. Returns the command name and args string,
// or empty command if not matched.
func ParseSlashCommand(text string, allowed []string) (command, args string) {
    if !strings.HasPrefix(text, "/") {
        return "", ""
    }
    // Split on first space: "/h2 list --json" → "h2", "list --json"
    rest := text[1:] // strip leading /
    parts := strings.SplitN(rest, " ", 2)
    cmd := strings.TrimSpace(parts[0])
    if cmd == "" {
        return "", ""
    }
    for _, a := range allowed {
        if cmd == a {
            if len(parts) > 1 {
                return cmd, parts[1]
            }
            return cmd, ""
        }
    }
    return "", ""
}
```

### 3. Command Execution

New file `internal/bridgeservice/exec.go`:

```go
// ExecCommand runs a whitelisted command and returns the formatted output.
func ExecCommand(command, args string) string {
    // Resolve command path via LookPath (no shell interpretation)
    path, err := exec.LookPath(command)
    if err != nil {
        return fmt.Sprintf("ERROR: command %q not found in PATH", command)
    }

    // Split args using shell-like splitting (respects quotes)
    argv, err := shlex.Split(args)
    if err != nil {
        return fmt.Sprintf("ERROR: invalid arguments: %v", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctx, path, argv...)
    output, err := cmd.CombinedOutput()

    result := strings.TrimRight(string(output), "\n")
    if err != nil {
        exitCode := 1
        if exitErr, ok := err.(*exec.ExitError); ok {
            exitCode = exitErr.ExitCode()
        }
        if ctx.Err() == context.DeadlineExceeded {
            return truncateOutput(fmt.Sprintf("ERROR (timeout after 30s):\n%s", result))
        }
        return truncateOutput(fmt.Sprintf("ERROR (exit %d):\n%s", exitCode, result))
    }

    if result == "" {
        return "(no output)"
    }
    return truncateOutput(result)
}
```

Key decisions:
- **No shell**: Uses `exec.Command` directly, never `sh -c`. Arguments are split by a Go shlex library (e.g. `google/shlex`), not a shell.
- **30s timeout**: Commands that hang don't block the bridge forever.
- **CombinedOutput**: Interleaved stdout+stderr matches what a user expects.
- **Output truncation**: Telegram has a 4096-char message limit. Truncate output with a `... (truncated)` suffix if it exceeds ~4000 chars.

### 4. Integration Point: Bridge Service

The bridge service's `handleInbound` method is the natural integration point. It already receives parsed messages before routing to agents.

**Option A (chosen): Intercept in `handleInbound`**

The `Service` struct gains an `allowedCommands []string` field set at construction time:

```go
// internal/bridgeservice/service.go

func New(bridges []bridge.Bridge, concierge, socketDir, user string, allowedCommands []string) *Service {
    return &Service{
        bridges:         bridges,
        concierge:       concierge,
        socketDir:       socketDir,
        user:            user,
        allowedCommands: allowedCommands,
    }
}

func (s *Service) handleInbound(targetAgent, body string) {
    // If an explicit agent prefix was parsed, route to that agent.
    // This ensures "concierge: /h2 list" sends the text "/h2 list" to
    // the concierge rather than executing it as a command.
    if targetAgent != "" {
        // ... existing agent routing logic ...
        return
    }

    // Check for slash commands before default-target agent routing.
    cmd, args := bridge.ParseSlashCommand(body, s.allowedCommands)
    if cmd != "" {
        log.Printf("bridge: executing command /%s %s", cmd, args)
        result := ExecCommand(cmd, args)
        s.reply(result)
        return
    }

    // ... existing default-target agent routing logic unchanged ...
}
```

Note: the slash command check uses the **original message text** (the `body` after agent-prefix parsing). Since `/h2 list` doesn't match the agent prefix pattern (`name: body`), it arrives in `body` with `targetAgent=""`. The slash command check happens before `resolveDefaultTarget()`.

Actually — looking more carefully at the flow: `ParseAgentPrefix("/h2 list")` would try to match `^([a-zA-Z0-9_-]+):\s*(.*)$` — the `/` prevents a match since `/h2` doesn't match `[a-zA-Z0-9_-]+`. So `body` = `/h2 list` and `targetAgent` = `""`. This is correct — the full original text reaches `handleInbound`.

### 5. Reply Path

Add a `reply` helper that sends to all Sender bridges (reuses the same pattern as `replyError`):

```go
func (s *Service) reply(msg string) {
    ctx := context.Background()
    for _, b := range s.bridges {
        if sender, ok := b.(bridge.Sender); ok {
            if err := sender.Send(ctx, msg); err != nil {
                log.Printf("bridge: reply via %s: %v", b.Name(), err)
            }
        }
    }
}
```

The existing `replyError` can be refactored to use `reply` internally.

### 6. Wiring Through the Call Chain

The allowed commands list needs to flow from config to the bridge daemon:

1. `bridge.go` CLI reads `userCfg.AllowedCommands`
2. Passes to `ForkBridge(user, concierge, allowedCommands)`
3. `ForkBridge` passes as CLI args to `_bridge-service` (e.g. `--allowed-cmd h2 --allowed-cmd bd`)
4. `_bridge-service` hidden command reconstructs the list and passes to `New()`

Alternatively, since the bridge daemon already loads config, it can read `allowedCommands` directly from config in the `_bridge-service` command rather than passing through CLI args. This is simpler and means config changes take effect on bridge restart.

**Chosen approach**: Read from config in `_bridge-service`. The daemon already loads config for bridge setup; adding `AllowedCommands` is trivial.

### 7. Security Considerations

- **Whitelist only**: Only explicitly listed command names are executed. Default is empty (no commands allowed).
- **No shell**: `exec.Command` is used directly, never through a shell interpreter. No shell expansion, no pipes, no redirects.
- **Argument splitting**: Go's shlex library handles quoting but doesn't interpret shell operators.
- **Command name validation**: Config loading validates that entries in `allowed_commands` match `^[a-zA-Z0-9_-]+$`. Empty strings are rejected.
- **LookPath resolution**: Commands are found via PATH, same as typing them in a terminal.
- **Timeout**: 30s hard timeout prevents hanging commands from blocking the bridge.
- **Output size**: Truncated to fit Telegram's message limit.
- **No stdin**: Commands get no stdin (nil).
- **Working directory**: Commands run in the h2 dir (from `ConfigDir()`), same environment as the bridge daemon. This means `h2` subcommands will resolve the h2 dir correctly. Commands that are project-dir-sensitive (e.g. git) would operate in the h2 dir, not a project root — acceptable for the intended `h2`/`bd` use case.

### Known Limitations (v1)

- **Reply broadcast**: Command output is sent to all configured Sender bridges, not just the one that originated the message. This matches existing `replyError` behavior. In practice most setups have a single platform bridge (Telegram). Supporting per-bridge reply routing would require threading bridge identity through `InboundHandler`, which is a larger refactor deferred to v2.

### 8. Output Truncation

Telegram messages max out at 4096 characters. The output formatter:

```go
const maxOutputLen = 4000 // leave room for ERROR prefix

func truncateOutput(s string) string {
    if len(s) <= maxOutputLen {
        return s
    }
    return s[:maxOutputLen] + "\n... (truncated)"
}
```

## File Changes

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `AllowedCommands` to `UserConfig` |
| `internal/bridge/bridge.go` | Add `ParseSlashCommand()` |
| `internal/bridge/bridge_test.go` | Tests for `ParseSlashCommand` |
| `internal/bridgeservice/exec.go` | New: `ExecCommand()`, `truncateOutput()` |
| `internal/bridgeservice/exec_test.go` | New: tests for command execution |
| `internal/bridgeservice/service.go` | Add `allowedCommands` field, intercept in `handleInbound`, add `reply()` |
| `internal/bridgeservice/service_test.go` | Update tests for new field + command interception |
| `internal/cmd/bridge_service.go` | Pass `AllowedCommands` from config to `New()` |

## Dependency

- `github.com/google/shlex` for safe argument splitting (or a vendored minimal implementation — it's ~50 lines)

## Testing Plan

### Unit Tests

**`bridge_test.go` — ParseSlashCommand:**
1. `/h2 list` with `["h2"]` → `("h2", "list")`
2. `/bd create "my issue"` with `["h2", "bd"]` → `("bd", "create \"my issue\"")`
3. `/h2` alone (no args) with `["h2"]` → `("h2", "")`
4. `/notallowed foo` with `["h2"]` → `("", "")`
5. Plain text `hello` → `("", "")`
6. Agent-prefixed `concierge: /h2 list` — will have been parsed by `ParseAgentPrefix` first, so body="/h2 list" would match. This is fine — `handleInbound` guards with `targetAgent != ""` check first.
7. Empty allowed list → always `("", "")`
8. `/H2 list` (case sensitivity) → `("", "")` (case-sensitive match)
9. `/h2   ` (trailing whitespace, no args) → `("h2", "")` (cmd trimmed)

**`exec_test.go` — ExecCommand:**
1. Successful command: `ExecCommand("echo", "hello")` → `"hello"`
2. Failed command: `ExecCommand("false", "")` → starts with `"ERROR (exit 1)"`
3. Command not found: `ExecCommand("nonexistent_cmd_xyz", "")` → `"ERROR: command ... not found"`
4. Output truncation: command producing >4000 chars → truncated with suffix
5. Timeout: command that sleeps >30s → `"ERROR (timeout after 30s)"`  (use a shorter timeout in test)
6. Empty output: `ExecCommand("true", "")` → `"(no output)"`
7. Argument quoting: `ExecCommand("echo", "'hello world'")` → `"hello world"`

**`service_test.go` — handleInbound with commands:**
1. Message `/h2 list` with `allowedCommands=["h2"]` → command executed, reply sent to bridge, NOT routed to agent
2. Message `hello` with `allowedCommands=["h2"]` → routed to agent as normal
3. Message `/h2 list` with `allowedCommands=[]` → routed to agent (empty whitelist)
4. Message `concierge: /h2 list` → arrives as targetAgent="concierge", body="/h2 list". Since targetAgent is set, it routes to concierge (agent-prefix takes priority over slash commands). This ensures explicit agent routing still works.

**Config validation tests:**
1. `allowed_commands: ["h2", "bd"]` → valid
2. `allowed_commands: ["/usr/bin/h2"]` → error (contains `/`)
3. `allowed_commands: ["rm -rf"]` → error (contains space)
4. `allowed_commands: ["h2;echo"]` → error (contains `;`)
5. `allowed_commands: [""]` → error (empty string)

### Integration/E2E Tests

1. **End-to-end with mock Telegram**: Use the existing `httptest.Server` pattern from Telegram tests. Send a Telegram update with `/h2 version`, verify the bot replies with version output.
2. **Interleaving**: Send `/h2 list`, then a regular message, then `/bd list`. Verify commands get replies and the regular message routes to an agent.
3. **Config reload**: Start bridge with empty `allowed_commands`, verify `/h2` routes to agent. Restart with `["h2"]`, verify `/h2` executes.
4. **Concurrent commands**: Send `/h2 list` and `/bd list` nearly simultaneously. Verify both get replies with no races in the reply path (run with `-race` flag).

## Alternatives Considered

**Intercept in `telegram.go` poll()**: Rejected because it would couple Telegram-specific code with command execution and bypass the bridge service abstraction. Other future bridge types (Slack, Discord) should get the same command execution for free.

**Use a shell for argument parsing**: Rejected for security. `sh -c` would enable injection via backticks, `$()`, pipes, etc. Go's shlex is sufficient for the intended use cases.

**Per-command config (timeout, allowed args)**: Deferred. Simple whitelist is sufficient for v1. Can add per-command config later if needed.
