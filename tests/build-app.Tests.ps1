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

# Bản sao của canary trong BuildApp.psm1, cố ý KHÔNG đọc lại từ module: hai chuỗi lệch nhau thì
# fixture dưới đây gieo chuỗi này còn cửa chặn tìm chuỗi kia, không ai ném lỗi, và Assert-ThrowsLike
# đỏ ngay. Tức bản sao này tự canh chính nó.
$providerCanary = 'sk-package-must-never-contain-7f36d2'

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
  $gotPackage = Assert-AppPackage -Out $packageRoot

  # Gieo canary rồi gọi lại chính Assert-AppPackage, KHÔNG gọi thẳng Assert-NoProviderCredential:
  # câu hỏi ở đây là "mỗi lần build có quét không", và build-app.ps1 chỉ gọi hàm ngoài. Một phép
  # quét đúng mà không được cắm vào cửa nào vẫn để gói mang khoá đi bán.
  #
  # Hỏng ở CẢ HAI đường, vì chúng canh hai chỗ khác nhau và một đường gãy không làm đường kia đỏ.
  # README.txt nằm trong danh sách trắng đuôi tệp; agentdc.exe thì không, nên chỉ lượt đọc binary
  # thấy nó.
  $readme = Join-Path $gotPackage 'README.txt'
  Write-TestFile $readme ("huong dan`n" + $providerCanary + "`n")
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'plaintext provider credential' -Message 'A package text asset carrying the canary was accepted'
  Write-TestFile $readme "fixture`n"

  $binary = Join-Path $gotPackage 'app\agentdc.exe'
  [IO.File]::WriteAllBytes($binary, [Text.Encoding]::UTF8.GetBytes("MZ`0" + $providerCanary))
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'plaintext provider credential' -Message 'A binary carrying the canary was accepted'
  Write-TestFile $binary "fixture`n"

  # Đuôi tệp mà một danh sách trắng sẽ bỏ sót, và đây là tệp CÓ THẬT: bản đầu của cửa chặn liệt
  # kê mười đuôi văn bản và để lọt brain\.claude\settings.local.json.example, tệp cấu hình duy
  # nhất của gói nằm ngoài node_modules.
  $config = Join-Path $gotPackage 'brain\.claude\settings.local.json.example'
  Write-TestFile $config ('{"canary":"' + $providerCanary + '"}' + "`n")
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'plaintext provider credential' -Message 'A package config file carrying the canary was accepted'
  Remove-Item -LiteralPath $config -Force

  # Tệp ẩn và tệp dưới thư mục ẩn: Get-ChildItem thiếu -Force bỏ qua cả hai KHÔNG một tiếng động.
  # Đường vào có thật chứ không phải giả định — cả ba chỗ chép vào gói đều dùng Copy-Item -Force và
  # nó giữ nguyên thuộc tính Hidden, còn đúng lớp tệp hay mang khoá (.env, .npmrc, settings.local)
  # là lớp mà công cụ Windows thỉnh thoảng đánh dấu ẩn.
  $hidden = Join-Path $gotPackage 'app\.env'
  Write-TestFile $hidden ('KEY=' + $providerCanary + "`n")
  (Get-Item -LiteralPath $hidden -Force).Attributes = [IO.FileAttributes]::Hidden
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'plaintext provider credential' -Message 'A hidden package file carrying the canary was accepted'
  Remove-Item -LiteralPath $hidden -Force

  $hiddenDir = Join-Path $gotPackage 'app\.config'
  Write-TestFile (Join-Path $hiddenDir 'keys.json') ('{"k":"' + $providerCanary + '"}' + "`n")
  (Get-Item -LiteralPath $hiddenDir -Force).Attributes = [IO.FileAttributes]::Directory -bor [IO.FileAttributes]::Hidden
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'plaintext provider credential' -Message 'A file under a hidden directory carrying the canary was accepted'
  Remove-Item -LiteralPath $hiddenDir -Recurse -Force

  # Hỏng-đóng phải là tính chất của CHÍNH hàm quét, không phải của $ErrorActionPreference mà người
  # gọi tình cờ đang đặt. Hạ nó xuống 'Continue' quanh lời gọi là cách duy nhất phân biệt hai điều
  # đó: đo được là thiếu -ErrorAction Stop tại chỗ gọi thì một tệp không đọc được chỉ sinh lỗi
  # không kết thúc, phép quét bỏ qua tệp ấy và gói báo SẠCH.
  #
  # Đặt ở scope của tệp test chứ không bên trong scriptblock của Assert-ThrowsLike: đặt bên trong
  # thì hàm trong module không thấy, và phép kiểm này xanh cả khi cửa chặn đã hỏng.
  $locked = Join-Path $gotPackage 'app\locked.txt'
  Write-TestFile $locked "khong doc duoc`n"
  $handle = [IO.File]::Open($locked, 'Open', 'Read', 'None')
  $savedPreference = $ErrorActionPreference
  try {
    $ErrorActionPreference = 'Continue'
    Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
      -Pattern 'locked\.txt' -Message 'An unreadable package file was skipped instead of failing the scan'
  } finally {
    $ErrorActionPreference = $savedPreference
    $handle.Close()
  }
  Remove-Item -LiteralPath $locked -Force
  Assert-AppPackage -Out $packageRoot | Out-Null

  Write-TestFile (Join-Path $packageRoot 'data\zalo\credentials.json') "secret`n"
  Assert-ThrowsLike -Action { Assert-AppPackage -Out $packageRoot } `
    -Pattern 'Zalo credentials' -Message 'A package containing Zalo credentials was accepted'

  Write-Host 'PASS: Assert-AppPackage requires runtime files and rejects credentials.'
  Write-Host 'PASS: Assert-AppPackage finds a provider credential in text, config, binary, hidden files, and fails closed on an unreadable one.'
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

func trigger() {
	err := a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)
}
'@
  # Test upstream gọi answerZalo hàng chục lần. Chúng có mặt ở đây để cửa đếm chỗ gọi phải thật
  # sự loại _test.go ra — bỏ sót thì cửa đó đỏ ngay trên repo thật và không ai build được.
  Write-TestFile (Join-Path $repo 'internal\daemon\duty_test.go') @'
package daemon

func TestAnswer(t *testing.T) {
	_ = a.answerZalo(ctx, zc, run, "t1", "câu hỏi", nil, ipc.ZaloOutboxDraft{})
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
  $dutyBefore = [IO.File]::ReadAllText((Join-Path $repo 'internal\daemon\duty.go'))

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
  $runnerSeam = 'a\.appZaloRunner\(deps\.cfg, deps\.run, threadID, len\(files\) > 0\)'
  if ([regex]::Matches($stagedDuty, $runnerSeam).Count -ne 1) { throw 'Runner seam was not applied exactly once' }
  # Chuỗi gọi phải còn nguyên hình dạng cũ quanh chỗ chèn: một seam khớp đúng chữ nhưng đặt sai
  # chỗ vẫn đếm ra 1, và cách duy nhất phân biệt là đọc cả lời gọi.
  $expectedCall = 'a.answerZalo(ctx, deps.cfg, a.appZaloRunner(deps.cfg, deps.run, threadID, ' +
    'len(files) > 0), threadID, question, step, reply, files...)'
  if ($stagedDuty.IndexOf($expectedCall, [StringComparison]::Ordinal) -lt 0) {
    throw 'Runner seam did not rewrite the answerZalo call in place'
  }
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $gotStage } `
    -Pattern 'route seam: inserted signature already present' -Message 'Applying seams twice was accepted'
  if ([regex]::Matches([IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\server.go')), 'a\.registerAppRoutes\(mux\)').Count -ne 1) {
    throw 'A repeated seam application duplicated the route call'
  }

  $sourceServer = [IO.File]::ReadAllText((Join-Path $repo 'internal\daemon\server.go'))
  if ($sourceServer -match 'registerAppRoutes') { throw 'Source repo was changed by staging' }
  Assert-Equal ([IO.File]::ReadAllText((Join-Path $repo 'internal\daemon\duty.go'))) $dutyBefore `
    'Source duty.go was changed by staging'
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

  $duplicateRunnerStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Trùng runner')
  $duplicateDutyPath = Join-Path $duplicateRunnerStage 'internal\daemon\duty.go'
  $duplicateDuty = [IO.File]::ReadAllText($duplicateDutyPath)
  $duplicateDuty += "`n`ta.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)`n"
  [IO.File]::WriteAllText($duplicateDutyPath, $duplicateDuty, [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $duplicateRunnerStage } `
    -Pattern 'runner seam: expected exactly 1 match' -Message 'A duplicate runner marker was accepted'

  # Chữ ký đã có sẵn phải chặn TRƯỚC khi Replace chạy, và nó phải chặn của CHÍNH seam này: ba
  # seam kia đều sạch ở stage này, nên nếu Apply-AppSeams đi qua được thì cửa chặn duty không có.
  $appliedRunnerStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Runner đã áp')
  $appliedDutyPath = Join-Path $appliedRunnerStage 'internal\daemon\duty.go'
  [IO.File]::WriteAllText($appliedDutyPath, ([IO.File]::ReadAllText($appliedDutyPath).Replace(
    'deps.run, threadID', 'a.appZaloRunner(deps.cfg, deps.run, threadID, len(files) > 0), threadID')),
    [Text.UTF8Encoding]::new($false))
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $appliedRunnerStage } `
    -Pattern 'runner seam: inserted signature already present' -Message 'An already-patched duty.go was accepted'

  # Một lượt trả lời thứ hai ở tệp KHÁC là cách duy nhất seam này hỏng lặng lẽ: duty.go vẫn khớp
  # đúng một lần, seam vẫn áp, và đường mới đi thẳng tới Claude Code không qua định tuyến.
  $driftStage = New-AppStage -Repo $repo -Overlay $overlay -StageRoot (Join-Path $stageTestRoot 'Hai chỗ gọi')
  Write-TestFile (Join-Path $driftStage 'internal\daemon\duty2.go') @'
package daemon

func triggerAgain() {
	_ = a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply)
}
'@
  Assert-ThrowsLike -Action { Apply-AppSeams -Stage $driftStage } `
    -Pattern 'runner seam: answerZalo has 2 call sites' -Message 'A second answerZalo call site was accepted'

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

  Write-Host 'PASS: staging copies tracked files and applies four guarded seams.'
} finally {
  if (Test-Path -LiteralPath $stageTestRoot) {
    Remove-Item -LiteralPath $stageTestRoot -Recurse -Force
  }
}
