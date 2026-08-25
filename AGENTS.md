# AO Next Agent Instructions

## Status And Role

AO Next is an experimental, locally qualified execution candidate. It owns strict request intake, deterministic effect admission, exactly-one-worker direct execution, verification, content-addressed evidence, recovery, terminal readback, and offline comparison records. AO Mission supervises objectives and reconciliation but does not execute AO Next work.

AO Next is not established as superior, production-ready, released, published, deployed, or authorized to replace current AO.

## Sources Of Truth

- `README.md` defines the operator surface and qualification boundary.
- `docs/architecture.md` defines the thin-envelope architecture and non-goals.
- `docs/superpowers/specs/2026-08-23-ao-next-dual-process-cross-platform-successor-design.md` defines the approved repair-first Engine/Mission target. It grants no provider, release, adoption, or AO2-retirement authority.
- `docs/mission-source-migration.md` owns the public Stage 1 history-import, process-separation, journal-prefix, projection, equivalence, rollback, and source-ownership contract.
- `tests/fixtures/mission-migration/corpus-v1.json` freezes the exact canonical Go Mission source inventory and behavior operations that old/new equivalence must preserve.
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
- Treat the authority envelope embedded in an N7 preparation input as requested scope only. `prepare-live` validates its identity and exclusions without granting execution freshness.
- Require an unexpired prepared receipt and a strict `ao.next.n7-execution-authority.v1` document issued after preparation before N7 provider spawn. Bind the document to the receipt digest, preparation input/request, observed Git base, workspace, requested scope, one provider process, and issue/expiry interval. Record provider intent before process creation.
- `recover-live` uses retained capture and the separately issued N7 authority only. It must not resolve a provider and must reject the provider gate and provider-program overrides; provider intent without retained output and effect intent without completion are terminally unknown. Completed effects remain eligible for verification after authority expiry.
- Keep N0 bound to AO2's existing sandbox and digest-approved patch path. Never relabel an N4 or N7 row as current AO.
- Retain raw provider captures only under the input's empty operator-owned private capture directory outside every worker authority root.
- Keep sealed hidden tests outside worker workspaces and authority roots. Never copy hidden bytes or paths into prompts, effect observations, logs, or public evidence.
- Repository co-location grants no authority. Keep Rust Engine and Go Mission as separate binaries, processes, state roots, and failure domains; neither process may discover or open the other's private state.
- Mission may import only an explicitly supplied, immutable, digest-bound `ao.next.execution-journal-prefix.v1` file. Import and projection are read-only: they never scan Engine state, call a provider, execute an effect, or change durable Mission source status.

## Working Method

- Use test-driven development: observe the focused test fail for the intended missing behavior, implement the minimum change, and re-run focused and workspace tests.
- Preserve exact request, source, workspace, model, prompt, tool, policy, effect, verifier, artifact, checkpoint, and terminal digests.
- For live-harness workspaces, validate sealed product bytes before creating one ordinary deterministic root Git repository. Bind and recheck the canonical root, common directory, branch, seed `HEAD`, and clean status immediately before provider spawn; exclude only that separately validated root `.git` directory from product snapshots.
- Keep `preflight-live-input` read-only with respect to the live workspace. `prepare-live` owns deterministic Git initialization and receipt emission; `run-live` consumes that exact receipt.
- Live execution and preflight require operator-owned corpus and verifier-profile digest anchors outside the input JSON. Campaign qualification additionally requires a digest-bound local fake executable and must derive evidence from actual row execution rather than caller-authored claims.
- Keep recovery distinct from model repair attempts. Evaluators must derive provider-free recovery qualification from their own deterministic probes in a fresh operator-owned evidence root, bind the exact sealed-live corpus, complete N7 adapter digest set, checkpoint replay, duplicate-effect prevention, and zero live-provider boundary, and emit the derived digest. Never accept a recovery receipt or digest from the comparison document or CLI input.
- The real N7 path must bind an append-only journal before worker dispatch, recheck N7 authority freshness immediately before each fresh effect intent, persist exact effect intent before native execution, treat intent without durable completion as unknown without automatic retry, and durably record verifier and content-addressed terminal publication events. One semantic lifecycle validator must govern loaded prefixes, appends, checkpoints, verifier records, and terminal publication.
- Recovery must carry one existing-only request-bound journal from authority comparison through effects, verification, and terminal publication. Never recreate its root, identity, or event directory; reject Windows reparse points and Unix symlinks at the root, identity, event directory, and every retained event file. On Unix, append through the retained event-directory descriptor and fail if its public locator changes.
- The functional N7 sentinel may replace only `greenfield-engineering-app` with `greenfield-native-write-sentinel` in an otherwise exact sealed live corpus. Keep that alternate validator out of evaluation and campaign qualification paths.
- Before live workspace preparation or provider spawn, derive `2 * context_limit + 2 * output_limit` with checked arithmetic and reject a lower `max_tokens`. After the provider returns, retain exact bounded stdout and stderr, synchronize an owner-only canonical incomplete index, journal `provider_output_retained`, publish and verify the staged final index, then normalize one trusted terminal usage envelope. Reject contradictory or overflowed counters and enforce `max_tokens` before AO2 preview/apply, admitted N7 effects, verification, or public evidence assembly. N4 may mutate its workspace inside the native provider process; the gate still precedes its verifier and evidence assembly. Preserve private failure-stage metadata without treating retained output as a successful measurement.
- Reject duplicate keys, unknown contract fields, oversized input, stale authority, unsafe paths, symlinks, non-regular files, identity drift, digest mismatch, and terminal contradictions.
- Preserve canonical Go Mission ancestry during migration. Import the exact source as a two-parent merge under `mission/` without squash, cherry-pick replay, or path-history rewriting; verify the second parent and imported tree object before changing imported bytes.
- Keep `ao-next-mission` behavior equivalent to the temporary `ao-mission` compatibility command through the frozen corpus. Resolve generic contract discriminators consistently across `schema`, `schema_version`, and `contract_version`, and reject conflicting values.
- Keep native effects and deterministic Git workspace preparation functional on Linux, macOS, and Windows. Reject Windows reparse points at workspace and provider-visible roots and every nested entry. Preserve descriptor-relative Unix traversal and handle-anchored Windows identity checks; never replace either with unchecked canonical-path access.
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

When `mission/` exists, also run:

```sh
(cd mission && gofmt -d cmd internal)
(cd mission && go test ./... -count=1)
(cd mission && go vet ./...)
(cd mission && go build ./cmd/ao-mission)
(cd mission && go build ./cmd/ao-next-mission)
(cd mission && python3 scripts/test_public_safety_scan.py)
```

Run schema drift, deterministic replay, recovery, Mission compatibility, and independent evidence-manifest checks when their surfaces change. Record skipped, unavailable, networked, credentialed, or failed checks explicitly.

## Completion

Local harness completion requires every local contract, negative fixture, verifier, evidence, recovery, CLI, adapter, corpus, repeated-trial, manifest, and reconciliation gate to pass at one exact source head. The terminal state `AO_NEXT_LIVE_HARNESS_READY` means only that a separately authorized live pilot can begin. It does not imply superiority, promotion, production readiness, or a live evaluation result.

Mission source migration reaches `MISSION_SOURCE_MIGRATION_READY_FOR_PACKAGING` only after canonical ancestry, frozen old/new behavior, journal-prefix import and projection, required three-platform provider-free gates, independent review, reviewed merge, post-merge checks, and Mission reconciliation pass at one exact head. That result authorizes only the next packaging-entry check; it grants no release, deployment, provider, adoption, publication, or AO2-retirement authority.
