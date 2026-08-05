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
    'Cẩm nang boizdeeptry v2.md'
    'Sổ tay nhận diện thành viên.md'
  )
  foreach ($name in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourcePath $name) -PathType Leaf)) {
      throw "nguồn persona thiếu file '$name' trong '$sourcePath'"
    }
  }

  return $sourcePath
}

Export-ModuleMember -Function Resolve-BuildPaths, Clear-AppOutput, Resolve-PersonaSource
