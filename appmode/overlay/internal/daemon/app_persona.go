package daemon

// Sửa toàn văn tệp văn phong và sổ tay từ trong portal. CHỈ có trong bản đóng gói.
//
// Ban đầu tôi bỏ việc này, với lý do "sửa toàn văn thì mở Notepad đúng hơn". Lý do đó sai, và sai
// theo một cách đo được: Notepad trên Windows có thể ghi lại tệp bằng một encoding khác, và một
// persona đọc ra mojibake làm bot MẤT GIỌNG mà không có gì báo — đã gặp đúng lỗi đó trong lúc dựng
// phần mềm này, với một tệp .ps1 bị thêm BOM.
//
// Ghi qua đây thì encoding do daemon quyết: UTF-8, không BOM, xuống dòng \n. Nên đường này không
// chỉ tiện hơn Notepad, nó ĐÚNG hơn Notepad.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"agentdc/internal/ipc"
)

// maxPersonaBytes chặn kích cỡ tệp ghi vào.
//
// Cẩm nang thật là 19 KB. 400 KB là trần rộng gấp hai mươi lần, đủ cho mọi lần bồi đắp thật, và
// vẫn chặn được việc dán cả một cuốn sách vào prompt của mọi câu trả lời.
const maxPersonaBytes = 400 << 10

// editableFile ánh xạ TÊN sang đường dẫn. Client gửi tên, không bao giờ gửi đường dẫn.
//
// Đây là cả cửa an toàn của tệp này. Nhận một đường dẫn từ request thì endpoint này thành đường
// GHI ĐÈ mọi tệp trên máy: credentials.json của Zalo, token của daemon, một tệp hệ thống. Nhận một
// tên trong đúng hai lựa chọn thì không có gì để lách.
func (a *api) editableFile(name string) (path, label string, err error) {
	if a.zalo == nil {
		return "", "", fmt.Errorf("vòng trực chưa bật")
	}
	switch name {
	case "persona":
		path, label = a.zalo.cfg.PersonaPath, "văn phong"
	case "roster":
		path, label = a.zalo.cfg.RosterPath, "sổ tay thành viên"
	default:
		return "", "", fmt.Errorf("chỉ sửa được văn phong hoặc sổ tay")
	}
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("chưa cấu hình tệp %s", label)
	}
	return path, label, nil
}

// handlePersonaGet trả về nội dung thô để đưa vào ô sửa.
func (a *api) handlePersonaGet(w http.ResponseWriter, r *http.Request) {
	p, label, err := a.editableFile(r.PathValue("name"))
	if err != nil {
		a.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		// Sổ tay được phép KHÔNG tồn tại: nó là tuỳ chọn, và một ô sửa rỗng đúng hơn một lỗi.
		if os.IsNotExist(err) {
			a.writeJSON(w, http.StatusOK, map[string]any{"text": "", "path": p, "label": label})
			return
		}
		a.logger.Error("persona: đọc tệp", "path", p, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không đọc được tệp "+label)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"text": string(b), "path": p, "label": label, "bytes": len(b),
	})
}

// handlePersonaPut ghi lại toàn văn.
func (a *api) handlePersonaPut(w http.ResponseWriter, r *http.Request) {
	p, label, err := a.editableFile(r.PathValue("name"))
	if err != nil {
		a.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(w, r, &req); err != nil {
		a.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Rỗng bị TỪ CHỐI cho văn phong, cho phép cho sổ tay.
	//
	// Một persona rỗng không làm bot hỏng thấy được — nó vẫn trả lời, chỉ là bằng giọng mặc định
	// của mô hình. Đó đúng là loại lỗi tệ nhất: im lặng, và chỉ khách nhận ra.
	if strings.TrimSpace(req.Text) == "" && p == a.zalo.cfg.PersonaPath {
		a.writeErr(w, http.StatusBadRequest,
			"văn phong không được để rỗng: bot sẽ vẫn trả lời nhưng mất hẳn giọng")
		return
	}
	if len(req.Text) > maxPersonaBytes {
		a.writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("dài quá %d KB — toàn bộ tệp này vào prompt của MỌI câu trả lời",
				maxPersonaBytes>>10))
		return
	}
	// JSON hợp lệ đã là UTF-8, nhưng kiểm lại: readJSON có thể nhận một chuỗi mang byte thay thế,
	// và một tệp văn phong hỏng encoding làm bot mất giọng lặng lẽ.
	if !utf8.ValidString(req.Text) {
		a.writeErr(w, http.StatusBadRequest, "nội dung không phải UTF-8 hợp lệ")
		return
	}

	// Bản gốc, ghi MỘT lần. Cùng cơ chế với việc điền chỗ trống, và cùng lý do: ổ này không có
	// git, nên .goc là đường lùi duy nhất.
	old, readErr := os.ReadFile(p)
	backup := p + ".goc"
	if readErr != nil && !(r.PathValue("name") == "roster" && errors.Is(readErr, os.ErrNotExist)) {
		a.logger.Error("persona: đọc tệp trước khi lưu", "path", p, "err", readErr)
		a.writeErr(w, http.StatusInternalServerError, "không đọc được tệp "+label+", chưa ghi gì")
		return
	}
	if readErr == nil {
		if err := writeAppBackupOnce(backup, old, 0o600); err != nil {
			a.logger.Error("persona: ghi bản gốc", "path", backup, "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không tạo được bản lưu gốc, chưa ghi gì")
			return
		}
	}

	// \r\n -> \n. Ô textarea của trình duyệt trả về \r\n theo chuẩn HTML, và một tệp lẫn hai kiểu
	// xuống dòng làm mọi lần so sánh về sau nhiễu.
	text := strings.ReplaceAll(req.Text, "\r\n", "\n")
	if err := writeAppFileAtomic(p, []byte(text), 0o600); err != nil {
		a.logger.Error("persona: ghi tệp", "path", p, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không ghi được tệp "+label)
		return
	}
	holes := scanPlaceholders(text)
	a.zlog.add(ipc.ZaloLogInfo, "", fmt.Sprintf("%s đã sửa (%d KB, %d chỗ trống còn lại)",
		label, len(text)>>10, len(holes)))
	// Trả về chỗ trống còn lại: người dùng có thể vừa dán vào một đoạn mang {{...}} mới, và trang
	// phải biết để đổi bảng trạng thái.
	a.writeJSON(w, http.StatusOK, map[string]any{
		"bytes": len(text), "placeholders": holes, "ready": len(holes) == 0,
	})
}
