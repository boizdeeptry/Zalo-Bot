import io
import sys

P = r'F:\dist\_build\appmode\index.html'
s = io.open(P, encoding='utf-8').read()

CSS = """  /* Hang dien nhanh trong o sua. Nen chim de no tach khoi vung chu ben duoi ma khong can mot
     duong vien nua. */
  .quick { padding: 12px 17px; border-bottom: 1px solid var(--line); background: var(--panel); }
  .quick.done { padding: 9px 17px; }
  .quick .qok { font-size: 12px; color: var(--live); }
  .quick .qh { font-size: 12px; color: var(--dim); margin-bottom: 9px; }
  /* Hai cot khi rong, mot cot khi hep. Hai cho trong thi vua mot hang, va nguoi dung thay ca hai
     cung luc thay vi cuon. */
  .quick .qg { display: grid; grid-template-columns: repeat(auto-fit, minmax(238px, 1fr)); gap: 11px; }
  .quick .qf label { display: flex; align-items: baseline; gap: 8px; margin-bottom: 4px; }
  .quick .qf input {
    width: 100%; font: inherit; font-size: 12.5px; padding: 6px 10px; border-radius: 6px;
    background: var(--sunk); color: var(--ink); border: 1px solid var(--line);
  }
  .quick .qf input:focus { outline: none; border-color: var(--live); }
  .quick .qr { display: flex; gap: 10px; align-items: center; margin-top: 10px; }
"""

A = '  .hint {'
if A not in s:
    sys.exit('khong thay moc CSS')
if '.quick {' in s:
    sys.exit('CSS da co, khong them lai')
io.open(P, 'w', encoding='utf-8', newline='\n').write(s.replace(A, CSS + A, 1))
print('da them CSS cho hang dien nhanh')
