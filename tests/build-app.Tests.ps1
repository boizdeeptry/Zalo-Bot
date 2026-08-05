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
  $personaFile = Join-Path $personaSource 'Cẩm nang boizdeeptry v2.md'
  $rosterFile = Join-Path $personaSource 'Sổ tay nhận diện thành viên.md'
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
  Write-TestFile (Join-Path $repo 'README.md') "tracked`n"
  Write-TestFile (Join-Path $repo 'untracked.txt') "must not be staged`n"
  Write-TestFile (Join-Path $overlay 'internal\daemon\app_routes.go') "package daemon`n"
  Write-TestFile (Join-Path $overlay 'internal\webui\static\core\router.js') "export {};`n"

  & git -C $repo add -- 'internal' 'README.md'
  if ($LASTEXITCODE -ne 0) { throw 'Could not stage fixture files' }
  $statusBefore = (& git -C $repo status --short) -join "`n"

  $gotStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot $stage
  Assert-Equal $gotStage ([IO.Path]::GetFullPath($stage)) 'Stage path was not normalized'
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
  if ([regex]::Matches($stagedServer, 'a\.registerAppRoutes\(mux\)').Count -ne 1) { throw 'Route seam was not applied exactly once' }
  if ([regex]::Matches($stagedStore, 'migrateApp\(db\)').Count -ne 1) { throw 'Migration seam was not applied exactly once' }
  if ([regex]::Matches($stagedZalo, 'evaluateAppWorkflow\(req, msg\)').Count -ne 1) { throw 'Workflow seam was not applied exactly once' }

  $sourceServer = [IO.File]::ReadAllText((Join-Path $repo 'internal\daemon\server.go'))
  if ($sourceServer -match 'registerAppRoutes') { throw 'Source repo was changed by staging' }
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

  Write-Host 'PASS: staging copies tracked files and applies three guarded seams.'
} finally {
  if (Test-Path -LiteralPath $stageTestRoot) {
    Remove-Item -LiteralPath $stageTestRoot -Recurse -Force
  }
}
