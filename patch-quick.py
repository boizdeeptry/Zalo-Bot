# Them hang dien nhanh cho trong vao NGAY TRONG o sua toan van.
import io
import sys

P = r'F:\dist\_build\appmode\manage.js'
s = io.open(P, encoding='utf-8').read()

OLD = '''  const ta = el("textarea");
  ta.value = d.text;
  ta.spellcheck = false;
  sheet.appendChild(ta);
'''
NEW = '''  const ta = el("textarea");
  ta.value = d.text;
  ta.spellcheck = false;

  // Hàng điền nhanh, NẰM TRONG ô sửa.
  //
  // Vì sao lặp lại nó ở đây thay vì bắt người dùng quay ra panel: mở ô sửa là lúc họ đang xem
  // đúng tệp đó. Bắt đóng ô, điền ở ngoài, rồi mở lại là ba bước cho một việc.
  //
  // Nó điền vào Ô SỬA, không ghi thẳng xuống tệp: một đường ghi duy nhất là nút Lưu, nên người
  // dùng thấy kết quả trước khi nó thành thật, và không có hai đường ghi để lệch nhau.
  const quick = el("div", "quick");
  sheet.appendChild(quick);
  sheet.appendChild(ta);

  const drawQuick = () => {
    quick.textContent = "";
    const holes = scanHoles(ta.value);
    if (!holes.length) {
      quick.className = "quick done";
      quick.appendChild(el("span", "qok", "\\u2713 Không còn chỗ trống nào trong nội dung này"));
      return;
    }
    quick.className = "quick";
    quick.appendChild(el("div", "qh",
      "Điền nhanh " + holes.length + " chỗ trống, nếu không muốn sửa nội dung:"));
    const grid = el("div", "qg");
    const boxes = {};
    for (const h of holes) {
      const c = el("div", "qf");
      const lab = el("label");
      lab.appendChild(el("span", "fk", "{{" + h.key + "}}"));
      lab.appendChild(el("span", "fc", h.count + " chỗ"));
      c.appendChild(lab);
      const i = el("input");
      i.type = "text";
      i.maxLength = 60;
      i.placeholder = HINTS[h.key] || "điền giá trị";
      // Enter điền luôn: người dùng gõ xong hay bấm Enter theo phản xạ.
      i.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); apply(); } };
      c.appendChild(i);
      boxes[h.key] = i;
      grid.appendChild(c);
    }
    quick.appendChild(grid);
    const apply = () => {
      let text = ta.value;
      let n = 0;
      for (const k in boxes) {
        const v = boxes[k].value.trim();
        if (v === "") continue;
        // Cùng luật với phía daemon: giá trị này được nhân vào tới 19 chỗ, nên nó là một cái
        // TÊN. Chặn ở đây để người dùng biết ngay thay vì sau khi bấm Lưu.
        if (v.includes("{{") || v.includes("}}")) {
          qnote.textContent = "{{" + k + "}}: không được chứa {{ }}";
          return;
        }
        text = text.split("{{" + k + "}}").join(v);
        n++;
      }
      if (n === 0) { qnote.textContent = "chưa điền ô nào"; return; }
      ta.value = text;
      drawQuick();
    };
    const row = el("div", "qr");
    const b = el("button", "btn", "Điền vào nội dung");
    b.onclick = apply;
    row.appendChild(b);
    const qnote = el("span", "note", "");
    row.appendChild(qnote);
    quick.appendChild(row);
  };
  drawQuick();
'''
if OLD not in s:
    sys.exit('khong khop khoi textarea')
s = s.replace(OLD, NEW)

SCAN = r'''
// scanHoles tim cho trong trong mot chuoi, phia trinh duyet.
//
// Mau PHAI khop scanPlaceholders ben Go (agentcfg.go): chi chu in va gach duoi. Rong hon thi mot
// dong vi du trong cam nang se bien thanh mot o nhap; hep hon thi mot cho trong that bi bo qua va
// bot se gui nguyen chu trong ngoac cho khach.
function scanHoles(text) {
  const re = /\{\{([A-Z][A-Z_]*)\}\}/g;
  const counts = new Map();
  let m;
  while ((m = re.exec(text)) !== null) {
    counts.set(m[1], (counts.get(m[1]) || 0) + 1);
  }
  return [...counts.entries()]
    .map(([key, count]) => ({ key, count }))
    .sort((a, b) => (a.key < b.key ? -1 : 1));
}
'''
s = s.rstrip() + '\n' + SCAN
io.open(P, 'w', encoding='utf-8', newline='\n').write(s)
print('da them hang dien nhanh trong o sua')
