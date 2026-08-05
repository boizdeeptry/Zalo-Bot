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

  Write-Host 'PASS: Resolve-BuildPaths normalizes and protects build paths.'
} finally {
  if (Test-Path -LiteralPath $root) {
    Remove-Item -LiteralPath $root -Recurse -Force
  }
}
