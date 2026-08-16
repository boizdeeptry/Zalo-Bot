param(
  [string]$UpstreamRepo = $env:ZALOBOT_REPO
)

$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($UpstreamRepo)) {
  throw 'Real staged acceptance requires -UpstreamRepo or the ZALOBOT_REPO environment variable.'
}
try {
  $resolvedUpstreamRepo = [IO.Path]::GetFullPath($UpstreamRepo)
} catch {
  throw "Invalid upstream repo path '$UpstreamRepo': $($_.Exception.Message)"
}
if (-not (Test-Path -LiteralPath $resolvedUpstreamRepo -PathType Container)) {
  throw "Upstream repo directory does not exist: '$resolvedUpstreamRepo'"
}
if (-not (Test-Path -LiteralPath (Join-Path $resolvedUpstreamRepo '.git'))) {
  throw "Upstream repo is not a Git checkout: '$resolvedUpstreamRepo'"
}

Import-Module (Join-Path $PSScriptRoot '..\scripts\BuildApp.psm1') -Force

if (-not ('PortalBuildTestPathAliases' -as [type])) {
  Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;

public static class PortalBuildTestPathAliases
{
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern uint GetShortPathNameW(string longPath, StringBuilder shortPath, uint bufferLength);

    public static string TryGetShortPath(string path)
    {
        StringBuilder buffer = new StringBuilder(32768);
        uint length = GetShortPathNameW(path, buffer, (uint)buffer.Capacity);
        if (length == 0 || length >= buffer.Capacity) return null;
        return buffer.ToString();
    }
}
'@
}

function Assert-Equal {
  param(
    [Parameter(Mandatory)]$Actual,
    [Parameter(Mandatory)]$Expected,
    [Parameter(Mandatory)][string]$Message
  )

  if ($Actual -ne $Expected) {
    throw "$Message. Actual: '$Actual'; expected: '$Expected'"
  }
}

function Assert-ThrowsLike {
  param(
    [Parameter(Mandatory)][scriptblock]$Action,
    [Parameter(Mandatory)][string]$Pattern,
    [Parameter(Mandatory)][string]$Message
  )

  try {
    & $Action
  } catch {
    if ($_.Exception.Message -notmatch $Pattern) {
      throw "$Message. Unexpected error: $($_.Exception.Message)"
    }
    return
  }

  throw "$Message. Expected an error matching '$Pattern'."
}

function Write-TestFile {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][string]$Content
  )

  [IO.Directory]::CreateDirectory((Split-Path -Parent $Path)) | Out-Null
  $normalized = [regex]::Replace($Content, '\r\n?', "`n")
  [IO.File]::WriteAllText($Path, $normalized, [Text.UTF8Encoding]::new($false))
}

function Write-TestBytes {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][byte[]]$Bytes
  )

  [IO.Directory]::CreateDirectory((Split-Path -Parent $Path)) | Out-Null
  [IO.File]::WriteAllBytes($Path, $Bytes)
}

function Assert-MemoryV2DeploymentTooling {
  $scriptsRoot = Join-Path $PSScriptRoot '..\scripts'
  $modulePath = Join-Path $scriptsRoot 'MemoryV2Deployment.psm1'
  $canaryPath = Join-Path $scriptsRoot 'Invoke-MemoryV2Canary.ps1'
  $deployPath = Join-Path $scriptsRoot 'Invoke-MemoryV2Deploy.ps1'
  $sqlitePath = Join-Path $scriptsRoot 'memory_v2_sqlite.py'
  foreach ($path in @($modulePath, $canaryPath, $deployPath, $sqlitePath)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
      throw "Memory V2 deployment tooling is missing '$path'"
    }
  }
  $moduleText = Get-Content -LiteralPath $modulePath -Raw
  $null = [ScriptBlock]::Create($moduleText)
  $null = [ScriptBlock]::Create((Get-Content -LiteralPath $canaryPath -Raw))
  $null = [ScriptBlock]::Create((Get-Content -LiteralPath $deployPath -Raw))
  $deployStart = $moduleText.IndexOf('function Invoke-MemoryV2Deployment', [StringComparison]::Ordinal)
  $gateIndex = $moduleText.IndexOf('$null = Test-MemoryV2CanaryManifest', $deployStart, [StringComparison]::Ordinal)
  $preflightIndex = $moduleText.IndexOf('$liveInspection = & $inspectLive', $deployStart, [StringComparison]::Ordinal)
  $stageIndex = $moduleText.IndexOf('[IO.File]::Copy($candidate, $staged', $deployStart, [StringComparison]::Ordinal)
  $stopIndex = $moduleText.IndexOf('& $stopLive', $deployStart, [StringComparison]::Ordinal)
  if ($deployStart -lt 0 -or $gateIndex -lt 0 -or $preflightIndex -lt 0 -or
      $stageIndex -lt 0 -or $stopIndex -lt 0 -or $gateIndex -ge $preflightIndex -or
      $preflightIndex -ge $stageIndex -or $stageIndex -ge $stopIndex) {
    throw 'Memory V2 deploy manifest/live-schema gates are not mechanically ordered before staging and stop'
  }
  $canaryText = Get-Content -LiteralPath $canaryPath -Raw
  $deployText = Get-Content -LiteralPath $deployPath -Raw
  foreach ($text in @($canaryText, $deployText)) {
    if ($text -notmatch 'ExpectedLiveSchema' -or $text -notmatch 'ExpectedTargetSchema') {
      throw 'Memory V2 executable wrapper lost explicit live/target schema binding'
    }
  }
  if ($moduleText -notmatch 'canary transcript status must be PASS' -or
      $moduleText -notmatch 'canary source must be a retained backup') {
    throw 'Memory V2 tooling lost transcript semantics or active-live source rejection'
  }
  $sqliteText = Get-Content -LiteralPath $sqlitePath -Raw
  if ($sqliteText -notmatch 'mode=ro&immutable=1' -or
      $moduleText -match '\[IO\.File\]::Replace\([^\r\n]*,\s*\$null\s*\)') {
    throw 'Memory V2 deployment tooling lost immutable SQLite or non-null atomic backup safety'
  }
  & python -c 'import ast,pathlib,sys; ast.parse(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))' $sqlitePath
  if ($LASTEXITCODE -ne 0) { throw 'Memory V2 SQLite helper does not parse' }
}

function Assert-PackageRejectsCanarySafely {
  param(
    [Parameter(Mandatory)][string]$Root,
    [Parameter(Mandatory)][string]$RelativePath,
    [Parameter(Mandatory)][string]$Canary,
    [Parameter(Mandatory)][Text.Encoding]$Encoding,
    [int]$PaddingBytes = 0,
    [switch]$AllowZaloCredentials
  )

  $path = Join-Path $Root $RelativePath
  $payload = $Encoding.GetBytes($Canary)
  $bytes = [byte[]]::new($PaddingBytes + $payload.Length)
  if ($PaddingBytes -gt 0) {
    [Array]::Fill($bytes, [byte]0x78, 0, $PaddingBytes)
  }
  [Buffer]::BlockCopy($payload, 0, $bytes, $PaddingBytes, $payload.Length)
  Write-TestBytes -Path $path -Bytes $bytes
  try {
    try {
      Assert-AppPackage -Out $Root -AllowZaloCredentials:$AllowZaloCredentials | Out-Null
    } catch {
      $expected = "package contains sensitive content in '$RelativePath'"
      if ($_.Exception.Message -ne $expected) {
        throw "Package scan returned unsafe or unexpected detail for '$RelativePath': $($_.Exception.Message)"
      }
      if ($_.Exception.Message.Contains($Canary)) {
        throw "Package scan leaked canary content for '$RelativePath'"
      }
      return
    }
    throw "Package scan accepted sensitive content in '$RelativePath'"
  } finally {
    if (Test-Path -LiteralPath $path) {
      Remove-Item -LiteralPath $path -Force
    }
  }
}

function Assert-PackageRejectsMissingSignatureSafely {
  param(
    [Parameter(Mandatory)][string]$Root,
    [Parameter(Mandatory)][string]$BinaryContent,
    [Parameter(Mandatory)][string]$Label,
    [Parameter(Mandatory)][string]$Canary
  )

  $binary = Join-Path $Root 'app\agentdc.exe'
  Write-TestFile -Path $binary -Content "$BinaryContent $Canary`n"
  try {
    Assert-AppPackage -Out $Root | Out-Null
  } catch {
    $errorMessage = $_.Exception.Message
    if ($errorMessage.Contains($Canary)) {
      throw "Package signature scan leaked canary content for $Label"
    }
    if ($errorMessage -ne "package binary missing $Label") {
      throw "Package missing $Label returned an unexpected error"
    }
    return
  }

  throw "Package missing $Label was accepted"
}

function Get-SeamTargetHashes {
  param([Parameter(Mandatory)][string]$Stage)

  $hashes = @{}
  foreach ($relative in @(
      'internal\daemon\server.go',
      'internal\store\store.go',
      'internal\daemon\zalo.go',
      'internal\daemon\duty.go',
      'internal\daemon\zalolesson.go',
      'internal\daemon\app_zalo_session_hook.go',
      'internal\webui\static\zalo.js'
    )) {
    $hashes[$relative] = (Get-FileHash -LiteralPath (Join-Path $Stage $relative) -Algorithm SHA256).Hash
  }
  return $hashes
}

function Assert-SeamTargetHashes {
  param(
    [Parameter(Mandatory)][string]$Stage,
    [Parameter(Mandatory)][hashtable]$Expected,
    [Parameter(Mandatory)][string]$Message
  )

  foreach ($relative in $Expected.Keys) {
    $actual = (Get-FileHash -LiteralPath (Join-Path $Stage $relative) -Algorithm SHA256).Hash
    Assert-Equal $actual $Expected[$relative] "$Message ($relative)"
  }
}

function Assert-CRLFUTF8File {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][string]$ExpectedText
  )

  $bytes = [IO.File]::ReadAllBytes($Path)
  if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
    throw 'UTF-8 BOM was added'
  }
  $strictUTF8 = [Text.UTF8Encoding]::new($false, $true)
  $text = $strictUTF8.GetString($bytes)
  if (-not $text.Contains($ExpectedText)) {
    throw 'Vietnamese text did not survive UTF-8 decoding'
  }
  if (-not $text.Contains("`r`n")) {
    throw 'CRLF line endings were removed'
  }
  if ([regex]::IsMatch($text, '(?<!\r)\n')) {
    throw 'File contains a bare LF'
  }
  if ([regex]::IsMatch($text, '\r(?!\n)')) {
    throw 'File contains a bare CR'
  }
}

function Invoke-StagedZaloSessionAcceptance {
  param(
    [Parameter(Mandatory)][string]$Repo,
    [Parameter(Mandatory)][string]$Overlay
  )

  Assert-CleanGitSource -Repo $Repo | Out-Null
  $sourceDuty = Join-Path $Repo 'internal\daemon\duty.go'
  $sourceLesson = Join-Path $Repo 'internal\daemon\zalolesson.go'
  $sourceHashBefore = (Get-FileHash -LiteralPath $sourceDuty -Algorithm SHA256).Hash
  $sourceLessonHashBefore = (Get-FileHash -LiteralPath $sourceLesson -Algorithm SHA256).Hash
  $sourceStatusBefore = (& git -C $Repo status --short) -join "`n"
  if ($LASTEXITCODE -ne 0) { throw 'Could not read real upstream status before staged acceptance' }
  $stageRoot = Join-Path ([IO.Path]::GetTempPath()) `
    ('zalo-session-stitched-' + [guid]::NewGuid().ToString('N'))
  $overlaySnapshot = Join-Path ([IO.Path]::GetTempPath()) `
    ('zalo-session-overlay-' + [guid]::NewGuid().ToString('N'))

  try {
    # The merge candidate is intentionally dirty until its single atomic merge commit exists.
    # Exercise the exact working overlay from an isolated snapshot without weakening the clean
    # production-overlay gate that build-app.ps1 uses.
    Copy-Item -LiteralPath $Overlay -Destination $overlaySnapshot -Recurse -Force
    Write-TestFile (Join-Path $overlaySnapshot 'internal\daemon\answer_call_comment.go') @'
package daemon

// a.appAnswerZalo(deps, threadID, question, reply, files) is documentation, not an executable call.
'@
    $stage = New-AppStage -Repo $Repo -Overlay $overlaySnapshot -StageRoot $stageRoot
    Apply-AppSeams -Stage $stage
    $stagedDuty = [IO.File]::ReadAllText((Join-Path $stage 'internal\daemon\duty.go'))
    $stagedLesson = [IO.File]::ReadAllText((Join-Path $stage 'internal\daemon\zalolesson.go'))
    Assert-AppProviderRouteTopology -Path (Join-Path $stage 'internal\daemon') `
      -ExpectedAnswerCallCount 1
    # The same package-wide AST verifier used by Apply-AppSeams ignores comments and _test.go.
    # Keep the remaining checks focused on independent post-seam capabilities.
    $sessionHook = [IO.File]::ReadAllText((Join-Path $stage 'internal\daemon\app_zalo_session_hook.go'))
    if ($sessionHook.IndexOf('SessionAdvanced', [StringComparison]::Ordinal) -lt 0) {
      throw 'Real staged session hook lost the structured SessionAdvanced gate'
    }
    $appSchema = [IO.File]::ReadAllText((Join-Path $stage 'internal\store\app_schema.go'))
    foreach ($signature in @(
        'const appSchemaVersion int64 = 8',
        'llm_combos',
        'app_memory_subject_revisions',
        'app_onboarding_provider_stages'
      )) {
      if ($appSchema.IndexOf($signature, [StringComparison]::Ordinal) -lt 0) {
        throw "Real staged schema lost capability-bridge signature '$signature'"
      }
    }
    if ($appSchema -match "(?s)INSERT\s+INTO\s+llm_combos.+?'default'") {
      throw 'Real staged schema reintroduced a seeded default combo'
    }
    if ([regex]::Matches($stagedDuty, 'a\.appAnswerZalo\(deps, threadID, question, reply, files\)').Count -ne 1 -or
        [regex]::Matches($stagedDuty, 'a\.appRunZalo\(ctx, run, pz, threadID, question, appZaloCurrentMsgID\(reply\.ReplyQuote\), history, found, files, step\)').Count -ne 1) {
      throw 'Real staged duty.go does not contain both session seams exactly once'
    }
    if ([regex]::Matches($stagedLesson, 'a\.st\.CreateAppLesson\(lesson\)').Count -ne 1 -or
        [regex]::Matches($stagedLesson, 'lesson := store\.AppLessonInput\{').Count -ne 1 -or
        $stagedLesson -match 'AddZaloMemory') {
      throw 'Real staged operator rewrite did not replace the legacy Memory seam exactly once'
    }

    Push-Location $stage
    try {
      $output = (& go test -count=1 `
        -run '^(TestAppZaloStagedSeamCreatesResumesAndIsolatesThreadsAcrossAPIRecreation|TestOperatorRewriteBecomesStructuredGlobalLesson|TestReplyEndpointRecordsTheLessonAndQueuesNextTurnRefresh|TestReplyEndpointKeepsSendingWhenLessonInsertFails|TestAppLLMRunnerStructuredAPISuccessUsesFullPromptWithoutAdvancingSession|TestAppLLMRunnerStructuredFallbackUsesClaudeDeltaAndAdvancesSession|TestAppZaloVirginSessionStaysFreshAfterStatelessSuccess|TestAppAnswerZaloBuildsProviderRunnerInsideThreadGate|TestAppZaloRunnerSilentWhenNoProvider|TestRunClaudeEscalatesWhenTerminalDeclines|TestAppLLMRunnerZeroesTheCredentialAfterTheCall|TestCLIAdapterGenerateClassifiesExitWithoutLeakingStderr)$' `
        -v ./internal/daemon 2>&1) -join "`n"
      $goExit = $LASTEXITCODE
    } finally {
      Pop-Location
    }
    if ($goExit -ne 0) {
      throw "Focused Go acceptance failed in the stitched stage (exit $goExit)`n$output"
    }
    foreach ($name in @(
        'TestAppZaloStagedSeamCreatesResumesAndIsolatesThreadsAcrossAPIRecreation',
        'TestOperatorRewriteBecomesStructuredGlobalLesson',
        'TestReplyEndpointRecordsTheLessonAndQueuesNextTurnRefresh',
        'TestReplyEndpointKeepsSendingWhenLessonInsertFails',
        'TestAppLLMRunnerStructuredAPISuccessUsesFullPromptWithoutAdvancingSession',
        'TestAppLLMRunnerStructuredFallbackUsesClaudeDeltaAndAdvancesSession',
        'TestAppZaloVirginSessionStaysFreshAfterStatelessSuccess',
        'TestAppAnswerZaloBuildsProviderRunnerInsideThreadGate',
        'TestAppZaloRunnerSilentWhenNoProvider',
        'TestRunClaudeEscalatesWhenTerminalDeclines',
        'TestAppLLMRunnerZeroesTheCredentialAfterTheCall',
        'TestCLIAdapterGenerateClassifiesExitWithoutLeakingStderr'
      )) {
      if ($output -notmatch ('--- PASS: ' + [regex]::Escape($name))) {
        throw "Focused Go acceptance '$name' was not executed in the stitched stage"
      }
    }

  } finally {
    if (Test-Path -LiteralPath $stageRoot) {
      Remove-Item -LiteralPath $stageRoot -Recurse -Force
    }
    if (Test-Path -LiteralPath $overlaySnapshot) {
      Remove-Item -LiteralPath $overlaySnapshot -Recurse -Force
    }
  }

  $sourceHashAfter = (Get-FileHash -LiteralPath $sourceDuty -Algorithm SHA256).Hash
  $sourceLessonHashAfter = (Get-FileHash -LiteralPath $sourceLesson -Algorithm SHA256).Hash
  $sourceStatusAfter = (& git -C $Repo status --short) -join "`n"
  if ($LASTEXITCODE -ne 0) { throw 'Could not read real upstream status after staged acceptance' }
  Assert-Equal $sourceHashAfter $sourceHashBefore 'Real upstream duty.go changed during staged acceptance'
  Assert-Equal $sourceLessonHashAfter $sourceLessonHashBefore 'Real upstream zalolesson.go changed during staged acceptance'
  Assert-Equal $sourceStatusAfter $sourceStatusBefore 'Real upstream status changed during staged acceptance'
  Assert-CleanGitSource -Repo $Repo | Out-Null
}

$root = Join-Path ([IO.Path]::GetTempPath()) ('portal-path-' + [guid]::NewGuid().ToString('N'))

try {
  $repo = Join-Path $root 'Mã nguồn AgentDC'
  $out = Join-Path $root 'Gói bán'
  New-Item -ItemType Directory -Force -Path (Join-Path $repo '.git') | Out-Null

  $got = Resolve-BuildPaths -Repo $repo -Out $out
  Assert-Equal $got.Repo ([IO.Path]::GetFullPath($repo)) 'Repo path was not normalized'
  Assert-Equal $got.Out ([IO.Path]::GetFullPath($out)) 'Out path was not normalized'

  Remove-Item -LiteralPath (Join-Path $repo '.git') -Recurse -Force
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $repo -Out $out } `
    -Pattern 'repo Git' -Message 'A directory without .git was accepted'

  $nestedRepo = Join-Path $out 'source'
  New-Item -ItemType Directory -Force -Path (Join-Path $nestedRepo '.git') | Out-Null
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $nestedRepo -Out $out } `
    -Pattern 'output' -Message 'A repo inside the output directory was accepted'

  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $nestedRepo -Out $nestedRepo } `
    -Pattern 'output' -Message 'Using the repo itself as output was accepted'

  $outInsideRepo = Join-Path $nestedRepo 'dist'
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $nestedRepo -Out $outInsideRepo } `
    -Pattern 'output' -Message 'An output directory inside the source repo was accepted'

  $identitySentinel = Join-Path $nestedRepo 'physical-identity-sentinel.bin'
  Write-TestFile $identitySentinel 'PHYSICAL-IDENTITY-SENTINEL-UNCHANGED'
  $identitySentinelHash = (Get-FileHash -LiteralPath $identitySentinel -Algorithm SHA256).Hash
  $extendedRepoAlias = '\\?\' + ([IO.Path]::GetFullPath($nestedRepo))
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $nestedRepo -Out $extendedRepoAlias } `
    -Pattern 'physical|output|trùng|inside|overlap' `
    -Message 'An extended-length alias to the source repository was accepted as output'
  Assert-ThrowsLike -Action {
    Clear-AppOutput -Out $extendedRepoAlias -ProtectedRoot $nestedRepo
  } -Pattern 'physical|protected|overlap' `
    -Message 'Clear-AppOutput authorized an extended-length alias to a protected source'

  $shortRepoAlias = [PortalBuildTestPathAliases]::TryGetShortPath([IO.Path]::GetFullPath($nestedRepo))
  if (-not [string]::IsNullOrWhiteSpace($shortRepoAlias) -and
      -not $shortRepoAlias.Equals([IO.Path]::GetFullPath($nestedRepo), [StringComparison]::OrdinalIgnoreCase)) {
    Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $nestedRepo -Out $shortRepoAlias } `
      -Pattern 'physical|output|trùng|inside|overlap' `
      -Message 'A short-path alias to the source repository was accepted as output'
    Assert-ThrowsLike -Action {
      Clear-AppOutput -Out $shortRepoAlias -ProtectedRoot $nestedRepo
    } -Pattern 'physical|protected|overlap' `
      -Message 'Clear-AppOutput authorized a short-path alias to a protected source'
  } else {
    Write-Host 'INFO: short-path alias fixture skipped because 8.3 aliases are unavailable on this volume.'
  }
  Assert-Equal ((Get-FileHash -LiteralPath $identitySentinel -Algorithm SHA256).Hash) `
    $identitySentinelHash 'Physical alias rejection changed the source sentinel'

  Write-Host 'PASS: Resolve-BuildPaths normalizes and protects build paths.'
} finally {
  if (Test-Path -LiteralPath $root) {
    Remove-Item -LiteralPath $root -Recurse -Force
  }
}

$clearRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-clear-' + [guid]::NewGuid().ToString('N'))

$reparseRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-reparse-path-' + [guid]::NewGuid().ToString('N'))
$externalTarget = Join-Path ([IO.Path]::GetTempPath()) ('portal-external-sentinel-' + [guid]::NewGuid().ToString('N'))
$outAlias = Join-Path $reparseRoot 'out-alias'
$parentAlias = Join-Path $reparseRoot 'parent-alias'
$normalOut = Join-Path $reparseRoot 'normal-out'
$nestedAlias = Join-Path $normalOut 'nested-alias'

try {
  $repo = Join-Path $reparseRoot 'repo'
  New-Item -ItemType Directory -Force -Path (Join-Path $repo '.git') | Out-Null
  Write-TestFile (Join-Path $externalTarget 'sentinel.bin') 'EXTERNAL-SENTINEL-UNCHANGED'
  $sentinelHash = (Get-FileHash -LiteralPath (Join-Path $externalTarget 'sentinel.bin') -Algorithm SHA256).Hash

  New-Item -ItemType Junction -Path $outAlias -Target $externalTarget -ErrorAction Stop | Out-Null
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $repo -Out $outAlias } `
    -Pattern 'reparse|junction|symbolic' -Message 'An output junction alias was accepted'

  $externalParent = Join-Path $externalTarget 'parent-target'
  New-Item -ItemType Directory -Force -Path $externalParent | Out-Null
  New-Item -ItemType Junction -Path $parentAlias -Target $externalParent -ErrorAction Stop | Out-Null
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $repo -Out (Join-Path $parentAlias 'new-output') } `
    -Pattern 'reparse|junction|symbolic' -Message 'An output path below a reparse parent was accepted'

  New-Item -ItemType Directory -Force -Path $normalOut | Out-Null
  New-Item -ItemType Junction -Path $nestedAlias -Target $externalTarget -ErrorAction Stop | Out-Null
  Assert-ThrowsLike -Action { Resolve-BuildPaths -Repo $repo -Out $normalOut } `
    -Pattern 'reparse|junction|symbolic' -Message 'An existing output tree containing a nested junction was accepted'

  $afterHash = (Get-FileHash -LiteralPath (Join-Path $externalTarget 'sentinel.bin') -Algorithm SHA256).Hash
  Assert-Equal $afterHash $sentinelHash 'Output path validation changed the external sentinel'
  Write-Host 'PASS: output path validation rejects root, parent, and nested reparse points without touching external data.'
} finally {
  foreach ($junction in @($nestedAlias, $parentAlias, $outAlias)) {
    if (Test-Path -LiteralPath $junction) {
      $item = Get-Item -LiteralPath $junction -Force -ErrorAction Stop
      if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0) {
        throw "Refusing unsafe cleanup because fixture is no longer a reparse point: '$junction'"
      }
      [IO.Directory]::Delete($item.FullName)
    }
  }
  if (Test-Path -LiteralPath $reparseRoot) {
    Remove-Item -LiteralPath $reparseRoot -Recurse -Force -ErrorAction Stop
  }
  if (Test-Path -LiteralPath $externalTarget) {
    Remove-Item -LiteralPath $externalTarget -Recurse -Force -ErrorAction Stop
  }
}

try {
  $outWithWildcard = Join-Path $clearRoot 'Gói [ab]'
  $matchingSibling = Join-Path $clearRoot 'Gói a'
  $protectedRoot = Join-Path $clearRoot 'protected-source'
  New-Item -ItemType Directory -Force -Path $protectedRoot | Out-Null
  Write-TestFile (Join-Path $outWithWildcard 'remove.txt') "remove`n"
  Write-TestFile (Join-Path $matchingSibling 'keep.txt') "keep`n"

  Assert-ThrowsLike -Action {
    Clear-AppOutput -Out $outWithWildcard -ProtectedRoot @() -WhatIf
  } -Pattern 'ProtectedRoot|protected root' -Message 'Clear-AppOutput accepted an empty protected-root set'

  $identityMismatchOut = Join-Path $clearRoot 'identity-mismatch-output'
  $identityMismatchSentinel = Join-Path $identityMismatchOut 'sentinel.bin'
  Write-TestFile $identityMismatchSentinel 'EXPECTED-IDENTITY-SENTINEL-UNCHANGED'
  $identityMismatchHash = (Get-FileHash -LiteralPath $identityMismatchSentinel -Algorithm SHA256).Hash
  Assert-ThrowsLike -Action {
    Clear-AppOutput -Out $identityMismatchOut -ProtectedRoot $protectedRoot `
      -ExpectedOutIdentity '\\?\Volume{00000000-0000-0000-0000-000000000000}\not-this-output'
  } -Pattern 'physical identity changed' `
    -Message 'Clear-AppOutput accepted a mismatched expected physical identity'
  Assert-Equal ((Get-FileHash -LiteralPath $identityMismatchSentinel -Algorithm SHA256).Hash) `
    $identityMismatchHash 'ExpectedOutIdentity rejection changed the output sentinel'

  Clear-AppOutput -Out $outWithWildcard -ProtectedRoot $protectedRoot
  if (Test-Path -LiteralPath (Join-Path $outWithWildcard 'remove.txt')) {
    throw 'Exact output contents were not removed'
  }
  if (-not (Test-Path -LiteralPath (Join-Path $matchingSibling 'keep.txt'))) {
    throw 'Wildcard output path removed a matching sibling directory'
  }

  Write-TestFile (Join-Path $outWithWildcard 'data\keep.txt') "keep data`n"
  Write-TestFile (Join-Path $outWithWildcard 'app\remove.txt') "remove app`n"
  Clear-AppOutput -Out $outWithWildcard -ProtectedRoot $protectedRoot -KeepData
  if (-not (Test-Path -LiteralPath (Join-Path $outWithWildcard 'data\keep.txt'))) {
    throw 'KeepData removed the output data directory'
  }
  if (Test-Path -LiteralPath (Join-Path $outWithWildcard 'app')) {
    throw 'KeepData preserved a non-data output directory'
  }

  Write-Host 'PASS: Clear-AppOutput treats wildcard characters literally.'
} finally {
  if (Test-Path -LiteralPath $clearRoot) {
    Remove-Item -LiteralPath $clearRoot -Recurse -Force
  }
}

$personaRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-persona-' + [guid]::NewGuid().ToString('N'))

try {
  $personaSource = Join-Path $personaRoot 'Nguồn persona'
  $personaFile = Join-Path $personaSource 'persona.md'
  $rosterFile = Join-Path $personaSource 'roster.md'
  Write-TestFile $personaFile "persona`n"
  Write-TestFile $rosterFile "roster`n"

  $resolvedPersona = Resolve-PersonaSource -PersonaSource $personaSource
  Assert-Equal $resolvedPersona ([IO.Path]::GetFullPath($personaSource)) 'Persona source was not normalized'

  Remove-Item -LiteralPath $rosterFile -Force
  Assert-ThrowsLike -Action { Resolve-PersonaSource -PersonaSource $personaSource } `
    -Pattern 'persona' -Message 'An incomplete persona source was accepted'

  Write-Host 'PASS: Resolve-PersonaSource validates explicit persona input.'
} finally {
  if (Test-Path -LiteralPath $personaRoot) {
    Remove-Item -LiteralPath $personaRoot -Recurse -Force
  }
}

$cleanRepoRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-clean-source-' + [guid]::NewGuid().ToString('N'))
$cleanOverlayRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-clean-overlay-' + [guid]::NewGuid().ToString('N'))
$dirtyOverlayStage = Join-Path ([IO.Path]::GetTempPath()) ('portal-dirty-overlay-stage-' + [guid]::NewGuid().ToString('N'))

try {
  New-Item -ItemType Directory -Force -Path $cleanRepoRoot | Out-Null
  & git -C $cleanRepoRoot init --quiet
  & git -C $cleanRepoRoot config user.name 'Portal Test'
  & git -C $cleanRepoRoot config user.email 'portal-test@example.invalid'
  Write-TestFile (Join-Path $cleanRepoRoot 'tracked.txt') "committed`n"
  & git -C $cleanRepoRoot add -- 'tracked.txt'
  & git -C $cleanRepoRoot commit --quiet -m 'fixture'
  if ($LASTEXITCODE -ne 0) { throw 'Could not commit the clean-source fixture' }

  Assert-CleanGitSource -Repo $cleanRepoRoot | Out-Null

  New-Item -ItemType Directory -Force -Path $cleanOverlayRoot | Out-Null
  & git -C $cleanOverlayRoot init --quiet
  & git -C $cleanOverlayRoot config user.name 'Portal Test'
  & git -C $cleanOverlayRoot config user.email 'portal-test@example.invalid'
  Write-TestFile (Join-Path $cleanOverlayRoot 'overlay.txt') "committed overlay`n"
  & git -C $cleanOverlayRoot add -- 'overlay.txt'
  & git -C $cleanOverlayRoot commit --quiet -m 'fixture'
  if ($LASTEXITCODE -ne 0) { throw 'Could not commit the clean-overlay fixture' }
  Write-TestFile (Join-Path $cleanOverlayRoot 'overlay.txt') "working-tree overlay edit`n"
  Assert-ThrowsLike -Action {
    New-AppStage -Repo $cleanRepoRoot -Overlay $cleanOverlayRoot -StageRoot $dirtyOverlayStage
  } -Pattern 'overlay.*không sạch|overlay.*not clean' `
    -Message 'A dirty overlay worktree was accepted'

  Write-TestFile (Join-Path $cleanRepoRoot 'tracked.txt') "working-tree edit`n"
  Assert-ThrowsLike -Action { Assert-CleanGitSource -Repo $cleanRepoRoot } `
    -Pattern 'không sạch|not clean' -Message 'A dirty source repository was accepted'

  $checkpointSource = [IO.File]::ReadAllText($PSCommandPath)
  if ([regex]::Matches(
      $checkpointSource, 'Assert-CleanGitSource -Repo \$Repo \| Out-Null').Count -ne 2) {
    throw 'Real staged acceptance does not enforce a clean upstream both before and after staging'
  }

  Write-Host 'PASS: source and overlay cleanliness gates reject uncommitted package inputs.'
} finally {
  foreach ($path in @($cleanRepoRoot, $cleanOverlayRoot, $dirtyOverlayStage)) {
    if (Test-Path -LiteralPath $path) {
      Remove-Item -LiteralPath $path -Recurse -Force
    }
  }
}

$skipPattern = Get-AppGoTestSkipPattern
$supersededTests = @(
  'TestAppJSKnowsTheSessionEndedCloseReason'
  'TestAppJSSendsThePortalMutationHeader'
  'TestPortalReloadedKeyWithLiveSessionReachesTheShell'
  'TestPortalRootServesTheShellWithACookie'
  'TestPortalUsesModalNotBrowserDialogs'
  'TestAgentPortalNoLongerCarriesZalo'
  'TestModalCallsPassAnObject'
  'TestJoinGreetsOnceForEveryone'
)
foreach ($testName in $supersededTests) {
  if ($testName -notmatch $skipPattern) {
    throw "Exact superseded test is not skipped: $testName"
  }
  foreach ($nearMatch in @("Prefix$testName", "${testName}Regression")) {
    if ($nearMatch -match $skipPattern) {
      throw "A future near-match test would be skipped: $nearMatch"
    }
  }
}
$expectedSkipPattern = '^(?:TestAppJSKnowsTheSessionEndedCloseReason|TestAppJSSendsThePortalMutationHeader|TestPortalReloadedKeyWithLiveSessionReachesTheShell|TestPortalRootServesTheShellWithACookie|TestPortalUsesModalNotBrowserDialogs|TestAgentPortalNoLongerCarriesZalo|TestModalCallsPassAnObject|TestJoinGreetsOnceForEveryone)$'
Assert-Equal $skipPattern $expectedSkipPattern 'Go checkpoint skip pattern widened, narrowed, or changed order'
Write-Host 'PASS: Go checkpoint skips exactly eight reviewed tests.'

$packageRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-package-' + [guid]::NewGuid().ToString('N'))
$scanExternal = Join-Path ([IO.Path]::GetTempPath()) ('portal-scan-external-' + [guid]::NewGuid().ToString('N'))

try {
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'package missing' -Message 'An incomplete package was accepted'

  $required = @(
    'app\agentdc.exe', 'app\transport\dist\index.js', 'app\node\node.exe', 'app\node\npm.cmd',
    'app\node\node_modules\npm\bin\npm-cli.js',
    'Start.vbs', 'Stop.bat', 'README.txt', 'brain\wiki\index.md'
  )
  foreach ($relative in $required) {
    Write-TestFile (Join-Path $packageRoot $relative) "fixture`n"
  }
  $requiredPortalAssets = @(
    'assets\components\provider-connect.js'
    'assets\components\persona-fields.js'
    'assets\pages\onboarding.js'
    'assets\pages\settings.js'
  )
  $packageReadme = Join-Path $packageRoot 'README.txt'
  $validOnboarding = @'
Nhan doi "Start.vbs". Start.vbs mo Portal quan ly tai http://127.0.0.1:8770/.

Lam theo dung thu tu:
1. Mo trang Providers. Bam Connect cho Claude Code hoac Codex.
Nut Connect tu dong cai Claude duoc quan ly neu can; khong can cai Claude global thu cong.
Provider API dung endpoint/API key hien chua co trong Portal (Sap co).
2. Mo trang Combos. Chon model, tao Combo va kich hoat Combo de tao route dang hoat dong.
3. Mo trang Zalo. Ket noi bang ma QR.

Khong co Provider da ket noi va route dang hoat dong, bot co y im lang.
claude --version chi la chan doan tuy chon, khong phai buoc thiet lap.
'@
  Write-TestFile $packageReadme $validOnboarding
  $packagedNode = Join-Path $packageRoot 'app\node\node.exe'
  $packagedNpmCmd = Join-Path $packageRoot 'app\node\npm.cmd'
  $packagedNpmCLI = Join-Path $packageRoot 'app\node\node_modules\npm\bin\npm-cli.js'
  $hostNode = (Get-Command node -CommandType Application -ErrorAction Stop).Source
  Copy-Item -LiteralPath $hostNode -Destination $packagedNode -Force
  $fixtureNpmCLI = "process.stdout.write('99.88.77\n');`n"
  $fixtureNpmCmd = @'
@ECHO OFF
SETLOCAL
SET "NODE_EXE=%~dp0\node.exe"
SET "NPM_CLI_JS=%~dp0\node_modules\npm\bin\npm-cli.js"
"%NODE_EXE%" "%NPM_CLI_JS%" %*
'@
  Write-TestFile $packagedNpmCmd $fixtureNpmCmd
  Write-TestFile $packagedNpmCLI $fixtureNpmCLI
  $packageBinary = Join-Path $packageRoot 'app\agentdc.exe'
  $requiredBinarySignatures = @(
    '/memory/threads/',
    'app_memory_revisions',
    'app_memory_subject_revisions',
    'memory_ops',
    '/memory/threads/{tid}/{id}/approve',
    'người trực sửa lại câu trả lời của bot',
    '/llm/providers',
    '/llm/combos',
    'llm_providers',
    'llm_combos',
    '@openai/codex',
    'CODEX_HOME',
    'app_zalo_cli_sessions',
    'claude_account_id',
    'claude_config_dir',
    'llm_accounts',
    '@anthropic-ai/claude-code',
    'CLAUDE_CONFIG_DIR',
    'zalo: im lặng — chưa cấu hình provider'
  )
  $validPackageBinary = 'fixture ' + ($requiredBinarySignatures -join ' ')
  Write-TestFile $packageBinary "$validPackageBinary`n"
  foreach ($relative in $requiredPortalAssets) {
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern ('package missing ' + [regex]::Escape($relative)) `
      -Message "A package missing Portal asset '$relative' was accepted"
    Write-TestFile (Join-Path $packageRoot $relative) "fixture module`n"
  }
  Assert-AppPackage -Out $packageRoot | Out-Null

  $legacyOnlyOnboarding = @'
Nhan doi "Start.vbs".
Sau khoang 4 giay trinh duyet tu mo trang Zalo. Bam Ket noi va quet ma QR.
MOT LAN DUY NHAT: DANG NHAP CLAUDE
Day la buoc DUY NHAT khong the bo. Kiem tra bang claude --version.
'@
  Write-TestFile $packageReadme $legacyOnlyOnboarding
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'package README' `
    -Message 'A package carrying the legacy Zalo-and-global-Claude-only onboarding was accepted'
  foreach ($onboardingCase in @(
      @{ Missing = 'Start.vbs mo Portal quan ly tai http://127.0.0.1:8770/.'; Label = 'management Portal startup guidance' },
      @{ Missing = '1. Mo trang Providers'; Label = 'Providers guidance' },
      @{ Missing = 'Bam Connect cho Claude Code hoac Codex'; Label = 'packaged Provider Connect guidance' },
      @{ Missing = 'Nut Connect tu dong cai Claude duoc quan ly neu can'; Label = 'managed Claude installation guidance' },
      @{ Missing = 'khong can cai Claude global thu cong'; Label = 'no-global-Claude-install guidance' },
      @{ Missing = 'Provider API dung endpoint/API key hien chua co trong Portal (Sap co)'; Label = 'unavailable API Provider guidance' },
      @{ Missing = '2. Mo trang Combos'; Label = 'Combos guidance' },
      @{ Missing = 'Chon model'; Label = 'model guidance' },
      @{ Missing = 'route dang hoat dong'; Label = 'active-route guidance' },
      @{ Missing = '3. Mo trang Zalo'; Label = 'Zalo-last guidance' },
      @{ Missing = 'bot co y im lang'; Label = 'intentional-silence guidance' },
      @{ Missing = 'claude --version chi la chan doan tuy chon, khong phai buoc thiet lap'; Label = 'optional CLI-diagnostic guidance' }
    )) {
    Write-TestFile $packageReadme $validOnboarding.Replace($onboardingCase.Missing, '')
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'package README' `
      -Message "A package missing $($onboardingCase.Label) was accepted"
  }
  $wrongOrderOnboarding = @'
Nhan doi "Start.vbs". Start.vbs mo Portal quan ly tai http://127.0.0.1:8770/.

Lam theo dung thu tu:
2. Mo trang Combos. Chon model, tao Combo va kich hoat Combo de tao route dang hoat dong.
1. Mo trang Providers. Bam Connect cho Claude Code hoac Codex.
Nut Connect tu dong cai Claude duoc quan ly neu can; khong can cai Claude global thu cong.
Provider API dung endpoint/API key hien chua co trong Portal (Sap co).
3. Mo trang Zalo. Ket noi bang ma QR.

Khong co Provider da ket noi va route dang hoat dong, bot co y im lang.
claude --version chi la chan doan tuy chon, khong phai buoc thiet lap.
'@
  Write-TestFile $packageReadme $wrongOrderOnboarding
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'onboarding order' `
    -Message 'A semantically complete package README with the wrong onboarding order was accepted'
  foreach ($misleadingGuidance in @(
      'Cai Claude Code global thu cong truoc khi mo Portal.',
      'Voi Provider API, nhap endpoint, API key va model trong Portal.'
    )) {
    Write-TestFile $packageReadme "$validOnboarding`n$misleadingGuidance`n"
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'misleading setup' `
      -Message "A package README containing misleading setup guidance was accepted: $misleadingGuidance"
  }
  Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\launcher\README.txt') `
    -Destination $packageReadme -Force
  Assert-AppPackage -Out $packageRoot | Out-Null
  Write-TestFile $packageReadme $validOnboarding

  foreach ($runtimeFile in @('app\node\npm.cmd', 'app\node\node_modules\npm\bin\npm-cli.js')) {
    $runtimePath = Join-Path $packageRoot $runtimeFile
    Remove-Item -LiteralPath $runtimePath -Force
    try {
      Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
        -Pattern ('package missing ' + [regex]::Escape($runtimeFile)) `
        -Message "A package missing npm runtime file '$runtimeFile' was accepted"
    } finally {
      if ($runtimeFile -eq 'app\node\node_modules\npm\bin\npm-cli.js') {
        Write-TestFile $runtimePath $fixtureNpmCLI
      } else {
        Write-TestFile $runtimePath $fixtureNpmCmd
      }
    }
  }

  Write-TestFile $packagedNode "corrupt packaged node`n"
  try {
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'packaged npm runtime|npm runtime' `
      -Message 'A corrupt packaged node executable was accepted'
  } finally {
    Copy-Item -LiteralPath $hostNode -Destination $packagedNode -Force
  }

  Write-TestFile $packagedNpmCLI "process.exit(17);`n"
  try {
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'packaged npm runtime|npm runtime' `
      -Message 'A corrupt packaged npm-cli.js was accepted'
  } finally {
    Write-TestFile $packagedNpmCLI $fixtureNpmCLI
  }

  Write-TestFile $packagedNpmCmd "@ECHO OFF`nECHO 99.88.77`n"
  try {
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'npm\.cmd|packaged npm shim' `
      -Message 'A nondelegating packaged npm.cmd shim was accepted'
  } finally {
    Write-TestFile $packagedNpmCmd $fixtureNpmCmd
  }

  Write-TestFile $packageBinary ($validPackageBinary.Replace('/memory/threads/', '') + "`n")
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'Memory API signature' -Message 'A package missing the Memory API signature was accepted'
  Write-TestFile $packageBinary ($validPackageBinary.Replace('app_memory_revisions', '') + "`n")
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'Memory schema signature' -Message 'A package missing the Memory schema signature was accepted'
  $memoryV2Canary = 'APP_TEST_MEMORY_V2_SIGNATURE_CANARY_6D40B3'
  Assert-PackageRejectsMissingSignatureSafely -Root $packageRoot `
    -BinaryContent $validPackageBinary.Replace('app_memory_subject_revisions', '') `
    -Label 'Memory V2 schema signature' -Canary $memoryV2Canary
  Assert-PackageRejectsMissingSignatureSafely -Root $packageRoot `
    -BinaryContent $validPackageBinary.Replace('memory_ops', '') `
    -Label 'Memory V2 prompt contract' -Canary $memoryV2Canary
  Assert-PackageRejectsMissingSignatureSafely -Root $packageRoot `
    -BinaryContent $validPackageBinary.Replace('/memory/threads/{tid}/{id}/approve', '') `
    -Label 'Memory V2 proposal API' -Canary $memoryV2Canary
  Assert-PackageRejectsMissingSignatureSafely -Root $packageRoot `
    -BinaryContent $validPackageBinary.Replace('người trực sửa lại câu trả lời của bot', '') `
    -Label 'structured operator lesson' -Canary $memoryV2Canary
  $providerPackageCanary = 'APP_TEST_PROVIDER_SIGNATURE_CANARY_9A1D4C'
  foreach ($signatureCase in @(
      @{ Signature = '/llm/providers'; Label = 'Provider API signature' },
      @{ Signature = '/llm/combos'; Label = 'Combo API signature' },
      @{ Signature = 'llm_providers'; Label = 'Provider schema signature' },
      @{ Signature = 'llm_combos'; Label = 'Combo schema signature' },
      @{ Signature = '@openai/codex'; Label = 'Codex CLI package signature' },
      @{ Signature = 'CODEX_HOME'; Label = 'Codex account isolation signature' }
    )) {
    $without = $validPackageBinary.Replace($signatureCase.Signature, '')
    Assert-PackageRejectsMissingSignatureSafely -Root $packageRoot `
      -BinaryContent $without -Label $signatureCase.Label -Canary $providerPackageCanary
  }
  foreach ($signatureCase in @(
      @{ Signature = 'app_zalo_cli_sessions'; Label = 'Zalo session schema signature' },
      @{ Signature = 'claude_account_id'; Label = 'Zalo session Claude account binding signature' },
      @{ Signature = 'claude_config_dir'; Label = 'Zalo session Claude config binding signature' },
      @{ Signature = 'llm_accounts'; Label = 'LLM account schema signature' },
      @{ Signature = '@anthropic-ai/claude-code'; Label = 'Claude CLI package signature' },
      @{ Signature = 'CLAUDE_CONFIG_DIR'; Label = 'Claude account isolation signature' },
      @{ Signature = 'zalo: im lặng — chưa cấu hình provider'; Label = 'silent no-provider route signature' }
    )) {
    $without = $validPackageBinary.Replace($signatureCase.Signature, '')
    Assert-PackageRejectsMissingSignatureSafely -Root $packageRoot `
      -BinaryContent $without -Label $signatureCase.Label -Canary $providerPackageCanary
  }
  Write-TestFile $packageBinary "$validPackageBinary`n"

  $publicScannerHarmless = Join-Path $packageRoot 'app\scanner-harmless.txt'
  $publicScannerCanary = Join-Path $packageRoot 'app\scanner-provider-canary.txt'
  Write-TestFile $publicScannerHarmless "harmless`n"
  Write-TestFile $publicScannerCanary "sk-package-must-never-contain-7f36d2`n"
  try {
    if ((Get-Command Assert-NoProviderCredential).Parameters.ContainsKey('Files')) {
      throw 'Public Provider credential scanner still exposes a caller-controlled Files subset'
    }
    Assert-ThrowsLike -Action { Assert-NoProviderCredential -Root $packageRoot } `
      -Pattern 'plaintext provider credential' `
      -Message 'The public Provider scanner did not enumerate the complete package root'
  } finally {
    Remove-Item -LiteralPath $publicScannerHarmless, $publicScannerCanary -Force
  }

  $canaryCases = @(
    @{
      Relative = 'app\prompt-probe.bin'
      Value = 'APP_TEST_PROMPT_CANARY_7F18A2'
      Encoding = [Text.UTF8Encoding]::new($false)
      AllowZalo = $false
      Padding = 65532
    },
    @{
      Relative = 'app\response-probe.bin'
      Value = 'APP_TEST_RESPONSE_CANARY_93C4D1'
      Encoding = [Text.UnicodeEncoding]::new($false, $false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'app\stderr-probe.bin'
      Value = 'APP_TEST_RAW_STDERR_CANARY_5B60E7'
      Encoding = [Text.UnicodeEncoding]::new($true, $false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'data\zalo\credentials.json'
      Value = 'APP_TEST_ZALO_CREDENTIAL_CANARY_A8D239'
      Encoding = [Text.UTF8Encoding]::new($false)
      AllowZalo = $true
      Padding = 0
    },
    @{
      Relative = 'data\providers\credential.cache'
      Value = 'APP_TEST_PROVIDER_CREDENTIAL_CANARY_C17F46'
      Encoding = [Text.UnicodeEncoding]::new($false, $false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'data\onboarding\test-token.txt'
      Value = 'ONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91'
      Encoding = [Text.UTF8Encoding]::new($false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'data\onboarding\user-prompt.txt'
      Value = 'ONBOARDING_USER_PROMPT_CANARY_CLEAR_8172'
      Encoding = [Text.UnicodeEncoding]::new($false, $false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'data\onboarding\answer.txt'
      Value = 'ONBOARDING_ANSWER_CANARY_CLEAR_BC43'
      Encoding = [Text.UnicodeEncoding]::new($true, $false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'data\onboarding\credential.cache'
      Value = 'ONBOARDING_CREDENTIAL_CANARY_CLEAR_D912'
      Encoding = [Text.UTF8Encoding]::new($false)
      AllowZalo = $false
      Padding = 0
    },
    @{
      Relative = 'data\accounts\claude-code\config-dir.txt'
      Value = 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E'
      Encoding = [Text.UTF8Encoding]::new($false)
      AllowZalo = $false
      Padding = 0
    }
  )
  foreach ($case in $canaryCases) {
    Assert-PackageRejectsCanarySafely -Root $packageRoot -RelativePath $case.Relative `
      -Canary $case.Value -Encoding $case.Encoding -PaddingBytes $case.Padding `
      -AllowZaloCredentials:([bool]$case.AllowZalo)
  }

  # This is the exact credential canary used by Provider tests. It deliberately does not use the
  # APP_TEST_ prefix, proving that the dedicated Provider gate is composed into Assert-AppPackage.
  $providerCanary = 'sk-package-must-never-contain-7f36d2'
  foreach ($credentialCase in @(
      @{ Relative = 'README.txt'; Bytes = [Text.Encoding]::UTF8.GetBytes("$validOnboarding`n$providerCanary`n"); Hidden = $false },
      @{ Relative = 'brain\.claude\settings.local.json.example'; Bytes = [Text.Encoding]::UTF8.GetBytes('{"canary":"' + $providerCanary + '"}'); Hidden = $false },
      @{ Relative = 'app\.env'; Bytes = [Text.Encoding]::UTF8.GetBytes("KEY=$providerCanary`n"); Hidden = $true },
      @{ Relative = 'app\agentdc.exe'; Bytes = [Text.Encoding]::UTF8.GetBytes("$validPackageBinary`n$providerCanary"); Hidden = $false },
      @{ Relative = 'app\provider-utf16le.bin'; Bytes = [Text.UnicodeEncoding]::new($false, $false).GetBytes($providerCanary); Hidden = $false },
      @{ Relative = 'app\provider-utf16be.bin'; Bytes = [Text.UnicodeEncoding]::new($true, $false).GetBytes($providerCanary); Hidden = $false }
    )) {
    $credentialPath = Join-Path $packageRoot $credentialCase.Relative
    $originalBytes = if (Test-Path -LiteralPath $credentialPath -PathType Leaf) {
      [IO.File]::ReadAllBytes($credentialPath)
    } else { $null }
    Write-TestBytes -Path $credentialPath -Bytes $credentialCase.Bytes
    if ($credentialCase.Hidden) {
      (Get-Item -LiteralPath $credentialPath -Force).Attributes =
        (Get-Item -LiteralPath $credentialPath -Force).Attributes -bor [IO.FileAttributes]::Hidden
    }
    try {
      Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
        -Pattern 'plaintext provider credential' `
        -Message "A package credential in '$($credentialCase.Relative)' was accepted"
    } finally {
      if ($null -ne $originalBytes) {
        [IO.File]::WriteAllBytes($credentialPath, $originalBytes)
      } elseif (Test-Path -LiteralPath $credentialPath) {
        Remove-Item -LiteralPath $credentialPath -Force
      }
    }
  }

  $hiddenDir = Join-Path $packageRoot 'app\.config'
  $hiddenCredential = Join-Path $hiddenDir 'keys.json'
  Write-TestFile $hiddenCredential ('{"canary":"' + $providerCanary + '"}' + "`n")
  (Get-Item -LiteralPath $hiddenDir -Force).Attributes =
    [IO.FileAttributes]::Directory -bor [IO.FileAttributes]::Hidden
  try {
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'plaintext provider credential' `
      -Message 'A file under a hidden directory carrying the Provider canary was accepted'
  } finally {
    (Get-Item -LiteralPath $hiddenDir -Force).Attributes = [IO.FileAttributes]::Directory
    Remove-Item -LiteralPath $hiddenDir -Recurse -Force
  }

  foreach ($dependencyCanary in @(
      (Join-Path $packageRoot 'app\node_modules\fixture\credential.txt'),
      (Join-Path $packageRoot 'app\node\node_modules\npm\fixture\credential.txt')
    )) {
    Write-TestFile $dependencyCanary "$providerCanary`n"
    try {
      Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
        -Pattern 'plaintext provider credential' `
        -Message "An exact Provider canary under node_modules was exempted: '$dependencyCanary'"
    } finally {
      Remove-Item -LiteralPath $dependencyCanary -Force
    }
  }

  $externalSentinel = Join-Path $scanExternal 'sentinel.bin'
  Write-TestFile $externalSentinel 'SCAN-EXTERNAL-SENTINEL-UNCHANGED'
  $externalHash = (Get-FileHash -LiteralPath $externalSentinel -Algorithm SHA256).Hash
  $packageJunction = Join-Path $packageRoot 'app\linked-external'
  New-Item -ItemType Junction -Path $packageJunction -Target $scanExternal -ErrorAction Stop | Out-Null
  try {
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'reparse|junction|symbolic' `
      -Message 'A package directory junction was accepted or followed'
    Assert-Equal ((Get-FileHash -LiteralPath $externalSentinel -Algorithm SHA256).Hash) $externalHash `
      'Package scan changed the external sentinel behind a junction'
  } finally {
    if (Test-Path -LiteralPath $packageJunction) {
      $junctionItem = Get-Item -LiteralPath $packageJunction -Force -ErrorAction Stop
      if (($junctionItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0) {
        throw "Refusing unsafe cleanup because package fixture is no longer a junction: '$packageJunction'"
      }
      [IO.Directory]::Delete($junctionItem.FullName)
    }
  }

  $locked = Join-Path $packageRoot 'app\locked.txt'
  Write-TestFile $locked "unreadable`n"
  $handle = [IO.File]::Open($locked, 'Open', 'Read', 'None')
  $savedPreference = $ErrorActionPreference
  try {
    $ErrorActionPreference = 'Continue'
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'locked\.txt' -Message 'An unreadable package file was skipped instead of failing closed'
  } finally {
    $ErrorActionPreference = $savedPreference
    $handle.Close()
    Remove-Item -LiteralPath $locked -Force
  }

  Write-TestFile (Join-Path $packageRoot 'data\zalo\credentials.json') `
    "APP_TEST_ZALO_CREDENTIAL_CANARY_A8D239`n"
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'Zalo credentials' -Message 'A package containing Zalo credentials was accepted'

  Write-Host 'PASS: package scan rejects prompt, response, raw-stderr, Zalo and Provider credential canaries.'
  Write-Host 'PASS: Assert-AppPackage requires runtime files and rejects credentials.'
} finally {
  if (Test-Path -LiteralPath $packageRoot) {
    Remove-Item -LiteralPath $packageRoot -Recurse -Force
  }
  if (Test-Path -LiteralPath $scanExternal) {
    Remove-Item -LiteralPath $scanExternal -Recurse -Force
  }
}

$stageTestRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-stage-' + [guid]::NewGuid().ToString('N'))

try {
  $repo = Join-Path $stageTestRoot 'Mã nguồn AgentDC'
  $overlay = Join-Path $stageTestRoot 'Portal overlay'
  $stage = Join-Path $stageTestRoot 'Bản dựng'

  New-Item -ItemType Directory -Force -Path $repo | Out-Null
  & git -C $repo init --quiet
  if ($LASTEXITCODE -ne 0) { throw 'Could not initialize the fixture Git repository' }

  Write-TestFile (Join-Path $repo 'internal\daemon\server.go') @'
package daemon

func server() {
	mux.Handle("POST /shutdown", a.auth(a.handleShutdown))
}
'@
  Write-TestFile (Join-Path $repo 'internal\store\store.go') @'
package store

func migrate(db *sql.DB) error {
	return nil
}

// hasColumn asks SQLite rather than tracking a version number.
'@
  Write-TestFile (Join-Path $repo 'internal\daemon\zalo.go') @'
package daemon

func incoming() {
	switch {
	default:
		delay := time.Second
		a.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{})
	}
}
'@
  Write-TestFile (Join-Path $repo 'internal\daemon\duty.go') @'
package daemon

type zaloConfig struct {
	Model string
}

const maxZaloAnswers = 6

func answer() {
	raw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)
	if err != nil {
		return escalate("the agent did not finish", "err", err)
	}
}

func trigger() {
	go func() {
		started := time.Now()
		defer a.turns.end(turn)
		ctx, cancel := context.WithTimeout(context.Background(), deps.cfg.Timeout)
		defer cancel()
		// Bước của lượt đi vào log dùng chung, không phải một danh sách riêng của lượt:
		// terminal là một dòng thời gian, và một lượt đã xong vẫn nằm đó để đọc.
		step := func(text string) { a.zlog.add(ipc.ZaloLogStep, threadID, text) }
		err := a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)
		took := time.Since(started).Round(time.Second)
	}()
}

func (e execZaloRunner) Run(ctx context.Context, prompt string, step func(string)) (string, error) {
	prof := agent.ConsultReadOnly()
	bin, err := exec.LookPath(prof.Binary)
	if err != nil {
		return "", fmt.Errorf("locate %s: %w", prof.Binary, err)
	}
	args, err := consultArgv(e.cfg, uuid.NewString())
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = prof.Env(os.Environ())
	if e.cfg.ThinkingTokens > 0 {
	}
	return "", nil
}
'@
  Write-TestFile (Join-Path $repo 'internal\daemon\zalolesson.go') @'
package daemon

import (
	"strings"
	"agentdc/internal/ipc"
)

// Ghi vào zalo_memory như mọi ghi chú khác, nên nó cũng KHÔNG trích dẫn được — xem chú thích bảng
// đó. Một bài học là chữ do hệ này tự sinh, tức đúng thứ không được thành nguồn.
func (a *api) noteOperatorRewrite(threadID, operatorText string) {
	text := "người trực sửa lại: bot nói \"" + clip(strings.TrimSpace(bot.Body), maxLessonPart) +
		"\" → người trực gửi \"" + clip(strings.TrimSpace(operatorText), maxLessonPart) + "\""
	if err := a.st.AddZaloMemory(ipc.ZaloMemory{ThreadID: threadID, Text: text}); err != nil {
		return
	}
}
'@
  Write-TestFile (Join-Path $repo 'internal\daemon\ignored_provider_route_test.go') @'
package daemon

// Test files are intentionally outside the production topology contract.
func ignoredProviderRouteTestFixture() {
	_ = productionAppRuntimeContext(a).appZaloRunner
	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	_ = run
	_ = a.appAnswerZalo(deps, threadID, question, reply, files)
}
'@
  Write-TestFile (Join-Path $repo 'internal\webui\static\zalo.js') @'
const el = (id) => document.getElementById(id);
let curThread = null;
let threads = [];
let listSig = '';
let feedSig = '';
let outboxSig = '';
let atBottom = true;

async function refreshMemCount() {}
async function refresh() {
  const ths = [];
    threads = ths || [];
  renderList();
}
'@
  Write-TestFile (Join-Path $repo 'README.md') "tracked`n"
  Write-TestFile (Join-Path $repo 'untracked.txt') "must not be staged`n"
  $upstreamStyles = @{
    'app.css' = "upstream app sentinel`n"
    'zalo.css' = "upstream zalo sentinel`n"
    'modal.css' = "upstream modal sentinel`n"
  }
  foreach ($style in $upstreamStyles.GetEnumerator()) {
    Write-TestFile (Join-Path $repo "internal\webui\static\$($style.Key)") $style.Value
  }
  Write-TestFile (Join-Path $overlay 'internal\daemon\app_routes.go') "package daemon`n"
  Write-TestFile (Join-Path $overlay 'internal\daemon\app_zalo_session_hook.go') @'
package daemon

func (a *api) appAnswerZalo(
	deps *zaloDeps,
	threadID, question string,
	reply ipc.ZaloOutboxDraft,
	files []ipc.ZaloAttachment,
) error {
	return a.appAnswerZaloWithRunnerFactory(
		deps, threadID, question, reply, files, productionAppRuntimeContext(a).appZaloRunner,
	)
}

func (a *api) appAnswerZaloWithRunnerFactory(
	deps *zaloDeps,
	threadID, question string,
	reply ipc.ZaloOutboxDraft,
	files []ipc.ZaloAttachment,
	route appZaloRunnerFactory,
) error {
	if route == nil {
		return errors.New("zalo session runner factory is required")
	}
	release, err := appZaloProcessThreadGate.Acquire(
		context.Background(),
		fmt.Sprintf("%p\x00%s", a.st, threadID),
	)
	if err != nil {
		return err
	}
	defer release()
	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	_ = run
	effectiveConfig := deps.cfg
	var binding appZaloClaudeBinding
	run, effectiveConfig, binding, err = a.appResolveZaloSessionRoute(
		ctx, run, effectiveConfig, threadID,
	)
	if err != nil {
		return err
	}
	snapshot := a.appCaptureZaloTurnSnapshot(effectiveConfig, threadID, files)
	if aware, ok := run.(appZaloAttachmentAwareRunner); ok {
		run = aware.appWithZaloAttachments(snapshot.hasAttachments)
	}
	if aware, ok := run.(appZaloStructuredClaudeAwareRunner); ok {
		run = aware.appWithZaloStructuredClaudeRequirement(
			appZaloDeltaHasAttachments(snapshot.resumeDelta.delta),
		)
	}
	if structured, ok := run.(appZaloStructuredRunner); ok {
		run = &appZaloStructuredAnswerRunner{
			a:                a,
			run:              structured,
			zc:               effectiveConfig,
			threadID:         threadID,
			question:         question,
			currentZaloMsgID: appZaloCurrentMsgID(reply.ReplyQuote),
			history:          snapshot.history,
			directFiles:      snapshot.directFiles,
			files:            snapshot.files,
			highWater:        snapshot.highWater,
			highWaterKnown:   snapshot.highWaterKnown,
			resumeDelta:      snapshot.resumeDelta,
			claudeBinding:    binding,
			agentDisplayName: snapshot.agentDisplayName,
		}
	} else {
		run = &appZaloCapturedIdentityRunner{
			zaloRunner: run, agentDisplayName: snapshot.agentDisplayName,
		}
	}
	if err := a.answerZalo(
		ctx, deps.cfg, run, threadID, question, step, reply, files...,
	); err != nil {
		return err
	}
	return nil
}
'@
  Write-TestFile (Join-Path $overlay 'internal\webui\static\core\router.js') "export {};`n"
  Write-TestFile (Join-Path $overlay 'internal\webui\static\portal.css') "portal overlay sentinel`n"
  $productionStatic = Join-Path $PSScriptRoot '..\appmode\overlay\internal\webui\static'
  if (-not (Test-Path -LiteralPath $productionStatic -PathType Container)) {
    throw "Production Portal static directory missing: $productionStatic"
  }
  foreach ($stylesheet in @('app.css', 'zalo.css', 'modal.css')) {
    $productionStylesheet = Join-Path $productionStatic $stylesheet
    if (Test-Path -LiteralPath $productionStylesheet) {
      Copy-Item -LiteralPath $productionStylesheet `
        -Destination (Join-Path $overlay "internal\webui\static\$stylesheet") -Force
    }
  }

  & git -C $repo add -- 'internal' 'README.md'
  if ($LASTEXITCODE -ne 0) { throw 'Could not stage fixture files' }
  $statusBefore = (& git -C $repo status --short) -join "`n"
  $sourceDutyPath = Join-Path $repo 'internal\daemon\duty.go'
  $sourceLessonPath = Join-Path $repo 'internal\daemon\zalolesson.go'
  $sourceDutyHashBefore = (Get-FileHash -LiteralPath $sourceDutyPath -Algorithm SHA256).Hash
  $sourceLessonHashBefore = (Get-FileHash -LiteralPath $sourceLessonPath -Algorithm SHA256).Hash

  $gotStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot $stage
  Assert-Equal $gotStage ([IO.Path]::GetFullPath($stage)) 'Stage path was not normalized'
  foreach ($style in $upstreamStyles.GetEnumerator()) {
    $stagedStyle = [IO.File]::ReadAllText((Join-Path $gotStage "internal\webui\static\$($style.Key)"))
    Assert-Equal $stagedStyle $style.Value "Upstream $($style.Key) was replaced by the Portal overlay"
  }
  Apply-AppSeams -Stage $gotStage

  if (-not (Test-Path -LiteralPath (Join-Path $gotStage 'internal\daemon\app_routes.go'))) {
    throw 'Recursive overlay file was not copied'
  }
  if (-not (Test-Path -LiteralPath (Join-Path $gotStage 'internal\webui\static\core\router.js'))) {
    throw 'Nested overlay asset was not copied'
  }
  if (Test-Path -LiteralPath (Join-Path $gotStage 'untracked.txt')) {
    throw 'An untracked source file was copied into the stage'
  }

  $stagedServer = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\server.go'))
  $stagedStore = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\store\store.go'))
  $stagedZalo = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\zalo.go'))
  $stagedDuty = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\duty.go'))
  $stagedLesson = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\zalolesson.go'))
  $stagedZaloUI = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\webui\static\zalo.js'))
  if ([regex]::Matches($stagedServer, 'a\.registerAppRoutes\(mux\)').Count -ne 1) { throw 'Route seam was not applied exactly once' }
  if ([regex]::Matches($stagedStore, 'migrateApp\(db\)').Count -ne 1) { throw 'Migration seam was not applied exactly once' }
  if ([regex]::Matches($stagedZalo, 'evaluateAppWorkflow\(req, msg\)').Count -ne 1) { throw 'Workflow seam was not applied exactly once' }
  if ([regex]::Matches($stagedDuty, 'a\.appAnswerZalo\(deps, threadID, question, reply, files\)').Count -ne 1) {
    throw 'Session answer seam was not applied exactly once'
  }
  if ([regex]::Matches($stagedDuty, 'a\.appRunZalo\(ctx, run, pz, threadID, question, appZaloCurrentMsgID\(reply\.ReplyQuote\), history, found, files, step\)').Count -ne 1) {
    throw 'Session runner seam was not applied exactly once'
  }
  foreach ($signature in @(
      'ConfigDir string',
      'Program string',
      'ProgramPrefixArgs []string',
      'var ErrZaloSilent = errors.New(',
      'errors.Is(err, ErrZaloSilent)',
      'CLAUDE_CONFIG_DIR=',
      'args = append(prefixArgs, args...)'
    )) {
    if ([regex]::Matches($stagedDuty, [regex]::Escape($signature)).Count -ne 1) {
      throw "Managed Claude/no-provider seam '$signature' was not applied exactly once"
    }
  }
  if ([regex]::Matches($stagedLesson, 'a\.st\.CreateAppLesson\(lesson\)').Count -ne 1 -or
      [regex]::Matches($stagedLesson, 'lesson := store\.AppLessonInput\{').Count -ne 1 -or
      $stagedLesson -match 'AddZaloMemory') {
    throw 'Structured operator lesson seam was not applied exactly once'
  }
  if ([regex]::Matches($stagedZaloUI, [regex]::Escape("const requestedThreadID = new URLSearchParams(window.location.search).get('thread')")).Count -ne 1) {
    throw 'Zalo deep-link declaration seam was not applied exactly once'
  }
  if ([regex]::Matches($stagedZaloUI, [regex]::Escape('if (!requestedThreadHandled) {')).Count -ne 1) {
    throw 'Zalo deep-link selection seam was not applied exactly once'
  }
  if ($stagedDuty -match 'context\.WithTimeout\(context\.Background\(\), deps\.cfg\.Timeout\)' -or
      $stagedDuty -match 'a\.answerZalo\(ctx, deps\.cfg, deps\.run' -or
      $stagedDuty -match 'run\.Run\(ctx, buildConsultPrompt\(') {
    throw 'A superseded stateless duty path remains in the stage'
  }
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $gotStage } `
    -Pattern 'route seam: inserted signature already present' -Message 'Applying seams twice was accepted'
  if ([regex]::Matches([IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\server.go')), 'a\.registerAppRoutes\(mux\)').Count -ne 1) {
    throw 'A repeated seam application duplicated the route call'
  }

  $sourceServer = [IO.File]::ReadAllText((Join-Path $repo 'internal\daemon\server.go'))
  if ($sourceServer -match 'registerAppRoutes') { throw 'Source repo was changed by staging' }
  $sourceDuty = [IO.File]::ReadAllText($sourceDutyPath)
  if ($sourceDuty -match 'appAnswerZalo|appRunZalo') { throw 'Source duty.go was changed by staging' }
  $sourceLesson = [IO.File]::ReadAllText($sourceLessonPath)
  if ($sourceLesson -match 'CreateAppLesson') { throw 'Source zalolesson.go was changed by staging' }
  $sourceDutyHashAfter = (Get-FileHash -LiteralPath $sourceDutyPath -Algorithm SHA256).Hash
  $sourceLessonHashAfter = (Get-FileHash -LiteralPath $sourceLessonPath -Algorithm SHA256).Hash
  Assert-Equal $sourceDutyHashAfter $sourceDutyHashBefore 'Source duty.go hash changed during staging'
  Assert-Equal $sourceLessonHashAfter $sourceLessonHashBefore 'Source zalolesson.go hash changed during staging'
  $statusAfter = (& git -C $repo status --short) -join "`n"
  Assert-Equal $statusAfter $statusBefore 'Source Git status changed during staging'

  $missingStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu marker')
  $missingServerPath = Join-Path $missingStage 'internal\daemon\server.go'
  $missingServer = [IO.File]::ReadAllText($missingServerPath).Replace(
    "`tmux.Handle(`"POST /shutdown`", a.auth(a.handleShutdown))", '')
  [IO.File]::WriteAllText($missingServerPath, $missingServer, [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingStage } `
    -Pattern 'route seam: expected exactly 1 match' -Message 'A missing route marker was accepted'

  $duplicateStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Trùng marker')
  $duplicateServerPath = Join-Path $duplicateStage 'internal\daemon\server.go'
  $duplicateServer = [IO.File]::ReadAllText($duplicateServerPath)
  $duplicateServer += @'

func duplicateServerMarker() {
	mux.Handle("POST /shutdown", a.auth(a.handleShutdown))
}
'@
  [IO.File]::WriteAllText($duplicateServerPath, $duplicateServer, [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $duplicateStage } `
    -Pattern 'route seam: expected exactly 1 match' -Message 'A duplicate route marker was accepted'

  $missingMigrationStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu migration')
  $missingStorePath = Join-Path $missingMigrationStage 'internal\store\store.go'
  $missingStore = [IO.File]::ReadAllText($missingStorePath).Replace(
    '// hasColumn asks SQLite rather than tracking a version number.', '// marker removed')
  [IO.File]::WriteAllText($missingStorePath, $missingStore, [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingMigrationStage } `
    -Pattern 'migration seam: expected exactly 1 match' -Message 'A missing migration marker was accepted'
  if ([IO.File]::ReadAllText((Join-Path $missingMigrationStage 'internal\daemon\server.go')) -match 'registerAppRoutes') {
    throw 'A failed seam validation partially modified the stage'
  }

  $missingAnswerStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu answer seam')
  $missingAnswerDutyPath = Join-Path $missingAnswerStage 'internal\daemon\duty.go'
  $missingAnswerDuty = [IO.File]::ReadAllText($missingAnswerDutyPath).Replace(
    "`t`terr := a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)",
    "`t`terr := oldAnswerPathRemoved()")
  [IO.File]::WriteAllText($missingAnswerDutyPath, $missingAnswerDuty, [Text.UTF8Encoding]::new($false))
  $missingAnswerHashes = Get-SeamTargetHashes -Stage $missingAnswerStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingAnswerStage } `
    -Pattern 'session answer seam: expected exactly 1 match' -Message 'A missing session answer marker was accepted'
  Assert-SeamTargetHashes -Stage $missingAnswerStage -Expected $missingAnswerHashes `
    -Message 'A missing session answer marker partially modified the stage'

  $missingRunnerStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu runner seam')
  $missingRunnerDutyPath = Join-Path $missingRunnerStage 'internal\daemon\duty.go'
  $missingRunnerDuty = [IO.File]::ReadAllText($missingRunnerDutyPath).Replace(
    "`traw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)",
    "`traw, err := oldRunnerPathRemoved()")
  [IO.File]::WriteAllText($missingRunnerDutyPath, $missingRunnerDuty, [Text.UTF8Encoding]::new($false))
  $missingRunnerHashes = Get-SeamTargetHashes -Stage $missingRunnerStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingRunnerStage } `
    -Pattern 'session runner seam: expected exactly 1 match' -Message 'A missing session runner marker was accepted'
  Assert-SeamTargetHashes -Stage $missingRunnerStage -Expected $missingRunnerHashes `
    -Message 'A missing session runner marker partially modified the stage'

  $missingManagedStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu managed CLI')
  $missingManagedDutyPath = Join-Path $missingManagedStage 'internal\daemon\duty.go'
  $missingManagedDuty = [IO.File]::ReadAllText($missingManagedDutyPath).Replace("`tModel string", "`tSelectedModel string")
  [IO.File]::WriteAllText($missingManagedDutyPath, $missingManagedDuty, [Text.UTF8Encoding]::new($false))
  $missingManagedHashes = Get-SeamTargetHashes -Stage $missingManagedStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingManagedStage } `
    -Pattern 'managed Claude config seam: expected exactly 1 match' `
    -Message 'A missing managed Claude config marker was accepted'
  Assert-SeamTargetHashes -Stage $missingManagedStage -Expected $missingManagedHashes `
    -Message 'A missing managed Claude marker partially modified the stage'

  $commentOnlyProviderStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route chỉ trong comment')
  $commentOnlyProviderHookPath = Join-Path $commentOnlyProviderStage 'internal\daemon\app_zalo_session_hook.go'
  $commentOnlyProviderHook = [IO.File]::ReadAllText($commentOnlyProviderHookPath).Replace(
    'productionAppRuntimeContext(a).appZaloRunner',
    'fallbackRunner') + "`n// productionAppRuntimeContext(a).appZaloRunner is not executable here.`n"
  [IO.File]::WriteAllText(
    $commentOnlyProviderHookPath, $commentOnlyProviderHook, [Text.UTF8Encoding]::new($false))
  $commentOnlyProviderHashes = Get-SeamTargetHashes -Stage $commentOnlyProviderStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $commentOnlyProviderStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A comment-only Provider route reference was accepted'
  Assert-SeamTargetHashes -Stage $commentOnlyProviderStage -Expected $commentOnlyProviderHashes `
    -Message 'A comment-only Provider route reference partially modified the stage'

  $legacyReceiverStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route receiver cũ')
  $legacyReceiverHookPath = Join-Path $legacyReceiverStage 'internal\daemon\app_zalo_session_hook.go'
  $legacyReceiverHook = [IO.File]::ReadAllText($legacyReceiverHookPath).Replace(
    'productionAppRuntimeContext(a).appZaloRunner', 'a.appZaloRunner')
  [IO.File]::WriteAllText(
    $legacyReceiverHookPath, $legacyReceiverHook, [Text.UTF8Encoding]::new($false))
  $legacyReceiverHashes = Get-SeamTargetHashes -Stage $legacyReceiverStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $legacyReceiverStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'The legacy static api.appZaloRunner receiver was accepted'
  Assert-SeamTargetHashes -Stage $legacyReceiverStage -Expected $legacyReceiverHashes `
    -Message 'Legacy static Provider receiver rejection partially modified the stage'

  $wrongContextStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route sai context')
  $wrongContextHookPath = Join-Path $wrongContextStage 'internal\daemon\app_zalo_session_hook.go'
  $wrongContextHook = [IO.File]::ReadAllText($wrongContextHookPath).Replace(
    'productionAppRuntimeContext(a).appZaloRunner', 'productionAppRuntimeContext(other).appZaloRunner')
  [IO.File]::WriteAllText(
    $wrongContextHookPath, $wrongContextHook, [Text.UTF8Encoding]::new($false))
  $wrongContextHashes = Get-SeamTargetHashes -Stage $wrongContextStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $wrongContextStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A production runtime context built from the wrong api receiver was accepted'
  Assert-SeamTargetHashes -Stage $wrongContextStage -Expected $wrongContextHashes `
    -Message 'Wrong runtime context rejection partially modified the stage'

  $aliasedContextStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route qua alias')
  $aliasedContextHookPath = Join-Path $aliasedContextStage 'internal\daemon\app_zalo_session_hook.go'
  $aliasedContextHook = [IO.File]::ReadAllText($aliasedContextHookPath).
    Replace('productionAppRuntimeContext(a).appZaloRunner', 'aliasedRoute').
    Replace("`treturn a.appAnswerZaloWithRunnerFactory(`n",
      "`taliasedRoute := productionAppRuntimeContext(a).appZaloRunner`n`treturn a.appAnswerZaloWithRunnerFactory(`n")
  [IO.File]::WriteAllText(
    $aliasedContextHookPath, $aliasedContextHook, [Text.UTF8Encoding]::new($false))
  $aliasedContextHashes = Get-SeamTargetHashes -Stage $aliasedContextStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $aliasedContextStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'An aliased production runtime runner was accepted instead of the exact direct argument'
  Assert-SeamTargetHashes -Stage $aliasedContextStage -Expected $aliasedContextHashes `
    -Message 'Aliased runtime runner rejection partially modified the stage'

  $unrelatedProviderStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route ngoài ngữ cảnh')
  $unrelatedProviderHookPath = Join-Path $unrelatedProviderStage 'internal\daemon\app_zalo_session_hook.go'
  $unrelatedProviderHook = [IO.File]::ReadAllText($unrelatedProviderHookPath).
    Replace('productionAppRuntimeContext(a).appZaloRunner', 'fallbackRunner').
    Replace('run := route(deps.cfg, deps.run, threadID, len(files) > 0)',
      'run := fallbackRoute(deps.cfg, deps.run, threadID, len(files) > 0)') + @'

func unrelatedProviderRoute() {
	_ = productionAppRuntimeContext(a).appZaloRunner
	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	_ = run
}
'@
  [IO.File]::WriteAllText(
    $unrelatedProviderHookPath, $unrelatedProviderHook, [Text.UTF8Encoding]::new($false))
  $unrelatedProviderHashes = Get-SeamTargetHashes -Stage $unrelatedProviderStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $unrelatedProviderStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'Unrelated Provider selector and route calls were accepted'
  Assert-SeamTargetHashes -Stage $unrelatedProviderStage -Expected $unrelatedProviderHashes `
    -Message 'Unrelated Provider selector and route calls partially modified the stage'

  $earlyProviderRouteStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route trước gate')
  $earlyProviderRouteHookPath = Join-Path $earlyProviderRouteStage 'internal\daemon\app_zalo_session_hook.go'
  $earlyProviderRouteHook = [IO.File]::ReadAllText($earlyProviderRouteHookPath).
    Replace("`trelease, err := appZaloProcessThreadGate.Acquire(`n",
      "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`trelease, err := appZaloProcessThreadGate.Acquire(`n").
    Replace("`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t_ = run",
      "`t_ = run")
  [IO.File]::WriteAllText(
    $earlyProviderRouteHookPath, $earlyProviderRouteHook, [Text.UTF8Encoding]::new($false))
  $earlyProviderRouteHashes = Get-SeamTargetHashes -Stage $earlyProviderRouteStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $earlyProviderRouteStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'Provider route composition before the per-thread gate was accepted'
  Assert-SeamTargetHashes -Stage $earlyProviderRouteStage -Expected $earlyProviderRouteHashes `
    -Message 'An early Provider route call partially modified the stage'

  $overwrittenProviderRouteStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route bị ghi đè')
  $overwrittenProviderRouteHookPath = Join-Path $overwrittenProviderRouteStage 'internal\daemon\app_zalo_session_hook.go'
  $overwrittenProviderRouteHook = [IO.File]::ReadAllText($overwrittenProviderRouteHookPath).Replace(
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t_ = run",
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`trun = deps.run`n`t_ = run")
  [IO.File]::WriteAllText(
    $overwrittenProviderRouteHookPath, $overwrittenProviderRouteHook, [Text.UTF8Encoding]::new($false))
  $overwrittenProviderRouteHashes = Get-SeamTargetHashes -Stage $overwrittenProviderRouteStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $overwrittenProviderRouteStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A route-derived runner overwritten before use was accepted'
  Assert-SeamTargetHashes -Stage $overwrittenProviderRouteStage -Expected $overwrittenProviderRouteHashes `
    -Message 'Overwritten Provider route rejection partially modified the stage'

  $overwrittenRouteFactoryStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route factory bị ghi đè')
  $overwrittenRouteFactoryHookPath = Join-Path $overwrittenRouteFactoryStage 'internal\daemon\app_zalo_session_hook.go'
  $overwrittenRouteFactoryHook = [IO.File]::ReadAllText($overwrittenRouteFactoryHookPath).Replace(
    "`tdefer release()`n`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)",
    "`tdefer release()`n`troute = fallbackRunnerFactory`n`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)")
  [IO.File]::WriteAllText(
    $overwrittenRouteFactoryHookPath, $overwrittenRouteFactoryHook, [Text.UTF8Encoding]::new($false))
  $overwrittenRouteFactoryHashes = Get-SeamTargetHashes -Stage $overwrittenRouteFactoryStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $overwrittenRouteFactoryStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route factory overwritten before capture was accepted'
  Assert-SeamTargetHashes -Stage $overwrittenRouteFactoryStage -Expected $overwrittenRouteFactoryHashes `
    -Message 'Overwritten Provider route factory rejection partially modified the stage'

  $releasedProviderRouteStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route sau khi thả gate')
  $releasedProviderRouteHookPath = Join-Path $releasedProviderRouteStage 'internal\daemon\app_zalo_session_hook.go'
  $releasedProviderRouteHook = [IO.File]::ReadAllText($releasedProviderRouteHookPath).Replace(
    "`tdefer release()`n`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)",
    "`tdefer release()`n`trelease()`n`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)")
  [IO.File]::WriteAllText(
    $releasedProviderRouteHookPath, $releasedProviderRouteHook, [Text.UTF8Encoding]::new($false))
  $releasedProviderRouteHashes = Get-SeamTargetHashes -Stage $releasedProviderRouteStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $releasedProviderRouteStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'Provider route capture after releasing the thread gate was accepted'
  Assert-SeamTargetHashes -Stage $releasedProviderRouteStage -Expected $releasedProviderRouteHashes `
    -Message 'Released Provider route rejection partially modified the stage'

  $aliasedReleaseStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider gate thả qua alias')
  $aliasedReleaseHookPath = Join-Path $aliasedReleaseStage 'internal\daemon\app_zalo_session_hook.go'
  $aliasedReleaseHook = [IO.File]::ReadAllText($aliasedReleaseHookPath).Replace(
    "`tdefer release()",
    "`tunlock := release`n`trelease = func() {}`n`tdefer release()`n`tunlock()")
  [IO.File]::WriteAllText(
    $aliasedReleaseHookPath, $aliasedReleaseHook, [Text.UTF8Encoding]::new($false))
  $aliasedReleaseHashes = Get-SeamTargetHashes -Stage $aliasedReleaseStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $aliasedReleaseStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A thread-gate release hidden behind an alias was accepted'
  Assert-SeamTargetHashes -Stage $aliasedReleaseStage -Expected $aliasedReleaseHashes `
    -Message 'Aliased thread-gate release rejection partially modified the stage'

  $disabledAttachmentStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider attachment predicate sai')
  $disabledAttachmentHookPath = Join-Path $disabledAttachmentStage 'internal\daemon\app_zalo_session_hook.go'
  $disabledAttachmentHook = [IO.File]::ReadAllText($disabledAttachmentHookPath).Replace(
    'run = aware.appWithZaloAttachments(snapshot.hasAttachments)',
    'run = aware.appWithZaloAttachments(false)')
  [IO.File]::WriteAllText(
    $disabledAttachmentHookPath, $disabledAttachmentHook, [Text.UTF8Encoding]::new($false))
  $disabledAttachmentHashes = Get-SeamTargetHashes -Stage $disabledAttachmentStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $disabledAttachmentStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider runner with a forged attachment predicate was accepted'
  Assert-SeamTargetHashes -Stage $disabledAttachmentStage -Expected $disabledAttachmentHashes `
    -Message 'Forged attachment predicate rejection partially modified the stage'

  $disabledStructuredStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider structured predicate sai')
  $disabledStructuredHookPath = Join-Path $disabledStructuredStage 'internal\daemon\app_zalo_session_hook.go'
  $disabledStructuredHook = [IO.File]::ReadAllText($disabledStructuredHookPath).Replace(
    'appZaloDeltaHasAttachments(snapshot.resumeDelta.delta)', 'false')
  [IO.File]::WriteAllText(
    $disabledStructuredHookPath, $disabledStructuredHook, [Text.UTF8Encoding]::new($false))
  $disabledStructuredHashes = Get-SeamTargetHashes -Stage $disabledStructuredStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $disabledStructuredStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider runner with a forged structured predicate was accepted'
  Assert-SeamTargetHashes -Stage $disabledStructuredStage -Expected $disabledStructuredHashes `
    -Message 'Forged structured predicate rejection partially modified the stage'

  $missingResolveStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider thiếu session resolve')
  $missingResolveHookPath = Join-Path $missingResolveStage 'internal\daemon\app_zalo_session_hook.go'
  $missingResolveHook = [IO.File]::ReadAllText($missingResolveHookPath).Replace(
    "`trun, effectiveConfig, binding, err = a.appResolveZaloSessionRoute(`n" +
      "`t`tctx, run, effectiveConfig, threadID,`n`t)`n", '')
  [IO.File]::WriteAllText(
    $missingResolveHookPath, $missingResolveHook, [Text.UTF8Encoding]::new($false))
  $missingResolveHashes = Get-SeamTargetHashes -Stage $missingResolveStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingResolveStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route without session resolution was accepted'
  Assert-SeamTargetHashes -Stage $missingResolveStage -Expected $missingResolveHashes `
    -Message 'Missing Provider session resolution rejection partially modified the stage'

  $forgedEffectiveConfigStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider effective config giả')
  $forgedEffectiveConfigPath = Join-Path $forgedEffectiveConfigStage 'internal\daemon\app_zalo_session_hook.go'
  $forgedEffectiveConfig = [IO.File]::ReadAllText($forgedEffectiveConfigPath).Replace(
    'effectiveConfig := deps.cfg', 'effectiveConfig := zaloConfig{}')
  [IO.File]::WriteAllText(
    $forgedEffectiveConfigPath, $forgedEffectiveConfig, [Text.UTF8Encoding]::new($false))
  $forgedEffectiveConfigHashes = Get-SeamTargetHashes -Stage $forgedEffectiveConfigStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $forgedEffectiveConfigStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A forged effective Zalo config source was accepted'
  Assert-SeamTargetHashes -Stage $forgedEffectiveConfigStage -Expected $forgedEffectiveConfigHashes `
    -Message 'Forged effective Zalo config rejection partially modified the stage'

  $forgedSnapshotStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider turn snapshot giả')
  $forgedSnapshotPath = Join-Path $forgedSnapshotStage 'internal\daemon\app_zalo_session_hook.go'
  $forgedSnapshot = [IO.File]::ReadAllText($forgedSnapshotPath).Replace(
    'snapshot := a.appCaptureZaloTurnSnapshot(effectiveConfig, threadID, files)',
    'snapshot := appZaloTurnSnapshot{}')
  [IO.File]::WriteAllText(
    $forgedSnapshotPath, $forgedSnapshot, [Text.UTF8Encoding]::new($false))
  $forgedSnapshotHashes = Get-SeamTargetHashes -Stage $forgedSnapshotStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $forgedSnapshotStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A forged Zalo turn snapshot source was accepted'
  Assert-SeamTargetHashes -Stage $forgedSnapshotStage -Expected $forgedSnapshotHashes `
    -Message 'Forged Zalo turn snapshot rejection partially modified the stage'

  $escapedSnapshotFieldStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider snapshot field thoát qua helper')
  $escapedSnapshotFieldPath = Join-Path $escapedSnapshotFieldStage 'internal\daemon\app_zalo_session_hook.go'
  $escapedSnapshotField = [IO.File]::ReadAllText($escapedSnapshotFieldPath).Replace(
    'snapshot := a.appCaptureZaloTurnSnapshot(effectiveConfig, threadID, files)',
    "snapshot := a.appCaptureZaloTurnSnapshot(effectiveConfig, threadID, files)`n`tmutateBool(&snapshot.hasAttachments)")
  [IO.File]::WriteAllText(
    $escapedSnapshotFieldPath, $escapedSnapshotField, [Text.UTF8Encoding]::new($false))
  $escapedSnapshotFieldHashes = Get-SeamTargetHashes -Stage $escapedSnapshotFieldStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $escapedSnapshotFieldStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A protected Zalo snapshot field address passed to a helper was accepted'
  Assert-SeamTargetHashes -Stage $escapedSnapshotFieldStage -Expected $escapedSnapshotFieldHashes `
    -Message 'Escaped Zalo snapshot field rejection partially modified the stage'

  $missingAttachmentAwareStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider thiếu attachment transform')
  $missingAttachmentAwareHookPath = Join-Path $missingAttachmentAwareStage 'internal\daemon\app_zalo_session_hook.go'
  $missingAttachmentAwareHook = [IO.File]::ReadAllText($missingAttachmentAwareHookPath).Replace(
    "`tif aware, ok := run.(appZaloAttachmentAwareRunner); ok {`n" +
      "`t`trun = aware.appWithZaloAttachments(snapshot.hasAttachments)`n`t}`n", '')
  [IO.File]::WriteAllText(
    $missingAttachmentAwareHookPath, $missingAttachmentAwareHook, [Text.UTF8Encoding]::new($false))
  $missingAttachmentAwareHashes = Get-SeamTargetHashes -Stage $missingAttachmentAwareStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingAttachmentAwareStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route without the attachment transform was accepted'
  Assert-SeamTargetHashes -Stage $missingAttachmentAwareStage -Expected $missingAttachmentAwareHashes `
    -Message 'Missing attachment transform rejection partially modified the stage'

  $missingStructuredAwareStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider thiếu structured transform')
  $missingStructuredAwareHookPath = Join-Path $missingStructuredAwareStage 'internal\daemon\app_zalo_session_hook.go'
  $missingStructuredAwareHook = [IO.File]::ReadAllText($missingStructuredAwareHookPath).Replace(
    "`tif aware, ok := run.(appZaloStructuredClaudeAwareRunner); ok {`n" +
      "`t`trun = aware.appWithZaloStructuredClaudeRequirement(`n" +
      "`t`t`tappZaloDeltaHasAttachments(snapshot.resumeDelta.delta),`n`t`t)`n`t}`n", '')
  [IO.File]::WriteAllText(
    $missingStructuredAwareHookPath, $missingStructuredAwareHook, [Text.UTF8Encoding]::new($false))
  $missingStructuredAwareHashes = Get-SeamTargetHashes -Stage $missingStructuredAwareStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingStructuredAwareStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route without the structured attachment transform was accepted'
  Assert-SeamTargetHashes -Stage $missingStructuredAwareStage -Expected $missingStructuredAwareHashes `
    -Message 'Missing structured transform rejection partially modified the stage'

  $missingWrapperStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider thiếu answer wrapper')
  $missingWrapperHookPath = Join-Path $missingWrapperStage 'internal\daemon\app_zalo_session_hook.go'
  $missingWrapperHook = [IO.File]::ReadAllText($missingWrapperHookPath).Replace(
    'if structured, ok := run.(appZaloStructuredRunner); ok {', 'if false {')
  [IO.File]::WriteAllText(
    $missingWrapperHookPath, $missingWrapperHook, [Text.UTF8Encoding]::new($false))
  $missingWrapperHashes = Get-SeamTargetHashes -Stage $missingWrapperStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingWrapperStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route without the final answer wrapper was accepted'
  Assert-SeamTargetHashes -Stage $missingWrapperStage -Expected $missingWrapperHashes `
    -Message 'Missing Provider answer wrapper rejection partially modified the stage'

  $wrongAcquireStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider gate key sai')
  $wrongAcquireHookPath = Join-Path $wrongAcquireStage 'internal\daemon\app_zalo_session_hook.go'
  $wrongAcquireHook = [IO.File]::ReadAllText($wrongAcquireHookPath).Replace(
    'fmt.Sprintf("%p\x00%s", a.st, threadID)', 'fmt.Sprintf("%p\x00%s", otherStore, otherThreadID)')
  [IO.File]::WriteAllText(
    $wrongAcquireHookPath, $wrongAcquireHook, [Text.UTF8Encoding]::new($false))
  $wrongAcquireHashes = Get-SeamTargetHashes -Stage $wrongAcquireStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $wrongAcquireStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route guarded by the wrong thread key was accepted'
  Assert-SeamTargetHashes -Stage $wrongAcquireStage -Expected $wrongAcquireHashes `
    -Message 'Wrong Provider thread key rejection partially modified the stage'

  $wrongHookArgumentsStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider hook args sai')
  $wrongHookArgumentsPath = Join-Path $wrongHookArgumentsStage 'internal\daemon\app_zalo_session_hook.go'
  $wrongHookArguments = [IO.File]::ReadAllText($wrongHookArgumentsPath).Replace(
    'deps, threadID, question, reply, files, productionAppRuntimeContext(a).appZaloRunner,',
    'deps, otherThreadID, forgedQuestion, reply, files, productionAppRuntimeContext(a).appZaloRunner,')
  [IO.File]::WriteAllText(
    $wrongHookArgumentsPath, $wrongHookArguments, [Text.UTF8Encoding]::new($false))
  $wrongHookArgumentsHashes = Get-SeamTargetHashes -Stage $wrongHookArgumentsStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $wrongHookArgumentsStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A production Provider hook with forged arguments was accepted'
  Assert-SeamTargetHashes -Stage $wrongHookArgumentsStage -Expected $wrongHookArgumentsHashes `
    -Message 'Forged Provider hook arguments rejection partially modified the stage'

  $wrongAnswerArgumentsStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider answer args sai')
  $wrongAnswerArgumentsPath = Join-Path $wrongAnswerArgumentsStage 'internal\daemon\app_zalo_session_hook.go'
  $wrongAnswerArguments = [IO.File]::ReadAllText($wrongAnswerArgumentsPath).Replace(
    'ctx, deps.cfg, run, threadID, question, step, reply, files...',
    'ctx, deps.cfg, run, otherThreadID, forgedQuestion, step, reply, files...')
  [IO.File]::WriteAllText(
    $wrongAnswerArgumentsPath, $wrongAnswerArguments, [Text.UTF8Encoding]::new($false))
  $wrongAnswerArgumentsHashes = Get-SeamTargetHashes -Stage $wrongAnswerArgumentsStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $wrongAnswerArgumentsStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A final answer sink with forged arguments was accepted'
  Assert-SeamTargetHashes -Stage $wrongAnswerArgumentsStage -Expected $wrongAnswerArgumentsHashes `
    -Message 'Forged final answer arguments rejection partially modified the stage'

  $wrongRouteArgumentsStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route args sai')
  $wrongRouteArgumentsHookPath = Join-Path $wrongRouteArgumentsStage 'internal\daemon\app_zalo_session_hook.go'
  $wrongRouteArgumentsHook = [IO.File]::ReadAllText($wrongRouteArgumentsHookPath).Replace(
    'route(deps.cfg, deps.run, threadID, len(files) > 0)',
    'route(deps.cfg, fallbackRun, otherThreadID, false)')
  [IO.File]::WriteAllText(
    $wrongRouteArgumentsHookPath, $wrongRouteArgumentsHook, [Text.UTF8Encoding]::new($false))
  $wrongRouteArgumentsHashes = Get-SeamTargetHashes -Stage $wrongRouteArgumentsStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $wrongRouteArgumentsStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A Provider route capture with forged inputs was accepted'
  Assert-SeamTargetHashes -Stage $wrongRouteArgumentsStage -Expected $wrongRouteArgumentsHashes `
    -Message 'Forged Provider route inputs rejection partially modified the stage'

  $addressedRunStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider run ghi qua con trỏ')
  $addressedRunHookPath = Join-Path $addressedRunStage 'internal\daemon\app_zalo_session_hook.go'
  $addressedRunHook = [IO.File]::ReadAllText($addressedRunHookPath).Replace(
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t_ = run",
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t*(&run) = deps.run`n`t_ = run")
  [IO.File]::WriteAllText(
    $addressedRunHookPath, $addressedRunHook, [Text.UTF8Encoding]::new($false))
  $addressedRunHashes = Get-SeamTargetHashes -Stage $addressedRunStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $addressedRunStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A route-derived runner overwritten through its address was accepted'
  Assert-SeamTargetHashes -Stage $addressedRunStage -Expected $addressedRunHashes `
    -Message 'Addressed Provider runner rejection partially modified the stage'

  $escapedRunStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider run thoát qua helper')
  $escapedRunHookPath = Join-Path $escapedRunStage 'internal\daemon\app_zalo_session_hook.go'
  $escapedRunHook = [IO.File]::ReadAllText($escapedRunHookPath).Replace(
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t_ = run",
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`treplaceRunner(&run)`n`t_ = run")
  [IO.File]::WriteAllText(
    $escapedRunHookPath, $escapedRunHook, [Text.UTF8Encoding]::new($false))
  $escapedRunHashes = Get-SeamTargetHashes -Stage $escapedRunStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $escapedRunStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A route-derived runner address passed to a helper was accepted'
  Assert-SeamTargetHashes -Stage $escapedRunStage -Expected $escapedRunHashes `
    -Message 'Escaped Provider runner rejection partially modified the stage'

  $deadProviderRouteStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route trong nhánh chết')
  $deadProviderRouteHookPath = Join-Path $deadProviderRouteStage 'internal\daemon\app_zalo_session_hook.go'
  $deadProviderRouteHook = [IO.File]::ReadAllText($deadProviderRouteHookPath).Replace(
    "`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t_ = run",
    "`tif false {`n`t`trun := route(deps.cfg, deps.run, threadID, len(files) > 0)`n`t`t_ = run`n`t}`n`trun := deps.run`n`t_ = run")
  [IO.File]::WriteAllText(
    $deadProviderRouteHookPath, $deadProviderRouteHook, [Text.UTF8Encoding]::new($false))
  $deadProviderRouteHashes = Get-SeamTargetHashes -Stage $deadProviderRouteStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $deadProviderRouteStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A route capture reachable only through a dead branch was accepted'
  Assert-SeamTargetHashes -Stage $deadProviderRouteStage -Expected $deadProviderRouteHashes `
    -Message 'Dead-branch Provider route rejection partially modified the stage'

  $deadProviderHookStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider hook trong nhánh chết')
  $deadProviderHookPath = Join-Path $deadProviderHookStage 'internal\daemon\app_zalo_session_hook.go'
  $deadProviderHook = [IO.File]::ReadAllText($deadProviderHookPath).Replace(
    "`treturn a.appAnswerZaloWithRunnerFactory(`n" +
      "`t`tdeps, threadID, question, reply, files, productionAppRuntimeContext(a).appZaloRunner,`n`t)",
    "`tif false {`n`t`treturn a.appAnswerZaloWithRunnerFactory(`n" +
      "`t`t`tdeps, threadID, question, reply, files, productionAppRuntimeContext(a).appZaloRunner,`n`t`t)`n`t}`n`treturn fallbackAnswer()")
  [IO.File]::WriteAllText(
    $deadProviderHookPath, $deadProviderHook, [Text.UTF8Encoding]::new($false))
  $deadProviderHookHashes = Get-SeamTargetHashes -Stage $deadProviderHookStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $deadProviderHookStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A production Provider hook reachable only through a dead branch was accepted'
  Assert-SeamTargetHashes -Stage $deadProviderHookStage -Expected $deadProviderHookHashes `
    -Message 'Dead Provider hook rejection partially modified the stage'

  $wrongProviderSinkStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route không đi vào answer')
  $wrongProviderSinkHookPath = Join-Path $wrongProviderSinkStage 'internal\daemon\app_zalo_session_hook.go'
  $wrongProviderSinkHook = [IO.File]::ReadAllText($wrongProviderSinkHookPath).Replace(
    'ctx, deps.cfg, run, threadID, question, step, reply, files...',
    'ctx, deps.cfg, deps.run, threadID, question, step, reply, files...')
  [IO.File]::WriteAllText(
    $wrongProviderSinkHookPath, $wrongProviderSinkHook, [Text.UTF8Encoding]::new($false))
  $wrongProviderSinkHashes = Get-SeamTargetHashes -Stage $wrongProviderSinkStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $wrongProviderSinkStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'An answer path bypassing the route-derived runner was accepted'
  Assert-SeamTargetHashes -Stage $wrongProviderSinkStage -Expected $wrongProviderSinkHashes `
    -Message 'Bypassed Provider route sink rejection partially modified the stage'

  $extraProviderRouteStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route dư ngoài function')
  $extraProviderRouteHookPath = Join-Path $extraProviderRouteStage 'internal\daemon\app_zalo_session_hook.go'
  $extraProviderRouteHook = [IO.File]::ReadAllText($extraProviderRouteHookPath) + @'

func extraProviderRoute() {
	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	_ = run
}
'@
  [IO.File]::WriteAllText(
    $extraProviderRouteHookPath, $extraProviderRouteHook, [Text.UTF8Encoding]::new($false))
  $extraProviderRouteHashes = Get-SeamTargetHashes -Stage $extraProviderRouteStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $extraProviderRouteStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'An extra Provider route call outside the guarded factory was accepted'
  Assert-SeamTargetHashes -Stage $extraProviderRouteStage -Expected $extraProviderRouteHashes `
    -Message 'An extra Provider route call outside the guarded factory partially modified the stage'

  $crossFileProviderStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Provider route dư ở production file khác')
  $crossFileProviderPath = Join-Path $crossFileProviderStage 'internal\daemon\extra_provider_route.go'
  Write-TestFile $crossFileProviderPath @'
package daemon

func extraProviderRouteInAnotherProductionFile() {
	_ = productionAppRuntimeContext(a).appZaloRunner
	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	_ = run
}
'@
  $crossFileProviderHashes = Get-SeamTargetHashes -Stage $crossFileProviderStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $crossFileProviderStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'Duplicate Provider selector and route in another production Go file were accepted'
  Assert-SeamTargetHashes -Stage $crossFileProviderStage -Expected $crossFileProviderHashes `
    -Message 'Cross-file Provider topology rejection partially modified existing seam targets'

  $crossFileAnswerStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Answer call dư ở production file khác')
  $crossFileAnswerPath = Join-Path $crossFileAnswerStage 'internal\daemon\extra_answer_call.go'
  Write-TestFile $crossFileAnswerPath @'
package daemon

// a.appAnswerZalo in a comment is intentionally irrelevant; the executable call below is not.
func extraAnswerCallInAnotherProductionFile() {
	_ = a.appAnswerZalo(deps, threadID, question, reply, files)
}
'@
  $crossFileAnswerHashes = Get-SeamTargetHashes -Stage $crossFileAnswerStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $crossFileAnswerStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'An existing appAnswerZalo call in another production Go file was accepted pre-seam'
  Assert-SeamTargetHashes -Stage $crossFileAnswerStage -Expected $crossFileAnswerHashes `
    -Message 'Cross-file answer-call rejection partially modified existing seam targets'

  $parenthesizedAnswerStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Answer call bọc ngoặc')
  $parenthesizedAnswerPath = Join-Path $parenthesizedAnswerStage 'internal\daemon\parenthesized_answer_call.go'
  Write-TestFile $parenthesizedAnswerPath @'
package daemon

func extraParenthesizedAnswerCall() {
	_ = (a.appAnswerZalo)(deps, threadID, question, reply, files)
}
'@
  $parenthesizedAnswerHashes = Get-SeamTargetHashes -Stage $parenthesizedAnswerStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $parenthesizedAnswerStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A parenthesized appAnswerZalo invocation in production was accepted'
  Assert-SeamTargetHashes -Stage $parenthesizedAnswerStage -Expected $parenthesizedAnswerHashes `
    -Message 'Parenthesized answer-call rejection partially modified existing seam targets'

  $capturedAnswerStage = New-AppStage -Repo $repo -Overlay $overlay `
    -StageRoot (Join-Path $stageTestRoot 'Answer method value capture')
  $capturedAnswerPath = Join-Path $capturedAnswerStage 'internal\daemon\captured_answer_call.go'
  Write-TestFile $capturedAnswerPath @'
package daemon

func extraCapturedAnswerCall() {
	answer := a.appAnswerZalo
	_ = answer(deps, threadID, question, reply, files)
}
'@
  $capturedAnswerHashes = Get-SeamTargetHashes -Stage $capturedAnswerStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $capturedAnswerStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A captured appAnswerZalo method value in production was accepted'
  Assert-SeamTargetHashes -Stage $capturedAnswerStage -Expected $capturedAnswerHashes `
    -Message 'Captured answer method-value rejection partially modified existing seam targets'

  $duplicateProviderRouteStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Trùng Provider route')
  $duplicateProviderHookPath = Join-Path $duplicateProviderRouteStage 'internal\daemon\app_zalo_session_hook.go'
  $duplicateProviderHook = [IO.File]::ReadAllText($duplicateProviderHookPath) + "`nvar duplicate = productionAppRuntimeContext(a).appZaloRunner`n"
  [IO.File]::WriteAllText($duplicateProviderHookPath, $duplicateProviderHook, [Text.UTF8Encoding]::new($false))
  $duplicateProviderHashes = Get-SeamTargetHashes -Stage $duplicateProviderRouteStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $duplicateProviderRouteStage } `
    -Pattern 'provider route topology verification failed' `
    -Message 'A duplicate Provider route reference was accepted'
  Assert-SeamTargetHashes -Stage $duplicateProviderRouteStage -Expected $duplicateProviderHashes `
    -Message 'A duplicate Provider route reference partially modified the stage'

  $missingLessonStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu operator lesson')
  $missingLessonPath = Join-Path $missingLessonStage 'internal\daemon\zalolesson.go'
  $missingLesson = [IO.File]::ReadAllText($missingLessonPath).Replace(
    'a.st.AddZaloMemory(ipc.ZaloMemory{ThreadID: threadID, Text: text})',
    'a.st.LegacyLessonPathRemoved()')
  [IO.File]::WriteAllText($missingLessonPath, $missingLesson, [Text.UTF8Encoding]::new($false))
  $missingLessonHashes = Get-SeamTargetHashes -Stage $missingLessonStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingLessonStage } `
    -Pattern 'operator lesson seam: expected exactly 1 match' -Message 'A missing operator lesson marker was accepted'
  Assert-SeamTargetHashes -Stage $missingLessonStage -Expected $missingLessonHashes `
    -Message 'A missing operator lesson marker partially modified the stage'

  $missingZaloUIStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Thiếu Zalo UI seam')
  $missingZaloUIPath = Join-Path $missingZaloUIStage 'internal\webui\static\zalo.js'
  $missingZaloUI = [IO.File]::ReadAllText($missingZaloUIPath).Replace('let curThread = null;', 'let selectedThread = null;')
  [IO.File]::WriteAllText($missingZaloUIPath, $missingZaloUI, [Text.UTF8Encoding]::new($false))
  $missingZaloUIHashes = Get-SeamTargetHashes -Stage $missingZaloUIStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $missingZaloUIStage } `
    -Pattern 'Zalo deep-link declaration seam: expected exactly 1 match' -Message 'A missing Zalo UI deep-link marker was accepted'
  Assert-SeamTargetHashes -Stage $missingZaloUIStage -Expected $missingZaloUIHashes `
    -Message 'A missing Zalo UI deep-link marker partially modified the stage'

  $duplicateZaloUIStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Trùng Zalo UI seam')
  $duplicateZaloUIPath = Join-Path $duplicateZaloUIStage 'internal\webui\static\zalo.js'
  $duplicateZaloUI = [IO.File]::ReadAllText($duplicateZaloUIPath) + "`n    threads = ths || [];`n"
  [IO.File]::WriteAllText($duplicateZaloUIPath, $duplicateZaloUI, [Text.UTF8Encoding]::new($false))
  $duplicateZaloUIHashes = Get-SeamTargetHashes -Stage $duplicateZaloUIStage
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $duplicateZaloUIStage } `
    -Pattern 'Zalo deep-link selection seam: expected exactly 1 match' -Message 'A duplicate Zalo UI deep-link marker was accepted'
  Assert-SeamTargetHashes -Stage $duplicateZaloUIStage -Expected $duplicateZaloUIHashes `
    -Message 'A duplicate Zalo UI deep-link marker partially modified the stage'

  foreach ($inserted in @(
      @{ Signature = "`t`terr := a.appAnswerZalo(deps, threadID, question, reply, files)"; Pattern = 'session answer seam: inserted signature already present'; Name = 'answer' },
      @{ Signature = "`traw, err := a.appRunZalo(ctx, run, pz, threadID, question, appZaloCurrentMsgID(reply.ReplyQuote), history, found, files, step)"; Pattern = 'session runner seam: inserted signature already present'; Name = 'runner' }
    )) {
    $insertedStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot ("Đã chèn " + $inserted.Name))
    $insertedDutyPath = Join-Path $insertedStage 'internal\daemon\duty.go'
    $insertedDuty = [IO.File]::ReadAllText($insertedDutyPath) + "`n" + $inserted.Signature + "`n"
    [IO.File]::WriteAllText($insertedDutyPath, $insertedDuty, [Text.UTF8Encoding]::new($false))
    Assert-ThrowsLike -Action { Apply-AppSeams -Stage $insertedStage } `
      -Pattern $inserted.Pattern -Message ("An already-inserted session " + $inserted.Name + " seam was accepted")
  }

  $insertedLessonStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Đã chèn operator lesson')
  $insertedLessonPath = Join-Path $insertedLessonStage 'internal\daemon\zalolesson.go'
  $insertedLesson = [IO.File]::ReadAllText($insertedLessonPath) +
    "`na.st.CreateAppLesson(lesson)`n"
  [IO.File]::WriteAllText($insertedLessonPath, $insertedLesson, [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $insertedLessonStage } `
    -Pattern 'operator lesson seam: inserted signature already present' `
    -Message 'An already-inserted operator lesson seam was accepted'

  $duplicateWorkflowStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Trùng workflow')
  $duplicateZaloPath = Join-Path $duplicateWorkflowStage 'internal\daemon\zalo.go'
  $duplicateZalo = [IO.File]::ReadAllText($duplicateZaloPath)
  $duplicateZalo += @'

func duplicateWorkflowMarker() {
	if true {
		a.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{})
	}
}
'@
  [IO.File]::WriteAllText($duplicateZaloPath, $duplicateZalo, [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $duplicateWorkflowStage } `
    -Pattern 'workflow seam: expected exactly 1 match' -Message 'A duplicate workflow marker was accepted'

  $orchestrator = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '..\build-app.ps1'))
  if ($orchestrator -notmatch 'New-AppStage' -or $orchestrator -notmatch 'Apply-AppSeams') {
    throw 'Build orchestration does not use the guarded staging functions'
  }
  if ($orchestrator -match 'mux\.Handle\(`"GET /kb') {
    throw 'Legacy direct route substitution remains in build orchestration'
  }

  $launcher = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '..\launcher\app\run.bat'))
  if ($launcher -notmatch 'http://127\.0\.0\.1:8770/"' -or $launcher -match '8770/zalo') {
    throw 'Launcher does not open the management Portal at /'
  }

  Write-Host 'PASS: staging copies tracked files and applies the guarded Memory, Provider, and managed-CLI seams.'
} finally {
  if (Test-Path -LiteralPath $stageTestRoot) {
    Remove-Item -LiteralPath $stageTestRoot -Recurse -Force
  }
}

$crlfStage = Join-Path ([IO.Path]::GetTempPath()) ('portal-crlf-stage-' + [guid]::NewGuid().ToString('N'))

try {
  Write-TestFile (Join-Path $crlfStage 'internal\daemon\server.go') @'
package daemon

func server() {
	mux.Handle("POST /shutdown", a.auth(a.handleShutdown))
}
'@
  Write-TestFile (Join-Path $crlfStage 'internal\store\store.go') @'
func migrate(db *sql.DB) error {
	return nil
}

// hasColumn asks SQLite rather than tracking a version number.
'@
  Write-TestFile (Join-Path $crlfStage 'internal\daemon\zalo.go') @'
package daemon

func incoming() {
		a.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{})
}
'@
  Write-TestFile (Join-Path $crlfStage 'internal\daemon\app_zalo_session_hook.go') @'
package daemon

func (a *api) appAnswerZalo(
	deps *zaloDeps,
	threadID, question string,
	reply ipc.ZaloOutboxDraft,
	files []ipc.ZaloAttachment,
) error {
	return a.appAnswerZaloWithRunnerFactory(
		deps, threadID, question, reply, files, productionAppRuntimeContext(a).appZaloRunner,
	)
}

func (a *api) appAnswerZaloWithRunnerFactory(
	deps *zaloDeps,
	threadID, question string,
	reply ipc.ZaloOutboxDraft,
	files []ipc.ZaloAttachment,
	route appZaloRunnerFactory,
) error {
	if route == nil {
		return errors.New("zalo session runner factory is required")
	}
	release, err := appZaloProcessThreadGate.Acquire(
		context.Background(),
		fmt.Sprintf("%p\x00%s", a.st, threadID),
	)
	if err != nil {
		return err
	}
	defer release()
	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	_ = run
	effectiveConfig := deps.cfg
	var binding appZaloClaudeBinding
	run, effectiveConfig, binding, err = a.appResolveZaloSessionRoute(
		ctx, run, effectiveConfig, threadID,
	)
	if err != nil {
		return err
	}
	snapshot := a.appCaptureZaloTurnSnapshot(effectiveConfig, threadID, files)
	if aware, ok := run.(appZaloAttachmentAwareRunner); ok {
		run = aware.appWithZaloAttachments(snapshot.hasAttachments)
	}
	if aware, ok := run.(appZaloStructuredClaudeAwareRunner); ok {
		run = aware.appWithZaloStructuredClaudeRequirement(
			appZaloDeltaHasAttachments(snapshot.resumeDelta.delta),
		)
	}
	if structured, ok := run.(appZaloStructuredRunner); ok {
		run = &appZaloStructuredAnswerRunner{
			a:                a,
			run:              structured,
			zc:               effectiveConfig,
			threadID:         threadID,
			question:         question,
			currentZaloMsgID: appZaloCurrentMsgID(reply.ReplyQuote),
			history:          snapshot.history,
			directFiles:      snapshot.directFiles,
			files:            snapshot.files,
			highWater:        snapshot.highWater,
			highWaterKnown:   snapshot.highWaterKnown,
			resumeDelta:      snapshot.resumeDelta,
			claudeBinding:    binding,
			agentDisplayName: snapshot.agentDisplayName,
		}
	} else {
		run = &appZaloCapturedIdentityRunner{
			zaloRunner: run, agentDisplayName: snapshot.agentDisplayName,
		}
	}
	if err := a.answerZalo(
		ctx, deps.cfg, run, threadID, question, step, reply, files...,
	); err != nil {
		return err
	}
	return nil
}
'@

  $vietnameseComment = '// Giữ nguyên tiếng Việt sau khi chèn đường nối phiên Zalo.'
  $dutyCRLF = @(
    'package daemon'
    ''
    'type zaloConfig struct {'
    "`tModel string"
    '}'
    ''
    'const maxZaloAnswers = 6'
    ''
    $vietnameseComment
    'func answer() {'
    "`traw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)"
    "`tif err != nil {"
    "`t`treturn escalate(`"the agent did not finish`", `"err`", err)"
    "`t}"
    '}'
    ''
    'func trigger() {'
    "`tgo func() {"
    "`t`tctx, cancel := context.WithTimeout(context.Background(), deps.cfg.Timeout)"
    "`t`tdefer cancel()"
    "`t`t// Bước của lượt đi vào log dùng chung, không phải một danh sách riêng của lượt:"
    "`t`t// terminal là một dòng thời gian, và một lượt đã xong vẫn nằm đó để đọc."
    "`t`tstep := func(text string) { a.zlog.add(ipc.ZaloLogStep, threadID, text) }"
    "`t`terr := a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)"
    "`t}()"
    '}'
    ''
    'func (e execZaloRunner) Run(ctx context.Context, prompt string, step func(string)) (string, error) {'
    "`tprof := agent.ConsultReadOnly()"
    "`tbin, err := exec.LookPath(prof.Binary)"
    "`tif err != nil {"
    "`t`treturn `"`", fmt.Errorf(`"locate %s: %w`", prof.Binary, err)"
    "`t}"
    "`targs, err := consultArgv(e.cfg, uuid.NewString())"
    "`tif err != nil {"
    "`t`treturn `"`", err"
    "`t}"
    "`tcmd := exec.CommandContext(ctx, bin, args...)"
    "`tcmd.Env = prof.Env(os.Environ())"
    "`tif e.cfg.ThinkingTokens > 0 {"
    "`t}"
    "`treturn `"`", nil"
    '}'
    ''
  ) -join "`r`n"
  $crlfDutyPath = Join-Path $crlfStage 'internal\daemon\duty.go'
  [IO.Directory]::CreateDirectory((Split-Path -Parent $crlfDutyPath)) | Out-Null
  [IO.File]::WriteAllText($crlfDutyPath, $dutyCRLF, [Text.UTF8Encoding]::new($false))

  $lessonCRLF = @(
    'package daemon'
    ''
    'import ('
    "`t`"strings`""
    "`t`"agentdc/internal/ipc`""
    ')'
    ''
    '// Ghi vào zalo_memory như mọi ghi chú khác, nên nó cũng KHÔNG trích dẫn được — xem chú thích bảng'
    '// đó. Một bài học là chữ do hệ này tự sinh, tức đúng thứ không được thành nguồn.'
    'func (a *api) noteOperatorRewrite(threadID, operatorText string) {'
    "`ttext := `"người trực sửa lại: bot nói \`"`" + clip(strings.TrimSpace(bot.Body), maxLessonPart) +"
    "`t`t`"\`" → người trực gửi \`"`" + clip(strings.TrimSpace(operatorText), maxLessonPart) + `"\`"`""
    "`tif err := a.st.AddZaloMemory(ipc.ZaloMemory{ThreadID: threadID, Text: text}); err != nil {"
    "`t`treturn"
    "`t}"
    '}'
    ''
  ) -join "`r`n"
  $crlfLessonPath = Join-Path $crlfStage 'internal\daemon\zalolesson.go'
  [IO.Directory]::CreateDirectory((Split-Path -Parent $crlfLessonPath)) | Out-Null
  [IO.File]::WriteAllText($crlfLessonPath, $lessonCRLF, [Text.UTF8Encoding]::new($false))

  $zaloUICRLF = @(
    "const el = (id) => document.getElementById(id);"
    'let curThread = null;'
    'let threads = [];'
    "let listSig = '';"
    "let feedSig = '';"
    "let outboxSig = '';"
    'let atBottom = true;'
    'async function refreshMemCount() {}'
    'async function refresh() {'
    '    threads = ths || [];'
    '    renderList();'
    '}'
    ''
  ) -join "`r`n"
  $crlfZaloUIPath = Join-Path $crlfStage 'internal\webui\static\zalo.js'
  [IO.Directory]::CreateDirectory((Split-Path -Parent $crlfZaloUIPath)) | Out-Null
  [IO.File]::WriteAllText($crlfZaloUIPath, $zaloUICRLF, [Text.UTF8Encoding]::new($false))

  Apply-AppSeams -Stage $crlfStage

  Assert-CRLFUTF8File -Path $crlfDutyPath -ExpectedText $vietnameseComment
  Assert-CRLFUTF8File -Path $crlfLessonPath -ExpectedText 'CreateAppLesson'
  $stagedCRLFDuty = [Text.UTF8Encoding]::new($false, $true).GetString([IO.File]::ReadAllBytes($crlfDutyPath))
  if ([regex]::Matches($stagedCRLFDuty, 'a\.appAnswerZalo\(deps, threadID, question, reply, files\)').Count -ne 1) {
    throw 'CRLF fixture is missing the session answer seam'
  }
  if ([regex]::Matches($stagedCRLFDuty, 'a\.appRunZalo\(ctx, run, pz, threadID, question, appZaloCurrentMsgID\(reply\.ReplyQuote\), history, found, files, step\)').Count -ne 1) {
    throw 'CRLF fixture is missing the session runner seam'
  }
  Assert-CRLFUTF8File -Path $crlfZaloUIPath `
    -ExpectedText "const requestedThreadID = new URLSearchParams(window.location.search).get('thread');"

  $mutatedDutyPath = Join-Path $crlfStage 'internal\daemon\duty-mutated.go'
  [IO.File]::WriteAllText($mutatedDutyPath, $stagedCRLFDuty.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Assert-CRLFUTF8File -Path $mutatedDutyPath -ExpectedText $vietnameseComment } `
    -Pattern 'CRLF line endings were removed|bare LF' -Message 'The CRLF regression assertion accepted an LF mutation'

  Write-Host 'PASS: duty seam preserves CRLF and BOM-free UTF-8 Vietnamese text.'
} finally {
  if (Test-Path -LiteralPath $crlfStage) {
    Remove-Item -LiteralPath $crlfStage -Recurse -Force
  }
}

$realOverlay = Join-Path $PSScriptRoot '..\appmode\overlay'
Invoke-StagedZaloSessionAcceptance -Repo $resolvedUpstreamRepo -Overlay $realOverlay
Assert-MemoryV2DeploymentTooling
Write-Host 'PASS: real staged duty and executable Memory V2 deployment gates parse and preserve ordered safety seams.'
