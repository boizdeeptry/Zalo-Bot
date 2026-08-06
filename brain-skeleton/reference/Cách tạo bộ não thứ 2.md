---
type: reference
tags: [setup, obsidian, claude-code, methodology, how-to]
created: 2026-04-27
updated: 2026-04-27
---

# Cách tạo "Bộ não thứ 2" với Obsidian + Claude Code

> Đây là quy trình thực hành để dựng vault bộ não thứ 2 của bạn. Tài liệu mô tả lại từng bước để bất kỳ ai (kể cả người chưa từng dùng Obsidian hay Claude Code) cũng có thể tự tạo một "bộ não thứ 2" cho riêng mình. Cover đa nền tảng macOS / Windows / Linux. Cover đầy đủ 3 workflow: **ingest / query / lint**.
>
> Lý thuyết nền tảng: xem [[reference/methodology|methodology]]. File này là phần thực hành tương ứng.

## Mục lục

- [Phần 0 — Hiểu trước khi làm](#phần-0--hiểu-trước-khi-làm)
- [Phần 1 — Yêu cầu & danh sách công cụ](#phần-1--yêu-cầu--danh-sách-công-cụ)
- [Phần 2 — Cài đặt nền tảng](#phần-2--cài-đặt-nền-tảng)
- [Phần 3 — Tạo vault Obsidian](#phần-3--tạo-vault-obsidian)
- [Phần 4 — Tạo các file khung](#phần-4--tạo-các-file-khung)
- [Phần 5 — Khởi động Claude Code lần đầu](#phần-5--khởi-động-claude-code-lần-đầu)
- [Phần 6 — Workflow Ingest (source đầu tiên)](#phần-6--workflow-ingest-source-đầu-tiên)
- [Phần 7 — Workflow Query (hỏi đáp với wiki)](#phần-7--workflow-query-hỏi-đáp-với-wiki)
- [Phần 8 — Workflow Lint (health-check định kỳ)](#phần-8--workflow-lint-health-check-định-kỳ)
- [Phần 9 — Tips & nâng cao](#phần-9--tips--nâng-cao)
- [Phần 10 — Checklist hoàn thành](#phần-10--checklist-hoàn-thành)
- [Phần 11 — FAQ & Troubleshooting](#phần-11--faq--troubleshooting)

---

## Phần 0 — Hiểu trước khi làm

### "Bộ não thứ 2" là gì

Cách phổ biến hiện nay khi dùng LLM với tài liệu cá nhân là **RAG**: bạn upload một đống file, mỗi lần hỏi LLM lại đi tìm chunk liên quan rồi tổng hợp. NotebookLM, ChatGPT file uploads đều theo kiểu này. Vấn đề: kiến thức **không được tích lũy** — mỗi câu hỏi LLM phải lục lại từ đầu.

**Bộ não thứ 2** đi theo hướng khác: LLM **biên soạn một wiki riêng** từ các nguồn bạn đưa vào. Khi có nguồn mới, LLM không chỉ index để tìm sau — nó **đọc, tách thông tin, cập nhật wiki, ghi mâu thuẫn, củng cố hoặc thách thức tổng hợp đang có**. Wiki là một **artefact bồi đắp dần**, sống chung với anh.

Khác biệt cốt lõi:
- RAG: *"LLM tìm trong sources mỗi lần hỏi."*
- Bộ não thứ 2: *"LLM compile sources vào wiki một lần, sau đó wiki là cái bạn hỏi."*

### 3 lớp

| Lớp | Vai trò | Ai sửa |
|---|---|---|
| **Raw sources** (`raw/`) | File gốc — articles, PDF, transcripts, hình ảnh, docx, xlsx | **Bất biến.** Bạn drop vào, LLM chỉ đọc, không sửa. |
| **Wiki** (`wiki/`) | Trang markdown do LLM sinh ra: source summary, entity, concept, topic, analysis | **LLM toàn quyền.** Bạn đọc, LLM viết. |
| **Schema** (`CLAUDE.md`) | Quy tắc & workflow — cách LLM phải vận hành vault | **Co-evolved.** Bạn và LLM cùng cập nhật theo thời gian. |

Cộng thêm 2 file điều hướng ở root:
- `index.md` — catalog tất cả wiki pages.
- `log.md` — sổ nhật ký append-only ghi mọi ingest / query / lint.

### Sơ đồ tổng thể

```
┌─────────────────────────────────────────────────────────────────┐
│  Anh: drop file vào raw/   →   "ingest" trong Claude Code REPL  │
└────────────┬────────────────────────────────────────────────────┘
             │
             ▼
┌─────────────────────────────────────────────────────────────────┐
│  Claude đọc CLAUDE.md → đọc source → discuss takeaways với bạn  │
│         → viết source page → tạo/cập nhật entity & concept      │
│              → update index.md → append log.md                  │
└────────────┬────────────────────────────────────────────────────┘
             │
             ▼
┌─────────────────────────────────────────────────────────────────┐
│  Bạn đọc trong Obsidian   ←   query lại sau bằng Claude Code    │
│            ←   wiki/analyses/ giữ insight đáng nhớ              │
└─────────────────────────────────────────────────────────────────┘
```

---

## Phần 1 — Yêu cầu & danh sách công cụ

### Phần cứng tối thiểu

- Máy tính cá nhân: macOS 11+, Windows 10+, hoặc Linux (Ubuntu 20.04+ hoặc tương đương).
- 4 GB RAM.
- 2 GB ổ đĩa trống (Obsidian + vault ban đầu). Vault sẽ tăng theo lượng source — Vault mẫu thường ~10 MB cho vài chục wiki pages + vài MB raw.
- Kết nối internet (Claude Code gọi API Anthropic).

### Tài khoản cần có

- **Anthropic account** (claude.ai) — để login Claude Code. Có thể dùng tài khoản free để thử, nhưng workflow ingest dài thường cần Pro/Max plan.
- **Tùy chọn:** iCloud / Google Drive / Dropbox / GitHub nếu muốn sync vault qua nhiều máy.

### Bảng công cụ sẽ cài

| Công cụ | Bắt buộc | Mục đích | Khuyến nghị? |
|---|---|---|---|
| **Obsidian** | ✅ | Editor + viewer cho vault markdown | ✅ |
| **Node.js** (≥ v18) | ✅ | Runtime cho Claude Code | ✅ |
| **Claude Code** (CLI) | ✅ | LLM agent đọc/ghi wiki | ✅ |
| **Dataview** plugin | Khuyến nghị | Auto-sinh list từ frontmatter | ✅ (đã cài, chưa dùng nhiều) |
| **Obsidian Web Clipper** (browser ext) | Tùy chọn | Clip web articles vào `raw/` | Chưa dùng |
| **Marp** plugin | Tùy chọn | Sinh slide deck từ wiki | Chưa cài |
| **qmd** (CLI search) | Khi vault > 500 pages | Local search BM25/vector | Chưa cài |

> **Nguyên tắc:** Tài liệu này hướng dẫn cài cả công cụ "khuyến nghị" và "tùy chọn" — bạn tự chọn cài cái nào theo nhu cầu. Setup tối thiểu khuyến nghị: **Obsidian + Claude Code + Dataview** là đủ vận hành.

---

## Phần 2 — Cài đặt nền tảng

Có 3 thứ phải cài: **Obsidian**, **Node.js**, **Claude Code**. Ba bước này độc lập, có thể làm song song.

### 2.1 — Cài Obsidian

#### macOS
1. Vào https://obsidian.md → bấm **Download for macOS** (file `.dmg`).
2. Mở file `.dmg` → kéo icon Obsidian vào folder Applications.
3. Mở Launchpad → tìm Obsidian → mở. Lần đầu macOS hỏi xác nhận → bấm Open.

Thay thế bằng Homebrew (nếu đã có):
```bash
brew install --cask obsidian
```

#### Windows
1. Vào https://obsidian.md → bấm **Download for Windows** (file `.exe`).
2. Chạy installer → next next next → Install.
3. Mở Obsidian từ Start menu.

Thay thế bằng Winget hoặc Chocolatey:
```powershell
winget install Obsidian.Obsidian
# hoặc
choco install obsidian
```

#### Linux
Có 3 cách, chọn 1:

**AppImage** (chạy luôn, không cần cài):
1. Vào https://obsidian.md → bấm **Download for Linux** → file `.AppImage`.
2. `chmod +x Obsidian-*.AppImage`
3. Double-click chạy, hoặc `./Obsidian-*.AppImage`.

**Flatpak** (Ubuntu/Fedora/Arch...):
```bash
flatpak install flathub md.obsidian.Obsidian
flatpak run md.obsidian.Obsidian
```

**Snap** (Ubuntu):
```bash
sudo snap install obsidian --classic
```

### 2.2 — Cài Node.js

Cần Node.js phiên bản ≥ 18 cho Claude Code.

#### macOS
```bash
brew install node
```
Hoặc tải installer `.pkg` từ https://nodejs.org → next next.

#### Windows
1. Vào https://nodejs.org → tải **LTS** (file `.msi`).
2. Chạy installer → next → finish.
3. Mở **PowerShell mới** (sau khi cài, terminal cũ phải mở lại để thấy lệnh `node`).

Hoặc Winget:
```powershell
winget install OpenJS.NodeJS.LTS
```

#### Linux
Khuyến nghị dùng `nvm` (Node Version Manager) để tránh đụng version hệ thống:
```bash
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash
# mở terminal mới, sau đó:
nvm install --lts
```

Hoặc package manager:
```bash
sudo apt install nodejs npm        # Ubuntu/Debian
sudo dnf install nodejs npm        # Fedora
sudo pacman -S nodejs npm          # Arch
```

#### Verify (cả 3 OS)
```bash
node --version    # phải in v18.x.x trở lên
npm --version     # phải in version >= 9
```

### 2.3 — Cài Claude Code

Cùng một lệnh cho cả 3 OS:
```bash
npm install -g @anthropic-ai/claude-code
```

Verify:
```bash
claude --version
```

Login lần đầu (sẽ tự mở browser để OAuth với Anthropic):
```bash
claude
```
Lần đầu sẽ:
1. Hỏi chọn theme → chọn dark/light tùy thích.
2. Mở browser → đăng nhập Anthropic.
3. Browser redirect về terminal → vào REPL.

Khi vào REPL bạn sẽ thấy prompt như:
```
> 
```
Gõ `/help` để xem lệnh có sẵn. Gõ `/quit` (hoặc Ctrl+C 2 lần) để thoát.

> ⚠️ **Trên Windows:** Khuyến nghị dùng **PowerShell** hoặc **Windows Terminal** thay vì CMD cũ. Một số ký tự Unicode trong Claude Code hiển thị tốt hơn.

---

## Phần 3 — Tạo vault Obsidian

### 3.1 — Quyết định vị trí & cách sync vault

Vault Obsidian là **một folder bình thường** trên ổ đĩa. Bạn đặt nó ở đâu cũng được — quyết định chính là có sync qua nhiều máy không.

Bảng so sánh:

| Cách sync | macOS | Windows | Linux | Ghi chú |
|---|---|---|---|---|
| **iCloud Drive** | ✅ tự nhiên | ⚠️ cần iCloud for Windows | ❌ | Ví dụ trên macOS. Path: `~/Library/Mobile Documents/iCloud~md~obsidian/Documents/<vault>`. |
| **Obsidian Sync** (paid, $4/tháng) | ✅ | ✅ | ✅ | E2E encrypted, đỡ đau đầu sync conflict, nhưng tốn phí. |
| **Google Drive / Dropbox / OneDrive** | ✅ | ✅ | ✅ | Coi chừng `.DS_Store` và sync conflict trên `.obsidian/workspace.json`. |
| **Git (GitHub/GitLab)** | ✅ | ✅ | ✅ | Phù hợp dev — có version history, commit message. Cần `.gitignore` để bỏ `.obsidian/workspace.json`. |
| **Syncthing** | ✅ | ✅ | ✅ | P2P miễn phí, không qua cloud. |
| **Không sync (local)** | ✅ | ✅ | ✅ | OK nếu chỉ dùng 1 máy. Đơn giản nhất. |

Khuyến nghị cho người mới: **bắt đầu local**, sync sau khi đã quen vault.

### 3.2 — Tạo folder vault

1. Tạo folder rỗng tại vị trí đã chọn. Đặt tên (ví dụ `My Brain`):
   - macOS Finder: New Folder.
   - Windows Explorer: New → Folder.
   - Terminal cả 3 OS: `mkdir "My Brain"`.

2. Mở Obsidian → màn hình hello hiện lên → bấm **Open folder as vault** → chọn folder vừa tạo.

3. Khi vault mở, vào **Settings** (icon bánh răng góc trái dưới):
   - **Editor → Default view mode → Source mode** (khuyến nghị — cho thấy markdown thô, dễ kiểm tra wikilinks).
   - **Files and links → New link format → Shortest path when possible** (giúp wikilink ngắn gọn).
   - **Files and links → Use [[Wikilinks]] → ON** (mặc định đã ON).

### 3.3 — Tạo cấu trúc thư mục

Cấu trúc bắt buộc (theo schema CLAUDE.md):

```
/<vault>/
├── CLAUDE.md              # schema (sẽ tạo ở Phần 4)
├── Home.md                # landing page
├── index.md               # catalog
├── log.md                 # nhật ký
├── raw/
│   └── assets/            # ảnh tải về cho Web Clipper
├── wiki/
│   ├── sources/           # 1 trang summary cho mỗi source
│   ├── entities/          # người, công ty, sản phẩm, địa điểm
│   ├── concepts/          # framework, ý tưởng, phương pháp luận
│   ├── topics/            # tổng hợp chủ đề chéo
│   └── analyses/          # query results đáng giữ
└── reference/             # docs về system (file này nằm đây)
```

Tạo bằng Obsidian (right-click trong File Explorer panel → New folder), hoặc terminal.

**Terminal — macOS / Linux** (từ trong vault folder):
```bash
mkdir -p raw/assets wiki/sources wiki/entities wiki/concepts wiki/topics wiki/analyses reference
```

**Terminal — Windows PowerShell** (từ trong vault folder):
```powershell
New-Item -ItemType Directory -Force -Path raw/assets, wiki/sources, wiki/entities, wiki/concepts, wiki/topics, wiki/analyses, reference
```

> 💡 **Linh hoạt:** Subfolder trong `wiki/` chỉ là gợi ý. Sau này có thể thêm `wiki/events/`, `wiki/decisions/`, `wiki/books/`... khi domain cần. Nhớ cập nhật `CLAUDE.md` khi thay đổi.

### 3.4 — Bật core plugins cần thiết

Vào **Settings → Core plugins**, đảm bảo các plugin sau **bật**:

- File explorer
- Search
- Quick switcher
- Graph view
- Backlinks
- Outgoing links
- Tag pane
- Properties
- Page preview
- Templates
- Note composer
- Command palette
- Bookmarks
- Outline
- Word count

(Mặc định Obsidian đã bật phần lớn — chỉ cần check lại.)

### 3.5 — Cài Dataview plugin

Cho phép query frontmatter để auto-sinh list:

1. **Settings → Community plugins** → bấm **Turn on community plugins** (lần đầu cần xác nhận, Obsidian cảnh báo về security).
2. Bấm **Browse** → search **Dataview** → bấm **Install** → bấm **Enable**.

Thử ngay: tạo file `test.md` với:
````markdown
```dataview
LIST FROM ""
LIMIT 5
```
````
→ thấy 5 file trong vault hiện ra. Xóa file test này.

### 3.6 — (Tùy chọn) Cấu hình attachment & Web Clipper

Nếu dự định dùng Obsidian Web Clipper (browser extension) để clip web articles:

1. **Settings → Files and links → Default location for new attachments** → chọn **In the folder specified below** → gõ `raw/assets`.
2. **Settings → Hotkeys** → search "Download attachments for current file" → bấm icon bút bên cạnh → bind hotkey (gợi ý: `Ctrl/Cmd+Shift+D`).
3. Cài extension Obsidian Web Clipper trên trình duyệt (Chrome / Firefox / Safari):
   - Vào https://obsidian.md/clipper
   - Tải extension cho browser
   - Configure: chọn vault `My Brain`, thư mục đích `raw/`.

Sau này khi clip 1 article có ảnh → article xuất ra `raw/<title>.md` → mở file đó trong Obsidian → bấm hotkey vừa bind → ảnh tự download về `raw/assets/`.

---

## Phần 4 — Tạo các file khung

Cần tạo **5 file** trước khi ingest source đầu tiên. Tất cả đều ở root vault (trừ methodology nằm trong `reference/`).

> 📋 **Cách dùng:** Copy nguyên template bên dưới vào file tương ứng. Sửa tên vault, tên anh, ngày tháng cho phù hợp.

### 4.1 — `CLAUDE.md` (schema — file quan trọng nhất)

`CLAUDE.md` là **hợp đồng** giữa bạn và Claude. Mỗi session Claude sẽ đọc file này đầu tiên để biết: cấu trúc vault, convention viết, workflow ingest/query/lint phải tuân theo.

Tạo file `CLAUDE.md` ở root vault với nội dung sau (trong starter template này):

````markdown
# CLAUDE.md — Wiki Schema for <Tên vault của anh>

This vault is a **persistent, LLM-maintained wiki**. Knowledge is compiled once when sources arrive and kept current — not re-derived on every query.

Read this file at the start of every session before reading or writing other files.

## Three layers

1. **Raw sources** (`raw/`) — immutable input documents (clipped articles, PDFs, notes, transcripts, images). Read from here, never modify.
2. **Wiki** (`wiki/`) — LLM-generated markdown pages: source summaries, entity pages, concept pages, syntheses, comparisons, analyses. The LLM owns this layer entirely.
3. **Schema** (this file) — conventions and workflows. Co-evolved with the user over time.

Plus two navigation files at the root:
- `index.md` — content catalog (what pages exist, organized by category)
- `log.md` — append-only chronological record (ingests, queries, lint passes)

## Directory structure

```
/
├── CLAUDE.md
├── index.md
├── log.md
├── raw/
│   ├── assets/
│   └── ...
├── wiki/
│   ├── sources/
│   ├── entities/
│   ├── concepts/
│   ├── topics/
│   └── analyses/
└── reference/
```

Subfolders inside `wiki/` are suggestions, not rigid. Add new categories when the domain calls for it (e.g., `events/`, `decisions/`, `books/`). Update this section when you do.

## File conventions

- **Filenames**: human-readable titles with spaces are fine (Obsidian handles them). Use the canonical name of the entity, concept, or source.
- **Wikilinks**: link aggressively with `[[Page Name]]`. Every entity, concept, and source mentioned in prose should be a link.
- **Frontmatter**: every wiki page starts with YAML frontmatter:
  ```yaml
  ---
  type: source | entity | concept | topic | analysis
  tags: [tag1, tag2]
  created: 2026-04-26
  updated: 2026-04-26
  sources: ["[[Source A]]", "[[Source B]]"]
  ---
  ```
- **Source pages** also include: `raw_path: raw/...`, `author`, `date`, `url` (when applicable).
- **Citations**: in wiki text, cite sources with `[[Source Title]]` inline. Avoid claims without a source link.
- **Language**: write each page in the language of its sources. If sources are mixed, the dominant language wins; preserve original quotes verbatim.

## Workflow: Ingest

When the user says "ingest X" or drops a file in `raw/`:

1. **Read** the source in full. For images, view them; for long files, read in chunks.
2. **Discuss key takeaways** with the user (3-5 bullet points). Ask what to emphasize before writing.
3. **Write the source summary** at `wiki/sources/<Source Title>.md` — frontmatter, 1-paragraph TL;DR, key claims with section/page refs, list of entities and concepts mentioned.
4. **Update or create entity pages** for each named entity. New entity → create `wiki/entities/<Name>.md`. Existing entity → append to the relevant section, link back to the new source.
5. **Update or create concept pages** the same way under `wiki/concepts/`.
6. **Note contradictions** explicitly. If a new source disagrees with an existing claim, add a "Contradictions" section on the relevant page with both views and source links — don't silently overwrite.
7. **Update `index.md`** — add the new source page and any new entity/concept pages.
8. **Append to `log.md`** — see Log format below.

A single source typically touches 5-15 wiki pages. After ingestion, summarize what changed for the user.

## Workflow: Query

When the user asks a question against the wiki:

1. **Read `index.md`** first to find candidate pages.
2. **Drill into relevant pages**. Follow wikilinks to gather context.
3. **Synthesize an answer with citations** — every claim links to a wiki page (or, for direct quotes, a raw source).
4. **Pick the right output format** for the question: prose, comparison table, bullet list, Marp slide deck (`.md` with `marp: true` frontmatter), matplotlib chart, canvas, etc.
5. **Offer to file the answer back** as a new page under `wiki/analyses/` if it's a non-trivial synthesis worth keeping. The user decides.

If the wiki doesn't have enough to answer, say so — don't fabricate. Suggest sources to ingest or web searches to run.

## Workflow: Lint

When the user says "lint the wiki" or periodically:

Health-check pass. Look for and report:
- **Contradictions** between pages on the same entity or concept.
- **Stale claims** — pages that newer sources have superseded but weren't updated.
- **Orphan pages** — no inbound wikilinks (use Obsidian graph or `grep`).
- **Missing pages** — concepts mentioned in 3+ sources but lacking their own page.
- **Missing cross-references** — entity A and entity B appear together repeatedly but neither page links to the other.
- **Index drift** — pages that exist on disk but aren't in `index.md`, or vice versa.
- **Open questions** — gaps the wiki has identified but not answered. Suggest sources or searches.

Report findings as a checklist. Don't fix silently; let the user prioritize.

## index.md format

Organized by category. Each entry is one line:

```markdown
- [[Source Title]] — one-line summary (date, author)
- [[Entity Name]] — one-line description
```

Sections: Sources, Entities, Concepts, Topics, Analyses. Add categories as the wiki grows.

## log.md format

Append-only. Each entry starts with a consistent prefix so it's greppable:

```markdown
## [2026-04-26] ingest | Article Title
- Created: [[Source Title]]
- Updated: [[Entity A]], [[Concept B]]
- Notes: contradicts earlier claim in [[Entity A]]; user emphasized X.
```

Newest entries at the bottom. Quick check: `grep "^## \[" log.md | tail -10`.

## Tips

- **Obsidian Web Clipper** drops articles into `raw/` as markdown.
- **Image downloads**: in Obsidian set attachment folder to `raw/assets/`. After clipping, run "Download attachments for current file" to pull images locally.
- **Graph view** shows wiki shape — hubs and orphans.
- **Marp** for slide decks (`marp: true` in frontmatter).
- **Dataview** queries page frontmatter — keep YAML clean and consistent.

## Co-evolving this file

When Claude discovers a workflow improvement, a naming convention that breaks down, or a recurring user preference, propose an edit to this file and ask the user to confirm. The schema is supposed to drift toward what works for this vault — not stay static.
````

> 📚 **Giải thích từng section:**
> - **Three layers**: nhắc lại 3 lớp raw / wiki / schema để Claude không quên.
> - **Directory structure**: bạn có thể thêm/bớt subfolder, nhớ update lại đoạn này.
> - **File conventions**: quan trọng nhất là `Frontmatter` — Dataview & Claude đều dùng để query.
> - **Workflows**: 3 workflow ingest / query / lint là "playbook" Claude phải theo. Bạn có thể thêm workflow mới (vd. `Workflow: Translate` cho song ngữ) khi cần.
> - **Co-evolving**: cho phép Claude **đề xuất** sửa schema — bạn duyệt rồi mới merge.

### 4.2 — `Home.md` (landing page)

Trang đầu tiên bạn thấy khi mở vault. Ban đầu để gần như rỗng — sẽ tự bồi đắp khi ingest sources.

Template tối giản:

```markdown
# <Tên vault>

Wiki kiến thức cá nhân của <Tên anh> — vận hành theo phương pháp **persistent LLM-maintained wiki** (xem [[reference/methodology|methodology]]).

## Bắt đầu ở đây

| | |
|---|---|
| 📜 [[CLAUDE]] | Schema — quy tắc viết & workflow ingest/query/lint |
| 🗂️ [[index]] | Catalog tất cả trang wiki, phân loại |
| 📅 [[log]] | Lịch sử ingest/query theo thời gian |
| 📚 [[reference/methodology|Methodology]] | Triết lý nền tảng của vault này |
| 🛠️ [[reference/Cách tạo bộ não thứ 2|Hướng dẫn setup]] | Cách dựng vault này từ đầu |

## Sources hiện có

*(sẽ tự cập nhật khi ingest)*

## Tip vận hành

- **Graph view** (Ctrl/Cmd+G) cho thấy cấu trúc liên kết — phát hiện hub & orphan trực quan hơn lint thủ công.
- **Khi drop source mới vào `raw/`**: chỉ cần nói *"ingest"* — Claude sẽ chạy đầy đủ workflow theo [[CLAUDE]].
```

### 4.3 — `index.md` (catalog)

Khởi tạo với section trống. Claude sẽ append entries khi ingest.

```markdown
# Index

Catalog of wiki pages. Updated on every ingest. See [[CLAUDE]] for conventions.

## Sources

*(none yet)*

## Entities

*(none yet)*

## Concepts

*(none yet)*

## Topics

*(none yet)*

## Analyses

*(none yet)*
```

### 4.4 — `log.md` (chronological log)

Khởi tạo với 1 entry bootstrap (thay `<YYYY-MM-DD>` bằng ngày hôm nay):

```markdown
# Log

Append-only chronological record. Newest at the bottom.
Format: `## [YYYY-MM-DD] <op> | <title>`. See [[CLAUDE]] for log format.

Quick check: `grep "^## \[" log.md | tail -10`

---

## [<YYYY-MM-DD>] bootstrap
- Created: vault structure (raw/, wiki/, reference/)
- Created: CLAUDE.md, Home.md, index.md, log.md, reference/methodology.md
- Notes: vault initialized following persistent LLM-maintained wiki pattern.
```

### 4.5 — `reference/methodology.md`

File này chứa **triết lý nền tảng** — copy nguyên văn từ bài blog gốc của Geoff Huntley để Claude (và sau này anh) có chỗ tham chiếu khi quên "tại sao mình làm như vậy".

Nội dung file: paste nguyên `reference/methodology.md` trong starter template này (file 7.3KB tiếng Bạn — nói về 3 layers, 3 operations Ingest/Query/Lint, indexing & logging, optional CLI tools, tips & tricks).

> 💡 Nếu bạn muốn Việt hóa, có thể nhờ Claude dịch — nhưng nên giữ bản gốc tiếng Bạn đối chiếu.

---

## Phần 5 — Khởi động Claude Code lần đầu

### 5.1 — Mở terminal trong vault folder

Quan trọng: Claude Code lấy **thư mục bạn đang đứng** làm working directory. Phải `cd` vào vault trước khi chạy `claude`.

#### macOS
**Cách 1 (Finder):**
1. Mở System Settings → Keyboard → Keyboard Shortcuts → Services → Files and Folders → bật **New Terminal at Folder**.
2. Finder → right-click folder vault → **Services → New Terminal at Folder**.

**Cách 2 (Terminal):**
```bash
cd "<đường dẫn đầy đủ tới vault>"
# ví dụ:
cd "$HOME/Documents/My Brain"
```

#### Windows
**Cách 1 (Explorer):**
- Shift + right-click trên folder vault → **Open in Terminal** (Windows 11) hoặc **Open PowerShell window here** (Windows 10).

**Cách 2 (PowerShell):**
```powershell
cd "$HOME\Documents\My Brain"
```

#### Linux
- Files manager: phụ thuộc DE — Nautilus, Dolphin, Nemo đều có "Open Terminal Here" trong right-click menu (đôi khi cần cài extension).
- Terminal: `cd "$HOME/Documents/My Brain"`

### 5.2 — Chạy `claude`

```bash
claude
```

Bạn vào REPL của Claude Code. Prompt nhìn như:
```
> 
```

### 5.3 — Verify Claude đã đọc CLAUDE.md

Đây là bước **bắt buộc** — nếu Claude không đọc được CLAUDE.md, mọi workflow sẽ sai.

Gõ vào REPL:
```
Bạn đã đọc CLAUDE.md chưa? Tóm tắt 3 lớp của vault này trong 3 dòng.
```

Câu trả lời đúng phải có 3 ý: **raw sources / wiki / schema**.

Nếu Claude không nhắc tới 3 lớp → kiểm tra:
- File `CLAUDE.md` có ở **root vault** không? (`ls CLAUDE.md` phải thấy)
- Bạn đã `cd` vào đúng vault folder trước khi chạy `claude` chưa?

### 5.4 — (Tùy chọn) Cấu hình permissions

Claude Code mặc định sẽ **hỏi xin phép** mỗi lần chạy lệnh shell hoặc đọc file ngoài vault. Để đỡ phiền, có thể tạo allowlist.

Tạo file `.claude/settings.local.json` ở root vault:

```bash
mkdir .claude
```

```powershell
New-Item -ItemType Directory -Path .claude
```

Tạo file `.claude/settings.local.json` với nội dung:
```json
{
  "permissions": {
    "allow": [
      "Read(./**)",
      "Write(./wiki/**)",
      "Write(./index.md)",
      "Write(./log.md)",
      "Write(./Home.md)",
      "Bash(ls:*)",
      "Bash(grep:*)",
      "Bash(find:*)",
      "Bash(wc:*)"
    ]
  }
}
```

> 💡 **Đừng thêm `Write(./CLAUDE.md)` vào allow.** Mỗi lần Claude muốn sửa schema phải xin phép — đó là điểm bạn muốn duyệt.

---

## Phần 6 — Workflow Ingest (source đầu tiên)

Đây là workflow **bạn sẽ chạy nhiều nhất**. Mỗi lần có source mới (article, PDF, video transcript, sách), bạn đều theo flow này.

### 6.1 — Chọn source đầu tiên

Khuyến nghị bắt đầu nhẹ:
- 1 file ngắn (PDF < 50 trang, hoặc article web < 5000 từ).
- Nội dung bạn đã quen → để dễ kiểm tra Claude trích đúng.

Ví dụ: 1 bài blog yêu thích, 1 chương sách PDF, 1 transcript họp 30 phút.

### 6.2 — Drop file vào `raw/`

Đơn giản: kéo thả file vào `raw/` trong Finder/Explorer/file manager. Hoặc:
```bash
cp ~/Downloads/article.pdf raw/
```

### 6.3 — Ra lệnh "ingest"

Trong Claude Code REPL:
```
ingest raw/article.pdf
```

Hoặc nói tự nhiên:
```
ingest cái pdf vừa drop vào raw/
```

### 6.4 — Discuss takeaways

Claude **không nên viết ngay**. Theo CLAUDE.md, bước 2 của workflow ingest là *"Discuss key takeaways with the user (3-5 bullet points). Ask what to emphasize before writing."*

Claude sẽ đọc source rồi đưa ra:
```
Đã đọc xong. Key takeaways:
1. ...
2. ...
3. ...

Bạn muốn nhấn mạnh ý nào? Có entity/concept nào bạn muốn tách trang riêng không?
```

Bạn trả lời:
- *"Tập trung vào ý 1 và 3."*
- *"Tách [tên người] thành entity page."*
- *"Bỏ qua phần phụ lục."*

> ⚠️ **Đừng skip bước này.** Đây là điểm `methodology.md` nhấn mạnh: *"stay involved, guide on what to emphasize"*. Bỏ qua = Claude tự quyết → wiki dễ bị lệch trọng tâm.

### 6.5 — Claude tự viết

Sau khi bạn xác nhận, Claude chạy nốt workflow:
1. Tạo `wiki/sources/<Tên source>.md` với frontmatter + TL;DR + key claims + danh sách entities/concepts.
2. Tạo `wiki/entities/<Tên>.md` cho mỗi entity mới.
3. Tạo `wiki/concepts/<Tên>.md` cho mỗi concept mới.
4. Cập nhật entity/concept pages đã có (append section, không overwrite).
5. Update `index.md` (thêm entries vào section tương ứng).
6. Append entry vào `log.md`.

Một source thường touch **5-15 wiki pages**.

### 6.6 — Verify

Mở Obsidian → bấm **Graph view** (`Ctrl/Cmd + G`):
- Thấy cụm node mới quanh source → đúng.
- Đọc 1-2 entity/concept page → check citation `[[Source]]` đúng.
- Mở `log.md` → entry mới ở cuối.
- Mở `index.md` → source mới + entities/concepts mới có ở đúng section.

Nếu sai → bảo Claude:
- *"Trang [X] thiếu citation về raw source."*
- *"Entity [Y] phải link tới [Z]."*
- *"Sửa trang [W] — bạn ghi sai số [N], thật ra là [M]."*

> 💡 **Mẹo từ kinh nghiệm thực tế:** Với source dài (transcript > 10.000 dòng), Claude dễ bỏ sót đoạn giữa khi đọc compress. Nên yêu cầu *"đọc theo chunks 1.300 dòng và liệt kê segments"* để verify đầy đủ. Ví dụ workflow: ingest nhiều pass, mỗi pass đọc bù phần thiếu.

---

## Phần 7 — Workflow Query (hỏi đáp với wiki)

### 7.1 — Cách hỏi

Mở Claude Code trong vault folder → hỏi câu cần synthesize:

Ví dụ thực trong starter template này:
```
Hệ thống bán hàng 8+2 và IPS 19 bước có gì chung và khác?
```

Hoặc:
```
Tổng hợp tất cả case study về bước "xử lý từ chối" trong các sources.
```

### 7.2 — Claude làm gì (theo CLAUDE.md workflow Query)

1. Đọc `index.md` để tìm trang liên quan.
2. Drill xuống các page, follow wikilinks để gom context.
3. Synthesize câu trả lời với citation `[[Page Name]]` cho mọi claim.
4. Đề xuất output format phù hợp.
5. Hỏi bạn có muốn lưu lại thành `wiki/analyses/` không.

### 7.3 — Output formats

Tùy câu hỏi, Claude có thể trả về:

| Format | Khi nào dùng | Ví dụ |
|---|---|---|
| **Prose** (mặc định) | Câu hỏi mở, cần giải thích | *"Tại sao công ty X chọn tên sản phẩm Y?"* |
| **Comparison table** | So sánh 2+ thứ | *"So sánh 3 kỹ thuật xử lý từ chối."* |
| **Bullet list** | Liệt kê | *"Liệt kê các case study Bước 4."* |
| **Marp slide deck** (`.md` với `marp: true`) | Cần trình bày | *"Tạo slide 5 phút giới thiệu Hệ thống 8+2 cho team."* |
| **Matplotlib chart** | Có data số | *"Vẽ chart giá các khoá học theo tier."* |
| **Obsidian Canvas** (`.canvas`) | Visual map | *"Vẽ canvas mối quan hệ giữa các dự án chính."* |
| **Mermaid diagram** | Flow / hierarchy | *"Vẽ flowchart workflow ingest."* |

Khi cần format đặc biệt, bạn nói rõ trong câu hỏi:
```
Tạo Marp slide 7 trang về [chủ đề], xuất ra wiki/analyses/.
```

### 7.4 — File analysis về wiki

Nếu câu trả lời có giá trị lâu dài, bảo Claude:
```
File this back as analysis.
```

Claude sẽ tạo `wiki/analyses/<Title>.md` với:
- Frontmatter `type: analysis`
- Câu hỏi gốc
- Câu trả lời với citations
- `sources: [...]` liệt kê các page đã dùng
- Update `index.md` section Analyses
- Append `log.md` entry kiểu `query | "..."`

> 💡 Theo `methodology.md`: *"your explorations compound in the knowledge base just like ingested sources do."* Đây là điểm khác biệt với ChatGPT — câu trả lời không bị chìm vào lịch sử chat.

---

## Phần 8 — Workflow Lint (health-check định kỳ)

### 8.1 — Khi nào lint

- Sau khi ingest **5-10 sources mới**.
- Định kỳ **hàng tháng**.
- Khi cảm thấy "vault đang lộn xộn" (sai citation, contradiction, đặt tên trùng...).
- Trước khi share vault với người khác.

### 8.2 — Ra lệnh

Trong Claude Code REPL:
```
lint the wiki
```

Hoặc cụ thể hơn:
```
lint vault — check contradictions, orphans, và index drift
```

### 8.3 — Claude check những gì

Theo workflow Lint trong CLAUDE.md:

| Issue | Mô tả | Cách fix |
|---|---|---|
| **Contradictions** | 2 page nói trái nhau về cùng entity/concept | Tạo section `## Contradictions` ghi cả 2 view với source link |
| **Stale claims** | Page chưa update theo source mới | Re-ingest hoặc update tay |
| **Orphan pages** | Không có inbound wikilink | Thêm link từ trang liên quan, hoặc xóa nếu không cần |
| **Missing pages** | Concept xuất hiện ≥ 3 sources nhưng chưa có page | Tạo page mới |
| **Missing cross-references** | Entity A & B luôn xuất hiện cùng nhau nhưng không link nhau | Thêm wikilink |
| **Index drift** | Page trên disk mà không có trong index.md (hoặc ngược lại) | Update index.md |
| **Open questions** | Gap đã ghi ra mà chưa lấp | Suggest source/web search |

### 8.4 — Cách dùng report

Claude trả về **checklist**, không tự fix. Bạn đọc rồi prioritize:

```
Tốt, fix #1, #3, #5. #2 và #4 để sau. #6 bạn sẽ tự xử.
```

> ⚠️ **Đừng để Claude fix silently.** CLAUDE.md & methodology.md đều warn: *"Don't fix silently; let the user prioritize."* Lý do: lint thường đụng vào logic của vault — bạn phải biết cái gì đã thay đổi.

---

## Phần 9 — Tips & nâng cao

### 9.1 — Obsidian Web Clipper workflow

Sau khi đã setup ở Phần 3.6:

1. Đang đọc article trên browser → bấm icon Web Clipper extension.
2. Chọn template (Default / Custom).
3. Save → file markdown xuất ra `raw/<title>.md`.
4. Mở file đó trong Obsidian → bấm hotkey download attachments → ảnh về `raw/assets/`.
5. Vào Claude Code REPL → `ingest raw/<file vừa clip>`.

### 9.2 — Dùng image trong wiki

Claude có thể **xem ảnh** khi cần context. Workflow:
1. Trong source page, để Claude liệt kê ảnh referenced.
2. Khi bạn hỏi câu liên quan đến ảnh → bảo *"view ảnh raw/assets/X.png trước khi trả lời"*.

> ⚠️ LLM **không đọc markdown có inline image trong 1 pass**. Phải đọc text trước, sau đó view ảnh riêng. Hơi clunky nhưng work.

### 9.3 — Marp slide deck

Cài plugin Marp:
1. Settings → Community plugins → Browse → search "Marp" → Install → Enable.

Tạo file `.md` với frontmatter:
```yaml
---
marp: true
theme: default
---

# Slide 1
Nội dung

---

# Slide 2
Nội dung
```

→ Marp render thành slide. Export PDF/PPTX qua Command Palette (`Ctrl/Cmd + P` → "Marp: Export").

Hữu ích cho: present analysis cho team, share insight ra ngoài.

### 9.4 — Dataview queries

Khi vault có nhiều page với frontmatter consistent, Dataview tự sinh list:

````markdown
```dataview
TABLE created, sources
FROM "wiki/entities"
WHERE contains(tags, "case-study")
SORT created DESC
LIMIT 20
```
````

Một số query hay dùng:
- Liệt kê tất cả source ingest tháng này.
- Đếm số entity per category.
- Tìm pages thiếu `updated` field.

> 💡 **Khi nào dùng Dataview vs static markdown:** Dataview update tự động khi có file mới, nhưng người ngoài cần cài plugin mới đọc được. Bạn Long hiện dùng static markdown trong `index.md` — đơn giản, share được ra ngoài Obsidian.

### 9.5 — Khi nào update CLAUDE.md

`CLAUDE.md` không phải đá tảng. Update khi:
- Phát hiện workflow mới (vd. *"khi ingest transcript dài phải đọc theo chunks"* — đã ghi vào memory feedback).
- Convention naming break down (vd. trùng tên giữa 2 entity → cần quy ước hậu tố).
- User có recurring preference (vd. *"luôn dịch quote sang tiếng Việt"*).

Cách thông thường: bảo Claude *"propose CLAUDE.md update for [tình huống]"* → review → merge.

### 9.6 — Tools nâng cao (khi vault lớn dần)

| Tool | Khi nào cần |
|---|---|
| **qmd** (CLI search BM25/vector) | Vault > 500 pages, search trong index.md không đủ |
| **MCP server custom** | Tích hợp domain-specific (vd. Notion, Linear, Slack) |
| **Custom skills/agents Claude Code** | Workflow lặp lại (vd. "ingest YouTube transcript" auto-download + chunk) |
| **Git versioning** | Bạn muốn rollback khi Claude phá hỏng cái gì |

---

## Phần 10 — Checklist hoàn thành

In ra giấy hoặc copy vào 1 note để tick từng mục:

### Cài đặt nền tảng
- [ ] Cài Obsidian (mở được, thấy welcome screen)
- [ ] Cài Node.js ≥ v18 (`node --version` ok)
- [ ] Cài Claude Code (`claude --version` ok)
- [ ] Login Claude Code lần đầu thành công

### Tạo vault
- [ ] Tạo folder vault, đặt tên (ví dụ `My Brain`)
- [ ] Quyết định cách sync (local / iCloud / Sync / Git...)
- [ ] Mở vault trong Obsidian
- [ ] Settings → Editor → Source mode
- [ ] Tạo cấu trúc thư mục `raw/`, `raw/assets/`, `wiki/sources/`, `wiki/entities/`, `wiki/concepts/`, `wiki/topics/`, `wiki/analyses/`, `reference/`
- [ ] Bật core plugins cần thiết
- [ ] Cài plugin Dataview

### Tạo file khung
- [ ] Tạo `CLAUDE.md` (paste template Phần 4.1)
- [ ] Tạo `Home.md` (paste template Phần 4.2)
- [ ] Tạo `index.md` (paste template Phần 4.3)
- [ ] Tạo `log.md` (paste template Phần 4.4)
- [ ] Tạo `reference/methodology.md` (paste content)
- [ ] (Optional) Tạo `.claude/settings.local.json` (Phần 5.4)

### Verify Claude Code
- [ ] `cd` vào vault folder
- [ ] Chạy `claude` → vào REPL
- [ ] Hỏi *"Tóm tắt 3 lớp của vault này"* → Claude trả lời đúng raw / wiki / schema

### Chạy thử workflow
- [ ] Ingest 1 source ngắn → có source page + ≥ 1 entity + ≥ 1 concept + index.md update + log.md update
- [ ] Query thử 1 câu → Claude trả lời với citation `[[...]]`
- [ ] (Khi có ≥ 5 sources) Lint thử → Claude trả về checklist

### Optional
- [ ] Cài Obsidian Web Clipper extension
- [ ] Bind hotkey download attachments
- [ ] Cài Marp plugin
- [ ] Setup sync (iCloud / Git / Syncthing...)

---

## Phần 11 — FAQ & Troubleshooting

### "Claude không thấy CLAUDE.md"
**Nguyên nhân thường gặp:**
- Chạy `claude` từ folder khác (không phải root vault).
- File tên sai: `claude.md` (chữ thường) thay vì `CLAUDE.md` (HOA). Trên macOS/Windows file system **không phân biệt hoa thường** nên ít ảnh hưởng, nhưng Claude Code expect đúng tên `CLAUDE.md`.
- File đặt nhầm trong `wiki/` thay vì root.

**Cách check:**
```bash
pwd                    # phải in path tới vault
ls CLAUDE.md           # phải thấy file
claude                 # vào REPL
```

### "Vault sync conflict trên iCloud / Dropbox"
- File `.obsidian/workspace.json` thay đổi liên tục → conflict.
- **Giải pháp:** vào `.gitignore` (nếu dùng Git) hoặc Obsidian Sync settings → exclude `workspace.json`.
- Nếu dùng iCloud: tránh mở vault đồng thời 2 máy.

### "Claude tạo entity page trùng tên" (vd. `Bạn Khánh.md` và `Bạn Khánh - gốm.md`)
- Nguyên nhân: Claude không biết 2 source nói cùng 1 người.
- **Giải pháp:** thêm convention vào CLAUDE.md (vd. *"entity trùng tên dùng `<Tên> - <ngữ cảnh>` để phân biệt"*).
- Đã trùng rồi: bảo Claude merge 2 page (move content sang 1 page, redirect page kia hoặc xóa).

### "Vault chậm khi > 1000 pages"
- Obsidian search index sẽ lớn.
- **Giải pháp:** Settings → Files and links → Excluded files → thêm `raw/` (Claude vẫn đọc được, nhưng Obsidian không index). Search vẫn chạy trên `wiki/`.

### "Claude tốn quá nhiều token mỗi lần ingest"
- Source dài (transcript YouTube 4 tiếng = ~30MB text).
- **Giải pháp:**
  - Pre-chunk source thành nhiều file `<title>_C01.txt`, `_C02.txt`... (chia source thành nhiều chunk nhỏ hơn).
  - Bảo Claude `read in chunks of 1300 lines, summarize each chunk first`.
  - Dùng skill `text-cat-chunk` hoặc `srt-cat-chunk` nếu đã có.

### "Tôi muốn rollback khi Claude viết sai"
- Obsidian có **File recovery** (core plugin) — settings → File recovery → snapshot interval.
- Hoặc dùng Git: `git init` trong vault, commit định kỳ.

### "Claude viết tiếng Bạn khi tôi muốn tiếng Việt"
- Theo CLAUDE.md: *"write each page in the language of its sources"*.
- Source tiếng Bạn → wiki page tiếng Anh. Source tiếng Việt → wiki page tiếng Việt.
- Muốn override: thêm vào CLAUDE.md *"All wiki pages must be written in Vietnamese, even if source is in English. Translate quotes inline."*

### "Tôi muốn dùng vault chung với team"
- iCloud/Dropbox: bị giới hạn 1 người sửa 1 lúc.
- Obsidian Sync: có collaborative mode (cần Sync plan).
- Git: tốt nhất cho team — branch + PR review. Nhưng yêu cầu mọi người biết Git.

### "Claude từ chối làm việc gì đó liên quan đến vault"
- Có thể `.claude/settings.local.json` thiếu permission.
- Thêm permission tương ứng (xem Phần 5.4).
- Hoặc gõ `/permissions` trong REPL để xem & sửa runtime.

---

## Tổng kết

Quy trình rút gọn 1 dòng:

> **Cài Obsidian + Node + Claude Code → tạo vault folder + cấu trúc thư mục → paste 5 file khung (CLAUDE.md, Home.md, index.md, log.md, methodology.md) → `cd` vào vault chạy `claude` → drop source vào `raw/` → nói "ingest" → vault tự lớn lên.**

Sau ngày đầu bạn sẽ có vault rỗng nhưng chạy được. Sau 1 tuần ingest 5-10 source, vault bắt đầu thành "bộ não" thật — bạn hỏi gì cũng có cite, lint phát hiện chỗ trống, query compound vào analyses.

Tốc độ xây vault phụ thuộc số lượng source và độ dài từng file.

**Schema sẽ tự co-evolve.** Đừng cố hoàn thiện CLAUDE.md ngày đầu — cứ ingest, gặp vấn đề thì update CLAUDE.md, tiếp tục. Sau 6 tháng vault của bạn sẽ rất khác bản template này — đó mới là điểm.

---

*File này là một phần của vault, có thể tự ingest lại nó: `ingest reference/Cách tạo bộ não thứ 2.md` để Claude biết cách dạy người khác setup khi bạn hỏi sau.*
