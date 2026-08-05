import io
import sys

P = r'F:\dist\_build\build-app.ps1'
s = io.open(P, encoding='utf-8').read()

OLD = """$cred = Join-Path $Out 'data\\zalo\\credentials.json'
if (Test-Path $cred) {
  Write-Host 'DUNG LAI: goi con chua PHIEN ZALO cua may nay.' -ForegroundColor Red
  Write-Host ('  ' + $cred) -ForegroundColor Red
  Write-Host '  Ai doc duoc tep do thi vao duoc tai khoan Zalo do. KHONG duoc nen thu muc nay de ban.'
  Write-Host '  Chay lai script khong kem -KeepData de xoa data\\.'
  exit 1
}"""

NEW = """$cred = Join-Path $Out 'data\\zalo\\credentials.json'
if (Test-Path $cred) {
  if ($KeepData) {
    # CANH BAO, khong chan. -KeepData ton tai de giu phien qua nhieu lan build khi dang thu, va
    # neu cua chan chan ca truong hop nay thi co do vo dung -- do la loi cua BAN DAU: no bao
    # exit 1 ngay sau khi vua khuyen dung -KeepData.
    Write-Host 'CANH BAO: goi dang chua PHIEN ZALO cua may nay (vi -KeepData).' -ForegroundColor Yellow
    Write-Host ('  ' + $cred) -ForegroundColor Yellow
    Write-Host '  Ban nay CHI de thu. Build lai KHONG kem -KeepData truoc khi nen de ban.'
  } else {
    # Khong co -KeepData thi data\\ da bi xoa o buoc 4, nen tep nay khong the ton tai. Con no
    # nghia la mot buoc nao do da hong -- va cai hong do dan tin phien Zalo ra ngoai.
    Write-Host 'DUNG LAI: goi con chua PHIEN ZALO cua may nay.' -ForegroundColor Red
    Write-Host ('  ' + $cred) -ForegroundColor Red
    Write-Host '  Ai doc duoc tep do thi vao duoc tai khoan Zalo do. KHONG duoc nen thu muc nay de ban.'
    exit 1
  }
}"""

if OLD not in s:
    sys.exit('khong khop cua chan')
io.open(P, 'w', encoding='utf-8', newline='\n').write(s.replace(OLD, NEW, 1))
print('cua chan: -KeepData gio canh bao thay vi chan')
