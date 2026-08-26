# Physical Windows NTFS Qualification

Run this provider-free procedure on a physical Windows host after capture publication or recovery changes. Hosted Windows remains required and does not replace this check.

Create a new empty disposable NTFS root whose path contains an ASCII space, place the checkout directly beneath it as its only entry, and create a second empty evidence root outside it. Pre-cache the locked Cargo dependencies; the harness runs Cargo offline. Confirm `AO_NEXT_LIVE_PROVIDER_CALLS`, `AO_NEXT_PROVIDER_FREE_PROGRAM`, and `AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST` are absent. From the checkout, run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests\cross-platform\qualify-recovery.ps1 `
  -TargetRoot 'D:\AO Next NTFS Qualification' `
  -EvidenceRoot 'E:\AO Next Qualification Evidence'
```

Retain and independently hash `recovery-result.json`, `qualification-host.json`, and `private-retained-capture` from a clean process. The public-safe recovery result contains no private paths or capture bytes. Keep the private fixture outside Git.

The harness runs one local fake provider during setup and zero providers during recovery. It grants no authority for provider gates or secrets, live provider calls, credentials, network access, release, deployment, publication, Mission migration, AO2 retirement, or production routing. The two live-provider tests remain intentionally ignored. A green result proves provider-free NTFS recovery at the tested head only.

## Mission Migration Equivalence

Run the same provider-free old/new replay on physical macOS arm64, Ubuntu
x86_64, and Windows x86_64, and in the hosted native matrix. Use separate
reference, candidate, state, scan, and evidence roots. Every run-owned root
must contain a space.

```sh
python3 tests/mission-migration/test_replay.py
python3 tests/mission-migration/replay.py \
  --corpus tests/fixtures/mission-migration/corpus-v1.json \
  --reference-source /absolute/path/to/ao-mission-05567fdd \
  --candidate-source mission \
  --evidence-root "/private/evidence root with spaces" \
  --output "/private/result root/equivalence-readback.json"
```

The rejected public-safety case must create and retain a real symlink on
macOS and Ubuntu or a real reparse-point symlink on Windows. Failure to create
that primitive fails the platform gate; it is never skipped or replaced.

Retain raw output privately. Verify that the sanitized readback binds the
exact source head and corpus digest, reports seven passing cases and zero
provider calls, and contains no private paths. Hash both the readback and the
platform manifest after copying them back from the host. A physical-host pass
does not replace the hosted matrix and grants no packaging, release,
deployment, publication, provider, adoption, or AO2-retirement authority.
