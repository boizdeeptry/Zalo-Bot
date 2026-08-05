package daemon

// Knowledge: upload tệp nguồn rồi để agent biên soạn thành trang wiki.
//
// Tệp này chỉ được chèn vào bản đóng gói qua appmode/overlay. Bản upstream không nhận endpoint
// tải tệp hay agent có quyền ghi khi người vận hành chưa chủ động chọn App mode.
//
// Hai tầng của brain, và cả tính năng này chỉ để đi từ tầng một sang tầng hai:
//
//	raw\   tệp người mua bỏ vào. Dài, lẫn, không sửa.
//	wiki\  trang đã biên soạn, một chủ đề một trang. Bot trả lời từ đây.
//
// Upload ghi vào raw\. Ingest chạy `claude` với quyền ghi trong brain\ và để CLAUDE.md ở đó
// quyết định đặt tên trang thế nào — bản chỉ dẫn ấy đi kèm bộ xương, nên không phải bịa quy ước
// trong prompt này.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"agentdc/internal/agent"
	"agentdc/internal/ipc"

	"github.com/google/uuid"
)

// envBrainDir là thư mục brain\, cha của wiki\ và raw\.
//
// Tường minh chứ không suy ra từ AGENTDC_ZALO_KB_DIRS: suy ra cha chung của hai đường dẫn thì
// đúng với bố cục hiện tại và sai lặng lẽ với mọi bố cục khác. Chay.bat đặt biến này.
const envBrainDir = "AGENTDC_BRAIN_DIR"

// uploadExt là đuôi được nhận. DANH SÁCH CHO PHÉP: một đuôi mới xuất hiện thì mặc định là không.
//
// Không có .exe, .bat, .ps1, .zip, .js. Đây là thư mục agent sẽ đọc, nên một tệp chạy được ở đó
// không có lý do chính đáng nào, và nội dung một .zip thì không kiểm được.
var uploadExt = map[string]bool{
	".pdf": true, ".md": true, ".txt": true, ".csv": true,
	".docx": true, ".xlsx": true, ".pptx": true,
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
}

// maxUploadBytes chặn kích cỡ một tệp. Cả request bị chặn ở maxUploadTotal.
const (
	maxUploadBytes = 50 << 20
	maxUploadTotal = 64 << 20
)

// ingestTimeout: biên soạn nhiều tệp là việc dài. 20 phút là trần, không phải kỳ vọng.
const ingestTimeout = 20 * time.Minute

// ingestState theo dõi lượt biên soạn đang chạy.
//
// Biến mức gói, và đó là một giới hạn có chủ đích: MỘT lượt ingest tại một thời điểm. Đây là
// phần mềm một người dùng trên một máy, và hai lượt agent cùng ghi vào wiki\ sẽ tranh nhau tên
// tệp. Muốn nhiều lượt song song thì chỗ này phải thành một hàng đợi, không phải thêm một khoá.
var ingest = &ingestState{}

type ingestState struct {
	mu      sync.Mutex
	running bool
	started time.Time
	steps   []string
	err     string
	done    bool
	cancel  func()
}

func (s *ingestState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"running": s.running, "done": s.done, "err": s.err}
	out["steps"] = slices.Clone(s.steps)
	if !s.started.IsZero() {
		out["elapsed_sec"] = int(time.Since(s.started).Seconds())
	}
	return out
}

func (s *ingestState) step(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Giữ 200 dòng cuối: một lượt dài in rất nhiều, và trang chỉ hiện được vài chục dòng.
	s.steps = append(s.steps, line)
	if len(s.steps) > 200 {
		s.steps = s.steps[len(s.steps)-200:]
	}
}

func (s *ingestState) finish(errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running, s.done, s.err = false, true, errMsg
	s.cancel = nil
}

// brainDir đọc và kiểm thư mục brain.
func brainDir() (string, error) {
	d := strings.TrimSpace(os.Getenv(envBrainDir))
	if d == "" {
		return "", fmt.Errorf("chưa đặt %s trỏ vào thư mục brain", envBrainDir)
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("%s phải là đường dẫn tuyệt đối", envBrainDir)
	}
	st, err := os.Stat(d)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("không thấy thư mục %s", d)
	}
	return d, nil
}

// kbCount đếm tệp trong một thư mục con của brain, đệ quy, bỏ .gitkeep.
func kbCount(root string) (int, []string) {
	var n int
	var recent []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // một thư mục không đọc được không làm sai con số của phần còn lại
		}
		if strings.EqualFold(d.Name(), ".gitkeep") {
			return nil
		}
		n++
		if rel, e := filepath.Rel(root, p); e == nil {
			recent = append(recent, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(recent)
	if len(recent) > 50 {
		recent = recent[:50]
	}
	return n, recent
}

// ------------------------------------------------------------------- Models

// modelChoices là những mô hình cho chọn. Bí danh, KHÔNG id đầy đủ.
//
// `claude --model` nhận cả bí danh (`opus`, `sonnet`, `haiku`) và id đầy đủ. Bí danh luôn trỏ về
// bản mới nhất của dòng đó, nên một tệp cấu hình ghi "sonnet" không hết hạn khi Anthropic ra bản
// tiếp theo — còn ghi một id đầy đủ thì có, và nó sẽ hỏng lặng lẽ trên máy người mua.
var modelChoices = []string{"haiku", "sonnet", "opus"}

// modelFile là nơi lựa chọn được ghi: MỘT dòng, trong data\.
//
// Vì sao một tệp text chứ không bảng settings trong SQLite: mô hình được truyền cho claude bằng
// tham số dòng lệnh, và tham số ấy dựng từ zaloConfig.Model — thứ đọc từ biến môi trường ĐÚNG MỘT
// LẦN lúc daemon dựng. Ghi vào SQLite thì daemon đang chạy không thấy, và sửa để nó thấy là sửa
// vòng trực trong repo.
//
// Một tệp text thì Chay.bat đọc được bằng `set /p` trước khi khởi động daemon. Nên lựa chọn có
// hiệu lực từ lần mở phần mềm sau, và đường đi của nó nhìn thấy được từ đầu tới cuối. Đánh đổi
// đã biết và nói rõ trên trang: KHÔNG có hiệu lực ngay.
func modelFile(homeDir string) string { return filepath.Join(homeDir, "model.txt") }

func (a *api) handleKBModelGet(w http.ResponseWriter, _ *http.Request) {
	// Giá trị ĐANG CHẠY lấy từ cấu hình đã nạp, không từ tệp: hai thứ khác nhau đúng trong
	// khoảng giữa lúc bấm Lưu và lúc mở lại, và đó là khoảng người dùng cần thấy rõ nhất.
	active := ""
	if a.zalo != nil {
		active = a.zalo.cfg.Model
	}
	saved := ""
	if b, err := os.ReadFile(modelFile(a.cfg.Dir)); err == nil {
		saved = strings.TrimSpace(string(b))
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"active":  active,
		"saved":   saved,
		"choices": modelChoices,
	})
}

func (a *api) handleKBModelPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := readJSON(w, r, &req); err != nil {
		a.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m := strings.ToLower(strings.TrimSpace(req.Model))
	// Danh sách CHO PHÉP: giá trị này đi thẳng vào dòng lệnh của claude, nên một chuỗi tự do ở
	// đây là một tham số tự do ở đó.
	if !slices.Contains(modelChoices, m) {
		a.writeErr(w, http.StatusBadRequest, "mô hình không hợp lệ")
		return
	}
	if err := os.WriteFile(modelFile(a.cfg.Dir), []byte(m+"\r\n"), 0o600); err != nil {
		a.logger.Error("kb: ghi lựa chọn mô hình", "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không ghi được lựa chọn")
		return
	}
	a.zlog.add(ipc.ZaloLogInfo, "", "mô hình đặt thành "+m+" — đang tự mở lại phần mềm")
	// Áp dụng NGAY, không bắt người dùng tự đóng mở.
	//
	// Lỗi khi hẹn khởi động lại KHÔNG làm request thất bại: lựa chọn đã ghi xuống đĩa rồi, và nói
	// "không lưu được" lúc đó là nói sai. Trang nhận restarting=false và tự nói ra việc phải làm
	// bằng tay — đó là đường rơi, không phải đường chính.
	restarting := true
	reason := ""
	if err := a.scheduleRestart(); err != nil {
		a.logger.Error("kb: hẹn khởi động lại", "err", err)
		restarting, reason = false, err.Error()
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"saved": m, "restarting": restarting, "reason": reason,
	})
}

// handleKBList: trạng thái hai tầng, đủ để trang vẽ.
func (a *api) handleKBList(w http.ResponseWriter, _ *http.Request) {
	dir, err := brainDir()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	rawN, rawFiles := kbCount(filepath.Join(dir, "raw"))
	wikiN, wikiFiles := kbCount(filepath.Join(dir, "wiki"))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"brain":      dir,
		"raw_count":  rawN,
		"raw_files":  rawFiles,
		"wiki_count": wikiN,
		"wiki_files": wikiFiles,
		"ingest":     ingest.snapshot(),
	})
}

// handleKBUpload nhận tệp vào raw\.
//
// Tên tệp là thứ nguy hiểm duy nhất ở đây, và nó bị TỪ CHỐI chứ không làm sạch: làm sạch một
// đường dẫn là một hàm phải đúng mọi lần trên mọi nền tảng, còn từ chối thì đúng theo cấu trúc.
// Dùng lại safeSendName của zalosend.go — cùng câu hỏi, cùng câu trả lời.
func (a *api) handleKBUpload(w http.ResponseWriter, r *http.Request) {
	dir, err := brainDir()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	rawDir := filepath.Join(dir, "raw")
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		a.logger.Error("kb: tạo thư mục raw", "dir", rawDir, "err", err)
		a.writeErr(w, http.StatusInternalServerError, "không tạo được thư mục raw")
		return
	}
	if r.ContentLength > maxUploadTotal {
		a.writeErr(w, http.StatusRequestEntityTooLarge, "dữ liệu tải lên vượt quá giới hạn 64 MiB")
		return
	}
	// Chặn ở tầng body TRƯỚC khi phân tích: ParseMultipartForm với một body khổng lồ sẽ đọc hết
	// vào đĩa tạm rồi mới báo lỗi.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.writeErr(w, http.StatusRequestEntityTooLarge, "dữ liệu tải lên vượt quá giới hạn 64 MiB")
			return
		}
		a.writeErr(w, http.StatusBadRequest, "không đọc được dữ liệu tải lên: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		a.writeErr(w, http.StatusBadRequest, "không có tệp nào")
		return
	}
	var saved, skipped []string
	duplicateCount := 0
	for _, fh := range files {
		name := filepath.Base(fh.Filename)
		if !safeKBUploadName(name) {
			skipped = append(skipped, fh.Filename+" (tên tệp không hợp lệ)")
			continue
		}
		if !uploadExt[strings.ToLower(filepath.Ext(name))] {
			skipped = append(skipped, name+" (đuôi không được nhận)")
			continue
		}
		if fh.Size > maxUploadBytes {
			skipped = append(skipped, name+" (quá lớn)")
			continue
		}
		if err := saveMultipart(fh, filepath.Join(rawDir, name)); err != nil {
			if os.IsExist(err) {
				duplicateCount++
				skipped = append(skipped, name+" (đã có trong raw\\)")
				continue
			}
			a.logger.Error("kb: ghi tệp tải lên", "name", name, "err", err)
			skipped = append(skipped, name+" (không ghi được)")
			continue
		}
		saved = append(saved, name)
		a.zlog.add(ipc.ZaloLogInfo, "", "knowledge: nhận "+name)
	}
	code := http.StatusOK
	if len(saved) == 0 {
		if duplicateCount == len(files) {
			code = http.StatusConflict
		} else {
			code = http.StatusBadRequest
		}
	}
	a.writeJSON(w, code, map[string]any{"saved": saved, "skipped": skipped})
}

func safeKBUploadName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || !safeSendName(name) {
		return false
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.ContainsAny(name, `<>:"/\|?*`) {
		return false
	}
	for _, char := range name {
		if char < 0x20 {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return false
	}
	return true
}

// saveMultipart ghi một tệp tải lên.
//
// O_EXCL, không O_TRUNC: raw\ là tầng KHÔNG sửa, và một tệp trùng tên có thể là một phiên bản
// khác của cùng tài liệu. Đè lặng lẽ thì người mua mất bản gốc mà không có gì báo. Trả về lỗi
// os.IsExist để người gọi nói ra "đã có" thay vì "không ghi được".
//
// io.LimitReader thêm một lần nữa dù fh.Size đã kiểm: Size là thứ client KHAI, còn cái này là
// thứ thật sự được ghi.
func saveMultipart(fh *multipart.FileHeader, dst string) error {
	src, err := fh.Open()
	if err != nil {
		return fmt.Errorf("mở tệp tải lên: %w", err)
	}
	defer func() { _ = src.Close() }()

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(src, maxUploadBytes)); err != nil {
		_ = f.Close()
		// Dọn tệp dở: một tệp cắt giữa trong raw\ sẽ được agent đọc như một nguồn thật.
		_ = os.Remove(dst)
		return fmt.Errorf("ghi %s: %w", dst, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("đóng %s: %w", dst, err)
	}
	return nil
}

// handleKBIngest chạy một lượt biên soạn.
//
// Đây là endpoint TỐN TIỀN THẬT: nó gọi model. Nên nó chỉ chạy khi người dùng bấm, và chỉ một
// lượt tại một thời điểm.
func (a *api) handleKBIngest(w http.ResponseWriter, _ *http.Request) {
	dir, err := brainDir()
	if err != nil {
		a.writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if n, _ := kbCount(filepath.Join(dir, "raw")); n == 0 {
		a.writeErr(w, http.StatusPreconditionFailed,
			"thư mục raw\\ đang rỗng: tải tệp nguồn lên trước khi biên soạn")
		return
	}
	ingest.mu.Lock()
	if ingest.running {
		ingest.mu.Unlock()
		a.writeErr(w, http.StatusConflict, "đang có một lượt biên soạn chạy")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ingestTimeout)
	ingest.running, ingest.done, ingest.err, ingest.steps = true, false, "", nil
	ingest.started, ingest.cancel = time.Now(), cancel
	ingest.mu.Unlock()

	go a.runIngest(ctx, cancel, dir)
	a.writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// handleKBIngestStop huỷ lượt đang chạy.
func (a *api) handleKBIngestStop(w http.ResponseWriter, _ *http.Request) {
	ingest.mu.Lock()
	c := ingest.cancel
	ingest.mu.Unlock()
	if c == nil {
		a.writeErr(w, http.StatusConflict, "không có lượt nào đang chạy")
		return
	}
	c()
	w.WriteHeader(http.StatusNoContent)
}

// ingestPrompt: nói VIỆC, không nói cách đặt tên.
//
// CLAUDE.md trong brain\ đã là schema — nó nói trang gồm gì, đặt tên thế nào, cập nhật index.md
// và log.md ra sao. Nhắc lại ở đây sẽ tạo ra hai nguồn luật, và khi người mua sửa CLAUDE.md thì
// prompt này sẽ ghi đè ý họ mà không ai thấy.
const ingestPrompt = `Đọc CLAUDE.md trong thư mục hiện tại TRƯỚC, rồi làm theo schema trong đó.

Việc: với mỗi tệp trong raw/ chưa có trang tương ứng trong wiki/, đọc nó và viết các trang wiki
theo đúng schema của CLAUDE.md. Cập nhật index.md và log.md như CLAUDE.md yêu cầu.

Không sửa và không xoá bất cứ gì trong raw/. Đó là tầng nguồn.
Tệp nào đã có trang rồi thì bỏ qua, đừng viết lại.
Xong thì nói ngắn gọn: đã viết những trang nào, bỏ qua tệp nào và vì sao.`

func (a *api) runIngest(ctx context.Context, cancel func(), dir string) {
	defer cancel()
	bin, err := exec.LookPath("claude")
	if err != nil {
		ingest.step("không thấy claude trên PATH")
		ingest.finish("chưa cài Claude Code, hoặc chưa đăng nhập. Xem DOC TRUOC.txt")
		return
	}
	// Quyền GHI, và chỉ trong brain\. Khác hẳn profile của bot trả lời khách, thứ chỉ-đọc.
	prof := agent.ConsultReadOnly()
	args, err := prof.HeadlessArgv(uuid.NewString())
	if err != nil {
		ingest.finish(err.Error())
		return
	}
	// Thay allowed-tools: HeadlessArgv trả về bộ chỉ-đọc, còn biên soạn thì phải ghi được.
	args = replaceAllowedTools(args, "Read", "Grep", "Glob", "Write", "Edit")
	args = append(args, "--add-dir", dir)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = prof.Env(os.Environ())
	cmd.Stdin = strings.NewReader(ingestPrompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		ingest.finish("không mở được stdout của claude")
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		ingest.finish("không chạy được claude: " + err.Error())
		return
	}
	ingest.step("agent bắt đầu đọc raw/")
	tail, resultMsg := streamIngestSteps(stdout, ingest.step)
	if err := cmd.Wait(); err != nil {
		// Thứ tự có chủ đích: `result` của claude TRƯỚC stderr trước err.Error().
		//
		// err.Error() là "exit status 1", đúng và vô dụng. stderr thường rỗng vì claude nói lỗi
		// qua stream-json. `result` là chỗ duy nhất có câu người đọc được, và đo được nó nói
		// đúng vấn đề: "API Error: 529 Overloaded ... try again in a moment".
		msg := resultMsg
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() != nil {
			msg = "đã huỷ"
		}
		// Đuôi thô vào các bước, không vào msg: msg hiện trên một dòng của trang, còn đuôi dài
		// và nhiều dòng. Không có nó thì "exit status 1" là toàn bộ những gì người dùng biết.
		for _, l := range tail {
			ingest.step("stdout: " + l)
		}
		ingest.step("kết thúc với lỗi: " + msg)
		ingest.finish(msg)
		return
	}
	ingest.step("xong")
	ingest.finish("")
}

// replaceAllowedTools đổi giá trị của --allowed-tools tại chỗ.
//
// Sửa mảng args thay vì dựng một profile mới: profile nằm trong internal/agent, tức trong repo,
// và tệp này cố ý không sửa gì trong repo.
func replaceAllowedTools(args []string, tools ...string) []string {
	out := make([]string, 0, len(args)+len(tools))
	for i := 0; i < len(args); i++ {
		if args[i] == "--allowed-tools" {
			out = append(out, "--allowed-tools")
			out = append(out, tools...)
			// Bỏ mọi giá trị cũ tới cờ tiếp theo.
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// streamIngestSteps đọc stream-json của claude theo DÒNG, đẩy từng bước ra, và trả về đuôi thô.
//
// Đọc theo dòng chứ không cmd.Output(): một lượt biên soạn chạy nhiều phút, và không có dòng nào
// thì trang chỉ hiện được "đang chạy" suốt thời gian đó.
//
// Vì sao bufio.Scanner + Unmarshal từng dòng chứ không json.Decoder: stream-json là JSON PHÂN
// DÒNG, và Decoder gặp một dòng không phải JSON (một cảnh báo của node, một dòng log) sẽ lỗi rồi
// dừng đọc — lúc đó pipe không ai rút, claude ghi vào pipe đầy và treo. Scanner bỏ qua dòng xấu
// và đọc tiếp.
//
// Và nó trả về ĐUÔI THÔ: bản đầu chỉ đẩy bước ra rồi bỏ phần còn lại, nên khi lượt thất bại với
// "exit status 1" thì không có gì để biết vì sao. Đo được: một lượt chạy 228 giây rồi thất bại,
// và toàn bộ bằng chứng đã bị nuốt.
func streamIngestSteps(r io.Reader, step func(string)) (tailOut []string, resultMsg string) {
	// Dòng stream-json mang cả nội dung tệp nên nó rất dài; Scanner mặc định vỡ ở 64 KB.
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var tail []string
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		tail = append(tail, clip(line, 400))
		if len(tail) > 15 {
			tail = tail[len(tail)-15:]
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		// Giữ lại `result`: khi lượt thất bại, ĐÂY là câu giải thích được, còn stderr thì rỗng.
		// Đo được: một lượt trượt vì API quá tải chỉ báo "exit status 1" cho người dùng, trong
		// khi result nói đúng "API Error: 529 Overloaded ... try again in a moment".
		if ev["type"] == "result" {
			if s, ok := ev["result"].(string); ok && strings.TrimSpace(s) != "" {
				resultMsg = strings.TrimSpace(s)
			}
		}
		if s := describeIngestEvent(ev); s != "" {
			step(s)
		}
	}
	return tail, resultMsg
}

// describeIngestEvent biến một event thành một dòng người đọc được, hoặc "" nếu không đáng in.
func describeIngestEvent(ev map[string]any) string {
	// api_retry: PHẢI in. Đo được: một lượt gặp API 529 thử lại 10 lần trong 240 giây, và suốt
	// thời gian đó trang chỉ hiện "agent bắt đầu đọc raw/" — người dùng không có cách nào biết
	// nó đang chờ máy chủ chứ không phải đang làm việc.
	if ev["subtype"] == "api_retry" {
		att, _ := ev["attempt"].(float64)
		max, _ := ev["max_retries"].(float64)
		reason, _ := ev["error"].(string)
		if reason == "" {
			reason = "lỗi máy chủ"
		}
		return fmt.Sprintf("đang thử lại lần %d/%d (%s)", int(att), int(max), reason)
	}
	switch ev["type"] {
	case "assistant":
		msg, _ := ev["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		for _, c := range content {
			b, _ := c.(map[string]any)
			if b["type"] == "tool_use" {
				name, _ := b["name"].(string)
				in, _ := b["input"].(map[string]any)
				if p, ok := in["file_path"].(string); ok {
					return name + ": " + filepath.Base(p)
				}
				return name
			}
		}
	case "result":
		if s, ok := ev["result"].(string); ok && s != "" {
			return "kết quả: " + clip(strings.ReplaceAll(s, "\n", " "), 300)
		}
	}
	return ""
}
