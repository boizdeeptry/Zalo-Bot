// Portal quản lý của bản đóng gói. CHỈ có trong gói bán, xem dist/appmode/.
//
// Một trang, một rail, một panel. Không router, không framework: bốn nhóm mục với đúng một mục
// đang chạy được thì một hàm switch là đủ, và thêm một tầng định tuyến bây giờ là thêm chỗ để
// hỏng mà không thêm gì dùng được.

"use strict";

// SECTIONS là bản đồ chức năng, và nó CỐ Ý liệt cả những mục chưa có.
//
// Ẩn mục chưa có thì người mua không thấy lộ trình. Để nó sáng như mục thật thì họ bấm vào rồi
// tưởng phần mềm hỏng. Nên mục chưa có vẫn hiện, mờ đi, có nhãn "chưa có", và bấm vào thì nói
// đúng nó sẽ làm gì khi có.
const SECTIONS = [
  { head: "BUILD" },
  { id: "agents",    ic: "\u{1F916}", name: "AI Agents" },
  { id: "knowledge", ic: "\u{1F9E0}", name: "Knowledge" },
  { id: "workflows", ic: "\u{1F504}", name: "Workflows",     todo: "Chuỗi bước tự động: nhận tin thì làm gì, khi nào chuyển người thật, khi nào gửi tệp." },
  { id: "tools",     ic: "\u{1F6E0}", name: "Tools & MCP",   todo: "Nối agent với công cụ ngoài qua MCP: CRM, đơn hàng, tồn kho." },
  { id: "models",    ic: "\u{1F9E9}", name: "Models" },
  { id: "memory",    ic: "\u{1F5C3}", name: "Memory",        todo: "Ghi chú bot tự viết cho từng hội thoại, và bài học rút từ lần người trực sửa câu. Hiện xem trong trang Zalo." },
  { head: "OPERATE" },
  { id: "convo",     ic: "\u{1F4AC}", name: "Conversations", go: "/zalo" },
  { id: "analytics", ic: "\u{1F4CA}", name: "Analytics",     todo: "Số tin, số lượt bot trả lời, tỉ lệ phải chuyển người thật, cảm xúc khách để lại." },
  { id: "eval",      ic: "\u{1F9EA}", name: "Evaluation",    todo: "Bộ câu hỏi mẫu chạy lại sau mỗi lần sửa văn phong, để biết sửa xong tốt hơn hay xấu đi." },
  { id: "deploy",    ic: "\u{1F680}", name: "Deployments",   todo: "Chạy nhiều tài khoản Zalo, hoặc chuyển sang máy chủ." },
  { head: "GOVERN" },
  { id: "security",  ic: "\u{1F6E1}", name: "Security",      todo: "Ai vào được portal, ai đọc được hội thoại, nhật ký truy cập." },
  { id: "workspace", ic: "\u{1F3E2}", name: "Workspace",     todo: "Nhiều người trực cùng dùng, phân quyền theo người." },
  { head: "SYSTEM" },
  { id: "settings",  ic: "⚙",    name: "Settings",      todo: "Cửa sổ gom tin, tên bot, thư mục tri thức. Hiện đặt trong trang Zalo và trong Chay.bat." },
];

let current = "knowledge";
let timer = null;

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

function renderRail() {
  const rail = document.getElementById("rail");
  rail.textContent = "";
  for (const s of SECTIONS) {
    if (s.head !== undefined) {
      rail.appendChild(el("div", "railhead", s.head));
      continue;
    }
    const b = el("button", "nav" + (s.todo ? " todo" : "") + (s.id === current ? " on" : ""));
    b.appendChild(el("span", "ic", s.ic));
    b.appendChild(el("span", null, s.name));
    b.onclick = () => {
      // Mục có `go` là một trang khác, không một panel: hội thoại đã có trang riêng đầy đủ, và
      // dựng lại nó ở đây là hai bản của cùng một thứ.
      if (s.go) { location.href = s.go; return; }
      current = s.id;
      renderRail();
      renderMain();
    };
    rail.appendChild(b);
  }
}

function sectionById(id) {
  return SECTIONS.find((s) => s.id === id);
}

function renderMain() {
  if (timer) { clearInterval(timer); timer = null; }
  const main = document.getElementById("main");
  main.textContent = "";
  const s = sectionById(current);
  if (current === "knowledge") { renderKnowledge(main); return; }
  if (current === "models") { renderModels(main); return; }
  if (current === "agents") { renderAgents(main); return; }
  main.appendChild(el("h1", null, s.name));
  main.appendChild(el("p", "sub", "Phần này chưa có."));
  const h = el("div", "hint");
  h.appendChild(el("div", null, s.todo));
  main.appendChild(h);
}

// --------------------------------------------------------------- AI Agents

// renderAgents: mot cau hoi truoc moi thu khac — bot mo cho khach that duoc chua.
//
// Ban giao di co persona con cho trong, va neu khong ai noi ra thi bot se gui cho khach mot cau
// chua "{{TEN_BOT}}". Nen cho trong hien len o TREN, mau canh bao, khong nam duoi mot bang thong
// tin nao.
async function renderAgents(main) {
  main.appendChild(el("h1", null, "AI Agents"));
  main.appendChild(el("p", "sub",
    "Một agent: con trả lời tin nhắn Zalo. Giọng nói của nó nằm trong tệp văn phong."));

  let d;
  try {
    const r = await fetch("/agent");
    if (!r.ok) {
      const h = el("div", "hint");
      h.appendChild(el("div", null, await r.text()));
      main.appendChild(h);
      return;
    }
    d = await r.json();
  } catch (e) {
    main.appendChild(el("p", null, "không đọc được cấu hình: " + e.message));
    return;
  }

  // Bang trang thai. ready quyet dinh mau, va do la thu nguoi dung phai thay dau tien.
  const banner = el("div", "banner" + (d.ready ? " ok" : " bad"));
  banner.appendChild(el("div", "bt", d.ready
    ? "Sẵn sàng nói chuyện với khách"
    : "CHƯA sẵn sàng: văn phong còn " + d.placeholders.length + " chỗ trống"));
  banner.appendChild(el("div", "bd", d.ready
    ? "Không còn chỗ trống nào trong tệp văn phong."
    : "Điền hết bên dưới rồi bấm Lưu. Chưa điền thì bot sẽ gửi cho khách nguyên chữ trong ngoặc."));
  main.appendChild(banner);

  if (d.placeholders.length) {
    const box = el("div");
    box.style.maxWidth = "660px";
    const inputs = {};
    for (const p of d.placeholders) {
      const f = el("div", "field");
      const lab = el("label");
      lab.appendChild(el("span", "fk", "{{" + p.key + "}}"));
      lab.appendChild(el("span", "fc", p.count + " chỗ trong tệp"));
      f.appendChild(lab);
      const i = el("input");
      i.type = "text";
      i.maxLength = 60;
      i.placeholder = HINTS[p.key] || "điền giá trị";
      f.appendChild(i);
      inputs[p.key] = i;
      // Dong vi du la phan quan trong: ten "TEN_CHUYEN_GIA" mot minh khong noi duoc no la ai.
      if (p.sample) f.appendChild(el("div", "fs", p.sample));
      box.appendChild(f);
    }
    main.appendChild(box);

    const row = el("div", "row");
    const save = el("button", "btn go", "Lưu văn phong");
    const note = el("span", "note", "");
    save.onclick = async () => {
      const values = {};
      for (const k in inputs) {
        const v = inputs[k].value.trim();
        if (v === "") { note.textContent = "còn ô chưa điền: {{" + k + "}}"; return; }
        values[k] = v;
      }
      save.disabled = true;
      note.textContent = "đang lưu…";
      try {
        const r = await fetch("/agent", {
          method: "PUT",
          headers: { "Content-Type": "application/json", "X-Agentdc-Portal": "1" },
          body: JSON.stringify({ values }),
        });
        if (!r.ok) { note.textContent = await r.text(); save.disabled = false; return; }
        // Ve lai ca panel: cho trong da bien mat khoi tep nen bang trang thai phai doi mau.
        renderMain();
        return;
      } catch (e) {
        note.textContent = "không lưu được: " + e.message;
      }
      save.disabled = false;
    };
    row.appendChild(save);
    row.appendChild(note);
    main.appendChild(row);

    const h = el("div", "hint");
    h.appendChild(el("div", null,
      "Lưu là ghi thẳng vào tệp văn phong, có hiệu lực NGAY ở lượt trả lời sau, không cần mở lại " +
      "phần mềm. Bản gốc được giữ cạnh nó với đuôi .goc trước lần ghi đầu."));
    main.appendChild(h);
  }

  // Thong tin chi doc. Dat DUOI phan phai dien: no la thu tra cuu, khong phai thu phai lam.
  const facts = el("div", "facts");
  const add = (k, v, note, opt) => {
    const r = el("div", "fact");
    r.appendChild(el("div", "fkk", k));
    const val = el("div", "fvv");
    val.appendChild(el("span", null, v));
    if (note) val.appendChild(el("span", "fn", note));
    r.appendChild(val);
    // Nút bút chì chỉ có ở hàng nào sửa được. Một icon trên hàng chỉ-đọc là một lời hứa hỏng.
    if (opt && opt.edit) {
      const b = el("button", "pen", "\u270E");
      b.title = "Sửa nội dung tệp";
      b.onclick = () => openEditor(opt.edit);
      r.appendChild(b);
    }
    facts.appendChild(r);
  };
  add("Mô hình", d.model || "(mặc định)", "đổi ở mục Models");
  add("Phạm vi quyền", (d.tools || []).join(", "), "CHỈ ĐỌC, không đổi được từ đây");
  add("Tệp văn phong", d.persona_name, Math.round((d.persona_size || 0) / 1024) + " KB",
      { edit: "persona" });
  add("Sổ tay thành viên", shortPath(d.roster_path), null, { edit: "roster" });
  add("Luật riêng từng nhóm", shortPath(d.overlay_dir));
  add("Thư mục tri thức", (d.kb_roots || []).map(shortPath).join("  ·  "));
  main.appendChild(facts);

  const h2 = el("div", "hint");
  h2.appendChild(el("div", null,
    "Phạm vi quyền cố định là chỉ-đọc, và đó là chủ đích: agent này tự động trả lời khách, nên nó " +
    "không có quyền ghi hay xoá bất cứ gì. Con agent biên soạn wiki ở mục Knowledge mới có quyền ghi."));
  main.appendChild(h2);
}

// HINTS goi y cho tung cho trong. Khong bat buoc, nhung mot o input trong khong noi duoc no can gi.
const HINTS = {
  TEN_BOT: "ví dụ: trợ lý An — tên bot tự gọi mình",
  TEN_CHUYEN_GIA: "ví dụ: Anh Nam — người mà tri thức thuộc về",
};

// shortPath bo phan dau duong dan cho de doc: nguoi dung biet phan mem cua ho o dau roi.
function shortPath(p) {
  if (!p) return "(chưa đặt)";
  const i = p.toLowerCase().indexOf("\\brain\\");
  return i >= 0 ? "brain\\" + p.slice(i + 7) : p;
}

// ------------------------------------------------------------------- Models

// MODEL_INFO là phần người đọc: cái phải hiểu là ĐÁNH ĐỔI, không phải tên.
//
// Ba dòng này là lý do có màn hình này. Một bot Zalo trả lời hàng trăm tin mỗi ngày thì tiền và
// độ trễ là hai thứ người vận hành cảm nhận được ngay, còn "mô hình nào thông minh hơn" thì
// không — nên mỗi lựa chọn phải nói giá của nó.
const MODEL_INFO = {
  haiku:  { nm: "Haiku",  tag: "nhanh nhất, rẻ nhất",
            ds: "Trả lời nhanh, chi phí thấp nhất. Đủ cho hỏi đáp thường ngày dựa trên wiki. Mặc định." },
  sonnet: { nm: "Sonnet", tag: "cân bằng",
            ds: "Suy luận tốt hơn Haiku, vẫn nhanh. Chọn khi khách hỏi những câu cần nối nhiều nguồn." },
  opus:   { nm: "Opus",   tag: "sâu nhất, đắt nhất",
            ds: "Suy luận sâu nhất, chậm hơn và tốn hơn nhiều. Cân nhắc kỹ nếu lượng tin lớn." },
};

async function renderModels(main) {
  main.appendChild(el("h1", null, "Models"));
  main.appendChild(el("p", "sub", "Mô hình mà bot dùng để viết câu trả lời."));
  const box = el("div", "picks");
  main.appendChild(box);
  const row = el("div", "row");
  main.appendChild(row);

  let d;
  try {
    const r = await fetch("/kb/model");
    d = await r.json();
  } catch (e) {
    main.appendChild(el("p", null, "không đọc được cấu hình: " + e.message));
    return;
  }
  // chosen bắt đầu từ giá trị ĐÃ LƯU nếu có, không từ giá trị đang chạy: đã lưu là ý muốn gần
  // nhất của người dùng, và nó là thứ sẽ có hiệu lực lần mở sau.
  let chosen = d.saved || d.active || "haiku";

  const draw = () => {
    box.textContent = "";
    for (const m of d.choices) {
      const info = MODEL_INFO[m] || { nm: m, ds: "", tag: "" };
      const b = el("button", "pick" + (m === chosen ? " on" : ""));
      b.appendChild(el("span", "dot"));
      const t = el("span");
      t.style.minWidth = "0";
      const nmLine = el("span", "nm", info.nm);
      if (m === d.active) {
        // Nói rõ cái nào ĐANG chạy: nếu không, người dùng bấm Lưu rồi tưởng nó đã đổi.
        const cur = el("span", null, "  · đang chạy");
        cur.style.fontWeight = "400";
        cur.style.fontSize = "11.5px";
        cur.style.color = "var(--live)";
        nmLine.appendChild(cur);
      }
      t.appendChild(nmLine);
      t.appendChild(el("div", "ds", info.ds));
      b.appendChild(t);
      if (info.tag) b.appendChild(el("span", "tag", info.tag));
      b.onclick = () => { chosen = m; draw(); };
      box.appendChild(b);
    }
  };
  draw();

  const save = el("button", "btn go", "Lưu");
  const note = el("span", null, "");
  note.style.fontSize = "12.5px";
  note.style.color = "var(--dim)";
  save.onclick = async () => {
    save.disabled = true;
    note.textContent = "đang áp dụng…";
    let out;
    try {
      const r = await fetch("/kb/model", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-Agentdc-Portal": "1" },
        body: JSON.stringify({ model: chosen }),
      });
      if (!r.ok) {
        note.textContent = "không lưu được: " + (await r.text());
        save.disabled = false;
        return;
      }
      out = await r.json();
    } catch (e) {
      note.textContent = "không lưu được: " + e.message;
      save.disabled = false;
      return;
    }
    if (!out.restarting) {
      // Duong roi: da luu nhung khong tu mo lai duoc. Noi ra viec phai lam bang tay, va noi ro
      // la da luu -- nguoi dung khong duoc nghi minh mat cong.
      note.textContent = "đã lưu, nhưng không tự mở lại được (" + out.reason +
        "). Đóng rồi mở lại phần mềm để áp dụng.";
      save.disabled = false;
      return;
    }
    waitForRestart(note);
  };
  row.appendChild(save);
  row.appendChild(note);

  const hint = el("div", "hint");
  hint.appendChild(el("div", null,
    "Bấm Lưu là phần mềm tự khởi động lại để áp dụng, mất khoảng 10 giây. Trang này tự tải " +
    "lại khi xong. Hội thoại, danh bạ và tri thức không mất gì, phiên Zalo cũng không phải quét lại."));
  main.appendChild(hint);
}

// ---------------------------------------------------------------- Knowledge

function renderKnowledge(main) {
  main.appendChild(el("h1", null, "Knowledge"));
  main.appendChild(el("p", "sub",
    "Tải tệp nguồn lên, rồi để agent biên soạn thành trang wiki. Bot trả lời khách từ wiki."));

  const cards = el("div", "cards");
  cards.id = "cards";
  main.appendChild(cards);

  const drop = el("div");
  drop.id = "drop";
  drop.appendChild(el("div", "big", "Kéo tệp vào đây, hoặc bấm để chọn"));
  drop.appendChild(el("div", "small", "pdf, docx, xlsx, pptx, md, txt, csv, png, jpg · tối đa 50 MB mỗi tệp"));
  main.appendChild(drop);

  const input = el("input");
  input.type = "file";
  input.multiple = true;
  input.style.display = "none";
  main.appendChild(input);

  drop.onclick = () => input.click();
  input.onchange = () => { if (input.files.length) upload(input.files); };
  // dragover phải preventDefault, nếu không trình duyệt sẽ MỞ tệp thay vì để trang nhận.
  drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("hot"); };
  drop.ondragleave = () => drop.classList.remove("hot");
  drop.ondrop = (e) => {
    e.preventDefault();
    drop.classList.remove("hot");
    if (e.dataTransfer.files.length) upload(e.dataTransfer.files);
  };

  const row = el("div", "row");
  const bIngest = el("button", "btn", "Biên soạn vào wiki");
  bIngest.id = "bIngest";
  bIngest.onclick = ingest;
  row.appendChild(bIngest);
  const bStop = el("button", "btn", "Huỷ");
  bStop.id = "bStop";
  bStop.style.display = "none";
  bStop.onclick = stopIngest;
  row.appendChild(bStop);
  const st = el("span", null, "");
  st.id = "istate";
  st.style.fontSize = "12.5px";
  st.style.opacity = ".7";
  row.appendChild(st);
  main.appendChild(row);

  const steps = el("div");
  steps.id = "steps";
  steps.style.display = "none";
  main.appendChild(steps);

  const cols = el("div", "cols");
  for (const [id, title] of [["rawList", "raw\\ — tệp nguồn"], ["wikiList", "wiki\\ — trang đã biên soạn"]]) {
    const c = el("div");
    c.appendChild(el("h3", null, title));
    const l = el("div", "flist");
    l.id = id;
    c.appendChild(l);
    cols.appendChild(c);
  }
  main.appendChild(cols);

  const hint = el("div", "hint");
  hint.appendChild(el("div", null,
    "Biên soạn gọi mô hình, nên nó tốn phí trên tài khoản Claude của bạn. Nó chỉ chạy khi bạn bấm."));
  main.appendChild(hint);

  refresh();
  timer = setInterval(refresh, 2000);
}

async function refresh() {
  let d;
  try {
    const r = await fetch("/kb");
    if (!r.ok) {
      const t = await r.text();
      document.getElementById("istate").textContent = t.slice(0, 200);
      return;
    }
    d = await r.json();
  } catch (e) {
    document.getElementById("istate").textContent = "không đọc được trạng thái: " + e.message;
    return;
  }
  const cards = document.getElementById("cards");
  if (!cards) return;
  cards.textContent = "";
  for (const [n, l] of [[d.raw_count, "tệp trong raw\\"], [d.wiki_count, "trang trong wiki\\"]]) {
    const c = el("div", "card");
    c.appendChild(el("div", "n", String(n)));
    c.appendChild(el("div", "l", l));
    cards.appendChild(c);
  }
  fillList("rawList", d.raw_files);
  fillList("wikiList", d.wiki_files);

  const ing = d.ingest || {};
  const bIngest = document.getElementById("bIngest");
  const bStop = document.getElementById("bStop");
  const stepsBox = document.getElementById("steps");
  bIngest.disabled = !!ing.running;
  bStop.style.display = ing.running ? "" : "none";
  if (ing.running) {
    document.getElementById("istate").textContent = "đang biên soạn · " + (ing.elapsed_sec || 0) + "s";
  } else if (ing.done) {
    document.getElementById("istate").textContent = ing.err ? "lỗi: " + ing.err : "xong";
  } else {
    document.getElementById("istate").textContent = "";
  }
  if (ing.steps && ing.steps.length) {
    stepsBox.style.display = "";
    // Chỉ vẽ lại khi có dòng mới: vẽ lại mỗi 2 giây sẽ kéo thanh cuộn về đầu giữa lúc đang đọc.
    if (stepsBox.dataset.n !== String(ing.steps.length)) {
      stepsBox.dataset.n = String(ing.steps.length);
      stepsBox.textContent = ing.steps.join("\n");
      stepsBox.scrollTop = stepsBox.scrollHeight;
    }
  }
}

function fillList(id, files) {
  const box = document.getElementById(id);
  if (!box) return;
  box.textContent = "";
  if (!files || !files.length) {
    box.appendChild(el("div", "none", "chưa có tệp nào"));
    return;
  }
  for (const f of files) box.appendChild(el("div", null, f));
}

async function upload(files) {
  const fd = new FormData();
  for (const f of files) fd.append("file", f);
  const st = document.getElementById("istate");
  st.textContent = "đang tải " + files.length + " tệp…";
  try {
    const r = await fetch("/kb/upload", {
      method: "POST",
      headers: { "X-Agentdc-Portal": "1" },
      body: fd,
    });
    const d = await r.json().catch(() => ({}));
    const saved = (d.saved || []).length;
    const skipped = d.skipped || [];
    let msg = "đã nhận " + saved + " tệp";
    if (skipped.length) msg += " · bỏ qua: " + skipped.join(", ");
    st.textContent = msg;
  } catch (e) {
    st.textContent = "tải lên thất bại: " + e.message;
  }
  refresh();
}

async function ingest() {
  const st = document.getElementById("istate");
  try {
    const r = await fetch("/kb/ingest", { method: "POST", headers: { "X-Agentdc-Portal": "1" } });
    if (!r.ok) { st.textContent = await r.text(); return; }
  } catch (e) {
    st.textContent = "không bắt đầu được: " + e.message;
    return;
  }
  refresh();
}

async function stopIngest() {
  try {
    await fetch("/kb/ingest", { method: "DELETE", headers: { "X-Agentdc-Portal": "1" } });
  } catch { /* huỷ thất bại thì lượt vẫn chạy, và refresh sẽ nói ra */ }
  refresh();
}

renderRail();
renderMain();

// waitForRestart tham do tới khi máy chủ trả lời lại rồi tự tải lại trang.
//
// Thăm dò chứ không đếm giây: khởi động lại nhanh chậm tuỳ máy, và một khoảng chờ cố định thì
// hoặc tải lại quá sớm (trang lỗi) hoặc bắt người dùng ngồi đợi vô cớ.
//
// Vì sao phải thấy nó CHẾT trước: ngay sau khi bấm, máy chủ cũ vẫn còn sống nửa giây nữa. Tải lại
// ngay lúc đó là tải lại bản CŨ, và người dùng thấy mô hình chưa đổi.
async function waitForRestart(note) {
  const alive = async () => {
    try {
      const r = await fetch("/kb/model", { cache: "no-store" });
      return r.ok;
    } catch { return false; }
  };
  let died = false;
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setTimeout(r, 700));
    const up = await alive();
    if (!died) {
      if (!up) { died = true; note.textContent = "đang khởi động lại…"; }
      else note.textContent = "đang tắt…";
      continue;
    }
    if (up) { note.textContent = "xong, đang tải lại trang…"; location.reload(); return; }
  }
  note.textContent = "khởi động lại lâu hơn dự kiến. Mở Start.vbs nếu trang không trở lại.";
}

// openEditor mo o sua toan van cho persona.md hoac roster.md.
//
// Vi sao co no du da co form dien cho trong: form chi phu nhung cho BAT BUOC. Doi cach bot noi --
// dai cau, khi nao tach tin, cai gi tuyet doi khong noi -- la sua van, va do la thu nguoi mua se
// muon lam nhieu nhat sau khi dung mot tuan.
//
// Va no AN TOAN HON Notepad: ghi qua daemon thi encoding do daemon quyet (UTF-8 khong BOM). Notepad
// co the ghi lai bang encoding khac, va mot persona mojibake lam bot mat giong ma khong gi bao.
async function openEditor(name) {
  let d;
  try {
    const r = await fetch("/agent/persona/" + name);
    if (!r.ok) { alert(await r.text()); return; }
    d = await r.json();
  } catch (e) { alert("không đọc được tệp: " + e.message); return; }

  const back = el("div", "sheetback");
  const sheet = el("div", "sheet");
  const head = el("div", "sheethead");
  head.appendChild(el("div", "st", "Sửa " + d.label));
  head.appendChild(el("div", "sp", d.path));
  sheet.appendChild(head);

  const ta = el("textarea");
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
      quick.appendChild(el("span", "qok", "\u2713 Không còn chỗ trống nào trong nội dung này"));
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

  const foot = el("div", "sheetfoot");
  const note = el("span", "note", "");
  const cancel = el("button", "btn", "Đóng");
  const save = el("button", "btn go", "Lưu");
  // Đóng bằng nút, bằng Escape, hoặc bằng cách bấm ra ngoài. Ba đường vì người dùng thử cả ba.
  const close = () => { document.removeEventListener("keydown", onKey); back.remove(); };
  const onKey = (e) => { if (e.key === "Escape") close(); };
  document.addEventListener("keydown", onKey);
  back.onclick = (e) => { if (e.target === back) close(); };
  cancel.onclick = close;

  save.onclick = async () => {
    save.disabled = true;
    note.textContent = "đang lưu…";
    try {
      const r = await fetch("/agent/persona/" + name, {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-Agentdc-Portal": "1" },
        body: JSON.stringify({ text: ta.value }),
      });
      if (!r.ok) { note.textContent = await r.text(); save.disabled = false; return; }
      close();
      // Vẽ lại panel: số chỗ trống và kích cỡ tệp đều có thể đã đổi.
      renderMain();
      return;
    } catch (e) { note.textContent = "không lưu được: " + e.message; }
    save.disabled = false;
  };

  const hint = el("span", "note",
    "Có hiệu lực ngay ở lượt trả lời sau, không cần mở lại phần mềm.");
  foot.appendChild(hint);
  foot.appendChild(note);
  foot.appendChild(cancel);
  foot.appendChild(save);
  sheet.appendChild(foot);
  back.appendChild(sheet);
  document.body.appendChild(back);
  ta.focus();
}

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
