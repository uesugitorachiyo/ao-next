# AO Next Stage-0 Final Blocker Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the three reviewed Stage-0 blockers by durably binding the exact N7 execution-authority document, retaining Windows directory anchors through provider-visible file reads, and qualifying through a PowerShell 5.1-safe physical NTFS harness.

**Architecture:** Extend the existing journal and terminal evidence with the canonical N7 execution-authority digest. Replace pathname collection/reopen with bounded file capture during anchored traversal. Move the physical Windows procedure into one repository-owned PowerShell script with a non-destructive 5.1 self-test, then run reviewed exact-head hosted and physical gates.

**Tech Stack:** Rust 1.95, standard-library filesystem APIs, existing `rustix`, serde/serde_json, schemars, sha2, clap, Windows PowerShell 5.1, Cargo tests, GitHub Actions, physical NTFS.

**Spec:** `docs/superpowers/specs/2026-08-24-ao-next-stage0-final-blocker-closure-design.md`

## Global Constraints

- Stage 0 remains Rust Engine-only. Do not move Mission source, add Go, package releases, build a plugin, or add MCP.
- Use exactly one worker and at most one provider process per fresh run.
- Recovery never starts or resolves a provider process.
- A durable provider intent prevents every later provider retry, including under a replacement authority.
- The canonical N7 execution-authority digest must match journal and terminal evidence.
- Reject network, credentials, remote mutation, release, deployment, publication, Mission migration, AO2 retirement, and production routing.
- Reject symlinks, Windows reparse points, unsafe roots, path replacement, unknown fields, duplicate keys, oversized input, and digest drift.
- Keep raw provider captures, private paths, account data, authority files, and physical qualification evidence outside tracked files.
- Preserve exactly two intentionally ignored live-provider tests.

## Execution Model Profile

| Work | Implementer | Reviewer |
| --- | --- | --- |
| Task 1 authority durability | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| Task 2 Windows anchored visibility | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| Task 3 PowerShell qualification harness | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| Task 4 final review and evidence interpretation | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |

---

### Task 1: Bind Exact N7 Execution Authority Into Journal And Terminal Evidence

**Files:**
- Modify: `crates/ao-next-core/src/contracts.rs`
- Modify: `crates/ao-next-core/src/recovery.rs`
- Modify: `crates/ao-next-core/tests/contracts.rs`
- Modify: `crates/ao-next-core/tests/evidence_recovery.rs`
- Modify: `crates/ao-next-cli/src/commands/live.rs`
- Modify: `crates/ao-next-cli/src/commands/live_recover.rs`
- Modify: `crates/ao-next-cli/tests/cli.rs`

**Interfaces:**
- Produces: `N7ExecutionAuthorityExpectation::execution_authority_digest: Digest`
- Produces: `ProviderJournalState::execution_authority_digest: Option<Digest>`
- Produces: `CheckpointJournal::record_provider_request_intent(&RunRequest, &Digest, &Digest)`
- Produces: `LiveRunRecord::n7_execution_authority_digest: Option<Digest>`

- [ ] **Step 1: Write failing contract and journal tests**

Add core tests that construct two current `N7ExecutionAuthority` values with identical receipt/base/workspace/scope fields but different `authority_id`, `issued_by`, and intervals. Require canonical digests to differ. Add a provider lifecycle test:

```rust
let authority_digest = canonical_digest(&authority).expect("authority digest");
journal
    .record_provider_request_intent(&request, &prepared_digest, &authority_digest)
    .expect("provider intent");
assert_eq!(
    journal.provider_state(&request).expect("provider state").execution_authority_digest,
    Some(authority_digest),
);
```

Add strict event-log mutation coverage that changes only `execution_authority_digest` and must fail digest/canonical validation.

- [ ] **Step 2: Run the focused RED checks**

Run:

```bash
cargo test -p ao-next-core --test contracts n7_execution_authority -- --nocapture
cargo test -p ao-next-core --test evidence_recovery provider_intent_binds_execution_authority -- --nocapture
```

Expected: compilation fails because the expectation/state/event/method do not carry the execution-authority digest.

- [ ] **Step 3: Implement the durable journal binding**

Change the provider intent event and state:

```rust
ProviderRequestIntent {
    prepared_run_digest: Digest,
    execution_authority_digest: Digest,
}

pub struct ProviderJournalState {
    pub prepared_run_digest: Option<Digest>,
    pub execution_authority_digest: Option<Digest>,
    // existing fields unchanged
}
```

Change `record_provider_request_intent` to accept both digests and update every producer-free fixture explicitly. The fresh N7 path calculates `canonical_digest(&execution_authority)` once after strict validation and passes that digest into `CaptureFirstRunner`. Record it before `provider_process_started`.

Add `execution_authority_digest: Digest` to `N7ExecutionAuthorityExpectation`. `validate_n7_execution_authority_identity` calculates the canonical authority digest and requires exact equality with the expectation.

- [ ] **Step 4: Write failing CLI substitution and terminal tests**

Add `recover_live_rejects_substituted_execution_authority_document`. Complete a retained run with authority A, create authority B with the same receipt/base/workspace/scope/provider allowance but a different ID, issuer, `issued_at`, and `expires_at`, then invoke recovery with B. Assert:

```rust
assert_json_error(&output, 3, "invalid_input");
assert_eq!(journal_provider_start_count(&journal_root), 1);
assert_eq!(std::fs::read(&provider_marker).expect("marker"), b"one");
assert_eq!(journal_event_count(&journal_root), events_before);
```

Extend the uninterrupted/recovered terminal parity test to require the same non-null `n7_execution_authority_digest`. Add an orphan-terminal mutation that changes only that field and must fail before appending `terminal_published`.

- [ ] **Step 5: Implement recovery and terminal binding**

Recovery loads the request-bound journal and provider state before accepting the supplied authority as the same document. It passes the recorded digest into `N7ExecutionAuthorityExpectation`, then runs identity/current validation. Do not repair the capture pair before this comparison.

Add this field to the internal live record:

```rust
n7_execution_authority_digest: Option<Digest>,
```

N7 records `Some(digest)`; N0/N4 record `None`. Include it in `live_record_digest`, strict orphan-terminal validation, and recovery output. A replacement current authority may not change the digest after provider intent.

- [ ] **Step 6: Run Task 1 gates**

```bash
cargo fmt --check
cargo test -p ao-next-core --test contracts -- --nocapture
cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture
cargo test -p ao-next-cli --test cli execution_authority -- --nocapture
cargo test -p ao-next-cli --test cli recover_live -- --nocapture
cargo clippy --workspace --all-targets -- -D warnings
```

- [ ] **Step 7: Commit**

```bash
git add crates/ao-next-core/src/contracts.rs crates/ao-next-core/src/recovery.rs crates/ao-next-core/tests/contracts.rs crates/ao-next-core/tests/evidence_recovery.rs crates/ao-next-cli/src/commands/live.rs crates/ao-next-cli/src/commands/live_recover.rs crates/ao-next-cli/tests/cli.rs
git commit -m "fix: bind exact N7 execution authority"
```

### Task 2: Read Provider-Visible Files During Anchored Windows Traversal

**Files:**
- Modify: `crates/ao-next-core/src/adapter/process.rs`
- Modify: `crates/ao-next-core/tests/process_runtime_adapter.rs`

**Interfaces:**
- Replaces: `collect_visible_paths`
- Produces privately: `CapturedVisibleFile { relative, text, bytes, digest }`
- Preserves: `ProviderVisibility::from_live_roots` and `ProviderVisibleFile`

- [ ] **Step 1: Add a failing anchored-lifetime test**

Inside `adapter/process.rs`, add a Windows-only module-private regression named `provider_visibility_holds_nested_ancestor_until_file_read`. Create `root/nested/file.txt` and an outside directory. Create a junction candidate with `cmd /c mklink /J`. Use a test-only probe invoked after the nested directory anchor opens and before `file.txt` is read. The probe attempts to rename `nested` and replace it with the junction. Require the rename to fail while visibility still returns the original bytes.

Keep the existing root/nested reparse rejection tests. Add a current-host ordering regression that compares returned paths with lexicographically sorted relative paths.

- [ ] **Step 2: Run RED and cross-target compilation**

```bash
cargo test -p ao-next-core --test process_runtime_adapter live_visibility -- --nocapture
RUSTC=/Users/torachiyouesugi/.rustup/toolchains/stable-aarch64-apple-darwin/bin/rustc \
  /Users/torachiyouesugi/.rustup/toolchains/stable-aarch64-apple-darwin/bin/cargo \
  test -p ao-next-core --target x86_64-pc-windows-gnu --no-run
```

Expected: the test seam/helper is missing; existing implementation still collects pathnames for later reopen.

- [ ] **Step 3: Capture bytes inside the recursive anchored call**

Replace the two-phase pathname flow with one bounded recursive collector:

```rust
struct CapturedVisibleFile {
    relative: PathBuf,
    text: String,
    bytes: Vec<u8>,
    digest: Digest,
}

fn collect_visible_files(
    root: &Path,
    directory: &Path,
    omit_root_git: bool,
    maximum_bytes: usize,
    total: &mut usize,
    files: &mut Vec<CapturedVisibleFile>,
) -> Result<(), AdapterError>;
```

On Windows, create `_directory_anchor = open_visible_directory(directory)?` before `read_dir` and keep it in the recursive stack while every descendant file is opened and read. Reject reparse metadata before recursion or file open. Read and digest each file inside this function. Do not return a pathname for later reopen.

After traversal, sort `CapturedVisibleFile` by `relative`, then build `ProviderVisibleFile` and `VisibleFileIdentity` from captured bytes. Enforce per-file and aggregate bounds before storing bytes. The root `.git` omission remains an exact ordinary-directory check before recursion.

A private test helper may accept `&mut dyn FnMut(&Path)` before each file read; production passes a no-op. Do not expose a public hook.

- [ ] **Step 4: Run Task 2 gates**

```bash
cargo fmt --check
cargo test -p ao-next-core --test process_runtime_adapter -- --nocapture
cargo test -p ao-next-core adapter::process -- --nocapture
cargo clippy -p ao-next-core --all-targets -- -D warnings
RUSTC=/Users/torachiyouesugi/.rustup/toolchains/stable-aarch64-apple-darwin/bin/rustc \
  /Users/torachiyouesugi/.rustup/toolchains/stable-aarch64-apple-darwin/bin/cargo \
  clippy -p ao-next-core --all-targets --target x86_64-pc-windows-gnu -- -D warnings
```

- [ ] **Step 5: Commit**

```bash
git add crates/ao-next-core/src/adapter/process.rs crates/ao-next-core/tests/process_runtime_adapter.rs
git commit -m "fix: anchor Windows provider visibility reads"
```

### Task 3: Add A PowerShell 5.1 Qualification Harness

**Files:**
- Create: `tests/cross-platform/qualify-recovery.ps1`
- Modify: `tests/cross-platform/README.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: `qualify-recovery.ps1 -TargetRoot 'D:\AO Next NTFS Qualification' -EvidenceRoot 'E:\AO Next Qualification Evidence'`
- Produces: `qualify-recovery.ps1 -SelfTest`

- [ ] **Step 1: Create PowerShell 5.1 helper functions and self-test**

Create the script with a parameter block and these helpers:

```powershell
param(
    [string]$TargetRoot,
    [string]$EvidenceRoot,
    [switch]$SelfTest
)

function Get-NormalizedPath([string]$Path) {
    [IO.Path]::GetFullPath($Path).TrimEnd('\')
}

function Test-PathWithin([string]$Child, [string]$Parent) {
    $childPath = Get-NormalizedPath $Child
    $parentPath = Get-NormalizedPath $Parent
    $prefix = $parentPath + '\'
    $childPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
}

function Get-Sha256Hex([byte[]]$Bytes) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes) } finally { $sha.Dispose() }
    (($hash | ForEach-Object { $_.ToString('x2') }) -join '')
}
```

`-SelfTest` requires `C:\AO Next\checkout` inside `C:\AO Next`, rejects `C:\AO Next Evil\checkout`, and verifies SHA-256 of UTF-8 `abc` equals `ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad`. It performs no deletion and runs no Cargo command.

- [ ] **Step 2: Move the physical procedure into the script**

Port the existing gate block. Require existing empty target/evidence roots, checkout inside target, evidence outside and not equal to target, NTFS target, absent provider environment gates, exact command exit capture, persistent recovery result, private manifest verification, target-tree digest, host result, working-directory change to evidence root, explicit target deletion, and cleanup record.

Use `Get-Sha256Hex` for the target-tree digest. Do not use `Convert.ToHexString`, static `SHA256.HashData`, or raw prefix containment.

- [ ] **Step 3: Reduce the README to an operator contract**

Document:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests\cross-platform\qualify-recovery.ps1 `
  -TargetRoot 'D:\AO Next NTFS Qualification' `
  -EvidenceRoot 'E:\AO Next Qualification Evidence'
```

Retain the evidence inventory and authority denials. Remove the duplicated inline implementation.

- [ ] **Step 4: Add hosted Windows PowerShell 5.1 self-test**

Add one matrix step:

```yaml
- name: Validate Windows PowerShell 5.1 recovery harness
  if: matrix.os == 'windows-latest'
  shell: powershell
  run: powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests\cross-platform\qualify-recovery.ps1 -SelfTest
```

- [ ] **Step 5: Run available checks**

On macOS, run Markdown/YAML diff checks and repository gates. If `powershell.exe` is unavailable, record the self-test as native-pending; do not substitute `pwsh` as PowerShell 5.1 evidence.

```bash
git diff --check
bash tests/bootstrap_contract.sh
cargo test --workspace
```

- [ ] **Step 6: Commit**

```bash
git add tests/cross-platform/qualify-recovery.ps1 tests/cross-platform/README.md .github/workflows/ci.yml
git commit -m "test: qualify recovery with Windows PowerShell 5.1"
```

### Task 4: Independent Review, Hosted And Physical Gates, Merge, Closure

**Files:**
- Create outside Git: one retained physical-Windows evidence root
- Modify after review only: files named by the reviewer

**Interfaces:**
- Consumes: Tasks 1-3 commits
- Produces: reviewed merged head and one exact Stage-0 terminal result

- [ ] **Step 1: Run exact-head local gates**

```bash
layout_shadow=$(mktemp -d)
ln -s "$PWD" "$layout_shadow/ao-next"
python3 /Users/torachiyouesugi/Documents/public/ao-architecture/scripts/verify_agent_instruction_layout.py --workspace-root "$layout_shadow" --repository ao-next
unlink "$layout_shadow/ao-next"
rmdir "$layout_shadow"
bash tests/bootstrap_contract.sh
cargo fmt --check
cargo test --workspace
cargo clippy --workspace --all-targets -- -D warnings
cargo build --workspace --release
git diff --check
```

Record exact head, exits, 226-or-later passing test total, and exactly two ignored live-provider tests.

- [ ] **Step 2: Request independent whole-range review**

Review from `28738b81820b92400cef828459076c65716dce1d` through the task head. Require explicit verdicts for authority substitution, journal/terminal digest binding, recovery no-retry, anchored Windows reads, root `.git` omission, PowerShell 5.1 compatibility, component containment, persistent private evidence, and authority denials. Fix every Critical or Important finding and re-review the fix range.

- [ ] **Step 3: Push and open one pull request**

Use title:

```text
feat: recover AO Next from retained provider captures
```

Record design/plan paths, exact head, no-provider scope, local commands, rollback, denied authority, and physical evidence pending status.

- [ ] **Step 4: Require exact-head hosted checks**

Wait for Linux, macOS, and Windows jobs. Windows must run 15 recovery integration tests and the `powershell.exe -SelfTest` step. Any failure blocks physical qualification and merge.

- [ ] **Step 5: Run physical NTFS qualification on the reviewed PR head**

Use the repository script from a new target root with spaces and a separate empty retained evidence root. Verify `setup_provider_process_count == 1`, `recovery_provider_process_count == 0`, source head equals the PR head, every gate exit is zero, private manifest hashes match, target-tree digest exists, and cleanup is true. Independently hash retained results from a clean process.

- [ ] **Step 6: Merge through the reviewed PR path and reverify main**

Merge only when review, hosted jobs, and physical evidence are green on the same head. Synchronize `main`, then run the full local gates again on the merge commit.

- [ ] **Step 7: Record one terminal result**

- `ENGINE_RECOVERY_READY_FOR_MISSION_MIGRATION` only when every local, hosted, physical, review, and post-merge gate passes.
- `STOP_ENGINE_RECOVERY_REPAIR` for any unresolved authority, reparse, PowerShell, hosted, physical, review, or merge gate.

Neither result authorizes a provider call, release, deployment, publication, Mission migration, AO2 retirement, or production routing.
