# AO Next Engine Recovery Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make AO Next prepare the exact Git base before provider authority, durably journal provider capture, and recover a retained provider result without a second provider process on Windows, macOS, and Ubuntu.

**Architecture:** Add a safe cross-platform capture-index store to `ao-next-core`, extend the existing append-only journal with ordered provider stages, and require an immutable prepared-run receipt for N7. Fresh execution retains and journals provider output before normalization; `recover-live` validates the same receipt, journal, capture index, and Git identity, then continues from retained bytes with no provider executable or provider gate.

**Tech Stack:** Rust 1.95, standard-library filesystem APIs, existing `rustix`, serde/serde_json, schemars, sha2, clap, Cargo tests, GitHub Actions, and physical Windows NTFS qualification.

**Spec:** `docs/superpowers/specs/2026-08-23-ao-next-dual-process-cross-platform-successor-design.md`

## Global Constraints

- Stage 0 changes the current Rust Engine only; do not move Mission source, add Go, package releases, build a plugin, or add MCP.
- Use exactly one worker and at most one provider process per run.
- Persist `provider_request_intent` before process spawn.
- A retained provider capture must never cause another provider process.
- Intent without retained provider bytes is an unknown provider outcome and stops for operator review.
- Intent without effect completion is an unknown effect outcome and never retries a write.
- `prepare-live` performs zero provider calls and emits the actual Git base that later authority binds.
- `recover-live` rejects `AO_NEXT_LIVE_PROVIDER_CALLS` and every provider-program override.
- Engine continues to reject network, credential, remote mutation, release, deployment, and publication effects.
- Windows x86_64, macOS arm64, and Ubuntu x86_64 are required native platforms; hosted Windows does not replace physical NTFS verification.
- Use no new dependency for Windows filesystem publication. Reuse safe standard-library patterns already present in `ao-next-core/src/effects.rs`.
- Preserve strict JSON, duplicate-key rejection, bounded files, non-symlink/reparse containment, exact digests, and create-new/idempotent semantics.
- Keep raw captures, provider output, private paths, authority files, and local evidence outside public Git.
- Run one mutation task at a time. Provider calls, releases, publication, deployment, AO2 retirement, and production routing remain unauthorized.

## Plan Ownership And Follow-On Work

AO Next owns this Stage-0 implementation plan because every mutation is in the
AO Next Engine repository. Codex executes the plan but is not durable program
state. AO Mission will own a new cross-stage roadmap and continuation handoff
only after Stage 0 records `ENGINE_RECOVERY_READY_FOR_MISSION_MIGRATION`.

The approved design's remaining subsystems require separate plans:

1. AO Next Mission source migration and journal-prefix projection;
2. coordinated Engine/Mission packaging;
3. separately authorized real cross-platform qualification; and
4. AO2 shadow comparison and the independent adoption decision.

None of those plans is implicitly authorized by this file.

## Execution Model Profile

Use explicit model and effort settings for every implementer and reviewer. Do
not rely on inherited defaults or silently substitute another model.

| Work | Implementer | Reviewer |
| --- | --- | --- |
| Plan controller and ordinary integration coordination | `gpt-5.6-sol`, `high` | not applicable |
| Task 1 capture publication and Windows durability | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| Task 2 provider journal state and no-retry ordering | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| Task 3 prepared Git identity and authority binding | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| Task 4 fresh N7 integration | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| Task 5 retained-capture recovery and expired authority | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| Task 6 mechanical docs and CI edits | `gpt-5.6-terra`, `medium` | `gpt-5.6-sol`, `high` |
| Task 6 cross-platform evidence interpretation | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| Task 7 final whole-branch review and Stage-0 decision | no implementer unless review finds a defect | `gpt-5.6-sol`, `xhigh` |

Use `gpt-5.6-terra` only after the relevant interface is frozen and the task is
mechanical. Do not use a fast/low-cost model for capture durability, journal
ordering, authority freshness, no-retry recovery, or final acceptance. If an
explicit model/effort pair is unavailable, stop before mutation and request an
operator decision.

Stage 0 performs no live provider call. In the later real-journey plan, the AO
Next provider profile should use `gpt-5.6-sol` with `high` effort, one turn, one
worker, and one exact provider-process allowance. Architecture, recovery, and
adoption review remain `gpt-5.6-sol` with `xhigh` effort outside the provider
run.

---

## File Map

| File | Responsibility |
| --- | --- |
| `crates/ao-next-core/src/capture.rs` | Bounded capture-index publication, interrupted-pair repair, and platform durability |
| `crates/ao-next-core/src/lib.rs` | Expose the capture module |
| `crates/ao-next-core/src/recovery.rs` | Provider journal events, strict ordering, and provider recovery state |
| `crates/ao-next-core/src/contracts.rs` | Prepared-run receipt and Git workspace identity contract |
| `crates/ao-next-core/tests/capture_store.rs` | Capture-store publication, interruption, contradiction, and containment tests |
| `crates/ao-next-core/tests/evidence_recovery.rs` | Provider journal sequencing and recovery tests |
| `docs/contracts/prepared-run-v1.schema.json` | Checked-in prepared-run wire schema |
| `crates/ao-next-cli/src/commands/mod.rs` | `prepare-live` and `recover-live` CLI arguments and dispatch |
| `crates/ao-next-cli/src/commands/live.rs` | Existing live validation, Git identity, fresh N7 wiring, and retained-output continuation |
| `crates/ao-next-cli/src/commands/live_prepare.rs` | Provider-free preparation and create-new receipt output |
| `crates/ao-next-cli/src/commands/live_recover.rs` | No-provider recovery and retained-capture runner |
| `crates/ao-next-cli/tests/cli.rs` | Machine-readable CLI, provider-count sentinels, and end-to-end recovery |
| `.github/workflows/ci.yml` | Required Linux, macOS, and Windows Stage-0 tests |
| `README.md` | Operator commands and authority boundaries |
| `docs/architecture.md` | Durable provider-capture and prepared-run architecture |
| `docs/live-evaluation-harness.md` | Fresh and recovery operator flow |
| `docs/runtime-adapters.md` | Retained-output normalization and no-provider recovery |
| `AGENTS.md` | Durable command, recovery, and authority rules |
| `tests/cross-platform/README.md` | Exact physical-Windows and native-host qualification procedure |

### Task 1: Cross-Platform Capture Index Store

**Files:**
- Create: `crates/ao-next-core/src/capture.rs`
- Modify: `crates/ao-next-core/src/lib.rs`
- Create: `crates/ao-next-core/tests/capture_store.rs`

**Interfaces:**
- Consumes: `ao_next_core::contracts::Digest`, `ao_next_core::evidence::digest_bytes`
- Produces: `CaptureIndexStore::open`, `CaptureIndexStore::publish`, `CaptureIndexStore::recover`, `CapturePublication`

- [ ] **Step 1: Write failing public capture-store tests**

Create `crates/ao-next-core/tests/capture_store.rs`:

```rust
use ao_next_core::capture::{CaptureIndexStore, CapturePublication};
use ao_next_core::evidence::digest_bytes;
use tempfile::TempDir;

fn store() -> (TempDir, CaptureIndexStore) {
    let root = TempDir::new().expect("capture root");
    let store = CaptureIndexStore::open(root.path().to_path_buf(), 1024)
        .expect("capture store");
    (root, store)
}

#[test]
fn publish_creates_one_final_index_and_removes_incomplete_name() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;
    let result = store.publish(bytes).expect("publish");
    assert_eq!(result, CapturePublication::Published(digest_bytes(bytes)));
    assert_eq!(
        std::fs::read(root.path().join("capture-index.json")).expect("final index"),
        bytes
    );
    assert!(!root.path().join("capture-index.json.incomplete").exists());
}

#[test]
fn recover_removes_an_identical_incomplete_name_without_rewriting_final() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;
    let incomplete = root.path().join("capture-index.json.incomplete");
    let final_path = root.path().join("capture-index.json");
    std::fs::write(&incomplete, bytes).expect("incomplete");
    std::fs::hard_link(&incomplete, &final_path).expect("published hard link");
    let result = store
        .recover(&digest_bytes(bytes))
        .expect("repair published pair");
    assert_eq!(result, CapturePublication::Repaired(digest_bytes(bytes)));
    assert_eq!(std::fs::read(final_path).expect("final index"), bytes);
    assert!(!incomplete.exists());
}

#[test]
fn recover_publishes_a_verified_incomplete_only_index() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;
    std::fs::write(root.path().join("capture-index.json.incomplete"), bytes)
        .expect("incomplete");
    let result = store.recover(&digest_bytes(bytes)).expect("repair");
    assert_eq!(result, CapturePublication::Repaired(digest_bytes(bytes)));
    assert_eq!(
        std::fs::read(root.path().join("capture-index.json")).expect("final index"),
        bytes
    );
    assert!(!root.path().join("capture-index.json.incomplete").exists());
}

#[test]
fn recover_rejects_contradictory_final_and_incomplete_bytes() {
    let (root, store) = store();
    std::fs::write(root.path().join("capture-index.json"), b"final")
        .expect("final");
    std::fs::write(
        root.path().join("capture-index.json.incomplete"),
        b"different",
    )
    .expect("incomplete");
    assert!(store.recover(&digest_bytes(b"final")).is_err());
    assert!(root.path().join("capture-index.json").exists());
    assert!(root.path().join("capture-index.json.incomplete").exists());
}
```

- [ ] **Step 2: Run tests and confirm the module is missing**

Run:

```bash
cargo test -p ao-next-core --test capture_store -- --nocapture
```

Expected: compilation fails because `ao_next_core::capture` does not exist.

- [ ] **Step 3: Implement the bounded store and platform publication**

Create `crates/ao-next-core/src/capture.rs` with:

```rust
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum CapturePublication {
    Published(Digest),
    Repaired(Digest),
}

impl CapturePublication {
    #[must_use]
    pub const fn digest(&self) -> &Digest {
        match self {
            Self::Published(digest) | Self::Repaired(digest) => digest,
        }
    }
}

#[derive(Debug, Error)]
pub enum CaptureStoreError {
    #[error("capture store I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("capture root or index path is unsafe")]
    UnsafePath,
    #[error("capture index exceeds {limit} bytes: {actual}")]
    Oversized { actual: u64, limit: u64 },
    #[error("capture index bytes or digest are contradictory")]
    Contradictory,
    #[error("capture index publication is incomplete")]
    Incomplete,
}

#[derive(Clone, Debug)]
pub struct CaptureIndexStore {
    root: PathBuf,
    maximum_bytes: u64,
}
```

Implement these signatures:

```rust
impl CaptureIndexStore {
    pub fn open(root: PathBuf, maximum_bytes: u64) -> Result<Self, CaptureStoreError>;
    pub fn publish(&self, bytes: &[u8]) -> Result<CapturePublication, CaptureStoreError>;
    pub fn recover(&self, expected: &Digest) -> Result<CapturePublication, CaptureStoreError>;
}
```

`open` requires an existing non-symlink directory and a positive byte limit.
`publish` requires both names absent, writes and syncs the incomplete file with
`create_new`, then calls a private platform `publish_final` function.
`recover` implements this state table:

```rust
match (final_path.exists(), incomplete.exists()) {
    (false, false) => Err(CaptureStoreError::Incomplete),
    (false, true) => {
        let bytes = read_bounded_regular(&incomplete)?;
        if digest_bytes(&bytes) != *expected {
            return Err(CaptureStoreError::Contradictory);
        }
        publish_final(&incomplete, &final_path)?;
        Ok(CapturePublication::Repaired(expected.clone()))
    }
    (true, false) => verify_final(expected).map(CapturePublication::Published),
    (true, true) => {
        let final_bytes = read_bounded_regular(&final_path)?;
        let incomplete_bytes = read_bounded_regular(&incomplete)?;
        if final_bytes != incomplete_bytes || digest_bytes(&final_bytes) != *expected {
            return Err(CaptureStoreError::Contradictory);
        }
        remove_incomplete_and_sync(&incomplete, &final_path)?;
        Ok(CapturePublication::Repaired(expected.clone()))
    }
}
```

Use these platform rules:

```rust
#[cfg(unix)]
fn publish_final(incomplete: &Path, final_path: &Path) -> std::io::Result<()> {
    std::fs::hard_link(incomplete, final_path)?;
    std::fs::File::open(final_path.parent().expect("validated parent"))?.sync_all()?;
    std::fs::remove_file(incomplete)?;
    std::fs::File::open(final_path.parent().expect("validated parent"))?.sync_all()
}

#[cfg(windows)]
fn publish_final(incomplete: &Path, final_path: &Path) -> std::io::Result<()> {
    std::fs::hard_link(incomplete, final_path)?;
    std::fs::remove_file(incomplete)?;
    sync_windows_regular(final_path)
}
```

`sync_windows_regular` copies the safe `OpenOptionsExt`,
`FILE_FLAG_OPEN_REPARSE_POINT`, reparse-point rejection, and `sync_all` pattern
from `ao-next-core/src/effects.rs`. Do not open or sync the Windows directory.

Add `pub mod capture;` to `crates/ao-next-core/src/lib.rs`.

- [ ] **Step 4: Add module-private interruption and containment tests**

Inside `capture.rs`, test incomplete-only state, final-only exact replay,
oversized bytes, symlinked root, symlinked final, and duplicate publication.
Every contradiction retains both original files.

- [ ] **Step 5: Run core gates**

```bash
cargo fmt --check
cargo test -p ao-next-core --test capture_store -- --nocapture
cargo test -p ao-next-core
cargo clippy -p ao-next-core --all-targets -- -D warnings
```

Expected: pass with no warnings.

- [ ] **Step 6: Commit**

```bash
git add crates/ao-next-core/src/capture.rs crates/ao-next-core/src/lib.rs crates/ao-next-core/tests/capture_store.rs
git commit -m "fix: publish capture indexes durably across platforms"
```

### Task 2: Provider-Capture Journal State

**Files:**
- Modify: `crates/ao-next-core/src/recovery.rs`
- Modify: `crates/ao-next-core/tests/evidence_recovery.rs`

**Interfaces:**
- Consumes: existing `CheckpointJournal`, `CheckpointIdentity`, `Digest`
- Produces: provider `JournalEventKind` variants, `ProviderJournalState`, and provider record methods

- [ ] **Step 1: Write failing provider-state tests**

Add to `crates/ao-next-core/tests/evidence_recovery.rs`:

```rust
#[test]
fn provider_capture_events_are_ordered_before_effect_intent() {
    let fixture = fixture();
    let prepared = digest_bytes(b"prepared");
    let invocation = digest_bytes(b"invocation");
    let raw = digest_bytes(b"raw-capture");
    let index = digest_bytes(b"capture-index");
    let turn = digest_bytes(b"adapter-turn");

    fixture.journal
        .record_provider_request_intent(&fixture.request, &prepared)
        .expect("intent");
    fixture.journal
        .record_provider_process_started(&fixture.request, &invocation)
        .expect("started");
    fixture.journal
        .record_provider_output_retained(&fixture.request, &raw)
        .expect("retained");
    fixture.journal
        .record_provider_capture_published(&fixture.request, &index)
        .expect("published");
    fixture.journal
        .record_provider_capture_verified(&fixture.request, &index)
        .expect("verified");
    fixture.journal
        .record_adapter_turn_normalized(&fixture.request, &turn)
        .expect("normalized");

    let state = fixture.journal.provider_state(&fixture.request).expect("state");
    assert_eq!(state.prepared_run_digest, Some(prepared));
    assert_eq!(state.capture_index_digest, Some(index));
    assert_eq!(state.adapter_turn_digest, Some(turn));
    assert!(state.provider_process_started);
}

#[test]
fn provider_intent_without_capture_is_unknown_and_cannot_restart() {
    let fixture = fixture();
    fixture.journal
        .record_provider_request_intent(&fixture.request, &digest_bytes(b"prepared"))
        .expect("intent");
    let state = fixture.journal.provider_state(&fixture.request).expect("state");
    assert!(state.outcome_unknown());
    assert!(fixture.journal.provider_may_start(&fixture.request).is_err());
}

#[test]
fn provider_event_reordering_digest_drift_and_duplicates_fail_closed() {
    let fixture = fixture();
    assert!(fixture.journal
        .record_provider_output_retained(&fixture.request, &digest_bytes(b"raw"))
        .is_err());
    fixture.journal
        .record_provider_request_intent(&fixture.request, &digest_bytes(b"prepared"))
        .expect("intent");
    assert!(fixture.journal
        .record_provider_request_intent(&fixture.request, &digest_bytes(b"other"))
        .is_err());
}
```

Extend the existing local fixture so tests expose `request` and `journal`; do
not create a parallel test harness.

- [ ] **Step 2: Run tests and verify missing methods**

```bash
cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture
```

Expected: compilation fails on the new provider journal methods.

- [ ] **Step 3: Add provider event variants and state**

Add to `JournalEventKind`:

```rust
ProviderRequestIntent { prepared_run_digest: Digest },
ProviderProcessStarted { invocation_digest: Digest },
ProviderOutputRetained { raw_capture_digest: Digest },
ProviderCaptureIndexPublished { index_digest: Digest },
ProviderCaptureVerified { index_digest: Digest },
AdapterTurnNormalized { turn_digest: Digest },
```

Add:

```rust
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ProviderJournalState {
    pub prepared_run_digest: Option<Digest>,
    pub provider_process_started: bool,
    pub raw_capture_digest: Option<Digest>,
    pub capture_index_digest: Option<Digest>,
    pub capture_verified: bool,
    pub adapter_turn_digest: Option<Digest>,
}

impl ProviderJournalState {
    #[must_use]
    pub const fn outcome_unknown(&self) -> bool {
        self.prepared_run_digest.is_some() && self.raw_capture_digest.is_none()
    }
}
```

Implement:

```rust
pub fn provider_state(&self, request: &RunRequest) -> Result<ProviderJournalState, RecoveryError>;
pub fn provider_may_start(&self, request: &RunRequest) -> Result<(), RecoveryError>;
pub fn record_provider_request_intent(&self, request: &RunRequest, prepared: &Digest) -> Result<(), RecoveryError>;
pub fn record_provider_process_started(&self, request: &RunRequest, invocation: &Digest) -> Result<(), RecoveryError>;
pub fn record_provider_output_retained(&self, request: &RunRequest, raw: &Digest) -> Result<(), RecoveryError>;
pub fn record_provider_capture_published(&self, request: &RunRequest, index: &Digest) -> Result<(), RecoveryError>;
pub fn record_provider_capture_verified(&self, request: &RunRequest, index: &Digest) -> Result<(), RecoveryError>;
pub fn record_adapter_turn_normalized(&self, request: &RunRequest, turn: &Digest) -> Result<(), RecoveryError>;
```

Allow legacy journals with no provider events. Once a provider event appears,
enforce exactly the listed order, equal published and verified index digests,
no duplicate event, no provider event after an effect, and no effect before
`AdapterTurnNormalized`.

- [ ] **Step 4: Update exhaustive journal matches**

Update `effect_state`, verifier/terminal validation, recovery summaries, and
every exhaustive `JournalEventKind` match. Provider events are ignored by
effect aggregation only after `provider_state` validates their order.

- [ ] **Step 5: Run recovery gates**

```bash
cargo fmt --check
cargo test -p ao-next-core --test evidence_recovery -- --nocapture
cargo clippy -p ao-next-core --all-targets -- -D warnings
```

Expected: all recovery tests pass, including legacy effect-only journals.

- [ ] **Step 6: Commit**

```bash
git add crates/ao-next-core/src/recovery.rs crates/ao-next-core/tests/evidence_recovery.rs
git commit -m "feat: journal provider capture lifecycle"
```

### Task 3: Prepared-Run Receipt And `prepare-live`

**Files:**
- Modify: `crates/ao-next-core/src/contracts.rs`
- Modify: `crates/ao-next-core/tests/contracts.rs`
- Create: `docs/contracts/prepared-run-v1.schema.json`
- Modify: `crates/ao-next-cli/src/commands/mod.rs`
- Create: `crates/ao-next-cli/src/commands/live_prepare.rs`
- Modify: `crates/ao-next-cli/src/commands/live.rs`
- Modify: `crates/ao-next-cli/tests/cli.rs`

**Interfaces:**
- Consumes: strict live-input validation and existing `prepare_git_workspace`
- Produces: `PreparedRunReceipt`, `PrepareLiveArgs`, `ao-next prepare-live`

- [ ] **Step 1: Add failing prepared-run contract tests**

In `crates/ao-next-core/tests/contracts.rs`, import `PreparedRunReceipt` and add:

```rust
#[test]
fn prepared_run_receipt_round_trips_with_exact_git_identity() {
    let receipt = PreparedRunReceipt {
        schema_version: "ao.next.prepared-run.v1".into(),
        run_id: "run-prepared-01".into(),
        input_digest: digest(ZERO_DIGEST),
        request_digest: digest(ONE_DIGEST),
        repository_root: PathBuf::from("workspace"),
        common_directory: PathBuf::from("workspace/.git"),
        branch: "ao-next-sealed-seed".into(),
        base_commit: "0123456789abcdef0123456789abcdef01234567".into(),
        control_digest: digest(ZERO_DIGEST),
        index_digest: digest(ONE_DIGEST),
        workspace_digest: digest(ZERO_DIGEST),
        journal_identity_digest: digest(ONE_DIGEST),
        prepared_at: timestamp("2026-08-23T00:00:00Z"),
        expires_at: timestamp("2026-08-24T00:00:00Z"),
        provider_calls: 0,
        safe_to_execute: false,
    };
    let bytes = canonical_json_bytes(&receipt).expect("receipt bytes");
    let decoded: PreparedRunReceipt =
        decode_strict_json(&bytes, 64 * 1024).expect("receipt decode");
    assert_eq!(decoded, receipt);
}
```

Add the prepared-run file to the existing generated-schema equality test.

- [ ] **Step 2: Run tests and confirm the type is missing**

```bash
cargo test -p ao-next-core --test contracts prepared_run -- --nocapture
```

Expected: compilation fails because `PreparedRunReceipt` is undefined.

- [ ] **Step 3: Implement the contract and schema**

Add to `contracts.rs`:

```rust
#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct PreparedRunReceipt {
    pub schema_version: String,
    pub run_id: String,
    pub input_digest: Digest,
    pub request_digest: Digest,
    pub repository_root: PathBuf,
    pub common_directory: PathBuf,
    pub branch: String,
    pub base_commit: String,
    pub control_digest: Digest,
    pub index_digest: Digest,
    pub workspace_digest: Digest,
    pub journal_identity_digest: Digest,
    pub prepared_at: DateTime<Utc>,
    pub expires_at: DateTime<Utc>,
    pub provider_calls: u32,
    pub safe_to_execute: bool,
}
```

The checked-in schema requires every field, rejects unknown fields, fixes
`provider_calls` to zero and `safe_to_execute` to false, and constrains
`base_commit` to 40 lowercase hexadecimal characters.

Add this entry to `generated_contract_schemas()`:

```rust
(
    "prepared-run-v1.schema.json",
    serde_json::to_value(schema_for!(PreparedRunReceipt))
        .expect("schema serialization"),
),
```

- [ ] **Step 4: Add failing provider-free preparation CLI tests**

Add tests that build argv directly from the validated fixture:

```rust
let output = run(&[
    "prepare-live",
    "--input",
    fixture.input_path.to_str().expect("input path"),
    "--trusted-corpus-digest",
    fixture.input.corpus.corpus_digest.as_str(),
    "--trusted-verifier-profile-digest",
    fixture.input.command_verifier.profile_digest.as_str(),
    "--out",
    receipt_path.to_str().expect("receipt path"),
]);
```

Assert:

```rust
assert_eq!(output.status.code(), Some(0));
assert!(workspace.join(".git").is_dir());
assert_eq!(receipt.base_commit, git_head(&workspace));
assert_eq!(receipt.provider_calls, 0);
assert!(!receipt.safe_to_execute);
assert!(!provider_marker.exists());
```

Add negative cases for existing output, preexisting Git metadata, input drift,
source drift, symlinked output, mismatched corpus/verifier anchors, and ambient
`AO_NEXT_LIVE_PROVIDER_CALLS`.

- [ ] **Step 5: Add CLI arguments and command**

Add to `commands/mod.rs`:

```rust
#[derive(Debug, Args)]
pub struct PrepareLiveArgs {
    #[arg(long)]
    pub input: PathBuf,
    #[arg(long)]
    pub trusted_corpus_digest: String,
    #[arg(long)]
    pub trusted_verifier_profile_digest: String,
    #[arg(long)]
    pub out: PathBuf,
}
```

Add `Command::PrepareLive(PrepareLiveArgs)` and dispatch to
`live_prepare::execute`.

`live_prepare::execute` must:

1. reject any provider authorization environment variable;
2. call the existing trusted-binding and live-input validation helpers;
3. create and verify the deterministic Git workspace exactly once;
4. create the journal and bind the request without provider events;
5. build `PreparedRunReceipt` from actual Git and journal identities;
6. write canonical receipt bytes to `--out` with create-new semantics; and
7. return the same receipt on stdout with `provider_calls=0`.

Make `LiveRunInput`, `LiveVariant`, and `GitWorkspaceIdentity` visible to sibling
command modules as `pub(super)`. Expose only these helpers from `live.rs`:

```rust
pub(super) fn load_trusted_live_input(
    path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &str,
    trusted_verifier_profile_digest: &str,
    now: DateTime<Utc>,
) -> Result<LiveRunInput, CommandFailure>;

pub(super) fn prepare_git_workspace(
    root: &Path,
    allowed_roots: &[PathBuf],
    seed_digest: &Digest,
) -> Result<GitWorkspaceIdentity, CommandFailure>;

pub(super) fn execution_journal_root(input: &LiveRunInput) -> PathBuf;

pub(super) fn execution_journal_maximum_bytes(request: &RunRequest) -> u64;
```

`load_trusted_live_input` strictly decodes the file, builds
`TrustedLiveBindings` from the supplied strings, runs
`validate_trusted_bindings` and `validate_input`, discards the temporary
borrowed validation view, and returns the owned input.

Do not duplicate validation or Git preparation in `live_prepare.rs`.

- [ ] **Step 6: Run contract and CLI tests**

```bash
cargo fmt --check
cargo test -p ao-next-core --test contracts -- --nocapture
cargo test -p ao-next-cli --test cli prepare_live -- --nocapture
cargo clippy --workspace --all-targets -- -D warnings
```

Expected: all focused tests pass.

- [ ] **Step 7: Commit**

```bash
git add crates/ao-next-core/src/contracts.rs crates/ao-next-core/tests/contracts.rs docs/contracts/prepared-run-v1.schema.json crates/ao-next-cli/src/commands/mod.rs crates/ao-next-cli/src/commands/live.rs crates/ao-next-cli/src/commands/live_prepare.rs crates/ao-next-cli/tests/cli.rs
git commit -m "feat: prepare exact live Git identity before authority"
```

### Task 4: Fresh N7 Provider-Capture Integration

**Files:**
- Modify: `crates/ao-next-cli/src/commands/mod.rs`
- Modify: `crates/ao-next-cli/src/commands/live.rs`
- Modify: `crates/ao-next-core/src/engine.rs`
- Modify: `crates/ao-next-core/tests/direct_engine.rs`
- Modify: `crates/ao-next-cli/tests/cli.rs`

**Interfaces:**
- Consumes: `PreparedRunReceipt`, `CaptureIndexStore`, provider journal methods
- Produces: N7 `run-live --prepared-run`, ordered provider-to-effect journal

- [ ] **Step 1: Write failing prepared-receipt CLI tests**

Add a test that invokes N7 without `--prepared-run`:

```rust
let missing = run_with_live_environment(
    &["run-live", "--input", input.to_str().expect("input")],
    bin_path,
    Some("operator-authorized"),
);
assert_json_error(&missing, 3, "invalid_input");
assert!(!provider_marker.exists());
```

Add drift cases for input digest, request digest, Git HEAD, branch, index digest,
control digest, workspace digest, journal identity, expiry, and run ID. Every
case asserts the provider marker is absent.

- [ ] **Step 2: Write failing event-order integration test**

Add a provider-free N7 test that completes one fake-provider run and reads the
journal event names. Assert they are exactly:

```rust
[
    "provider_request_intent",
    "provider_process_started",
    "provider_output_retained",
    "provider_capture_index_published",
    "provider_capture_verified",
    "adapter_turn_normalized",
    "effect_intent",
    "effect_completed",
    "verification_started",
    "verifier_recorded",
    "terminal_published",
]
```

- [ ] **Step 3: Run tests and verify failure**

```bash
cargo test -p ao-next-cli --test cli prepared_run -- --nocapture
cargo test -p ao-next-cli commands::live::tests::provider_journal -- --nocapture
```

Expected: tests fail because N7 does not accept a prepared receipt or journal
provider stages.

- [ ] **Step 4: Require and validate `--prepared-run` for N7**

Extend `LiveRunArgs`:

```rust
#[arg(long)]
pub prepared_run: Option<PathBuf>,
```

N7 requires `Some`; N0 and N4 reject it during Stage 0. Load the strict receipt,
require schema `ao.next.prepared-run.v1`, require an unexpired receipt, rehash
input/request, and verify current Git using the receipt instead of preparing
Git again. Calculate `prepared_run_digest = canonical_digest(&receipt)` after
validation and pass that exact digest into `CaptureFirstRunner`.

- [ ] **Step 5: Split capture retention from index publication**

Refactor `persist_raw_captures` into:

```rust
fn retain_raw_capture_files(
    root: &Path,
    context: &CaptureContext,
    captures: &[RuntimeCapture],
    outputs: &[InvocationOutput],
) -> Result<(RawCaptureIndex, Digest), CommandFailure>;

fn publish_raw_capture_index(
    root: &Path,
    index: &RawCaptureIndex,
) -> Result<CapturePublication, CommandFailure>;
```

`publish_raw_capture_index` serializes canonical index bytes and delegates to
`CaptureIndexStore`. Delete `publish_private_index` and every Windows directory
`File::open(root).sync_all()` call.

- [ ] **Step 6: Journal provider lifecycle around the process**

Add `journal`, `request`, and `prepared_run_digest` to `CaptureFirstRunner`.
Its `run` method follows:

```rust
journal.provider_may_start(request)?;
journal.record_provider_request_intent(request, prepared_run_digest)?;
journal.record_provider_process_started(request, &invocation_digest(invocation)?)?;
let output = runner.run(invocation, cancellation)?;
let (index, raw_digest) = retain_raw_capture_files(
    &self.raw_capture_root,
    &self.capture_context,
    &[],
    std::slice::from_ref(&output),
)?;
journal.record_provider_output_retained(request, &raw_digest)?;
let publication = publish_raw_capture_index(&self.raw_capture_root, &index)?;
let index_digest = publication.digest().clone();
journal.record_provider_capture_published(request, &index_digest)?;
verify_raw_capture_index(
    &self.raw_capture_root,
    &self.capture_context,
    &index_digest,
    self.capture_context.maximum_output_bytes,
)?;
journal.record_provider_capture_verified(request, &index_digest)?;
verify_and_gate_capture(
    &self.raw_capture_root,
    &self.capture_context,
    &index_digest,
    &self.runtime,
    &output,
    self.max_tokens,
)?;
Ok(output)
```

Implement `invocation_digest` as canonical digest material containing the
provider program, ordered args, working-directory digest, stdin digest, and
sorted environment key names. Do not include environment values.

- [ ] **Step 7: Record normalized adapter turn before effects**

In `DirectEngine::run_inner`, immediately after a durable adapter turn is
returned and before iterating over effects, query provider state. Record the
normalized turn only when the journal contains provider intent:

```rust
if let Some(journal) = journal {
    let provider_result = journal.provider_state(request).and_then(|state| {
        if state.prepared_run_digest.is_some() {
            journal.record_adapter_turn_normalized(request, &canonical_digest(&turn)?)
        } else {
            Ok(())
        }
    });
    if let Err(error) = provider_result {
        return transition_and_finish(
            lifecycle,
            identity,
            metrics,
            events,
            verifier_report_digest,
            RunState::Failed,
            "journal_failure",
            &error.to_string(),
        );
    }
}
```

Map failure to the existing `journal_failure` terminal path. Keep non-durable
`run` and durable legacy journals without provider events unchanged.

- [ ] **Step 8: Run fresh N7 and workspace gates**

```bash
cargo fmt --check
cargo test -p ao-next-cli commands::live::tests::provider_journal -- --nocapture
cargo test -p ao-next-core --test direct_engine -- --nocapture
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
```

Expected: exact event order, one provider process, and all legacy tests pass.

- [ ] **Step 9: Commit**

```bash
git add crates/ao-next-cli/src/commands/mod.rs crates/ao-next-cli/src/commands/live.rs crates/ao-next-core/src/engine.rs crates/ao-next-core/tests/direct_engine.rs crates/ao-next-cli/tests/cli.rs
git commit -m "feat: journal fresh provider capture before effects"
```

### Task 5: No-Provider `recover-live`

**Files:**
- Modify: `crates/ao-next-core/src/contracts.rs`
- Modify: `crates/ao-next-core/tests/contracts.rs`
- Modify: `crates/ao-next-cli/src/commands/mod.rs`
- Create: `crates/ao-next-cli/src/commands/live_recover.rs`
- Modify: `crates/ao-next-cli/src/commands/live.rs`
- Modify: `crates/ao-next-cli/tests/cli.rs`

**Interfaces:**
- Consumes: prepared receipt, provider journal, verified capture index, existing N7 adapter/engine/verifier path
- Produces: `RecoverLiveArgs`, `ao-next recover-live`, `RetainedCaptureRunner`

- [ ] **Step 1: Write failing end-to-end recovery test**

Add a CLI fixture that:

1. completes an uninterrupted provider-free N7 reference run and retains its
   terminal digest;
2. prepares a second byte-identical N7 workspace and receipt;
3. records provider intent, process started, and output retained;
4. retains the same fake-provider stdout/stderr used by the reference run;
5. creates identical final and incomplete capture-index names;
6. creates a `provider-started` marker containing `one`; and
7. invokes `recover-live` without a provider gate.

Assert:

```rust
assert_eq!(recovered.status.code(), Some(0));
assert_eq!(std::fs::read(&provider_marker).expect("marker"), b"one");
assert!(!capture_root.join("capture-index.json.incomplete").exists());
assert_eq!(terminal["terminal_state"], "passed");
assert_eq!(journal_provider_start_count(&journal_root), 1);
assert_eq!(terminal["record_digest"], uninterrupted["record_digest"]);
```

Add negative tests for missing receipt, unknown provider outcome,
contradictory pair, tampered capture, changed Git identity, expired authority
with pending effects, existing provider gate, and provider-program override.

Add core contract tests proving identity/root/schema validation accepts an
expired envelope for read-only evidence inspection but mutation-time freshness
still rejects it.

- [ ] **Step 2: Run tests and verify command is missing**

```bash
cargo test -p ao-next-cli --test cli recover_live -- --nocapture
```

Expected: usage failure because `recover-live` is undefined.

- [ ] **Step 3: Add recovery arguments**

```rust
#[derive(Debug, Args)]
pub struct RecoverLiveArgs {
    #[arg(long)]
    pub input: PathBuf,
    #[arg(long)]
    pub prepared_run: PathBuf,
    #[arg(long)]
    pub trusted_corpus_digest: String,
    #[arg(long)]
    pub trusted_verifier_profile_digest: String,
}
```

Dispatch `Command::RecoverLive` to `live_recover::execute`.

- [ ] **Step 4: Split intake identity validation from freshness**

Refactor `validate_intake` in `contracts.rs` into shared public checks:

```rust
pub fn validate_intake_identity(
    request: &RunRequest,
    expectation: &IntakeExpectation,
) -> Result<(), IntakeError>;

pub fn validate_authority_current(
    authority: &AuthorityEnvelope,
    now: DateTime<Utc>,
) -> Result<(), IntakeError>;
```

`validate_intake_identity` checks request/authority schemas, run, source,
workspace, `issued_at < expires_at`, and workspace-root binding, but not whether
`expires_at` is after `expectation.now`. `validate_authority_current` checks
`issued_at <= now < expires_at`. The existing `validate_intake` calls both so
fresh execution behavior does not change.

Recovery calls `validate_intake_identity` before reading retained evidence. It
calls `validate_authority_current` immediately before any not-yet-completed
effect is admitted.

In `live.rs`, split capture-root validation by mode:

```rust
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum CaptureRootMode {
    RequireEmpty,
    RequireRetained,
}

pub(super) fn load_trusted_live_input_for_recovery(
    path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &str,
    trusted_verifier_profile_digest: &str,
    now: DateTime<Utc>,
) -> Result<LiveRunInput, CommandFailure>;
```

Refactor the current validator through one private
`validate_input_with_capture_mode`. Fresh preflight, preparation, and execution
use `RequireEmpty`. Recovery uses `RequireRetained`, which requires the same
safe private root and allows only bounded regular capture stdout/stderr, final
or incomplete index names, and an optional capture-terminal record. Unknown
entries, directories, symlinks, reparse points, or duplicate capture paths fail
before journal state changes.

- [ ] **Step 5: Implement retained capture loading**

Expose from `live.rs`:

```rust
pub(super) fn load_verified_capture(
    root: &Path,
    context: &CaptureContext,
    provider_state: &ProviderJournalState,
    maximum_output_bytes: u64,
) -> Result<(InvocationOutput, Digest, CapturePublication), CommandFailure>;
```

It repairs an identical pair through `CaptureIndexStore`, strictly decodes the
index, requires exactly one N7 entry, verifies all sizes/digests/paths, and
returns retained status/stdout/stderr, the canonical index digest, and whether
publication was repaired. It never resolves a provider program.

When `provider_state.capture_index_digest` is present, require that exact
digest. Otherwise decode the bounded incomplete or final index, calculate its
canonical digest, verify every entry, and require the entry's raw capture digest
to equal `provider_state.raw_capture_digest` before asking `CaptureIndexStore`
to publish or clean the pair.

- [ ] **Step 6: Implement retained-output runner and recovery**

Create in `live_recover.rs`:

```rust
struct RetainedCaptureRunner {
    output: Option<InvocationOutput>,
}

impl ProcessRunner for RetainedCaptureRunner {
    fn run(
        &mut self,
        _: &PreparedInvocation,
        _: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.output
            .take()
            .ok_or_else(|| InvocationError::Io("retained capture already consumed".into()))
    }
}
```

`execute` must:

1. reject `AO_NEXT_LIVE_PROVIDER_CALLS`, `AO_NEXT_PROVIDER_FREE_PROGRAM`, and
   `AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST` when present;
2. call `load_trusted_live_input_for_recovery`, then validate receipt, Git
   identity, request, journal, and provider state without granting freshness;
3. reject provider intent without retained capture as unknown;
4. repair/verify the index and record missing published/verified events;
5. load exact retained output and apply the trusted usage gate;
6. run the existing N7 adapter, effect broker, verifier, evidence, and terminal
   path with `RetainedCaptureRunner` but without `CaptureFirstRunner`;
7. record normalized turn and all later events through the same journal;
8. return the normal `ao.next.live-run-record.v1`; and
9. never append another provider-process-started event.

Normalize the retained turn before the freshness decision. For each effect,
query `journal.effect_state`; if any effect is fresh or unknown, require
`validate_authority_current` before admission. If all effects are durably
complete, allow verifier and terminal recovery after expiry without new
mutation authority. An unknown effect remains blocked even when later authority
is current.

- [ ] **Step 7: Run no-retry recovery gates**

```bash
cargo fmt --check
cargo test -p ao-next-core --test contracts intake -- --nocapture
cargo test -p ao-next-cli --test cli recover_live -- --nocapture
cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
```

Expected: the same terminal digest, unchanged provider marker, and one provider
start event.

- [ ] **Step 8: Commit**

```bash
git add crates/ao-next-core/src/contracts.rs crates/ao-next-core/tests/contracts.rs crates/ao-next-cli/src/commands/mod.rs crates/ao-next-cli/src/commands/live.rs crates/ao-next-cli/src/commands/live_recover.rs crates/ao-next-cli/tests/cli.rs
git commit -m "feat: recover live runs from retained provider capture"
```

### Task 6: Cross-Platform Qualification And Operator Contracts

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/live-evaluation-harness.md`
- Modify: `docs/runtime-adapters.md`
- Modify: `AGENTS.md`
- Create: `tests/cross-platform/README.md`

**Interfaces:**
- Consumes: `prepare-live`, `run-live --prepared-run`, `recover-live`
- Produces: hosted three-platform gate and physical-Windows procedure

- [ ] **Step 1: Add focused recovery tests to hosted CI**

Ensure the existing Linux, macOS, and Windows matrix runs:

```bash
cargo test -p ao-next-core --test capture_store -- --nocapture
cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture
cargo test -p ao-next-cli --test cli recover_live -- --nocapture
```

Do not add a provider secret or live-provider environment gate.

- [ ] **Step 2: Write the physical Windows procedure**

Create `tests/cross-platform/README.md` with:

```powershell
cargo test -p ao-next-core --test capture_store -- --nocapture
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo test -p ao-next-cli --test cli recover_live -- --nocapture
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo test --workspace
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo clippy --workspace --all-targets -- -D warnings
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo build --workspace --release
exit $LASTEXITCODE
```

Require a new empty NTFS root, path containing spaces, no provider gate, and
result JSON outside the checkout. Record OS build, filesystem, source head,
command exits, final/incomplete inventory, provider process count zero, target
hash, and cleanup result.

- [ ] **Step 3: Update operator documentation**

Document:

```text
preflight-live-input  -> read-only validation
prepare-live          -> deterministic Git seed, zero provider calls
run-live              -> exact receipt plus separately authorized provider
recover-live          -> retained capture only, provider gate forbidden
```

State that `recover-live` is not a retry command and provider intent without
retained output is terminally unknown.

- [ ] **Step 4: Update AGENTS.md**

Require a prepared receipt before N7 spawn, provider intent before process
creation, retained-capture recovery without provider resolution, and physical
NTFS qualification for publication changes.

- [ ] **Step 5: Run documentation and repository gates**

```bash
python3 ../ao-architecture/scripts/verify_agent_instruction_layout.py --workspace-root .. --repository ao-next
bash tests/bootstrap_contract.sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

Expected: all gates pass; live-provider tests remain intentionally ignored.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ci.yml README.md docs/architecture.md docs/live-evaluation-harness.md docs/runtime-adapters.md AGENTS.md tests/cross-platform/README.md
git commit -m "docs: qualify retained capture recovery across platforms"
```

### Task 7: Reviewed Merge And Stage-0 Closure

**Files:**
- Create outside Git: one run-owned qualification evidence root
- Modify after review findings only: files named by the reviewer

**Interfaces:**
- Consumes: Tasks 1-6 commits and verification output
- Produces: reviewed merged Stage-0 head, hosted CI, physical Windows result, independent evidence digest, exact next-stage recommendation

- [ ] **Step 1: Run the exact-head local gate**

```bash
bash tests/bootstrap_contract.sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

Record exact `git rev-parse HEAD`, command exits, test totals, and the two
intentionally ignored live-provider contract tests.

- [ ] **Step 2: Request independent review**

Review the full design-baseline-to-task-head range for Windows publication,
interrupted-pair recovery, provider ordering, no second process, receipt/Git
drift, retained-output usage gating, expired authority, reparse/symlink safety,
and CLI/schema compatibility. Fix every Critical or Important finding through
the original task owner and re-run its focused tests.

- [ ] **Step 3: Push and open one pull request**

Use title:

```text
feat: recover AO Next from retained provider captures
```

Record design/plan paths, exact source head, no-provider scope, local commands,
physical Windows evidence status, rollback, and denied authority.

- [ ] **Step 4: Wait for every hosted platform check**

Require successful Linux, macOS, and Windows jobs on the exact PR head. Green
Linux/macOS results do not override Windows failure.

- [ ] **Step 5: Run physical Windows NTFS qualification**

Run Task 6 on the exact reviewed PR head. Independently copy and hash the
public-safe result and private retained-capture fixture. Verify provider process
count remains zero.

- [ ] **Step 6: Merge only after review and Windows gates pass**

Merge through the reviewed PR path, synchronize `main`, then run:

```bash
bash tests/bootstrap_contract.sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

- [ ] **Step 7: Clean the task branch and worktree**

Remove only the clean run-owned task worktree after the merge contains every
commit. Delete the local branch normally; never force-delete preserved work.

- [ ] **Step 8: Record Stage-0 closure**

Record exactly one result:

- `ENGINE_RECOVERY_READY_FOR_MISSION_MIGRATION` when local, hosted, and
  physical-Windows gates pass; or
- `STOP_ENGINE_RECOVERY_REPAIR` when a capture, authority, no-retry, or
  cross-platform gate remains failed.

The closure grants no provider call, Mission migration, package publication,
plugin work, AO2 retirement, or production route change. Stage 1 requires a new
plan and handoff after this closure verifies.
