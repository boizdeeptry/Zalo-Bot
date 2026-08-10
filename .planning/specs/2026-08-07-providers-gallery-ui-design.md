# Providers gallery + trang chi tiết (UI kiểu 9Router) — design

> Sub-project **#1/4** của mảng "Providers UI kiểu 9Router". Thứ tự: **#1 gallery+detail (spec này)** → #2 connect trong Portal → #3 multi-account → #4 combos. Tất cả trên nhánh `feat/cli-subscription-providers`, ship một lần ở cuối.

## Goal

Viết lại trang **Providers** của Portal thành **gallery card chia nhóm + trang chi tiết mỗi provider** theo look 9Router (nền tối, card, badge), đổ **state THẬT** từ các endpoint `/llm/*` đã có. Thuần frontend, không đụng backend.

## Non-goals (để #2–#4)

- **KHÔNG** làm luồng đăng nhập/connect (nút "Add Connection" hiển thị nhưng chưa chạy) → #2.
- **KHÔNG** làm multi-account, Round Robin, Sticky (vẽ layout được, nhưng quản lý nhiều tài khoản) → #3.
- **KHÔNG** làm bật/tắt model, Add Model, hay Combos → #4.
- **KHÔNG** đụng trang Models (trình sửa chuỗi fallback) — giữ nguyên chạy tạm tới khi #4 thay bằng khu Combos.
- **KHÔNG** thêm/sửa endpoint backend.

## Bối cảnh (đã chốt trong /discuss)

- Engine CLI-subscription đã xong (`45834d8`): 6 provider hỗ trợ = 2 CLI chính chủ (Claude Code, OpenAI Codex) + 4 API-key (OpenAI, Anthropic, Gemini, OpenRouter). Gemini CLI đã bỏ (Google khai tử login cá nhân); Gemini chỉ còn ở nhóm API-key.
- Khác biệt cốt lõi với 9Router: 9Router chạy inference bằng cách **giả client** → banner đỏ "Account may be banned". Bot mình **spawn CLI chính chủ** → **không rủi ro đó**. Nên chỗ Risk Notice đỏ, mình đặt **badge XANH "chính chủ an toàn"** — điểm bán hơn 9Router.
- Mock đã duyệt: `scratchpad/providers-mock.html`.

## Approach (đã chọn) + lý do

**Thuần frontend restyle `providers.js`**, gallery vẽ từ một **catalog cố định ở frontend** rồi phủ state thật từ `/llm/*`.

Vì sao catalog ở frontend chứ không thêm endpoint: bộ 6 provider là cố định (focused-Zalo), và tên/nhóm/logo/màu/prefix là **dữ liệu trình bày** — thuộc về frontend. Backend đã đủ để trả *trạng thái thật* (đã thêm chưa, đăng nhập chưa, model gì). Thêm endpoint catalog chỉ để lặp lại dữ liệu tĩnh là thừa (YAGNI). Nếu về sau catalog cần động (multi-account, provider tuỳ biến), #3/#4 sẽ cân nhắc — không phải bây giờ.

**Thật lòng về #1:** trên bản cài mới, gallery hầu hết hiện **"Chưa kết nối"** (chưa ai thêm provider) — đúng trạng thái fresh install. Trạng thái "2 đã kết nối" giàu như mock chỉ xuất hiện sau khi #2/#3 thêm tài khoản. #1 giao đúng: **look + điều hướng + đọc state thật + search**.

## Kiến trúc

Trang `providers` là một module SPA trong Portal (hash route `#providers`). Viết lại thành 3 đơn vị nhỏ trong cùng `providers.js`:

1. **catalog** — hằng dữ liệu 6 provider (thuần đọc, không state).
2. **gallery view** — render 2 nhóm card từ catalog + state thật; search; "Kiểm tra tất cả".
3. **detail view** — render 1 provider: header + badge an toàn + Connections + Available Models.

Điều hướng trong trang: `#providers` = gallery; `#providers/<kind>` = detail của provider đó (ví dụ `#providers/codex`). Back = về `#providers`.

### Catalog (hằng frontend)

Mỗi mục: `{ kind, name, group, prefix, logoText, logoColor }`.

| kind | name | group | prefix | logoColor |
|------|------|-------|--------|-----------|
| `claude-code` | Claude Code | subscription | `cc` | `#c8613b` |
| `codex` | OpenAI Codex | subscription | `cx` | `#0f7a63` |
| `openai` | OpenAI | apikey | `oa` | `#10a37f` |
| `anthropic` | Anthropic | apikey | `an` | `#c8613b` |
| `gemini` | Gemini | apikey | `gm` | `#3f6ff5` |
| `openrouter` | OpenRouter | apikey | `or` | `#5b5ef0` |

Hai nhóm hiển thị: **"Gói thuê bao — CLI chính chủ"** (`subscription`, có badge xanh ở group + trang detail) và **"API Key"** (`apikey`, không badge). `kind` khớp đúng `p.Kind` mà engine dùng, để đối chiếu với `GET /llm/providers`.

Prefix chỉ để **hiển thị** id model kiểu 9Router (`cx/gpt-5.6-terra`); nút copy chép id đã có prefix. Chưa mang nghĩa chức năng ở #1 (Combos có thể dùng làm alias sau).

## Data flow (chỉ đọc, endpoint sẵn có)

Khi vào `#providers`:

1. `GET /llm/providers` → danh sách provider ĐÃ thêm vào store (+ model của chúng). Dựng map `kind → providerRecord`.
2. Render gallery từ **catalog**; mỗi card:
   - Không có trong map → trạng thái **"Chưa kết nối"** (chưa thêm).
   - Có trong map → gọi `POST /llm/providers/{id}/test` (rẻ: CLI = `checkCLIAuth`, API = kiểm khoá) → **"Connected"** / **"Chưa đăng nhập"**. Trong lúc chờ: "đang kiểm…".
3. "Kiểm tra tất cả" (mỗi nhóm) → chạy `POST /llm/providers/{id}/test` loạt cho các provider đã thêm trong nhóm, cập nhật trạng thái card.
4. Search → lọc client-side theo tên/kind trên catalog.

Khi vào `#providers/<kind>`:

1. Tra `kind` trong catalog (không có → 404 nhẹ, nút về gallery).
2. Nếu provider đã thêm: đọc model từ record (hoặc `POST /llm/providers/{id}/discover` nếu cần) → lưới **Available Models** (id `<prefix>/<modelId>` + tên + copy). `POST /.../{id}/test` để hiện trạng thái Connections.
3. Nếu chưa thêm: Connections hiện **"No connections yet" + nút "Add Connection"** (disabled/gắn nhãn "sắp có" — luồng ở #2); Available Models hiện danh sách seed tĩnh của descriptor nếu lấy được, hoặc rỗng với chú thích.

## UI (bám mock đã duyệt)

- **Gallery:** header (tiêu đề "Providers" + phụ đề + ô Search) → nhóm "Gói thuê bao — CLI chính chủ" (badge xanh cạnh tiêu đề nhóm + nút "Kiểm tra tất cả") → grid card → nhóm "API Key" (+ "Kiểm tra tất cả") → grid card. Card = logo vuông bo góc + tên + dòng trạng thái (chấm xanh "Connected" / chấm xám "Chưa kết nối").
- **Detail:** "← Về Providers" → header (logo lớn + tên + số kết nối) → **badge xanh**: *"Đăng nhập chính chủ qua CLI — không giả client, không proxy, không rủi ro khoá tài khoản."* → panel **Kết nối** (danh sách connection hoặc "No connections yet" + nút Add Connection; Round Robin/Sticky vẽ ở layout nhưng thuộc #3) → panel **Model khả dụng** (lưới model + copy + "Thêm model"[#4] + "Tắt tất cả"[#4]).
- Nền tối + accent xanh khớp Portal hiện tại (không dùng cam 9Router). Không emoji trong bản thật — dùng ký hiệu/inline SVG/icon-set sẵn có của Portal cho logo và icon.

## Ranh giới nút (present nhưng chưa chạy)

Các nút sau **hiển thị đúng chỗ** để giữ look, nhưng **chưa gắn luồng** (gắn nhãn "sắp có" hoặc disabled, có tooltip): Add Connection / Bulk Add / Test từng cái (#2, #3), bật-tắt model / Add Model / Tắt tất cả (#4). "Kiểm tra tất cả" và "Test" của provider ĐÃ thêm thì **chạy thật** (endpoint có sẵn).

## Error handling

- `GET /llm/providers` lỗi → gallery hiện khối lỗi inline + nút "Thử lại", KHÔNG để trắng.
- `POST /.../{id}/test` lỗi/timeout → card về trạng thái **"chưa kiểm tra"** (KHÔNG bịa "Connected"/"lỗi khoá"); không chặn render các card khác.
- `#providers/<kind>` với kind lạ → thông báo nhẹ + nút về gallery.
- Mọi fetch bọc try/catch, log ra console qua logger sẵn có của Portal, không nuốt lỗi im lặng.

## Testing (Portal `node --test`, `.mjs`)

Thay/ghi test trang Providers (mirror `models.test.mjs`). Ghim hành vi qua DOM, không mock nội bộ:

1. Gallery render **đủ 2 nhóm** với đúng bộ card catalog (6 provider, đúng nhóm).
2. Nhóm subscription có **badge xanh an toàn**; **không** có chuỗi "Risk Notice"/"banned" ở đâu.
3. Trạng thái card khớp `GET /llm/providers` giả lập: provider trong danh sách → "Connected/Chưa đăng nhập" theo kết quả `test`; ngoài danh sách → "Chưa kết nối".
4. Bấm card → điều hướng sang detail đúng provider; "← Về Providers" quay lại gallery.
5. Detail: badge xanh có mặt; panel Connections + Available Models render; nút chưa-chạy ở trạng thái disabled/"sắp có".
6. Search lọc đúng theo tên.
7. `GET /llm/providers` lỗi → khối lỗi + nút thử lại (không trắng).

Chạy: `npm --prefix appmode test`. Cổng gofmt/go không liên quan (thuần frontend), nhưng `build-app.ps1` vẫn phải xanh (typecheck + package) trước khi coi #1 xong.

## File structure

- **Sửa:** `appmode/overlay/internal/webui/static/pages/providers.js` — viết lại: `catalog`, render gallery, render detail, router trong trang, search, actions (chạy: test/kiểm-tra-tất-cả; deferred: phần còn lại).
- **Sửa:** `appmode/overlay/internal/webui/static/portal.css` — thêm style gallery/detail/badge/card từ mock, theo đúng quy ước scope Provider của Task 7 (để test "Provider CSS regions stays scoped to its page" còn xanh). *Lưu ý:* nếu Task 7 để style provider inline trong `providers.js` thay vì `portal.css`, plan theo đúng chỗ đó — mở `providers.js` khi lập kế hoạch để xác nhận.
- **Sửa:** `appmode/tests/providers.test.mjs` — thay test trang cũ bằng bộ test trên. Có thể phải chỉnh `navigation.test.mjs`/`router.test.mjs` nếu route `#providers/<kind>` đụng router chung (plan kiểm khi mở code).
- **Không đụng:** `models.js`, backend, seam build.

## Rủi ro / câu hỏi mở

- **CSS provider ở `portal.css` hay inline trong `providers.js`** — plan xác nhận khi mở code Task 7; giữ nguyên quy ước scope để test "CSS scoped per page" còn xanh.
- **`test` loạt khi mở trang** có thể chậm nếu nhiều provider đã thêm — ở fresh install chỉ 0–1 nên không lo; nếu về sau nhiều, cân nhắc lười-tải (chỉ test khi bấm). Ghi chú, chưa tối ưu ở #1.
- Prefix hiển thị (`cx/`, `cc/`…) thuần cosmetic ở #1; nếu #4 Combos dùng nó làm alias thật thì thống nhất lại ở đó.
