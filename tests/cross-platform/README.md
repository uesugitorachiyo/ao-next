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
