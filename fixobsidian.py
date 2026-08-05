import io
import sys

# Sua lai phan Obsidian trong README cua brain.
#
# Ban truoc liet "tim toan van nhanh tren ca tram trang" nhu mot ly do cai Obsidian. Do la NOI QUA,
# va do duoc: grep tren 300 trang (1,07 MB) mat 161-165 ms, trong khi mot luot goi mo hinh mat
# 42-114 GIAY. Tim kiem la 0,2% cua mot luot. Va bot khong dung search cua Obsidian -- no dung
# ripgrep tren he tep.
P = r'F:\dist\_build\brain-skeleton\README.md'
s = io.open(P, encoding='utf-8').read()

OLD = """### Obsidian giúp được gì, và khi nào cần

Không cần để phần mềm chạy. Mọi tệp ở đây là markdown thuần, mở bằng Notepad cũng được, và `wiki/`
thì do agent tự viết chứ không phải bạn gõ tay.

Nó đáng cài khi `wiki/` đã lớn, vì ba việc Notepad không làm được:

- **Liên kết ngược** — mở một trang và thấy ngay những trang nào đang trỏ tới nó.
- **Đồ thị** — nhìn ra chủ đề nào đang cô lập, tức chưa được nối vào phần còn lại.
- **Tìm toàn văn** nhanh trên cả trăm trang.

Cách dùng: Obsidian → *Open folder as vault* → chọn chính thư mục `brain` này. Không phải chuyển
đổi gì, nó đọc thẳng các tệp `.md`."""

NEW = """### Obsidian: không cần cài

Nói ngắn: **đừng cài**, ít nhất là lúc bắt đầu. Nó không đổi gì trong cách phần mềm hoạt động.

Bot tìm bằng công cụ của riêng nó chạy thẳng trên hệ tệp. Obsidian có chỉ mục riêng nằm trong
`.obsidian\\`, và bot **không có đường nào chạm vào chỉ mục đó**. Obsidian cài hay chưa, đang mở hay
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
tệp `.md`."""

if OLD not in s:
    sys.exit('khong khop doan Obsidian')
io.open(P, 'w', encoding='utf-8', newline='\n').write(s.replace(OLD, NEW, 1))
print('brain/README.md: sua lai phan Obsidian, them so do duoc')
