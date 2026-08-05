# Chuyen index.md vao wiki\ de BOT DOC DUOC no.
#
# Loi do duoc: goc KB cua bot la brain\wiki va brain\raw. index.md nam o brain\, tuc NGOAI ca hai.
# Nhung CLAUDE.md:79 quy dinh luong tim kiem la "Read index.md first to find candidate pages" --
# nen cai muc luc duoc thiet ke lam cua vao thi bot khong voi tay den.
#
# Hau qua: bot phai Grep mo mam khap wiki\ thay vi doc mot tep roi biet co nhung trang nao.
#
# Vi sao KHONG them brain\ thanh goc KB thu ba: lam vay thi brain\reference\persona nam trong goc
# KB, va bot TRICH DAN duoc van phong. Van phong khong phai can cu tra loi khach. Ranh gioi do dat
# hon mot muc luc.
#
# Nen chuyen tep. Ca hai agent deu thay: agent bien soan co quyen ghi ca brain\ nen no ghi duoc,
# agent tra loi khach co goc KB la wiki\ nen no doc duoc.
import io
import os
import sys

D = r'F:\dist\_build\brain-skeleton'

src = os.path.join(D, 'index.md')
dst = os.path.join(D, 'wiki', 'index.md')
if os.path.exists(src):
    body = io.open(src, encoding='utf-8').read()
    io.open(dst, 'w', encoding='utf-8', newline='\n').write(body)
    os.remove(src)
    print('chuyen index.md -> wiki/index.md')
elif os.path.exists(dst):
    print('index.md da o wiki/ roi')
else:
    sys.exit('khong thay index.md o dau ca')

# Sua moi cho tro vao index.md. Khong dung replace tho tren ca tep: "index.md" xuat hien trong ca
# cay thu muc ve, va o do no phai doi CHO chu khong doi ten.
C = os.path.join(D, 'CLAUDE.md')
c = io.open(C, encoding='utf-8').read()
pairs = [
    ('- `index.md` — content catalog (what pages exist, organized by category)',
     '- `wiki/index.md` — content catalog (what pages exist, organized by category). Lives INSIDE\n'
     '  `wiki/` on purpose: the answering bot is granted `wiki/` and `raw/` only, so a catalog at the\n'
     '  vault root would be unreachable by the very agent it exists to help.'),
    ('├── index.md               # content catalog', '├── wiki/index.md         # content catalog'),
    ('7. **Update `index.md`**', '7. **Update `wiki/index.md`**'),
    ('1. **Read `index.md`** first to find candidate pages.',
     '1. **Read `wiki/index.md`** first to find candidate pages.'),
    ("aren't in `index.md`", "aren't in `wiki/index.md`"),
    ('## index.md format', '## wiki/index.md format'),
]
miss = [a for a, _ in pairs if a not in c]
if miss:
    sys.exit('khong khop trong CLAUDE.md:\n  ' + '\n  '.join(miss))
for a, b in pairs:
    c = c.replace(a, b)
io.open(C, 'w', encoding='utf-8', newline='\n').write(c)
print('CLAUDE.md: %d cho da tro sang wiki/index.md' % len(pairs))

# README cua brain: cay thu muc phai dung.
R = os.path.join(D, 'README.md')
r = io.open(R, encoding='utf-8').read()
OLDTREE = """wiki/
  sources/           tóm tắt từng nguồn"""
NEWTREE = """wiki/
  index.md           MỤC LỤC — bot đọc tệp này trước để biết có những trang nào
  sources/           tóm tắt từng nguồn"""
if OLDTREE not in r:
    sys.exit('khong khop cay trong README.md')
r = r.replace(OLDTREE, NEWTREE, 1)
OLDIDX = 'index.md             mục lục\n'
if OLDIDX in r:
    r = r.replace(OLDIDX, '')
io.open(R, 'w', encoding='utf-8', newline='\n').write(r)
print('brain/README.md: cay thu muc da sua')
