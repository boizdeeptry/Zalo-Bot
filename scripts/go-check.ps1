# Vong lap Go nhanh cho code trong appmode\overlay.
#
#   pwsh -NoProfile -File .\scripts\go-check.ps1
#   pwsh -NoProfile -File .\scripts\go-check.ps1 -Run TestLLMRoute
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
  # Mac dinh: thu muc AgentDC nam canh repo nay.
  [string]$Repo = (Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'AgentDC'),
  # Loc test theo regex, giong `go test -run`. Bo trong = chay het.
  [string]$Run
)

$ErrorActionPreference = 'Stop'
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
