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
	"bytes"
	"crypto/sha256"
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
	"agentdc/internal/store"
)

// placeholderRe recognizes every closed mustache hole. Persona templates are
// user-authored and may legitimately use lowercase, Unicode, spaces, or dashes.
var placeholderRe = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// maxPlaceholderValue chặn độ dài giá trị điền vào.
//
// Đây là TÊN, không phải một đoạn văn. Giá trị này được nhân bản vào 7 tới 19 chỗ trong prompt,
// nên một chuỗi dài là một cách bơm chỉ dẫn vào prompt của chính mình mà không ai thấy.
const maxPlaceholderValue = 60

var writeAgentFileAtomic = writeAppFileAtomic

func writeAppTemp(path string, data []byte, mode os.FileMode) (tempPath string, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath = tmp.Name()
	complete := false
	defer func() {
		_ = tmp.Close()
		if !complete {
			_ = os.Remove(tempPath)
		}
	}()

	if err = tmp.Chmod(mode); err != nil {
		return tempPath, err
	}
	if _, err = tmp.Write(data); err != nil {
		return tempPath, err
	}
	if err = tmp.Sync(); err != nil {
		return tempPath, err
	}
	if err = tmp.Close(); err != nil {
		return tempPath, err
	}
	complete = true
	return tempPath, nil
}

// writeAppFileAtomic writes beside the destination, flushes the complete file,
// then swaps it into place with the platform replacement primitive.
func writeAppFileAtomic(path string, data []byte, mode os.FileMode) error {
	return writeAppFileAtomicWith(path, data, mode, replaceAppFile)
}

func writeAppFileAtomicWith(path string, data []byte, mode os.FileMode, replace func(string, string) error) error {
	tempPath, err := writeAppTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempPath) }()
	return replace(tempPath, path)
}

// writeAppBackupOnce flushes a private temporary copy, then atomically publishes
// it with a hard link. The final .goc name is never visible with partial bytes,
// and link creation cannot overwrite a backup from an earlier edit.
func writeAppBackupOnce(path string, data []byte, mode os.FileMode) error {
	return writeAppBackupOnceWith(path, data, mode, os.Link)
}

func writeAppBackupOnceWith(path string, data []byte, mode os.FileMode, publish func(string, string) error) error {
	tempPath, err := writeAppTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempPath) }()
	if err := publish(tempPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bản lưu gốc không phải tệp thường: %s", path)
		}
		return nil
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
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

func personaValidationError(text string) string {
	withoutClosedHoles := placeholderRe.ReplaceAllString(text, "")
	if strings.Contains(withoutClosedHoles, "{{") {
		return "văn phong còn dấu {{ chưa đóng"
	}
	return ""
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
	displayName := ""
	if a.st != nil {
		displayName, err = a.st.AgentDisplayName()
		if err != nil {
			a.logger.Error("agent: đọc tên hiển thị", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không đọc được tên bot")
			return
		}
	}
	validationError := personaValidationError(string(b))
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
		"ready":            len(holes) == 0 && validationError == "",
		"display_name":     displayName,
		"validation_error": validationError,
		"model":            a.zalo.cfg.Model,
		"kb_roots":         a.zalo.cfg.KBRoots,
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
	var req appAgentPutRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	values, displayName, err := normalizeAgentPutRequest(req)
	if err != nil {
		status := http.StatusBadRequest
		if req.RequireComplete || errors.Is(err, errAgentDisplayNameConflict) {
			status = http.StatusUnprocessableEntity
		}
		code := "AGENT_VALUE_INVALID"
		if errors.Is(err, errAgentDisplayNameConflict) {
			code = "AGENT_DISPLAY_NAME_CONFLICT"
		} else if errors.Is(err, errAgentDisplayNameRequired) {
			code = "AGENT_DISPLAY_NAME_REQUIRED"
		}
		if req.RequireComplete || errors.Is(err, errAgentDisplayNameConflict) {
			a.writeLLMErr(w, status, code, err.Error(), nil)
		} else {
			a.writeErr(w, status, err.Error())
		}
		return
	}
	if req.RequireComplete {
		if !a.requireOnboardingRevision(w, req.OnboardingRevision) {
			return
		}
		a.handleAgentCompletePut(w, req.OnboardingRevision, values, displayName)
		return
	}
	if len(values) == 0 {
		a.writeErr(w, http.StatusBadRequest, "không có giá trị nào")
		return
	}
	a.handleAgentNormalPut(w, values, displayName)
}

type appAgentPutRequest struct {
	Values             map[string]string `json:"values"`
	DisplayName        *string           `json:"display_name"`
	RequireComplete    bool              `json:"require_complete"`
	OnboardingRevision int64             `json:"onboarding_revision"`
}

var (
	errAgentDisplayNameConflict = errors.New("tên bot không khớp với {{TEN_BOT}}")
	errAgentDisplayNameRequired = errors.New("cần nhập tên hiển thị của bot")
)

func normalizeAgentPutRequest(req appAgentPutRequest) (map[string]string, string, error) {
	values := make(map[string]string, len(req.Values))
	keys := make([]string, 0, len(req.Values))
	for key := range req.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, err := normalizeAgentNameValue("{{"+key+"}}", req.Values[key])
		if err != nil {
			return nil, "", err
		}
		values[key] = value
	}

	displayName := ""
	if req.DisplayName != nil {
		var err error
		displayName, err = normalizeAgentNameValue("tên hiển thị", *req.DisplayName)
		if err != nil {
			return nil, "", err
		}
	}
	if botName, ok := values["TEN_BOT"]; ok {
		if req.DisplayName != nil && displayName != botName {
			return nil, "", errAgentDisplayNameConflict
		}
		displayName = botName
	} else if req.RequireComplete && req.DisplayName == nil {
		return nil, "", errAgentDisplayNameRequired
	}
	return values, displayName, nil
}

func normalizeAgentNameValue(label, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%s chưa điền", label)
	}
	if len([]rune(value)) > maxPlaceholderValue {
		return "", fmt.Errorf("%s dài quá %d ký tự — đây là một cái tên, không phải một câu",
			label, maxPlaceholderValue)
	}
	if strings.ContainsAny(value, "\r\n") || strings.Contains(value, "{{") || strings.Contains(value, "}}") {
		return "", fmt.Errorf("%s không được chứa xuống dòng hay {{ }}", label)
	}
	return value, nil
}

func (a *api) handleAgentNormalPut(w http.ResponseWriter, values map[string]string, displayName string) {
	p, err := a.personaPath()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
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
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(known, k) {
			a.writeErr(w, http.StatusBadRequest, "không có chỗ trống {{"+k+"}} trong văn phong")
			return
		}
		text = strings.ReplaceAll(text, "{{"+k+"}}", values[k])
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
	if err := writeAgentFileAtomic(p, []byte(text), 0o600); err != nil {
		a.logger.Error("agent: ghi persona", "path", p, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không ghi được văn phong")
		return
	}
	if displayName != "" && a.st != nil {
		if err := a.st.SetAgentDisplayName(displayName); err != nil {
			if restoreErr := writeAppFileAtomic(p, b, 0o600); restoreErr != nil {
				a.logger.Error("agent: khôi phục persona sau lỗi tên hiển thị", "path", p, "err", restoreErr)
			}
			a.logger.Error("agent: lưu tên hiển thị", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không lưu được tên bot")
			return
		}
	}
	left := scanPlaceholders(text)
	validationError := personaValidationError(text)
	a.zlog.add(ipc.ZaloLogInfo, "", fmt.Sprintf("văn phong: đã điền %d chỗ, còn %d", len(keys), len(left)))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"placeholders":     left,
		"ready":            len(left) == 0 && validationError == "",
		"display_name":     displayName,
		"validation_error": validationError,
	})
}

func (a *api) handleAgentCompletePut(
	w http.ResponseWriter,
	expectedRevision int64,
	values map[string]string,
	displayName string,
) {
	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()

	state, ok := a.onboardingMutationState(w, expectedRevision)
	if !ok {
		return
	}
	if state.Phase != store.OnboardingPhasePersona {
		a.writeOnboardingStoreError(w, fmt.Errorf("complete persona from %q: %w", state.Phase, store.ErrOnboardingInvalidPhase))
		return
	}
	p, err := a.personaPath()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	original, err := os.ReadFile(p)
	if err != nil {
		a.writeErr(w, http.StatusInternalServerError, "không đọc được văn phong")
		return
	}
	text, keys, ok := a.renderAgentValues(w, string(original), values)
	if !ok {
		return
	}
	left := scanPlaceholders(text)
	validationError := personaValidationError(text)
	if len(left) != 0 || validationError != "" {
		fields := safeMissingPlaceholderFields(left)
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "AGENT_PLACEHOLDERS_REMAIN",
			"Văn phong vẫn còn chỗ trống hoặc dấu {{ chưa đóng", fields)
		return
	}

	finalBytes := []byte(text)
	changed := !bytes.Equal(original, finalBytes)
	if changed {
		if err := writeAppBackupOnce(p+".goc", original, 0o600); err != nil {
			a.logger.Error("agent: ghi bản gốc persona", "path", p+".goc", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không tạo được bản lưu gốc, chưa ghi gì")
			return
		}
		if err := writeAgentFileAtomic(p, finalBytes, 0o600); err != nil {
			a.logger.Error("agent: ghi persona hoàn chỉnh", "path", p, "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không ghi được văn phong")
			return
		}
	}

	fingerprintBytes := append(append([]byte(nil), finalBytes...), []byte(displayName)...)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(fingerprintBytes))
	updated, err := a.st.AdvanceOnboardingPersona(expectedRevision, fingerprint, displayName)
	if err != nil {
		if changed {
			if restoreErr := writeAppFileAtomic(p, original, 0o600); restoreErr != nil {
				a.logger.Error("agent: khôi phục persona sau lỗi onboarding", "path", p, "err", restoreErr)
			}
		}
		a.writeOnboardingStoreError(w, err)
		return
	}
	a.zlog.add(ipc.ZaloLogInfo, "", fmt.Sprintf("văn phong: đã điền %d chỗ, sẵn sàng kiểm tra", len(keys)))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"placeholders":     []placeholder{},
		"ready":            true,
		"display_name":     displayName,
		"onboarding_phase": updated.Phase,
		"revision":         updated.Revision,
	})
}

func (a *api) renderAgentValues(
	w http.ResponseWriter,
	text string,
	values map[string]string,
) (string, []string, bool) {
	known := make([]string, 0, len(scanPlaceholders(text)))
	for _, hole := range scanPlaceholders(text) {
		known = append(known, hole.Key)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !slices.Contains(known, key) {
			a.writeErr(w, http.StatusBadRequest, "không có chỗ trống {{"+key+"}} trong văn phong")
			return "", nil, false
		}
		text = strings.ReplaceAll(text, "{{"+key+"}}", values[key])
	}
	return text, keys, true
}

func safeMissingPlaceholderFields(holes []placeholder) map[string]string {
	fields := make(map[string]string, len(holes))
	for _, hole := range holes {
		key := strings.TrimSpace(hole.Key)
		if key == "" || len([]rune(key)) > maxPlaceholderValue || strings.ContainsAny(key, "\r\n") {
			continue
		}
		fields[key] = "chưa điền"
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}
