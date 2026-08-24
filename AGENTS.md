# AO Next Agent Instructions

## Status And Role

AO Next is an experimental, locally qualified execution candidate. It owns strict request intake, deterministic effect admission, exactly-one-worker direct execution, verification, content-addressed evidence, recovery, terminal readback, and offline comparison records. AO Mission supervises objectives and reconciliation but does not execute AO Next work.

AO Next is not established as superior, production-ready, released, published, deployed, or authorized to replace current AO.

## Sources Of Truth

- `README.md` defines the operator surface and qualification boundary.
- `docs/architecture.md` defines the thin-envelope architecture and non-goals.
- `docs/superpowers/specs/2026-08-23-ao-next-dual-process-cross-platform-successor-design.md` defines the proposed repair-first Engine/Mission monorepo target. It does not authorize implementation, provider use, release, adoption, or AO2 retirement.
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
- `run-current-ao-baseline`, `run-live`, `run-direct-baseline`, and `evaluate-live` must check `AO_NEXT_LIVE_PROVIDER_CALLS=operator-authorized` before resolving input or starting a process. Tests use injected fake runners and must not set this gate.
- Require an unexpired prepared receipt before N7 provider spawn. Record provider intent in the append-only journal before process creation.
- `recover-live` uses retained capture only. It must not resolve a provider and must reject the provider gate and provider-program overrides; provider intent without retained output is terminally unknown.
- Keep N0 bound to AO2's existing sandbox and digest-approved patch path. Never relabel an N4 or N7 row as current AO.
- Retain raw provider captures only under the input's empty operator-owned private capture directory outside every worker authority root.
- Keep sealed hidden tests outside worker workspaces and authority roots. Never copy hidden bytes or paths into prompts, effect observations, logs, or public evidence.

## Working Method

- Use test-driven development: observe the focused test fail for the intended missing behavior, implement the minimum change, and re-run focused and workspace tests.
- Preserve exact request, source, workspace, model, prompt, tool, policy, effect, verifier, artifact, checkpoint, and terminal digests.
- For live-harness workspaces, validate sealed product bytes before creating one ordinary deterministic root Git repository. Bind and recheck the canonical root, common directory, branch, seed `HEAD`, and clean status immediately before provider spawn; exclude only that separately validated root `.git` directory from product snapshots.
- Keep `preflight-live-input` read-only with respect to the live workspace. Git initialization belongs to the exact live execution so a successful preflight remains composable with that execution.
- Live execution and preflight require operator-owned corpus and verifier-profile digest anchors outside the input JSON. Campaign qualification additionally requires a digest-bound local fake executable and must derive evidence from actual row execution rather than caller-authored claims.
- Keep recovery distinct from model repair attempts. Evaluators must derive provider-free recovery qualification from their own deterministic probes in a fresh operator-owned evidence root, bind the exact sealed-live corpus, complete N7 adapter digest set, checkpoint replay, duplicate-effect prevention, and zero live-provider boundary, and emit the derived digest. Never accept a recovery receipt or digest from the comparison document or CLI input.
- The real N7 path must bind an append-only journal before worker dispatch, persist exact effect intent before native execution, treat intent without durable completion as unknown without automatic retry, and durably record verifier and content-addressed terminal publication events.
- The functional N7 sentinel may replace only `greenfield-engineering-app` with `greenfield-native-write-sentinel` in an otherwise exact sealed live corpus. Keep that alternate validator out of evaluation and campaign qualification paths.
- Before live workspace preparation or provider spawn, derive `2 * context_limit + 2 * output_limit` with checked arithmetic and reject a lower `max_tokens`. After the provider returns, retain exact bounded stdout and stderr in the operator-owned private capture root, atomically publish and verify one immutable capture index, normalize one trusted terminal usage envelope, reject contradictory or overflowed counters, and enforce `max_tokens` before AO2 preview/apply, admitted N7 effects, verification, or public evidence assembly. N4 may mutate its workspace inside the native provider process; the gate still precedes its verifier and evidence assembly. Preserve private failure-stage metadata without treating retained output as a successful measurement.
- Reject duplicate keys, unknown contract fields, oversized input, stale authority, unsafe paths, symlinks, non-regular files, identity drift, digest mismatch, and terminal contradictions.
- Keep native effects and deterministic Git workspace preparation functional on Linux, macOS, and Windows. Preserve descriptor-relative Unix traversal and handle-anchored Windows identity checks; never replace either with unchecked canonical-path access.
- Publication changes require physical Windows qualification on a new empty NTFS root whose path contains spaces, with result JSON outside the checkout.
- Store retained evidence beneath digest-addressed paths and preserve the original locator separately.
- Keep `target/`, `.ao-next/`, artifacts, logs, evaluation output, and local transcripts out of source changes.
- If durable commands, architecture, authority, or lifecycle ownership changes, update this file in the same commit.

## Verification

Run focused tests first, then:

```sh
bash tests/bootstrap_contract.sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

Run schema drift, deterministic replay, recovery, Mission compatibility, and independent evidence-manifest checks when their surfaces change. Record skipped, unavailable, networked, credentialed, or failed checks explicitly.

## Completion

Local harness completion requires every local contract, negative fixture, verifier, evidence, recovery, CLI, adapter, corpus, repeated-trial, manifest, and reconciliation gate to pass at one exact source head. The terminal state `AO_NEXT_LIVE_HARNESS_READY` means only that a separately authorized live pilot can begin. It does not imply superiority, promotion, production readiness, or a live evaluation result.
