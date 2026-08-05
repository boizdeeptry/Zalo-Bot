# Dung ban persona CHUNG cho goi ban: giu HANH VI, thay DANH TINH bang cho trong.
#
#   python genpersona.py <thu-muc-goi>
#
# Chi sua ban trong goi. F:\brain khong bi cham.
#
# Vi sao khong bo han persona: no LA thu duoc ban. Cach noi, do dai cau, khi nao tach thanh
# nhieu tin, khi nao chuyen cho nguoi that -- 315 dong day la san pham. Phan rieng cua mot
# doanh nghiep chi la hai cai TEN va mot vi du nganh, va do la dung ba thu duoc thay o day.
import io
import os
import re
import sys

if len(sys.argv) < 2:
    sys.exit('thieu tham so: python genpersona.py <thu-muc-goi>')
SRC = os.path.join(sys.argv[1], 'brain', 'reference', 'persona', 'persona.md')
if not os.path.exists(SRC):
    sys.exit('khong thay ' + SRC)

s = io.open(SRC, encoding='utf-8').read()
before = len(s.split('\n'))

# 1. Bo khoi ghi chu noi bo o dau file: no tro vao mot tep khong di kem, va no khung ca cam
#    nang thanh "ban lam viec chua chot" -- dung thu khong nen co trong mot san pham ban ra.
s = re.sub(
    r'> Đây là bản làm việc.*?dòng chỉ dẫn cho mô hình\.\n\n',
    '> Đây là cẩm nang văn phong của bot. Toàn bộ nội dung ở đây được nạp vào prompt như chỉ dẫn,\n'
    '> nên một dòng ghi chú cho người đọc cũng thành một dòng chỉ dẫn cho mô hình. Sửa nội dung là\n'
    '> sửa cách bot nói, có hiệu lực ngay ở lượt trả lời sau.\n\n',
    s, flags=re.S)

# 2. Danh tinh -> cho trong.
subs = [
    ('boizdeeptry', '{{TEN_BOT}}'),
    ('Anh Trường', '{{TEN_CHUYEN_GIA}}'),
    # 3. Vi du mang tri thuc nganh -> vi du dung REGISTER nhung khong mang nganh nao. Giu hinh
    #    dang (mot khang dinh cu the, noi bang van noi) vi do la thu cau nay day.
    ('nước trung tính mới là nước cơ thể cần hằng ngày',
     'cái quan trọng là dùng đều, không phải dùng nhiều'),
    ('"theo bác sĩ Phúc", "theo bác sĩ Ngọc"', '"theo bác sĩ A", "theo chuyên gia B"'),
]
for a, b in subs:
    s = s.replace(a, b)

# 4. Bo dau banh rang va cac dong "cho chot": mot cam nang ban ra khong the mang cau hoi mo cua
#    doi ngu khac.
s = s.replace('## 9. Phạm vi quyền (⚙️ còn chờ người quyết)', '## 9. Phạm vi quyền')
s = re.sub(r'\*\*⚙️ Các điểm chờ chốt:\*\*.*?(?=\n\n|\Z)',
           '**Ba điểm phải tự quyết trước khi mở bot cho khách thật:** (1) phạm vi quyền, có được\n'
           'chốt đơn và báo giá không; (2) quy tắc chuyển người thật, rộng hay hẹp; (3) nguồn bảng\n'
           'giá chính thức để báo giá mà không bịa.', s, flags=re.S)
s = s.replace(' \u2699\ufe0f', '').replace('\u2699\ufe0f', '').replace('\u2699', '')

# Cua chan: thieu cho trong nghia la nguon da doi ten, va ghi de se cho ra mot ban ban con mang
# ten khach hang. Dung lai thay vi ghi.
for need in ('{{TEN_BOT}}', '{{TEN_CHUYEN_GIA}}'):
    if need not in s:
        sys.exit('KHONG thay ' + need + ' sau khi thay -- dung lai, khong ghi de')
for bad in ('boizdeeptry', 'Anh Tr\u01b0\u1eddng', '\u2699'):
    if bad in s:
        sys.exit('con sot ' + repr(bad) + ' -- dung lai')

io.open(SRC, 'w', encoding='utf-8', newline='\n').write(s)

# roster va overlay: cung phep thay, khong co cua chan vi chung co the khong nhac ten nao
for extra in (os.path.join(sys.argv[1], 'brain', 'reference', 'persona', 'roster.md'),
              os.path.join(sys.argv[1], 'brain', 'reference', 'persona', 'overlay', 'README.md')):
    if not os.path.exists(extra):
        continue
    t = io.open(extra, encoding='utf-8').read()
    for a, b in subs[:2]:
        t = t.replace(a, b)
    # Ten engine tien nhiem. Mot ban ban khong nen ke lai no hoc tu dau.
    t = t.replace('Học từ Bé Mi.', 'Học từ một engine đi trước.').replace('Bé Mi', 'engine đi trước')
    io.open(extra, 'w', encoding='utf-8', newline='\n').write(t)

print('      persona: %d -> %d dong, %d cho trong'
      % (before, len(s.split('\n')), s.count('{{TEN_BOT}}') + s.count('{{TEN_CHUYEN_GIA}}')))
