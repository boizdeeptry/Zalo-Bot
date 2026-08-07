package daemon

import (
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
		{"claude_code", "CLAUDE_CONFIG_DIR", true},
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
