param(
    [string]$TargetRoot,
    [string]$EvidenceRoot,
    [switch]$SelfTest
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

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

if ($SelfTest) {
    if (-not (Test-PathWithin 'C:\AO Next\checkout' 'C:\AO Next')) {
        throw 'component containment rejected a child path'
    }
    if (Test-PathWithin 'C:\AO Next Evil\checkout' 'C:\AO Next') {
        throw 'component containment accepted a sibling-prefix path'
    }
    $observed = Get-Sha256Hex ([Text.Encoding]::UTF8.GetBytes('abc'))
    if ($observed -ne 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad') {
        throw 'SHA-256 self-test failed'
    }
    Write-Output 'Windows PowerShell 5.1 recovery harness self-test passed'
    return
}

if ([string]::IsNullOrWhiteSpace($TargetRoot) -or [string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    throw 'TargetRoot and EvidenceRoot are required'
}
if (-not (Test-Path -LiteralPath $TargetRoot -PathType Container)) {
    throw 'target root must be an existing directory'
}
if (-not (Test-Path -LiteralPath $EvidenceRoot -PathType Container)) {
    throw 'evidence root must be an existing directory'
}

$targetItem = Get-Item -Force -LiteralPath $TargetRoot
$evidenceItem = Get-Item -Force -LiteralPath $EvidenceRoot
$target = Get-NormalizedPath $targetItem.FullName
$evidence = Get-NormalizedPath $evidenceItem.FullName
$checkoutPath = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
$checkout = Get-NormalizedPath $checkoutPath

if (($targetItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw 'target root must not be a reparse point'
}
if (($evidenceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw 'evidence root must not be a reparse point'
}
if ($target -notmatch '\s') { throw 'target root path must contain a space' }
if (-not (Test-PathWithin $checkout $target)) { throw 'checkout is outside target root' }
if ($evidence.Equals($target, [StringComparison]::OrdinalIgnoreCase) -or
    (Test-PathWithin $evidence $target) -or
    (Test-PathWithin $target $evidence)) {
    throw 'target and evidence roots must be separate'
}
if (@(Get-ChildItem -Force -LiteralPath $EvidenceRoot).Count -ne 0) {
    throw 'evidence root is not empty'
}

$checkoutRelative = $checkout.Substring($target.Length).TrimStart('\')
$checkoutTop = $checkoutRelative.Split([char]'\')[0]
$targetEntries = @(Get-ChildItem -Force -LiteralPath $TargetRoot)
if ($targetEntries.Count -ne 1) {
    throw 'target root must contain only the checkout'
}
$observedTop = Get-NormalizedPath $targetEntries[0].FullName
$expectedTop = Get-NormalizedPath (Join-Path $target $checkoutTop)
if (-not $observedTop.Equals($expectedTop, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'target root must contain only the checkout'
}

if ($target -notmatch '^[A-Za-z]:\\') { throw 'target root must use a local drive' }
$volume = Get-Volume -DriveLetter $target.Substring(0, 1)
if ($volume.FileSystem -ne 'NTFS') { throw 'target root is not NTFS' }
foreach ($name in 'AO_NEXT_LIVE_PROVIDER_CALLS','AO_NEXT_PROVIDER_FREE_PROGRAM','AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST') {
    if (Test-Path "Env:$name") { throw "$name must be absent" }
}

$gates = [System.Collections.Generic.List[object]]::new()
function Invoke-Gate([string]$Name, [string[]]$Arguments) {
    & cargo @Arguments
    $code = $LASTEXITCODE
    $command = 'cargo ' + ($Arguments -join ' ')
    $script:gates.Add([ordered]@{ name = $Name; command = $command; exit_code = $code })
    if ($code -ne 0) { throw "$Name failed with exit $code" }
}

Set-Location $checkout
Invoke-Gate 'capture-store' @('test','-p','ao-next-core','--test','capture_store','--','--nocapture')
Invoke-Gate 'provider-journal' @('test','-p','ao-next-core','--test','evidence_recovery','provider_','--','--nocapture')
try {
    $env:AO_NEXT_RECOVERY_EVIDENCE_ROOT = $evidence
    Invoke-Gate 'persistent-real-recovery' @(
        'test','-p','ao-next-cli','--test','cli',
        'recover_live_reuses_retained_capture_without_a_second_provider',
        '--','--exact','--nocapture'
    )
} finally {
    Remove-Item Env:AO_NEXT_RECOVERY_EVIDENCE_ROOT -ErrorAction SilentlyContinue
}
Invoke-Gate 'workspace-tests' @('test','--workspace')
Invoke-Gate 'workspace-clippy' @('clippy','--workspace','--all-targets','--','-D','warnings')
Invoke-Gate 'release-build' @('build','--workspace','--release')

$sourceHead = (& git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw "git rev-parse HEAD failed with exit $LASTEXITCODE" }

$resultPath = Join-Path $evidence 'recovery-result.json'
$result = Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json
if ($result.source_head -ne $sourceHead) { throw 'recovery result source head differs from checkout' }
if ($result.setup_provider_process_count -ne 1) { throw 'setup provider count is not one' }
if ($result.recovery_provider_process_count -ne 0) { throw 'recovery started a provider' }

$privateRoot = Get-NormalizedPath (Join-Path $evidence 'private-retained-capture')
$manifest = Get-Content -Raw -LiteralPath (Join-Path $privateRoot 'fixture-manifest.json') | ConvertFrom-Json
if ($manifest.fixture_digest -ne $result.private_retained_fixture_digest) {
    throw 'private fixture digest differs from recovery result'
}
if (@($manifest.files).Count -eq 0) { throw 'private fixture manifest is empty' }
foreach ($file in $manifest.files) {
    $filePath = Get-NormalizedPath ([IO.Path]::Combine($privateRoot, [string]$file.name))
    if (-not (Test-PathWithin $filePath $privateRoot) -or
        -not (Test-Path -LiteralPath $filePath -PathType Leaf)) {
        throw "invalid private fixture path: $($file.name)"
    }
    $fileItem = Get-Item -Force -LiteralPath $filePath
    if (($fileItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "private fixture is a reparse point: $($file.name)"
    }
    $observed = 'sha256:' + (Get-FileHash -Algorithm SHA256 -LiteralPath $filePath).Hash.ToLowerInvariant()
    if ($observed -ne $file.digest) { throw "private fixture digest mismatch: $($file.name)" }
}

$treeLines = Get-ChildItem -File -Recurse -LiteralPath $target | Sort-Object FullName | ForEach-Object {
    $relative = $_.FullName.Substring($target.Length).TrimStart('\')
    "$relative $((Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant())"
}
$treeBytes = [Text.Encoding]::UTF8.GetBytes(($treeLines -join "`n"))
$treeHash = 'sha256:' + (Get-Sha256Hex $treeBytes)

$hostResult = [ordered]@{
    schema_version = 'ao.next.physical-windows-host-result.v1'
    windows_build = (Get-CimInstance Win32_OperatingSystem).BuildNumber
    filesystem = $volume.FileSystem
    source_head = $sourceHead
    gates = $gates
    target_tree_digest = $treeHash
    cleanup_complete = $false
}
$hostPath = Join-Path $evidence 'qualification-host.json'
$hostResult | ConvertTo-Json -Depth 6 | Set-Content -NoNewline -Encoding utf8 -LiteralPath $hostPath

Set-Location $evidence
Remove-Item -Recurse -Force -LiteralPath $target
$hostResult.cleanup_complete = -not (Test-Path -LiteralPath $target)
$hostResult | ConvertTo-Json -Depth 6 | Set-Content -NoNewline -Encoding utf8 -LiteralPath $hostPath
if (-not $hostResult.cleanup_complete) { throw 'disposable target cleanup failed' }
