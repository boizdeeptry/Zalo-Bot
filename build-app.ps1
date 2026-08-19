# Dung ban App de ban. Mot lenh, tu dau den cuoi.
#
#   pwsh -File .\build-app.ps1 -Repo <agentdc> -Out <package> -PersonaSource <persona>
#
# ============================================================================
# NGUYEN TAC DUY NHAT CUA TEP NAY: KHONG SUA REPO.
#
# Ban ban va ban dang chay khac nhau o vai cho -- ten san pham trong prompt,
# ten nguoi trong chu thich, tri thuc nganh. Cach SAI la sua truc tiep trong
# repo roi tra lai sau: mot lan quen tra lai la bot dang chay bi doi prompt, va
# khong co gi bao.
#
# Nen script nay COPY repo sang mot thu muc tam, sua o DO, build o DO, roi xoa.
# Repo khong bi cham mot byte nao. Kiem duoc: chay script roi `git status` phai
# sach y nhu truoc.
# ============================================================================
#   pwsh -File dist\build-app.ps1 -KeepData    # giu data\ de thu, KHONG de ban
#
# MAC DINH XOA data\, va day khong phai mot tuy chon cho tien.
#
# data\zalo\credentials.json LA phien Zalo cua may dung de build. Ai doc duoc tep
# do thi vao duoc tai khoan Zalo do. Neu build tren may co phien dang nhap roi nen
# ca thu muc de ban, nguoi mua nhan luon tai khoan Zalo cua nguoi ban.
#
# Do la ly do mac dinh la XOA: hong theo huong "phai quet lai QR" thi mat mot phut,
# hong theo huong kia thi mat tai khoan. Cua chan o buoc 6 tu choi ket thuc neu tep
# do con.
param(
  [Parameter(Mandatory)][string]$Repo,
  [Parameter(Mandatory)][string]$Out,
  [Parameter(Mandatory)][string]$PersonaSource,
  [switch]$KeepData,
  [switch]$KeepStage
)

$ErrorActionPreference = 'Stop'

Import-Module (Join-Path $PSScriptRoot 'scripts\BuildApp.psm1') -Force

function Resolve-BuildTool {
  param(
    [Parameter(Mandatory)][string]$Name,
    [string[]]$Candidates = @()
  )

  $command = Get-Command -Name $Name -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
  if ($command) { return $command.Source }
  foreach ($candidate in $Candidates) {
    if ($candidate -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
      return [IO.Path]::GetFullPath($candidate)
    }
  }
  throw "không tìm thấy công cụ build '$Name'"
}

function Invoke-AppCommand {
  param(
    [Parameter(Mandatory)][string]$Label,
    [Parameter(Mandatory)][string]$FilePath,
    [Parameter(Mandatory)][string[]]$Arguments,
    [Parameter(Mandatory)][string]$WorkingDirectory
  )

  Write-Host ("      > {0} {1}" -f $Label, ($Arguments -join ' '))
  Push-Location -LiteralPath $WorkingDirectory
  try {
    & $FilePath @Arguments
    $commandExitCode = $LASTEXITCODE
  } finally {
    Pop-Location
  }
  if ($commandExitCode -ne 0) {
    throw "$Label thất bại (exit $commandExitCode)"
  }
}

$paths = Resolve-BuildPaths -Repo $Repo -Out $Out
$Repo = $paths.Repo
$Out = $paths.Out
$PersonaSource = Resolve-PersonaSource -PersonaSource $PersonaSource
Assert-CleanGitSource -Repo $Repo | Out-Null
$personaSnapshot = Read-AppPersonaSourceSnapshot -PersonaSource $PersonaSource -Out $Out -ProtectedRoot @($Repo, $PSScriptRoot)

# Repo chi de DOC. Script nay khong nam trong no nua, co chu dich: nguon dong goi
# la thu rieng cua ban ban, va de no trong repo lam `git status` cua ban dang chay
# luon ban mot thu muc khong lien quan gi den bot dang tra loi khach.
$Tmp  = Join-Path $env:TEMP ('agentdc-app-' + [guid]::NewGuid().ToString('N').Substring(0, 8))

Write-Host "repo : $Repo"
Write-Host "goi  : $Out"
Write-Host "tam  : $Tmp"
Write-Host ''

$phase = 'khởi tạo stage'
$buildComplete = $false

try {

# ---------------------------------------------------------------- 1. ban tam
# Chi copy tep git theo doi. Bo node_modules (32 MB, dung lai ban co san), bo
# .git, bo moi thu khong commit -- ban ban phai dung tu nguon da biet.
Write-Host '[1/7] copy repo sang thu muc tam'
$phase = 'stage source và overlay'
Assert-CleanGitSource -Repo $Repo | Out-Null
$overlay = Join-Path $PSScriptRoot 'appmode\overlay'
$Tmp = New-AppStage -Repo $Repo -Overlay $overlay -StageRoot $Tmp
Apply-AppSeams -Stage $Tmp
Assert-CleanGitSource -Repo $Repo | Out-Null
$fileCount = (Get-ChildItem -LiteralPath $Tmp -Recurse -File).Count
Write-Host ("      {0} tep" -f $fileCount)

# ---------------------------------------------------------- 2. lam sach clone
# Nhung chuoi mang danh tinh mot doanh nghiep cu the, va chung o trong PROMPT
# nen chung di vao moi cau tra loi cua moi nguoi mua.
#
# Moi cap duoi day phai giu nguyen SUC NANG cua vi du goc. "2 hop loai 180mcg"
# van la mot so luong cong mot thong so doc duoc tren vo -- do la thu vi du do
# day (cu the du de khach biet bot da mo anh ra). Bo nhan hang khong lam no bot
# cu the.
Write-Host '[2/7] lam sach ban tam'
# BON MUC DANH TINH KHACH HANG DA DI NGUOC LEN THUONG NGUON 18/08/2026 -- da go.
#
# Chung tung thay 'MenaQ7', 'Anh Truong', 'boizdeeptry' bang ban trung tinh. Repo nguon gio
# khong con chuoi nao trong so do (kiem: 0 tep cho ca bon), nen Contains() tra false va cua
# 'throw' o cuoi vong lap DUNG CA BUILD -- do dung la thu da chan pipeline toi 18/08/2026.
#
# Go phan tiem, GIU cua chan: bao dam that khong nam o danh sach nay ma o Assert-AppPackage,
# thu quet GOI da build voi danh sach chuoi cam trong scripts/BuildApp.psm1 (~dong 211: MenaQ7,
# boizdeeptry, 'Anh Truong', 'Be Mi'). Cua do khong doi, nen mot ngay danh tinh quay lai nguon
# thi build van do -- chi khac la do o cong goi thay vi o day.
$subs = @(
  # Ten engine tien nhiem trong chu thich. Khong phai du lieu khach hang, nhung
  # no noi ra xuat xu -- va mot ban ban khong nen ke lai no hoc tu dau.
  @{ f = 'tuvan-zalo\src\listener.ts'
     a = '// Học từ engine cũ: nó chỉ lên tiếng khi được nhắc tên (khi được gọi tên bot), reply vào'
     b = '// Chỉ lên tiếng khi được nhắc tên, reply vào' },
  @{ f = 'tuvan-zalo\src\listener.ts'
     a = '// Học từ engine cũ: listen.mjs của nó gọi requestOldMessages(User) và (Group) trong handler'
     b = '// Zalo Web gọi requestOldMessages(User) và (Group) trong handler' },
  # zalo.js duoc NHUNG vao binary, nen quet tep trong goi khong bao gio thay chu
  # thich nay -- chi quet binary moi thay. Cua chan da bat dung cho nay.
  @{ f = 'internal\webui\static\zalo.js'
     a = '// Học từ engine cũ: "vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng'
     b = '// "Vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng' },
  # Chu thich SQL trong schema. Day la mot COMMENT nhung no nam trong mot chuoi Go,
  # nen no di vao binary -- khac han chu thich Go, thu bi trinh bien dich bo. Cua
  # chan quet binary la thu duy nhat thay duoc no, va no da bat dung cho nay.
  @{ f = 'internal\store\store.go'
     a = '-- Học từ engine cũ: "Cứ mỗi tin nhắn gửi vào nhóm thì vừa trả lời vừa xét có chắt lọc được gì'
     b = '-- Nguyên tắc: "Cứ mỗi tin nhắn gửi vào nhóm thì vừa trả lời vừa xét có chắt lọc được gì' }
)
# ---------------------------------------------------- portal quan ly cua goi
# `/` cua goi ban LA portal quan ly, khong phai portal dieu phoi agent.
#
# Nguoi mua khong mua phan dieu phoi agent lap trinh, nen thay ca index.html chu
# khong them mot route /manage: them route thi ton mot route, mot nut, va van con
# mot trang khong ai can o `/`. Nut trong zalo.html tro `/` vi the tu nhien dung.
# app.js cua portal cu khong con duoc nap, nhung no van nam trong assets. Bo di:
# mot tep 60 KB khong ai goi la mot tep nguoi doc code sau nay phai doan xem con
# dung khong.
Remove-Item -LiteralPath (Join-Path $Tmp 'internal\webui\static\app.js') -Force -EA SilentlyContinue

# ...VA BO LUON TEST CUA PORTAL DO. Xoa app.js ma giu lai test mo ta app.js thi cong test o
# duoi chac chan do.
#
# Da do 18/08/2026: 16 test that bai, 15 trong projectui_test.go va 1 trong provider_test.go,
# tat ca voi mot loi giong nhau -- open static/app.js: file does not exist -- hoac
# 'index.html does not reference provider.js' sau khi index.html bi thay bang ban Zalo.
#
# VI SAO XOA THEO TEP, khong them ten vao Get-AppGoTestSkipPattern: danh sach ten do di theo
# TUNG TEST, nen no lech ra ngay lan ke tiep co nguoi them mot test cho portal dieu phoi -- va
# do dung la chuyen da xay ra. Hai tep nay ton tai chi de mo ta portal dieu phoi agent: 50 test
# trong projectui_test.go doc app.js, app.css, provider.js, provider.css va index.html cua
# portal do, con provider_test.go doc hop thoai Provider cua no. Goi ban KHONG co portal do,
# nen ca hai tep mat nghia cung luc voi app.js. Mot luat ("stage bo tinh nang nao thi bo test
# cua tinh nang do") thay cho 16 dong.
#
# Kiem truoc khi xoa: bon helper cua projectui_test.go (asset, stripJSLineComments,
# stripCSSComments, jsBody) khong duoc tep test nao khac trong goi webui dung toi (do: 0 lan o
# provider_test.go, integrity_test.go, icons_test.go, feedsig_test.go), nen xoa khong lam vo
# buoc bien dich cua nhung test con lai. feedsig_test.go co doc app.js o dung mot cho, va cho
# do da nam trong skip-list theo ten tu truoc (TestModalCallsPassAnObject).
foreach ($portalTest in @(
  'internal\webui\projectui_test.go'
  'internal\webui\provider_test.go'
)) {
  $portalTestPath = Join-Path $Tmp $portalTest
  if (-not (Test-Path -LiteralPath $portalTestPath)) {
    # DUNG LAI thay vi bo qua, cung ly do nhu danh sach scrub: mot duong dan khong con dung
    # nghia la nguon da doi, va im lang se cho ra mot ban goi ma khong ai biet no da bo qua
    # dieu gi.
    throw "khong tim thay tep test can bo: $portalTest"
  }
  Remove-Item -LiteralPath $portalTestPath -Force
}

$subs += @(
  # Nut ve trang chu trong rail Zalo: goi ban khong co portal dieu phoi agent.
  @{ f = 'internal\webui\static\zalo.html'
     a = 'title="Về portal điều phối agent"'
     b = 'title="Về trang quản lý"' }
)

foreach ($s in $subs) {
  $p = Join-Path $Tmp $s.f
  $t = [IO.File]::ReadAllText($p)
  if (-not $t.Contains($s.a)) {
    # DUNG LAI, khong bo qua. Mot chuoi khong khop nghia la nguon da doi, va bo
    # qua se cho ra mot ban ban con mang ten khach hang ma khong ai biet.
    throw ("khong tim thay chuoi can thay trong {0}:`n  {1}" -f $s.f, $s.a)
  }
  [IO.File]::WriteAllText($p, $t.Replace($s.a, $s.b), (New-Object Text.UTF8Encoding $false))
}
Write-Host ("      {0} chuoi da thay" -f $subs.Count)

# ------------------------------------------------------------- 3. checkpoint
Write-Host '[3/7] kiem thu checkpoint truoc khi build'
$phase = 'checkpoint tests'
$programFiles = [Environment]::GetFolderPath('ProgramFiles')
$goCandidates = if ($programFiles) { @(Join-Path $programFiles 'Go\bin\go.exe') } else { @() }
$goExe = Resolve-BuildTool -Name 'go' -Candidates $goCandidates
$npmExe = Resolve-BuildTool -Name 'npm'

# node_modules dùng lại bản đã khoá dependency trong repo nguồn. Chỉ tạo junction trong stage;
# repo nguồn vẫn chỉ đọc và status được kiểm lại ngay sau staging ở trên.
$transportStage = Join-Path $Tmp 'tuvan-zalo'
$sourceNodeModules = Join-Path $Repo 'tuvan-zalo\node_modules'
if (-not (Test-Path -LiteralPath $sourceNodeModules -PathType Container)) {
  throw "thiếu dependencies Zalo ở '$sourceNodeModules'; chạy yarn install trong repo nguồn"
}
New-Item -ItemType Junction -Path (Join-Path $transportStage 'node_modules') `
  -Target $sourceNodeModules -ErrorAction Stop | Out-Null

$skipTests = Get-AppGoTestSkipPattern

Invoke-AppCommand -Label 'go test' -FilePath $goExe `
  -Arguments @('test', '-skip', $skipTests, './...') -WorkingDirectory $Tmp
Invoke-AppCommand -Label 'Portal test' -FilePath $npmExe `
  -Arguments @('--prefix', (Join-Path $PSScriptRoot 'appmode'), 'test') -WorkingDirectory $PSScriptRoot
Invoke-AppCommand -Label 'Zalo test compile' -FilePath $npmExe `
  -Arguments @('--prefix', $transportStage, 'run', 'build') -WorkingDirectory $Tmp
Invoke-AppCommand -Label 'Zalo test' -FilePath $npmExe `
  -Arguments @('--prefix', $transportStage, 'test') -WorkingDirectory $Tmp
Invoke-AppCommand -Label 'Zalo typecheck' -FilePath $npmExe `
  -Arguments @('--prefix', $transportStage, 'run', 'typecheck') -WorkingDirectory $Tmp

# ------------------------------------------------------------------ 4. build
Write-Host '[4/7] build binary va transport tu ban tam'
$phase = 'build binary và transport'
Invoke-AppCommand -Label 'go build' -FilePath $goExe `
  -Arguments @('build', '-o', (Join-Path $Tmp 'agentdc.exe'), './cmd/agentdc') -WorkingDirectory $Tmp
Invoke-AppCommand -Label 'Zalo build' -FilePath $npmExe `
  -Arguments @('--prefix', $transportStage, 'run', 'build') -WorkingDirectory $Tmp

# ----------------------------------------------------------- 5. lap thu muc
Write-Host '[5/7] lap thu muc goi'
$phase = 'lắp thư mục gói'
if (Test-Path -LiteralPath $Out) {
  if ($KeepData) {
    Write-Host '      -KeepData: giu data\ (CHI de thu, khong de ban)' -ForegroundColor Yellow
    Clear-AppOutput -Out $Out -ProtectedRoot @($Repo, $PersonaSource, $PSScriptRoot) `
      -ExpectedOutIdentity $paths.OutIdentity -KeepData
  } else {
    Clear-AppOutput -Out $Out -ProtectedRoot @($Repo, $PersonaSource, $PSScriptRoot) `
      -ExpectedOutIdentity $paths.OutIdentity
  }
}
# Bo cuc goc: CHI nhung gi nguoi mua can nhin.
#
#   Start.vbs  Stop.bat  README.txt   <- ba tep, mot cai de bam
#   brain\  data\                     <- cua ho
#   app\                              <- may moc, khong ai phai mo
#
# Vi sao don vao app\: de run.bat canh Start.vbs la moi nguoi mua bam nham vao cai
# mo cua so den, roi goi dien hoi vi sao. Mot thu muc giai quyet dieu do.
@(
  $Out
  (Join-Path $Out 'app')
  (Join-Path $Out 'app\node')
  (Join-Path $Out 'app\transport')
  (Join-Path $Out 'data')
) | ForEach-Object { [IO.Directory]::CreateDirectory($_) | Out-Null }

# brain: bo xuong Second Brain, KHONG hai thu muc rong.
#
# Truoc day goi giao dung `wiki\` va `raw\` rong. Ky thuat thi chay, nhung nguoi
# mua mo ra thay hai thu muc rong thi khong biet bo gi vao dau, va se bo tat ca
# vao mot cho. Bo xuong nay giao san wiki\{analyses,concepts,entities,sources,
# topics} cong CLAUDE.md giai thich tung tang -- do la khac biet giua "co cho de
# bo" va "biet bo the nao".
#
# Doc tu dist\brain-skeleton trong REPO, khong tu Downloads: mot ban build phai
# dung duoc sau khi thu muc Downloads bi don.
# Tao $Out\brain TRUOC: Copy-Item vao mot dich chua ton tai se coi dich la mot
# TEP, va bao "Container cannot be copied onto existing leaf item".
[IO.Directory]::CreateDirectory((Join-Path $Out 'brain')) | Out-Null
Get-ChildItem -LiteralPath (Join-Path $PSScriptRoot 'brain-skeleton') -Force | ForEach-Object {
  Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $Out 'brain') -Recurse -Force
}
[IO.Directory]::CreateDirectory((Join-Path $Out 'brain\reference\persona\overlay')) | Out-Null

Copy-Item -LiteralPath (Join-Path $Tmp 'agentdc.exe') -Destination (Join-Path $Out 'app') -Force
Copy-Item -LiteralPath (Join-Path $Tmp 'tuvan-zalo\dist') -Destination (Join-Path $Out 'app\transport') -Recurse -Force
Copy-Item -LiteralPath (Join-Path $Tmp 'tuvan-zalo\package.json') -Destination (Join-Path $Out 'app\transport') -Force
Copy-Item -LiteralPath (Join-Path $Repo 'tuvan-zalo\node_modules') -Destination (Join-Path $Out 'app\transport') -Recurse -Force
$portalAssetsOut = Join-Path $Out 'assets'
foreach ($asset in @(
    'components\provider-connect.js'
    'components\persona-fields.js'
    'pages\onboarding.js'
    'pages\settings.js'
  )) {
  $assetSource = Join-Path (Join-Path $Tmp 'internal\webui\static') $asset
  $assetDestination = Join-Path $portalAssetsOut $asset
  [IO.Directory]::CreateDirectory((Split-Path -Parent $assetDestination)) | Out-Null
  Copy-Item -LiteralPath $assetSource -Destination $assetDestination -Force
}
# Tia dev deps TRONG GOI, khong trong repo: repo con can typescript de build.
Invoke-AppCommand -Label 'npm prune' -FilePath $npmExe `
  -Arguments @('--prefix', (Join-Path $Out 'app\transport'), 'prune', '--omit=dev', '--silent') `
  -WorkingDirectory $Out

# node.exe + npm di kem: khong can trinh cai dat Node tren may nguoi mua.
#
# Vi sao KEM CA npm (khong chi node.exe): buoc "Ket noi" codex trong Portal chay
# `npm install -g @openai/codex` ngay tren may nguoi mua -- va may do co the KHONG
# cai Node. npm.cmd dung `%~dp0` (tro node.exe + node_modules\npm cung thu muc no)
# nen chi can dat ca ba canh nhau trong app\node la thanh mot ban node+npm doc lap,
# di chuyen duoc. run.bat chen app\node len dau PATH, va tro npm_config_prefix vao
# data\cli, nen runtime tu chua du va `npm root -g` tra dung noi daemon tim codex.js.
# Rieng thao tac Connect/cai Codex hoac Claude van can ket noi online toi npm registry;
# viec dong goi npm khong bien mot lan cai package moi thanh thao tac offline.
$node = (Get-Command node).Source
$nodeSrcDir = Split-Path -Parent $node
$nodeOut = Join-Path $Out 'app\node'
Copy-Item -LiteralPath $node -Destination $nodeOut -Force
foreach ($shim in 'npm', 'npm.cmd', 'npx', 'npx.cmd') {
  Copy-Item -LiteralPath (Join-Path $nodeSrcDir $shim) -Destination $nodeOut -Force
}
$npmModuleSrc = Join-Path $nodeSrcDir 'node_modules\npm'
if (-not (Test-Path -LiteralPath $npmModuleSrc -PathType Container)) {
  throw "thieu node_modules\npm o '$npmModuleSrc' -- ban Node nguon phai kem npm"
}
$nodeModulesOut = Join-Path $nodeOut 'node_modules'
[IO.Directory]::CreateDirectory($nodeModulesOut) | Out-Null
Copy-Item -LiteralPath $npmModuleSrc -Destination $nodeModulesOut -Recurse -Force

# ------------------------------------------------------- 6. persona chung hoa
Write-Host '[6/7] persona: đóng gói bản hoàn chỉnh và bất biến'
$phase = 'đóng gói persona và launcher'
# Ten tep ASCII co chu dich: .bat doc theo codepage OEM chu khong UTF-8, nen mot
# duong dan tieng Viet trong Chay.bat se bien dang va daemon khong doc duoc.
# reference\persona\, KHONG brain\persona\: khop dung vi tri ban dev dung, va
# reference\ la thu muc bo xuong danh cho thu NGUOI doc chu khong phai bot trich
# dan. Persona phai o ngoai moi goc KB (wiki, raw) -- mot tep trong goc KB thi
# bot trich dan duoc no, va van phong khong phai can cu.
Write-AppPersonaPackage -Snapshot $personaSnapshot -Out $Out

# Ba tep khoi chay: giu ban trong dist\launcher, copy vao goi.
Get-ChildItem -LiteralPath (Join-Path $PSScriptRoot 'launcher') -Force | ForEach-Object {
  Copy-Item -LiteralPath $_.FullName -Destination $Out -Recurse -Force
}

# --------------------------------------------------------------- 7. do goi
Write-Host '[7/7] kiem tra goi va nguon'
$phase = 'package gates'

Write-Host ''
# Cua chan quan trong nhat trong tep nay. Phien Zalo la mot credential, khong mot
# tep du lieu -- nen no khong bao gio duoc ra khoi may nay.
$cred = Join-Path $Out 'data\zalo\credentials.json'
if (Test-Path -LiteralPath $cred) {
  if ($KeepData) {
    # CANH BAO, khong chan. -KeepData ton tai de giu phien qua nhieu lan build khi dang thu, va
    # neu cua chan chan ca truong hop nay thi co do vo dung -- do la loi cua BAN DAU: no bao
    # chặn ngay sau khi vừa khuyên dùng -KeepData.
    Write-Host 'CANH BAO: goi dang chua PHIEN ZALO cua may nay (vi -KeepData).' -ForegroundColor Yellow
    Write-Host ('  ' + $cred) -ForegroundColor Yellow
    Write-Host '  Ban nay CHI de thu. Build lai KHONG kem -KeepData truoc khi nen de ban.'
  } else {
    # Khong co -KeepData thi data\ da bi xoa o buoc 4, nen tep nay khong the ton tai. Con no
    # nghia la mot buoc nao do da hong -- va cai hong do dan tin phien Zalo ra ngoai.
    Write-Host 'DUNG LAI: goi con chua PHIEN ZALO cua may nay.' -ForegroundColor Red
    Write-Host ('  ' + $cred) -ForegroundColor Red
    Write-Host '  Ai doc duoc tep do thi vao duoc tai khoan Zalo do. KHONG duoc nen thu muc nay de ban.'
    throw 'package contains Zalo credentials'
  }
}
Assert-AppPackage -Out $Out -AllowZaloCredentials:$KeepData | Out-Null
Assert-CleanGitSource -Repo $Repo | Out-Null
Assert-AppPersonaPackagePrivacy -Out $Out -Snapshot $personaSnapshot
Write-Host 'sach: persona va danh tinh chi nam trong dung tep da khai bao' -ForegroundColor Green

$f = Get-ChildItem -LiteralPath $Out -Recurse -File
Write-Host ('goi: {0} tep, {1:N1} MB  ->  {2}' -f $f.Count, (($f | Measure-Object Length -Sum).Sum / 1MB), $Out)
$buildComplete = $true
} catch {
  Write-Host ("THẤT BẠI ở phase '{0}': {1}" -f $phase, $_.Exception.Message) -ForegroundColor Red
  if (Test-Path -LiteralPath $Tmp) {
    Write-Host ("stage được giữ để kiểm tra: {0}" -f $Tmp) -ForegroundColor Yellow
  }
  throw
} finally {
  if ($buildComplete -and -not $KeepStage -and (Test-Path -LiteralPath $Tmp)) {
    Remove-Item -LiteralPath $Tmp -Recurse -Force -ErrorAction Stop
  } elseif ($buildComplete -and $KeepStage -and (Test-Path -LiteralPath $Tmp)) {
    Write-Host ("giữ stage theo -KeepStage: {0}" -f $Tmp)
  }
}
