$ErrorActionPreference = 'Stop'

Import-Module (Join-Path $PSScriptRoot '..\scripts\BuildApp.psm1') -Force

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
  [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($false))
}

function Get-SeamTargetHashes {
  param([Parameter(Mandatory)][string]$Stage)

  $hashes = @{}
  foreach ($relative in @(
      'internal\daemon\server.go',
      'internal\store\store.go',
      'internal\daemon\zalo.go',
      'internal\daemon\duty.go'
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

  Write-Host 'PASS: Resolve-BuildPaths normalizes and protects build paths.'
} finally {
  if (Test-Path -LiteralPath $root) {
    Remove-Item -LiteralPath $root -Recurse -Force
  }
}

$clearRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-clear-' + [guid]::NewGuid().ToString('N'))

try {
  $outWithWildcard = Join-Path $clearRoot 'Gói [ab]'
  $matchingSibling = Join-Path $clearRoot 'Gói a'
  Write-TestFile (Join-Path $outWithWildcard 'remove.txt') "remove`n"
  Write-TestFile (Join-Path $matchingSibling 'keep.txt') "keep`n"

  Clear-AppOutput -Out $outWithWildcard
  if (Test-Path -LiteralPath (Join-Path $outWithWildcard 'remove.txt')) {
    throw 'Exact output contents were not removed'
  }
  if (-not (Test-Path -LiteralPath (Join-Path $matchingSibling 'keep.txt'))) {
    throw 'Wildcard output path removed a matching sibling directory'
  }

  Write-TestFile (Join-Path $outWithWildcard 'data\keep.txt') "keep data`n"
  Write-TestFile (Join-Path $outWithWildcard 'app\remove.txt') "remove app`n"
  Clear-AppOutput -Out $outWithWildcard -KeepData
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
  Write-TestFile (Join-Path $cleanRepoRoot 'tracked.txt') "working-tree edit`n"
  Assert-ThrowsLike -Action { Assert-CleanGitSource -Repo $cleanRepoRoot } `
    -Pattern 'không sạch|not clean' -Message 'A dirty source repository was accepted'

  Write-Host 'PASS: Assert-CleanGitSource rejects uncommitted source content.'
} finally {
  if (Test-Path -LiteralPath $cleanRepoRoot) {
    Remove-Item -LiteralPath $cleanRepoRoot -Recurse -Force
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
Write-Host 'PASS: Go checkpoint skips exactly seven superseded tests.'

$packageRoot = Join-Path ([IO.Path]::GetTempPath()) ('portal-package-' + [guid]::NewGuid().ToString('N'))

try {
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'package missing' -Message 'An incomplete package was accepted'

  $required = @(
    'app\agentdc.exe', 'app\transport\dist\index.js', 'app\node\node.exe',
    'Start.vbs', 'Stop.bat', 'README.txt', 'brain\wiki\index.md'
  )
  foreach ($relative in $required) {
    Write-TestFile (Join-Path $packageRoot $relative) "fixture`n"
  }
  Assert-AppPackage -Out $packageRoot | Out-Null

  Write-TestFile (Join-Path $packageRoot 'data\zalo\credentials.json') "secret`n"
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'Zalo credentials' -Message 'A package containing Zalo credentials was accepted'

  Write-Host 'PASS: Assert-AppPackage requires runtime files and rejects credentials.'
} finally {
  if (Test-Path -LiteralPath $packageRoot) {
    Remove-Item -LiteralPath $packageRoot -Recurse -Force
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
	default:
		delay := time.Second
		a.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{})
}
'@
  Write-TestFile (Join-Path $repo 'internal\daemon\duty.go') @'
package daemon

func answer() {
	raw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)
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
  $sourceDutyHashBefore = (Get-FileHash -LiteralPath $sourceDutyPath -Algorithm SHA256).Hash

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
  if ([regex]::Matches($stagedServer, 'a\.registerAppRoutes\(mux\)').Count -ne 1) { throw 'Route seam was not applied exactly once' }
  if ([regex]::Matches($stagedStore, 'migrateApp\(db\)').Count -ne 1) { throw 'Migration seam was not applied exactly once' }
  if ([regex]::Matches($stagedZalo, 'evaluateAppWorkflow\(req, msg\)').Count -ne 1) { throw 'Workflow seam was not applied exactly once' }
  if ([regex]::Matches($stagedDuty, 'a\.appAnswerZalo\(deps, threadID, question, reply, files\)').Count -ne 1) {
    throw 'Session answer seam was not applied exactly once'
  }
  if ([regex]::Matches($stagedDuty, 'a\.appRunZalo\(ctx, run, pz, threadID, question, appZaloCurrentMsgID\(reply\.ReplyQuote\), history, found, files, step\)').Count -ne 1) {
    throw 'Session runner seam was not applied exactly once'
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
  $sourceDutyHashAfter = (Get-FileHash -LiteralPath $sourceDutyPath -Algorithm SHA256).Hash
  Assert-Equal $sourceDutyHashAfter $sourceDutyHashBefore 'Source duty.go hash changed during staging'
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
  $duplicateServer += "`n`tmux.Handle(`"POST /shutdown`", a.auth(a.handleShutdown))`n"
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

  $duplicateWorkflowStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Trùng workflow')
  $duplicateZaloPath = Join-Path $duplicateWorkflowStage 'internal\daemon\zalo.go'
  $duplicateZalo = [IO.File]::ReadAllText($duplicateZaloPath)
  $duplicateZalo += "`n`t`ta.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{}`n"
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

  Write-Host 'PASS: staging copies tracked files and applies five guarded seams.'
} finally {
  if (Test-Path -LiteralPath $stageTestRoot) {
    Remove-Item -LiteralPath $stageTestRoot -Recurse -Force
  }
}

$crlfStage = Join-Path ([IO.Path]::GetTempPath()) ('portal-crlf-stage-' + [guid]::NewGuid().ToString('N'))

try {
  Write-TestFile (Join-Path $crlfStage 'internal\daemon\server.go') @'
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
func incoming() {
		a.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{})
}
'@

  $vietnameseComment = '// Giữ nguyên tiếng Việt sau khi chèn đường nối phiên Zalo.'
  $dutyCRLF = @(
    'package daemon'
    ''
    $vietnameseComment
    'func answer() {'
    "`traw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)"
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
  ) -join "`r`n"
  $crlfDutyPath = Join-Path $crlfStage 'internal\daemon\duty.go'
  [IO.Directory]::CreateDirectory((Split-Path -Parent $crlfDutyPath)) | Out-Null
  [IO.File]::WriteAllText($crlfDutyPath, $dutyCRLF, [Text.UTF8Encoding]::new($false))

  Apply-AppSeams -Stage $crlfStage

  Assert-CRLFUTF8File -Path $crlfDutyPath -ExpectedText $vietnameseComment
  $stagedCRLFDuty = [Text.UTF8Encoding]::new($false, $true).GetString([IO.File]::ReadAllBytes($crlfDutyPath))
  if ([regex]::Matches($stagedCRLFDuty, 'a\.appAnswerZalo\(deps, threadID, question, reply, files\)').Count -ne 1) {
    throw 'CRLF fixture is missing the session answer seam'
  }
  if ([regex]::Matches($stagedCRLFDuty, 'a\.appRunZalo\(ctx, run, pz, threadID, question, appZaloCurrentMsgID\(reply\.ReplyQuote\), history, found, files, step\)').Count -ne 1) {
    throw 'CRLF fixture is missing the session runner seam'
  }

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
