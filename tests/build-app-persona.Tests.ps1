$ErrorActionPreference = 'Stop'

Import-Module (Join-Path $PSScriptRoot '..\scripts\BuildApp.psm1') -Force

$script:Utf8 = [Text.UTF8Encoding]::new($false, $true)
$script:LegacyPersonaSHA256 = '8c6ca4f7aa19096a0619c706286f7cff97452c3c4025c03ab59b083895bbacf2'
$script:BuildAppModule = Get-Module BuildApp -ErrorAction Stop

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

function Assert-True {
  param(
    [Parameter(Mandatory)][bool]$Condition,
    [Parameter(Mandatory)][string]$Message
  )

  if (-not $Condition) { throw $Message }
}

function Assert-ThrowsLike {
  param(
    [Parameter(Mandatory)][scriptblock]$Action,
    [Parameter(Mandatory)][string]$Pattern,
    [Parameter(Mandatory)][string]$Message,
    [string]$ForbiddenDetail
  )

  try {
    & $Action
  } catch {
    if ($_.Exception.Message -notmatch $Pattern) {
      throw "$Message. Unexpected error: $($_.Exception.Message)"
    }
    if ($ForbiddenDetail -and $_.Exception.Message.Contains($ForbiddenDetail)) {
      throw "$Message. Error leaked forbidden detail."
    }
    return
  }
  throw "$Message. Expected an error matching '$Pattern'."
}

function Write-TestBytes {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][byte[]]$Bytes
  )

  [IO.Directory]::CreateDirectory((Split-Path -Parent $Path)) | Out-Null
  [IO.File]::WriteAllBytes($Path, $Bytes)
}

function Write-TestText {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][string]$Text
  )

  $normalized = [regex]::Replace($Text, '\r\n?', "`n")
  Write-TestBytes -Path $Path -Bytes $script:Utf8.GetBytes($normalized)
}

function Get-TestIdentityJSON {
  param(
    [Parameter(Mandatory)][string]$DisplayName,
    [Parameter(Mandatory)][object[]]$Identities
  )

  $document = [ordered]@{
    version = 1
    display_name = $DisplayName
    persona_identities = @($Identities | ForEach-Object {
      [ordered]@{ role = [string]$_.Role; value = [string]$_.Value }
    })
  }
  return (($document | ConvertTo-Json -Compress -Depth 4) + "`n")
}

function New-TestPersonaSource {
  param(
    [Parameter(Mandatory)][string]$Path,
    [object[]]$Identities = @(
      [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
      [PSCustomObject]@{ Role = 'expert'; Value = 'Anh Trường' }
    ),
    [string]$DisplayName = 'boizdeeptry',
    [string]$PersonaText,
    [switch]$WithSourceBackup
  )

  if ($PSBoundParameters.ContainsKey('PersonaText') -eq $false) {
    $PersonaText = "# Persona`nboizdeeptry tu van cung Anh Trường.`n"
  }
  [IO.Directory]::CreateDirectory((Join-Path $Path 'overlay')) | Out-Null
  Write-TestText -Path (Join-Path $Path 'identity.json') `
    -Text (Get-TestIdentityJSON -DisplayName $DisplayName -Identities $Identities)
  Write-TestText -Path (Join-Path $Path 'persona.md') -Text $PersonaText
  Write-TestText -Path (Join-Path $Path 'roster.md') -Text "boizdeeptry`n"
  Write-TestText -Path (Join-Path $Path 'overlay\README.md') -Text "Anh Trường`n"
  if ($WithSourceBackup) {
    Write-TestBytes -Path (Join-Path $Path 'persona.md.goc') -Bytes ([byte[]](0xff, 0xfe, 0x7b))
  }
  return $Path
}

function Get-TestDirectoryFingerprint {
  param([Parameter(Mandatory)][string]$Path)

  $records = [Collections.Generic.List[string]]::new()
  $rootItem = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
  $records.Add(".`0root`0$([int64]$rootItem.Attributes)`0$($rootItem.LinkType)`0$($rootItem.Target -join '|')")
  foreach ($item in @(Get-ChildItem -LiteralPath $Path -Force -Recurse | Sort-Object FullName)) {
    $relative = [IO.Path]::GetRelativePath($Path, $item.FullName).Replace('\', '/')
    $kind = if ($item.PSIsContainer) { 'directory' } else { 'file' }
    $record = "$relative`0$kind`0$([int64]$item.Attributes)`0$($item.LinkType)`0$($item.Target -join '|')"
    if (-not $item.PSIsContainer) {
      $hash = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
      $record += "`0$($item.Length)`0$hash"
    }
    $records.Add($record)
  }
  $payload = $script:Utf8.GetBytes(($records -join "`n"))
  return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($payload)).ToLowerInvariant()
}

function New-TestCaseRoots {
  param([Parameter(Mandatory)][string]$Label)

  $safe = [regex]::Replace($Label, '[^A-Za-z0-9-]', '-')
  $root = Join-Path ([IO.Path]::GetTempPath()) (
    'persona-' + $safe + '-' + [guid]::NewGuid().ToString('N'))
  $source = Join-Path $root 'source'
  $out = Join-Path $root 'out'
  New-TestPersonaSource -Path $source | Out-Null
  Write-TestBytes -Path (Join-Path $out 'sentinel.bin') `
    -Bytes $script:Utf8.GetBytes('OUTPUT-SENTINEL-MUST-STAY-BYTE-EXACT')
  return [PSCustomObject]@{ Root = $root; Source = $source; Out = $out }
}

function Invoke-InvalidPersonaCase {
  param(
    [Parameter(Mandatory)][string]$Label,
    [Parameter(Mandatory)][scriptblock]$Mutate,
    [string]$Pattern = 'persona',
    [string]$ForbiddenDetail
  )

  $case = New-TestCaseRoots -Label $Label
  try {
    & $Mutate $case.Source $case.Root
    $sourceBefore = Get-TestDirectoryFingerprint -Path $case.Source
    $outBefore = Get-TestDirectoryFingerprint -Path $case.Out
    $sentinel = Join-Path $case.Out 'sentinel.bin'
    $sentinelBefore = (Get-FileHash -LiteralPath $sentinel -Algorithm SHA256).Hash
    $rejectionFailure = $null
    try {
      Assert-ThrowsLike -Action {
        Read-AppPersonaSourceSnapshot -PersonaSource $case.Source -Out $case.Out
      } -Pattern $Pattern -ForbiddenDetail $ForbiddenDetail `
        -Message "Invalid Persona case '$Label' was accepted"
    } catch {
      $rejectionFailure = $_
    }
    Assert-Equal (Get-TestDirectoryFingerprint -Path $case.Source) $sourceBefore `
      "Invalid Persona case '$Label' changed its source"
    Assert-Equal (Get-TestDirectoryFingerprint -Path $case.Out) $outBefore `
      "Invalid Persona case '$Label' changed the output tree"
    Assert-Equal ((Get-FileHash -LiteralPath $sentinel -Algorithm SHA256).Hash) $sentinelBefore `
      "Invalid Persona case '$Label' changed the output sentinel"
    Assert-Equal ([IO.File]::ReadAllText($sentinel)) 'OUTPUT-SENTINEL-MUST-STAY-BYTE-EXACT' `
      "Invalid Persona case '$Label' changed sentinel bytes"
    if ($rejectionFailure) { throw $rejectionFailure }
  } finally {
    if (Test-Path -LiteralPath $case.Root) {
      Remove-Item -LiteralPath $case.Root -Recurse -Force
    }
  }
}

function Get-TestSnapshotFiles {
  param([Parameter(Mandatory)]$Snapshot)

  $files = @{}
  foreach ($file in @($Snapshot.GetFiles())) {
    $files[[string]$file.Path] = [byte[]]$file.GetBytes()
  }
  return $files
}

function Add-TestHashBytes {
  param(
    [Parameter(Mandatory)][Security.Cryptography.IncrementalHash]$Hash,
    [Parameter(Mandatory)][byte[]]$Bytes
  )

  $Hash.AppendData($Bytes)
}

function Get-TestUInt64BigEndian {
  param([Parameter(Mandatory)][UInt64]$Value)

  $bytes = [BitConverter]::GetBytes($Value)
  if ([BitConverter]::IsLittleEndian) { [Array]::Reverse($bytes) }
  return $bytes
}

function Get-TestPersonaTreeSHA256 {
  param([Parameter(Mandatory)][hashtable]$Files)

  $paths = [string[]]@($Files.Keys)
  [Array]::Sort($paths, [StringComparer]::Ordinal)
  $hash = [Security.Cryptography.IncrementalHash]::CreateHash(
    [Security.Cryptography.HashAlgorithmName]::SHA256)
  try {
    Add-TestHashBytes -Hash $hash -Bytes $script:Utf8.GetBytes("agentdc/persona-default-tree/v1`0")
    foreach ($path in $paths) {
      $pathBytes = $script:Utf8.GetBytes($path)
      $content = [byte[]]$Files[$path]
      Add-TestHashBytes -Hash $hash -Bytes (Get-TestUInt64BigEndian ([UInt64]$pathBytes.Length))
      Add-TestHashBytes -Hash $hash -Bytes $pathBytes
      Add-TestHashBytes -Hash $hash -Bytes (Get-TestUInt64BigEndian ([UInt64]$content.Length))
      Add-TestHashBytes -Hash $hash -Bytes $content
    }
    return [Convert]::ToHexString($hash.GetHashAndReset()).ToLowerInvariant()
  } finally {
    $hash.Dispose()
  }
}

function Get-TestCanonicalPersonaManifest {
  param(
    [Parameter(Mandatory)][hashtable]$Files,
    [Parameter(Mandatory)][string]$TreeSHA256
  )

  $paths = [string[]]@($Files.Keys)
  [Array]::Sort($paths, [StringComparer]::Ordinal)
  $entries = [Collections.Generic.List[string]]::new()
  foreach ($path in $paths) {
    $content = [byte[]]$Files[$path]
    $sha256 = [Convert]::ToHexString(
      [Security.Cryptography.SHA256]::HashData($content)).ToLowerInvariant()
    $jsonPath = '"' + [Text.Json.JsonEncodedText]::Encode([string]$path).ToString() + '"'
    $entries.Add('{"path":' + $jsonPath + ',"bytes":' +
      $content.Length.ToString([Globalization.CultureInfo]::InvariantCulture) +
      ',"sha256":"' + $sha256 + '"}')
  }
  return '{"version":1,"files":[' + ($entries -join ',') +
    '],"tree_sha256":"' + $TreeSHA256 +
    '","legacy_persona_sha256":["' + $script:LegacyPersonaSHA256 + '"]}' + "`n"
}

# These commands are the public seam used by both this suite and build-app.ps1. Keep a real
# source/output sentinel around the RED seam check so a missing implementation can never hide
# an accidental preflight mutation.
$publicSeamRoot = Join-Path ([IO.Path]::GetTempPath()) (
  'persona-public-seam-' + [guid]::NewGuid().ToString('N'))
try {
  $publicSeamSource = Join-Path $publicSeamRoot 'source'
  $publicSeamOut = Join-Path $publicSeamRoot 'out'
  New-TestPersonaSource -Path $publicSeamSource | Out-Null
  Write-TestText (Join-Path $publicSeamOut 'sentinel.bin') 'PUBLIC-SEAM-SENTINEL-UNCHANGED'
  $publicSeamSourceBefore = Get-TestDirectoryFingerprint $publicSeamSource
  $publicSeamSentinelBefore = (Get-FileHash -LiteralPath (
      Join-Path $publicSeamOut 'sentinel.bin') -Algorithm SHA256).Hash
  try {
    foreach ($commandName in @(
        'Read-AppPersonaSourceSnapshot',
        'Write-AppPersonaPackage',
        'Assert-AppPersonaPackagePrivacy'
      )) {
      Get-Command $commandName -CommandType Function -ErrorAction Stop | Out-Null
    }
  } catch {
    Assert-Equal (Get-TestDirectoryFingerprint $publicSeamSource) $publicSeamSourceBefore `
      'Missing Persona public seam changed source bytes'
    Assert-Equal ((Get-FileHash -LiteralPath (
        Join-Path $publicSeamOut 'sentinel.bin') -Algorithm SHA256).Hash) `
      $publicSeamSentinelBefore 'Missing Persona public seam changed output sentinel'
    Write-Host 'RED GUARD PASS: missing Persona public seam left source/output byte-identical.'
    throw
  }
} finally {
  if (Test-Path -LiteralPath $publicSeamRoot) {
    Remove-Item -LiteralPath $publicSeamRoot -Recurse -Force
  }
}

$invalidIdentityCases = @(
  @{
    Label = 'missing-display-name'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"persona_identities":[{"role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'unknown-root-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":"boizdeeptry"}],"secret":true}'
    }
  },
  @{
    Label = 'duplicate-root-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'case-folded-root-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"Version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'case-folded-duplicate-root-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"Version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'missing-entry-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":[{"value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'unknown-entry-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":"boizdeeptry","note":"x"}]}'
    }
  },
  @{
    Label = 'duplicate-entry-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'case-folded-duplicate-entry-field'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","Role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'identity-root-is-array'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') '[]'
    }
  },
  @{
    Label = 'identity-version-is-string'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":"1","display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":"boizdeeptry"}]}'
    }
  },
  @{
    Label = 'identity-list-is-object'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":{"role":"assistant","value":"boizdeeptry"}}'
    }
  },
  @{
    Label = 'identity-entry-is-scalar'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":["boizdeeptry"]}'
    }
  },
  @{
    Label = 'identity-value-is-number'
    Mutate = {
      param($source)
      Write-TestText (Join-Path $source 'identity.json') `
        '{"version":1,"display_name":"boizdeeptry","persona_identities":[{"role":"assistant","value":7}]}'
    }
  },
  @{
    Label = 'unsupported-version'
    Mutate = {
      param($source)
      $text = [IO.File]::ReadAllText((Join-Path $source 'identity.json')).Replace('"version":1', '"version":2')
      Write-TestText (Join-Path $source 'identity.json') $text
    }
  },
  @{
    Label = 'invalid-role'
    Mutate = {
      param($source)
      $text = [IO.File]::ReadAllText((Join-Path $source 'identity.json')).Replace('"expert"', '"operator"')
      Write-TestText (Join-Path $source 'identity.json') $text
    }
  },
  @{
    Label = 'blank-identity'
    Mutate = {
      param($source)
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = '   ' })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName '   ' -Identities $ids)
    }
  },
  @{
    Label = 'nonblank-trimmed-identity'
    Mutate = {
      param($source)
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = ' boizdeeptry ' })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName ' boizdeeptry ' -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') " boizdeeptry `n"
    }
  },
  @{
    Label = 'non-nfc-identity'
    Mutate = {
      param($source)
      $nonNFC = "A$([char]0x0301)gent"
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = $nonNFC })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName $nonNFC -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "$nonNFC`n"
    }
  },
  @{
    Label = 'oversize-identity'
    Mutate = {
      param($source)
      $large = 'x' * 257
      $ids = @(
        [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
        [PSCustomObject]@{ Role = 'expert'; Value = $large },
        [PSCustomObject]@{ Role = 'business'; Value = 'Anh Trường' }
      )
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "boizdeeptry $large Anh Trường`n"
    }
  },
  @{
    Label = 'display-name-over-60-code-points'
    Mutate = {
      param($source)
      $large = 'a' * 61
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = $large })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName $large -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "$large`n"
    }
  },
  @{
    Label = 'expert-identity-over-60-code-points'
    Mutate = {
      param($source)
      $large = 'e' * 61
      $ids = @(
        [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
        [PSCustomObject]@{ Role = 'expert'; Value = $large },
        [PSCustomObject]@{ Role = 'business'; Value = 'Anh Trường' }
      )
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "boizdeeptry $large Anh Trường`n"
    }
  },
  @{
    Label = 'expert-astral-identity-over-60-code-points'
    Mutate = {
      param($source)
      $large = [char]::ConvertFromUtf32(0x1f331) * 61
      $ids = @(
        [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
        [PSCustomObject]@{ Role = 'expert'; Value = $large },
        [PSCustomObject]@{ Role = 'business'; Value = 'Anh Trường' }
      )
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "boizdeeptry $large Anh Trường`n"
    }
  },
  @{
    Label = 'display-name-with-newline'
    Mutate = {
      param($source)
      $bad = "bot`nname"
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = $bad })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName $bad -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "$bad`n"
    }
  },
  @{
    Label = 'display-name-with-control'
    Mutate = {
      param($source)
      $bad = "bot$([char]0)name"
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = $bad })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName $bad -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "$bad`n"
    }
  },
  @{
    Label = 'display-name-with-placeholder'
    Mutate = {
      param($source)
      $bad = 'bot{{name}}'
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = $bad })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName $bad -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "$bad`n"
    }
  },
  @{
    Label = 'duplicate-identity-values'
    Mutate = {
      param($source)
      $ids = @(
        [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
        [PSCustomObject]@{ Role = 'expert'; Value = 'boizdeeptry' }
      )
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids)
    }
  },
  @{
    Label = 'zero-assistant-identities'
    Mutate = {
      param($source)
      $ids = @([PSCustomObject]@{ Role = 'expert'; Value = 'Anh Trường' })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'Anh Trường' -Identities $ids)
    }
  },
  @{
    Label = 'two-assistant-identities'
    Mutate = {
      param($source)
      $ids = @(
        [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
        [PSCustomObject]@{ Role = 'assistant'; Value = 'Bot Hai' }
      )
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "boizdeeptry Bot Hai`n"
    }
  },
  @{
    Label = 'display-name-mismatch'
    Mutate = {
      param($source)
      $text = [IO.File]::ReadAllText((Join-Path $source 'identity.json')).Replace(
        '"display_name":"boizdeeptry"', '"display_name":"Anh Trường"')
      Write-TestText (Join-Path $source 'identity.json') $text
    }
  },
  @{
    Label = 'more-than-eight-identities'
    Mutate = {
      param($source)
      $ids = [Collections.Generic.List[object]]::new()
      $ids.Add([PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' })
      foreach ($index in 1..8) {
        $ids.Add([PSCustomObject]@{ Role = 'expert'; Value = "Expert $index" })
      }
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids.ToArray())
      Write-TestText (Join-Path $source 'persona.md') `
        ("boizdeeptry " + (($ids | Select-Object -Skip 1 | ForEach-Object Value) -join ' ') + "`n")
    }
  },
  @{
    Label = 'declared-identity-missing-from-persona-set'
    Mutate = {
      param($source)
      $ids = @(
        [PSCustomObject]@{ Role = 'assistant'; Value = 'boizdeeptry' },
        [PSCustomObject]@{ Role = 'expert'; Value = 'Không xuất hiện' }
      )
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName 'boizdeeptry' -Identities $ids)
    }
  }
)

foreach ($case in $invalidIdentityCases) {
  Invoke-InvalidPersonaCase -Label $case.Label -Mutate $case.Mutate -Pattern 'persona|identity'
}
Write-Host 'PASS: strict identity schema, normalization, cardinality and presence are fail closed.'

$contentCases = @(
  @{ Label = 'persona-blank'; Relative = 'persona.md'; Bytes = $script:Utf8.GetBytes(" `t`n") },
  @{ Label = 'persona-over-400-kib'; Relative = 'persona.md'; Bytes = $script:Utf8.GetBytes(
      "boizdeeptry Anh Trường " + ('x' * 400KB) + "`n") },
  @{ Label = 'persona-hole'; Relative = 'persona.md'; Bytes = $script:Utf8.GetBytes("boizdeeptry Anh Trường {{TEN_BOT}}`n") },
  @{ Label = 'roster-hole'; Relative = 'roster.md'; Bytes = $script:Utf8.GetBytes("boizdeeptry {{TEN_BOT}}`n") },
  @{ Label = 'overlay-hole'; Relative = 'overlay\README.md'; Bytes = $script:Utf8.GetBytes("Anh Trường {{TEN_BOT}}`n") },
  @{ Label = 'persona-unmatched-open'; Relative = 'persona.md'; Bytes = $script:Utf8.GetBytes("boizdeeptry Anh Trường {oops`n") },
  @{ Label = 'roster-unmatched-close'; Relative = 'roster.md'; Bytes = $script:Utf8.GetBytes("boizdeeptry oops}`n") },
  @{ Label = 'overlay-invalid-utf8'; Relative = 'overlay\README.md'; Bytes = [byte[]](0xc3, 0x28) },
  @{ Label = 'roster-invalid-utf8'; Relative = 'roster.md'; Bytes = [byte[]](0xff, 0xfe) },
  @{ Label = 'persona-invalid-utf8'; Relative = 'persona.md'; Bytes = [byte[]](0xf0, 0x28, 0x8c, 0xbc) }
)
foreach ($contentCase in $contentCases) {
  $relative = $contentCase.Relative
  $bytes = $contentCase.Bytes
  Invoke-InvalidPersonaCase -Label $contentCase.Label -Pattern 'persona' -Mutate {
    param($source)
    Write-TestBytes -Path (Join-Path $source $relative) -Bytes $bytes
  }
}

Invoke-InvalidPersonaCase -Label 'identity-invalid-utf8' -Pattern 'persona|identity' -Mutate {
  param($source)
  Write-TestBytes -Path (Join-Path $source 'identity.json') -Bytes ([byte[]](0xff, 0xfe))
}
Invoke-InvalidPersonaCase -Label 'identity-file-missing' -Pattern 'persona|identity' -Mutate {
  param($source)
  Remove-Item -LiteralPath (Join-Path $source 'identity.json') -Force
}
Invoke-InvalidPersonaCase -Label 'undeclared-historical-identity' -Pattern 'persona|identity' `
  -ForbiddenDetail 'Bé Mi' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'persona.md') "boizdeeptry Anh Trường Bé Mi`n"
}
Invoke-InvalidPersonaCase -Label 'unknown-source-sibling' -Pattern 'persona' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'notes.txt') "unknown`n"
}
Invoke-InvalidPersonaCase -Label 'unknown-source-directory' -Pattern 'persona' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'private\notes.md') "unknown`n"
}
Invoke-InvalidPersonaCase -Label 'non-nfc-overlay-path' -Pattern 'persona' -Mutate {
  param($source)
  $decomposed = "Cafe$([char]0x0301).md"
  Write-TestText (Join-Path $source (Join-Path 'overlay' $decomposed)) "boizdeeptry Anh Trường`n"
}
Invoke-InvalidPersonaCase -Label 'declared-identity-in-overlay-path' -Pattern 'persona|privacy' `
  -ForbiddenDetail 'boizdeeptry' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'overlay\boizdeeptry-note.md') "safe content`n"
}
Invoke-InvalidPersonaCase -Label 'sensitive-canary-in-overlay-path' -Pattern 'persona|privacy' `
  -ForbiddenDetail 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'overlay\ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E.md') "safe content`n"
}
Invoke-InvalidPersonaCase -Label 'sensitive-canary-in-invalid-utf8-overlay-path' `
  -Pattern 'persona|privacy|UTF-8' -ForbiddenDetail 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E' -Mutate {
  param($source)
  Write-TestBytes (Join-Path $source 'overlay\ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E.md') `
    ([byte[]](0xff, 0xfe))
}
Invoke-InvalidPersonaCase -Label 'declared-identity-in-invalid-utf8-overlay-path' `
  -Pattern 'persona|privacy|UTF-8' -ForbiddenDetail 'boizdeeptry' -Mutate {
  param($source)
  Write-TestBytes (Join-Path $source 'overlay\boizdeeptry-note.md') `
    ([byte[]](0xff, 0xfe))
}

$relativePathValidation = & $script:BuildAppModule {
  param([string[]]$Paths)

  if (-not (Get-Command Assert-AppPersonaRelativePaths -CommandType Function `
      -ErrorAction SilentlyContinue)) {
    return [PSCustomObject]@{ Outcome = 'missing'; Message = '' }
  }
  try {
    Assert-AppPersonaRelativePaths -RelativePath $Paths
    return [PSCustomObject]@{ Outcome = 'accepted'; Message = '' }
  } catch {
    return [PSCustomObject]@{ Outcome = 'rejected'; Message = $_.Exception.Message }
  }
} @('overlay/A.md', 'overlay/a.md')
Assert-Equal $relativePathValidation.Outcome 'rejected' `
  'Case-folded Persona relative paths were not rejected before snapshot reads'
Assert-True (-not $relativePathValidation.Message.Contains('overlay/A.md')) `
  'Case-folded path rejection leaked a source-relative path'

$controlPathValidation = & $script:BuildAppModule {
  param([string[]]$Paths)
  try {
    Assert-AppPersonaRelativePaths -RelativePath $Paths
    return [PSCustomObject]@{ Outcome = 'accepted'; Message = '' }
  } catch {
    return [PSCustomObject]@{ Outcome = 'rejected'; Message = $_.Exception.Message }
  }
} @("overlay/a$([char]9)b.md")
Assert-Equal $controlPathValidation.Outcome 'rejected' `
  'A control-character Persona path passed preflight validation'
Assert-True (-not $controlPathValidation.Message.Contains("a$([char]9)b.md")) `
  'Control-character path rejection leaked a source-relative path'

foreach ($whitespacePath in @(
    'overlay/ leading.md',
    'overlay/trailing.md ',
    "overlay/$([char]0x2003)wide.md",
    'overlay /child.md'
  )) {
  $whitespacePathValidation = & $script:BuildAppModule {
    param([string[]]$Paths)
    try {
      Assert-AppPersonaRelativePaths -RelativePath $Paths
      return [PSCustomObject]@{ Outcome = 'accepted'; Message = '' }
    } catch {
      return [PSCustomObject]@{ Outcome = 'rejected'; Message = $_.Exception.Message }
    }
  } @($whitespacePath)
  Assert-Equal $whitespacePathValidation.Outcome 'rejected' `
    "A whitespace-padded Persona path component was accepted: '$whitespacePath'"
  Assert-True (-not $whitespacePathValidation.Message.Contains($whitespacePath)) `
    'Whitespace-component path rejection leaked a source-relative path'
}
Invoke-InvalidPersonaCase -Label 'leading-whitespace-overlay-path' -Pattern 'persona|path' `
  -ForbiddenDetail ' leading.md' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'overlay\ leading.md') "safe content`n"
}

$controlIdentityValidation = & $script:BuildAppModule {
  param([string]$Value)
  try {
    $null = Assert-AppPersonaIdentityValue -Value $Value
    return 'accepted'
  } catch {
    return 'rejected'
  }
} "bot$([char]0)name"
Assert-Equal $controlIdentityValidation 'rejected' `
  'A control-character Persona identity passed preflight validation'

$unpairedSurrogatePath = 'overlay/' + ([char]0xd800).ToString() + '.md'
$invalidPathValidation = & $script:BuildAppModule {
  param([string[]]$Paths)

  if (-not (Get-Command Assert-AppPersonaRelativePaths -CommandType Function `
      -ErrorAction SilentlyContinue)) {
    return [PSCustomObject]@{ Outcome = 'missing'; Message = '' }
  }
  try {
    Assert-AppPersonaRelativePaths -RelativePath $Paths
    return [PSCustomObject]@{ Outcome = 'accepted'; Message = '' }
  } catch {
    return [PSCustomObject]@{ Outcome = 'rejected'; Message = $_.Exception.Message }
  }
} @($unpairedSurrogatePath)
Assert-Equal $invalidPathValidation.Outcome 'rejected' `
  'An unpaired-surrogate Persona relative path passed snapshot path validation'

$invalidPathFile = [AgentDCAppPersonaSnapshotFile]::new(
  $unpairedSurrogatePath, ([byte[]](0x61)), ('0' * 64))
Assert-ThrowsLike -Action {
  & $script:BuildAppModule {
    param($File)
    Get-AppPersonaTreeSHA256 -Files ([AgentDCAppPersonaSnapshotFile[]]@($File))
  } $invalidPathFile
} -Pattern 'persona|path|UTF-8' `
  -Message 'Persona tree hashing replaced an invalid UTF-16 path instead of rejecting it'
Invoke-InvalidPersonaCase -Label 'source-overlay-reparse' -Pattern 'persona|reparse' -Mutate {
  param($source, $root)
  Remove-Item -LiteralPath (Join-Path $source 'overlay') -Recurse -Force
  $target = Join-Path $root 'overlay-target'
  Write-TestText (Join-Path $target 'README.md') "Anh Trường`n"
  New-Item -ItemType Junction -Path (Join-Path $source 'overlay') -Target $target | Out-Null
}
Invoke-InvalidPersonaCase -Label 'sensitive-canary-in-nested-reparse-path' `
  -Pattern 'persona|reparse' -ForbiddenDetail 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E' -Mutate {
  param($source, $root)
  $target = Join-Path $root 'nested-overlay-target'
  Write-TestText (Join-Path $target 'README.md') "safe content`n"
  New-Item -ItemType Junction `
    -Path (Join-Path $source 'overlay\ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E') `
    -Target $target | Out-Null
}
Invoke-InvalidPersonaCase -Label 'source-goc-not-regular' -Pattern 'persona' -Mutate {
  param($source)
  [IO.Directory]::CreateDirectory((Join-Path $source 'persona.md.goc')) | Out-Null
}
Invoke-InvalidPersonaCase -Label 'source-goc-oversize' -Pattern 'persona' -Mutate {
  param($source)
  Write-TestBytes (Join-Path $source 'persona.md.goc') ([byte[]]::new(1MB + 1))
}
Invoke-InvalidPersonaCase -Label 'single-file-oversize' -Pattern 'persona' -Mutate {
  param($source)
  Write-TestBytes (Join-Path $source 'overlay\large.md') ([byte[]]::new(1MB + 1))
}
Invoke-InvalidPersonaCase -Label 'tree-oversize' -Pattern 'persona' -Mutate {
  param($source)
  foreach ($index in 1..4) {
    $bytes = [byte[]]::new(1MB)
    [Array]::Fill($bytes, [byte](0x40 + $index))
    Write-TestBytes (Join-Path $source "overlay\large-$index.md") $bytes
  }
}
Invoke-InvalidPersonaCase -Label 'too-many-files' -Pattern 'persona' -Mutate {
  param($source)
  foreach ($index in 1..62) {
    Write-TestText (Join-Path $source ("overlay\file-{0:D2}.md" -f $index)) "bounded`n"
  }
}
Invoke-InvalidPersonaCase -Label 'overlay-goc-is-not-input' -Pattern 'persona|privacy' -Mutate {
  param($source)
  Write-TestText (Join-Path $source 'overlay\old.goc') "boizdeeptry Anh Trường`n"
}
Invoke-InvalidPersonaCase -Label 'source-utf16le-canary' -Pattern 'persona|privacy' `
  -ForbiddenDetail 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E' -Mutate {
  param($source)
  Write-TestBytes (Join-Path $source 'overlay\README.md') `
    ([Text.UnicodeEncoding]::new($false, $false).GetBytes('ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E'))
}

foreach ($sourceCanaryCase in @(
    @{ Label = 'source-persona-session-canary'; Relative = 'persona.md'; Canary = 'APP_TEST_SESSION_CANARY_CLEAR_18D1' },
    @{ Label = 'source-roster-token-canary'; Relative = 'roster.md'; Canary = 'ONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91' },
    @{ Label = 'source-persona-prompt-canary'; Relative = 'persona.md'; Canary = 'ONBOARDING_USER_PROMPT_CANARY_CLEAR_8172' },
    @{ Label = 'source-roster-answer-canary'; Relative = 'roster.md'; Canary = 'ONBOARDING_ANSWER_CANARY_CLEAR_BC43' },
    @{ Label = 'source-overlay-config-canary'; Relative = 'overlay\README.md'; Canary = 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E' },
    @{ Label = 'source-identity-credential-canary'; Relative = 'identity.json'; Canary = 'ONBOARDING_CREDENTIAL_CANARY_CLEAR_D912'; Identity = $true },
    @{ Label = 'source-provider-credential-canary'; Relative = 'overlay\README.md'; Canary = 'sk-package-must-never-contain-7f36d2' }
  )) {
  $relative = $sourceCanaryCase.Relative
  $canary = $sourceCanaryCase.Canary
  $identityCase = [bool]$sourceCanaryCase.Identity
  Invoke-InvalidPersonaCase -Label $sourceCanaryCase.Label -Pattern 'persona|identity|privacy' `
    -ForbiddenDetail $canary -Mutate {
    param($source)
    if ($identityCase) {
      $ids = @([PSCustomObject]@{ Role = 'assistant'; Value = $canary })
      Write-TestText (Join-Path $source 'identity.json') `
        (Get-TestIdentityJSON -DisplayName $canary -Identities $ids)
      Write-TestText (Join-Path $source 'persona.md') "$canary`n"
    } else {
      $path = Join-Path $source $relative
      Write-TestText $path ([IO.File]::ReadAllText($path) + $canary + "`n")
    }
  }
}
# Exercise output overlap separately because the shared invalid-case helper owns its fixed sentinel path.
$overlapRoot = Join-Path ([IO.Path]::GetTempPath()) ('persona-overlap-' + [guid]::NewGuid().ToString('N'))
try {
  $overlapSource = Join-Path $overlapRoot 'source'
  New-TestPersonaSource -Path $overlapSource | Out-Null
  $nestedOut = Join-Path $overlapSource 'nested\new-output'
  $before = Get-TestDirectoryFingerprint $overlapSource
  Assert-ThrowsLike -Action {
    Read-AppPersonaSourceSnapshot -PersonaSource $overlapSource -Out $nestedOut
  } -Pattern 'overlap|protected' -Message 'A nonexistent output inside PersonaSource was accepted'
  Assert-True (-not (Test-Path -LiteralPath $nestedOut)) `
    'Output-overlap preflight created the nonexistent nested output'
  Assert-Equal (Get-TestDirectoryFingerprint $overlapSource) $before `
    'Output-overlap preflight changed PersonaSource'
  Assert-ThrowsLike -Action {
    Read-AppPersonaSourceSnapshot -PersonaSource $overlapSource -Out $overlapSource
  } -Pattern 'overlap|protected' -Message 'Output equal to PersonaSource was accepted'
  Assert-ThrowsLike -Action {
    Read-AppPersonaSourceSnapshot -PersonaSource $overlapSource -Out $overlapRoot
  } -Pattern 'overlap|protected' -Message 'Output containing PersonaSource was accepted'

  $protectedRoot = Join-Path $overlapRoot 'protected'
  [IO.Directory]::CreateDirectory($protectedRoot) | Out-Null
  $protectedOut = Join-Path $protectedRoot 'nested\new-output'
  Assert-ThrowsLike -Action {
    Read-AppPersonaSourceSnapshot -PersonaSource $overlapSource -Out $protectedOut `
      -ProtectedRoot @($protectedRoot)
  } -Pattern 'overlap|protected' `
    -Message 'A nonexistent output inside another protected root was accepted'
  Assert-True (-not (Test-Path -LiteralPath $protectedOut)) `
    'Protected-root overlap preflight created the nonexistent output'

  $alias = Join-Path $overlapRoot 'protected-alias'
  New-Item -ItemType Junction -Path $alias -Target $protectedRoot | Out-Null
  try {
    $aliasOut = Join-Path $alias 'aliased-output'
    Assert-ThrowsLike -Action {
      Read-AppPersonaSourceSnapshot -PersonaSource $overlapSource -Out $aliasOut `
        -ProtectedRoot @($protectedRoot)
    } -Pattern 'overlap|protected|reparse' `
      -Message 'A nonexistent output through a protected-root alias was accepted'
    Assert-True (-not (Test-Path -LiteralPath $aliasOut)) `
      'Protected-root alias preflight created the nonexistent output'
  } finally {
    Remove-Item -LiteralPath $alias -Force
  }
} finally {
  if (Test-Path -LiteralPath $overlapRoot) {
    Remove-Item -LiteralPath $overlapRoot -Recurse -Force
  }
}
Write-Host 'PASS: Persona file set, UTF-8, completeness, bounds and physical paths are fail closed.'

$minimalRoot = Join-Path ([IO.Path]::GetTempPath()) ('persona-minimal-' + [guid]::NewGuid().ToString('N'))
try {
  $minimalSource = Join-Path $minimalRoot 'source'
  New-TestPersonaSource -Path $minimalSource | Out-Null
  Remove-Item -LiteralPath (Join-Path $minimalSource 'roster.md') -Force
  Remove-Item -LiteralPath (Join-Path $minimalSource 'overlay') -Recurse -Force
  Assert-Equal (Resolve-PersonaSource -PersonaSource $minimalSource) `
    ([IO.Path]::GetFullPath($minimalSource)) 'Resolve rejected optional roster/overlay absence'
  $minimalOut = Join-Path $minimalRoot 'out'
  $minimalSnapshot = Read-AppPersonaSourceSnapshot -PersonaSource $minimalSource -Out $minimalOut
  Assert-Equal @($minimalSnapshot.GetFiles()).Count 2 `
    'A valid source without optional roster/overlay did not produce the exact two-file snapshot'
  Assert-Equal ((@($minimalSnapshot.GetFiles() | ForEach-Object Path) -join ',')) `
    'identity.json,persona.md' 'Minimal Persona snapshot file set drifted'
  Write-AppPersonaPackage -Snapshot $minimalSnapshot -Out $minimalOut
  foreach ($relative in @('identity.json', 'persona.md')) {
    Assert-True (Test-Path -LiteralPath (Join-Path $minimalOut `
        ('brain\reference\persona\' + $relative)) -PathType Leaf) `
      "Minimal working package missing '$relative'"
    Assert-True (Test-Path -LiteralPath (Join-Path $minimalOut `
        ('app\defaults\persona\' + $relative)) -PathType Leaf) `
      "Minimal default package missing '$relative'"
  }
  $minimalManifest = [IO.File]::ReadAllText(
    (Join-Path $minimalOut 'app\defaults\persona\build-manifest.json')) | ConvertFrom-Json
  Assert-Equal @($minimalManifest.files).Count 2 'Minimal Persona manifest did not contain exactly two files'

  $workingOverlayScaffold = Join-Path $minimalOut 'brain\reference\persona\overlay'
  [IO.Directory]::CreateDirectory($workingOverlayScaffold) | Out-Null
  Assert-AppPersonaPackagePrivacy -Out $minimalOut -Snapshot $minimalSnapshot

  foreach ($unexpectedDirectory in @(
      'brain\reference\persona\overlay\unexpected-empty',
      'app\defaults\persona\unexpected-empty'
    )) {
    $unexpectedDirectoryPath = Join-Path $minimalOut $unexpectedDirectory
    [IO.Directory]::CreateDirectory($unexpectedDirectoryPath) | Out-Null
    try {
      Assert-ThrowsLike -Action {
        Assert-AppPersonaPackagePrivacy -Out $minimalOut -Snapshot $minimalSnapshot
      } -Pattern 'persona|privacy|directory' -ForbiddenDetail 'unexpected-empty' `
        -Message "A minimal package accepted an unexpected Persona directory: '$unexpectedDirectory'"
    } finally {
      Remove-Item -LiteralPath $unexpectedDirectoryPath -Force
    }
  }
} finally {
  if (Test-Path -LiteralPath $minimalRoot) {
    Remove-Item -LiteralPath $minimalRoot -Recurse -Force
  }
}
Write-Host 'PASS: roster.md and overlay are optional Persona inputs.'

$writeRaceRoot = Join-Path ([IO.Path]::GetTempPath()) ('persona-write-race-' + [guid]::NewGuid().ToString('N'))
try {
  $writeRaceSource = Join-Path $writeRaceRoot 'source'
  $writeRaceOut = Join-Path $writeRaceRoot 'out'
  $writeRaceExternal = Join-Path $writeRaceRoot 'external-target'
  New-TestPersonaSource -Path $writeRaceSource | Out-Null
  Write-TestText (Join-Path $writeRaceExternal 'sentinel.bin') 'EXTERNAL-TARGET-MUST-STAY-UNCHANGED'
  $externalBefore = Get-TestDirectoryFingerprint $writeRaceExternal
  $writeRaceSnapshot = Read-AppPersonaSourceSnapshot -PersonaSource $writeRaceSource -Out $writeRaceOut
  $writeRaceSourceBefore = Get-TestDirectoryFingerprint $writeRaceSource
  New-Item -ItemType Junction -Path $writeRaceOut -Target $writeRaceExternal | Out-Null
  try {
    Assert-ThrowsLike -Action {
      Write-AppPersonaPackage -Snapshot $writeRaceSnapshot -Out $writeRaceOut
    } -Pattern 'persona|reparse|physical|overlap' `
      -Message 'Write accepted an output replaced by a junction after preflight'
    Assert-Equal (Get-TestDirectoryFingerprint $writeRaceExternal) $externalBefore `
      'Rejected Persona destination junction changed the external target'
    Assert-Equal (Get-TestDirectoryFingerprint $writeRaceSource) $writeRaceSourceBefore `
      'Rejected Persona destination junction changed PersonaSource'
  } finally {
    Remove-Item -LiteralPath $writeRaceOut -Force
  }
} finally {
  if (Test-Path -LiteralPath $writeRaceRoot) {
    Remove-Item -LiteralPath $writeRaceRoot -Recurse -Force
  }
}
Write-Host 'PASS: Write revalidates destination safety after snapshot preflight.'

$validRoot = Join-Path ([IO.Path]::GetTempPath()) ('persona-valid-' + [guid]::NewGuid().ToString('N'))
try {
  $source = Join-Path $validRoot 'source'
  New-TestPersonaSource -Path $source -WithSourceBackup | Out-Null
  Write-TestText (Join-Path $source 'overlay\Z.md') "boizdeeptry Z`n"
  Write-TestText (Join-Path $source 'overlay\a.md') "Anh Trường a`n"
  Write-TestText (Join-Path $source 'overlay\a+`b.md') "boizdeeptry punctuation`n"
  Write-TestText (Join-Path $source 'overlay\é.md') "boizdeeptry UTF-8`n"
  $sourceBefore = Get-TestDirectoryFingerprint $source
  $snapshot = Read-AppPersonaSourceSnapshot -PersonaSource $source
  Assert-Equal $snapshot.DisplayName 'boizdeeptry' 'Snapshot display name drifted'
  Assert-Equal @($snapshot.GetIdentities()).Count 2 'Snapshot identity count drifted'
  $snapshotFiles = Get-TestSnapshotFiles $snapshot
  Assert-Equal $snapshotFiles.Count 8 'Snapshot file count drifted'
  Assert-True (-not $snapshotFiles.ContainsKey('persona.md.goc')) `
    'Source operator backup entered the immutable snapshot'
  Assert-Equal $snapshot.TreeSHA256 (Get-TestPersonaTreeSHA256 $snapshotFiles) `
    'Snapshot tree digest does not match the independent v1 algorithm'

  $capturedPersona = [byte[]]$snapshotFiles['persona.md'].Clone()
  $exposed = @($snapshot.GetFiles() | Where-Object Path -eq 'persona.md')[0].GetBytes()
  $exposed[0] = $exposed[0] -bxor 0xff
  Write-TestText (Join-Path $source 'persona.md') "SOURCE-DRIFT-AFTER-SNAPSHOT`n"
  Remove-Item -LiteralPath (Join-Path $source 'roster.md') -Force
  $driftedSourceBeforeWrite = Get-TestDirectoryFingerprint $source

  $outOne = Join-Path $validRoot 'out-one'
  $outTwo = Join-Path $validRoot 'out-two'
  Write-AppPersonaPackage -Snapshot $snapshot -Out $outOne
  Write-AppPersonaPackage -Snapshot $snapshot -Out $outTwo

  Assert-Equal (Get-TestDirectoryFingerprint $source) $driftedSourceBeforeWrite `
    'Writing the Persona package changed the drifted source'
  foreach ($relative in $snapshotFiles.Keys) {
    $working = Join-Path $outOne ('brain\reference\persona\' + $relative.Replace('/', '\'))
    $default = Join-Path $outOne ('app\defaults\persona\' + $relative.Replace('/', '\'))
    Assert-True (Test-Path -LiteralPath $working -PathType Leaf) "Working Persona missing '$relative'"
    Assert-True (Test-Path -LiteralPath $default -PathType Leaf) "Default Persona missing '$relative'"
    Assert-True ([Linq.Enumerable]::SequenceEqual(
        [byte[]][IO.File]::ReadAllBytes($working), [byte[]]$snapshotFiles[$relative])) `
      "Working Persona bytes drifted for '$relative'"
    Assert-True ([Linq.Enumerable]::SequenceEqual(
        [byte[]][IO.File]::ReadAllBytes($default), [byte[]]$snapshotFiles[$relative])) `
      "Default Persona bytes drifted for '$relative'"
  }
  Assert-True ([Linq.Enumerable]::SequenceEqual(
      [byte[]][IO.File]::ReadAllBytes((Join-Path $outOne 'brain\reference\persona\persona.md')),
      $capturedPersona)) 'Write re-read drifted PersonaSource or accepted a mutated snapshot clone'
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $outOne 'brain\reference\persona\persona.md.goc'))) `
    'Fresh package copied the source-side .goc backup'
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $outOne 'app\defaults\persona\persona.md.goc'))) `
    'Immutable default contains a .goc backup'

  $manifestOnePath = Join-Path $outOne 'app\defaults\persona\build-manifest.json'
  $manifestTwoPath = Join-Path $outTwo 'app\defaults\persona\build-manifest.json'
  $manifestOneBytes = [IO.File]::ReadAllBytes($manifestOnePath)
  $manifestTwoBytes = [IO.File]::ReadAllBytes($manifestTwoPath)
  Assert-True ([Linq.Enumerable]::SequenceEqual([byte[]]$manifestOneBytes, [byte[]]$manifestTwoBytes)) `
    'Persona manifest is not deterministic'
  Assert-True ($manifestOneBytes.Length -gt 1 -and
      -not ($manifestOneBytes[0] -eq 0xef -and $manifestOneBytes[1] -eq 0xbb)) `
    'Persona manifest contains a UTF-8 BOM'
  $manifestText = $script:Utf8.GetString($manifestOneBytes)
  Assert-True ($manifestText.EndsWith("`n", [StringComparison]::Ordinal) -and
      -not $manifestText.Contains("`r")) 'Persona manifest is not canonical LF text'
  Assert-True ($manifestText.StartsWith('{"version":1,"files":[', [StringComparison]::Ordinal)) `
    'Persona manifest is not canonical compact JSON'
  Assert-Equal $manifestText (Get-TestCanonicalPersonaManifest `
      -Files $snapshotFiles -TreeSHA256 (Get-TestPersonaTreeSHA256 $snapshotFiles)) `
    'Persona manifest bytes differ from the independent canonical JSON contract'
  foreach ($identity in @('boizdeeptry', 'Anh Trường')) {
    Assert-True (-not $manifestText.Contains($identity)) `
      'Persona manifest repeated a declared identity literal'
  }
  $manifest = $manifestText | ConvertFrom-Json
  Assert-Equal $manifest.version 1 'Persona manifest version drifted'
  Assert-Equal $manifest.tree_sha256 $snapshot.TreeSHA256 'Persona manifest tree digest drifted'
  Assert-Equal @($manifest.legacy_persona_sha256).Count 1 'Legacy Persona hash count drifted'
  Assert-Equal @($manifest.legacy_persona_sha256)[0] $script:LegacyPersonaSHA256 `
    'Legacy Persona hash drifted'
  $manifestPaths = [string[]]@($manifest.files | ForEach-Object path)
  $expectedPaths = [string[]]@($snapshotFiles.Keys)
  [Array]::Sort($expectedPaths, [StringComparer]::Ordinal)
  Assert-Equal ($manifestPaths -join "`n") ($expectedPaths -join "`n") `
    'Persona manifest paths are not exact ordinal snapshot order'
  Assert-True (-not ($manifestPaths -contains 'build-manifest.json')) `
    'Persona manifest included itself in the tree'
  foreach ($entry in @($manifest.files)) {
    $expectedBytes = [byte[]]$snapshotFiles[[string]$entry.path]
    $expectedHash = [Convert]::ToHexString(
      [Security.Cryptography.SHA256]::HashData($expectedBytes)).ToLowerInvariant()
    Assert-Equal ([UInt64]$entry.bytes) ([UInt64]$expectedBytes.Length) `
      "Persona manifest byte length drifted for '$($entry.path)'"
    Assert-Equal ([string]$entry.sha256) $expectedHash `
      "Persona manifest digest drifted for '$($entry.path)'"
  }
  Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot

  $decomposedIdentity = 'Anh Trường'.Normalize([Text.NormalizationForm]::FormD)
  foreach ($pathLeakCase in @(
      @{
        Relative = 'app\boizdeeptry-invalid.bin'
        Cleanup = 'app\boizdeeptry-invalid.bin'
        Forbidden = 'boizdeeptry'
      },
      @{
        Relative = "data\$decomposedIdentity-private\invalid.bin"
        Cleanup = "data\$decomposedIdentity-private"
        Forbidden = $decomposedIdentity
      },
      @{
        Relative = 'launcher\ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E-private\invalid.bin'
        Cleanup = 'launcher\ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E-private'
        Forbidden = 'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E'
      },
      @{
        Relative = 'app\defaults\persona\boizdeeptry-invalid.bin'
        Cleanup = 'app\defaults\persona\boizdeeptry-invalid.bin'
        Forbidden = 'boizdeeptry'
      }
    )) {
    $pathLeak = Join-Path $outOne $pathLeakCase.Relative
    $pathLeakCleanup = Join-Path $outOne $pathLeakCase.Cleanup
    Write-TestBytes $pathLeak ([byte[]](0xff, 0xfe, 0x00))
    try {
      Assert-ThrowsLike -Action {
        Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
      } -Pattern 'persona|privacy|identity|sensitive' `
        -ForbiddenDetail $pathLeakCase.Forbidden `
        -Message "A private package entry path was accepted: '$($pathLeakCase.Relative)'"
    } finally {
      Remove-Item -LiteralPath $pathLeakCleanup -Recurse -Force
    }
  }

  foreach ($unexpectedDirectory in @(
      'brain\reference\persona\unexpected-empty',
      'app\defaults\persona\overlay\unexpected-empty'
    )) {
    $unexpectedDirectoryPath = Join-Path $outOne $unexpectedDirectory
    [IO.Directory]::CreateDirectory($unexpectedDirectoryPath) | Out-Null
    try {
      Assert-ThrowsLike -Action {
        Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
      } -Pattern 'persona|privacy|directory' -ForbiddenDetail 'unexpected-empty' `
        -Message "An unexpected empty Persona directory was accepted: '$unexpectedDirectory'"
    } finally {
      Remove-Item -LiteralPath $unexpectedDirectoryPath -Force
    }
  }

  $workingBackup = Join-Path $outOne 'brain\reference\persona\persona.md.goc'
  Copy-Item -LiteralPath (Join-Path $outOne 'app\defaults\persona\persona.md') `
    -Destination $workingBackup -Force
  Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
  Write-TestText $workingBackup "mismatch boizdeeptry`n"
  Assert-ThrowsLike -Action {
    Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
  } -Pattern 'persona|privacy' -ForbiddenDetail 'boizdeeptry' `
    -Message 'A mismatched working Persona backup passed final privacy scan'
  Remove-Item -LiteralPath $workingBackup -Force
  $wrongBackup = Join-Path $outOne 'app\defaults\persona\persona.md.goc'
  Copy-Item -LiteralPath (Join-Path $outOne 'app\defaults\persona\persona.md') `
    -Destination $wrongBackup -Force
  Assert-ThrowsLike -Action {
    Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
  } -Pattern 'persona|privacy' -Message 'A .goc outside the exact working backup path was accepted'
  Remove-Item -LiteralPath $wrongBackup -Force

  [IO.Directory]::CreateDirectory($workingBackup) | Out-Null
  Assert-ThrowsLike -Action {
    Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
  } -Pattern 'persona|privacy|reparse' `
    -Message 'A directory at the exact working Persona backup path was accepted'
  Remove-Item -LiteralPath $workingBackup -Force
  $backupTarget = Join-Path $validRoot 'backup-junction-target'
  [IO.Directory]::CreateDirectory($backupTarget) | Out-Null
  New-Item -ItemType Junction -Path $workingBackup -Target $backupTarget | Out-Null
  try {
    Assert-ThrowsLike -Action {
      Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
    } -Pattern 'persona|privacy|reparse' `
      -Message 'A reparse point at the exact working Persona backup path was accepted'
  } finally {
    Remove-Item -LiteralPath $workingBackup -Force
  }

  $allowedPersonaPath = Join-Path $outOne 'brain\reference\persona\persona.md'
  $allowedPersonaBytes = [IO.File]::ReadAllBytes($allowedPersonaPath)
  Write-TestBytes $allowedPersonaPath ($allowedPersonaBytes +
    $script:Utf8.GetBytes("`nONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91`n"))
  try {
    Assert-ThrowsLike -Action {
      Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
    } -Pattern 'persona|privacy|sensitive' `
      -ForbiddenDetail 'ONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91' `
      -Message 'A sensitive canary inside an allowed Persona destination was exempted'
  } finally {
    Write-TestBytes $allowedPersonaPath $allowedPersonaBytes
  }

  foreach ($leakCase in @(
      @{ Relative = 'app\agentdc.exe'; Value = 'boizdeeptry' },
      @{ Relative = 'data\private.bin'; Value = 'Anh Trường' },
      @{ Relative = 'data\logs\daemon.log'; Value = 'boizdeeptry' },
      @{ Relative = 'launcher\notes.txt'; Value = 'Anh Trường' },
      @{ Relative = 'assets\identity.js'; Value = 'boizdeeptry' },
      @{ Relative = 'brain\wiki\identity.md'; Value = 'Anh Trường' },
      @{ Relative = 'brain\reference\persona\notes.md'; Value = 'boizdeeptry' },
      @{ Relative = 'app\defaults\persona\notes.md'; Value = 'Anh Trường' },
      @{ Relative = 'brain\reference\persona\overlay\extra.md'; Value = 'boizdeeptry' },
      @{ Relative = 'README.txt'; Value = 'boizdeeptry' },
      @{ Relative = 'app\historical.bin'; Value = 'Bé Mi' },
      @{ Relative = 'app\identity-utf16le.bin'; Value = 'boizdeeptry'; Encoding = [Text.UnicodeEncoding]::new($false, $false) },
      @{ Relative = 'app\identity-utf16be.bin'; Value = 'Anh Trường'; Encoding = [Text.UnicodeEncoding]::new($true, $false) }
    )) {
    $leakPath = Join-Path $outOne $leakCase.Relative
    if ($leakCase.Encoding) {
      Write-TestBytes $leakPath $leakCase.Encoding.GetBytes($leakCase.Value)
    } else {
      Write-TestText $leakPath ($leakCase.Value + "`n")
    }
    try {
      Assert-ThrowsLike -Action {
        Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
      } -Pattern 'persona|privacy' -ForbiddenDetail $leakCase.Value `
        -Message "Identity leak in '$($leakCase.Relative)' was accepted"
    } finally {
      Remove-Item -LiteralPath $leakPath -Force
    }
  }
  foreach ($canary in @(
      'APP_TEST_SESSION_CANARY_CLEAR_18D1',
      'ONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91',
      'ONBOARDING_CREDENTIAL_CANARY_CLEAR_D912',
      'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E',
      'sk-package-must-never-contain-7f36d2'
    )) {
    $canaryPath = Join-Path $outOne 'app\canary.bin'
    Write-TestText $canaryPath ($canary + "`n")
    try {
      Assert-ThrowsLike -Action {
        Assert-AppPersonaPackagePrivacy -Out $outOne -Snapshot $snapshot
      } -Pattern 'persona|privacy|sensitive|credential' -ForbiddenDetail $canary `
        -Message 'An existing token/credential/session/config canary passed Persona privacy scan'
    } finally {
      Remove-Item -LiteralPath $canaryPath -Force
    }
  }
  Assert-Equal (Get-TestDirectoryFingerprint $source) $driftedSourceBeforeWrite `
    'Final Persona privacy scans changed the source'
  Write-Host 'PASS: immutable snapshot packaging, deterministic manifest and scoped privacy gates hold.'
} finally {
  if (Test-Path -LiteralPath $validRoot) {
    Remove-Item -LiteralPath $validRoot -Recurse -Force
  }
}

$buildScriptPath = Join-Path $PSScriptRoot '..\build-app.ps1'
$buildScript = [IO.File]::ReadAllText($buildScriptPath)
$snapshotIndex = $buildScript.IndexOf('Read-AppPersonaSourceSnapshot', [StringComparison]::Ordinal)
$stageIndex = $buildScript.IndexOf('New-AppStage', [StringComparison]::Ordinal)
$clearIndex = $buildScript.IndexOf('Clear-AppOutput', [StringComparison]::Ordinal)
$firstOutCreateIndex = $buildScript.IndexOf('[IO.Directory]::CreateDirectory((Join-Path $Out', [StringComparison]::Ordinal)
$writeIndex = $buildScript.IndexOf('Write-AppPersonaPackage', [StringComparison]::Ordinal)
$privacyIndex = $buildScript.IndexOf('Assert-AppPersonaPackagePrivacy', [StringComparison]::Ordinal)
$packageGateIndex = $buildScript.IndexOf('Assert-AppPackage -Out $Out', [StringComparison]::Ordinal)
$finalCleanGitIndex = $buildScript.LastIndexOf(
  'Assert-CleanGitSource -Repo $Repo', [StringComparison]::Ordinal)
$launcherCopyIndex = $buildScript.IndexOf("Join-Path `$PSScriptRoot 'launcher'", [StringComparison]::Ordinal)
if ($snapshotIndex -lt 0 -or $stageIndex -lt 0 -or $clearIndex -lt 0 -or
    $firstOutCreateIndex -lt 0 -or $writeIndex -lt 0 -or $privacyIndex -lt 0 -or
    $packageGateIndex -lt 0 -or $finalCleanGitIndex -lt 0 -or $launcherCopyIndex -lt 0 -or
    $snapshotIndex -ge $stageIndex -or $snapshotIndex -ge $clearIndex -or
    $snapshotIndex -ge $firstOutCreateIndex -or $writeIndex -le $clearIndex -or
    $launcherCopyIndex -ge $packageGateIndex -or $writeIndex -ge $packageGateIndex -or
    $packageGateIndex -ge $finalCleanGitIndex -or $finalCleanGitIndex -ge $privacyIndex) {
  throw 'build-app.ps1 does not finish executable package gates before its final Persona privacy scan'
}
if ($buildScript -match 'genpersona\.py') {
  throw 'build-app.ps1 still invokes the mutating genpersona.py path'
}
if ($buildScript -notmatch 'Read-AppPersonaSourceSnapshot[^\r\n]*-Out\s+\$Out') {
  throw 'build-app.ps1 Persona preflight is not bound to the physical output path'
}
if ($buildScript -notmatch 'Read-AppPersonaSourceSnapshot[\s\S]{0,300}-ProtectedRoot\s+@\(\$Repo,\s*\$PSScriptRoot\)') {
  throw 'build-app.ps1 Persona preflight does not protect Repo and packaging worktree roots'
}
$runBat = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '..\launcher\app\run.bat'))
$defaultDirLine = 'set "AGENTDC_ZALO_PERSONA_DEFAULT_DIR=%ROOT%\app\defaults\persona"'
Assert-Equal ([regex]::Matches($runBat, [regex]::Escape($defaultDirLine)).Count) 1 `
  'Launcher must set the exact immutable Persona default directory once'
$defaultDirIndex = $runBat.IndexOf($defaultDirLine, [StringComparison]::Ordinal)
$daemonIndex = $runBat.IndexOf('"%~dp0agentdc.exe" daemon', [StringComparison]::Ordinal)
if ($defaultDirIndex -lt 0 -or $daemonIndex -lt 0 -or $defaultDirIndex -ge $daemonIndex) {
  throw 'Launcher must set the immutable Persona default directory before starting the daemon'
}
Write-Host 'PASS: build ordering performs Persona preflight before the first output mutation.'

Write-Host 'All Persona build tests passed.'
