# AO Next Agent Instructions

## Status And Role

AO Next is an experimental, locally qualified execution candidate. It owns strict request intake, deterministic effect admission, exactly-one-worker direct execution, verification, content-addressed evidence, recovery, terminal readback, and offline comparison records. AO Mission supervises objectives and reconciliation but does not execute AO Next work.

AO Next is not established as superior, production-ready, released, published, deployed, or authorized to replace current AO.

## Sources Of Truth

- `README.md` defines the operator surface and qualification boundary.
- `docs/architecture.md` defines the thin-envelope architecture and non-goals.
- `docs/contracts/*.schema.json` and Rust types in `ao-next-core` jointly own checked-in wire contracts; drift must fail verification.
- `docs/evaluation/` owns paired comparison protocol and decision policy.
- Core module tests own state, policy, effect, evidence, recovery, and terminal behavior.

## Authority And Safety

- Direct mode uses exactly one runtime worker identity. Do not add subagents, fan-out, runtime graph growth, planner/reviewer roles, workflow compilation, or a generic workflow language without a later evidence-backed architecture decision.
- Treat adapter, model, scheduler, workspace, checkpoint, artifact, and Mission inputs as untrusted.
- The model cannot grant authority, change policy, remove verifiers, rewrite committed evidence, or declare terminal success.
- Admit effects before execution. Use structured program and argument arrays; never evaluate model output through a shell or as code, YAML, templates, or workflow source.
- Deny network, credentials, remote mutation, release, deployment, publication, and other external effects unless a later operator-authored authority envelope separately grants the exact capability.
- Keep provider transcripts, credentials, account identifiers, private paths, and unredacted workspace content out of tracked files.
- Live provider calls, GitHub publication, releases, deployments, credential changes, AO migration, and production-readiness claims require separate explicit authority.

## Working Method

- Use test-driven development: observe the focused test fail for the intended missing behavior, implement the minimum change, and re-run focused and workspace tests.
- Preserve exact request, source, workspace, model, prompt, tool, policy, effect, verifier, artifact, checkpoint, and terminal digests.
- Reject duplicate keys, unknown contract fields, oversized input, stale authority, unsafe paths, symlinks, non-regular files, identity drift, digest mismatch, and terminal contradictions.
- Store retained evidence beneath digest-addressed paths and preserve the original locator separately.
- Keep `target/`, `.ao-next/`, artifacts, logs, evaluation output, and local transcripts out of source changes.
- If durable commands, architecture, authority, or lifecycle ownership changes, update this file in the same commit.

## Verification

Run focused tests first, then:

```sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

Run schema drift, deterministic replay, recovery, Mission compatibility, and independent evidence-manifest checks when their surfaces change. Record skipped, unavailable, networked, credentialed, or failed checks explicitly.

## Completion

Local completion requires every local contract, negative fixture, verifier, evidence, recovery, CLI, adapter, evaluation, manifest, and reconciliation gate to pass at one exact source head. The honest local terminal state is `AO_NEXT_READY_FOR_LIVE_EVALUATION`; it does not imply superiority or live evaluation success.
