param(
  [ValidateSet('store', 'daemon', 'all')]
  [string]$Package = 'all',
  [string]$Run = '',
  [string]$Repo = 'C:\Users\manva\OneDrive\Máy tính\agentdc'
)

$ErrorActionPreference = 'Stop'

$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$overlay = Join-Path $projectRoot 'appmode\overlay'
$stageRoot = Join-Path ([IO.Path]::GetTempPath()) `
  ('agentdc-overlay-tests-' + [guid]::NewGuid().ToString('N'))
$stage = $null
$succeeded = $false

Import-Module (Join-Path $projectRoot 'scripts\BuildApp.psm1') -Force

try {
  Assert-CleanGitSource -Repo $Repo | Out-Null
  $stage = New-AppStage -Repo $Repo -Overlay $overlay -StageRoot $stageRoot
  Apply-AppSeams -Stage $stage

  $target = switch ($Package) {
    'store' { './internal/store' }
    'daemon' { './internal/daemon' }
    default { './...' }
  }
  $arguments = @('test', '-count=1', '-skip', (Get-AppGoTestSkipPattern))
  if ($Run) { $arguments += @('-run', $Run) }
  $arguments += $target

  Push-Location -LiteralPath $stage
  try {
    & go @arguments
    if ($LASTEXITCODE -ne 0) {
      throw "go test failed with exit code $LASTEXITCODE"
    }
  } finally {
    Pop-Location
  }

  Assert-CleanGitSource -Repo $Repo | Out-Null
  $succeeded = $true
} finally {
  if ($succeeded -and (Test-Path -LiteralPath $stageRoot)) {
    Remove-Item -LiteralPath $stageRoot -Recurse -Force
  } elseif (-not $succeeded -and (Test-Path -LiteralPath $stageRoot)) {
    Write-Warning "Stage retained for diagnosis: $stageRoot"
  }
}
