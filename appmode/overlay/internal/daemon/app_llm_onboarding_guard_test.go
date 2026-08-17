package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentdc/internal/store"
)

func TestLLMAPIRejectsOnboardingStageReferenceWithPrivateConflict(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "update Provider", method: http.MethodPut, path: "/llm/providers/codex", body: `{"name":"Renamed","enabled":true}`},
		{name: "delete Provider", method: http.MethodDelete, path: "/llm/providers/codex"},
		{name: "change staged model", method: http.MethodPost, path: "/llm/providers/codex/models", body: `{"model_id":"codex-model","name":"Renamed"}`},
		{name: "delete staged model", method: http.MethodDelete, path: "/llm/providers/codex/models?model_id=codex-model"},
		{name: "delete staged Account", method: http.MethodDelete, path: "/llm/providers/codex/accounts/codex-account"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, configDir := seedLLMAPIOnboardingStageGuardForTest(t)
			marker := filepath.Join(configDir, "keep.txt")
			if err := os.WriteFile(marker, []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}

			rr := env.serve(test.method, test.path, test.body)
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGE_IN_USE")
			if strings.Contains(rr.Body.String(), configDir) || strings.Contains(rr.Body.String(), "codex-account") ||
				strings.Contains(rr.Body.String(), "codex-model") {
				t.Fatalf("stage conflict leaked identity/path: %s", rr.Body.String())
			}
			after, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if before.State != after.State || len(before.Stages) != len(after.Stages) || before.Stages[0] != after.Stages[0] {
				t.Fatal("rejected LLM API mutation changed onboarding stage")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("rejected LLM API mutation removed Account config: %v", err)
			}
		})
	}
}

func TestLLMAPIRejectsOnboardingStageReferenceForActiveSetupAccountWithoutFilesystemRemoval(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	selected, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := env.a.st.BeginOnboardingProvider(selected.State.Revision, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.EnsureOnboardingProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(env.dataDir, "active-setup-account")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(configDir, "keep.txt")
	if err := os.WriteFile(marker, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	account := store.LLMAccount{
		ID: "active-setup-account", ProviderID: "codex", Label: "Active Setup",
		Email: "private-setup@example.test", ConfigDir: configDir,
	}
	bound, err := env.a.st.BindOnboardingAccount(connected.State.Revision, "codex", account)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State.Phase != store.OnboardingPhaseSetup || bound.State.AccountID != account.ID {
		t.Fatalf("active Setup fixture = %+v", bound)
	}

	rr := env.serve(http.MethodDelete, "/llm/providers/codex/accounts/"+account.ID, "")
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGE_IN_USE")
	if strings.Contains(rr.Body.String(), configDir) || strings.Contains(rr.Body.String(), account.ID) {
		t.Fatalf("active Setup conflict leaked identity/path: %s", rr.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("active Setup Account config was removed: %v", err)
	}
	accounts, err := env.a.st.LLMAccounts("codex")
	if err != nil || len(accounts) != 1 || accounts[0].ID != account.ID {
		t.Fatalf("active Setup Account after rejection = %+v err=%v", accounts, err)
	}
}

func seedLLMAPIOnboardingStageGuardForTest(t *testing.T) (*onboardingRouteTestEnv, string) {
	t.Helper()
	env := newOnboardingRouteTestEnv(t)
	if err := env.a.st.EnsureOnboardingProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(env.dataDir, "private-codex-account")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: "codex-account", ProviderID: "codex", Label: "Private account",
		Email: "private@example.test", ConfigDir: configDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.AddLLMModel(store.LLMModel{
		ProviderID: "codex", ModelID: "codex-model", Name: "Codex model",
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	env.replaceProviderStages(t, appOnboardingProviderWire{
		Kind: "codex", Status: "ready", ProviderID: "codex",
		AccountID: "codex-account", ModelID: "codex-model", Position: 0,
	})
	return env, configDir
}
