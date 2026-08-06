# AO Next

AO Next is a local experimental execution kernel for bounded engineering work. It pairs one model worker with deterministic authority checks, structured effects, verification, content-addressed evidence, recovery, and Mission-compatible readback.

The launch candidate deliberately excludes subagents, dynamic fan-out, workflow compilation, live provider qualification, publication, release, deployment, and AO migration. Current AO remains the production baseline.

## Workspace

The Rust workspace contains:

- `ao-next-core`: contracts, policy, effects, engine, verifiers, evidence, recovery, and terminal state.
- `ao-next-cli`: operator commands and machine-readable output without policy decisions.
- `ao-next-eval`: sealed offline comparison records for N0, N4, and N7.

[Codex and Claude runtime adapters](docs/runtime-adapters.md) are locally contract-tested without provider calls. `run-current-ao-baseline` executes N0 through AO2's sandbox adapter and digest-approved patch path, `run-direct-baseline` executes N4 through native Codex, and `run-live` executes N7 through the one-worker engine. All three commands stop before input resolution or process spawn unless `AO_NEXT_LIVE_PROVIDER_CALLS` is exactly `operator-authorized` in the operator process.

The [offline evaluation policy](docs/evaluation/decision-policy.md) compares three trials per task and variant. Offline and fake-process records can conclude only `AO_NEXT_NOT_YET_SUPERIOR` or `AO_NEXT_READY_FOR_LIVE_EVALUATION`. They cannot authorize promotion, fan-out, or a live-passed result.

## Local Verification

```sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

## Operator CLI

`ao-next run` executes only an explicitly supplied offline scripted plan. `inspect` validates a terminal readback, `verify-evidence` independently audits a sealed run, and `replay` computes a recovery plan without executing pending effects. `verify-corpus` checks the strict live-corpus identity and digest. `instantiate-corpus` derives a new exact-model corpus from a sealed parent and strict runtime bindings. `preflight-live-input` validates one scheduled input only when provider authorization is absent. `evaluate` validates a complete repeated-trial comparison but cannot emit a live-passed decision. `evaluate-live` calls the live-authorized evaluator and requires the same exact operator gate as the live runners.

The provider-gated live commands accept one [strict sealed-trial input](docs/live-evaluation-harness.md). They write one digest-bound run record to stdout, create private raw captures under the input's empty operator-owned capture directory, and write a concise summary to stderr. Tests call the internal execution path with injected fake runners; they never set the live-provider gate.

AO Mission's current canonical terminal-index consumer requires lease, root, and lineage evidence that a single AO Next readback does not contain. [The bounded compatibility proposal](docs/mission-compatibility.md) defines a separate read-only, digest-idempotent importer; this candidate does not change Mission or fabricate the missing artifacts.

Offline harness qualification may establish `AO_NEXT_LIVE_HARNESS_READY`. This means the harness is ready for a separately authorized pilot. It is not a live result, a superiority finding, or permission to replace current AO.
