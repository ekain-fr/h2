# Concierge Review: Codex Agent Type Design Plan

Review of `plan-codex-agent-type.md` (planner) and `plan-codex-agent-type-review.md` (reviewer).

## Agreement with the Reviewer

The reviewer's feedback is thorough and accurate. I agree with all five key issues:

1. **Metadata write gate on ClaudeConfigDir** — confirmed at `daemon.go:98`: `if s.SessionDir != "" && s.ClaudeConfigDir != ""`. This will silently skip metadata for Codex agents. Must change to just `s.SessionDir != ""`.

2. **Full daemon pipeline** — confirmed. `ForkDaemonOpts` → `_daemon` CLI flags → `RunDaemonOpts` → `Session` fields → `childArgs()`. The `RoleArgs()` approach is right but the plan underestimates the plumbing. The `ForkDaemonOpts` struct and the `_daemon` CLI flag parsing both pass Claude-specific fields unconditionally. Codex will receive `--append-system-prompt` and `--permission-mode` flags that it doesn't understand.

3. **Auth checking** — confirmed in `agent_setup.go`. While there's no explicit `IsRoleAuthenticated()` call visible in the current `setupAndForkAgent`, the `EnsureClaudeConfigDir` call at line 43 creates Claude-specific config infrastructure. For Codex, this should be skipped.

4. **CodexType should store command** — yes, minor but correct.

5. **Confirm Codex CLI flags** — essential before implementation.

## Additional Issues Not Raised

### A. CLAUDECODE env var leaks into forked agents

**This is a real bug I hit today.** `ForkDaemon` at `daemon.go:306` does `env := os.Environ()` which inherits the `CLAUDECODE` environment variable from the parent Claude Code session. When a Claude Code agent (like me, the concierge) runs `h2 run` to launch another agent, the `CLAUDECODE` env var leaks through, causing Claude Code to refuse to start with "cannot be launched inside another Claude Code session."

The QA runner handles this correctly — `qa_run.go:219` uses `filteredEnv("CLAUDECODE", ...)`. But `ForkDaemon` doesn't.

**Fix needed regardless of Codex support**: Filter `CLAUDECODE` from the environment in `ForkDaemon`, just like `filteredEnv` does in `qa_run.go`. This is a pre-existing bug that affects agent-spawns-agent workflows with Claude Code too, not just Codex.

### B. The plan doesn't address `codex exec` vs `codex` (interactive)

The plan mentions both modes but doesn't make a clear decision. For h2's PTY-based model, the agent needs to run interactively. But Codex's interactive mode is `codex` (the TUI), not `codex exec`. The `codex exec` command is one-shot.

Questions:
- Does `codex` (the interactive TUI) work in a PTY managed by h2? It's a full TUI app — does it play nicely with h2's virtual terminal?
- If so, does it accept pasted text as user input (for message delivery)?
- If not, we may need `codex exec` in a loop, which changes the entire model.

This is the biggest unknown and should be investigated before any implementation begins.

### C. The `RoleArgs()` method needs careful scoping

The plan proposes `RoleArgs(cfg RoleConfig) []string` on the `AgentType` interface. But `RoleConfig` as described only covers a subset of role fields. What about:
- `instructions` delivery — Claude uses `--append-system-prompt`, Codex may need stdin or a different mechanism
- `working_dir` — already handled by `ForkDaemonOpts.CWD`, not via CLI args
- `extra_args` — the generic escape hatch

The interface method should be minimal. Consider passing the full `*config.Role` rather than a struct that will keep growing. Or better: have the agent type produce its own `ForkDaemonOpts` fields rather than just CLI args.

### D. Instructions delivery for Codex

The plan is vague about how instructions reach Codex. Claude Code uses `--append-system-prompt`. Codex doesn't have that flag. Options:
1. Prefix the initial prompt with instructions
2. Use a config file/env var if Codex supports one
3. Use stdin (`echo "instructions" | codex exec -`) — but this is one-shot mode

This needs a concrete answer before implementation.

## Assessment of the Plan

**Good foundation, needs one round of revision.** The architecture (CodexType, phased collectors, OutputCollector MVP) is sound. The reviewer caught the important implementation gaps. My additions above (CLAUDECODE env leak, interactive vs exec mode, instructions delivery) need answers before starting.

**Suggested revision priority:**
1. Fix the CLAUDECODE env leak in `ForkDaemon` (bug fix, independent of Codex)
2. Investigate Codex interactive TUI in a PTY (blocker — determines feasibility)
3. Decide on instructions delivery mechanism
4. Address the reviewer's 5 items
5. Then implement the plan's Phase 1
