# AO Next Stage-0 Final Blocker Closure Design

- Status: approved by operator continuation instruction
- Date: 2026-08-24
- Repository: AO Next
- Parent design: [Dual-Process Cross-Platform Successor Design](2026-08-23-ao-next-dual-process-cross-platform-successor-design.md)
- Prior implementation plan: [Engine Recovery Repair](../plans/2026-08-23-ao-next-engine-recovery-repair.md)
- Corrective implementation plan: [Stage-0 Final Blocker Closure](../plans/2026-08-24-ao-next-stage0-final-blocker-closure.md)

## Purpose

Close the three load-bearing findings that remained after the prior plan's final fix review. This amendment does not widen Stage 0. It changes no worker count, provider allowance, release authority, Mission ownership, or adoption state.

## 1. Durable N7 Execution-Authority Identity

`N7ExecutionAuthority` remains the separately issued post-preparation authority. Its canonical digest becomes part of the durable execution identity.

Fresh N7 execution must:

1. Strictly decode and validate the authority against the prepared receipt.
2. Calculate the canonical authority digest.
3. Record that digest with `provider_request_intent` before the provider boundary.
4. Carry the same digest into the terminal record and its semantic digest.

Recovery must load the journal before accepting an execution-authority document. It must calculate the supplied document's canonical digest and require exact equality with the digest recorded in `provider_request_intent`. A document with the same receipt, base, workspace, scope, and provider allowance but a different authority ID, issuer, issue time, or expiry is a different authority and must fail before capture repair, normalization, effect admission, verification, or terminal publication.

The digest is not a retry token. Once provider intent exists, no current or replacement authority may start another provider process. Completed-effect recovery may finish verification after the recorded authority expires, but it must use the same authority document digest.

`LiveRunRecord` must expose `n7_execution_authority_digest` for N7 and `null` for N0/N4. Terminal-file recovery must validate this field before reusing orphan bytes.

## 2. Handle-Anchored Windows Provider Visibility

Provider visibility must not collect pathnames and reopen them after directory anchors are dropped.

Traversal must open and retain the root and every ancestor directory handle while each descendant file is opened and read. On Windows, directory handles must use `FILE_FLAG_OPEN_REPARSE_POINT` and withhold delete sharing. Every root, directory entry, and file must reject `FILE_ATTRIBUTE_REPARSE_POINT`. File bytes, relative path, size, and digest must be captured during the anchored recursive call. Sorting may happen after capture.

The ordinary validated workspace-root `.git` directory remains omitted without traversal. Nested `.git`, case-insensitive Git-control variants, fixture `.git`, symlinks, junctions, and other reparse points remain errors.

A native Windows regression must pause traversal after a nested ancestor handle is opened, attempt to replace that ancestor with a junction, and prove replacement fails while the original file bytes are read. Cross-target compilation is not native evidence.

## 3. PowerShell 5.1 Physical Qualification Harness

Move the physical NTFS procedure from an inline Markdown block into `tests/cross-platform/qualify-recovery.ps1`.

The script must run under Windows PowerShell 5.1. It must:

- accept explicit disposable target and retained evidence roots;
- compare normalized paths by complete components, including a separator boundary;
- reject equal, nested, or sibling-prefix evidence/target contradictions;
- use `SHA256.Create().ComputeHash()` and lowercase byte formatting, not .NET 5 static hashing APIs;
- run the repository recovery gates with immediate exit handling;
- persist the real recovery fixture outside the disposable target;
- record setup provider count `1` and recovery provider count `0` separately;
- verify the private fixture manifest from a clean read;
- record OS build, NTFS, exact source head, gate exits, target-tree digest, and cleanup;
- delete only the explicit disposable target after changing to the retained evidence root.

The script must support `-SelfTest`. Hosted Windows runs that mode through `powershell.exe`, exercising component containment and a known SHA-256 vector without deleting files or running provider fixtures.

`tests/cross-platform/README.md` becomes a short operator contract that invokes the script. It must not duplicate the implementation block.

## Acceptance

Stage 0 may advance only when all of these hold at one exact reviewed head:

1. Authority-substitution regressions fail before any durable mutation.
2. Fresh and recovered N7 terminal records contain the same recorded authority digest.
3. Windows provider visibility holds ancestor handles through file reads and native junction-swap tests pass.
4. Windows PowerShell 5.1 harness self-tests pass on hosted Windows.
5. Hosted Linux, macOS, and Windows repository jobs pass on the reviewed head.
6. The physical NTFS harness passes on that same head and retained evidence hashes independently verify.
7. Local bootstrap, format, workspace tests, Clippy, release build, instruction layout, schema drift, and diff checks pass.
8. Exactly two live-provider tests remain intentionally ignored. No live provider call is part of this closure.

The only successful terminal result is `ENGINE_RECOVERY_READY_FOR_MISSION_MIGRATION`. Any unresolved authority substitution, reparse race, PowerShell incompatibility, hosted failure, or physical NTFS failure records `STOP_ENGINE_RECOVERY_REPAIR`.
