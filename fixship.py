import io
import sys

# 1. build-app.ps1: cua chan cuoi PHAI quet ca "Be Mi".
#
# Do la ly do ba dong lot qua: mau cua no la MIDU|MenaQ7|boizdeeptry|Anh Truong, khong co ten
# engine tien nhiem. Mot cua chan thieu mot muc thi no bao "sach" mot cach tu tin.
B = r'F:\dist\_build\build-app.ps1'
s = io.open(B, encoding='utf-8').read()
OLD_PAT = "$pat = 'MIDU|MenaQ7|boizdeeptry|Anh Trường'"
NEW_PAT = "$pat = 'MIDU|MenaQ7|boizdeeptry|Anh Trường|Bé Mi'"
if OLD_PAT not in s:
    sys.exit('khong khop mau quet')
s = s.replace(OLD_PAT, NEW_PAT, 1)

# 2. Hai chu thich trong listener.ts nhac ten engine tien nhiem.
OLD_SUBS = """  @{ f = 'tuvan-zalo\\src\\listener.ts'
     a = '// "Trường" bị ghi thành "boizdeeptry" và đè lên cả tên hội thoại. dName là thứ Zalo'
     b = '// người A bị ghi thành tên đã lưu của người B, đè lên cả tên hội thoại. dName là thứ Zalo' }
)"""
NEW_SUBS = """  @{ f = 'tuvan-zalo\\src\\listener.ts'
     a = '// "Trường" bị ghi thành "boizdeeptry" và đè lên cả tên hội thoại. dName là thứ Zalo'
     b = '// người A bị ghi thành tên đã lưu của người B, đè lên cả tên hội thoại. dName là thứ Zalo' },
  # Ten engine tien nhiem trong chu thich. Khong phai du lieu khach hang, nhung
  # no noi ra xuat xu -- va mot ban ban khong nen ke lai no hoc tu dau.
  @{ f = 'tuvan-zalo\\src\\listener.ts'
     a = '// Học từ engine cũ: nó chỉ lên tiếng khi được nhắc tên ("Mi ơi"/"Bé Mi"/@Bé Mi), reply vào'
     b = '// Chỉ lên tiếng khi được nhắc tên, reply vào' },
  @{ f = 'tuvan-zalo\\src\\listener.ts'
     a = '// Học từ Bé Mi: listen.mjs của nó gọi requestOldMessages(User) và (Group) trong handler'
     b = '// Zalo Web gọi requestOldMessages(User) và (Group) trong handler' }
)"""
if OLD_SUBS not in s:
    sys.exit('khong khop khoi $subs cua listener.ts')
s = s.replace(OLD_SUBS, NEW_SUBS, 1)
io.open(B, 'w', encoding='utf-8', newline='\n').write(s)
print('build-app.ps1: mau quet + 2 chu thich listener.ts')

# 3. genpersona.py: thay ca "Be Mi" trong roster va overlay README.
G = r'F:\dist\_build\genpersona.py'
g = io.open(G, encoding='utf-8').read()
OLD_EX = """    t = io.open(extra, encoding='utf-8').read()
    for a, b in subs[:2]:
        t = t.replace(a, b)"""
NEW_EX = """    t = io.open(extra, encoding='utf-8').read()
    for a, b in subs[:2]:
        t = t.replace(a, b)
    # Ten engine tien nhiem. Mot ban ban khong nen ke lai no hoc tu dau.
    t = t.replace('Học từ Bé Mi.', 'Học từ một engine đi trước.').replace('Bé Mi', 'engine đi trước')"""
if OLD_EX not in g:
    sys.exit('khong khop vong extra trong genpersona.py')
io.open(G, 'w', encoding='utf-8', newline='\n').write(g.replace(OLD_EX, NEW_EX, 1))
print('genpersona.py: thay ten engine trong roster + overlay')
