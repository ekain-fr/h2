# h2 Codebase Analysis

Detailed architectural analysis of the h2 agent runner, generated February 2026.

## Reports

| # | Report | Scope |
|---|--------|-------|
| 00 | [Architecture Overview](00-architecture-overview.md) | High-level architecture, module inventory, dependency graph, process model, key design decisions |
| 01 | [Session Runtime](01-session-runtime.md) | Session lifecycle, daemon architecture, socket listener, attach protocol, lifecycle loop |
| 02 | [Client UI](02-client-ui.md) | Input modes, mode state machine, input handling, rendering system, status bar, cursor/history |
| 03 | [Agent Telemetry](03-agent-telemetry.md) | OTEL HTTP server, metrics aggregation, collector hierarchy, state machine, hook/output/OTEL collectors |
| 04 | [Message System](04-message-system.md) | Priority queue, delivery loop, message persistence, wire protocol, binary framing |
| 05 | [Config and Templates](05-config-and-templates.md) | h2 dir resolution, roles, pods, template engine, overrides, session setup, routes registry |
| 06 | [Bridge and Telegram](06-bridge-and-telegram.md) | Bridge interfaces, bridge service daemon, Telegram long-polling, macOS notifications, routing |
| 07 | [Communication Channels](07-communication-channels.md) | **Focus**: All bidirectional communication paths -- PTY, OTEL, hooks, Unix sockets, Telegram |
| 08 | [Agent Runner](08-agent-runner.md) | **Focus**: Full agent launch flow, process isolation, control operations, lifecycle management, pods |

## Codebase Statistics

- **Language**: Go 1.24
- **Total LOC**: ~36,800 (including tests)
- **Source files**: ~85 `.go` files
- **Test coverage**: Extensive -- most modules have companion `_test.go` files
- **Key dependencies**: cobra (CLI), creack/pty (PTY), vito/midterm (VT100), termenv (colors), yaml.v3 (config)
