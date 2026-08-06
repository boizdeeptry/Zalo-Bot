# Vong lap Go nhanh cho code trong appmode\overlay.
#
#   pwsh -NoProfile -File .\scripts\go-check.ps1
#   pwsh -NoProfile -File .\scripts\go-check.ps1 -Run TestLLMRoute
#
# Duong dan repo nguon lay tu $env:ZALOBOT_REPO (xem README.md). Truyen -Repo de de len.
#
# Code Go trong overlay KHONG compile duoc tai cho: no chi ton tai khi duoc chep de len mot ban
# sao cua repo nguon. build-app.ps1 lam viec do roi chay test, nhung no chay tiep den tan dong goi
# -- ~3 phut va mot goi 94MB moi lan. Trong mot vong TDD thi phan sau khong tra loi cau hoi nao.
#
# Tep nay dung DUNG hai ham build-app.ps1 dung (New-AppStage, Apply-AppSeams) roi dung lai o
# `go test`: ~40 giay. No KHONG thay the build-app.ps1 -- cua chan Portal, Zalo, quet dau khach
# hang va package gate chi co o do, nen chay full build truoc khi commit.
#
# Repo nguon chi duoc DOC. Stage nam trong TEMP va bi xoa khi test xanh; test do thi giu lai de
# con mo ra xem.
[CmdletBinding()]
param(
  # Duong dan repo nguon (AgentDC). Bo trong = doc $env:ZALOBOT_REPO.
  #
  # KHONG suy ra tu vi tri script: ban dau tep nay lam the, va trong mot worktree
  # ($repo\.worktrees\<nhanh>) phep suy do tro ra $repo\.worktrees\AgentDC -- khong ton tai, va
  # thong bao loi noi ve mot duong dan chua ai go bao gio. Bien moi truong noi that va di theo
  # tung may, nen repo push len khong mang duong dan cua ai ca.
  [string]$Repo = $env:ZALOBOT_REPO,
  # Loc test theo regex, giong `go test -run`. Bo trong = chay het.
  [string]$Run
)

$ErrorActionPreference = 'Stop'

if (-not $Repo) {
  throw "thiếu repo nguồn: đặt `$env:ZALOBOT_REPO trỏ tới checkout AgentDC, hoặc truyền -Repo. Xem README.md."
}
if (-not (Test-Path -LiteralPath $Repo -PathType Container)) {
  throw "không thấy repo nguồn ở '$Repo' (từ `$env:ZALOBOT_REPO hoặc -Repo)"
}
Import-Module (Join-Path $PSScriptRoot 'BuildApp.psm1') -Force -WarningAction SilentlyContinue

$root = Split-Path -Parent $PSScriptRoot
$overlay = Join-Path $root 'appmode\overlay'

# Stage chi chep tep git THEO DOI cua repo nguon, nen mot thay doi chua commit o do se bi bo qua
# lang le. Dung lai thay vi de nguoi doc ngoi doan vi sao sua ma khong thay gi doi.
Assert-CleanGitSource -Repo $Repo | Out-Null

$stage = Join-Path $env:TEMP ('gocheck-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
$stage = New-AppStage -Repo $Repo -Overlay $overlay -StageRoot $stage
Apply-AppSeams -Stage $stage

$goArgs = @('test', '-skip', (Get-AppGoTestSkipPattern))
if ($Run) { $goArgs += @('-run', $Run) }
$goArgs += './...'

Push-Location $stage
try {
  & go @goArgs
  $code = $LASTEXITCODE
} finally {
  Pop-Location
}

if ($code -eq 0) {
  Remove-Item -LiteralPath $stage -Recurse -Force
  Write-Host 'go: PASS  (chay build-app.ps1 truoc khi commit)' -ForegroundColor Green
} else {
  Write-Host ("go: FAIL -- stage duoc giu de kiem tra: {0}" -f $stage) -ForegroundColor Red
}
exit $code
