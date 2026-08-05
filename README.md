# AO Next

AO Next is a local experimental execution kernel for bounded engineering work. It pairs one model worker with deterministic authority checks, structured effects, verification, content-addressed evidence, recovery, and Mission-compatible readback.

The launch candidate deliberately excludes subagents, dynamic fan-out, workflow compilation, live provider qualification, publication, release, deployment, and AO migration. Current AO remains the production baseline.

## Workspace

The Rust workspace contains:

- `ao-next-core`: contracts, policy, effects, engine, verifiers, evidence, recovery, and terminal state.
- `ao-next-cli`: operator commands and machine-readable output without policy decisions.
- `ao-next-eval`: sealed offline comparison records for N0, N4, and N7.

## Local Verification

```sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

Local qualification may establish only `AO_NEXT_READY_FOR_LIVE_EVALUATION`. Live runtime measurements and any replacement decision require separate authorization and evidence.
