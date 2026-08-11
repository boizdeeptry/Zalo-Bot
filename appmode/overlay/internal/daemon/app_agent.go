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
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

const (
	agentPersonaRecoverySuffix  = ".agentdc-recovery"
	agentPersonaRecoveryVersion = 2
	agentPersonaRecoveryDomain  = "agentdc/agent-persona-recovery/v2"
)

var (
	writeAgentFileAtomic   = writeAppFileAtomic
	restoreAgentFileAtomic = writeAppFileAtomic

	errAgentPersonaRecoveryPending = errors.New("agent persona recovery is pending")
)

type agentPersonaRecoveryRecord struct {
	Version         int    `json:"version"`
	Domain          string `json:"domain"`
	Token           string `json:"token"`
	PreviousToken   string `json:"previous_token"`
	PersonaBinding  string `json:"persona_binding"`
	Original        []byte `json:"original"`
	OriginalHash    string `json:"original_sha256"`
	ReplacementHash string `json:"replacement_sha256"`
}

type agentPersonaRecoveryJournal struct {
	record agentPersonaRecoveryRecord
	info   os.FileInfo
}

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

func agentPersonaRecoveryPath(personaPath string) string {
	return personaPath + agentPersonaRecoverySuffix
}

func agentPersonaRecoveryBinding(personaPath string) (string, error) {
	absolute, err := filepath.Abs(personaPath)
	if err != nil {
		return "", fmt.Errorf("resolve persona recovery identity: %w", err)
	}
	canonical := filepath.Clean(absolute)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	return agentFramedSHA256(agentPersonaRecoveryDomain+"/path", []byte(canonical)), nil
}

func (a *api) prepareAgentPersonaRecovery(
	personaPath string,
	original []byte,
	replacement []byte,
) (string, error) {
	if a.st == nil {
		return "", fmt.Errorf("prepare persona recovery without Store")
	}
	if len(original) > maxPersonaBytes || len(replacement) > maxPersonaBytes {
		return "", fmt.Errorf("persona recovery document is oversized")
	}
	previousToken, err := a.st.AgentPersonaRecoveryToken()
	if err != nil {
		return "", fmt.Errorf("read previous persona recovery token: %w", err)
	}
	if previousToken != "" && !validAgentRecoveryToken(previousToken) {
		return "", fmt.Errorf("previous persona recovery token is invalid")
	}
	binding, err := agentPersonaRecoveryBinding(personaPath)
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("create persona recovery token: %w", err)
	}
	record := agentPersonaRecoveryRecord{
		Version:         agentPersonaRecoveryVersion,
		Domain:          agentPersonaRecoveryDomain,
		Token:           hex.EncodeToString(tokenBytes),
		PreviousToken:   previousToken,
		PersonaBinding:  binding,
		Original:        append([]byte(nil), original...),
		OriginalHash:    agentFramedSHA256(agentPersonaRecoveryDomain+"/original", original),
		ReplacementHash: agentFramedSHA256(agentPersonaRecoveryDomain+"/replacement", replacement),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode persona recovery obligation: %w", err)
	}
	if len(encoded) > maxPersonaBytes*2 {
		return "", fmt.Errorf("persona recovery obligation is oversized")
	}
	sidecarPath := agentPersonaRecoveryPath(personaPath)
	if _, err := os.Lstat(sidecarPath); err == nil {
		return "", fmt.Errorf("persona recovery obligation already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect persona recovery obligation: %w", err)
	}
	if err := writeAppFileAtomic(sidecarPath, encoded, 0o600); err != nil {
		return "", fmt.Errorf("write persona recovery obligation: %w", err)
	}
	return record.Token, nil
}

func readAgentPersonaRecovery(personaPath string) (agentPersonaRecoveryJournal, error) {
	sidecarPath := agentPersonaRecoveryPath(personaPath)
	before, err := os.Lstat(sidecarPath)
	if err != nil {
		return agentPersonaRecoveryJournal{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		(runtime.GOOS != "windows" && before.Mode().Perm()&0o077 != 0) {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("persona recovery obligation is not a regular file")
	}
	if before.Size() < 0 || before.Size() > maxPersonaBytes*2 {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("persona recovery obligation is oversized")
	}
	file, err := os.Open(sidecarPath)
	if err != nil {
		return agentPersonaRecoveryJournal{}, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("inspect opened persona recovery obligation: %w", err)
	}
	after, err := os.Lstat(sidecarPath)
	if err != nil {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("recheck persona recovery obligation: %w", err)
	}
	if !opened.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(after, opened) {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("persona recovery obligation changed while opening")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maxPersonaBytes*2)+1))
	if err != nil {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("read persona recovery obligation: %w", err)
	}
	if len(encoded) > maxPersonaBytes*2 {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("persona recovery obligation is oversized")
	}
	var record agentPersonaRecoveryRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("decode persona recovery obligation: %w", err)
	}
	binding, err := agentPersonaRecoveryBinding(personaPath)
	if err != nil {
		return agentPersonaRecoveryJournal{}, err
	}
	if !validAgentRecoveryToken(record.Token) ||
		(record.PreviousToken != "" && !validAgentRecoveryToken(record.PreviousToken)) ||
		record.Version != agentPersonaRecoveryVersion || record.Domain != agentPersonaRecoveryDomain ||
		len(record.Original) > maxPersonaBytes || record.PersonaBinding != binding ||
		record.OriginalHash != agentFramedSHA256(agentPersonaRecoveryDomain+"/original", record.Original) ||
		!validAgentRecoveryHash(record.ReplacementHash) {
		return agentPersonaRecoveryJournal{}, fmt.Errorf("persona recovery obligation is invalid")
	}
	return agentPersonaRecoveryJournal{record: record, info: opened}, nil
}

func (a *api) resolveAgentPersonaRecovery(personaPath string) error {
	journal, err := readAgentPersonaRecovery(personaPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", errAgentPersonaRecoveryPending, err)
	}
	if a.st == nil {
		return fmt.Errorf("%w: Store is unavailable", errAgentPersonaRecoveryPending)
	}
	record := journal.record
	storedToken, err := a.st.AgentPersonaRecoveryToken()
	if err != nil {
		return fmt.Errorf("%w: inspect Store token: %v", errAgentPersonaRecoveryPending, err)
	}
	current, err := os.ReadFile(personaPath)
	if err != nil {
		return fmt.Errorf("%w: read current persona: %v", errAgentPersonaRecoveryPending, err)
	}
	matchesOriginal := record.OriginalHash == agentFramedSHA256(
		agentPersonaRecoveryDomain+"/original",
		current,
	)
	matchesReplacement := record.ReplacementHash == agentFramedSHA256(
		agentPersonaRecoveryDomain+"/replacement",
		current,
	)

	finalDomain := agentPersonaRecoveryDomain + "/original"
	finalHash := record.OriginalHash
	switch storedToken {
	case record.Token:
		if !matchesReplacement {
			return fmt.Errorf("%w: committed persona bytes do not match recovery obligation", errAgentPersonaRecoveryPending)
		}
		finalDomain = agentPersonaRecoveryDomain + "/replacement"
		finalHash = record.ReplacementHash
	case record.PreviousToken:
		switch {
		case matchesOriginal:
			// The filesystem write never happened, or restoration completed before a
			// crash. Nothing should overwrite these already-authoritative bytes.
		case matchesReplacement:
			if err := restoreAgentFileAtomic(personaPath, record.Original, 0o600); err != nil {
				return fmt.Errorf("%w: restore original bytes: %v", errAgentPersonaRecoveryPending, err)
			}
		default:
			return fmt.Errorf("%w: uncommitted persona bytes do not match recovery obligation", errAgentPersonaRecoveryPending)
		}
	default:
		return fmt.Errorf("%w: stale persona recovery token lineage", errAgentPersonaRecoveryPending)
	}
	if ok, err := agentPersonaMatchesRecoveryHash(personaPath, finalDomain, finalHash); err != nil || !ok {
		return fmt.Errorf("%w: persona bytes changed before recovery finalization", errAgentPersonaRecoveryPending)
	}
	if err := removeAgentPersonaRecoveryJournal(personaPath, journal.info); err != nil {
		return fmt.Errorf("%w: clear recovery obligation: %v", errAgentPersonaRecoveryPending, err)
	}
	return nil
}

func agentPersonaRecoveryPending(personaPath string) bool {
	_, err := os.Lstat(agentPersonaRecoveryPath(personaPath))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func validAgentRecoveryToken(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func validAgentRecoveryHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func agentPersonaMatchesRecoveryHash(personaPath, domain, want string) (bool, error) {
	current, err := os.ReadFile(personaPath)
	if err != nil {
		return false, err
	}
	return agentFramedSHA256(domain, current) == want, nil
}

func removeAgentPersonaRecoveryJournal(personaPath string, expected os.FileInfo) error {
	sidecarPath := agentPersonaRecoveryPath(personaPath)
	current, err := os.Lstat(sidecarPath)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(current, expected) {
		return fmt.Errorf("persona recovery obligation changed before removal")
	}
	return os.Remove(sidecarPath)
}

func (a *api) writeAgentRollbackFailed(w http.ResponseWriter, cause error) {
	_ = cause
	if a.logger != nil {
		// The recovery journal can contain customer-authored prompt bytes and its
		// errors can contain an absolute installation path. Keep both out of logs.
		a.logger.Error("agent persona recovery pending")
	}
	a.writeLLMErr(w, http.StatusInternalServerError, "AGENT_ROLLBACK_FAILED",
		"Văn phong đang chờ khôi phục an toàn; chưa thể ghi thay đổi khác", nil)
}

func (a *api) rollbackAgentPersona(
	w http.ResponseWriter,
	personaPath string,
	storeErr error,
) {
	if err := a.resolveAgentPersonaRecovery(personaPath); err != nil {
		a.writeAgentRollbackFailed(w, fmt.Errorf("Store update failed: %v; recovery failed: %v", storeErr, err))
		return
	}
	a.writeOnboardingStoreError(w, storeErr)
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
	return analyzePersona(text).Placeholders
}

type personaAnalysis struct {
	Placeholders    []placeholder
	ValidationError string
}

func analyzePersona(text string) personaAnalysis {
	counts := map[string]int{}
	sample := map[string]string{}
	malformed := false
	matches := placeholderRe.FindAllStringSubmatchIndex(text, -1)
	var unmatched strings.Builder
	previousEnd := 0
	for _, match := range matches {
		unmatched.WriteString(text[previousEnd:match[0]])
		previousEnd = match[1]
		key := text[match[2]:match[3]]
		if strings.ContainsAny(key, "\r\n") {
			malformed = true
			continue
		}
		counts[key]++
		if _, ok := sample[key]; !ok {
			lineStart := strings.LastIndex(text[:match[0]], "\n") + 1
			lineEnd := strings.Index(text[match[1]:], "\n")
			if lineEnd < 0 {
				lineEnd = len(text)
			} else {
				lineEnd += match[1]
			}
			line := text[lineStart:lineEnd]
			sample[key] = clip(strings.TrimSpace(strings.TrimLeft(line, "-* \t")), 160)
		}
	}
	unmatched.WriteString(text[previousEnd:])
	if strings.Contains(unmatched.String(), "{{") || strings.Contains(unmatched.String(), "}}") {
		malformed = true
	}
	out := make([]placeholder, 0, len(counts))
	for k, n := range counts {
		out = append(out, placeholder{Key: k, Count: n, Sample: sample[k]})
	}
	// Thứ tự ổn định theo tên: một danh sách nhảy chỗ giữa hai lần tải làm người dùng mất chỗ.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	validationError := ""
	if malformed {
		validationError = "văn phong có dấu {{ }} lỗi hoặc đi qua nhiều dòng"
	}
	return personaAnalysis{Placeholders: out, ValidationError: validationError}
}

func personaValidationError(text string) string {
	return analyzePersona(text).ValidationError
}

func agentPersonaFingerprint(persona []byte, displayName string) string {
	return agentFramedSHA256("agentdc/agent-persona-fingerprint/v1", persona, []byte(displayName))
}

func agentFramedSHA256(domain string, fields ...[]byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(domain + "\x00"))
	for _, field := range fields {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(field)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
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
	analysis := analyzePersona(string(b))
	displayName := ""
	displayNameError := "chưa có tên hiển thị của bot"
	if a.st != nil {
		displayName, err = a.st.AgentDisplayName()
		if err != nil {
			a.logger.Error("agent: đọc tên hiển thị", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không đọc được tên bot")
			return
		}
	}
	if displayName != "" {
		normalized, nameErr := normalizeAgentNameValue("tên hiển thị", displayName)
		if nameErr == nil {
			displayName = normalized
			displayNameError = ""
		} else {
			displayNameError = "tên hiển thị của bot không hợp lệ"
		}
	}
	validationError := analysis.ValidationError
	if validationError == "" {
		validationError = displayNameError
	}
	if agentPersonaRecoveryPending(p) {
		validationError = "văn phong đang chờ khôi phục an toàn"
	}
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
		"placeholders": analysis.Placeholders,
		// ready là câu trả lời cho "mở cho khách thật được chưa". Một cờ, không một danh sách,
		// vì trang cần đổi màu theo nó.
		"ready":            len(analysis.Placeholders) == 0 && validationError == "",
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
	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()

	p, err := a.personaPath()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if err := a.resolveAgentPersonaRecovery(p); err != nil {
		a.writeAgentRollbackFailed(w, err)
		return
	}
	if displayName == "" && a.st != nil {
		displayName, err = a.st.AgentDisplayName()
		if err != nil {
			a.logger.Error("agent: đọc tên hiển thị trước khi lưu", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không đọc được tên bot")
			return
		}
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
	recoveryToken := ""
	if a.st != nil {
		recoveryToken, err = a.prepareAgentPersonaRecovery(p, b, []byte(text))
		if err != nil {
			a.logger.Error("agent: chuẩn bị khôi phục persona", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không chuẩn bị được bản khôi phục, chưa ghi gì")
			return
		}
	}
	if err := writeAgentFileAtomic(p, []byte(text), 0o600); err != nil {
		if recoveryToken != "" {
			if cleanupErr := a.resolveAgentPersonaRecovery(p); cleanupErr != nil {
				a.writeAgentRollbackFailed(w, cleanupErr)
				return
			}
		}
		a.logger.Error("agent: ghi persona", "path", p, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không ghi được văn phong")
		return
	}
	if a.st != nil {
		if _, err := a.st.UpdateAgentPersona(displayName, recoveryToken); err != nil {
			a.rollbackAgentPersona(w, p, err)
			return
		}
		if err := a.resolveAgentPersonaRecovery(p); err != nil {
			a.writeAgentRollbackFailed(w, err)
			return
		}
	}
	analysis := analyzePersona(text)
	_, displayNameErr := normalizeAgentNameValue("tên hiển thị", displayName)
	validationError := analysis.ValidationError
	if validationError == "" && displayNameErr != nil {
		validationError = "tên hiển thị của bot không hợp lệ"
	}
	a.zlog.add(ipc.ZaloLogInfo, "", fmt.Sprintf("văn phong: đã điền %d chỗ, còn %d", len(keys), len(analysis.Placeholders)))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"placeholders":     analysis.Placeholders,
		"ready":            len(analysis.Placeholders) == 0 && validationError == "",
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

	p, err := a.personaPath()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if err := a.resolveAgentPersonaRecovery(p); err != nil {
		a.writeAgentRollbackFailed(w, err)
		return
	}
	state, ok := a.onboardingMutationState(w, expectedRevision)
	if !ok {
		return
	}
	if state.Phase != store.OnboardingPhasePersona && state.Phase != store.OnboardingPhaseTest {
		a.writeOnboardingStoreError(w, fmt.Errorf("complete persona from %q: %w", state.Phase, store.ErrOnboardingInvalidPhase))
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
	analysis := analyzePersona(text)
	if len(analysis.Placeholders) != 0 || analysis.ValidationError != "" {
		fields := safeMissingPlaceholderFields(analysis.Placeholders)
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "AGENT_PLACEHOLDERS_REMAIN",
			"Văn phong vẫn còn chỗ trống hoặc dấu {{ chưa đóng", fields)
		return
	}

	finalBytes := []byte(text)
	changed := !bytes.Equal(original, finalBytes)
	recoveryToken := ""
	if changed {
		if err := writeAppBackupOnce(p+".goc", original, 0o600); err != nil {
			a.logger.Error("agent: ghi bản gốc persona", "path", p+".goc", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không tạo được bản lưu gốc, chưa ghi gì")
			return
		}
		recoveryToken, err = a.prepareAgentPersonaRecovery(p, original, finalBytes)
		if err != nil {
			a.logger.Error("agent: chuẩn bị khôi phục persona", "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không chuẩn bị được bản khôi phục, chưa ghi gì")
			return
		}
		if err := writeAgentFileAtomic(p, finalBytes, 0o600); err != nil {
			if cleanupErr := a.resolveAgentPersonaRecovery(p); cleanupErr != nil {
				a.writeAgentRollbackFailed(w, cleanupErr)
				return
			}
			a.logger.Error("agent: ghi persona hoàn chỉnh", "path", p, "err", err)
			a.writeErr(w, http.StatusInternalServerError, "không ghi được văn phong")
			return
		}
	}

	fingerprint := agentPersonaFingerprint(finalBytes, displayName)
	var updated store.OnboardingState
	if recoveryToken != "" {
		updated, err = a.st.AdvanceOnboardingPersonaWithRecovery(
			expectedRevision,
			fingerprint,
			displayName,
			recoveryToken,
		)
	} else {
		updated, err = a.st.AdvanceOnboardingPersona(expectedRevision, fingerprint, displayName)
	}
	if err != nil {
		if changed {
			a.rollbackAgentPersona(w, p, err)
			return
		}
		a.writeOnboardingStoreError(w, err)
		return
	}
	if recoveryToken != "" {
		if err := a.resolveAgentPersonaRecovery(p); err != nil {
			a.writeAgentRollbackFailed(w, err)
			return
		}
	}
	a.zlog.add(ipc.ZaloLogInfo, "", fmt.Sprintf("văn phong: đã điền %d chỗ, sẵn sàng kiểm tra", len(keys)))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"placeholders":        []placeholder{},
		"ready":               true,
		"display_name":        displayName,
		"onboarding_phase":    updated.Phase,
		"onboarding_revision": updated.Revision,
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
