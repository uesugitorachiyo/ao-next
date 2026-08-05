# AO Next

AO Next is a local experimental execution kernel for bounded engineering work. It pairs one model worker with deterministic authority checks, structured effects, verification, content-addressed evidence, recovery, and Mission-compatible readback.

The launch candidate deliberately excludes subagents, dynamic fan-out, workflow compilation, live provider qualification, publication, release, deployment, and AO migration. Current AO remains the production baseline.

## Workspace

The Rust workspace contains:

- `ao-next-core`: contracts, policy, effects, engine, verifiers, evidence, recovery, and terminal state.
- `ao-next-cli`: operator commands and machine-readable output without policy decisions.
- `ao-next-eval`: sealed offline comparison records for N0, N4, and N7.

[Codex and Claude runtime adapters](docs/runtime-adapters.md) are locally contract-tested without provider calls. Their live smoke tests remain ignored behind a separate operator-only authorization gate.

The [offline evaluation policy](docs/evaluation/decision-policy.md) compares sealed N0/N4/N7 rows and can conclude only `AO_NEXT_NOT_YET_SUPERIOR` or `AO_NEXT_READY_FOR_LIVE_EVALUATION`. It cannot authorize promotion, fan-out, or a live-passed result.

## Local Verification

```sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

## Operator CLI

`ao-next run` executes only an explicitly supplied offline scripted plan. `inspect` validates a terminal readback, `verify-evidence` independently audits a sealed run, and `replay` computes a recovery plan without executing pending effects. `evaluate` is reserved for the sealed phase-9 comparison harness. Every command writes one JSON value to stdout and a concise operator summary to stderr.

AO Mission's current canonical terminal-index consumer requires lease, root, and lineage evidence that a single AO Next readback does not contain. [The bounded compatibility proposal](docs/mission-compatibility.md) defines a separate read-only, digest-idempotent importer; this candidate does not change Mission or fabricate the missing artifacts.

Local qualification may establish only `AO_NEXT_READY_FOR_LIVE_EVALUATION`. Live runtime measurements and any replacement decision require separate authorization and evidence.
