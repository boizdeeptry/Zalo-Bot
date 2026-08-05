import io
import sys

B = r'F:\dist\_build\build-app.ps1'
s = io.open(B, encoding='utf-8').read()

# 1. Chu thich trong zalo.js -- tep nay NHUNG vao binary nen quet tep khong thay no.
#    Do la ly do phai quet ca binary, va cua chan da bat dung cho nay.
OLD = """  @{ f = 'tuvan-zalo\\src\\listener.ts'
     a = '// Học từ Bé Mi: listen.mjs của nó gọi requestOldMessages(User) và (Group) trong handler'
     b = '// Zalo Web gọi requestOldMessages(User) và (Group) trong handler' }
)"""
NEW = """  @{ f = 'tuvan-zalo\\src\\listener.ts'
     a = '// Học từ Bé Mi: listen.mjs của nó gọi requestOldMessages(User) và (Group) trong handler'
     b = '// Zalo Web gọi requestOldMessages(User) và (Group) trong handler' },
  # zalo.js duoc NHUNG vao binary, nen quet tep trong goi khong bao gio thay chu
  # thich nay -- chi quet binary moi thay. Cua chan da bat dung cho nay.
  @{ f = 'internal\\webui\\static\\zalo.js'
     a = '// Học từ Bé Mi: "vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng'
     b = '// "Vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng' }
)"""
if OLD not in s:
    sys.exit('khong khop khoi subs')
s = s.replace(OLD, NEW, 1)

# 2. Cua chan danh tinh phai CHAN, khong chi canh bao.
#
#    Bat can xung o ban dau: cua chan phien Zalo exit 1, con cua chan danh tinh chi in do roi
#    exit 0. Nen mot ban con ten khach hang van build "thanh cong" -- va do dung la thu khong
#    duoc phep ra khoi may.
OLD_END = """if (-not $hits -and $binHits -eq 0) { Write-Host 'sach: khong con dau khach hang nao' -ForegroundColor Green }"""
NEW_END = """if ($hits -or $binHits -gt 0) {
  Write-Host '  KHONG duoc nen thu muc nay de ban.' -ForegroundColor Red
  exit 1
}
Write-Host 'sach: khong con dau khach hang nao' -ForegroundColor Green"""
if OLD_END not in s:
    sys.exit('khong khop dong ket cua cua chan')
s = s.replace(OLD_END, NEW_END, 1)

io.open(B, 'w', encoding='utf-8', newline='\n').write(s)
print('build-app.ps1: them sub cho zalo.js, va cua chan danh tinh gio CHAN')
