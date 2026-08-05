# brain — nơi chứa tri thức của bot

Thư mục này là **bộ nhớ dài hạn** của trợ lý Zalo. Bot đọc từ đây để trả lời khách.

Nó được giao **rỗng có chủ đích**: phần mềm đi kèm *cách nói*, không đi kèm tri thức ngành của
người bán. Cấu trúc đã dựng sẵn để bạn biết bỏ gì vào đâu.

---

## Cần cài gì thêm không

| Công cụ | Bắt buộc? | Vì sao |
| --- | --- | --- |
| **Claude Code** | **Có** | Bot viết câu trả lời bằng nó, chạy trên tài khoản của bạn. Không có nó thì phần mềm vẫn chạy, vẫn nhận tin, nhưng **không trả lời câu nào**. Xem `README.txt` ở thư mục cha. |
| **Node.js** | Không | Đã đóng kèm trong `app\node\`. Không cần cài. |
| **Obsidian** | Không | Chỉ là một trình soạn markdown. Xem bên dưới. |

### Obsidian: không cần cài

Nói ngắn: **đừng cài**, ít nhất là lúc bắt đầu. Nó không đổi gì trong cách phần mềm hoạt động.

Bot tìm bằng công cụ của riêng nó chạy thẳng trên hệ tệp. Obsidian có chỉ mục riêng nằm trong
`.obsidian\`, và bot **không có đường nào chạm vào chỉ mục đó**. Obsidian cài hay chưa, đang mở hay
đã đóng, bot chạy y như nhau.

**Tìm kiếm cũng không phải chỗ chậm.** Đo trên 300 trang wiki (1,07 MB): tìm toàn văn mất
**0,16 giây**. Một lượt trả lời của bot mất **42 đến 114 giây**, gần hết là thời gian gọi mô hình.
Nên tìm kiếm chiếm khoảng **0,2%** một lượt — có tăng tốc nó mười lần thì cũng không ai thấy khác.

Nó chỉ đáng cài về sau, khi `wiki/` đã lớn và **bạn muốn tự đọc** kho tri thức của mình, vì hai việc
Notepad không làm được:

- **Liên kết ngược** — mở một trang, thấy ngay những trang nào đang trỏ tới nó.
- **Đồ thị** — nhìn ra chủ đề nào đang cô lập, tức chưa nối vào phần còn lại.

Cả hai là để **người** kiểm tra kho tri thức, không phải để bot nhanh hơn. Nếu cài: Obsidian →
*Open folder as vault* → chọn chính thư mục `brain` này. Không phải chuyển đổi gì, nó đọc thẳng các
tệp `.md`.

Còn văn phong trong `reference/persona/` thì **sửa trong phần mềm** đúng hơn: trang **AI Agents** có
nút bút chì. Ghi qua đó thì phần mềm bảo đảm đúng bảng mã, còn Notepad có thể lưu lại bằng bảng mã
khác — và một tệp văn phong lỗi bảng mã làm bot **mất giọng mà không có gì báo**.

---

## Hai tầng, và phân biệt được chúng là điều quan trọng nhất ở đây

```
raw/     thứ bạn BỎ VÀO. Không sửa, không xoá.
wiki/    thứ đã BIÊN SOẠN thành trang ngắn, một chủ đề một trang.
```

Bot trả lời tốt nhất từ `wiki/`. `raw/` là văn bản thô, dài và lẫn — bot đọc được nhưng phải lội qua
nhiều thứ không liên quan.

```
raw/
  assets/            ảnh và tệp đính kèm
wiki/
  index.md           MỤC LỤC — bot đọc tệp này trước để biết có những trang nào
  sources/           tóm tắt từng nguồn
  entities/          người, sản phẩm, tổ chức
  concepts/          khái niệm
  topics/            chủ đề tổng hợp nhiều nguồn
  analyses/          so sánh, phân tích
reference/           cho NGƯỜI đọc, bot KHÔNG trích dẫn từ đây
  persona/           văn phong của bot
  methodology.md     phương pháp
CLAUDE.md            schema: đặt tên trang, mỗi trang gồm gì
log.md               sổ tay: đã nạp gì, ngày nào
```

**Vì sao `reference/` nằm ngoài:** một tệp trong `wiki/` hay `raw/` thì bot **trích dẫn được** nó.
Văn phong không phải căn cứ để trả lời khách, nên nó phải ở ngoài hai thư mục đó. Đây là một ranh
giới, không phải một cách xếp tệp.

---

## Đi từ raw/ sang wiki/

Cách dễ nhất, dùng chính phần mềm:

1. Mở phần mềm, vào mục **Knowledge**
2. Kéo tệp vào vùng tải lên (pdf, docx, xlsx, pptx, md, txt, csv, ảnh)
3. Bấm **Biên soạn vào wiki**

Nó chạy một agent có quyền ghi trong thư mục này, và agent đó đọc `CLAUDE.md` để biết đặt tên trang
thế nào. Việc này **gọi mô hình nên tốn phí** trên tài khoản Claude của bạn, và nó chỉ chạy khi bạn
bấm.

Cách thủ công, nếu muốn kiểm soát từng bước: mở Command Prompt, `cd` vào thư mục này, gõ `claude`,
rồi bảo nó *"đọc raw/ và viết trang wiki cho những nguồn mới"*.

---

## Để rỗng thì sao

Bot vẫn chạy, vẫn đúng văn phong, vẫn trả lời — chỉ là không có dữ kiện ngành nào để dẫn, nên nó trả
lời chung chung và **nói thẳng là chưa có căn cứ**. Đó là hành vi đúng, không phải lỗi.

Càng cụ thể càng tốt: một tệp một chủ đề, đặt tên theo chủ đề.

Bot đọc được ngay khi có tệp mới, **không cần khởi động lại**.
