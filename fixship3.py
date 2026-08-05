import io
import sys

B = r'F:\dist\_build\build-app.ps1'
s = io.open(B, encoding='utf-8').read()

OLD = """  @{ f = 'internal\\webui\\static\\zalo.js'
     a = '// Học từ Bé Mi: "vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng'
     b = '// "Vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng' }
)"""
NEW = """  @{ f = 'internal\\webui\\static\\zalo.js'
     a = '// Học từ Bé Mi: "vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng'
     b = '// "Vừa trả lời vừa xét có chắt lọc được gì không", để vòng trực sau kế thừa vòng' },
  # Chu thich SQL trong schema. Day la mot COMMENT nhung no nam trong mot chuoi Go,
  # nen no di vao binary -- khac han chu thich Go, thu bi trinh bien dich bo. Cua
  # chan quet binary la thu duy nhat thay duoc no, va no da bat dung cho nay.
  @{ f = 'internal\\store\\store.go'
     a = '-- Học từ Bé Mi: "Cứ mỗi tin nhắn gửi vào nhóm thì vừa trả lời vừa xét có chắt lọc được gì'
     b = '-- Nguyên tắc: "Cứ mỗi tin nhắn gửi vào nhóm thì vừa trả lời vừa xét có chắt lọc được gì' }
)"""
if OLD not in s:
    sys.exit('khong khop khoi subs zalo.js')
io.open(B, 'w', encoding='utf-8', newline='\n').write(s.replace(OLD, NEW, 1))
print('build-app.ps1: them sub cho chu thich SQL trong store.go')
