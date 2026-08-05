# Offline Runtime Adapter Contracts

AO Next has provider-neutral normalization contracts for the locally installed Codex and Claude CLIs. Phase-8 qualification used only local `--version` and `--help` commands. It made no model or provider call.

Both adapters prepare structured program-plus-argument invocations, pass the prompt through bounded stdin, preserve one exact worker identity, parse bounded structured output into `AdapterTurn`, and reject malformed output or runtime-identity drift. The shared process runner has hard input, combined-output, timeout, cancellation, working-directory, and missing-executable behavior and never invokes a shell.

The Codex invocation is ephemeral, ignores user configuration and rules, disables approval escalation, uses a read-only sandbox, and requests JSONL plus an operator-supplied output schema. The Claude invocation uses bare print mode, disables persistence and slash commands, supplies an empty strict MCP configuration, and disables all built-in tools. Neither adapter enables dynamic agents or dangerous permission bypasses.

Live smoke tests are ignored. They require a separately authorized operator process with `AO_NEXT_LIVE_ADAPTER_TESTS=operator-authorized`; model output is only parsed as data and cannot alter that process environment. The sanitized help/version fixtures contain no executable paths, credentials, account identifiers, sessions, or provider output.
