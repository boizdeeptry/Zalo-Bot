# Planning state

step: ship
current_plan: .planning/plans/2026-08-06-provider-routing.md
current_spec: .planning/specs/2026-08-06-provider-routing-design.md
research: .planning/research/9router-RESEARCH.md
branch: feat/provider-routing
last_updated: 2026-08-06

## Notes

- Cả 9 task đã thực thi và qua hai vòng review mỗi task; review cuối trên toàn nhánh
  tìm thêm hai lỗi tích hợp, đã sửa ở `547abc4`.
- Spec và plan viết trên máy khác nên đường dẫn kiểm chứng đã đổi sang
  `$env:ZALOBOT_REPO` / `$env:ZALOBOT_PERSONA`; xem `README.md`.
- STATE.md này được tạo lúc `/execute` vì dự án chưa từng chạy `/new-project`.

## Còn lại (Minor, cố ý hoãn)

- `/llm` phát vài trường không trang nào đọc: `last_checked_at`, `position`,
  envelope `{"provider":…}`, `{"ok":true}`.
- `hintFields` trong `providers.js` chỉ nối `fields.name` và `fields.credential`;
  server còn gửi `fields.kind` và `fields.model_id` mà UI bỏ đi.
- Comment `providers.js:36` nói `/llm` từ chối thân thiếu — sai, `decodeLLMBody`
  chỉ đặt `DisallowUnknownFields` và chấp nhận thân rỗng.
- `selectorsIn` trùng nguyên văn ở `shell.test.mjs` và `models.test.mjs`.
- `.providers-page .facts` là bản sao byte-identical của `.agents-page .facts`,
  trong khi comment nói là "dùng lại".
