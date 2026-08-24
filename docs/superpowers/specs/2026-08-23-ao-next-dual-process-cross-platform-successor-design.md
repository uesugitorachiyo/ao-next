# AO Next Dual-Process Cross-Platform Successor Design

- Status: approved design; Stage-0 implementation plan ready for execution choice
- Date: 2026-08-23
- Product repository: AO Next
- Lifecycle authority: AO Architecture
- Supported fallback: AO2
- Initial native platforms: Windows x86_64, macOS arm64, Ubuntu x86_64
- Stage-0 plan: [AO Next Engine Recovery Repair](../plans/2026-08-23-ao-next-engine-recovery-repair.md)

## Summary

AO Next should become a successor candidate through one repository that ships
two independently operated products:

- `ao-next-engine`, which validates and executes one bounded worker; and
- `ao-next-mission`, which records durable objectives, continuation, evidence,
  and operator state without executing repository mutations.

The products share versioned wire contracts, not memory, databases, locks, or
authority. Engine produces an append-only journal, retained provider captures,
verifier reports, evidence, and a local terminal result. Mission imports those
producer-owned artifacts read-only and derives a pure status projection.

The stopped Month-1 experiment proved the effect journal and Mission candidate
import provider-free, then exposed two predecessor defects in the first real
Windows journey: capture-index publication was not durable on Windows, and the
authority-bound expected Git base did not equal the Engine-created seed commit.
The provider returned successfully, no effect was admitted, and no file changed.
This design repairs those failures before moving Mission or building a plugin.

The terminal evidence is recorded in the
[AO Next Windows Successor-Slice Month 1 Decision](https://github.com/uesugitorachiyo/ao-mission/blob/2b0cb22c3cf05a3d4f8bd035d5d8780ae6567909/docs/roadmap/ao-next-windows-successor-slice-month1-decision.md).

## Current Baseline

The stable AO product remains the independently released AO2-based stack:

- AO2 `v0.5.12`;
- AO2 Control Plane `v0.1.19`;
- AO Mission `v0.1.6`;
- AO Command `v0.1.3`;
- AO Atlas `v0.2.1`;
- AO Forge `v0.1.5`; and
- AO Covenant `v0.1.1`.

AO Next has no public release and is excluded from the qualified 14-repository
stable development baseline. Its current Rust workspace contains
`ao-next-core`, `ao-next-cli`, and `ao-next-eval`. AO Mission remains an
independent Go repository with substantially more durable lifecycle behavior
than AO Next should attempt to recreate.

AO2 remains the supported execution, release, cross-host, legacy, and rollback
owner until an independent adoption decision assigns every retained
responsibility to a qualified successor.

## Goals

1. Make provider capture, native effects, verification, evidence, terminal
   publication, and restart recovery one ordered durable Engine state machine.
2. Guarantee that a retained provider result is never requested a second time.
3. Produce the actual deterministic Git base before exact execution authority
   is issued.
4. Host Engine and Mission in AO Next while preserving separate processes,
   state roots, failure domains, and authority enforcement.
5. Reuse the existing Go Mission implementation instead of rewriting its
   lifecycle and import semantics.
6. Publish separately verifiable Engine and Mission binaries for Windows,
   macOS, and Ubuntu from one compatible source tag.
7. Qualify clean installation, real one-worker execution, recovery, Mission
   import, upgrade, rollback, and removal on all three platforms.
8. Keep AO Architecture outside AO Next as the independent lifecycle and
   adoption authority.

## Non-Goals

- No provider retry after an unknown or retained provider outcome.
- No multi-worker execution, dynamic fan-out, planner/reviewer agents, or
  generic workflow compiler.
- No shared Engine/Mission database, in-process Mission plugin, or direct
  Mission access to Engine internals.
- No Rust rewrite of Mission during the initial repository migration.
- No AO2 retirement, compatibility removal, repository archival, or production
  route change.
- No Control Plane, Blueprint, Atlas, Foundry, Forge, Covenant, Sentinel, or
  Promoter reimplementation in the first successor candidate.
- No skills plugin, MCP server, custom UI, hot reload, or self-modifying plugin
  until the native two-process product is qualified.
- No release, publication, deployment, provider call, or credential change
  granted by this design.

## Alternatives Rejected

| Alternative | Reason rejected |
| --- | --- |
| One Engine/Mission binary | Combines execution and lifecycle state in one failure and authority domain, enabling accidental self-certification |
| Keep Engine and Mission in separate repositories indefinitely | Preserves today's ownership but does not provide the requested coherent product, compatibility manifest, or coordinated native release |
| Rewrite Mission in Rust during migration | Discards proven Go lifecycle, retention, import, and fail-closed behavior for no measured product benefit |
| Copy the full AO stack into AO Next | Recreates workflow, policy, assurance, and release systems before the two-process kernel is qualified |
| Build a plugin before native qualification | Productizes an unqualified backend and hides recovery gaps behind a friendlier surface |

## Repository Shape

Keep the current Rust workspace at the repository root to avoid a speculative
directory migration. Add Mission as a separate Go module:

```text
ao-next/
├── Cargo.toml
├── crates/
│   ├── ao-next-core/
│   ├── ao-next-cli/
│   └── ao-next-eval/
├── mission/
│   ├── go.mod
│   ├── cmd/ao-next-mission/
│   └── internal/mission/
├── docs/contracts/
├── packaging/
├── scripts/
└── tests/cross-platform/
```

The current `ao-next` binary remains a compatibility name during migration.
The first coordinated candidate release publishes:

- `ao-next-engine`, built from the current Rust CLI and core; and
- `ao-next-mission`, built from the imported Go Mission module.

Do not introduce a shared-language requirement. A repository can own a
coherent product without forcing both processes into one runtime.

## Component Responsibilities

| Responsibility | Engine | Mission |
| --- | --- | --- |
| Validate exact run request and authority | Owns | Records requested authority digest only |
| Spawn provider process | Owns, exactly once | Never |
| Admit and execute native effects | Owns | Never |
| Persist effect intent before execution | Owns | Never |
| Retain provider capture | Owns | Imports digest/readback only |
| Run mechanical verifier | Owns | Never |
| Publish evidence and terminal result | Owns | Imports read-only |
| Objective and Mission identity | Consumes exact identity | Owns |
| Route and continuation | Reports capabilities | Owns |
| Checkpoints and operator stop/pause | Reports run phase | Owns |
| Final adoption or release authority | Never | Never; AO Architecture/operator owns |

Mission must not approve Engine work merely because the processes share a
repository or release tag. Engine must independently validate every authority
field at intake and again immediately before each effect.

## Process And State Separation

Engine and Mission run as separate native processes. Each has one state root:

```text
Windows:
  %LOCALAPPDATA%\AO Next\engine
  %LOCALAPPDATA%\AO Next\mission

macOS and Ubuntu:
  $XDG_STATE_HOME/ao-next/engine
  $XDG_STATE_HOME/ao-next/mission
```

If `XDG_STATE_HOME` is absent, the platform-specific CLI chooses and reports a
documented user-state default. Neither binary silently discovers or opens the
other process's private state root.

The bridge is a bounded artifact exchange:

```text
Mission objective and exact authority reference
  -> Engine prepared-run receipt
  -> Engine append-only journal prefix
  -> Engine verifier report and terminal record
  -> Mission read-only import and pure projection
```

Local files are the first transport. Engine exports one immutable bundle to an
operator-selected exchange root that is distinct from both private state roots.
Mission imports only an explicitly supplied bundle path, retains the bytes in
its own content-addressed store, and never scans or opens the Engine state root.
A later authenticated service transport may carry the same bytes, but it must
not change contract or authority semantics.

## Durable Engine State Machine

### Journal events

Extend the append-only execution journal with provider-capture stages:

```text
run_admitted
provider_request_intent
provider_process_started
provider_output_retained
provider_capture_index_published
provider_capture_verified
adapter_turn_normalized
effect_intent
effect_completed | effect_completion_unknown | effect_denied
verification_started
verifier_recorded
terminal_published
```

Every event binds:

- run, request, source, workspace, authority, model, prompt, tool-contract,
  policy, verifier, and preceding-event digests;
- a strictly increasing sequence;
- the current one-worker identity; and
- event-specific content and retained-artifact digests.

No event may be reconstructed from a later terminal record. The journal is the
producer source of truth; terminal and Mission views are projections.

### Provider request rule

`provider_request_intent` must be durable before process spawn. The provider
process may start only when no later provider event exists for the run.

Recovery rules:

| Durable state | Recovery action |
| --- | --- |
| No provider intent | Provider may start under current exact authority |
| Intent, no retained capture | Provider outcome is unknown; stop for operator review |
| Retained capture, index not published | Repair publication from retained bytes; do not call provider |
| Published and verified capture | Normalize retained bytes; do not call provider |
| Normalized turn, no effect intent | Continue deterministic effect admission |
| Effect intent, no completion | Mark effect completion unknown; do not retry write |
| Effect completed | Reuse completion observation |
| Verifier started, no report | Re-run the same mechanical verifier with journal-owned attempt |
| Verifier report, no terminal | Publish the same terminal result idempotently |

The recovery command must reject any environment or option that would grant a
new provider process for a run with durable provider intent.

## Cross-Platform Capture Publication

The capture store owns a platform adapter with one semantic contract:

> Publish exactly one final capture index from create-new bytes. After success,
> a clean process can open the final name, verify its digest, and resume without
> the incomplete name. A crash at any point produces either no final index or
> one independently verifiable final index; it never causes provider retry.

### POSIX

1. Create the incomplete file with owner-only permissions and `create_new`.
2. Write canonical bytes and synchronize the file.
3. Publish the final name without overwriting an existing entry.
4. Synchronize the parent directory.
5. Remove the incomplete name and synchronize the directory again.

### Windows

Do not call `std::fs::File::open(directory).sync_all()`. The publication adapter
must use a Windows-native same-volume, no-overwrite atomic move with write-through
semantics, or an equivalent directory handle opened with the required directory
flags. The implementation must prove behavior on NTFS rather than accepting a
mocked filesystem result.

### Interrupted publication

If both incomplete and final names exist after restart:

1. Open both without following reparse points.
2. Require bounded regular files with identical bytes and expected identities.
3. Verify the canonical index and every referenced capture.
4. Remove only the redundant incomplete name.
5. Record `provider_capture_index_published` and
   `provider_capture_verified` without starting a provider.

Any contradiction fails closed and retains both names for audit.

## Prepared Run And Exact Authority

Split the current provider-free preflight from deterministic workspace
preparation.

```text
ao-next-engine prepare-live \
  --input <live-input.json> \
  --run-root <new-empty-root> \
  --out <prepared-run.json>
```

`prepare-live` performs no provider call. It:

1. Validates the sealed corpus, verifier anchors, model envelope, and authority
   exclusions.
2. Verifies the source snapshot and creates the deterministic Git seed in the
   target workspace.
3. Emits the actual Git root, common directory, branch, base commit, index
   digest, control digest, workspace digest, request digest, and run identity.
4. Records `provider_calls=0`, `safe_to_execute=false`, and a preparation
   expiry.

The operator issues exact execution authority only after verifying this
receipt. `run-live` accepts the receipt digest and rejects any Git or input
drift before recording provider intent.

Do not predict the Git base in a separate script. Engine owns the deterministic
Git procedure and reports the result that authority binds.

## Retained-Capture Recovery Command

```text
ao-next-engine recover-live \
  --run-root <existing-run-root> \
  --prepared-run <prepared-run.json> \
  --authority <exact-authority.json>
```

`recover-live`:

- validates the prepared-run, authority, journal, capture index, raw captures,
  Git identity, verifier profile, and every digest from a clean process;
- never reads ambient provider credentials;
- rejects `AO_NEXT_LIVE_PROVIDER_CALLS` and any provider-program override;
- resumes only from durable state allowed by the recovery table;
- records every new journal event before its corresponding effect; and
- returns one terminal record or one exact blocker.

An expired original authority may be used only to validate and retain existing
evidence. Recovery may finish non-mutating verification or terminal publication
from already completed effects, but it must not admit a pending mutation after
authority expiry. That run stops with an exact blocker; a later authority cannot
grant another provider process.

The original run and recovery process must produce the same terminal digest as
an uninterrupted run over identical provider output.

## Mission Import And Projection

Import the current Go Mission product into `mission/` with source history
preserved. The migration must first reproduce the current Mission gates without
behavioral changes.

Mission accepts a new producer contract:

```text
ao.next.execution-journal-prefix.v1
```

It contains or references:

- run and request identity;
- first and last sequence;
- prefix digest and preceding-prefix digest;
- prepared-run digest;
- provider capture-index digest when present;
- verifier-report digest when present;
- terminal digest when present; and
- all-false execution, approval, release, and authority-advance flags.

Mission retains the original bytes and derives one pure projection:

```text
prepared
provider_intent_recorded
provider_outcome_unknown
provider_captured
effects_pending
effect_outcome_unknown
verifying
passed
failed
stopped
```

Projection rules live in one deterministic package with no filesystem writes
or provider access. Exact reimport is idempotent. Changed bytes, sequence gaps,
identity drift, digest contradictions, unsafe paths, duplicate keys, or a
terminal state unsupported by the journal fail closed before Mission state
changes.

Mission durable source status remains separate from the Engine projection on
every Mission and Command-compatible view.

## Capability Surface

After Engine and Mission contracts stabilize, add read-only capability commands:

```text
ao-next-engine capabilities --json
ao-next-mission capabilities --json
```

They report:

- binary version and source commit;
- supported platforms and architectures;
- contract and schema versions;
- capture publication and retained-capture recovery versions;
- supported provider adapters without credential state;
- limits and denied capabilities;
- Mission projection versions; and
- compatible Engine/Mission version ranges.

Capabilities never report token values, credential locations, account IDs, or
private state paths. They are package inspection, not authority.

## Packaging And Releases

Use one repository tag and one compatibility manifest, but publish separate
archives:

```text
ao-next-engine-<version>-windows-x86_64.zip
ao-next-mission-<version>-windows-x86_64.zip
ao-next-engine-<version>-macos-aarch64.tar.gz
ao-next-mission-<version>-macos-aarch64.tar.gz
ao-next-engine-<version>-linux-x86_64.tar.gz
ao-next-mission-<version>-linux-x86_64.tar.gz
SHA256SUMS
SBOMs
provenance
compatibility-manifest.json
```

The first release is a candidate, not a supported AO successor. A combined
installer or service bundle is deferred until separate-binary installation,
upgrade, rollback, and removal are proven.

## Cross-Platform Qualification

### Provider-free matrix

Run the same source-owned suite on clean Windows x86_64, macOS arm64, and Ubuntu
x86_64 hosts:

- capture publication and every interrupted-publication state;
- retained-capture recovery without a provider executable or provider gate;
- deterministic prepared-run and exact Git base;
- effect-intent unknown, effect-completion reuse, verifier restart, and terminal
  idempotency;
- journal-prefix import and pure Mission projection;
- clean install, version/capability inspection, upgrade, rollback, and removal;
- paths containing spaces, non-ASCII names, symlinks, junctions, reparse points,
  stale locks, disk-full simulation, cancellation, and process termination; and
- independent manifest and evidence verification from a clean process.

Hosted Windows is necessary but not sufficient. Capture publication and
filesystem recovery must also pass on a physical NTFS Windows host.

### Real journeys

After provider-free closure, separately authorize one one-worker journey per
platform. Each journey requires an exact prepared-run receipt, target, base
commit, write scope, verifier, provider process count, token ceiling, rollback,
and evidence root.

Each platform must prove:

1. one provider process;
2. no duplicate effect;
3. mechanically verified terminal result;
4. independent terminal and evidence verification;
5. Mission journal-prefix import and matching status projection; and
6. recovery from one retained-capture interruption without another provider
   process.

No platform result substitutes for another.

## Migration Stages

### Stage 0: Engine recovery repair

- Fix Windows capture-index publication.
- Add provider-capture journal events.
- Add `prepare-live` and `recover-live`.
- Prove no-provider retained-capture recovery on all three platforms.

Exit gate: current AO Next repository gates and cross-platform recovery matrix
pass at one reviewed merged head. No Mission move, plugin, or provider call.

### Stage 1: Mission source migration

- Import the existing Go Mission source with history into `mission/`.
- Preserve current commands, contracts, tests, and public-safety behavior.
- Rename the candidate binary to `ao-next-mission` while retaining a temporary
  compatibility command for migration qualification.
- Add journal-prefix import and pure projection without duplicating Engine
  semantics.

Exit gate: old and new Mission binaries produce equivalent readbacks for the
frozen compatibility corpus; no execution authority changes.

### Stage 2: Coordinated candidate packaging

- Publish no artifacts yet.
- Build the two binaries and manifests reproducibly on the three platforms.
- Rehearse install, upgrade, rollback, removal, checksums, SBOMs, provenance,
  and compatibility validation.

Exit gate: clean-machine package rehearsal is green and independently verified.

### Stage 3: Real cross-platform qualification

- Run one separately authorized journey per platform.
- Exercise retained-capture recovery on each platform without a second provider
  call.
- Import the exact journal prefix into Mission and compare every view.

Exit gate: all three platform records and manifests verify with zero duplicate
effects, no unresolved critical safety issue, and no readback contradiction.

### Stage 4: AO2 shadow comparison

- Compare identical bounded tasks using AO2, direct Codex, and AO Next without
  applying multiple candidates to one target.
- Measure code size, operator interventions, completion time, provider usage,
  recovery outcomes, and cost of one additional capability.
- Keep AO2 as the explicit fallback; never switch after mutation begins.

Exit gate: an evidence-backed adoption proposal assigns every relevant AO2
responsibility an implemented successor, retained AO2 owner, blocker, or
non-goal.

### Stage 5: Independent adoption decision

AO Architecture records exactly one decision:

- `ADVANCE_AO_NEXT_DUAL_PROCESS_SUCCESSOR`;
- `RETAIN_AO2_WITH_AO_NEXT_EXPERIMENTAL`; or
- `STOP_AO_NEXT_SUCCESSOR`.

No decision automatically releases software, changes production routing,
retires AO2, publishes a plugin, or begins production migration.

## Plugin Boundary

The skills-only Codex plugin proposed in earlier research remains deferred.
After Stage 3, a plugin may wrap stable commands in this order:

```text
capabilities
prepare
run
status
recover
```

Do not expose corpus construction, N0/N4/N7 evaluation internals, raw provider
captures, low-level journal repair, or release controls as normal user skills.
Add MCP only if measured workflows require authenticated structured tools that
the CLI and skill cannot provide.

## Security And Public-Safety Requirements

- Provider credentials remain ambient to the separately authorized provider
  process and are never inspected, persisted, or exposed to the model.
- Hidden verifiers remain outside every model authority root.
- Raw provider output, private paths, target contents, account identifiers, and
  local evidence stay outside public Git.
- Windows reparse points and POSIX symlinks fail closed at every state-root,
  capture, journal, workspace, and retained-artifact boundary.
- All JSON inputs reject duplicate keys, unknown fields, oversized bytes,
  identity drift, and digest contradictions.
- Engine and Mission use create-new or exact-digest-idempotent writes and retain
  original locators separately from content-addressed bytes.
- A shared repository, release tag, or compatibility result never grants
  execution, approval, provider, release, deployment, publication, retirement,
  or adoption authority.

## Acceptance Criteria

The design is implemented only when all of the following are proven:

1. A Windows capture interruption identical to the stopped Month-1 failure
   resumes from retained bytes with zero additional provider processes.
2. `prepare-live` emits the exact Git base later observed by `run-live` and
   `recover-live` on all three platforms.
3. Every journal phase is append-only, identity-bound, sequence-bound, and
   independently verifiable.
4. Engine and Mission run as separate processes and cannot open each other's
   private state roots.
5. Mission's projection is reproducible from an immutable journal prefix and
   duplicates no Engine policy, recovery, verifier, or terminal logic.
6. Windows, macOS, and Ubuntu provider-free qualification passes from clean
   machines and paths containing spaces.
7. One real one-worker journey and retained-capture recovery pass per platform
   under separate exact authority.
8. Separate Engine and Mission packages install, inspect, upgrade, roll back,
   and uninstall without deleting retained evidence.
9. AO2 remains available and no silent fallback occurs.
10. AO Architecture independently accepts any successor claim.

## Rollback

Every stage is independently reversible:

- Stage 0 can revert Engine commits while retaining the failed Windows evidence.
- Stage 1 keeps the original AO Mission repository canonical until the imported
  module passes equivalence and Architecture records ownership transfer.
- Stage 2 publishes nothing; failed packaging rehearsals delete only run-owned
  candidate directories after evidence retention.
- Stage 3 mutates only disposable exact-scope targets and retains their seeds.
- Stage 4 keeps AO2 installed and qualified.
- Stage 5 may retain the dual-process candidate without adopting it.

Failure at any stage stops progression. Later stages require a new exact
handoff after the preceding evidence and entry gate verify.
