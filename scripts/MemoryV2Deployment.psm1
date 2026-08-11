Set-StrictMode -Version Latest

$script:CanaryManifestVersion = 'memory-v2-canary/v1'
$script:Sha256Pattern = '^[A-Fa-f0-9]{64}$'
$script:UnsafeCanaryFiles = @(
  'daemon.lock'
  'token'
  'zalo-transport.pid'
  'zalo-transport.log'
  'zalo\credentials.json'
)

function Get-MemoryV2CanonicalPath {
  param([Parameter(Mandatory)][string]$Path)
  return [IO.Path]::GetFullPath($Path)
}

function Test-MemoryV2PathWithin {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][string]$Root
  )
  $pathValue = (Get-MemoryV2CanonicalPath $Path).TrimEnd('\', '/')
  $rootValue = (Get-MemoryV2CanonicalPath $Root).TrimEnd('\', '/')
  if ($pathValue.Equals($rootValue, [StringComparison]::OrdinalIgnoreCase)) { return $true }
  return $pathValue.StartsWith(
    $rootValue + [IO.Path]::DirectorySeparatorChar,
    [StringComparison]::OrdinalIgnoreCase
  )
}

function Get-MemoryV2Sha256 {
  param([Parameter(Mandatory)][string]$Path)
  return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToUpperInvariant()
}

function Assert-MemoryV2Sha256 {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][string]$Expected,
    [Parameter(Mandatory)][string]$Label
  )
  if ($Expected -notmatch $script:Sha256Pattern) {
    throw "$Label expected SHA-256 is invalid"
  }
  if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
    throw "$Label is missing: $Path"
  }
  $actual = Get-MemoryV2Sha256 $Path
  if (-not $actual.Equals($Expected, [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Label SHA-256 mismatch: expected $Expected, got $actual"
  }
  return $actual
}

function Copy-MemoryV2DirectoryContents {
  param(
    [Parameter(Mandatory)][string]$Source,
    [Parameter(Mandatory)][string]$Destination
  )
  [IO.Directory]::CreateDirectory($Destination) | Out-Null
  foreach ($item in Get-ChildItem -LiteralPath $Source -Force) {
    Copy-Item -LiteralPath $item.FullName -Destination $Destination -Recurse -Force
  }
}

function Initialize-MemoryV2CanaryHome {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$SourceHome,
    [Parameter(Mandatory)][string]$CanaryHome,
    [Parameter(Mandatory)][string]$LiveRoot
  )

  $sourcePath = Get-MemoryV2CanonicalPath $SourceHome
  $canaryPath = Get-MemoryV2CanonicalPath $CanaryHome
  $livePath = Get-MemoryV2CanonicalPath $LiveRoot
  if (-not (Test-Path -LiteralPath $sourcePath -PathType Container)) {
    throw "source home is missing: $sourcePath"
  }
  $sourceIsLiveRoot = $sourcePath.Equals($livePath, [StringComparison]::OrdinalIgnoreCase)
  $sourceIsActiveLive = $sourceIsLiveRoot -or
    (Test-MemoryV2PathWithin -Path $sourcePath -Root (Join-Path $livePath 'app')) -or
    (Test-MemoryV2PathWithin -Path $sourcePath -Root (Join-Path $livePath 'data'))
  if ($sourceIsActiveLive) {
    throw 'canary source must be a retained backup, not the active live root/app/data'
  }
  if ($sourcePath.Equals($canaryPath, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'source and canary home must be different'
  }
  if (Test-MemoryV2PathWithin -Path $canaryPath -Root $livePath) {
    throw "canary home must be outside live root '$livePath'"
  }
  if (Test-Path -LiteralPath $canaryPath) {
    throw "canary home already exists: $canaryPath"
  }
  $parent = Split-Path -Parent $canaryPath
  [IO.Directory]::CreateDirectory($parent) | Out-Null
  $resolvedParent = (Resolve-Path -LiteralPath $parent).Path
  $canaryPath = Join-Path $resolvedParent (Split-Path -Leaf $canaryPath)
  if (Test-MemoryV2PathWithin -Path $canaryPath -Root $livePath) {
    throw "resolved canary home must be outside live root '$livePath'"
  }

  Copy-MemoryV2DirectoryContents -Source $sourcePath -Destination $canaryPath
  $removed = [Collections.Generic.List[string]]::new()
  foreach ($relative in $script:UnsafeCanaryFiles) {
    $target = Join-Path $canaryPath $relative
    if (Test-Path -LiteralPath $target) {
      Remove-Item -LiteralPath $target -Force
      $removed.Add($relative)
    }
  }
  $credentialFiles = @(Get-ChildItem -LiteralPath $canaryPath -Recurse -Force -File |
      Where-Object Name -CEQ 'credentials.json')
  if ($credentialFiles.Count -ne 0) {
    throw 'canary isolation failed: credentials.json remains in copied home'
  }
  foreach ($relative in @('daemon.lock', 'token', 'zalo-transport.pid')) {
    if (Test-Path -LiteralPath (Join-Path $canaryPath $relative)) {
      throw "canary isolation failed: runtime control remains: $relative"
    }
  }
  $sourceDB = Join-Path $sourcePath 'agentdc.db'
  $copyDB = Join-Path $canaryPath 'agentdc.db'
  if (-not (Test-Path -LiteralPath $sourceDB -PathType Leaf) -or
      -not (Test-Path -LiteralPath $copyDB -PathType Leaf)) {
    throw 'source or copied canary database is missing'
  }
  $sourceHash = Get-MemoryV2Sha256 $sourceDB
  $copyHash = Get-MemoryV2Sha256 $copyDB
  if ($sourceHash -ne $copyHash) {
    throw "canary database copy SHA-256 mismatch: source=$sourceHash copy=$copyHash"
  }
  return [PSCustomObject]@{
    SourceHome = $sourcePath
    CanaryHome = $canaryPath
    SourceDB = $sourceDB
    CopyDB = $copyDB
    DBHash = $copyHash
    Removed = @($removed)
  }
}

function Get-MemoryV2RequiredProperty {
  param(
    [Parameter(Mandatory)]$Object,
    [Parameter(Mandatory)][string]$Name,
    [Parameter(Mandatory)][string]$Context
  )
  if ($null -eq $Object) { throw "$Context is missing" }
  $property = $Object.PSObject.Properties[$Name]
  if ($null -eq $property) { throw "$Context.$Name is missing" }
  return $property.Value
}

function Assert-MemoryV2ManifestRedacted {
  param([Parameter(Mandatory)]$Value, [string]$Context = 'manifest')
  if ($null -eq $Value) { return }
  if ($Value -is [string] -or $Value -is [ValueType]) { return }
  if ($Value -is [Collections.IDictionary]) {
    foreach ($key in $Value.Keys) {
      if ([string]$key -match '^(token|credential|credentials|secret|authorization|cookie|password|bearer)$') {
        throw "$Context contains a sensitive field '$key'; redact the manifest"
      }
      Assert-MemoryV2ManifestRedacted -Value $Value[$key] -Context "$Context.$key"
    }
    return
  }
  if ($Value -is [Collections.IEnumerable]) {
    foreach ($item in $Value) { Assert-MemoryV2ManifestRedacted -Value $item -Context $Context }
    return
  }
  foreach ($property in $Value.PSObject.Properties) {
    if ($property.Name -match '^(token|credential|credentials|secret|authorization|cookie|password|bearer)$') {
      throw "$Context contains a sensitive field '$($property.Name)'; redact the manifest"
    }
    Assert-MemoryV2ManifestRedacted -Value $property.Value -Context "$Context.$($property.Name)"
  }
}

function Assert-MemoryV2HashField {
  param([Parameter(Mandatory)]$Value, [Parameter(Mandatory)][string]$Label)
  if ([string]$Value -notmatch $script:Sha256Pattern) {
    throw "$Label evidence SHA-256 is missing or invalid"
  }
}

function Test-MemoryV2CanaryManifest {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$ManifestPath,
    [Parameter(Mandatory)][string]$CandidatePath,
    [Parameter(Mandatory)][string]$ApprovedCandidateHash,
    [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedLiveSchema,
    [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedTargetSchema,
    [ValidateRange(1, 1440)][int]$MaxAgeMinutes = 60
  )

  $manifestFile = Get-MemoryV2CanonicalPath $ManifestPath
  $candidateFile = Get-MemoryV2CanonicalPath $CandidatePath
  if (-not (Test-Path -LiteralPath $manifestFile -PathType Leaf)) {
    throw "canary manifest is missing: $manifestFile"
  }
  if (-not (Test-Path -LiteralPath $candidateFile -PathType Leaf)) {
    throw "candidate is missing: $candidateFile"
  }
  try {
    $manifest = Get-Content -LiteralPath $manifestFile -Raw | ConvertFrom-Json -Depth 20
  } catch {
    throw "cannot parse canary manifest JSON: $($_.Exception.Message)"
  }
  Assert-MemoryV2ManifestRedacted -Value $manifest

  $version = Get-MemoryV2RequiredProperty $manifest 'version' 'manifest'
  if ($version -ne $script:CanaryManifestVersion) {
    throw "unsupported canary manifest version '$version'"
  }
  $status = Get-MemoryV2RequiredProperty $manifest 'status' 'manifest'
  if ($status -ne 'PASS') { throw "canary manifest status must be PASS, got '$status'" }
  $generatedRaw = Get-MemoryV2RequiredProperty $manifest 'generatedUtc' 'manifest'
  $generated = [DateTimeOffset]::MinValue
  if (-not [DateTimeOffset]::TryParse(
      [string]$generatedRaw,
      [Globalization.CultureInfo]::InvariantCulture,
      [Globalization.DateTimeStyles]::AssumeUniversal,
      [ref]$generated
    )) {
    throw 'canary manifest generatedUtc is invalid'
  }
  $now = [DateTimeOffset]::UtcNow
  if ($generated -lt $now.AddMinutes(-$MaxAgeMinutes) -or $generated -gt $now.AddMinutes(5)) {
    throw "canary manifest is not fresh (generatedUtc=$generatedRaw)"
  }
  $port = [int](Get-MemoryV2RequiredProperty $manifest 'port' 'manifest')
  if ($port -lt 1 -or $port -gt 65535) { throw 'canary manifest port is invalid' }

  $candidate = Get-MemoryV2RequiredProperty $manifest 'candidate' 'manifest'
  $manifestCandidatePath = Get-MemoryV2CanonicalPath (
    Get-MemoryV2RequiredProperty $candidate 'path' 'manifest.candidate'
  )
  if (-not $manifestCandidatePath.Equals($candidateFile, [StringComparison]::OrdinalIgnoreCase)) {
    throw "canary candidate path mismatch: manifest=$manifestCandidatePath requested=$candidateFile"
  }
  Assert-MemoryV2HashField $ApprovedCandidateHash 'approved candidate'
  $manifestHash = [string](Get-MemoryV2RequiredProperty $candidate 'sha256' 'manifest.candidate')
  Assert-MemoryV2HashField $manifestHash 'manifest candidate'
  if (-not $manifestHash.Equals($ApprovedCandidateHash, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'canary candidate SHA-256 does not match the approved SHA-256'
  }
  Assert-MemoryV2Sha256 -Path $candidateFile -Expected $ApprovedCandidateHash -Label 'candidate' | Out-Null

  $source = Get-MemoryV2RequiredProperty $manifest 'source' 'manifest'
  $sourceSchema = [int](Get-MemoryV2RequiredProperty $source 'schema' 'manifest.source')
  $sourceQuick = [string](Get-MemoryV2RequiredProperty $source 'quickCheck' 'manifest.source')
  if ($sourceSchema -ne $ExpectedLiveSchema -or $sourceQuick -ne 'ok') {
    throw "source must have schema $ExpectedLiveSchema and quick_check=ok"
  }
  $sourceHash = [string](Get-MemoryV2RequiredProperty $source 'dbSha256' 'manifest.source')
  Assert-MemoryV2HashField $sourceHash 'source DB'

  $copy = Get-MemoryV2RequiredProperty $manifest 'copy' 'manifest'
  $copyBeforeHash = [string](Get-MemoryV2RequiredProperty $copy 'dbSha256Before' 'manifest.copy')
  Assert-MemoryV2HashField $copyBeforeHash 'copied DB before start'
  if ($copyBeforeHash -ne $sourceHash) { throw 'source/copy DB SHA-256 mismatch before canary start' }
  if ([int](Get-MemoryV2RequiredProperty $copy 'schemaBefore' 'manifest.copy') -ne $ExpectedLiveSchema -or
      [string](Get-MemoryV2RequiredProperty $copy 'quickCheckBefore' 'manifest.copy') -ne 'ok') {
    throw "copied canary must start at schema $ExpectedLiveSchema with quick_check=ok"
  }
  if ([int](Get-MemoryV2RequiredProperty $copy 'schemaMigrated' 'manifest.copy') -ne $ExpectedTargetSchema -or
      [string](Get-MemoryV2RequiredProperty $copy 'quickCheckMigrated' 'manifest.copy') -ne 'ok') {
    throw "canary migration must reach schema $ExpectedTargetSchema with quick_check=ok"
  }
  if ([int](Get-MemoryV2RequiredProperty $copy 'schemaFinal' 'manifest.copy') -ne $ExpectedTargetSchema -or
      [string](Get-MemoryV2RequiredProperty $copy 'quickCheckFinal' 'manifest.copy') -ne 'ok') {
    throw "final canary quick_check/schema must be ok/$ExpectedTargetSchema"
  }
  Assert-MemoryV2HashField (
    Get-MemoryV2RequiredProperty $copy 'dbSha256Final' 'manifest.copy'
  ) 'final canary DB'

  $journeys = Get-MemoryV2RequiredProperty $manifest 'journeys' 'manifest'
  foreach ($name in 'J1', 'J2', 'J3', 'J4', 'J5', 'J6') {
    if ((Get-MemoryV2RequiredProperty $journeys $name 'manifest.journeys') -ne $true) {
      throw "canary journey $name must be true"
    }
  }
  $isolation = Get-MemoryV2RequiredProperty $manifest 'isolation' 'manifest'
  foreach ($name in @(
      'outsideLiveRoot', 'credentialsAbsent', 'transportEnvironmentCleared',
      'runtimeControlsRemoved', 'portInitiallyFree', 'candidatePathExact'
    )) {
    if ((Get-MemoryV2RequiredProperty $isolation $name 'manifest.isolation') -ne $true) {
      throw "canary isolation assertion $name must be true"
    }
  }
  $stopped = Get-MemoryV2RequiredProperty $manifest 'stopped' 'manifest'
  if ((Get-MemoryV2RequiredProperty $stopped 'processExited' 'manifest.stopped') -ne $true) {
    throw 'canary stopped.processExited must be true'
  }
  if ((Get-MemoryV2RequiredProperty $stopped 'listenerAbsent' 'manifest.stopped') -ne $true) {
    throw 'canary stopped.listenerAbsent must be true'
  }

  $evidence = Get-MemoryV2RequiredProperty $manifest 'evidence' 'manifest'
  foreach ($name in @(
      'transcriptSha256', 'sourceBaselineSha256', 'migrationComparisonSha256', 'finalDbSha256'
    )) {
    Assert-MemoryV2HashField (
      Get-MemoryV2RequiredProperty $evidence $name 'manifest.evidence'
    ) "manifest evidence $name"
  }
  $transcriptPath = Get-MemoryV2CanonicalPath (
    Get-MemoryV2RequiredProperty $evidence 'transcriptPath' 'manifest.evidence'
  )
  $transcriptHash = [string](Get-MemoryV2RequiredProperty $evidence 'transcriptSha256' 'manifest.evidence')
  Assert-MemoryV2Sha256 -Path $transcriptPath -Expected $transcriptHash -Label 'canary transcript' | Out-Null
  try {
    $transcript = Get-Content -LiteralPath $transcriptPath -Raw | ConvertFrom-Json -Depth 20
  } catch {
    throw "cannot parse canary transcript JSON: $($_.Exception.Message)"
  }
  Assert-MemoryV2ManifestRedacted -Value $transcript -Context 'transcript'
  if ((Get-MemoryV2RequiredProperty $transcript 'version' 'transcript') -ne
      'memory-v2-canary-transcript/v1') {
    throw 'unsupported canary transcript version'
  }
  if ((Get-MemoryV2RequiredProperty $transcript 'status' 'transcript') -ne 'PASS') {
    throw 'canary transcript status must be PASS'
  }
  if ([int](Get-MemoryV2RequiredProperty $transcript 'port' 'transcript') -ne $port) {
    throw 'canary transcript port does not match manifest port'
  }
  $transcriptGeneratedRaw = Get-MemoryV2RequiredProperty $transcript 'generatedUtc' 'transcript'
  $transcriptGenerated = [DateTimeOffset]::MinValue
  if (-not [DateTimeOffset]::TryParse(
      [string]$transcriptGeneratedRaw,
      [Globalization.CultureInfo]::InvariantCulture,
      [Globalization.DateTimeStyles]::AssumeUniversal,
      [ref]$transcriptGenerated
    )) {
    throw 'canary transcript generatedUtc is invalid'
  }
  if ($transcriptGenerated -lt $now.AddMinutes(-$MaxAgeMinutes) -or
      $transcriptGenerated -gt $now.AddMinutes(5)) {
    throw "canary transcript is not fresh (generatedUtc=$transcriptGeneratedRaw)"
  }
  if ($transcriptGenerated -gt $generated.AddSeconds(5) -or
      $transcriptGenerated -lt $generated.AddMinutes(-30)) {
    throw 'canary transcript freshness is not bound to the manifest run'
  }
  $events = @(Get-MemoryV2RequiredProperty $transcript 'events' 'transcript')
  if ($events.Count -eq 0) { throw 'canary transcript has no events' }
  $seenEvents = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
  foreach ($event in $events) {
    $eventName = [string](Get-MemoryV2RequiredProperty $event 'name' 'transcript.events')
    $eventStatus = [string](Get-MemoryV2RequiredProperty $event 'status' "transcript.events.$eventName")
    if ($eventStatus -ne 'PASS') { throw "canary transcript event $eventName must be PASS" }
    if (-not $seenEvents.Add($eventName)) { throw "duplicate canary transcript event $eventName" }
    $eventUtcRaw = Get-MemoryV2RequiredProperty $event 'utc' "transcript.events.$eventName"
    $eventUtc = [DateTimeOffset]::MinValue
    if (-not [DateTimeOffset]::TryParse(
        [string]$eventUtcRaw,
        [Globalization.CultureInfo]::InvariantCulture,
        [Globalization.DateTimeStyles]::AssumeUniversal,
        [ref]$eventUtc
      )) {
      throw "canary transcript event $eventName has invalid utc"
    }
    if ($eventUtc -lt $now.AddMinutes(-$MaxAgeMinutes) -or $eventUtc -gt $transcriptGenerated.AddSeconds(5)) {
      throw "canary transcript event $eventName is outside the fresh run window"
    }
  }
  foreach ($requiredEvent in @(
      'copy-and-scrub', 'isolation-before-start', 'migration-start', 'migration-stop',
      'fake-fixtures', 'J1', 'J2', 'J3', 'J4', 'J5', 'J6', 'canary-stop', 'final-evidence'
    )) {
    if (-not $seenEvents.Contains($requiredEvent)) {
      throw "canary transcript required event $requiredEvent is missing"
    }
  }
  $sourceEventCount = [int]$seenEvents.Contains('source') + [int]$seenEvents.Contains('source-v3')
  if ($sourceEventCount -ne 1) { throw 'canary transcript requires exactly one source event' }
  $finalHash = [string](Get-MemoryV2RequiredProperty $copy 'dbSha256Final' 'manifest.copy')
  $evidenceFinalHash = [string](Get-MemoryV2RequiredProperty $evidence 'finalDbSha256' 'manifest.evidence')
  if ($finalHash -ne $evidenceFinalHash) { throw 'final DB evidence SHA-256 mismatch' }

  return $manifest
}

function Get-MemoryV2SanitizedEnvironment {
  param(
    [Parameter(Mandatory)][string]$DataHome,
    [Parameter(Mandatory)][int]$Port
  )
  $environment = [Collections.Generic.Dictionary[string,string]]::new(
    [StringComparer]::OrdinalIgnoreCase
  )
  $cleared = [Collections.Generic.List[string]]::new()
  foreach ($entry in Get-ChildItem Env:) {
    if ($entry.Name -like 'AGENTDC_ZALO_*' -or $entry.Name -like 'ZALO_*' -or
        $entry.Name -in @('AGENTDC_TOKEN', 'AGENTDC_URL', 'AGENTDC_PORTAL_OPEN')) {
      $cleared.Add($entry.Name)
      continue
    }
    $environment[$entry.Name] = [string]$entry.Value
  }
  $environment['AGENTDC_HOME'] = (Get-MemoryV2CanonicalPath $DataHome)
  $environment['AGENTDC_PORT'] = [string]$Port
  $environment['AGENTDC_PORTAL_OPEN'] = '0'
  return [PSCustomObject]@{ Values = $environment; ClearedNames = @($cleared) }
}

function New-MemoryV2ProcessStartInfo {
  param(
    [Parameter(Mandatory)][string]$FilePath,
    [Parameter(Mandatory)][string[]]$Arguments,
    [Parameter(Mandatory)][Collections.Generic.Dictionary[string,string]]$Environment,
    [Parameter(Mandatory)][string]$WorkingDirectory,
    [string]$StdoutPath,
    [string]$StderrPath
  )
  $info = [Diagnostics.ProcessStartInfo]::new()
  $info.FileName = $FilePath
  $info.UseShellExecute = $false
  $info.CreateNoWindow = $true
  $info.WorkingDirectory = $WorkingDirectory
  foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
  $info.Environment.Clear()
  foreach ($key in $Environment.Keys) { $info.Environment[$key] = $Environment[$key] }
  if ($StdoutPath) { $info.RedirectStandardOutput = $true }
  if ($StderrPath) { $info.RedirectStandardError = $true }
  return [PSCustomObject]@{ Info = $info; StdoutPath = $StdoutPath; StderrPath = $StderrPath }
}

function Start-MemoryV2Process {
  param(
    [Parameter(Mandatory)][string]$FilePath,
    [Parameter(Mandatory)][string[]]$Arguments,
    [Parameter(Mandatory)][Collections.Generic.Dictionary[string,string]]$Environment,
    [Parameter(Mandatory)][string]$WorkingDirectory,
    [string]$StdoutPath,
    [string]$StderrPath
  )
  $start = New-MemoryV2ProcessStartInfo @PSBoundParameters
  $process = [Diagnostics.Process]::new()
  $process.StartInfo = $start.Info
  if (-not $process.Start()) { throw "could not start native process '$FilePath'" }
  if ($StdoutPath) { $process.BeginOutputReadLine() }
  if ($StderrPath) { $process.BeginErrorReadLine() }
  return $process
}

function Invoke-MemoryV2NativeChecked {
  param(
    [Parameter(Mandatory)][string]$Label,
    [Parameter(Mandatory)][string]$FilePath,
    [Parameter(Mandatory)][string[]]$Arguments,
    [Parameter(Mandatory)][Collections.Generic.Dictionary[string,string]]$Environment,
    [Parameter(Mandatory)][string]$WorkingDirectory,
    [ValidateRange(1, 300)][int]$TimeoutSeconds = 30
  )
  $info = [Diagnostics.ProcessStartInfo]::new()
  $info.FileName = $FilePath
  $info.UseShellExecute = $false
  $info.CreateNoWindow = $true
  $info.RedirectStandardOutput = $true
  $info.RedirectStandardError = $true
  $info.WorkingDirectory = $WorkingDirectory
  foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
  $info.Environment.Clear()
  foreach ($key in $Environment.Keys) { $info.Environment[$key] = $Environment[$key] }
  $process = [Diagnostics.Process]::new()
  $process.StartInfo = $info
  if (-not $process.Start()) { throw "$Label failed to start" }
  $stdoutTask = $process.StandardOutput.ReadToEndAsync()
  $stderrTask = $process.StandardError.ReadToEndAsync()
  if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
    try { $process.Kill($true) } catch { }
    throw "$Label timed out after $TimeoutSeconds seconds"
  }
  $stdout = $stdoutTask.GetAwaiter().GetResult()
  $null = $stderrTask.GetAwaiter().GetResult()
  if ($process.ExitCode -ne 0) {
    throw "$Label failed with exit $($process.ExitCode); native output was redacted"
  }
  return $stdout
}

function Invoke-MemoryV2SQLiteTool {
  param(
    [Parameter(Mandatory)][string]$PythonPath,
    [Parameter(Mandatory)][string[]]$Arguments,
    [Parameter(Mandatory)][string]$Label
  )
  $helper = Join-Path $PSScriptRoot 'memory_v2_sqlite.py'
  if (-not (Test-Path -LiteralPath $helper -PathType Leaf)) { throw "SQLite helper is missing: $helper" }
  $environment = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::OrdinalIgnoreCase)
  foreach ($entry in Get-ChildItem Env:) { $environment[$entry.Name] = [string]$entry.Value }
  $environment['PYTHONIOENCODING'] = 'utf-8'
  $nativeArguments = @($helper) + @($Arguments)
  $stdout = Invoke-MemoryV2NativeChecked -Label $Label -FilePath $PythonPath `
    -Arguments $nativeArguments -Environment $environment -WorkingDirectory $PSScriptRoot
  try { return $stdout | ConvertFrom-Json -Depth 20 } catch { throw "$Label returned invalid JSON" }
}

function Get-MemoryV2Operation {
  param(
    [Parameter(Mandatory)][Collections.IDictionary]$Operations,
    [Parameter(Mandatory)][string]$Name
  )
  if (-not $Operations.Contains($Name) -or $Operations[$Name] -isnot [scriptblock]) {
    throw "deployment operation '$Name' is missing"
  }
  return [scriptblock]$Operations[$Name]
}

function Wait-MemoryV2ProcessStopped {
  param(
    [Parameter(Mandatory)][string]$ExecutablePath,
    [Parameter(Mandatory)][int]$Port,
    [ValidateRange(1, 120)][int]$TimeoutSeconds = 20
  )
  $expected = Get-MemoryV2CanonicalPath $ExecutablePath
  $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
  do {
    $matching = @(Get-CimInstance Win32_Process -Filter "Name='agentdc.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and
          (Get-MemoryV2CanonicalPath $_.ExecutablePath).Equals($expected, [StringComparison]::OrdinalIgnoreCase) })
    $listeners = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
    if ($matching.Count -eq 0 -and $listeners.Count -eq 0) { return }
    Start-Sleep -Milliseconds 100
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "process/listener did not stop within $TimeoutSeconds seconds for '$expected' port $Port"
}

function Assert-MemoryV2BoundedHealth {
  param(
    [Parameter(Mandatory)][string]$ExecutablePath,
    [Parameter(Mandatory)][string]$ExpectedHash,
    [Parameter(Mandatory)][int]$Port,
    [ValidateRange(1, 120)][int]$TimeoutSeconds = 30
  )
  $expectedPath = Get-MemoryV2CanonicalPath $ExecutablePath
  $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
  $lastError = 'not started'
  do {
    try {
      Assert-MemoryV2Sha256 -Path $expectedPath -Expected $ExpectedHash -Label 'installed binary' | Out-Null
      $listeners = @(Get-NetTCPConnection -LocalAddress '127.0.0.1' -LocalPort $Port `
          -State Listen -ErrorAction Stop)
      if ($listeners.Count -ne 1) { throw "expected one loopback listener, got $($listeners.Count)" }
      $pidValue = [int]$listeners[0].OwningProcess
      $process = Get-Process -Id $pidValue -ErrorAction Stop
      $actualPath = Get-MemoryV2CanonicalPath $process.Path
      if (-not $actualPath.Equals($expectedPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw "listener process path mismatch: expected $expectedPath, got $actualPath"
      }
      $response = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/status" `
        -UseBasicParsing -TimeoutSec 3
      if ($response.StatusCode -ne 200) { throw "status HTTP $($response.StatusCode)" }
      return [PSCustomObject]@{
        ProcessID = $pidValue
        ProcessPath = $actualPath
        Listener = $true
        HTTPStatus = 200
      }
    } catch {
      $lastError = $_.Exception.Message
      Start-Sleep -Milliseconds 150
    }
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "bounded health failed for '$expectedPath' port $Port`: $lastError"
}

function New-MemoryV2DeploymentOperations {
  param([Parameter(Mandatory)][string]$PythonPath)
  $python = $PythonPath
  # GetNewClosure creates a dynamic module. Capture the original module-bound
  # helper scriptblocks explicitly so private helper resolution survives that boundary.
  $sqliteTool = ${function:Invoke-MemoryV2SQLiteTool}
  $sanitizedEnvironment = ${function:Get-MemoryV2SanitizedEnvironment}
  $nativeChecked = ${function:Invoke-MemoryV2NativeChecked}
  $processStopped = ${function:Wait-MemoryV2ProcessStopped}
  $boundedHealth = ${function:Assert-MemoryV2BoundedHealth}
  $operations = @{
    InspectLive = {
      param($db, $expectedSchema)
      & $sqliteTool -PythonPath $python -Label 'pre-stop live DB verification' `
        -Arguments @('inspect-live', '--db', $db, '--expected-schema', [string]$expectedSchema)
    }.GetNewClosure()
    StopLive = {
      param($binary, $data, $port)
      $sanitized = & $sanitizedEnvironment -DataHome $data -Port $port
      & $nativeChecked -Label 'live daemon stop' -FilePath $binary `
        -Arguments @('daemon', 'stop', '--force') -Environment $sanitized.Values `
        -WorkingDirectory (Split-Path -Parent $binary) | Out-Null
    }.GetNewClosure()
    WaitStopped = {
      param($binary, $port)
      & $processStopped -ExecutablePath $binary -Port $port -TimeoutSeconds 30
    }.GetNewClosure()
    InspectBackup = {
      param($db, $expectedSchema)
      & $sqliteTool -PythonPath $python -Label 'immutable backup verification' `
        -Arguments @('inspect', '--db', $db, '--expected-schema', [string]$expectedSchema)
    }.GetNewClosure()
    StartLive = {
      param($scriptPath)
      $environment = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::OrdinalIgnoreCase)
      foreach ($entry in Get-ChildItem Env:) { $environment[$entry.Name] = [string]$entry.Value }
      $wscript = (Get-Command wscript.exe -CommandType Application -ErrorAction Stop).Source
      & $nativeChecked -Label 'live daemon start' -FilePath $wscript `
        -Arguments @('//B', '//Nologo', $scriptPath) -Environment $environment `
        -WorkingDirectory (Split-Path -Parent $scriptPath) -TimeoutSeconds 30 | Out-Null
    }.GetNewClosure()
    AssertPostStart = {
      param($binary, $hash, $data, $port, $expectedSchema)
      $health = & $boundedHealth -ExecutablePath $binary -ExpectedHash $hash `
        -Port $port -TimeoutSeconds 30
      $db = Join-Path $data 'agentdc.db'
      $inspection = & $sqliteTool -PythonPath $python -Label 'post-start live DB verification' `
        -Arguments @('inspect-live', '--db', $db, '--expected-schema', [string]$expectedSchema)
      if ($inspection.quickCheck -ne 'ok' -or [int]$inspection.schema -ne $expectedSchema) {
        throw 'post-start live DB quick_check/schema failed'
      }
      return [PSCustomObject]@{
        processPath = $health.ProcessPath
        listener = $health.Listener
        httpStatus = $health.HTTPStatus
        quickCheck = $inspection.quickCheck
        schema = $inspection.schema
      }
    }.GetNewClosure()
    AssertRollbackHealth = {
      param($binary, $hash, $data, $port)
      & $boundedHealth -ExecutablePath $binary -ExpectedHash $hash `
        -Port $port -TimeoutSeconds 30
    }.GetNewClosure()
  }
  return $operations
}

function Invoke-MemoryV2Deployment {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$ManifestPath,
    [Parameter(Mandatory)][string]$CandidatePath,
    [Parameter(Mandatory)][string]$ApprovedCandidateHash,
    [Parameter(Mandatory)][string]$LiveRoot,
    [Parameter(Mandatory)][string]$BackupRoot,
    [Parameter(Mandatory)][ValidateRange(1, 65535)][int]$Port,
    [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedLiveSchema,
    [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedTargetSchema,
    [string]$PythonPath = 'python',
    [Collections.IDictionary]$Operations
  )

  # This validation is deliberately the first operation. No staging, stop, copy,
  # or other live mutation is permitted before the mechanical canary gate passes.
  $null = Test-MemoryV2CanaryManifest -ManifestPath $ManifestPath `
    -CandidatePath $CandidatePath -ApprovedCandidateHash $ApprovedCandidateHash `
    -ExpectedLiveSchema $ExpectedLiveSchema -ExpectedTargetSchema $ExpectedTargetSchema

  $candidate = Get-MemoryV2CanonicalPath $CandidatePath
  $live = Get-MemoryV2CanonicalPath $LiveRoot
  $backupBase = Get-MemoryV2CanonicalPath $BackupRoot
  $liveApp = Join-Path $live 'app'
  $liveData = Join-Path $live 'data'
  $liveBinary = Join-Path $liveApp 'agentdc.exe'
  $liveDB = Join-Path $liveData 'agentdc.db'
  $startScript = Join-Path $live 'Start.vbs'
  foreach ($path in @($candidate, $liveBinary, $liveDB, $startScript)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "required deployment file is missing: $path" }
  }
  if ((Test-MemoryV2PathWithin -Path $candidate -Root $liveApp) -or
      (Test-MemoryV2PathWithin -Path $candidate -Root $liveData)) {
    throw 'candidate must be outside the live app and data directories'
  }
  if ((Test-MemoryV2PathWithin -Path $backupBase -Root $liveApp) -or
      (Test-MemoryV2PathWithin -Path $backupBase -Root $liveData)) {
    throw 'backup root must be outside the live app and data directories'
  }
  Assert-MemoryV2Sha256 -Path $candidate -Expected $ApprovedCandidateHash -Label 'candidate' | Out-Null
  if ($null -eq $Operations) { $Operations = New-MemoryV2DeploymentOperations -PythonPath $PythonPath }

  $inspectLive = Get-MemoryV2Operation $Operations 'InspectLive'
  $stopLive = Get-MemoryV2Operation $Operations 'StopLive'
  $waitStopped = Get-MemoryV2Operation $Operations 'WaitStopped'
  $inspectBackup = Get-MemoryV2Operation $Operations 'InspectBackup'
  $startLive = Get-MemoryV2Operation $Operations 'StartLive'
  $assertPostStart = Get-MemoryV2Operation $Operations 'AssertPostStart'
  $assertRollbackHealth = Get-MemoryV2Operation $Operations 'AssertRollbackHealth'

  $staged = Join-Path $liveApp 'agentdc.memory-v2.staged.exe'
  $rollbackStage = Join-Path $liveApp 'agentdc.memory-v2.rollback.exe'
  $replaceBackup = Join-Path $liveApp 'agentdc.memory-v2.replaced-old.exe'
  $failedCandidate = Join-Path $liveApp 'agentdc.memory-v2.failed-candidate.exe'
  foreach ($path in @($staged, $rollbackStage, $replaceBackup, $failedCandidate)) {
    if (Test-Path -LiteralPath $path) { throw "deployment recovery path collision: $path" }
  }
  $oldHash = Get-MemoryV2Sha256 $liveBinary
  $liveInspection = & $inspectLive $liveDB $ExpectedLiveSchema
  if ($liveInspection.quickCheck -ne 'ok' -or
      [int]$liveInspection.schema -ne $ExpectedLiveSchema) {
    throw "preflight live DB must have quick_check=ok and schema $ExpectedLiveSchema"
  }
  [IO.File]::Copy($candidate, $staged, $false)
  Assert-MemoryV2Sha256 -Path $staged -Expected $ApprovedCandidateHash -Label 'staged candidate' | Out-Null

  $stopped = $false
  $stopAttempted = $false
  $mutationAttempted = $false
  $newStartAttempted = $false
  $backupDirectory = ''
  try {
    $stopAttempted = $true
    & $stopLive $liveBinary $liveData $Port
    & $waitStopped $liveBinary $Port
    $stopped = $true

    [IO.Directory]::CreateDirectory($backupBase) | Out-Null
    $stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
    $backupDirectory = Join-Path $backupBase "memory-v2-$stamp"
    [IO.Directory]::CreateDirectory($backupDirectory) | Out-Null
    $backupBinary = Join-Path $backupDirectory 'agentdc.exe'
    $backupData = Join-Path $backupDirectory 'data'
    [IO.File]::Copy($liveBinary, $backupBinary, $false)
    Copy-MemoryV2DirectoryContents -Source $liveData -Destination $backupData
    Assert-MemoryV2Sha256 -Path $backupBinary -Expected $oldHash -Label 'backup old binary' | Out-Null
    $backupInspection = & $inspectBackup (Join-Path $backupData 'agentdc.db') $ExpectedLiveSchema
    if ($backupInspection.quickCheck -ne 'ok' -or
        [int]$backupInspection.schema -ne $ExpectedLiveSchema -or
        $backupInspection.baselineStable -ne $true) {
      throw "immutable backup verification did not prove quick_check=ok, schema=$ExpectedLiveSchema, and stable DB/WAL/SHM"
    }

    Assert-MemoryV2Sha256 -Path $candidate -Expected $ApprovedCandidateHash -Label 'candidate before replace' | Out-Null
    Assert-MemoryV2Sha256 -Path $staged -Expected $ApprovedCandidateHash -Label 'staged candidate before replace' | Out-Null
    $mutationAttempted = $true
    [IO.File]::Replace($staged, $liveBinary, $replaceBackup)
    Assert-MemoryV2Sha256 -Path $liveBinary -Expected $ApprovedCandidateHash -Label 'installed candidate' | Out-Null
    Assert-MemoryV2Sha256 -Path $replaceBackup -Expected $oldHash -Label 'automatic replace backup' | Out-Null

    $newStartAttempted = $true
    & $startLive $startScript
    $postStart = & $assertPostStart $liveBinary $ApprovedCandidateHash $liveData $Port $ExpectedTargetSchema
    if ($postStart.processPath -and
        -not (Get-MemoryV2CanonicalPath $postStart.processPath).Equals(
          $liveBinary, [StringComparison]::OrdinalIgnoreCase
        )) {
      throw 'post-start check returned a different process path'
    }
    if ($postStart.listener -ne $true -or [int]$postStart.httpStatus -ne 200 -or
        $postStart.quickCheck -ne 'ok' -or [int]$postStart.schema -ne $ExpectedTargetSchema) {
      throw "post-start check did not prove listener, HTTP 200, quick_check=ok, and schema $ExpectedTargetSchema"
    }
    $recoveryCopy = Join-Path $backupDirectory 'file-replace-backup-agentdc.exe'
    [IO.File]::Copy($replaceBackup, $recoveryCopy, $false)
    Assert-MemoryV2Sha256 -Path $recoveryCopy -Expected $oldHash -Label 'preserved recovery binary' | Out-Null
    Remove-Item -LiteralPath $replaceBackup -Force
    return [PSCustomObject]@{
      Status = 'PASS'
      BackupDirectory = $backupDirectory
      CandidateSha256 = $ApprovedCandidateHash.ToUpperInvariant()
      OldBinarySha256 = $oldHash
      PostStart = $postStart
    }
  } catch {
    $original = $_.Exception
    $rollbackError = $null
    try {
      if ($mutationAttempted) {
        if ($newStartAttempted) {
          $candidateStopError = $null
          try { & $stopLive $liveBinary $liveData $Port } catch { $candidateStopError = $_.Exception }
          & $waitStopped $liveBinary $Port
          # A non-zero stop is tolerated only when the independent bounded check
          # proves that both the exact process and listener are already absent.
          $null = $candidateStopError
        }
        $backupBinary = Join-Path $backupDirectory 'agentdc.exe'
        Assert-MemoryV2Sha256 -Path $backupBinary -Expected $oldHash -Label 'rollback source' | Out-Null
        if (Test-Path -LiteralPath $rollbackStage) { throw "rollback staging collision: $rollbackStage" }
        [IO.File]::Copy($backupBinary, $rollbackStage, $false)
        Assert-MemoryV2Sha256 -Path $rollbackStage -Expected $oldHash -Label 'rollback staged binary' | Out-Null
        if (Test-Path -LiteralPath $liveBinary -PathType Leaf) {
          if (Test-Path -LiteralPath $failedCandidate) { throw "failed-candidate collision: $failedCandidate" }
          [IO.File]::Replace($rollbackStage, $liveBinary, $failedCandidate)
        } else {
          [IO.File]::Move($rollbackStage, $liveBinary)
        }
        Assert-MemoryV2Sha256 -Path $liveBinary -Expected $oldHash -Label 'restored old binary' | Out-Null
        & $startLive $startScript
        & $assertRollbackHealth $liveBinary $oldHash $liveData $Port | Out-Null
        if (Test-Path -LiteralPath $replaceBackup -PathType Leaf) {
          Move-Item -LiteralPath $replaceBackup `
            -Destination (Join-Path $backupDirectory 'forward-file-replace-backup-agentdc.exe')
        }
        if (Test-Path -LiteralPath $failedCandidate -PathType Leaf) {
          Move-Item -LiteralPath $failedCandidate `
            -Destination (Join-Path $backupDirectory 'failed-candidate-agentdc.exe')
        }
      } elseif ($stopAttempted) {
        if (-not $stopped) {
          try {
            & $waitStopped $liveBinary $Port
            $stopped = $true
          } catch {
            # A failed stop command may mean either "already stopped" or "still
            # healthy". If absence cannot be proven, require bounded health of
            # the exact old binary before leaving it running.
            & $assertRollbackHealth $liveBinary $oldHash $liveData $Port | Out-Null
          }
        }
        if ($stopped) {
          Assert-MemoryV2Sha256 -Path $liveBinary -Expected $oldHash -Label 'pre-mutation old binary' | Out-Null
          & $startLive $startScript
          & $assertRollbackHealth $liveBinary $oldHash $liveData $Port | Out-Null
        }
      }
    } catch {
      $rollbackError = $_.Exception
    }
    if (-not $mutationAttempted -and (Test-Path -LiteralPath $staged)) {
      Remove-Item -LiteralPath $staged -Force
    }
    if ($rollbackError) {
      throw "deployment failed: $($original.Message); rollback/restart failed: $($rollbackError.Message)"
    }
    throw $original
  }
}

function Write-MemoryV2JSONFile {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)]$Value
  )
  $canonical = Get-MemoryV2CanonicalPath $Path
  [IO.Directory]::CreateDirectory((Split-Path -Parent $canonical)) | Out-Null
  $json = $Value | ConvertTo-Json -Depth 20
  [IO.File]::WriteAllText($canonical, $json + "`n", [Text.UTF8Encoding]::new($false))
  return $canonical
}

function Read-MemoryV2CanaryToken {
  param(
    [Parameter(Mandatory)][string]$DataHome,
    [ValidateRange(1, 30)][int]$TimeoutSeconds = 10
  )
  $path = Join-Path $DataHome 'token'
  $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
  do {
    if (Test-Path -LiteralPath $path -PathType Leaf) {
      $value = [IO.File]::ReadAllText($path).Trim()
      if ($value -match '^[A-Fa-f0-9]{64}$') { return $value }
    }
    Start-Sleep -Milliseconds 100
  } while ([DateTime]::UtcNow -lt $deadline)
  throw 'canary token was not created within the bounded startup window'
}

function Invoke-MemoryV2CanaryHTTP {
  param(
    [Parameter(Mandatory)][ValidateSet('GET', 'POST', 'PUT', 'DELETE')][string]$Method,
    [Parameter(Mandatory)][string]$Uri,
    [Parameter(Mandatory)][string]$Token,
    [Parameter(Mandatory)][int]$ExpectedStatus,
    $Body
  )
  $headers = @{ Authorization = "Bearer $Token" }
  $parameters = @{
    Method = $Method
    Uri = $Uri
    Headers = $headers
    UseBasicParsing = $true
    TimeoutSec = 5
    SkipHttpErrorCheck = $true
  }
  if ($PSBoundParameters.ContainsKey('Body')) {
    $parameters.ContentType = 'application/json'
    $parameters.Body = $Body | ConvertTo-Json -Depth 8 -Compress
  }
  $response = Invoke-WebRequest @parameters
  if ([int]$response.StatusCode -ne $ExpectedStatus) {
    throw "canary API $Method failed: expected HTTP $ExpectedStatus, got $($response.StatusCode)"
  }
  if ([string]::IsNullOrWhiteSpace($response.Content)) { return $null }
  try { return $response.Content | ConvertFrom-Json -Depth 20 } catch {
    throw "canary API $Method returned invalid JSON"
  }
}

function Get-MemoryV2SectionKeys {
  param($Section)
  if ($null -eq $Section) { return @() }
  return @($Section | ForEach-Object { [string]$_.memory_key })
}

function Assert-MemoryV2CanaryPortFree {
  param([Parameter(Mandatory)][int]$Port)
  $listeners = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
  if ($listeners.Count -ne 0) { throw "canary port $Port is already listening" }
}

function Invoke-MemoryV2Canary {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$CandidatePath,
    [Parameter(Mandatory)][string]$ApprovedCandidateHash,
    [Parameter(Mandatory)][string]$SourceHome,
    [Parameter(Mandatory)][string]$CanaryHome,
    [Parameter(Mandatory)][string]$LiveRoot,
    [Parameter(Mandatory)][ValidateRange(1, 65535)][int]$Port,
    [Parameter(Mandatory)][string]$ManifestPath,
    [Parameter(Mandatory)][string]$TranscriptPath,
    [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedLiveSchema,
    [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedTargetSchema,
    [string]$PythonPath = 'python'
  )

  $candidate = Get-MemoryV2CanonicalPath $CandidatePath
  $source = Get-MemoryV2CanonicalPath $SourceHome
  $canary = Get-MemoryV2CanonicalPath $CanaryHome
  $live = Get-MemoryV2CanonicalPath $LiveRoot
  $manifestFile = Get-MemoryV2CanonicalPath $ManifestPath
  $transcriptFile = Get-MemoryV2CanonicalPath $TranscriptPath
  if ($manifestFile.Equals($transcriptFile, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'manifest and transcript paths must be different'
  }
  [IO.Directory]::CreateDirectory((Split-Path -Parent $manifestFile)) | Out-Null
  [IO.Directory]::CreateDirectory((Split-Path -Parent $transcriptFile)) | Out-Null
  if ((Test-Path -LiteralPath $manifestFile) -or (Test-Path -LiteralPath $transcriptFile)) {
    throw 'canary manifest/transcript output path already exists'
  }
  if (Test-MemoryV2PathWithin -Path $canary -Root $live) {
    throw 'canary copied home must be outside live root'
  }
  $candidateHash = Assert-MemoryV2Sha256 -Path $candidate `
    -Expected $ApprovedCandidateHash -Label 'approved canary candidate'
  Assert-MemoryV2CanaryPortFree -Port $Port
  $portInitiallyFree = $true

  $runningCandidate = @(Get-CimInstance Win32_Process -Filter "Name='agentdc.exe'" -ErrorAction SilentlyContinue |
      Where-Object { $_.ExecutablePath -and
        (Get-MemoryV2CanonicalPath $_.ExecutablePath).Equals(
          $candidate, [StringComparison]::OrdinalIgnoreCase
        ) })
  if ($runningCandidate.Count -ne 0) { throw 'approved candidate path is already running' }

  $events = [Collections.Generic.List[object]]::new()
  $journeys = [ordered]@{ J1 = $false; J2 = $false; J3 = $false; J4 = $false; J5 = $false; J6 = $false }
  $process = $null
  $sanitized = $null
  $copyInfo = $null
  $sourceInitial = $null
  $sourceFinal = $null
  $copyBefore = $null
  $migrated = $null
  $comparison = $null
  $final = $null
  $stoppedCleanly = $false

  $addEvent = {
    param([string]$Name, [string]$Status, [string]$Detail)
    $events.Add([ordered]@{
      utc = [DateTime]::UtcNow.ToString('o')
      name = $Name
      status = $Status
      detail = $Detail
    })
  }

  try {
    $sourceDB = Join-Path $source 'agentdc.db'
    $sourceInitial = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable source verification' `
      -Arguments @('inspect', '--db', $sourceDB, '--expected-schema', [string]$ExpectedLiveSchema, '--include-stable')
    & $addEvent 'source' 'PASS' 'immutable quick_check/schema/baseline'

    $copyInfo = Initialize-MemoryV2CanaryHome -SourceHome $source -CanaryHome $canary -LiveRoot $live
    $copyBefore = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable copied source verification' `
      -Arguments @('inspect', '--db', $copyInfo.CopyDB, '--expected-schema', [string]$ExpectedLiveSchema, '--include-stable')
    if ($copyBefore.dbSha256 -ne $sourceInitial.dbSha256) {
      throw 'copied DB hash differs from immutable source DB hash'
    }
    & $addEvent 'copy-and-scrub' 'PASS' 'byte-identical DB; controls and credentials absent'

    $sanitized = Get-MemoryV2SanitizedEnvironment -DataHome $canary -Port $Port
    $unsafeEnvironmentNames = @($sanitized.Values.Keys | Where-Object {
        $_ -like 'AGENTDC_ZALO_*' -or $_ -like 'ZALO_*' -or
        $_ -in @('AGENTDC_TOKEN', 'AGENTDC_URL')
      })
    if ($unsafeEnvironmentNames.Count -ne 0) {
      throw 'canary child environment still contains transport-related variables'
    }
    foreach ($relative in @('daemon.lock', 'token', 'zalo-transport.pid', 'zalo\credentials.json')) {
      if (Test-Path -LiteralPath (Join-Path $canary $relative)) {
        throw "canary isolation assertion failed before startup: $relative"
      }
    }
    & $addEvent 'isolation-before-start' 'PASS' 'no transport env, credential, PID, lock, or copied token'

    $process = Start-MemoryV2Process -FilePath $candidate -Arguments @('daemon') `
      -Environment $sanitized.Values -WorkingDirectory (Split-Path -Parent $candidate)
    $health = Assert-MemoryV2BoundedHealth -ExecutablePath $candidate -ExpectedHash $candidateHash `
      -Port $Port -TimeoutSeconds 30
    if ($health.ProcessID -ne $process.Id) { throw 'canary listener is owned by a different process' }
    $tokenValue = Read-MemoryV2CanaryToken -DataHome $canary
    $null = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "http://127.0.0.1:$Port/memory" `
      -Token $tokenValue -ExpectedStatus 200
    & $addEvent 'migration-start' 'PASS' 'exact process path; loopback listener; status/memory HTTP 200'

    Invoke-MemoryV2NativeChecked -Label 'canary migration stop' -FilePath $candidate `
      -Arguments @('daemon', 'stop', '--force') -Environment $sanitized.Values `
      -WorkingDirectory (Split-Path -Parent $candidate) | Out-Null
    Wait-MemoryV2ProcessStopped -ExecutablePath $candidate -Port $Port -TimeoutSeconds 30
    $process = $null
    $migrated = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable migrated target verification' `
      -Arguments @('inspect', '--db', $copyInfo.CopyDB, '--expected-schema', [string]$ExpectedTargetSchema, '--include-stable')
    $comparison = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable V3 preservation comparison' `
      -Arguments @(
        'compare', '--source-db', $sourceDB, '--migrated-db', $copyInfo.CopyDB,
        '--expected-source-schema', [string]$ExpectedLiveSchema,
        '--expected-target-schema', [string]$ExpectedTargetSchema
      )
    if ($comparison.stable.equal -ne $true) { throw 'stable V3 fields were not preserved' }
    & $addEvent 'migration-stop' 'PASS' "clean stop; schema $ExpectedLiveSchema to $ExpectedTargetSchema; stable fields preserved"

    $runID = 'canary-' + [DateTime]::UtcNow.ToString('yyyyMMddHHmmss') + '-' +
      [guid]::NewGuid().ToString('N').Substring(0, 6)
    $fixture = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath -Label 'seed opaque canary fixtures' `
      -Arguments @(
        'seed', '--db', $copyInfo.CopyDB, '--run-id', $runID,
        '--expected-schema', [string]$ExpectedTargetSchema
      )
    $fixturePath = Join-Path $canary 'memory-v2-fixture.json'
    Write-MemoryV2JSONFile -Path $fixturePath -Value $fixture | Out-Null
    & $addEvent 'fake-fixtures' 'PASS' 'opaque local-only fixtures seeded while stopped'

    $process = Start-MemoryV2Process -FilePath $candidate -Arguments @('daemon') `
      -Environment $sanitized.Values -WorkingDirectory (Split-Path -Parent $candidate)
    $health = Assert-MemoryV2BoundedHealth -ExecutablePath $candidate -ExpectedHash $candidateHash `
      -Port $Port -TimeoutSeconds 30
    if ($health.ProcessID -ne $process.Id) { throw 'canary API listener is owned by a different process' }
    $tokenValue = Read-MemoryV2CanaryToken -DataHome $canary
    $base = "http://127.0.0.1:$Port/memory/threads"
    $groupID = [Uri]::EscapeDataString([string]$fixture.group)
    $memberA = [Uri]::EscapeDataString([string]$fixture.memberA)
    $memberB = [Uri]::EscapeDataString([string]$fixture.memberB)
    $privateID = [Uri]::EscapeDataString([string]$fixture.private)

    $commonDetail = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID" `
      -Token $tokenValue -ExpectedStatus 200
    $aDetail = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberA" `
      -Token $tokenValue -ExpectedStatus 200
    $bDetail = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberB" `
      -Token $tokenValue -ExpectedStatus 200
    $commonKeys = @(Get-MemoryV2SectionKeys $commonDetail.active)
    $aKeys = @(Get-MemoryV2SectionKeys $aDetail.active)
    $bKeys = @(Get-MemoryV2SectionKeys $bDetail.active)
    if ($commonKeys -notcontains 'canary.common' -or $aKeys -notcontains 'canary.color' -or
        $bKeys -notcontains 'canary.member' -or $aKeys -contains 'canary.member' -or
        $bKeys -contains 'canary.color' -or $aDetail.synced -ne $true -or $bDetail.synced -ne $false) {
      throw 'J1 member/common isolation or selected-member sync assertion failed'
    }
    $journeys.J1 = $true
    & $addEvent 'J1' 'PASS' 'common/member isolation and selected-member sync state'

    $approved = Invoke-MemoryV2CanaryHTTP -Method POST `
      -Uri "$base/$groupID/$($fixture.ids.pendingA)/approve" -Token $tokenValue -ExpectedStatus 200 `
      -Body ([ordered]@{
        uid = $fixture.memberA; memory_key = 'canary.color'; text = 'CANARY-A-NEW'
        category = 'preference'; expected_revision = 1
      })
    $aAfterApprove = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberA" `
      -Token $tokenValue -ExpectedStatus 200
    $aApprovedKeys = @(Get-MemoryV2SectionKeys $aAfterApprove.active)
    if ($approved.status -ne 'active' -or [int]$aAfterApprove.subject_revision -ne 2 -or
        $aApprovedKeys -notcontains 'canary.color' -or
        @($aAfterApprove.active | Where-Object text -EQ 'CANARY-A-OLD').Count -ne 0) {
      throw 'J2 approve replacement assertion failed'
    }
    $journeys.J2 = $true
    & $addEvent 'J2' 'PASS' 'replacement approved and subject revision advanced once'

    $null = Invoke-MemoryV2CanaryHTTP -Method POST `
      -Uri "$base/$groupID/$($fixture.ids.sensitiveB)/reject" -Token $tokenValue -ExpectedStatus 204 `
      -Body ([ordered]@{ uid = $fixture.memberB; expected_revision = 1 })
    $bAfterReject = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberB" `
      -Token $tokenValue -ExpectedStatus 200
    if ([int]$bAfterReject.subject_revision -ne 1 -or
        @(Get-MemoryV2SectionKeys $bAfterReject.pending) -contains 'canary.sensitive') {
      throw 'J3 reject sensitive pending assertion failed'
    }
    $journeys.J3 = $true
    & $addEvent 'J3' 'PASS' 'sensitive pending rejected without active revision change'

    $restored = Invoke-MemoryV2CanaryHTTP -Method POST `
      -Uri "$base/$groupID/$($fixture.ids.expiredA)/restore" -Token $tokenValue -ExpectedStatus 200 `
      -Body ([ordered]@{ uid = $fixture.memberA; expected_revision = 2 })
    if ($restored.status -ne 'active' -or $null -eq $restored.expires_at) {
      throw 'J4 restore did not reactivate with expiry'
    }
    $pinned = Invoke-MemoryV2CanaryHTTP -Method PUT `
      -Uri "$base/$groupID/$($fixture.ids.expiredA)" -Token $tokenValue -ExpectedStatus 200 `
      -Body ([ordered]@{
        uid = $fixture.memberA; memory_key = 'canary.expired'; text = 'CANARY-A-EXPIRED'
        category = 'interest'; pinned = $true; expected_revision = 3
      })
    $aAfterPin = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberA" `
      -Token $tokenValue -ExpectedStatus 200
    $pinnedExpiryProperty = $pinned.PSObject.Properties['expires_at']
    $pinnedHasExpiry = $null -ne $pinnedExpiryProperty -and $null -ne $pinnedExpiryProperty.Value
    if ($pinned.pinned -ne $true -or $pinnedHasExpiry -or
        [int]$aAfterPin.subject_revision -ne 4) {
      throw 'J4 pin assertion failed'
    }
    $journeys.J4 = $true
    & $addEvent 'J4' 'PASS' 'expired Memory restored then pinned; revision advanced twice'

    $null = Invoke-MemoryV2CanaryHTTP -Method DELETE `
      -Uri "$base/$privateID/$($fixture.ids.lineageActive)" -Token $tokenValue -ExpectedStatus 204 `
      -Body ([ordered]@{ uid = $fixture.private; expected_revision = 1 })
    $privateAfter = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$privateID" `
      -Token $tokenValue -ExpectedStatus 200
    if ([int]$privateAfter.subject_revision -ne 2 -or
        @(Get-MemoryV2SectionKeys $privateAfter.active) -contains 'canary.lineage') {
      throw 'J5 lineage deletion API assertion failed'
    }
    $journeys.J5 = $true
    & $addEvent 'J5' 'PASS' 'private lineage deleted through API; message retention checked after stop'

    $stableOne = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberA" `
      -Token $tokenValue -ExpectedStatus 200
    $stableTwo = Invoke-MemoryV2CanaryHTTP -Method GET -Uri "$base/$groupID`?uid=$memberA" `
      -Token $tokenValue -ExpectedStatus 200
    if ([int]$stableOne.common_revision -ne [int]$stableTwo.common_revision -or
        [int]$stableOne.subject_revision -ne [int]$stableTwo.subject_revision -or
        [bool]$stableOne.synced -ne [bool]$stableTwo.synced) {
      throw 'J6 unchanged repeated revision read was not stable'
    }
    $journeys.J6 = $true
    & $addEvent 'J6' 'PASS' 'two unchanged same-subject reads kept revisions and sync state stable'

    Invoke-MemoryV2NativeChecked -Label 'canary API stop' -FilePath $candidate `
      -Arguments @('daemon', 'stop', '--force') -Environment $sanitized.Values `
      -WorkingDirectory (Split-Path -Parent $candidate) | Out-Null
    Wait-MemoryV2ProcessStopped -ExecutablePath $candidate -Port $Port -TimeoutSeconds 30
    $process = $null
    $stoppedCleanly = $true
    Assert-MemoryV2CanaryPortFree -Port $Port
    & $addEvent 'canary-stop' 'PASS' 'exact candidate process exited; listener absent'

    $finalAssertions = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable final journey assertions' `
      -Arguments @(
        'assert-final', '--db', $copyInfo.CopyDB, '--fixture', $fixturePath,
        '--expected-schema', [string]$ExpectedTargetSchema
      )
    if ($finalAssertions.all -ne $true) { throw 'final canary DB assertions were not all true' }
    $final = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable final canary verification' `
      -Arguments @('inspect', '--db', $copyInfo.CopyDB, '--expected-schema', [string]$ExpectedTargetSchema)
    $sourceFinal = Invoke-MemoryV2SQLiteTool -PythonPath $PythonPath `
      -Label 'immutable source re-verification' `
      -Arguments @('inspect', '--db', $sourceDB, '--expected-schema', [string]$ExpectedLiveSchema, '--include-stable')
    if ($sourceFinal.baselineSha256 -ne $sourceInitial.baselineSha256 -or
        $sourceFinal.dbSha256 -ne $sourceInitial.dbSha256) {
      throw 'source V3 DB/WAL/SHM evidence changed during canary'
    }
    if ((Test-Path -LiteralPath (Join-Path $canary 'zalo-transport.pid')) -or
        (Test-Path -LiteralPath (Join-Path $canary 'zalo\credentials.json'))) {
      throw 'canary created Zalo transport PID or credentials despite isolation'
    }
    & $addEvent 'final-evidence' 'PASS' 'immutable final schema/quick_check and source baseline stable'

    $transcript = [ordered]@{
      version = 'memory-v2-canary-transcript/v1'
      status = 'PASS'
      generatedUtc = [DateTime]::UtcNow.ToString('o')
      port = $Port
      events = @($events)
    }
    Write-MemoryV2JSONFile -Path $transcriptFile -Value $transcript | Out-Null
    $transcriptHash = Get-MemoryV2Sha256 $transcriptFile
    $manifest = [ordered]@{
      version = $script:CanaryManifestVersion
      status = 'PASS'
      generatedUtc = [DateTime]::UtcNow.ToString('o')
      port = $Port
      candidate = [ordered]@{ path = $candidate; sha256 = $candidateHash }
      source = [ordered]@{
        dbPath = $sourceDB
        dbSha256 = [string]$sourceInitial.dbSha256
        schema = [int]$sourceInitial.schema
        quickCheck = [string]$sourceInitial.quickCheck
      }
      copy = [ordered]@{
        homePath = $canary
        dbSha256Before = [string]$copyBefore.dbSha256
        schemaBefore = [int]$copyBefore.schema
        quickCheckBefore = [string]$copyBefore.quickCheck
        schemaMigrated = [int]$migrated.schema
        quickCheckMigrated = [string]$migrated.quickCheck
        dbSha256Final = [string]$final.dbSha256
        schemaFinal = [int]$final.schema
        quickCheckFinal = [string]$final.quickCheck
      }
      journeys = $journeys
      isolation = [ordered]@{
        outsideLiveRoot = $true
        credentialsAbsent = $true
        transportEnvironmentCleared = $true
        runtimeControlsRemoved = $true
        portInitiallyFree = $portInitiallyFree
        candidatePathExact = $true
      }
      stopped = [ordered]@{ processExited = $stoppedCleanly; listenerAbsent = $true }
      evidence = [ordered]@{
        transcriptPath = $transcriptFile
        transcriptSha256 = $transcriptHash
        sourceBaselineSha256 = [string]$sourceInitial.baselineSha256
        migrationComparisonSha256 = [string]$comparison.comparisonSha256
        finalDbSha256 = [string]$final.dbSha256
      }
    }
    Assert-MemoryV2ManifestRedacted -Value $manifest
    Write-MemoryV2JSONFile -Path $manifestFile -Value $manifest | Out-Null
    Test-MemoryV2CanaryManifest -ManifestPath $manifestFile -CandidatePath $candidate `
      -ApprovedCandidateHash $candidateHash -ExpectedLiveSchema $ExpectedLiveSchema `
      -ExpectedTargetSchema $ExpectedTargetSchema | Out-Null
    return [PSCustomObject]@{
      Status = 'PASS'
      ManifestPath = $manifestFile
      TranscriptPath = $transcriptFile
      CanaryHome = $canary
      Port = $Port
      ManifestSha256 = Get-MemoryV2Sha256 $manifestFile
      TranscriptSha256 = $transcriptHash
    }
  } catch {
    $original = $_.Exception
    if ($null -ne $process -and $null -ne $sanitized) {
      try {
        Invoke-MemoryV2NativeChecked -Label 'failed canary cleanup stop' -FilePath $candidate `
          -Arguments @('daemon', 'stop', '--force') -Environment $sanitized.Values `
          -WorkingDirectory (Split-Path -Parent $candidate) | Out-Null
        Wait-MemoryV2ProcessStopped -ExecutablePath $candidate -Port $Port -TimeoutSeconds 30
        $stoppedCleanly = $true
      } catch {
        $stoppedCleanly = $false
      }
    }
    & $addEvent 'failure' 'FAIL' $original.GetType().Name
    $failureTranscript = [ordered]@{
      version = 'memory-v2-canary-transcript/v1'
      status = 'FAIL'
      generatedUtc = [DateTime]::UtcNow.ToString('o')
      port = $Port
      events = @($events)
    }
    if (-not (Test-Path -LiteralPath $transcriptFile)) {
      Write-MemoryV2JSONFile -Path $transcriptFile -Value $failureTranscript | Out-Null
    }
    $failureManifest = [ordered]@{
      version = $script:CanaryManifestVersion
      status = 'FAIL'
      generatedUtc = [DateTime]::UtcNow.ToString('o')
      port = $Port
      candidate = [ordered]@{ path = $candidate; sha256 = $candidateHash }
      stopped = [ordered]@{
        processExited = $stoppedCleanly
        listenerAbsent = (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -eq 0)
      }
      evidence = [ordered]@{
        transcriptPath = $transcriptFile
        transcriptSha256 = Get-MemoryV2Sha256 $transcriptFile
      }
    }
    if (-not (Test-Path -LiteralPath $manifestFile)) {
      Write-MemoryV2JSONFile -Path $manifestFile -Value $failureManifest | Out-Null
    }
    throw $original
  }
}

Export-ModuleMember -Function `
  Initialize-MemoryV2CanaryHome, `
  Invoke-MemoryV2Canary, `
  Test-MemoryV2CanaryManifest, `
  Invoke-MemoryV2Deployment
