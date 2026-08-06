# Planning state

step: research
current_topic: cli-subscription-providers
current_spec: .planning/specs/2026-08-06-cli-subscription-providers-design.md
last_updated: 2026-08-06

## Đã ship

- **Provider routing** — merge vào `main` ở `40cd198`. Bốn API Provider với chuỗi
  fallback toàn cục, khoá mã hoá DPAPI, lượt có tệp đi thẳng Claude Code, cổng quét
  credential trong gói. Chưa push lên `origin`.

## Đang làm

- **CLI subscription providers** — spec duyệt ở `d1e4799`, qua review độc lập. Gói
  thuê bao (Claude/ChatGPT/Google AI) thành mắt xích chuỗi fallback bằng cách spawn
  CLI chính chủ đã đăng nhập. Bước tiếp: `/plan`.

## Xếp hàng, mỗi cái một vòng /discuss

1. **Multi-account** — nhiều tài khoản mỗi Provider. Tiền đề cho Round Robin.
2. **Combos** — chuỗi Models chọn chiến lược: Capacity auto-switch, Round Robin,
   Fusion. Người dùng đã chọn cả ba.
