# Physical Windows NTFS Qualification

Run this provider-free procedure on a physical Windows host after capture publication or recovery changes. Hosted Windows remains required and does not replace this check. The procedure grants no provider call or publication authority.

1. Create a new empty disposable NTFS root whose path contains spaces, such as `D:\AO Next NTFS Qualification`, and place the checkout beneath it. Create a second new empty evidence root outside the disposable root, such as `E:\AO Next Qualification Evidence`.
2. Confirm `AO_NEXT_LIVE_PROVIDER_CALLS`, `AO_NEXT_PROVIDER_FREE_PROGRAM`, and `AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST` are absent.
3. Set the two paths below, then run the block from the checkout. The existing real recovery integration test starts one local fake provider during setup and zero providers during recovery. It writes `recovery-result.json` and a hash-manifested private retained fixture to the evidence root.

```powershell
$TargetRoot = 'D:\AO Next NTFS Qualification'
$EvidenceRoot = 'E:\AO Next Qualification Evidence'
$Checkout = (Get-Location).Path

$target = (Resolve-Path -LiteralPath $TargetRoot).Path.TrimEnd('\')
$evidence = (Resolve-Path -LiteralPath $EvidenceRoot).Path.TrimEnd('\')
if (-not $Checkout.StartsWith($target, [StringComparison]::OrdinalIgnoreCase)) { throw 'checkout is outside target root' }
if ($evidence.StartsWith($target, [StringComparison]::OrdinalIgnoreCase)) { throw 'evidence root is inside disposable target' }
if ((Get-ChildItem -Force -LiteralPath $EvidenceRoot | Measure-Object).Count -ne 0) { throw 'evidence root is not empty' }
if ((Get-Volume -DriveLetter $target.Substring(0, 1)).FileSystem -ne 'NTFS') { throw 'target root is not NTFS' }
foreach ($name in 'AO_NEXT_LIVE_PROVIDER_CALLS','AO_NEXT_PROVIDER_FREE_PROGRAM','AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST') {
    if (Test-Path "Env:$name") { throw "$name must be absent" }
}

$gates = [System.Collections.Generic.List[object]]::new()
function Invoke-Gate([string]$Name, [scriptblock]$Command) {
    & $Command
    $code = $LASTEXITCODE
    $script:gates.Add([ordered]@{ name = $Name; exit_code = $code })
    if ($code -ne 0) { throw "$Name failed with exit $code" }
}

Invoke-Gate 'capture-store' { cargo test -p ao-next-core --test capture_store -- --nocapture }
Invoke-Gate 'provider-journal' { cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture }
try {
    $env:AO_NEXT_RECOVERY_EVIDENCE_ROOT = $EvidenceRoot
    Invoke-Gate 'persistent-real-recovery' { cargo test -p ao-next-cli --test cli recover_live_reuses_retained_capture_without_a_second_provider -- --exact --nocapture }
} finally {
    Remove-Item Env:AO_NEXT_RECOVERY_EVIDENCE_ROOT -ErrorAction SilentlyContinue
}
Invoke-Gate 'workspace-tests' { cargo test --workspace }
Invoke-Gate 'workspace-clippy' { cargo clippy --workspace --all-targets -- -D warnings }
Invoke-Gate 'release-build' { cargo build --workspace --release }

$resultPath = Join-Path $EvidenceRoot 'recovery-result.json'
$result = Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json
if ($result.setup_provider_process_count -ne 1) { throw 'setup provider count is not one' }
if ($result.recovery_provider_process_count -ne 0) { throw 'recovery started a provider' }
$privateRoot = Join-Path $EvidenceRoot 'private-retained-capture'
$manifest = Get-Content -Raw -LiteralPath (Join-Path $privateRoot 'fixture-manifest.json') | ConvertFrom-Json
foreach ($file in $manifest.files) {
    $observed = 'sha256:' + (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $privateRoot $file.name)).Hash.ToLowerInvariant()
    if ($observed -ne $file.digest) { throw "private fixture digest mismatch: $($file.name)" }
}

$treeLines = Get-ChildItem -File -Recurse -LiteralPath $TargetRoot | Sort-Object FullName | ForEach-Object {
    $relative = $_.FullName.Substring($target.Length).TrimStart('\')
    "$relative $((Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant())"
}
$treeBytes = [Text.Encoding]::UTF8.GetBytes(($treeLines -join "`n"))
$treeHash = 'sha256:' + [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($treeBytes)).ToLowerInvariant()
$hostResult = [ordered]@{
    schema_version = 'ao.next.physical-windows-host-result.v1'
    windows_build = (Get-CimInstance Win32_OperatingSystem).BuildNumber
    filesystem = (Get-Volume -DriveLetter $target.Substring(0, 1)).FileSystem
    source_head = (git rev-parse HEAD).Trim()
    gates = $gates
    target_tree_digest = $treeHash
    cleanup_complete = $false
}
$hostPath = Join-Path $EvidenceRoot 'qualification-host.json'
$hostResult | ConvertTo-Json -Depth 6 | Set-Content -NoNewline -Encoding utf8 -LiteralPath $hostPath

Set-Location $EvidenceRoot
Remove-Item -Recurse -Force -LiteralPath $TargetRoot
$hostResult.cleanup_complete = -not (Test-Path -LiteralPath $TargetRoot)
$hostResult | ConvertTo-Json -Depth 6 | Set-Content -NoNewline -Encoding utf8 -LiteralPath $hostPath
if (-not $hostResult.cleanup_complete) { throw 'disposable target cleanup failed' }
```

4. Retain and hash `recovery-result.json`, `qualification-host.json`, and the `private-retained-capture` directory from a clean process. The public-safe recovery result contains no private paths or capture bytes. Keep the private fixture outside Git.

The two live-provider tests remain intentionally ignored. A green result proves provider-free NTFS recovery at the tested head only.
