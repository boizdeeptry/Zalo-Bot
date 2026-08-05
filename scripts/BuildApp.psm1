Set-StrictMode -Version Latest

function Resolve-BuildPaths {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Repo,
    [Parameter(Mandatory)][string]$Out
  )

  $repoPath = [IO.Path]::GetFullPath($Repo)
  $outPath = [IO.Path]::GetFullPath($Out)

  if (-not (Test-Path -LiteralPath (Join-Path $repoPath '.git'))) {
    throw "không thấy repo Git ở '$repoPath'"
  }

  $repoKey = $repoPath.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
  $outKey = $outPath.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
  $repoPrefix = $repoKey + [IO.Path]::DirectorySeparatorChar
  $outPrefix = $outKey + [IO.Path]::DirectorySeparatorChar

  if ($repoKey.Equals($outKey, [StringComparison]::OrdinalIgnoreCase) -or
      $repoKey.StartsWith($outPrefix, [StringComparison]::OrdinalIgnoreCase) -or
      $outKey.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "repo và output không được trùng hoặc nằm bên trong nhau: '$repoPath', '$outPath'"
  }

  [PSCustomObject]@{
    Repo = $repoPath
    Out = $outPath
  }
}

function Clear-AppOutput {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Out,
    [switch]$KeepData
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  $volumeRoot = [IO.Path]::GetPathRoot($outPath)
  if ($outPath.TrimEnd('\', '/').Equals($volumeRoot.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase)) {
    throw "từ chối xóa nội dung thư mục gốc '$outPath'"
  }
  if (-not (Test-Path -LiteralPath $outPath)) { return }

  $items = Get-ChildItem -LiteralPath $outPath -Force
  if ($KeepData) {
    $items = $items | Where-Object { $_.Name -cne 'data' }
  }
  foreach ($item in $items) {
    Remove-Item -LiteralPath $item.FullName -Recurse -Force
  }
}

function Resolve-PersonaSource {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$PersonaSource)

  $sourcePath = [IO.Path]::GetFullPath($PersonaSource)
  $required = @(
    'persona.md'
    'roster.md'
  )
  foreach ($name in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourcePath $name) -PathType Leaf)) {
      throw "nguồn persona thiếu file '$name' trong '$sourcePath'"
    }
  }

  return $sourcePath
}

function New-AppStage {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Repo,
    [Parameter(Mandatory)][string]$Overlay,
    [Parameter(Mandatory)][string]$StageRoot
  )

  $paths = Resolve-BuildPaths -Repo $Repo -Out $StageRoot
  $repoPath = $paths.Repo
  $stagePath = $paths.Out
  $overlayPath = [IO.Path]::GetFullPath($Overlay)

  if (-not (Test-Path -LiteralPath $overlayPath -PathType Container)) {
    throw "không thấy Portal overlay ở '$overlayPath'"
  }
  if (Test-Path -LiteralPath $stagePath) {
    throw "thư mục stage đã tồn tại: '$stagePath'"
  }

  $repoPrefix = $repoPath.TrimEnd(
    [IO.Path]::DirectorySeparatorChar,
    [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
  if ($stagePath.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "stage không được nằm trong repo nguồn '$repoPath'"
  }

  [IO.Directory]::CreateDirectory($stagePath) | Out-Null

  $trackedOutput = & git -C $repoPath --no-optional-locks ls-files -z
  if ($LASTEXITCODE -ne 0) {
    throw "git ls-files thất bại cho '$repoPath'"
  }
  $tracked = (($trackedOutput -join "`n") -split [char]0) |
    Where-Object { $_.Length -gt 0 }

  foreach ($entry in $tracked) {
    $relative = $entry.Replace('/', [IO.Path]::DirectorySeparatorChar)
    $source = Join-Path $repoPath $relative
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
      throw "Git theo dõi một file không đọc được: '$entry'"
    }
    $destination = Join-Path $stagePath $relative
    [IO.Directory]::CreateDirectory((Split-Path -Parent $destination)) | Out-Null
    Copy-Item -LiteralPath $source -Destination $destination -Force
  }

  foreach ($item in Get-ChildItem -LiteralPath $overlayPath -File -Recurse -Force) {
    $relative = [IO.Path]::GetRelativePath($overlayPath, $item.FullName)
    $destination = Join-Path $stagePath $relative
    [IO.Directory]::CreateDirectory((Split-Path -Parent $destination)) | Out-Null
    Copy-Item -LiteralPath $item.FullName -Destination $destination -Force
  }

  return $stagePath
}

function Replace-ExactlyOnce {
  param(
    [Parameter(Mandatory)][string]$Text,
    [Parameter(Mandatory)][string]$Needle,
    [Parameter(Mandatory)][string]$Replacement,
    [Parameter(Mandatory)][string]$Label
  )

  $count = 0
  $offset = 0
  while ($offset -le $Text.Length - $Needle.Length) {
    $match = $Text.IndexOf($Needle, $offset, [StringComparison]::Ordinal)
    if ($match -lt 0) { break }
    $count++
    $offset = $match + $Needle.Length
  }
  if ($count -ne 1) {
    throw "$Label`: expected exactly 1 match, found $count"
  }

  return $Text.Replace($Needle, $Replacement)
}

function Assert-SignatureAbsent {
  param(
    [Parameter(Mandatory)][string]$Text,
    [Parameter(Mandatory)][string]$Signature,
    [Parameter(Mandatory)][string]$Label
  )

  if ($Text.IndexOf($Signature, [StringComparison]::Ordinal) -ge 0) {
    throw "$Label`: inserted signature already present"
  }
}

function Apply-AppSeams {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Stage)

  $stagePath = [IO.Path]::GetFullPath($Stage)
  $serverPath = Join-Path $stagePath 'internal\daemon\server.go'
  $storePath = Join-Path $stagePath 'internal\store\store.go'
  $zaloPath = Join-Path $stagePath 'internal\daemon\zalo.go'

  $server = [IO.File]::ReadAllText($serverPath)
  $store = [IO.File]::ReadAllText($storePath)
  $zalo = [IO.File]::ReadAllText($zaloPath)
  $serverNewline = if ($server.Contains("`r`n")) { "`r`n" } else { "`n" }
  $storeNewline = if ($store.Contains("`r`n")) { "`r`n" } else { "`n" }
  $zaloNewline = if ($zalo.Contains("`r`n")) { "`r`n" } else { "`n" }

  Assert-SignatureAbsent -Text $server -Signature 'a.registerAppRoutes(mux)' -Label 'route seam'
  Assert-SignatureAbsent -Text $store -Signature 'migrateApp(db)' -Label 'migration seam'
  Assert-SignatureAbsent -Text $zalo -Signature 'evaluateAppWorkflow(req, msg)' -Label 'workflow seam'

  $routeNeedle = "`tmux.Handle(`"POST /shutdown`", a.auth(a.handleShutdown))"
  $serverUpdated = Replace-ExactlyOnce -Text $server -Needle $routeNeedle `
    -Replacement ("`ta.registerAppRoutes(mux)" + $serverNewline + $routeNeedle) `
    -Label 'route seam'

  $migrationNeedle = "`treturn nil" + $storeNewline + '}' + $storeNewline + $storeNewline +
    '// hasColumn asks SQLite rather than tracking a version number'
  $storeUpdated = Replace-ExactlyOnce -Text $store -Needle $migrationNeedle `
    -Replacement ("`tif err := migrateApp(db); err != nil { return err }" + $storeNewline + $migrationNeedle) `
    -Label 'migration seam'

  $workflowNeedle = "`t`ta.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{"
  $workflowPrefix = @(
    "`t`tdecision, workflowErr := a.evaluateAppWorkflow(req, msg)"
    "`t`tif workflowErr != nil {"
    "`t`t`ta.logger.Error(`"portal workflow failed`", `"error`", workflowErr)"
    "`t`t`tbreak"
    "`t`t}"
    "`t`tif decision != appWorkflowContinue {"
    "`t`t`tbreak"
    "`t`t}"
  ) -join $zaloNewline
  $zaloUpdated = Replace-ExactlyOnce -Text $zalo -Needle $workflowNeedle `
    -Replacement ($workflowPrefix + $zaloNewline + $workflowNeedle) `
    -Label 'workflow seam'

  $utf8NoBom = [Text.UTF8Encoding]::new($false)
  [IO.File]::WriteAllText($serverPath, $serverUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($storePath, $storeUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($zaloPath, $zaloUpdated, $utf8NoBom)
}

function Assert-AppPackage {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Out,
    [switch]$AllowZaloCredentials
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  $required = @(
    'app\agentdc.exe'
    'app\transport\dist\index.js'
    'app\node\node.exe'
    'Start.vbs'
    'Stop.bat'
    'README.txt'
    'brain\wiki\index.md'
  )
  foreach ($relative in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $outPath $relative) -PathType Leaf)) {
      throw "package missing $relative"
    }
  }

  $credentials = Join-Path $outPath 'data\zalo\credentials.json'
  if (-not $AllowZaloCredentials -and (Test-Path -LiteralPath $credentials -PathType Leaf)) {
    throw 'package contains Zalo credentials'
  }

  return $outPath
}

Export-ModuleMember -Function Resolve-BuildPaths, Clear-AppOutput, Resolve-PersonaSource, New-AppStage, Apply-AppSeams, Assert-AppPackage
