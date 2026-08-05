package daemon

// AI Agents: cấu hình con agent trả lời Zalo.
//
// Tệp này chỉ được chèn vào bản đóng gói qua appmode/overlay.
//
// Việc chính của trang này không phải "hiện cấu hình" mà là TRẢ LỜI MỘT CÂU: bot đã sẵn sàng nói
// chuyện với khách chưa. Với bản giao đi thì câu trả lời là CHƯA, vì persona.md còn chỗ trống —
// và nếu không ai nói ra thì bot sẽ thật sự gửi cho khách một câu chứa "{{TEN_BOT}}".
//
// Nên nó quét chỗ trống trong persona.md, cho điền, ghi lại, rồi quét lại. Không có ô nào để
// người dùng dán cả tệp vào: sửa toàn văn thì mở tệp bằng Notepad đúng hơn, còn thứ BẮT BUỘC phải
// điền thì phải có một chỗ không thể bỏ qua.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"agentdc/internal/ipc"
)

// placeholderRe khớp chỗ trống dạng {{TEN_HOA}}.
//
// Chỉ chữ in và gạch dưới, có chủ đích: nó phải không bao giờ khớp một câu tiếng Việt bình thường
// trong cẩm nang. Một mẫu rộng hơn sẽ biến một dòng ví dụ thành một ô nhập.
var placeholderRe = regexp.MustCompile(`\{\{([A-Z][A-Z_]*)\}\}`)

// maxPlaceholderValue chặn độ dài giá trị điền vào.
//
// Đây là TÊN, không phải một đoạn văn. Giá trị này được nhân bản vào 7 tới 19 chỗ trong prompt,
// nên một chuỗi dài là một cách bơm chỉ dẫn vào prompt của chính mình mà không ai thấy.
const maxPlaceholderValue = 60

// writeAppFileAtomic writes beside the destination, flushes the complete file,
// then swaps it into place. A crash can therefore leave the old or new persona,
// never a half-written one.
func writeAppFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// writeAppBackupOnce creates the recovery copy before the first edit and never
// overwrites it. O_EXCL also closes the race between checking and creating.
func writeAppBackupOnce(path string, data []byte, mode os.FileMode) (err error) {
	backup, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = backup.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err = backup.Write(data); err != nil {
		return err
	}
	if err = backup.Sync(); err != nil {
		return err
	}
	if err = backup.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

// personaPath lấy đường persona đang được nạp.
func (a *api) personaPath() (string, error) {
	if a.zalo == nil {
		return "", fmt.Errorf("vòng trực chưa bật: chưa cấu hình " + envZaloKBDirs)
	}
	p := strings.TrimSpace(a.zalo.cfg.PersonaPath)
	if p == "" {
		return "", fmt.Errorf("chưa cấu hình " + envZaloPersona)
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("không đọc được %s", p)
	}
	return p, nil
}

type placeholder struct {
	Key    string `json:"key"`
	Count  int    `json:"count"`
	Sample string `json:"sample"`
}

// scanPlaceholders tìm chỗ trống và MỘT dòng ví dụ cho mỗi cái.
//
// Dòng ví dụ là phần quan trọng: "TEN_CHUYEN_GIA" một mình không nói được nó là ai. Dòng
// "Gọi người truyền tri thức: {{TEN_CHUYEN_GIA}}" thì nói được, và người điền không phải đoán.
func scanPlaceholders(text string) []placeholder {
	counts := map[string]int{}
	sample := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		for _, m := range placeholderRe.FindAllStringSubmatch(line, -1) {
			k := m[1]
			counts[k]++
			if _, ok := sample[k]; !ok {
				sample[k] = clip(strings.TrimSpace(strings.TrimLeft(line, "-* \t")), 160)
			}
		}
	}
	out := make([]placeholder, 0, len(counts))
	for k, n := range counts {
		out = append(out, placeholder{Key: k, Count: n, Sample: sample[k]})
	}
	// Thứ tự ổn định theo tên: một danh sách nhảy chỗ giữa hai lần tải làm người dùng mất chỗ.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// handleAgentGet: mọi thứ trang AI Agents cần, trong một lời gọi.
func (a *api) handleAgentGet(w http.ResponseWriter, _ *http.Request) {
	p, err := a.personaPath()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		a.logger.Error("agent: đọc persona", "path", p, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không đọc được văn phong")
		return
	}
	holes := scanPlaceholders(string(b))
	roster, overlay := "", ""
	if a.zalo != nil {
		roster, overlay = a.zalo.cfg.RosterPath, a.zalo.cfg.OverlayDir
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"persona_path": p,
		"persona_name": filepath.Base(p),
		"persona_size": len(b),
		"roster_path":  roster,
		"overlay_dir":  overlay,
		"placeholders": holes,
		// ready là câu trả lời cho "mở cho khách thật được chưa". Một cờ, không một danh sách,
		// vì trang cần đổi màu theo nó.
		"ready":    len(holes) == 0,
		"model":    a.zalo.cfg.Model,
		"kb_roots": a.zalo.cfg.KBRoots,
		// Phạm vi quyền của agent trả lời khách. Cố định, KHÔNG đặt được từ đây — xem ghi chú ở
		// handleAgentPut.
		"tools": []string{"Read", "Grep", "Glob", "WebFetch"},
	})
}

// handleAgentPut điền chỗ trống vào persona.md.
//
// KHÔNG có endpoint nào đổi được "tools". Phạm vi quyền của agent trả lời khách là chỉ-đọc, và nó
// cố định trong mã chứ không phải một ô cấu hình: một cú bấm thêm quyền Write cho con agent đang
// tự động trả lời khách trong nhóm đông là thứ không có đường lùi. Muốn đổi thì phải sửa mã và
// build lại — đó là chủ đích, không phải thiếu sót.
func (a *api) handleAgentPut(w http.ResponseWriter, r *http.Request) {
	p, err := a.personaPath()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	var req struct {
		Values map[string]string `json:"values"`
	}
	if err := readJSON(w, r, &req); err != nil {
		a.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Values) == 0 {
		a.writeErr(w, http.StatusBadRequest, "không có giá trị nào")
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		a.writeErr(w, http.StatusInternalServerError, "không đọc được văn phong")
		return
	}
	text := string(b)
	// Chỉ nhận key ĐANG CÓ trong tệp: một key lạ nghĩa là trang đã cũ so với tệp, và im lặng bỏ
	// qua thì người dùng tưởng đã điền.
	known := make([]string, 0, 4)
	for _, h := range scanPlaceholders(text) {
		known = append(known, h.Key)
	}
	keys := make([]string, 0, len(req.Values))
	for k := range req.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(known, k) {
			a.writeErr(w, http.StatusBadRequest, "không có chỗ trống {{"+k+"}} trong văn phong")
			return
		}
		v := strings.TrimSpace(req.Values[k])
		if v == "" {
			a.writeErr(w, http.StatusBadRequest, "{{"+k+"}} chưa điền")
			return
		}
		if len([]rune(v)) > maxPlaceholderValue {
			a.writeErr(w, http.StatusBadRequest,
				fmt.Sprintf("{{%s}} dài quá %d ký tự — đây là một cái tên, không phải một câu",
					k, maxPlaceholderValue))
			return
		}
		// Xuống dòng và {{ }} đều bị từ chối: giá trị này được nhân vào tới 19 chỗ trong prompt,
		// nên một chuỗi nhiều dòng là một cách bơm chỉ dẫn mới vào prompt của chính mình.
		if strings.ContainsAny(v, "\r\n") || strings.Contains(v, "{{") || strings.Contains(v, "}}") {
			a.writeErr(w, http.StatusBadRequest, "{{"+k+"}} không được chứa xuống dòng hay {{ }}")
			return
		}
		text = strings.ReplaceAll(text, "{{"+k+"}}", v)
	}

	// Bản gốc, ghi MỘT lần và không bao giờ ghi lại.
	//
	// persona.md là thứ đắt nhất trong gói này. Điền sai một cái tên rồi muốn quay lại thì không
	// có git ở đây, và chỗ trống đã bị thay mất nên không tìm lại được. Một tệp .goc cạnh nó là
	// đường lùi duy nhất, và nó phải được tạo TRƯỚC lần ghi đầu.
	backup := p + ".goc"
	if err := writeAppBackupOnce(backup, b, 0o600); err != nil {
		a.logger.Error("agent: ghi bản gốc persona", "path", backup, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không tạo được bản lưu gốc, chưa ghi gì")
		return
	}
	if err := writeAppFileAtomic(p, []byte(text), 0o600); err != nil {
		a.logger.Error("agent: ghi persona", "path", p, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không ghi được văn phong")
		return
	}
	left := scanPlaceholders(text)
	a.zlog.add(ipc.ZaloLogInfo, "", fmt.Sprintf("văn phong: đã điền %d chỗ, còn %d", len(keys), len(left)))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"placeholders": left,
		"ready":        len(left) == 0,
	})
}
