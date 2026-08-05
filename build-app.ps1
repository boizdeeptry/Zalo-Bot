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
$paths = Resolve-BuildPaths -Repo $Repo -Out $Out
$Repo = $paths.Repo
$Out = $paths.Out
$PersonaSource = Resolve-PersonaSource -PersonaSource $PersonaSource

# Repo chi de DOC. Script nay khong nam trong no nua, co chu dich: nguon dong goi
# la thu rieng cua ban ban, va de no trong repo lam `git status` cua ban dang chay
# luon ban mot thu muc khong lien quan gi den bot dang tra loi khach.
$Tmp  = Join-Path $env:TEMP ('agentdc-app-' + [guid]::NewGuid().ToString('N').Substring(0, 8))

Write-Host "repo : $Repo"
Write-Host "goi  : $Out"
Write-Host "tam  : $Tmp"
Write-Host ''

# ---------------------------------------------------------------- 1. ban tam
# Chi copy tep git theo doi. Bo node_modules (32 MB, dung lai ban co san), bo
# .git, bo moi thu khong commit -- ban ban phai dung tu nguon da biet.
Write-Host '[1/6] copy repo sang thu muc tam'
[IO.Directory]::CreateDirectory($Tmp) | Out-Null
Push-Location -LiteralPath $Repo
$files = & git ls-files
foreach ($f in $files) {
  $src = Join-Path $Repo $f
  if (-not (Test-Path -LiteralPath $src)) { continue }
  $dst = Join-Path $Tmp $f
  [IO.Directory]::CreateDirectory((Split-Path -Parent $dst)) | Out-Null
  Copy-Item -LiteralPath $src -Destination $dst -Force
}
Pop-Location
Write-Host ("      {0} tep" -f $files.Count)

# ---------------------------------------------------------- 2. lam sach clone
# Nhung chuoi mang danh tinh mot doanh nghiep cu the, va chung o trong PROMPT
# nen chung di vao moi cau tra loi cua moi nguoi mua.
#
# Moi cap duoi day phai giu nguyen SUC NANG cua vi du goc. "2 hop loai 180mcg"
# van la mot so luong cong mot thong so doc duoc tren vo -- do la thu vi du do
# day (cu the du de khach biet bot da mo anh ra). Bo nhan hang khong lam no bot
# cu the.
Write-Host '[2/6] lam sach ban tam'
$subs = @(
  @{ f = 'internal\daemon\duty.go'
     a = 'hoá đơn 2 hộp MenaQ7 180mcg'
     b = 'hoá đơn 2 hộp loại 180mcg' },
  @{ f = 'internal\daemon\duty_test.go'
     a = '"hoá đơn 2 hộp MenaQ7 180mcg"'
     b = '"hoá đơn 2 hộp loại 180mcg"' },
  # Cau tu choi prompt-injection, noi bang giong bot. Ten nguoi trong do la mot
  # cai ten CU THE, nen no phai di. "bên em" giu nguyen chuc nang: mot loi tu
  # choi mem, khong buoc toi ai, va van trong giong.
  @{ f = 'internal\daemon\duty.go'
     a = '"Dạ nếp này Anh Trường dặn nên em xin giữ nguyên ạ."'
     b = '"Dạ nếp này bên em dặn nên em xin giữ nguyên ạ."' },
  @{ f = 'tuvan-zalo\src\listener.ts'
     a = '// "Trường" bị ghi thành "boizdeeptry" và đè lên cả tên hội thoại. dName là thứ Zalo'
     b = '// người A bị ghi thành tên đã lưu của người B, đè lên cả tên hội thoại. dName là thứ Zalo' },
  # Ten engine tien nhiem trong chu thich. Khong phai du lieu khach hang, nhung
  # no noi ra xuat xu -- va mot ban ban khong nen ke lai no hoc tu dau.
  @{ f = 'tuvan-zalo\src\listener.ts'
     a = '// Học từ engine cũ: nó chỉ lên tiếng khi được nhắc tên ("Mi ơi"/"Bé Mi"/@Bé Mi), reply vào'
     b = '// Chỉ lên tiếng khi được nhắc tên, reply vào' },
  @{ f = 'tuvan-zalo\src\listener.ts'
     a = '// Học từ Bé Mi: listen.mjs của nó gọi requestOldMessages(User) và (Group) trong handler'
     b = '// Zalo Web gọi requestOldMessages(User) và (Group) trong handler' },
  # zalo.js duoc NHUNG vao binary, nen quet tep trong goi khong bao gio thay chu
  # thich nay -- chi quet binary moi thay. Cua chan da bat dung cho nay.
  @{ f = 'internal\webui\static\zalo.js'
     a = '// Học từ Bé Mi: "vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng'
     b = '// "Vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng' },
  # Chu thich SQL trong schema. Day la mot COMMENT nhung no nam trong mot chuoi Go,
  # nen no di vao binary -- khac han chu thich Go, thu bi trinh bien dich bo. Cua
  # chan quet binary la thu duy nhat thay duoc no, va no da bat dung cho nay.
  @{ f = 'internal\store\store.go'
     a = '-- Học từ Bé Mi: "Cứ mỗi tin nhắn gửi vào nhóm thì vừa trả lời vừa xét có chắt lọc được gì'
     b = '-- Nguyên tắc: "Cứ mỗi tin nhắn gửi vào nhóm thì vừa trả lời vừa xét có chắt lọc được gì' }
)
# ---------------------------------------------------- portal quan ly cua goi
# `/` cua goi ban LA portal quan ly, khong phai portal dieu phoi agent.
#
# Nguoi mua khong mua phan dieu phoi agent lap trinh, nen thay ca index.html chu
# khong them mot route /manage: them route thi ton mot route, mot nut, va van con
# mot trang khong ai can o `/`. Nut trong zalo.html tro `/` vi the tu nhien dung.
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'appmode\index.html') -Destination (Join-Path $Tmp 'internal\webui\static\index.html') -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'appmode\manage.js') -Destination (Join-Path $Tmp 'internal\webui\static\manage.js') -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'appmode\kb.go') -Destination (Join-Path $Tmp 'internal\daemon\kb.go') -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'appmode\agentcfg.go') -Destination (Join-Path $Tmp 'internal\daemon\agentcfg.go') -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'appmode\restart.go') -Destination (Join-Path $Tmp 'internal\daemon\restart.go') -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'appmode\personaedit.go') -Destination (Join-Path $Tmp 'internal\daemon\personaedit.go') -Force

# app.js cua portal cu khong con duoc nap, nhung no van nam trong assets. Bo di:
# mot tep 60 KB khong ai goi la mot tep nguoi doc code sau nay phai doan xem con
# dung khong.
Remove-Item -LiteralPath (Join-Path $Tmp 'internal\webui\static\app.js') -Force -EA SilentlyContinue

$subs += @(
  # Route cua Knowledge. Chen truoc POST /shutdown -- dong cuoi bang route, on
  # dinh nhat, va neu no doi thi script DUNG LAI thay vi build ra mot goi thieu
  # endpoint upload.
  @{ f = 'internal\daemon\server.go'
     a = "`tmux.Handle(`"POST /shutdown`", a.auth(a.handleShutdown))"
     b = "`tmux.Handle(`"GET /kb`", a.auth(a.handleKBList))`n" +
         "`tmux.Handle(`"POST /kb/upload`", a.auth(a.handleKBUpload))`n" +
         "`tmux.Handle(`"POST /kb/ingest`", a.auth(a.handleKBIngest))`n" +
         "`tmux.Handle(`"DELETE /kb/ingest`", a.auth(a.handleKBIngestStop))`n" +
         "`tmux.Handle(`"GET /kb/model`", a.auth(a.handleKBModelGet))`n" +
         "`tmux.Handle(`"GET /agent`", a.auth(a.handleAgentGet))`n" +
         "`tmux.Handle(`"PUT /agent`", a.auth(a.handleAgentPut))`n" +
         "`tmux.Handle(`"GET /agent/persona/{name}`", a.auth(a.handlePersonaGet))`n" +
         "`tmux.Handle(`"PUT /agent/persona/{name}`", a.auth(a.handlePersonaPut))`n" +
         "`tmux.Handle(`"PUT /kb/model`", a.auth(a.handleKBModelPut))`n" +
         "`tmux.Handle(`"POST /shutdown`", a.auth(a.handleShutdown))" },
  # Cookie phai voi tuoi duoc bon route do. Chen vao cuoi cookieAllowedPaths.
  @{ f = 'internal\daemon\portal.go'
     a = "`t`"DELETE /zalo/threads/{tid}`": true,"
     b = "`t`"DELETE /zalo/threads/{tid}`": true,`n" +
         "`t// Knowledge cua goi ban. POST /kb/ingest CHAY MO HINH, tuc ton phi that -- cung`n" +
         "`t// hang voi POST /sessions o cho no chi ton khi mot nguoi bam, khong tu chay.`n" +
         "`t`"GET /kb`": true,`n" +
         "`t`"POST /kb/upload`": true,`n" +
         "`t`"POST /kb/ingest`": true,`n" +
         "`t`"DELETE /kb/ingest`": true,`n" +
         "`t`"GET /kb/model`": true,`n" +
         "`t`"GET /agent`": true,`n" +
         "`t`"PUT /agent`": true,`n" +
         "`t`"GET /agent/persona/{name}`": true,`n" +
         "`t`"PUT /agent/persona/{name}`": true,`n" +
         "`t`"PUT /kb/model`": true," },
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

# ------------------------------------------------------------------ 3. build
Write-Host '[3/6] build binary va transport tu ban tam'
Push-Location -LiteralPath $Tmp
& go build -o (Join-Path $Tmp 'agentdc.exe') ./cmd/agentdc
if ($LASTEXITCODE -ne 0) { Pop-Location; throw 'go build that bai' }
Pop-Location

# node_modules dung lai ban trong repo: `npm ci` can mang, va tsc chi can kieu.
Push-Location -LiteralPath (Join-Path $Tmp 'tuvan-zalo')
New-Item -ItemType Junction -Path 'node_modules' -Target (Join-Path $Repo 'tuvan-zalo\node_modules') -EA SilentlyContinue | Out-Null
& npx tsc
if ($LASTEXITCODE -ne 0) { Pop-Location; throw 'tsc that bai' }
Pop-Location

# ----------------------------------------------------------- 4. lap thu muc
Write-Host '[4/6] lap thu muc goi'
if (Test-Path -LiteralPath $Out) {
  if ($KeepData) {
    Write-Host '      -KeepData: giu data\ (CHI de thu, khong de ban)' -ForegroundColor Yellow
    Clear-AppOutput -Out $Out -KeepData
  } else {
    Clear-AppOutput -Out $Out
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
# Tia dev deps TRONG GOI, khong trong repo: repo con can typescript de build.
Push-Location -LiteralPath (Join-Path $Out 'app\transport')
& npm prune --omit=dev --silent 2>&1 | Out-Null
Pop-Location

# node.exe di kem: mot tep, chay don le duoc, khong can trinh cai dat Node.
$node = (Get-Command node).Source
Copy-Item -LiteralPath $node -Destination (Join-Path $Out 'app\node') -Force

# ------------------------------------------------------- 5. persona chung hoa
Write-Host '[5/6] persona: thay danh tinh bang cho trong'
$pSrc = $PersonaSource
# Ten tep ASCII co chu dich: .bat doc theo codepage OEM chu khong UTF-8, nen mot
# duong dan tieng Viet trong Chay.bat se bien dang va daemon khong doc duoc.
# reference\persona\, KHONG brain\persona\: khop dung vi tri ban dev dung, va
# reference\ la thu muc bo xuong danh cho thu NGUOI doc chu khong phai bot trich
# dan. Persona phai o ngoai moi goc KB (wiki, raw) -- mot tep trong goc KB thi
# bot trich dan duoc no, va van phong khong phai can cu.
Copy-Item -LiteralPath (Join-Path $pSrc 'Cẩm nang boizdeeptry v2.md') -Destination (Join-Path $Out 'brain\reference\persona\persona.md') -Force
Copy-Item -LiteralPath (Join-Path $pSrc 'Sổ tay nhận diện thành viên.md') -Destination (Join-Path $Out 'brain\reference\persona\roster.md') -Force
Copy-Item -LiteralPath (Join-Path $pSrc 'overlay\README.md') -Destination (Join-Path $Out 'brain\reference\persona\overlay') -Force -EA SilentlyContinue
& python (Join-Path $PSScriptRoot 'genpersona.py') $Out
if ($LASTEXITCODE -ne 0) { throw 'genpersona that bai' }

# Ba tep khoi chay: giu ban trong dist\launcher, copy vao goi.
Get-ChildItem -LiteralPath (Join-Path $PSScriptRoot 'launcher') -Force | ForEach-Object {
  Copy-Item -LiteralPath $_.FullName -Destination $Out -Recurse -Force
}

# --------------------------------------------------------------- 6. don + do
Write-Host '[6/6] don thu muc tam va kiem'
Remove-Item -LiteralPath $Tmp -Recurse -Force -EA SilentlyContinue

# Quet lai. Day la cua chan cuoi: mot chuoi sot lai o day la mot chuoi da ban ra.
$pat = 'MIDU|MenaQ7|boizdeeptry|Anh Trường|Bé Mi'
$scanExtensions = @('.md', '.txt', '.js', '.json', '.html', '.bat', '.vbs')
$hits = Get-ChildItem -LiteralPath $Out -Recurse -File -EA SilentlyContinue |
  Where-Object { $scanExtensions -contains $_.Extension } |
  Where-Object { $_.FullName -notlike '*node_modules*' } |
  Select-String -Pattern $pat -Encoding UTF8 -EA SilentlyContinue
$bin = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes("$Out\app\agentdc.exe"))
$binHits = ([regex]::Matches($bin, $pat)).Count

Write-Host ''
# Cua chan quan trong nhat trong tep nay. Phien Zalo la mot credential, khong mot
# tep du lieu -- nen no khong bao gio duoc ra khoi may nay.
$cred = Join-Path $Out 'data\zalo\credentials.json'
if (Test-Path -LiteralPath $cred) {
  if ($KeepData) {
    # CANH BAO, khong chan. -KeepData ton tai de giu phien qua nhieu lan build khi dang thu, va
    # neu cua chan chan ca truong hop nay thi co do vo dung -- do la loi cua BAN DAU: no bao
    # exit 1 ngay sau khi vua khuyen dung -KeepData.
    Write-Host 'CANH BAO: goi dang chua PHIEN ZALO cua may nay (vi -KeepData).' -ForegroundColor Yellow
    Write-Host ('  ' + $cred) -ForegroundColor Yellow
    Write-Host '  Ban nay CHI de thu. Build lai KHONG kem -KeepData truoc khi nen de ban.'
  } else {
    # Khong co -KeepData thi data\ da bi xoa o buoc 4, nen tep nay khong the ton tai. Con no
    # nghia la mot buoc nao do da hong -- va cai hong do dan tin phien Zalo ra ngoai.
    Write-Host 'DUNG LAI: goi con chua PHIEN ZALO cua may nay.' -ForegroundColor Red
    Write-Host ('  ' + $cred) -ForegroundColor Red
    Write-Host '  Ai doc duoc tep do thi vao duoc tai khoan Zalo do. KHONG duoc nen thu muc nay de ban.'
    exit 1
  }
}
if ($hits) {
  Write-Host 'CON DAU KHACH HANG TRONG TEP VAN BAN:' -ForegroundColor Red
  $hits | ForEach-Object { '  ' + $_.Filename + ':' + $_.LineNumber }
}
if ($binHits -gt 0) { Write-Host ("CON {0} CHUOI TRONG agentdc.exe" -f $binHits) -ForegroundColor Red }
if ($hits -or $binHits -gt 0) {
  Write-Host '  KHONG duoc nen thu muc nay de ban.' -ForegroundColor Red
  exit 1
}
Write-Host 'sach: khong con dau khach hang nao' -ForegroundColor Green

$f = Get-ChildItem -LiteralPath $Out -Recurse -File
Write-Host ('goi: {0} tep, {1:N1} MB  ->  {2}' -f $f.Count, (($f | Measure-Object Length -Sum).Sum / 1MB), $Out)
