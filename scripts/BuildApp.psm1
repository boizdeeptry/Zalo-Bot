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
  $outPrefix = $outKey + [IO.Path]::DirectorySeparatorChar

  if ($repoKey.Equals($outKey, [StringComparison]::OrdinalIgnoreCase) -or
      $repoKey.StartsWith($outPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "repo không được trùng hoặc nằm trong thư mục output '$outPath'"
  }

  [PSCustomObject]@{
    Repo = $repoPath
    Out = $outPath
  }
}

Export-ModuleMember -Function Resolve-BuildPaths
