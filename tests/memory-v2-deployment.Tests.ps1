$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-Equal {
  param($Actual, $Expected, [Parameter(Mandatory)][string]$Message)
  if ($Actual -ne $Expected) {
    throw "$Message. Actual: '$Actual'; expected: '$Expected'"
  }
}

function Assert-True {
  param([bool]$Condition, [Parameter(Mandatory)][string]$Message)
  if (-not $Condition) { throw $Message }
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

function Write-TestText {
  param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Text)
  [IO.Directory]::CreateDirectory((Split-Path -Parent $Path)) | Out-Null
  [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function Get-TestHash {
  param([Parameter(Mandatory)][string]$Path)
  return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToUpperInvariant()
}

function New-ValidManifestFixture {
  param(
    [Parameter(Mandatory)][string]$Root,
    [Parameter(Mandatory)][string]$Candidate,
    [Parameter(Mandatory)][string]$Transcript,
    [ValidateRange(1, [int]::MaxValue)][int]$ExpectedLiveSchema = 3,
    [ValidateRange(1, [int]::MaxValue)][int]$ExpectedTargetSchema = 4
  )
  $candidatePath = [IO.Path]::GetFullPath($Candidate)
  $transcriptPath = [IO.Path]::GetFullPath($Transcript)
  $candidateHash = Get-TestHash $candidatePath
  $transcriptHash = Get-TestHash $transcriptPath
  $dbHash = ('A' * 64)
  return [ordered]@{
    version = 'memory-v2-canary/v1'
    status = 'PASS'
    generatedUtc = [DateTime]::UtcNow.ToString('o')
    port = 48871
    candidate = [ordered]@{ path = $candidatePath; sha256 = $candidateHash }
    source = [ordered]@{
      dbPath = [IO.Path]::GetFullPath((Join-Path $Root 'source\agentdc.db'))
      dbSha256 = $dbHash
      schema = $ExpectedLiveSchema
      quickCheck = 'ok'
    }
    copy = [ordered]@{
      homePath = [IO.Path]::GetFullPath((Join-Path $Root 'canary-home'))
      dbSha256Before = $dbHash
      schemaBefore = $ExpectedLiveSchema
      quickCheckBefore = 'ok'
      schemaMigrated = $ExpectedTargetSchema
      quickCheckMigrated = 'ok'
      dbSha256Final = ('B' * 64)
      schemaFinal = $ExpectedTargetSchema
      quickCheckFinal = 'ok'
    }
    journeys = [ordered]@{ J1 = $true; J2 = $true; J3 = $true; J4 = $true; J5 = $true; J6 = $true }
    isolation = [ordered]@{
      outsideLiveRoot = $true
      credentialsAbsent = $true
      transportEnvironmentCleared = $true
      runtimeControlsRemoved = $true
      portInitiallyFree = $true
      candidatePathExact = $true
    }
    stopped = [ordered]@{ processExited = $true; listenerAbsent = $true }
    evidence = [ordered]@{
      transcriptPath = $transcriptPath
      transcriptSha256 = $transcriptHash
      sourceBaselineSha256 = ('C' * 64)
      migrationComparisonSha256 = ('D' * 64)
      finalDbSha256 = ('B' * 64)
    }
  }
}

function Write-ManifestFixture {
  param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)]$Manifest)
  Write-TestText -Path $Path -Text ($Manifest | ConvertTo-Json -Depth 12)
}

function New-ValidTranscriptFixture {
  param([ValidateRange(1, 65535)][int]$Port = 48871)
  $events = @(
    'source', 'copy-and-scrub', 'isolation-before-start', 'migration-start',
    'migration-stop', 'fake-fixtures', 'J1', 'J2', 'J3', 'J4', 'J5', 'J6',
    'canary-stop', 'final-evidence'
  ) | ForEach-Object {
    [ordered]@{
      utc = [DateTime]::UtcNow.AddSeconds(-1).ToString('o')
      name = $_
      status = 'PASS'
      detail = 'redacted fixture checkpoint'
    }
  }
  return [ordered]@{
    version = 'memory-v2-canary-transcript/v1'
    status = 'PASS'
    generatedUtc = [DateTime]::UtcNow.ToString('o')
    port = $Port
    events = $events
  }
}

$modulePath = Join-Path $PSScriptRoot '..\scripts\MemoryV2Deployment.psm1'
$canaryScript = Join-Path $PSScriptRoot '..\scripts\Invoke-MemoryV2Canary.ps1'
$deployScript = Join-Path $PSScriptRoot '..\scripts\Invoke-MemoryV2Deploy.ps1'
$sqliteHelper = Join-Path $PSScriptRoot '..\scripts\memory_v2_sqlite.py'

foreach ($path in @($modulePath, $canaryScript, $deployScript, $sqliteHelper)) {
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "required Memory V2 deployment tool is missing: $path"
  }
}
$null = [ScriptBlock]::Create((Get-Content -LiteralPath $canaryScript -Raw))
$null = [ScriptBlock]::Create((Get-Content -LiteralPath $deployScript -Raw))
Import-Module $modulePath -Force
foreach ($name in @(
    'Initialize-MemoryV2CanaryHome',
    'Invoke-MemoryV2Canary',
    'Test-MemoryV2CanaryManifest',
    'Invoke-MemoryV2Deployment'
  )) {
  if (-not (Get-Command $name -CommandType Function -ErrorAction SilentlyContinue)) {
    throw "Memory V2 deployment module does not export $name"
  }
}
Write-Host 'PASS: Memory V2 executable scripts parse and required module commands are exported.'

$fixtureRoot = Join-Path ([IO.Path]::GetTempPath()) ('memory-v2-deploy-test-' + [guid]::NewGuid().ToString('N'))
try {
  $liveRoot = Join-Path $fixtureRoot 'live-root'
  $sourceRoot = Join-Path $fixtureRoot 'source-home'
  $canaryRoot = Join-Path $fixtureRoot 'isolated\canary-home'
  [IO.Directory]::CreateDirectory($liveRoot) | Out-Null
  foreach ($relative in @(
      'agentdc.db', 'agentdc.db-wal', 'agentdc.db-shm', 'daemon.lock', 'token',
      'zalo-transport.pid', 'zalo-transport.log', 'zalo\credentials.json', 'keep\sentinel.txt'
    )) {
    Write-TestText -Path (Join-Path $sourceRoot $relative) -Text "fixture-$relative"
  }

  $result = Initialize-MemoryV2CanaryHome -SourceHome $sourceRoot -CanaryHome $canaryRoot `
    -LiveRoot $liveRoot
  Assert-Equal $result.CanaryHome ([IO.Path]::GetFullPath($canaryRoot)) 'Canary home was not canonicalized'
  foreach ($relative in @(
      'daemon.lock', 'token', 'zalo-transport.pid', 'zalo-transport.log', 'zalo\credentials.json'
    )) {
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $canaryRoot $relative))) `
      "Canary copy retained unsafe control '$relative'"
  }
  Assert-True (Test-Path -LiteralPath (Join-Path $canaryRoot 'keep\sentinel.txt')) `
    'Canary scrub removed unrelated copied evidence'
  Assert-Equal (Get-TestHash (Join-Path $sourceRoot 'agentdc.db')) `
    (Get-TestHash (Join-Path $canaryRoot 'agentdc.db')) 'Canary DB copy was not byte-identical'

  Assert-ThrowsLike -Action {
    Initialize-MemoryV2CanaryHome -SourceHome $sourceRoot `
      -CanaryHome (Join-Path $liveRoot 'forbidden-canary') -LiveRoot $liveRoot
  } -Pattern 'outside|ngoài|live root' -Message 'Canary home inside live root was accepted'
  Assert-ThrowsLike -Action {
    Initialize-MemoryV2CanaryHome -SourceHome $sourceRoot `
      -CanaryHome $sourceRoot -LiveRoot $liveRoot
  } -Pattern 'different|khác|source' -Message 'Source home was accepted as the canary home'
  $activeLiveData = Join-Path $liveRoot 'data'
  Write-TestText -Path (Join-Path $activeLiveData 'agentdc.db') -Text 'active-live-db'
  Assert-ThrowsLike -Action {
    Initialize-MemoryV2CanaryHome -SourceHome $activeLiveData `
      -CanaryHome (Join-Path $fixtureRoot 'isolated\from-active-live') -LiveRoot $liveRoot
  } -Pattern 'active|live data|retained backup|source' `
    -Message 'Active live data was accepted as a canary copy source'
  Write-Host 'PASS: canary copy is outside live root and scrubs only runtime controls and Zalo credentials.'

  $candidate = Join-Path $fixtureRoot 'candidate\agentdc.exe'
  $transcript = Join-Path $fixtureRoot 'evidence\transcript.json'
  $manifestPath = Join-Path $fixtureRoot 'evidence\manifest.json'
  Write-TestText $candidate 'candidate-v1'
  Write-TestText $transcript ((New-ValidTranscriptFixture) | ConvertTo-Json -Depth 12)
  $valid = New-ValidManifestFixture -Root $fixtureRoot -Candidate $candidate -Transcript $transcript
  Write-ManifestFixture $manifestPath $valid
  $approvedHash = Get-TestHash $candidate
  $validated = Test-MemoryV2CanaryManifest -ManifestPath $manifestPath `
    -CandidatePath $candidate -ApprovedCandidateHash $approvedHash `
    -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -MaxAgeMinutes 60
  Assert-Equal $validated.status 'PASS' 'Valid canary manifest was not accepted'
  Assert-ThrowsLike -Action {
    Invoke-MemoryV2Canary -CandidatePath $candidate -ApprovedCandidateHash $approvedHash `
      -SourceHome $sourceRoot -CanaryHome (Join-Path $fixtureRoot 'unused-canary') `
      -LiveRoot $liveRoot -Port 48872 -ManifestPath $manifestPath -TranscriptPath $transcript `
      -ExpectedLiveSchema 3 -ExpectedTargetSchema 4
  } -Pattern 'output path.*exists|đã tồn tại' `
    -Message 'Canary did not fail cleanly on an existing evidence output path'

  $invalidCases = @(
    @{ Name = 'status'; Pattern = 'PASS|status'; Change = { param($m) $m.status = 'FAIL' } },
    @{ Name = 'stale'; Pattern = 'fresh|stale|cũ'; Change = { param($m) $m.generatedUtc = [DateTime]::UtcNow.AddHours(-3).ToString('o') } },
    @{ Name = 'path'; Pattern = 'candidate.*path|đường dẫn'; Change = { param($m) $m.candidate.path = (Join-Path $fixtureRoot 'other.exe') } },
    @{ Name = 'hash'; Pattern = 'hash|SHA-256'; Change = { param($m) $m.candidate.sha256 = ('E' * 64) } },
    @{ Name = 'source-schema'; Pattern = 'schema.*3|source'; Change = { param($m) $m.source.schema = 4 } },
    @{ Name = 'migration-schema'; Pattern = 'schema.*4|migrat'; Change = { param($m) $m.copy.schemaMigrated = 3 } },
    @{ Name = 'final-quick'; Pattern = 'quick_check|quickCheck'; Change = { param($m) $m.copy.quickCheckFinal = 'broken' } },
    @{ Name = 'journey'; Pattern = 'J4|journey'; Change = { param($m) $m.journeys.J4 = $false } },
    @{ Name = 'isolation'; Pattern = 'isolation|credentialsAbsent'; Change = { param($m) $m.isolation.credentialsAbsent = $false } },
    @{ Name = 'process'; Pattern = 'stopped|processExited'; Change = { param($m) $m.stopped.processExited = $false } },
    @{ Name = 'listener'; Pattern = 'listener'; Change = { param($m) $m.stopped.listenerAbsent = $false } },
    @{ Name = 'evidence'; Pattern = 'evidence|SHA-256|hash'; Change = { param($m) $m.evidence.sourceBaselineSha256 = '' } },
    @{ Name = 'secret-key'; Pattern = 'secret|sensitive|redact'; Change = { param($m) $m.evidence.Add('token', 'do-not-accept') } }
  )
  foreach ($case in $invalidCases) {
    $mutated = New-ValidManifestFixture -Root $fixtureRoot -Candidate $candidate -Transcript $transcript
    & $case.Change $mutated
    $casePath = Join-Path $fixtureRoot "evidence\invalid-$($case.Name).json"
    Write-ManifestFixture $casePath $mutated
    Assert-ThrowsLike -Action {
      Test-MemoryV2CanaryManifest -ManifestPath $casePath -CandidatePath $candidate `
        -ApprovedCandidateHash $approvedHash -ExpectedLiveSchema 3 `
      -ExpectedTargetSchema 4 -MaxAgeMinutes 60
    } -Pattern $case.Pattern -Message "Invalid manifest case '$($case.Name)' was accepted"
  }

  $invalidTranscriptCases = @(
    @{ Name = 'status'; Pattern = 'transcript.*PASS|status'; Change = { param($t) $t.status = 'FAIL' } },
    @{ Name = 'version'; Pattern = 'transcript.*version'; Change = { param($t) $t.version = 'wrong/v1' } },
    @{ Name = 'port'; Pattern = 'transcript.*port'; Change = { param($t) $t.port = 48872 } },
    @{ Name = 'stale'; Pattern = 'transcript.*fresh|stale'; Change = { param($t) $t.generatedUtc = [DateTime]::UtcNow.AddHours(-3).ToString('o') } },
    @{ Name = 'missing-event'; Pattern = 'J4|required.*event|event.*J4'; Change = {
        param($t) $t.events = @($t.events | Where-Object { $_.name -ne 'J4' })
      } },
    @{ Name = 'failed-event'; Pattern = 'J4|event.*PASS'; Change = {
        param($t) ($t.events | Where-Object { $_.name -eq 'J4' }).status = 'FAIL'
      } },
    @{ Name = 'secret-field'; Pattern = 'secret|sensitive|redact|token'; Change = {
        param($t) $t.events[0].Add('token', 'must-not-be-accepted')
      } }
  )
  foreach ($case in $invalidTranscriptCases) {
    $mutatedTranscript = New-ValidTranscriptFixture
    & $case.Change $mutatedTranscript
    $caseTranscriptPath = Join-Path $fixtureRoot "evidence\invalid-transcript-$($case.Name).json"
    Write-TestText $caseTranscriptPath ($mutatedTranscript | ConvertTo-Json -Depth 12)
    $boundManifest = New-ValidManifestFixture -Root $fixtureRoot -Candidate $candidate `
      -Transcript $caseTranscriptPath
    $caseManifestPath = Join-Path $fixtureRoot "evidence\invalid-transcript-$($case.Name)-manifest.json"
    Write-ManifestFixture $caseManifestPath $boundManifest
    Assert-ThrowsLike -Action {
      Test-MemoryV2CanaryManifest -ManifestPath $caseManifestPath -CandidatePath $candidate `
        -ApprovedCandidateHash $approvedHash -ExpectedLiveSchema 3 `
        -ExpectedTargetSchema 4 -MaxAgeMinutes 60
    } -Pattern $case.Pattern -Message "Invalid transcript case '$($case.Name)' was accepted"
  }
  Write-TestText (Join-Path $fixtureRoot 'evidence\malformed.json') '{broken'
  Assert-ThrowsLike -Action {
    Test-MemoryV2CanaryManifest -ManifestPath (Join-Path $fixtureRoot 'evidence\malformed.json') `
      -CandidatePath $candidate -ApprovedCandidateHash $approvedHash `
      -ExpectedLiveSchema 3 -ExpectedTargetSchema 4
  } -Pattern 'parse|JSON|đọc' -Message 'Malformed manifest was accepted'
  Assert-ThrowsLike -Action {
    Test-MemoryV2CanaryManifest -ManifestPath (Join-Path $fixtureRoot 'evidence\missing.json') `
      -CandidatePath $candidate -ApprovedCandidateHash $approvedHash `
      -ExpectedLiveSchema 3 -ExpectedTargetSchema 4
  } -Pattern 'missing|không.*manifest' -Message 'Missing manifest was accepted'
  Write-Host 'PASS: manifest gate rejects every stale, mismatched, incomplete, active, or sensitive case.'

  $deployRoot = Join-Path $fixtureRoot 'deploy'
  $deployLive = Join-Path $deployRoot 'live'
  $deployApp = Join-Path $deployLive 'app'
  $deployData = Join-Path $deployLive 'data'
  $backupRoot = Join-Path $deployRoot 'backups'
  $liveBinary = Join-Path $deployApp 'agentdc.exe'
  $startScript = Join-Path $deployLive 'Start.vbs'
  [IO.Directory]::CreateDirectory($deployData) | Out-Null
  Write-TestText $liveBinary 'old-binary-v3'
  Write-TestText (Join-Path $deployData 'agentdc.db') 'fake-schema3-db'
  Write-TestText $startScript 'fake-start'

  $stopCalls = 0
  $invalidOps = @{
    StopLive = { param($binary, $data, $port) $script:stopCalls++ }
  }
  $badManifest = New-ValidManifestFixture -Root $fixtureRoot -Candidate $candidate -Transcript $transcript
  $badManifest.journeys.J2 = $false
  $badManifestPath = Join-Path $fixtureRoot 'evidence\deploy-invalid.json'
  Write-ManifestFixture $badManifestPath $badManifest
  Assert-ThrowsLike -Action {
    Invoke-MemoryV2Deployment -ManifestPath $badManifestPath -CandidatePath $candidate `
      -ApprovedCandidateHash $approvedHash -LiveRoot $deployLive -BackupRoot $backupRoot `
      -Port 48901 -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -Operations $invalidOps
  } -Pattern 'J2|journey' -Message 'Deployment accepted a failed canary manifest'
  Assert-Equal $stopCalls 0 'Deployment touched stop seam before validating the manifest'

  $manifest = New-ValidManifestFixture -Root $fixtureRoot -Candidate $candidate -Transcript $transcript
  $manifestPath = Join-Path $fixtureRoot 'evidence\deploy-valid.json'
  Write-ManifestFixture $manifestPath $manifest

  $schemaMismatchStopCalls = 0
  $schemaMismatchOps = @{
    InspectLive = {
      param($db, $expectedSchema)
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 4 }
    }
    StopLive = { param($binary, $data, $port) $script:schemaMismatchStopCalls++ }
    WaitStopped = { param($binary, $port) throw 'must not wait after preflight mismatch' }
    InspectBackup = { param($db, $expectedSchema) throw 'must not inspect backup after preflight mismatch' }
    StartLive = { param($scriptPath) throw 'must not start after preflight mismatch' }
    AssertPostStart = { param($binary, $hash, $data, $port, $schema) throw 'must not post-check' }
    AssertRollbackHealth = { param($binary, $hash, $data, $port) throw 'must not rollback' }
  }
  Assert-ThrowsLike -Action {
    Invoke-MemoryV2Deployment -ManifestPath $manifestPath -CandidatePath $candidate `
      -ApprovedCandidateHash $approvedHash -LiveRoot $deployLive -BackupRoot $backupRoot `
      -Port 48901 -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -Operations $schemaMismatchOps
  } -Pattern 'preflight|schema|live database' `
    -Message 'Live schema mismatch did not fail before stop'
  Assert-Equal $schemaMismatchStopCalls 0 'Live schema mismatch reached StopLive'

  $events = [Collections.Generic.List[string]]::new()
  $oldHash = Get-TestHash $liveBinary
  $happyOps = @{
    InspectLive = {
      param($db, $expectedSchema)
      $events.Add('preflight-live')
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 3 }
    }
    StopLive = { param($binary, $data, $port) $events.Add('stop') }
    WaitStopped = { param($binary, $port) $events.Add('stopped') }
    InspectBackup = {
      param($db, $expectedSchema)
      $events.Add('immutable-backup')
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 3; baselineStable = $true }
    }
    StartLive = { param($scriptPath) $events.Add('start') }
    AssertPostStart = {
      param($binary, $hash, $data, $port, $expectedSchema)
      $events.Add('post-start')
      [PSCustomObject]@{ processPath = $binary; listener = $true; httpStatus = 200; quickCheck = 'ok'; schema = 4 }
    }
    AssertRollbackHealth = { param($binary, $hash, $data, $port) $events.Add('rollback-health') }
  }
  $deployResult = Invoke-MemoryV2Deployment -ManifestPath $manifestPath -CandidatePath $candidate `
    -ApprovedCandidateHash $approvedHash -LiveRoot $deployLive -BackupRoot $backupRoot `
    -Port 48901 -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -Operations $happyOps
  Assert-Equal $deployResult.Status 'PASS' 'Happy deployment did not return PASS'
  Assert-Equal (Get-TestHash $liveBinary) $approvedHash 'Happy deployment did not install candidate bytes'
  Assert-Equal ($events -join ',') 'preflight-live,stop,stopped,immutable-backup,start,post-start' `
    'Deployment gates ran in the wrong order'
  Assert-True (Test-Path -LiteralPath $deployResult.BackupDirectory) 'Deployment did not preserve backup directory'

  Write-TestText $liveBinary 'old-binary-v3-again'
  $oldHash = Get-TestHash $liveBinary
  $events.Clear()
  $rollbackOps = @{
    InspectLive = {
      param($db, $expectedSchema)
      $events.Add('preflight-live')
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 3 }
    }
    StopLive = { param($binary, $data, $port) $events.Add('stop') }
    WaitStopped = { param($binary, $port) $events.Add('stopped') }
    InspectBackup = {
      param($db, $expectedSchema)
      $events.Add('immutable-backup')
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 3; baselineStable = $true }
    }
    StartLive = { param($scriptPath) $events.Add('start') }
    AssertPostStart = {
      param($binary, $hash, $data, $port, $expectedSchema)
      $events.Add('post-start-fail')
      throw 'simulated exact process path mismatch'
    }
    AssertRollbackHealth = {
      param($binary, $hash, $data, $port)
      $events.Add('rollback-health')
      Assert-Equal (Get-TestHash $binary) $hash 'Rollback health ran before old hash was restored'
    }
  }
  Assert-ThrowsLike -Action {
    Invoke-MemoryV2Deployment -ManifestPath $manifestPath -CandidatePath $candidate `
      -ApprovedCandidateHash $approvedHash -LiveRoot $deployLive -BackupRoot $backupRoot `
      -Port 48901 -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -Operations $rollbackOps
  } -Pattern 'simulated exact process path mismatch' `
    -Message 'Post-start failure did not terminate deployment'
  Assert-Equal (Get-TestHash $liveBinary) $oldHash 'Post-start failure did not atomically restore old binary'
  Assert-Equal ($events -join ',') `
    'preflight-live,stop,stopped,immutable-backup,start,post-start-fail,stop,stopped,start,rollback-health' `
    'Post-start failure did not run complete rollback and bounded old health'

  Write-TestText $liveBinary 'old-binary-v3-third'
  $events.Clear()
  $backupFailOps = @{
    InspectLive = {
      param($db, $expectedSchema)
      $events.Add('preflight-live')
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 3 }
    }
    StopLive = { param($binary, $data, $port) $events.Add('stop') }
    WaitStopped = { param($binary, $port) $events.Add('stopped') }
    InspectBackup = { param($db, $expectedSchema) throw 'simulated immutable backup drift' }
    StartLive = { param($scriptPath) $events.Add('restart-old') }
    AssertPostStart = { param($binary, $hash, $data, $port) throw 'must not reach post-start' }
    AssertRollbackHealth = { param($binary, $hash, $data, $port) $events.Add('old-health') }
  }
  Assert-ThrowsLike -Action {
    Invoke-MemoryV2Deployment -ManifestPath $manifestPath -CandidatePath $candidate `
      -ApprovedCandidateHash $approvedHash -LiveRoot $deployLive -BackupRoot $backupRoot `
      -Port 48901 -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -Operations $backupFailOps
  } -Pattern 'simulated immutable backup drift' `
    -Message 'Immutable backup failure did not terminate deployment'
  Assert-Equal ($events -join ',') 'preflight-live,stop,stopped,restart-old,old-health' `
    'Pre-mutation backup failure did not restart and health-check the old binary'
  Assert-True ((Get-TestHash $liveBinary) -ne $approvedHash) `
    'Pre-mutation backup failure unexpectedly installed the candidate'

  Write-TestText $liveBinary 'old-binary-v3-stop-error'
  $events.Clear()
  $stopErrorOps = @{
    InspectLive = {
      param($db, $expectedSchema)
      $events.Add('preflight-live')
      [PSCustomObject]@{ quickCheck = 'ok'; schema = 3 }
    }
    StopLive = {
      param($binary, $data, $port)
      $events.Add('stop-error')
      throw 'simulated stop command error after daemon exited'
    }
    WaitStopped = { param($binary, $port) $events.Add('confirmed-stopped') }
    InspectBackup = { param($db, $expectedSchema) throw 'must not inspect after stop command error' }
    StartLive = { param($scriptPath) $events.Add('restart-old') }
    AssertPostStart = { param($binary, $hash, $data, $port) throw 'must not reach post-start' }
    AssertRollbackHealth = { param($binary, $hash, $data, $port) $events.Add('old-health') }
  }
  Assert-ThrowsLike -Action {
    Invoke-MemoryV2Deployment -ManifestPath $manifestPath -CandidatePath $candidate `
      -ApprovedCandidateHash $approvedHash -LiveRoot $deployLive -BackupRoot $backupRoot `
      -Port 48901 -ExpectedLiveSchema 3 -ExpectedTargetSchema 4 -Operations $stopErrorOps
  } -Pattern 'simulated stop command error after daemon exited' `
    -Message 'Stop command failure did not terminate deployment'
  Assert-Equal ($events -join ',') 'preflight-live,stop-error,confirmed-stopped,restart-old,old-health' `
    'Stop command failure after exit did not confirm stop, restart old binary, and check health'
  Write-Host 'PASS: deployment validates before stop, gates immutable backup, and rolls back post-start failures.'
} finally {
  if (Test-Path -LiteralPath $fixtureRoot) {
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force
  }
}
