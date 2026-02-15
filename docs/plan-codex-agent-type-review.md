# Review: Codex Agent Type Design Plan

## What's Good

**Correct understanding of the AgentType interface.** The proposed `CodexType` struct accurately mirrors the existing `GenericType` pattern — `Collectors()` returns an empty `CollectorSet{}`, `OtelParser()` returns nil, etc. This is exactly how you'd add a new "no OTEL, no hooks" agent type.

**Phased collector strategy is pragmatic.** Starting with `OutputCollector` as the sole collector is the right call. The plan correctly identifies that `StartCollectors()` always creates an `OutputCollector` as a fallback, and when no OTEL or hooks are enabled, it becomes the primary. Zero Codex-specific code needed for MVP state tracking.

**Good identification of the `childArgs()` problem.** This is the most important issue in the plan. `childArgs()` in `session.go` unconditionally appends Claude-specific flags (`--append-system-prompt`, `--system-prompt`, `--model`, `--permission-mode`, `--allowedTools`, `--disallowedTools`). Passing these to Codex would fail. The plan correctly flags this and proposes a fix.

**Option A for `RoleArgs()` is the right abstraction.** Moving role-to-CLI-arg mapping into the `AgentType` interface via a `RoleArgs(cfg RoleConfig)` method is clean and extensible. Each agent type knows its own CLI.

**Honest about limitations.** The plan doesn't oversell. It acknowledges that metrics, peek, and structured event parsing are all Phase 2 items that depend on understanding Codex's actual runtime behavior. The open questions section is useful and shows the author knows what they don't know.

**Messaging analysis is solid.** The PTY-based delivery mechanism should work for Codex interactive mode since it accepts text input at its prompt. The plan correctly identifies the `codex exec` incompatibility.

## Issues and Inaccuracies

### 1. `DisplayCommand()` should probably differ from `Command()`

The plan has `DisplayCommand()` returning `"codex"` — same as `Command()`. This works, but it's worth noting: if the user specifies a full path like `/usr/local/bin/codex`, `ResolveAgentType` matches on `filepath.Base(command)`, but `Command()` should return the original full path (not just `"codex"`). The `GenericType` gets this right by storing and returning `t.command`. The plan's `CodexType` hardcodes `"codex"` which would be wrong if the user passed a full path.

**Fix:** Store the resolved command path in `CodexType`, like `GenericType` does.

### 2. `ResolveAgentType` is called with the command from `session.New()`

The plan shows `ResolveAgentType` just adding a `case "codex":` branch. But look at how it's called — `session.New()` passes `command` which comes from either `role.GetAgentType()` or the `--agent-type` flag or the `--command` flag. When `--agent-type codex` is used in `run.go`, the value `"codex"` is passed directly as the command. When a role has `agent_type: codex`, `setupAndForkAgent` passes `role.GetAgentType()` as the command. So `filepath.Base("codex")` is just `"codex"` — this works. But worth confirming the plan accounts for the case where someone runs `--command /usr/local/bin/codex`.

### 3. Role validation has a gap

The plan says `Role.Validate()` should "warn (not error) if Claude-specific fields are set with a non-Claude agent type." But the current `Validate()` method in `role.go` doesn't check `AgentType` at all — it validates `PermissionMode` against `ValidPermissionModes` unconditionally. If a Codex role sets `permission_mode: dontAsk`, the current code would accept it even though it's meaningless for Codex.

More importantly: `Validate()` requires `Instructions != "" || SystemPrompt != ""`. This makes sense for Claude but may be overly restrictive for Codex roles. If Codex roles are expected to have instructions, fine, but this should be explicitly called out — will a Codex role always need an instructions field?

### 4. The `childArgs()` refactoring is more involved than described

The plan focuses on `childArgs()` in `session.go`, but the Claude-specific fields flow through the entire daemon fork pipeline:

- `ForkDaemonOpts` struct has `Instructions`, `SystemPrompt`, `Model`, `PermissionMode`, `AllowedTools`, `DisallowedTools` fields
- `ForkDaemon()` passes these as CLI args to the `_daemon` subprocess
- `RunDaemonOpts` receives them and sets them on the `Session` struct
- `childArgs()` reads them from the `Session` and appends them to the command

The plan's `RoleArgs()` approach would clean up `childArgs()`, but the fields still need to flow through `ForkDaemonOpts` → `_daemon` CLI → `RunDaemonOpts` → `Session`. For Codex, some of these fields (like `PermissionMode`) should simply not be set. The plan should address whether `setupAndForkAgent` needs to be agent-type-aware when populating `ForkDaemonOpts`, or whether unused fields just get ignored downstream.

### 5. Session metadata doesn't record agent type

The plan proposes adding `AgentType` to `SessionMetadata` (Section 6, item 1). This is correct and important. But the current `SessionMetadata` struct has no such field — it has `Command` which could be inspected but isn't the same thing (command could be a path). The plan should note that `daemon.go`'s metadata write (line 100-113) needs updating to include the agent type.

Also: the metadata is only written when `s.SessionDir != ""` AND `s.ClaudeConfigDir != ""`. For Codex agents launched without a role (i.e., `--agent-type codex`), `ClaudeConfigDir` will be empty, so **no metadata gets written at all**. The `peek` command and any other metadata consumers will fail. This conditional needs to be relaxed for non-Claude agents.

### 6. Missing: `extra_args` field in Role struct

The plan recommends a generic `extra_args` field (Section 4, Option 1) but doesn't address that this field doesn't exist in the `Role` struct today. Adding it requires:
- Adding the YAML field to `Role`
- Flowing it through `setupAndForkAgent` → `ForkDaemonOpts` → `_daemon` CLI → `RunDaemonOpts` → `childArgs()`
- Or, if using the `RoleArgs()` approach, the agent type could consume it from the role config struct

This is straightforward but should be explicitly scoped.

### 7. The `summarizeWithHaiku` function in `peek.go` hardcodes `claude`

`peek.go`'s `summarizeWithHaiku` function runs `claude --model haiku --print` as an external command. This is fine for Claude agents but would need to be considered if peek were ever extended to Codex — though the plan recommends skipping peek for Codex in MVP, which sidesteps this.

### 8. Auth checking is underspecified

Open question 5 mentions `codex login` for authentication. This matters more than the plan suggests. The current flow in `setupAndForkAgent` checks `role.IsRoleAuthenticated()` which calls `IsClaudeConfigAuthenticated()`. For Codex roles, this would either:
- Always return false (no `.claude.json` in a Codex context)
- Need to be skipped entirely

The plan should clarify whether the auth check in the launch path needs to be gated on agent type.

## Alternative Approaches Worth Considering

### Embed CodexType as a variant of GenericType

Since `CodexType` is functionally identical to `GenericType` for MVP (no collectors, no OTEL, no hooks), consider whether a separate type is needed at all. You could use `GenericType` and distinguish behavior based on the command name only where it matters (peek, future collectors). This avoids a type that has zero behavioral difference from Generic.

**Counter-argument:** Having a named type makes it easier to add Codex-specific behavior later (like `RoleArgs()` mapping). The type is cheap to add. On balance the plan's approach is probably better.

### Codex config via environment variables instead of CLI args

Instead of mapping role fields to Codex CLI flags, another approach is environment variable injection. If Codex respects env vars for configuration (as many OpenAI tools do), `ChildEnv()` could be the primary configuration mechanism for Codex, keeping `PrependArgs()` minimal.

## Questions the Plan Should Answer

1. **What happens when `ClaudeConfigDir` is empty for non-Claude agents?** The session metadata write is gated on this being non-empty. Should the gate be changed to just `SessionDir != ""`?

2. **Should `setupAndForkAgent` call `role.IsRoleAuthenticated()` for non-Claude roles?** If yes, what does auth look like for Codex?

3. **What's the actual Codex CLI interface?** The plan references `--full-auto`, `--model`, `--cd`, `--ephemeral` but doesn't cite documentation. Are these confirmed flags? Does Codex support `--model` the same way Claude does?

4. **What version of Codex CLI is targeted?** Codex CLI is relatively new and evolving. The plan should pin the expected interface version.

5. **How does `h2 send` work for Codex?** The plan says the `h2` binary is in PATH and `H2_ACTOR` is set. But does Codex have a `Bash` tool (or equivalent) that can run shell commands? If Codex can't run arbitrary shell commands, it can't send h2 messages.

## Overall Assessment

**Approve with changes.** The plan is well-structured, demonstrates good understanding of the codebase, and makes sensible decisions (OutputCollector MVP, RoleArgs interface, skip peek). The phased approach is appropriate.

The main gaps to address before implementation:

1. **Fix the metadata write gate** — the `ClaudeConfigDir != ""` condition will prevent metadata from being written for Codex agents. This is a blocker for peek and other metadata consumers.
2. **Address the full daemon pipeline** for role fields, not just `childArgs()`. The `ForkDaemonOpts` and `_daemon` CLI interface may need agent-type-aware logic or just graceful handling of unused fields.
3. **Clarify auth checking** in the launch path for non-Claude agent types.
4. **Confirm Codex CLI flags** against actual documentation before implementing `RoleArgs()`.
5. **Store the command in CodexType** (like GenericType does) rather than hardcoding `"codex"`.

None of these are fundamental design flaws — they're gaps that would be caught during implementation but are worth addressing in the plan to avoid rework.
