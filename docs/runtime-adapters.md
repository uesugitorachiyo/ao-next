# Offline Runtime Adapter Contracts

AO Next has provider-neutral normalization contracts for the locally installed Codex and Claude CLIs. Phase-8 qualification used only local `--version` and `--help` commands. It made no model or provider call.

Both adapters prepare structured program-plus-argument invocations, pass the prompt through bounded stdin, preserve one exact worker identity, parse bounded structured output into `AdapterTurn`, and reject malformed output or runtime-identity drift. The shared process runner enforces input, combined-output, timeout, cancellation, working-directory, and missing-executable bounds. It never invokes a shell.

The N7 Codex invocation is ephemeral, ignores user configuration and rules, disables approval escalation, uses a read-only sandbox, and requests JSONL plus an operator-supplied output schema. The Claude invocation uses bare print mode, disables persistence and slash commands, supplies an empty strict MCP configuration, and disables all built-in tools. N7 workspace changes occur only through admitted structured effects.

The N4 baseline is a separate fixed Codex invocation. It is ephemeral, ignores ambient configuration and rules, disables approval escalation, and uses the native workspace-write sandbox. Its prompt carries the same source, workspace, objective, policy, and verifier bindings as N7. The model never selects the verifier or live authority.

Runtime usage comes only from Codex `turn.completed` or Claude result envelopes. The harness overwrites model-authored counters, stores each bounded status/stdout/stderr digest, and binds the ordered capture-digest list into the measurement. Missing, duplicate, malformed, or contradictory trusted usage fails the run.

The provider commands require `AO_NEXT_LIVE_PROVIDER_CALLS=operator-authorized`. The gate is checked before the input path or executable is resolved. Live contract smoke tests remain ignored behind their separate `AO_NEXT_LIVE_ADAPTER_TESTS=operator-authorized` gate. Model output is data and cannot alter either process environment. Sanitized help fixtures and fake captures contain no credentials, account identifiers, sessions, or real provider output.
