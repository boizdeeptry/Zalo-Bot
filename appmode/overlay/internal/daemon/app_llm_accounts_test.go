package daemon

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"agentdc/internal/store"
)

func accs(ids ...string) []store.LLMAccount {
	out := make([]store.LLMAccount, 0, len(ids))
	for _, id := range ids {
		out = append(out, store.LLMAccount{ID: id, ProviderID: "codex", Enabled: true})
	}
	return out
}

func TestSelectorRoundRobin(t *testing.T) {
	s := newAccountSelector()
	in := accs("a", "b", "c")
	var got []string
	for i := 0; i < 4; i++ {
		a, ok := s.pick("codex", in)
		if !ok {
			t.Fatalf("pick %d: ok=false", i)
		}
		got = append(got, a.ID)
	}
	want := []string{"a", "b", "c", "a"}
	if !slices.Equal(got, want) {
		t.Fatalf("round-robin = %v; want %v", got, want)
	}
}

func TestSelectorSkipsCooldown(t *testing.T) {
	s := newAccountSelector()
	in := accs("a", "b")
	s.penalize("a") // a cooldown
	a, ok := s.pick("codex", in)
	if !ok || a.ID != "b" {
		t.Fatalf("pick = %q,%v; want b (a cooling)", a.ID, ok)
	}
}

func TestSelectorAllCoolingPicksSoonest(t *testing.T) {
	s := newAccountSelector()
	in := accs("a", "b")
	s.cooldown["a"] = time.Now().Add(time.Minute) // a hết sớm hơn
	s.cooldown["b"] = time.Now().Add(time.Hour)
	a, ok := s.pick("codex", in)
	if !ok || a.ID != "a" {
		t.Fatalf("pick = %q,%v; want a (soonest)", a.ID, ok)
	}
}

func TestSelectorNoEnabled(t *testing.T) {
	s := newAccountSelector()
	if _, ok := s.pick("codex", nil); ok {
		t.Fatalf("pick on 0 accounts: ok=true; want false")
	}
	dis := []store.LLMAccount{{ID: "x", Enabled: false}}
	if _, ok := s.pick("codex", dis); ok {
		t.Fatalf("pick on disabled-only: ok=true; want false")
	}
}

func TestEnvVarFor(t *testing.T) {
	for _, tc := range []struct {
		kind, want string
		ok         bool
	}{
		{"codex", "CODEX_HOME", true},
		{"claude-code", "CLAUDE_CONFIG_DIR", true},
		{"claude_code", "", false}, // kind cũ (gạch dưới) không còn khớp sau khi hợp nhất
		{"openai", "", false},
	} {
		got, ok := envVarFor(tc.kind)
		if got != tc.want || ok != tc.ok {
			t.Errorf("envVarFor(%q) = %q,%v; want %q,%v", tc.kind, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMakeAccountEnvPicksAndFormats(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
	_ = st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "x",
		ConfigDir: `C:\home\accounts\codex\a1`, Enabled: true})
	sel := newAccountSelector()
	envFn := makeAccountEnv(st, sel, "codex", "codex")
	env, penalize, ok := envFn()
	if !ok || penalize == nil {
		t.Fatalf("envFn ok=%v penalize==nil=%v; want true, non-nil", ok, penalize == nil)
	}
	if !slices.Contains(env, `CODEX_HOME=C:\home\accounts\codex\a1`) {
		t.Fatalf("env = %v; want CODEX_HOME=...a1", env)
	}
}

func TestMakeAccountEnvNoAccount(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	envFn := makeAccountEnv(st, newAccountSelector(), "codex", "codex")
	if _, _, ok := envFn(); ok {
		t.Fatalf("envFn ok=true with 0 accounts; want false")
	}
}

// TestMakeAccountEnvPenalizeOnlyOnRateLimit ghim nhánh gating: penalize(false) KHÔNG được cooldown
// (mọi lượt thành công đều gọi penalize(false)); chỉ penalize(true) mới cooldown. Nhánh này lật là
// âm thầm bào mòn mọi account, mà các test khác vẫn xanh — nên cần guard riêng.
func TestMakeAccountEnvPenalizeOnlyOnRateLimit(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
	_ = st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "x", ConfigDir: "d", Enabled: true})
	sel := newAccountSelector()
	_, penalize, _ := makeAccountEnv(st, sel, "codex", "codex")()
	penalize(false)
	if _, cooling := sel.cooldown["a1"]; cooling {
		t.Fatalf("penalize(false) cooled down a1; want no-op")
	}
	penalize(true)
	if _, cooling := sel.cooldown["a1"]; !cooling {
		t.Fatalf("penalize(true) did not cool down a1")
	}
}

func TestOnboardingPinnedAccountResolversIgnoreLiveSelection(t *testing.T) {
	staged := store.OnboardingStagingAccount{
		AccountID: "staged", ProviderID: "codex", ProviderKind: "codex",
		ConfigDir: `C:\onboarding\staged`,
	}
	pick := makeOnboardingPinnedConfigDir(staged)
	for i := 0; i < 3; i++ {
		configDir, penalize, ok := pick()
		if !ok || configDir != staged.ConfigDir || penalize == nil {
			t.Fatalf("pinned config call %d = %q, penalize-nil=%t, ok=%t; want staged directory", i, configDir, penalize == nil, ok)
		}
		penalize(true)
	}

	envFn := makeOnboardingPinnedAccountEnv(staged)
	env, penalize, ok := envFn()
	if !ok || penalize == nil {
		t.Fatalf("pinned env = _, penalize-nil=%t, ok=%t; want exact staged account", penalize == nil, ok)
	}
	want := "CODEX_HOME=" + staged.ConfigDir
	if !slices.Contains(env, want) {
		t.Fatalf("pinned env lacks %q: %v", want, env)
	}
}

// TestSpawnSetsAccountEnvAndPenalizesOnRateLimit ghim seam accountEnv trên đường TEST (a.run):
// spawn phải wire penalize TRƯỚC nhánh a.run, nên một lỗi rate_limit từ CLI phải gọi penalize(true).
func TestSpawnSetsAccountEnvAndPenalizesOnRateLimit(t *testing.T) {
	var penalized bool
	a := newCLIAdapter(cliDescriptors["codex"], "codex", slog.New(slog.DiscardHandler))
	a.accountEnv = func() ([]string, func(bool), bool) {
		return []string{`CODEX_HOME=C:\d\a1`}, func(rl bool) { penalized = rl }, true
	}
	// run giả trả lỗi rate_limit (chuỗi "usage limit" → classifyCLIError = rate_limit).
	a.run = func(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
		return nil, &cliExit{err: errors.New("x"), stderr: "usage limit reached"}
	}
	_, err := a.Generate(context.Background(), llmRequest{Prompt: "hi", Model: "gpt-5.6-terra"}, nil)
	if llmErrorKindOf(err) != llmErrorRateLimit {
		t.Fatalf("kind = %v; want rate_limit", llmErrorKindOf(err))
	}
	if !penalized {
		t.Fatalf("penalize không được gọi khi rate_limit")
	}
}

// TestGenerateNoAccountIsCredential: accountEnv trả ok=false (0 account enabled) → Generate trả
// credential (DỪNG chuỗi), y như CLI chưa cài — chưa đăng nhập thì thử tiếp vô ích.
func TestGenerateNoAccountIsCredential(t *testing.T) {
	a := newCLIAdapter(cliDescriptors["codex"], "codex", slog.New(slog.DiscardHandler))
	a.accountEnv = func() ([]string, func(bool), bool) { return nil, nil, false }
	_, err := a.Generate(context.Background(), llmRequest{Prompt: "hi", Model: "gpt-5.6-terra"}, nil)
	if llmErrorKindOf(err) != llmErrorCredential {
		t.Fatalf("kind = %v; want credential (0 account = chưa đăng nhập)", llmErrorKindOf(err))
	}
}
