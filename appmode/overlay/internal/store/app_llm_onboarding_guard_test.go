package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLLMMutationRejectsOnboardingStageReference(t *testing.T) {
	t.Run("provider delete protects pending and ready kind references", func(t *testing.T) {
		for _, status := range []string{onboardingProviderStagePending, onboardingProviderStageReady} {
			t.Run(status, func(t *testing.T) {
				st := seedLLMOnboardingStageGuardForTest(t, status)
				before := onboardingProviderMutationDigestForTest(t, st)
				if err := st.DeleteLLMProvider("codex"); !errors.Is(err, ErrLLMOnboardingStageInUse) {
					t.Fatalf("DeleteLLMProvider() error = %v; want stage-in-use", err)
				}
				if after := onboardingProviderMutationDigestForTest(t, st); after != before {
					t.Fatal("rejected Provider delete mutated database")
				}
			})
		}
	})

	t.Run("provider metadata remains mutable while enabled is protected", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		provider := llmProviderForGuardTest(t, st, "codex")
		checkedAt := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
		provider.Name = "Renamed"
		provider.LastCheckStatus = LLMAttemptError
		provider.LastError = "sanitized health"
		provider.LastCheckedAt = &checkedAt
		if err := st.UpdateLLMProvider(provider); err != nil {
			t.Fatalf("Provider metadata update = %v", err)
		}
		updated := llmProviderForGuardTest(t, st, "codex")
		if updated.Name != provider.Name || updated.LastCheckStatus != provider.LastCheckStatus ||
			updated.LastError != provider.LastError || updated.LastCheckedAt == nil ||
			!updated.LastCheckedAt.Equal(checkedAt) || updated.Enabled != provider.Enabled {
			t.Fatalf("updated Provider = %+v; want metadata from %+v", updated, provider)
		}
		before := onboardingProviderMutationDigestForTest(t, st)
		updated.Enabled = !updated.Enabled
		if err := st.UpdateLLMProvider(updated); !errors.Is(err, ErrLLMOnboardingStageInUse) {
			t.Fatalf("staged Provider Enabled change error = %v; want stage-in-use", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("rejected Provider Enabled change mutated database")
		}
	})

	t.Run("ready account and model delete", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		before := onboardingProviderMutationDigestForTest(t, st)
		if err := st.DeleteLLMAccount("codex-account"); !errors.Is(err, ErrLLMOnboardingStageInUse) {
			t.Fatalf("DeleteLLMAccount() error = %v; want stage-in-use", err)
		}
		if err := st.DeleteLLMModel("codex", "codex-model"); !errors.Is(err, ErrLLMOnboardingStageInUse) {
			t.Fatalf("DeleteLLMModel() error = %v; want stage-in-use", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("rejected Account/model delete mutated database")
		}
	})

	t.Run("staged model upsert permits only exact no-op", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		exact := LLMModel{
			ProviderID: "codex", ModelID: "codex-model", Name: "Codex model",
			Source: LLMModelDiscovered, Available: true,
		}
		before := onboardingProviderMutationDigestForTest(t, st)
		if err := st.AddLLMModel(exact); err != nil {
			t.Fatalf("exact staged model no-op = %v", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("exact staged model no-op changed database")
		}
		for _, changed := range []LLMModel{
			{ProviderID: "codex", ModelID: "codex-model", Name: "renamed", Source: LLMModelDiscovered, Available: true},
			{ProviderID: "codex", ModelID: "codex-model", Name: "Codex model", Source: LLMModelManual, Available: true},
			{ProviderID: "codex", ModelID: "codex-model", Name: "Codex model", Source: LLMModelDiscovered, Available: false},
		} {
			if err := st.AddLLMModel(changed); !errors.Is(err, ErrLLMOnboardingStageInUse) {
				t.Fatalf("changed AddLLMModel(%+v) error = %v; want stage-in-use", changed, err)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatal("rejected staged model upsert mutated database")
			}
		}
	})
}

func TestLLMMutationRejectsOnboardingStageReferenceForActiveSetupAccount(t *testing.T) {
	st, connected := prepareOnboardingMultiConnectForTest(t, "codex")
	account := onboardingMultiAccountForTest("codex", "active-setup-account")
	bound, err := st.BindOnboardingAccount(connected.State.Revision, "codex", account)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State.Phase != OnboardingPhaseSetup || bound.State.AccountID != account.ID ||
		len(bound.Stages) == 0 || bound.Stages[0].Status != onboardingProviderStagePending {
		t.Fatalf("active Setup fixture = %+v", bound)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	if err := st.DeleteLLMAccount(account.ID); !errors.Is(err, ErrLLMOnboardingStageInUse) {
		t.Fatalf("Delete active Setup Account error = %v; want stage-in-use", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("rejected active Setup Account delete mutated database")
	}
}

func TestLLMMutationRejectsOnboardingStageReferenceDuringModelReplacement(t *testing.T) {
	tests := []struct {
		name   string
		models []LLMModel
		want   error
	}{
		{name: "omitted", models: nil, want: ErrLLMOnboardingStageInUse},
		{name: "unavailable", models: []LLMModel{{ModelID: "codex-model", Name: "Codex model", Available: false}}, want: ErrLLMOnboardingStageInUse},
		{name: "preserved", models: []LLMModel{{ModelID: "codex-model", Name: "refreshed label", Available: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
			before := onboardingProviderMutationDigestForTest(t, st)
			err := st.ReplaceLLMModels("codex", LLMModelDiscovered, test.models)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("ReplaceLLMModels() error = %v; want %v", err, test.want)
				}
				if after := onboardingProviderMutationDigestForTest(t, st); after != before {
					t.Fatal("rejected model replacement mutated database")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			models, err := st.LLMModels("codex")
			if err != nil || len(models) != 1 || models[0].ModelID != "codex-model" || !models[0].Available {
				t.Fatalf("preserved replacement models=%+v err=%v", models, err)
			}
		})
	}

	t.Run("pending stage does not block discovery", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStagePending)
		if err := st.ReplaceLLMModels("codex", LLMModelDiscovered, nil); err != nil {
			t.Fatalf("pending stage model discovery = %v", err)
		}
	})

	t.Run("unrelated source and Provider remain mutable", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		if err := st.ReplaceLLMModels("codex", LLMModelManual, []LLMModel{{ModelID: "manual", Name: "manual", Available: true}}); err != nil {
			t.Fatalf("unrelated source replacement = %v", err)
		}
		if err := st.CreateLLMProvider(LLMProvider{ID: "other", Name: "Other", Kind: "openai"}); err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceLLMModels("other", LLMModelDiscovered, nil); err != nil {
			t.Fatalf("unrelated Provider replacement = %v", err)
		}
	})

	t.Run("mid-replacement error rolls back", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		before := onboardingProviderMutationDigestForTest(t, st)
		err := st.ReplaceLLMModels("codex", LLMModelDiscovered, []LLMModel{
			{ModelID: "codex-model", Name: "Codex model", Available: true},
			{ModelID: "", Name: "invalid", Available: true},
		})
		if err == nil {
			t.Fatal("invalid replacement unexpectedly succeeded")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("mid-replacement error escaped rollback")
		}
	})
}

func TestLLMMutationRejectsOnboardingStageReferenceConcurrentlyAndReleasesAfterStages(t *testing.T) {
	t.Run("concurrent deletes fail closed", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		before := onboardingProviderMutationDigestForTest(t, st)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- st.DeleteLLMModel("codex", "codex-model")
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if !errors.Is(err, ErrLLMOnboardingStageInUse) {
				t.Fatalf("concurrent delete error = %v; want stage-in-use", err)
			}
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("concurrent rejected deletes mutated database")
		}
	})

	t.Run("empty stage table restores normal mutations", func(t *testing.T) {
		st := seedLLMOnboardingStageGuardForTest(t, onboardingProviderStageReady)
		if _, err := st.db.Exec(`DELETE FROM app_onboarding_provider_stages`); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteLLMModel("codex", "codex-model"); err != nil {
			t.Fatalf("DeleteLLMModel after stages cleared = %v", err)
		}
		if err := st.DeleteLLMAccount("codex-account"); err != nil {
			t.Fatalf("DeleteLLMAccount after stages cleared = %v", err)
		}
		if err := st.DeleteLLMProvider("codex"); err != nil {
			t.Fatalf("DeleteLLMProvider after stages cleared = %v", err)
		}
	})
}

func seedLLMOnboardingStageGuardForTest(t *testing.T, status string) *Store {
	t.Helper()
	st := openAppStoreForTest(t)
	if err := st.CreateLLMProvider(LLMProvider{ID: "codex", Name: "Codex", Kind: "codex"}); err != nil {
		t.Fatal(err)
	}
	if status == onboardingProviderStageReady {
		if err := st.CreateLLMAccount(LLMAccount{
			ID: "codex-account", ProviderID: "codex", Label: "Codex account",
			ConfigDir: "D:/private/codex-account",
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddLLMModel(LLMModel{
			ProviderID: "codex", ModelID: "codex-model", Name: "Codex model",
			Source: LLMModelDiscovered, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES ('codex', 'ready', 0, 'codex', 'codex-account', 'codex-model', '2026-08-15T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES ('codex', 'pending', 0, '', '', '', '2026-08-15T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func llmProviderForGuardTest(t *testing.T, st *Store, id string) LLMProvider {
	t.Helper()
	providers, err := st.LLMProviders()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		if provider.ID == id {
			return provider
		}
	}
	t.Fatalf("Provider %q not found", id)
	return LLMProvider{}
}
