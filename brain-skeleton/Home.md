# <Tên vault của bạn>

Wiki kiến thức cá nhân — vận hành theo phương pháp **persistent LLM-maintained wiki** (xem [[reference/methodology|methodology]]).

> **Lần đầu mở vault?** Đọc [[README]] trước để biết cách bắt đầu trong 10 phút.

## Bắt đầu ở đây

| | |
|---|---|
| 📜 [[CLAUDE]] | Schema — quy tắc viết & workflow ingest/query/lint |
| 🗂️ [[index]] | Catalog tất cả trang wiki, phân loại |
| 📅 [[log]] | Lịch sử ingest/query theo thời gian |
| 📚 [[reference/methodology\|Methodology]] | Triết lý nền tảng (English) |
| 🛠️ [[reference/Cách tạo bộ não thứ 2\|Hướng dẫn setup]] | Quy trình dựng vault A-Z |

## Sources hiện có

*(sẽ tự cập nhật khi ingest source đầu tiên — drop file vào `raw/` và gõ "ingest" trong Claude Code)*

## Tip vận hành

- **Graph view** (`Ctrl/Cmd+G`) cho thấy cấu trúc liên kết — phát hiện hub & orphan trực quan hơn lint thủ công.
- **Khi drop source mới vào `raw/`**: chỉ cần nói *"ingest"* — Claude chạy đầy đủ workflow theo [[CLAUDE]].
- **Sau 5-10 sources**, ra lệnh `lint the wiki` để health-check.
- **Câu trả lời query có giá trị lâu dài** → bảo *"file this back as analysis"* để lưu vào `wiki/analyses/`.
- **Schema sẽ tự co-evolve** — đừng cố hoàn thiện CLAUDE.md ngày đầu, ingest gặp vấn đề rồi sửa.

## Tùy biến trang này

Khi vault giàu lên (10-20 sources), trang này nên có thêm các section:
- **Trục chính** — nhân vật / công ty / domain trung tâm
- **Framework chính** — methodology hay reference nhất
- **Sources hiện có** — list ngắn các source quan trọng
- **Câu hỏi mở** — gap đã phát hiện, chờ source trả lời
- **Lịch sử ingest gần nhất** — link tới [[log]]

(Sau 10–20 sources, Home.md nên trở thành landing page giàu thông tin — link tới hub entities, framework chính, và câu hỏi mở từ [[log]].)
