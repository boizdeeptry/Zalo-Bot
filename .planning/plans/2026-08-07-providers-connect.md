# Connect trong Portal (zero-terminal) — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Biến nút "Thêm kết nối" (disabled từ UI#1) của provider subscription thành luồng đăng nhập thật trong Portal: detect → install (nếu thiếu) → login (OAuth) → poll → Connected. Người mua không mở terminal.

**TDD mode:** yes

**Architecture:** Một `connectManager` giữ MỘT `connectJob` có trạng thái (state machine) trong daemon; Portal poll qua 3 endpoint `POST/GET/DELETE /llm/providers/{kind}/connect`. Ba bước có tác dụng phụ (install/login/poll) đi qua một seam `connectRunner` — mặc định chạy CLI thật, test tiêm giả nên toàn bộ state machine test được không spawn CLI. Cancel/timeout giết cả cây bằng `killPidTree` của engine.

**Tech stack:** Go (`os/exec`, `sync`, `context`, `log/slog`), engine primitives `runCLIProcess`/`killPidTree`/`resolveCLIProgram`/`checkCLIAuth`/`cliDescriptors` (đã có trong `app_llm_cli.go`); SQLite qua `store`; ES module Portal (`element`/`requestJSON` + poll qua `AbortController`); `node --test`.

**Spec:** `.planning/specs/2026-08-07-providers-connect-design.md`

**Research:** skipped — dựng trên khuôn có sẵn (handler daemon: `decodeLLMBody`/`PathValue`/`writeJSON`/`writeLLMProviderErr`; seam `run` của engine; UI poll của Portal). Không thư viện mới.

**Sub-project:** #2/4. Subscription CLI only (Claude Code, OpenAI Codex). Lệnh install/login THẬT ghim **capture-first** ở Task 5 (checkpoint NEEDS-LOGIN như engine Task 8).

---

## File structure

- **Mới** `appmode/overlay/internal/daemon/app_llm_connect.go` — `connectRunner` (interface), `connectState`/`connectPhase`, `connectJob`, `connectManager`, default runner, 3 handler. Một trách nhiệm: điều phối luồng connect.
- **Mới** `appmode/overlay/internal/daemon/app_llm_connect_test.go` — test state machine qua fake runner + handler qua httptest.
- **Sửa** `appmode/overlay/internal/daemon/app_routes.go` — allowlist + đăng ký 3 route; khởi tạo `connectManager` trong `api`.
- **Sửa** `appmode/overlay/internal/store/app_llm.go` (+ `app_llm_test.go`) — `EnsureProviderForKind`.
- **Sửa** `appmode/overlay/internal/webui/static/pages/providers.js` (+ `appmode/tests/providers.test.mjs`) — panel Connect + poll + cancel.
- **Sửa** `appmode/overlay/internal/webui/static/portal.css` — style panel connect (scoped `.providers-page`).
- **Sửa** `scripts/BuildApp.psm1` (+ `tests/build-app.Tests.ps1`) — bundle npm vào `app\node`; cập nhật `Assert-AppPackage`.

### Kiểu dùng chung (khai trong app_llm_connect.go — Task 2 tạo)

```go
type connectPhase string

const (
	phaseDetecting     connectPhase = "detecting"
	phaseInstalling    connectPhase = "installing"
	phaseAwaitingLogin connectPhase = "awaiting_login"
	phasePolling       connectPhase = "polling"
	phaseConnected     connectPhase = "connected"
	phaseError         connectPhase = "error"
	phaseCanceled      connectPhase = "canceled"
)

type connectState struct {
	Kind     string       `json:"kind"`
	Phase    connectPhase `json:"phase"`
	Message  string       `json:"message,omitempty"`
	LoginURL string       `json:"loginUrl,omitempty"`
	Error    string       `json:"error,omitempty"`
}

// connectRunner tách ba bước tác dụng-phụ khỏi state machine để test không spawn CLI.
type connectRunner interface {
	detect(kind string) (installed bool, err error)
	install(ctx context.Context, kind string, onLine func(string)) error
	// login spawn `<cli> login`; trả URL OAuth (nếu bắt được) + hàm wait chặn tới khi tiến trình
	// login kết thúc. Huỷ = ctx bị cancel (state machine lo killPidTree qua ctx của runCLIProcess).
	login(ctx context.Context, kind string) (loginURL string, wait func() error, err error)
	pollAuth(kind string) authState
}

// subscriptionKinds: chỉ hai kind này được connect (login OAuth). Kind khác → 400.
var subscriptionKinds = map[string]bool{"claude-code": true, "codex": true}
```

---

### Task 1: `EnsureProviderForKind` — tạo provider row cho kind subscription nếu chưa có

**Files:**
- Modify: `appmode/overlay/internal/store/app_llm.go`
- Test: `appmode/overlay/internal/store/app_llm_test.go`

**Public behavior to verify:** Gọi `EnsureProviderForKind("codex")` trên store trống tạo đúng một provider `kind=codex` (enabled, không credential); gọi lại là no-op (idempotent); không đụng `claude-code` đã seed.

- [ ] **Step 1: Viết test đỏ**

  ```go
  func TestEnsureProviderForKind(t *testing.T) {
      st := newLLMStore(t)
      // Tạo lần đầu.
      if err := st.EnsureProviderForKind("codex"); err != nil {
          t.Fatalf("EnsureProviderForKind(codex) = %v; want nil", err)
      }
      providers, err := st.LLMProviders()
      if err != nil { t.Fatal(err) }
      got := 0
      for _, p := range providers {
          if p.Kind == "codex" { got++; if p.System { t.Errorf("codex provider không được là system") } }
      }
      if got != 1 { t.Fatalf("có %d provider kind=codex; want 1", got) }
      // Idempotent: gọi lại không tạo thêm.
      if err := st.EnsureProviderForKind("codex"); err != nil { t.Fatal(err) }
      providers, _ = st.LLMProviders()
      again := 0
      for _, p := range providers { if p.Kind == "codex" { again++ } }
      if again != 1 { t.Errorf("sau lần gọi thứ hai có %d codex; want 1 (idempotent)", again) }
  }
  ```

  (Dùng helper `newLLMStore(t)` sẵn có trong `app_llm_test.go`; nếu tên khác thì theo tên thật.)

- [ ] **Step 2: Chạy đỏ** — `pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run TestEnsureProviderForKind` → FAIL: chưa có method.

- [ ] **Step 3: Cài đặt** — thêm vào `store/app_llm.go`. Tên hiển thị lấy từ một map nhỏ; enabled=true; đi qua `CreateLLMProvider` (đã hardcode system=0, credential rỗng — đúng cho subscription):

  ```go
  var subscriptionDisplayName = map[string]string{"claude-code": "Claude Code", "codex": "OpenAI Codex"}

  // EnsureProviderForKind bảo đảm có MỘT provider cho kind subscription. Idempotent: đã có →
  // no-op. Provider subscription không mang credential (CLI tự giữ phiên), nên chỉ cần row để
  // gallery/chuỗi tham chiếu. id = kind (một tài khoản ở #2; #3 multi-account đổi cách đánh id).
  func (s *Store) EnsureProviderForKind(kind string) error {
      name, ok := subscriptionDisplayName[kind]
      if !ok {
          return fmt.Errorf("ensure provider: kind không phải subscription: %q", kind)
      }
      providers, err := s.LLMProviders()
      if err != nil {
          return err
      }
      for _, p := range providers {
          if p.Kind == kind {
              return nil // đã có
          }
      }
      return s.CreateLLMProvider(LLMProvider{ID: kind, Name: name, Kind: kind, Enabled: true})
  }
  ```

- [ ] **Step 4: Chạy xanh.**
- [ ] **Step 5: Refactor** — không.
- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/store/app_llm_test.go
  git commit -m "feat: EnsureProviderForKind creates a subscription provider row idempotently"
  ```

---

### Task 2: State machine — happy path (detect-đã-cài → login → poll → connected) qua fake runner

**Files:**
- Create: `appmode/overlay/internal/daemon/app_llm_connect.go`
- Test: `appmode/overlay/internal/daemon/app_llm_connect_test.go`

**Public behavior to verify:** `connectManager.start("codex")` với runner giả (đã-cài, login trả URL, poll trả loggedIn) chạy qua `detecting → awaiting_login → polling → connected`, gọi `ensure(kind)` đúng một lần, và `status()` phản ánh phase cuối `connected` + `loginUrl`.

- [ ] **Step 1: Viết test đỏ**

  ```go
  package daemon

  import (
      "context"
      "log/slog"
      "sync"
      "testing"
      "time"
  )

  // fakeRunner điều khiển từng bước; ghi lại lời gọi.
  type fakeRunner struct {
      installed  bool
      loginURL   string
      auth       authState
      installErr error
      loginErr   error
      installed_ int
      ensured    int
  }
  func (f *fakeRunner) detect(string) (bool, error) { return f.installed, nil }
  func (f *fakeRunner) install(ctx context.Context, _ string, onLine func(string)) error {
      f.installed_++; if onLine != nil { onLine("cài…") }; return f.installErr
  }
  func (f *fakeRunner) login(ctx context.Context, _ string) (string, func() error, error) {
      if f.loginErr != nil { return "", nil, f.loginErr }
      return f.loginURL, func() error { <-ctx.Done(); return ctx.Err() }, nil
  }
  func (f *fakeRunner) pollAuth(string) authState { return f.auth }

  func newTestManager(r connectRunner, ensure func(string) error) *connectManager {
      return &connectManager{runner: r, ensure: ensure, logger: slog.New(slog.DiscardHandler)}
  }
  // waitPhase poll status tới khi đạt phase mong muốn hoặc hết giờ (state machine chạy nền).
  func waitPhase(t *testing.T, m *connectManager, kind string, want connectPhase) connectState {
      t.Helper()
      deadline := time.Now().Add(3 * time.Second)
      for time.Now().Before(deadline) {
          st, _ := m.status(kind)
          if st.Phase == want || st.Phase == phaseError { return st }
          time.Sleep(10 * time.Millisecond)
      }
      t.Fatalf("không đạt phase %q trong 3s", want); return connectState{}
  }

  func TestConnectHappyPathAlreadyInstalled(t *testing.T) {
      var ensured int
      var mu sync.Mutex
      r := &fakeRunner{installed: true, loginURL: "https://auth.example/login", auth: authLoggedIn}
      m := newTestManager(r, func(string) error { mu.Lock(); ensured++; mu.Unlock(); return nil })
      if _, err := m.start("codex"); err != nil { t.Fatalf("start = %v; want nil", err) }
      st := waitPhase(t, m, "codex", phaseConnected)
      if st.Phase != phaseConnected { t.Fatalf("phase = %q; want connected (err=%q)", st.Phase, st.Error) }
      if st.LoginURL != "https://auth.example/login" { t.Errorf("loginUrl = %q", st.LoginURL) }
      if r.installed_ != 0 { t.Errorf("đã-cài nhưng vẫn gọi install %d lần", r.installed_) }
      mu.Lock(); defer mu.Unlock()
      if ensured != 1 { t.Errorf("ensure gọi %d lần; want 1", ensured) }
  }
  ```

- [ ] **Step 2: Chạy đỏ** — `go-check -Run TestConnectHappyPathAlreadyInstalled` → FAIL: `connectManager` chưa có.

- [ ] **Step 3: Cài đặt** — tạo `app_llm_connect.go` với kiểu dùng chung (mục "Kiểu dùng chung" ở trên) + `connectJob`/`connectManager` chạy state machine nền:

  ```go
  type connectJob struct {
      mu     sync.Mutex
      state  connectState
      cancel context.CancelFunc
  }
  func (j *connectJob) set(mut func(*connectState)) {
      j.mu.Lock(); defer j.mu.Unlock(); mut(&j.state)
  }
  func (j *connectJob) snapshot() connectState { j.mu.Lock(); defer j.mu.Unlock(); return j.state }

  type connectManager struct {
      mu      sync.Mutex
      runner  connectRunner
      ensure  func(kind string) error
      logger  *slog.Logger
      job     *connectJob
      jobKind string
  }

  const connectLoginTimeout = 5 * time.Minute
  const connectPollInterval = 2 * time.Second

  // start khởi động job cho kind; một job tại một thời điểm. Đang chạy cùng kind → trả trạng thái
  // hiện tại (không lỗi). Đang chạy KIND KHÁC → lỗi ErrConnectBusy (handler map thành 409).
  var errConnectBusy = errors.New("connect: đang có phiên kết nối khác")

  func (m *connectManager) start(kind string) (connectState, error) {
      m.mu.Lock()
      if m.job != nil {
          cur := m.job.snapshot()
          busy := m.jobKind != kind && cur.Phase != phaseConnected && cur.Phase != phaseError && cur.Phase != phaseCanceled
          m.mu.Unlock()
          if busy { return connectState{}, errConnectBusy }
          if m.jobKind == kind { return cur, nil }
      }
      ctx, cancel := context.WithCancel(context.Background())
      job := &connectJob{state: connectState{Kind: kind, Phase: phaseDetecting}, cancel: cancel}
      m.job = job
      m.jobKind = kind
      m.mu.Unlock()
      go m.run(ctx, kind, job)
      return job.snapshot(), nil
  }

  func (m *connectManager) status(kind string) (connectState, bool) {
      m.mu.Lock(); defer m.mu.Unlock()
      if m.job == nil || m.jobKind != kind { return connectState{}, false }
      return m.job.snapshot(), true
  }

  func (m *connectManager) cancel(kind string) bool {
      m.mu.Lock(); job := m.job; k := m.jobKind; m.mu.Unlock()
      if job == nil || k != kind { return false }
      job.cancel()
      job.set(func(s *connectState) { if s.Phase != phaseConnected { s.Phase = phaseCanceled } })
      return true
  }

  func (m *connectManager) fail(job *connectJob, msg string) {
      job.set(func(s *connectState) { s.Phase = phaseError; s.Error = msg })
  }

  func (m *connectManager) run(ctx context.Context, kind string, job *connectJob) {
      installed, err := m.runner.detect(kind)
      if err != nil { m.fail(job, "không kiểm được CLI: "+err.Error()); return }
      if !installed {
          job.set(func(s *connectState) { s.Phase = phaseInstalling; s.Message = "Đang cài…" })
          if err := m.runner.install(ctx, kind, func(line string) {
              job.set(func(s *connectState) { s.Message = line })
          }); err != nil {
              m.fail(job, "cài thất bại"); return
          }
      }
      job.set(func(s *connectState) { s.Phase = phaseAwaitingLogin })
      loginURL, wait, err := m.runner.login(ctx, kind)
      if err != nil { m.fail(job, "không mở được đăng nhập"); return }
      if loginURL != "" { job.set(func(s *connectState) { s.LoginURL = loginURL }) }
      // poll auth song song trong khi login đang chờ OAuth; dừng khi loggedIn, timeout, hoặc cancel.
      loginCtx, stop := context.WithTimeout(ctx, connectLoginTimeout)
      defer stop()
      go func() { _ = wait() }() // wait thoát khi loginCtx/ctx done (runner killPidTree ở default impl)
      job.set(func(s *connectState) { s.Phase = phasePolling })
      tick := time.NewTicker(connectPollInterval); defer tick.Stop()
      for {
          if m.runner.pollAuth(kind) == authLoggedIn {
              if err := m.ensure(kind); err != nil { m.fail(job, "lưu provider lỗi: "+err.Error()); return }
              job.set(func(s *connectState) { s.Phase = phaseConnected; s.Message = "" })
              return
          }
          select {
          case <-loginCtx.Done():
              if ctx.Err() != nil { job.set(func(s *connectState) { s.Phase = phaseCanceled }); return }
              m.fail(job, "hết giờ đăng nhập"); return
          case <-tick.C:
          }
      }
  }
  ```

  Imports: `context errors log/slog sync time`.

- [ ] **Step 4: Chạy xanh.**
- [ ] **Step 5: Refactor** — `set`/`snapshot` giữ mọi truy cập state sau mutex (an toàn -race). Chạy `go-check -Run TestConnect` với `-race` nếu go-check bật race.
- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_connect.go appmode/overlay/internal/daemon/app_llm_connect_test.go
  git commit -m "feat: connect job state machine (happy path) with a test seam"
  ```

---

### Task 3: State machine — install branch, lỗi/timeout, cancel-giết-cây, một-job

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go` (nếu cần chỉnh nhỏ)
- Test: `appmode/overlay/internal/daemon/app_llm_connect_test.go`

**Public behavior to verify:** detect-thiếu → chạy install rồi mới login; install lỗi → `error`; login timeout → `error`; cancel giữa chừng → `canceled` và `wait` thoát; job thứ hai kind khác khi đang chạy → `errConnectBusy`.

- [ ] **Step 1: Viết test đỏ**

  ```go
  func TestConnectInstallsWhenMissing(t *testing.T) {
      r := &fakeRunner{installed: false, auth: authLoggedIn}
      m := newTestManager(r, func(string) error { return nil })
      m.start("codex")
      st := waitPhase(t, m, "codex", phaseConnected)
      if st.Phase != phaseConnected { t.Fatalf("phase=%q err=%q", st.Phase, st.Error) }
      if r.installed_ != 1 { t.Errorf("install gọi %d lần; want 1 (đang thiếu)", r.installed_) }
  }

  func TestConnectInstallFailureIsError(t *testing.T) {
      r := &fakeRunner{installed: false, installErr: errors.New("mạng hỏng")}
      m := newTestManager(r, func(string) error { return nil })
      m.start("codex")
      st := waitPhase(t, m, "codex", phaseError)
      if st.Phase != phaseError { t.Fatalf("phase=%q; want error", st.Phase) }
  }

  func TestConnectCancelStopsLoginAndPolls(t *testing.T) {
      // login chờ ctx; auth không bao giờ loggedIn → phải poll tới khi cancel.
      r := &fakeRunner{installed: true, auth: authLoggedOut, loginURL: "https://x"}
      m := newTestManager(r, func(string) error { return nil })
      m.start("codex")
      waitPhase(t, m, "codex", phasePolling)
      if !m.cancel("codex") { t.Fatal("cancel trả false") }
      st, _ := m.status("codex")
      if st.Phase != phaseCanceled { t.Errorf("phase=%q; want canceled", st.Phase) }
  }

  func TestConnectSecondKindWhileBusyIsRejected(t *testing.T) {
      r := &fakeRunner{installed: true, auth: authLoggedOut, loginURL: "https://x"}
      m := newTestManager(r, func(string) error { return nil })
      m.start("codex")
      waitPhase(t, m, "codex", phasePolling)
      if _, err := m.start("claude-code"); !errors.Is(err, errConnectBusy) {
          t.Errorf("start(claude-code) khi bận = %v; want errConnectBusy", err)
      }
  }
  ```

- [ ] **Step 2: Chạy đỏ** — chạy; nếu Task 2 đã đúng thì phần lớn có thể XANH ngay. Ca nào đỏ thì đó là hành vi còn thiếu — sửa ở Step 3.
- [ ] **Step 3: Cài đặt** — chỉ chỉnh nếu một ca đỏ (ví dụ: bảo đảm nhánh `!installed` gọi install trước login; `cancel` set `canceled` cả khi đang `polling`; `errConnectBusy` đúng điều kiện). Logic Task 2 nên đã bao phủ; đây là ghim biên.
- [ ] **Step 4: Chạy xanh** (4 test).
- [ ] **Step 5: Refactor** — none.
- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_connect.go appmode/overlay/internal/daemon/app_llm_connect_test.go
  git commit -m "feat: connect job install/error/timeout/cancel/one-job invariants"
  ```

---

### Task 4: 3 endpoint HTTP + đăng ký route + validate kind

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go` (handlers), `app_routes.go` (allowlist + đăng ký + khởi tạo manager)
- Test: `appmode/overlay/internal/daemon/app_llm_connect_test.go`

**Public behavior to verify:** `POST /llm/providers/codex/connect` khởi động job và trả JSON trạng thái; `GET` trả trạng thái hiện tại; `DELETE` huỷ; kind không-subscription → 400; job khác đang chạy → 409.

- [ ] **Step 1: Viết test đỏ** (dùng `newAppRouteAPI(t)` — có sẵn ở router test — nhưng test này ở package daemon nên dựng `api` với manager tiêm fake runner; nếu `newAppRouteAPI` ở test khác cùng package thì tái dùng):

  ```go
  func TestConnectEndpoints(t *testing.T) {
      a := newAppRouteAPI(t) // *api với store thật
      a.connect = newTestManager(&fakeRunner{installed: true, auth: authLoggedIn, loginURL: "https://x"},
          a.st.EnsureProviderForKind)
      // POST start
      rec := httptest.NewRecorder()
      req := httptest.NewRequest("POST", "/llm/providers/codex/connect", nil)
      req.SetPathValue("kind", "codex")
      a.handleLLMConnectStart(rec, req)
      if rec.Code != http.StatusOK { t.Fatalf("POST connect = %d; want 200", rec.Code) }
      // kind không subscription → 400
      rec2 := httptest.NewRecorder()
      req2 := httptest.NewRequest("POST", "/llm/providers/openai/connect", nil)
      req2.SetPathValue("kind", "openai")
      a.handleLLMConnectStart(rec2, req2)
      if rec2.Code != http.StatusBadRequest { t.Errorf("POST connect(openai) = %d; want 400", rec2.Code) }
  }
  ```

  (Nếu `newAppRouteAPI` không ở package hoặc `a.connect` field chưa có, Task này thêm field `connect *connectManager` vào `api` và khởi tạo trong `app_routes.go`.)

- [ ] **Step 2: Chạy đỏ** — handler/field chưa có.
- [ ] **Step 3: Cài đặt** — thêm field `connect *connectManager` vào struct `api`; khởi tạo trong `app_routes.go` (`a.connect = &connectManager{runner: newDefaultConnectRunner(a.logger), ensure: a.st.EnsureProviderForKind, logger: a.logger}` — `newDefaultConnectRunner` ở Task 5, tạm nil-safe stub nếu Task 5 chưa xong). Handlers trong `app_llm_connect.go`:

  ```go
  func (a *api) handleLLMConnectStart(w http.ResponseWriter, r *http.Request) {
      kind := r.PathValue("kind")
      if !subscriptionKinds[kind] {
          a.writeLLMProviderErr(w, "CONNECT_KIND_UNSUPPORTED", "chỉ Claude Code / OpenAI Codex mới connect được", kind, nil)
          return
      }
      st, err := a.connect.start(kind)
      if errors.Is(err, errConnectBusy) {
          a.writeJSON(w, http.StatusConflict, map[string]any{"error": "đang có phiên kết nối khác"})
          return
      }
      if err != nil { a.writeLLMProviderErr(w, "CONNECT_FAILED", "không khởi động được", kind, err); return }
      a.writeJSON(w, http.StatusOK, st)
  }
  func (a *api) handleLLMConnectStatus(w http.ResponseWriter, r *http.Request) {
      st, ok := a.connect.status(r.PathValue("kind"))
      if !ok { a.writeJSON(w, http.StatusOK, map[string]any{"phase": "idle"}); return }
      a.writeJSON(w, http.StatusOK, st)
  }
  func (a *api) handleLLMConnectCancel(w http.ResponseWriter, r *http.Request) {
      a.connect.cancel(r.PathValue("kind"))
      a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
  }
  ```

  `writeLLMProviderErr` với code `CONNECT_KIND_UNSUPPORTED` phải map ra 400 — kiểm hàm đó ánh xạ code→status thế nào; nếu nó mặc định 4xx theo code lạ thì đặt code phù hợp, hoặc dùng `a.writeJSON(w, http.StatusBadRequest, …)` trực tiếp cho nhánh kind. **Đọc `writeLLMProviderErr` khi làm để chọn đúng.** Trong `app_routes.go` thêm allowlist + 3 dòng `mux.Handle`:

  ```go
  "POST /llm/providers/{kind}/connect",
  "GET /llm/providers/{kind}/connect",
  "DELETE /llm/providers/{kind}/connect",
  // ...
  mux.Handle("POST /llm/providers/{kind}/connect", a.auth(a.handleLLMConnectStart))
  mux.Handle("GET /llm/providers/{kind}/connect", a.auth(a.handleLLMConnectStatus))
  mux.Handle("DELETE /llm/providers/{kind}/connect", a.auth(a.handleLLMConnectCancel))
  ```

  **Lưu ý ServeMux:** đã có `POST /llm/providers/{id}/test` v.v. dùng `{id}`; route mới dùng `{kind}` ở CÙNG vị trí segment. Go 1.22 mux KHÔNG cho hai pattern khác tên wildcard cùng chỗ nếu chồng — nhưng `/connect` là literal segment cuối khác `/test`, nên không đụng. Xác nhận build không báo "conflicting patterns".

- [ ] **Step 4: Chạy xanh** + `go-check` full daemon (không vỡ handler khác).
- [ ] **Step 5: Refactor** — none.
- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_connect.go appmode/overlay/internal/daemon/app_routes.go appmode/overlay/internal/daemon/app_llm_connect_test.go
  git commit -m "feat: connect start/status/cancel endpoints"
  ```

---

### Task 5: Default `connectRunner` thật (detect/login/poll) — install/login ghim CAPTURE-FIRST

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go` (`newDefaultConnectRunner` + `defaultConnectRunner`)
- Test: không unit-test spawn thật (đó là capture-first); chỉ bảo đảm build + detect/poll tái dùng engine.

> **CHECKPOINT NEEDS-LOGIN.** Task này chạm CLI + trình duyệt THẬT. Executor DỪNG và nhờ người dùng: (a) môi trường ổn định (node ổn, codex/claude cài lại được), (b) sẵn sàng đăng nhập thật một lần. Ghim lệnh install/login + cách parse `loginURL` theo output THẬT — KHÔNG bịa. Giống engine Task 8.

- [ ] **Step 1: Khung runner tái dùng engine (không cần login thật)**

  ```go
  type defaultConnectRunner struct{ logger *slog.Logger }
  func newDefaultConnectRunner(l *slog.Logger) *defaultConnectRunner { return &defaultConnectRunner{logger: l} }

  func (d *defaultConnectRunner) detect(kind string) (bool, error) {
      desc, ok := cliDescriptors[kind]
      if !ok { return false, fmt.Errorf("kind lạ: %s", kind) }
      _, _, err := resolveCLIProgram(desc) // engine: LookPath(claude) / npm root -g + binJS(codex)
      if err == nil { return true, nil }
      // Không phân biệt được "chưa cài" vs lỗi khác ở đây thì coi như chưa cài để thử install;
      // install fail sẽ báo lỗi rõ. (resolveCLIProgram trả lỗi cả hai trường hợp.)
      return false, nil
  }
  func (d *defaultConnectRunner) pollAuth(kind string) authState {
      return checkCLIAuth(context.Background(), cliDescriptors[kind], geminiCredPath(), d.logger)
  }
  ```

- [ ] **Step 2: CAPTURE-FIRST — install + login (chạy thật lúc execute)**

  Chạy thật và ghi lại, RỒI mới viết `install`/`login`:
  - codex install: `node <npm-cli.js> install -g @openai/codex` qua node bundle — chốt đường npm-cli.js trong gói + output tiến trình.
  - claude install: installer chính chủ Windows (`irm https://claude.ai/install.ps1 | iex` hoặc `claude install`) — chốt lệnh chạy được không cần terminal.
  - login: `codex login` / `claude auth login` spawn nền — bắt URL OAuth in ra stdout (regex `https://\S+`), xác nhận trình duyệt tự mở. Nếu đòi TTY → thử `claude setup-token` / cờ device-auth; ghim cái chạy được.

  `install(ctx, kind, onLine)`: `exec.CommandContext` lệnh đã chốt, stream stdout qua `onLine`. `login(ctx, kind)`: spawn qua `runCLIProcess`-style (cancel = killPidTree cả cây), đọc stdout tìm URL, trả `(url, wait, nil)` với `wait` = chờ tiến trình. **Chỉ viết sau khi capture.**

- [ ] **Step 3–4:** build xanh (`go-check`); `detect`/`pollAuth` chạy được không cần login. `install`/`login` verify bằng một lần connect THẬT trong Portal (Task 6 xong thì thử cả luồng).
- [ ] **Step 5: Refactor** — none.
- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_connect.go
  git commit -m "feat: real connect runner (detect/poll via engine; install/login pinned to captured output)"
  ```

---

### Task 6: Frontend — panel Connect + poll + cancel trong trang chi tiết

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js`
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** Ở detail của provider subscription CHƯA kết nối, nút "Thêm kết nối" bật; bấm → `POST .../connect` rồi poll `GET .../connect`; hiển thị phase; `awaiting_login` hiện nút "Mở trang đăng nhập" (từ `loginUrl`) + Huỷ; `connected` gọi refresh.

- [ ] **Step 1: Viết test đỏ** (mock request trả POST rồi các GET theo phase; dùng harness sẵn có):

  ```js
  test("subscription connect: click drives phases and finishes connected", async (t) => {
    const phases = ["awaiting_login", "polling", "connected"];
    let i = 0;
    const { main } = mountPage(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method)
        return { providers: [], kinds: [] }; // codex chưa thêm → detail "chưa kết nối"
      if (path === "/llm/providers/codex/connect" && options.method === "POST")
        return { kind: "codex", phase: "detecting" };
      if (path === "/llm/providers/codex/connect" && !options.method) {
        const p = phases[Math.min(i++, phases.length - 1)];
        return { kind: "codex", phase: p, loginUrl: p === "awaiting_login" ? "https://auth.example/x" : "" };
      }
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    await flush();
    cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
    await flush();
    const detail = find(main, (n) => hasClass(n, "pv-detail"));
    const connect = find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n)));
    assert.equal(connect.disabled, false, "subscription connect phải bật");
    connect.click();
    await flush();
    // panel connect xuất hiện; poll chạy tới connected (đẩy timer tay nếu poll dùng setTimeout —
    // xem Step 3: poll qua async loop + flush là đủ trong harness fake).
    // ... assert panel có nút "Mở trang đăng nhập" khi awaiting_login, và cuối cùng thoát về gallery/Connected.
  });
  ```

  (Test cụ thể theo cơ chế poll đã cài; giữ nguyên tinh thần: phase hiển thị, loginUrl→nút, connected→refresh. Poll nên dùng vòng `await`-able để test đẩy bằng `flush()` thay vì `setTimeout` thực — hoặc tiêm khoảng poll = 0 trong test.)

- [ ] **Step 2: Chạy đỏ** — nút connect còn disabled (UI#1) → assert `connect.disabled === false` fail.

- [ ] **Step 3: Cài đặt** — trong `renderConnections` (chỉ provider `group === "subscription"`): nút "Thêm kết nối" BẬT (bỏ `disabled` cho subscription; API-key vẫn disabled). Handler: `service` thêm `connectStart(kind)/connectStatus(kind)/connectCancel(kind)` (POST/GET/DELETE `/llm/providers/{kind}/connect`). Bấm → mở panel `.pv-connect` trong detail, gọi start, rồi vòng poll (dùng khoảng poll đặt qua tham số để test = 0):

  ```js
  async function runConnect(kind, panel, redraw) {
    await service.connectStart(kind);
    for (;;) {
      const st = await service.connectStatus(kind);
      renderConnectPhase(panel, st); // cập nhật message/phase/loginUrl/cancel
      if (st.phase === "connected") { await refresh(); return; }
      if (st.phase === "error" || st.phase === "canceled") return;
      await sleep(pollMs); // pollMs mặc định 1500; test tiêm 0
    }
  }
  ```

  `renderConnectPhase`: `awaiting_login` → text + nút "Mở trang đăng nhập" (`openLink(st.loginUrl)` / `<a href>`), nút "Huỷ" (`service.connectCancel(kind)` + dừng vòng). Poll bọc trong `AbortController` của trang (dispose huỷ). `sleep`/`pollMs` tiêm được để test không chờ thật.

- [ ] **Step 4: Chạy xanh** (`npm --prefix appmode test`).
- [ ] **Step 5: Refactor** — poll dừng sạch khi rời trang (dispose) và khi terminal phase.
- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: in-Portal connect panel drives login and flips to Connected"
  ```

---

### Task 7: Bundle npm + CSS panel + cổng verify đầy đủ

**Files:**
- Modify: `scripts/BuildApp.psm1`, `tests/build-app.Tests.ps1`, `appmode/overlay/internal/webui/static/portal.css`

**Public behavior to verify:** Gói chứa npm chạy được (cài codex on-demand); `Assert-AppPackage` xanh (npm không bị coi là credential/rác); panel connect có style tối; toàn bộ cổng xanh.

- [ ] **Step 1: Bundle npm** — trong staging của `BuildApp.psm1`, chép cây `npm` (node_modules/npm + bin) kèm `node.exe` vào `app\node` để `node <npm-cli.js>` chạy được offline. Cập nhật `Assert-AppPackage` chấp nhận cây npm (và bảo đảm cổng quét credential không báo nhầm literal trong node_modules/npm — loại trừ hoặc xác nhận sạch).
- [ ] **Step 2: CSS** — thêm `.providers-page .pv-connect` (panel), `.pv-phase`, `.pv-login-link`, spinner đơn giản, nút Huỷ — scoped `.providers-page`, giữ dấu `providers:begin/end`.
- [ ] **Step 3: Verify matrix**
  ```powershell
  $env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User')
  $env:ZALOBOT_PERSONA=[Environment]::GetEnvironmentVariable('ZALOBOT_PERSONA','User')
  npm --prefix appmode test
  pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out (Join-Path $env:TEMP ('cn-'+[guid]::NewGuid().ToString('N').Substring(0,8)))
  ```
  Expected: tất cả xanh; gói chứa npm; `sạch: không còn dấu khách hàng nào`; nguồn AgentDC sạch trước/sau. Xoá Out.
- [ ] **Step 4: Confirm GREEN.**
- [ ] **Step 5: Refactor** — none.
- [ ] **Step 6: Commit**
  ```bash
  git add scripts/BuildApp.psm1 tests/build-app.Tests.ps1 appmode/overlay/internal/webui/static/portal.css
  git commit -m "feat: bundle npm for on-demand codex install; connect panel CSS; full gate green"
  ```

---

## Ghi chú thứ tự & rủi ro

- Task 5 là **NEEDS-LOGIN checkpoint** — executor phải dừng chờ người dùng (môi trường ổn định + đăng nhập thật). Task 1–4 và 6 (frontend) làm được KHÔNG cần CLI thật (state machine qua fake runner; frontend qua mock). Nên có thể chạy 1→4, 6 trước, rồi 5 khi người dùng sẵn sàng, rồi 7.
- Môi trường đang trôi (node v22 dịch chỗ codex). Trước Task 5, ổn định lại: cài lại codex/claude, đăng nhập, để capture-first cho đúng.
- `writeLLMProviderErr` ánh xạ code→status: đọc hàm khi làm Task 4 để nhánh kind-unsupported ra 400 (hoặc dùng `writeJSON(…,400,…)` trực tiếp).
