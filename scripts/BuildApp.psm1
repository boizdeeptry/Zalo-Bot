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

function Assert-CleanGitSource {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Repo)

  $repoPath = [IO.Path]::GetFullPath($Repo)
  $status = (& git -C $repoPath status --porcelain=v1 --untracked-files=all) -join "`n"
  if ($LASTEXITCODE -ne 0) {
    throw "không đọc được Git status của '$repoPath'"
  }
  if (-not [string]::IsNullOrWhiteSpace($status)) {
    throw "repo nguồn không sạch; commit hoặc cất thay đổi trước khi build: '$repoPath'`n$status"
  }

  return $repoPath
}

function Get-AppGoTestSkipPattern {
  [CmdletBinding()]
  param()

  $names = @(
    'TestAppJSKnowsTheSessionEndedCloseReason'
    'TestAppJSSendsThePortalMutationHeader'
    'TestPortalReloadedKeyWithLiveSessionReachesTheShell'
    'TestPortalRootServesTheShellWithACookie'
    'TestPortalUsesModalNotBrowserDialogs'
    'TestAgentPortalNoLongerCarriesZalo'
    'TestModalCallsPassAnObject'
    # Base test cua upstream engine ma overlay dao nguoc trong stage. No-default: mot bot CHUA cau
    # hinh provider thi IM (khong con Claude mac dinh), nen loi chao khi nhap nhom KHONG phat cho toi
    # khi co provider — dung nhu seam runner + appZaloRunner dua moi luot qua chuoi fallback. Base-only
    # AgentDC (khong seam) van chay va van xanh test nay, nen hanh vi engine goc van co bao phu.
    'TestJoinGreetsOnceForEveryone'
  )
  $escaped = $names | ForEach-Object { [regex]::Escape($_) }
  return '^(?:' + ($escaped -join '|') + ')$'
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

  $trackedOutput = & git -C $repoPath ls-files -z
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
  $dutyPath = Join-Path $stagePath 'internal\daemon\duty.go'

  $server = [IO.File]::ReadAllText($serverPath)
  $store = [IO.File]::ReadAllText($storePath)
  $zalo = [IO.File]::ReadAllText($zaloPath)
  $duty = [IO.File]::ReadAllText($dutyPath)
  $serverNewline = if ($server.Contains("`r`n")) { "`r`n" } else { "`n" }
  $storeNewline = if ($store.Contains("`r`n")) { "`r`n" } else { "`n" }
  $zaloNewline = if ($zalo.Contains("`r`n")) { "`r`n" } else { "`n" }

  Assert-SignatureAbsent -Text $server -Signature 'a.registerAppRoutes(mux)' -Label 'route seam'
  Assert-SignatureAbsent -Text $store -Signature 'migrateApp(db)' -Label 'migration seam'
  Assert-SignatureAbsent -Text $zalo -Signature 'evaluateAppWorkflow(req, msg)' -Label 'workflow seam'
  Assert-SignatureAbsent -Text $duty -Signature 'a.appZaloRunner(' -Label 'runner seam'

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

  # Runner seam: mỗi lượt Zalo đi qua chuỗi fallback đang lưu thay vì thẳng tới Claude Code.
  # Thay ngay tại tham số runner của lời gọi, nên không có dòng nào chèn thêm và cấu hình cùng
  # tệp đính kèm của chính lượt đó vẫn là thứ quyết định đường đi.
  $runnerNeedle = 'a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)'
  $runnerReplacement = 'a.answerZalo(ctx, deps.cfg, a.appZaloRunner(deps.cfg, deps.run, threadID, ' +
    'len(files) > 0), threadID, question, step, reply, files...)'
  $dutyUpdated = Replace-ExactlyOnce -Text $duty -Needle $runnerNeedle `
    -Replacement $runnerReplacement -Label 'runner seam'

  # Replace-ExactlyOnce chỉ chứng minh duty.go có đúng một lượt trả lời, không chứng minh cả cây
  # có. Một đường trả lời thứ hai ở tệp khác là cách DUY NHẤT seam này hỏng lặng lẽ: nó vẫn áp,
  # và đường mới đi thẳng tới Claude Code không qua định tuyến. Đếm sau khi Replace chạy, để một
  # duty.go có hai lời gọi vẫn báo bằng thông điệp của Replace.
  $callSites = (Get-ChildItem (Join-Path $stagePath 'internal\daemon') -Filter '*.go' |
    Where-Object { -not $_.Name.EndsWith('_test.go') } |
    ForEach-Object { [regex]::Matches([IO.File]::ReadAllText($_.FullName), 'a\.answerZalo\(').Count } |
    Measure-Object -Sum).Sum
  if ($callSites -ne 1) { throw "runner seam: answerZalo has $callSites call sites, expected 1" }

  $utf8NoBom = [Text.UTF8Encoding]::new($false)
  [IO.File]::WriteAllText($serverPath, $serverUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($storePath, $storeUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($zaloPath, $zaloUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($dutyPath, $dutyUpdated, $utf8NoBom)
}

# Khoá thử nghiệm dùng chung của mọi tầng test: llmPackageCanary trong app_llm_router_test.go,
# llmAPIKey/llmAPINextKey trong app_llm_api_test.go, CANARY_KEY trong providers.test.mjs.
#
# Quét một chuỗi mà KHÔNG tệp nào trong repo mang thì luôn ra 0 lần khớp, kể cả khi cửa chặn đã
# hỏng — một phép kiểm như thế trông như đang canh cửa mà không canh gì. Chuỗi này nằm trong
# fixture của cả ba tầng, nên nó có đường thật vào gói: một hằng test bị nhấc lên tệp sản xuất,
# hay một khoá dán vào trang Portal (assets nhúng thẳng vào agentdc.exe).
#
# Tệp này KHÔNG đi vào stage lẫn vào gói (nó thuộc repo đóng gói, không thuộc repo nguồn), nên
# chuỗi ở đây không tự làm cửa chặn đỏ.
$script:ProviderCredentialCanary = 'sk-package-must-never-contain-7f36d2'

# Assert-NoProviderCredential từ chối một gói còn mang khoá Provider thử nghiệm.
#
# MỌI tệp ngoài node_modules đều bị quét — không có danh sách trắng đuôi tệp. Bản đầu có một danh
# sách như thế và nó bỏ sót brain\.claude\settings.local.json.example, tệp cấu hình DUY NHẤT của
# gói: gieo canary vào đó thì cửa chặn báo sạch. Một danh sách trắng phải đoán trước mọi đuôi tệp
# tương lai, và mỗi lần đoán thiếu là một lần cửa xanh nhầm. node_modules mới là thứ gánh phần
# giới hạn: 1209 tệp của gói chỉ có 52 tệp nằm ngoài nó.
#
# Tách khỏi Assert-AppPackage để hàm kia đọc được trong một màn hình, nhưng nó chạy MỖI lần
# Assert-AppPackage chạy và không nhận công tắc bỏ qua — một cửa chặn phải nhớ bật thì không phải
# cửa chặn. Không xuất khẩu: build-app.ps1 và test đều đi qua Assert-AppPackage, vì "mỗi lần build
# có quét không" mới là câu hỏi, và chỉ lời gọi ngoài đó trả lời được.
function Assert-NoProviderCredential {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Package,
    [string]$Canary = $script:ProviderCredentialCanary
  )

  $packagePath = [IO.Path]::GetFullPath($Package)
  if (-not (Test-Path -LiteralPath $packagePath -PathType Container)) {
    throw "không thấy gói ở '$packagePath'"
  }

  # -Force là BẮT BUỘC: không có nó, Get-ChildItem lặng lẽ bỏ qua tệp ẩn và mọi thứ nằm dưới một
  # thư mục ẩn. Đó không phải chuyện lý thuyết — cả ba chỗ chép vào gói đều dùng Copy-Item -Force
  # và Copy-Item giữ nguyên thuộc tính Hidden, còn đúng lớp tệp hay mang khoá thật (.env, .npmrc,
  # .claude\settings.local.json) lại là lớp mà công cụ Windows thỉnh thoảng đánh dấu ẩn. Một bit
  # thuộc tính là đủ để cửa chặn báo sạch trên một gói đang mang khoá.
  #
  # -ErrorAction Stop tại CHỖ GỌI chứ không dựa vào $ErrorActionPreference của người gọi: một tệp
  # không liệt kê hay không đọc được mà bị nuốt lặng thì phép quét trả về 0 lần khớp y hệt một gói
  # sạch, và tính chất đó quá quan trọng để nằm trong một biến ở ngoài hàm này. Đường .exe bên dưới
  # đã hỏng-đóng sẵn: ngoại lệ của một phương thức .NET luôn là lỗi kết thúc.
  #
  # Loại trừ xét trên đường dẫn TƯƠNG ĐỐI so với gốc gói, và xét cả dấu phân cách. Hai chi tiết,
  # hai lỗ khác nhau: so trên đường dẫn tuyệt đối thì một gói dựng vào ...\node_modules\... nào đó
  # tự loại trừ SẠCH mọi tệp của chính nó, còn so chuỗi con không có dấu '\' thì
  # brain\node_modules-notes\ cũng được miễn. Lệch với cửa quét dấu khách hàng ở build-app.ps1 là
  # cố ý — cửa này canh credential, và một thư mục đặt tên gần giống không được là chỗ trốn.
  $files = Get-ChildItem -LiteralPath $packagePath -Recurse -File -Force -ErrorAction Stop |
    Where-Object { [IO.Path]::GetRelativePath($packagePath, $_.FullName) -notlike '*node_modules\*' }

  # .exe đi đường byte chứ không qua Select-String: đó là hai tệp cỡ chục MB gần như không có dấu
  # xuống dòng, và đọc chúng theo dòng là dựng một chuỗi khổng lồ để tìm đúng một chuỗi con. Cùng
  # lý do khiến đường này phải tồn tại: assets Portal được go:embed vào agentdc.exe, nên một khoá
  # dán vào pages/providers.js không nằm trong tệp văn bản nào của gói — chỉ ở trong binary.
  $binaries, $texts = ($files | Where-Object { $_.Extension -eq '.exe' }),
                      ($files | Where-Object { $_.Extension -ne '.exe' })

  $hits = $texts | Select-String -SimpleMatch -Pattern $Canary -Encoding UTF8 -ErrorAction Stop
  $binaryHits = @($binaries | Where-Object {
    [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($_.FullName)).
      IndexOf($Canary, [StringComparison]::Ordinal) -ge 0
  })

  if ($hits -or $binaryHits) {
    # Chỉ đường dẫn và số dòng, KHÔNG in dòng khớp: dòng đó chính là bí mật đang bị tố cáo, và
    # thông báo hỏng này đi vào log build, tức đi xa hơn cái gói.
    $where = @($hits | ForEach-Object { '{0}:{1}' -f $_.Path, $_.LineNumber }) +
             @($binaryHits | ForEach-Object { $_.FullName })
    throw ('package contains plaintext provider credential: ' + ($where -join '; '))
  }

  return $packagePath
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

  # Không có công tắc bỏ qua, khác cửa Zalo ở trên: -KeepData tồn tại vì giữ phiên đăng nhập qua
  # nhiều lần build là việc hợp lệ lúc đang thử, còn một khoá Provider trong gói thì không bao giờ.
  Assert-NoProviderCredential -Package $outPath | Out-Null

  return $outPath
}

Export-ModuleMember -Function Resolve-BuildPaths, Assert-CleanGitSource, Get-AppGoTestSkipPattern, Clear-AppOutput, Resolve-PersonaSource, New-AppStage, Apply-AppSeams, Assert-AppPackage
