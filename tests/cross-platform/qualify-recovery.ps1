param(
    [string]$TargetRoot,
    [string]$EvidenceRoot,
    [switch]$SelfTest
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-NormalizedPath([string]$Path) {
    $full = [IO.Path]::GetFullPath($Path)
    $root = [IO.Path]::GetPathRoot($full)
    if ($full.Equals($root, [StringComparison]::OrdinalIgnoreCase)) { return $root }
    $full.TrimEnd('\')
}

function Test-PathWithin([string]$Child, [string]$Parent) {
    $childPath = Get-NormalizedPath $Child
    $parentPath = Get-NormalizedPath $Parent
    if ($childPath.Equals($parentPath, [StringComparison]::OrdinalIgnoreCase)) { return $false }
    $prefix = if ($parentPath.EndsWith('\')) { $parentPath } else { $parentPath + '\' }
    $childPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
}

function Test-LocalDriveRootSyntax([string]$Path) {
    -not [string]::IsNullOrWhiteSpace($Path) -and $Path -match '^[A-Za-z]:\\'
}

function Test-LocalFixedDriveType([int]$DriveType) {
    $DriveType -eq 3
}

function Get-LocalFixedDisk([string]$Path, [string]$Label) {
    if (-not (Test-LocalDriveRootSyntax $Path)) { throw "$Label root must use an explicit local drive path" }
    $deviceId = $Path.Substring(0, 2).ToUpperInvariant()
    $disk = Get-CimInstance -ClassName Win32_LogicalDisk -Filter "DeviceID='$deviceId'"
    if ($null -eq $disk -or -not (Test-LocalFixedDriveType $disk.DriveType)) {
        throw "$Label root must be backed by a local fixed disk"
    }
    $disk
}

function Test-RootsSeparate([string]$First, [string]$Second) {
    $firstPath = Get-NormalizedPath $First
    $secondPath = Get-NormalizedPath $Second
    return (-not $firstPath.Equals($secondPath, [StringComparison]::OrdinalIgnoreCase) -and
        -not (Test-PathWithin $firstPath $secondPath) -and
        -not (Test-PathWithin $secondPath $firstPath))
}

function Get-Sha256Hex([byte[]]$Bytes) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes) } finally { $sha.Dispose() }
    (($hash | ForEach-Object { $_.ToString('x2') }) -join '')
}

function Get-FixtureCanonicalJson([object[]]$Entries) {
    $parts = @($Entries | ForEach-Object {
        $size = [Convert]::ToString([Int64]$_.size_bytes, [Globalization.CultureInfo]::InvariantCulture)
        '{"digest":"' + $_.digest + '","name":"' + $_.name + '","size_bytes":' + $size + '}'
    })
    '[' + ($parts -join ',') + ']'
}

function Get-FixtureDigest([object[]]$Entries) {
    $bytes = [Text.Encoding]::UTF8.GetBytes((Get-FixtureCanonicalJson $Entries))
    'sha256:' + (Get-Sha256Hex $bytes)
}

function Assert-NoReparseComponents([string]$Path) {
    $normalized = Get-NormalizedPath $Path
    $root = [IO.Path]::GetPathRoot($normalized)
    $candidates = [System.Collections.Generic.List[string]]::new()
    $candidates.Add($root)
    $current = $root
    foreach ($part in @($normalized.Substring($root.Length) -split '\\' | Where-Object { $_ -ne '' })) {
        $current = Join-Path $current $part
        $candidates.Add($current)
    }
    foreach ($candidate in $candidates) {
        if (-not (Test-Path -LiteralPath $candidate)) { throw "path component is missing: $candidate" }
        $item = Get-Item -Force -LiteralPath $candidate
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "path component is a reparse point: $candidate"
        }
    }
}

function Assert-TargetContainsOnlyCheckout([string]$Target, [string]$Checkout) {
    Assert-NoReparseComponents $Target
    Assert-NoReparseComponents $Checkout
    $entries = @(Get-ChildItem -Force -LiteralPath $Target)
    if ($entries.Count -ne 1 -or -not $entries[0].PSIsContainer) {
        throw 'target root must contain only the checkout directory'
    }
    Assert-NoReparseComponents $entries[0].FullName
    $entryPath = Get-NormalizedPath $entries[0].FullName
    if (-not $entryPath.Equals((Get-NormalizedPath $Checkout), [StringComparison]::OrdinalIgnoreCase)) {
        throw 'checkout must be the sole direct target entry'
    }
}

function Get-OrdinaryTreeFiles([string]$Root) {
    $files = [System.Collections.Generic.List[object]]::new()
    $directories = [System.Collections.Generic.Stack[string]]::new()
    $directories.Push((Get-NormalizedPath $Root))
    while ($directories.Count -ne 0) {
        $directory = $directories.Pop()
        Assert-NoReparseComponents $directory
        foreach ($entry in @(Get-ChildItem -Force -LiteralPath $directory)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "tree entry is a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) { $directories.Push($entry.FullName) } else { $files.Add($entry) }
        }
    }
    $files.ToArray()
}

function Assert-ExactProperties([object]$Value, [string[]]$Expected, [string]$Label) {
    $actual = @($Value.PSObject.Properties | ForEach-Object { $_.Name })
    if ($actual.Count -ne $Expected.Count) { throw "$Label has an unexpected property count" }
    foreach ($name in $Expected) {
        if (-not ($actual -ccontains $name)) { throw "$Label is missing property $name" }
    }
}

function Assert-JsonInteger([object]$Value, [string]$Label) {
    if ($Value -isnot [Int32] -and $Value -isnot [Int64]) { throw "$Label is not an integer" }
}

if ($SelfTest) {
    if ((Get-NormalizedPath 'E:\') -cne 'E:\') { throw 'drive-root normalization failed' }
    if (-not (Test-PathWithin 'C:\AO Next\checkout' 'C:\AO Next')) { throw 'child containment failed' }
    if (-not (Test-PathWithin 'C:\AO Next' 'C:\')) { throw 'drive-root containment failed' }
    if (Test-PathWithin 'C:\AO Next' 'C:\AO Next') { throw 'equal paths counted as containment' }
    if (Test-PathWithin 'C:\' 'C:\') { throw 'equal drive roots counted as containment' }
    if (Test-PathWithin 'C:\AO Next Evil\checkout' 'C:\AO Next') { throw 'sibling-prefix containment passed' }
    if (Test-PathWithin 'D:\AO Next\checkout' 'C:\AO Next') { throw 'cross-drive containment passed' }
    if (Test-RootsSeparate 'C:\AO Next' 'C:\AO Next') { throw 'equal roots counted as separate' }
    if (Test-RootsSeparate 'C:\AO Next' 'C:\AO Next\evidence') { throw 'nested evidence counted as separate' }
    if (Test-RootsSeparate 'C:\AO Next\target' 'C:\AO Next') { throw 'nested target counted as separate' }
    if (Test-RootsSeparate 'C:\' 'C:\AO Next') { throw 'drive-root nesting counted as separate' }
    if (-not (Test-RootsSeparate 'C:\AO Next' 'C:\AO Next Evil')) { throw 'sibling-prefix roots conflicted' }
    if (-not (Test-RootsSeparate 'C:\AO Next' 'D:\AO Next')) { throw 'cross-drive roots conflicted' }
    if (-not (Test-LocalDriveRootSyntax 'C:\AO Next')) { throw 'local drive syntax rejected' }
    if (Test-LocalDriveRootSyntax '\\server\share\AO Next') { throw 'UNC root syntax passed' }
    if (-not (Test-LocalFixedDriveType 3)) { throw 'local fixed drive type rejected' }
    if (Test-LocalFixedDriveType 4) { throw 'mapped network drive type passed' }
    $observed = Get-Sha256Hex ([Text.Encoding]::UTF8.GetBytes('abc'))
    if ($observed -ne 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad') {
        throw 'SHA-256 self-test failed'
    }
    $fixtureEntries = @(
        [pscustomobject][ordered]@{ digest = 'sha256:' + ('0' * 64); name = 'capture-000.stdout'; size_bytes = 0 },
        [pscustomobject][ordered]@{ digest = 'sha256:' + ('1' * 64); name = 'capture-000.stderr'; size_bytes = 1 },
        [pscustomobject][ordered]@{ digest = 'sha256:' + ('2' * 64); name = 'capture-index.json'; size_bytes = 2 }
    )
    if ((Get-FixtureDigest $fixtureEntries) -ne 'sha256:a33f55ef6008e788dd0a9c82768391d4674fca9d8499091f8b8e283b24fae3ac') {
        throw 'fixture digest self-test failed'
    }
    Write-Output 'Windows PowerShell 5.1 recovery harness self-test passed'
    return
}

if ([string]::IsNullOrWhiteSpace($TargetRoot) -or [string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    throw 'TargetRoot and EvidenceRoot are required'
}
$targetDisk = Get-LocalFixedDisk $TargetRoot 'target'
$evidenceDisk = Get-LocalFixedDisk $EvidenceRoot 'evidence'
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

Assert-NoReparseComponents $target
Assert-NoReparseComponents $evidence
if (-not $target.Contains(' ')) { throw 'target root path must contain an ASCII space' }
if (-not (Test-RootsSeparate $target $evidence)) { throw 'target and evidence roots must be separate' }
Assert-TargetContainsOnlyCheckout $target $checkout
if (@(Get-ChildItem -Force -LiteralPath $evidence).Count -ne 0) { throw 'evidence root is not empty' }

if ($targetDisk.FileSystem -ne 'NTFS') { throw 'target root is not NTFS' }
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
Invoke-Gate 'capture-store' @('test','--locked','--offline','-p','ao-next-core','--test','capture_store','--','--nocapture')
Invoke-Gate 'provider-journal' @('test','--locked','--offline','-p','ao-next-core','--test','evidence_recovery','provider_','--','--nocapture')
try {
    $env:AO_NEXT_RECOVERY_EVIDENCE_ROOT = $evidence
    Invoke-Gate 'persistent-real-recovery' @(
        'test','--locked','--offline','-p','ao-next-cli','--test','cli',
        'recover_live_reuses_retained_capture_without_a_second_provider',
        '--','--exact','--nocapture'
    )
} finally {
    Remove-Item Env:AO_NEXT_RECOVERY_EVIDENCE_ROOT -ErrorAction SilentlyContinue
}
Invoke-Gate 'workspace-tests' @('test','--locked','--offline','--workspace')
Invoke-Gate 'workspace-clippy' @('clippy','--locked','--offline','--workspace','--all-targets','--','-D','warnings')
Invoke-Gate 'release-build' @('build','--locked','--offline','--workspace','--release')

$headOutput = & git rev-parse HEAD
$headCode = $LASTEXITCODE
if ($headCode -ne 0) { throw "git rev-parse HEAD failed with exit $headCode" }
$sourceHead = ($headOutput -join "`n").Trim()

Assert-NoReparseComponents $evidence
$evidenceItems = @(Get-ChildItem -Force -LiteralPath $evidence)
$evidenceNames = @($evidenceItems | ForEach-Object { $_.Name })
if ($evidenceItems.Count -ne 2 -or
    -not ($evidenceNames -ccontains 'private-retained-capture') -or
    -not ($evidenceNames -ccontains 'recovery-result.json')) {
    throw 'recovery evidence inventory is not exact'
}

$resultPath = Join-Path $evidence 'recovery-result.json'
Assert-NoReparseComponents $resultPath
$resultJson = Get-Content -Raw -LiteralPath $resultPath
$result = $resultJson | ConvertFrom-Json
Assert-ExactProperties $result @(
    'incomplete_index_removed','private_retained_fixture_digest','recovery_provider_process_count',
    'schema_version','setup_provider_process_count','source_head','terminal_record_digest','terminal_state'
) 'recovery result'
if ($result.schema_version -cne 'ao.next.physical-recovery-result.v1') { throw 'recovery result schema is invalid' }
if ($result.terminal_state -cne 'passed') { throw 'recovery result terminal state is not passed' }
if ($result.incomplete_index_removed -isnot [bool] -or -not $result.incomplete_index_removed) {
    throw 'recovery result did not remove the incomplete index'
}
Assert-JsonInteger $result.setup_provider_process_count 'setup provider count'
Assert-JsonInteger $result.recovery_provider_process_count 'recovery provider count'
if ($result.source_head -cne $sourceHead) { throw 'recovery result source head differs from checkout' }
if ($result.setup_provider_process_count -ne 1) { throw 'setup provider count is not one' }
if ($result.recovery_provider_process_count -ne 0) { throw 'recovery started a provider' }
if ($result.terminal_record_digest -cnotmatch '^sha256:[0-9a-f]{64}$') { throw 'terminal record digest is invalid' }
if ($result.private_retained_fixture_digest -cnotmatch '^sha256:[0-9a-f]{64}$') {
    throw 'private fixture digest is invalid'
}
$expectedResultJson = (
    '{"incomplete_index_removed":true,"private_retained_fixture_digest":"' +
    $result.private_retained_fixture_digest + '","recovery_provider_process_count":0,"schema_version":"' +
    $result.schema_version + '","setup_provider_process_count":1,"source_head":"' + $sourceHead +
    '","terminal_record_digest":"' + $result.terminal_record_digest + '","terminal_state":"passed"}'
)
if ($resultJson -cne $expectedResultJson) { throw 'recovery result is not exact canonical JSON' }

$privateRoot = Get-NormalizedPath (Join-Path $evidence 'private-retained-capture')
Assert-NoReparseComponents $privateRoot
$privateItems = @(Get-ChildItem -Force -LiteralPath $privateRoot)
$privateNames = @($privateItems | ForEach-Object { $_.Name })
$expectedPrivateNames = @('capture-000.stdout','capture-000.stderr','capture-index.json','fixture-manifest.json')
if ($privateItems.Count -ne $expectedPrivateNames.Count) { throw 'private fixture inventory is not exact' }
foreach ($name in $expectedPrivateNames) {
    if (-not ($privateNames -ccontains $name)) { throw "private fixture is missing $name" }
}
foreach ($item in $privateItems) {
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "private fixture entry is not an ordinary file: $($item.Name)"
    }
}

$manifestPath = Join-Path $privateRoot 'fixture-manifest.json'
Assert-NoReparseComponents $manifestPath
$manifestJson = Get-Content -Raw -LiteralPath $manifestPath
$manifest = $manifestJson | ConvertFrom-Json
Assert-ExactProperties $manifest @('files','fixture_digest','schema_version') 'private fixture manifest'
if ($manifest.schema_version -cne 'ao.next.private-retained-recovery-fixture.v1') {
    throw 'private fixture manifest schema is invalid'
}
if ($manifest.fixture_digest -cnotmatch '^sha256:[0-9a-f]{64}$') { throw 'manifest fixture digest is invalid' }

$files = @($manifest.files)
$expectedFiles = @('capture-000.stdout','capture-000.stderr','capture-index.json')
if ($files.Count -ne $expectedFiles.Count) { throw 'manifest file inventory is not exact' }
for ($index = 0; $index -lt $expectedFiles.Count; $index++) {
    $file = $files[$index]
    Assert-ExactProperties $file @('digest','name','size_bytes') "manifest file $index"
    if ($file.name -cne $expectedFiles[$index]) { throw "manifest file order or name is invalid at $index" }
    if ($file.digest -cnotmatch '^sha256:[0-9a-f]{64}$') { throw "manifest digest is invalid: $($file.name)" }
    Assert-JsonInteger $file.size_bytes "manifest size: $($file.name)"
    if ($file.size_bytes -lt 0) { throw "manifest size is negative: $($file.name)" }
    $filePath = Join-Path $privateRoot $file.name
    Assert-NoReparseComponents $privateRoot
    Assert-NoReparseComponents $filePath
    $fileBytes = [IO.File]::ReadAllBytes($filePath)
    if ([Int64]$fileBytes.LongLength -ne [Int64]$file.size_bytes) { throw "private fixture size mismatch: $($file.name)" }
    $observed = 'sha256:' + (Get-Sha256Hex $fileBytes)
    if ($observed -cne $file.digest) { throw "private fixture digest mismatch: $($file.name)" }
}

$fixtureDigest = Get-FixtureDigest $files
if ($fixtureDigest -cne $manifest.fixture_digest -or
    $fixtureDigest -cne $result.private_retained_fixture_digest) {
    throw 'recomputed private fixture digest does not match retained evidence'
}
$expectedManifestJson = (
    '{"files":' + (Get-FixtureCanonicalJson $files) + ',"fixture_digest":"' +
    $fixtureDigest + '","schema_version":"' + $manifest.schema_version + '"}'
)
if ($manifestJson -cne $expectedManifestJson) { throw 'private fixture manifest is not exact canonical JSON' }

$treeFiles = @(Get-OrdinaryTreeFiles $target)
$treeLines = $treeFiles | Sort-Object FullName | ForEach-Object {
    Assert-NoReparseComponents $_.FullName
    $relative = $_.FullName.Substring($target.Length).TrimStart('\')
    "$relative $((Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant())"
}
$treeBytes = [Text.Encoding]::UTF8.GetBytes(($treeLines -join "`n"))
$treeHash = 'sha256:' + (Get-Sha256Hex $treeBytes)

$hostResult = [ordered]@{
    schema_version = 'ao.next.physical-windows-host-result.v1'
    windows_build = (Get-CimInstance Win32_OperatingSystem).BuildNumber
    filesystem = $targetDisk.FileSystem
    source_head = $sourceHead
    gates = $gates
    target_tree_digest = $treeHash
    cleanup_complete = $false
}
$hostPath = Join-Path $evidence 'qualification-host.json'
$hostResult | ConvertTo-Json -Depth 6 | Set-Content -NoNewline -Encoding utf8 -LiteralPath $hostPath
Assert-NoReparseComponents $hostPath

Set-Location $evidence
if (-not (Test-RootsSeparate $target $evidence)) { throw 'target and evidence roots changed before cleanup' }
Assert-NoReparseComponents $evidence
Assert-TargetContainsOnlyCheckout $target $checkout
Get-OrdinaryTreeFiles $checkout | Out-Null
Remove-Item -Recurse -Force -LiteralPath $target
$hostResult.cleanup_complete = -not (Test-Path -LiteralPath $target)
Assert-NoReparseComponents $hostPath
$hostResult | ConvertTo-Json -Depth 6 | Set-Content -NoNewline -Encoding utf8 -LiteralPath $hostPath
if (-not $hostResult.cleanup_complete) { throw 'disposable target cleanup failed' }
