# Planning state

step: ready
last_shipped: .planning/specs/2026-08-06-provider-routing-design.md
last_updated: 2026-08-06

## Đã xong

- **Provider routing** — merge vào `main` ở `40cd198` (31 commit trên nhánh
  `feat/provider-routing`). Bốn API Provider với chuỗi fallback toàn cục, khoá mã
  hoá DPAPI, lượt có tệp đi thẳng Claude Code, cổng quét credential trong gói.
  Chưa push lên `origin`.

## Minor cố ý hoãn

- `/llm` phát vài trường không trang nào đọc: `last_checked_at`, `position`,
  envelope `{"provider":…}`, `{"ok":true}`.
- `hintFields` trong `providers.js` bỏ qua `fields.kind` và `fields.model_id`.
- Comment `providers.js:36` nói `/llm` từ chối thân thiếu — sai.
- `selectorsIn` trùng nguyên văn ở `shell.test.mjs` và `models.test.mjs`.
- `.providers-page .facts` là bản sao của `.agents-page .facts` chứ không phải
  dùng lại.
