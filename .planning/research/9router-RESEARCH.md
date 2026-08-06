# Tham khảo 9Router — những gì lấy được và những gì cố ý không lấy

**Ngày:** 2026-08-06
**Nguồn:** https://github.com/decolua/9router (Next.js / JavaScript, source công khai)
**Dùng cho:** Task 3 (adapter) và Task 4 (router) của `2026-08-06-provider-routing.md`

Spec đã nói "tham khảo mô hình Provider, model và fallback của 9Router, nhưng không nhúng hoặc
chạy nguyên". Tài liệu này ghi cụ thể phần nào tham khảo được, sau khi đọc source thật.

## 1. Bài học chính: descriptor tách khỏi codec

9Router có 150+ file trong `open-sse/providers/registry/`, mỗi file là **dữ liệu khai báo**, không
phải code gọi API. Đây là `anthropic.js` nguyên văn:

```js
export default {
  id: "anthropic",
  priority: 30,
  category: "apikey",
  transport: {
    baseUrl: "https://api.anthropic.com/v1/messages",
    format: "claude",
    headers: { "anthropic-version": "2023-06-01", "Anthropic-Beta": "..." },
  },
  models: [{ id: "claude-sonnet-4-20250514", name: "Claude Sonnet 4" }],
  serviceKinds: ["llm", "imageToText"],
};
```

Còn `openrouter.js` **không có** `format` — nó rơi về codec OpenAI, và chỉ khai thêm header riêng:

```js
transport: {
  baseUrl: "https://openrouter.ai/api/v1/chat/completions",
  thinkingFormat: "openai",
  headers: { "HTTP-Referer": "...", "X-Title": "..." },
}
```

Nghĩa là 150 Provider **không** ứng với 150 adapter. Chúng ứng với một nhúm **format** (`claude`,
`openai`, `gemini`), và mỗi Provider chỉ khai nó nói format nào cộng vài header riêng.

**Áp dụng cho Task 3:** đừng để "bốn Provider" tự động thành "bốn bản sao mọi thứ". Tách phần khai
báo (endpoint, header, tên) khỏi phần dùng chung (transport có timeout, decode giới hạn 2 MiB,
phân loại status) và viết phần chung một lần.

**Nhưng đừng gộp quá tay — ghi lại sau khi Task 3 chạy thật.** Bản đầu của ghi chú này nói OpenAI
và OpenRouter "cùng giao thức OpenAI" nên gộp được codec generate. Sai. 9Router cho OpenAI dùng
`chat/completions`, còn plan của ta pin OpenAI vào **Responses API** (`{model, input}` →
`output[].content[].output_text`), khác hẳn OpenRouter (`{model, messages}` →
`choices[].message.content`). Gộp hai cái đó là gửi một payload mà một trong hai Provider từ chối.

Cái thực sự chung giữa chúng: envelope `GET models`, header bearer, và toàn bộ transport/decode/
status. Chừng đó gộp được và đã gộp. **Độ chính xác endpoint đứng trên việc gộp codec** — một bộ
parse dùng chung mà sai payload sẽ pass với fake server của chính mình rồi hỏng với Provider thật,
đúng cái hạng lỗi tệ nhất.

## 2. Chi tiết transport kiểm chứng được

| Việc | 9Router | Plan của ta | Kết luận |
| --- | --- | --- | --- |
| Anthropic version header | `anthropic-version: 2023-06-01` | giống hệt | khớp, yên tâm |
| Anthropic endpoint | `POST /v1/messages` | giống hệt | khớp |
| OpenRouter endpoint | `POST /api/v1/chat/completions` | giống hệt | khớp |
| OpenRouter header phụ | `HTTP-Referer` + `X-Title` | **không nhắc** | **thiếu, xem mục 4** |

## 3. Chuẩn hoá id Provider

`src/lib/providerNormalization.js` chuẩn hoá tên Provider người dùng gõ về id chuẩn: thử khớp
nguyên văn, rồi slug hoá (`toLowerCase`, ký tự lạ thành `-`), rồi khớp theo display name.

Ta **không cần** phần này: id Provider của ta do server sinh và người dùng không bao giờ gõ tay.
Ghi lại để khỏi ai đó thấy hay rồi bê về.

## 4. Việc cần bổ sung vào Task 3

OpenRouter khuyến nghị `HTTP-Referer` và `X-Title` để định danh ứng dụng gọi tới. Thiếu chúng
request vẫn chạy, nên không phải lỗi chặn — nhưng có thì đúng chuẩn nhà cung cấp hơn, và 9Router
đặt chúng cho mọi request. Thêm hai header tĩnh này vào `OpenRouterAdapter`.

## 5. Những gì cố ý KHÔNG lấy

- **Dịch định dạng hai chiều** (OpenAI ↔ Claude ↔ Gemini ↔ Cursor ↔ Ollama). 9Router là proxy phục
  vụ client bất kỳ nên buộc phải dịch xuôi ngược. Router của ta chỉ đi một chiều: prompt vào, text
  ra. Bê phần dịch về là mang theo một lớp không ai gọi.
- **Lưu credential trong SQLite trần.** 9Router để key trong `db/data.sqlite`, tài liệu không nói
  gì về mã hoá at-rest. Thiết kế của ta mã hoá bằng DPAPI theo tài khoản Windows — mạnh hơn hẳn,
  và đây là chỗ ta cố ý đi khác chứ không phải bỏ sót.
- **Phân tầng Subscription → Cheap → Free, quota, OAuth, token saver, multi-account, analytics.**
  Spec đã liệt kê tất cả những thứ này trong "Ngoài phạm vi MVP".

## 6. Chỗ ta chặt hơn 9Router

9Router **không công bố** taxonomy lỗi. Tài liệu chỉ nói chung "quota exhaustion" và "errors" kích
hoạt fallback tầng sau. Không có danh sách status code nào.

Spec của ta pin chính xác: fallback **chỉ** với timeout, lỗi mạng, `429`, `5xx`; còn credential,
model không tồn tại, request sai và nội dung bị từ chối thì **dừng chuỗi**. Đây là điểm thiết kế
của ta rõ hơn nguồn tham khảo, nên **đừng nới nó ra cho giống 9Router**. Lý do dừng ở lỗi policy
nằm trong spec: đổi Provider để né chính sách là một hành vi ta không muốn có.

## Nguồn

- [decolua/9router](https://github.com/decolua/9router)
- [registry/anthropic.js](https://github.com/decolua/9router/blob/main/open-sse/providers/registry/anthropic.js)
- [registry/openrouter.js](https://github.com/decolua/9router/blob/main/open-sse/providers/registry/openrouter.js)
- [lib/providerNormalization.js](https://github.com/decolua/9router/blob/main/src/lib/providerNormalization.js)
