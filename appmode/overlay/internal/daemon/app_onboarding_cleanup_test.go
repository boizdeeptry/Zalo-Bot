package daemon

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"agentdc/internal/store"
)

func TestOnboardingCleanupRejectsNonCanonicalAccountID(t *testing.T) {
	tests := []string{
		`.`, `..`, `../victim`, `..\claude-code\victim`, `nested/account`, `nested\account`,
		`C:alternate`, `safe.`, `safe `, `CON`, `con.txt`, `NUL`, `COM1`, `LPT9.log`,
	}
	for _, accountID := range tests {
		t.Run(accountID, func(t *testing.T) {
			dataDir := t.TempDir()
			configDir := accountConfigDir(dataDir, "codex", accountID)
			err := removeOwnedOnboardingAccount(dataDir, store.OnboardingStagingAccount{
				AccountID: accountID, ProviderKind: "codex", ConfigDir: configDir,
			})
			if !errors.Is(err, errOnboardingUnsafeAccountPath) {
				t.Fatalf("account id %q error=%v; want errOnboardingUnsafeAccountPath", accountID, err)
			}
		})
	}
}

func TestOnboardingCleanupRemovesOnlyRootRelativeAccountPath(t *testing.T) {
	dataDir := t.TempDir()
	accountID := "owned"
	configDir := accountConfigDir(dataDir, "codex", accountID)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	original := removeAllOnboardingAccountRooted
	var gotRoot, gotName string
	removeAllOnboardingAccountRooted = func(root *os.Root, name string) error {
		gotRoot, gotName = root.Name(), name
		return root.RemoveAll(name)
	}
	t.Cleanup(func() { removeAllOnboardingAccountRooted = original })

	err := removeOwnedOnboardingAccount(dataDir, store.OnboardingStagingAccount{
		AccountID: accountID, ProviderKind: "codex", ConfigDir: configDir,
	})
	if err != nil {
		t.Fatalf("removeOwnedOnboardingAccount() error = %v", err)
	}
	resolvedDataDir, err := resolveOnboardingPath(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !sameOnboardingPath(gotRoot, resolvedDataDir) {
		t.Fatalf("removal root=%q; want resolved data dir %q", gotRoot, resolvedDataDir)
	}
	if want := filepath.Join("accounts", "codex", accountID); gotName != want {
		t.Fatalf("removal name=%q; want exact root-relative path %q", gotName, want)
	}
}

func TestAppOnboardingCleanupRejectsCrossProviderTraversalWithoutDataLoss(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	if err := env.a.st.EnsureProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.EnsureProviderForKind("claude-code"); err != nil {
		t.Fatal(err)
	}
	victimDir := accountConfigDir(env.dataDir, "claude-code", "victim")
	if err := os.MkdirAll(victimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(victimDir, "live.canary")
	if err := os.WriteFile(canary, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: "live-victim", ProviderID: "claude-code", Label: "live victim",
		ConfigDir: victimDir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	traversalID := `..\claude-code\victim`
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: traversalID, ProviderID: "codex", Label: "malformed staging",
		ConfigDir: victimDir, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", ProviderID: "codex",
		AccountID: traversalID, Revision: 1,
	})
	before := env.state(t)

	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if after := env.state(t); after != before {
		t.Fatalf("traversal cleanup changed state: before=%+v after=%+v", before, after)
	}
	requireAccountPresent(t, env, "codex", traversalID, true)
	requireAccountPresent(t, env, "claude-code", "live-victim", true)
	if contents, err := os.ReadFile(canary); err != nil || string(contents) != "keep" {
		t.Fatalf("traversal cleanup damaged victim: contents=%q err=%v", contents, err)
	}
}

func TestAppOnboardingCleanupRejectsForcedWindowsJunctionsWithoutDataLoss(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("forced Windows junction coverage")
	}
	tests := []struct {
		name  string
		setup func(*testing.T, *onboardingRouteTestEnv) (configDir, victimDir string)
	}{
		{
			name: "accounts root junction inside data dir",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) (string, string) {
				realAccounts := filepath.Join(env.dataDir, "real-accounts")
				if err := os.MkdirAll(realAccounts, 0o700); err != nil {
					t.Fatal(err)
				}
				accounts := filepath.Join(env.dataDir, "accounts")
				makeForcedOnboardingJunction(t, realAccounts, accounts)
				victim := filepath.Join(realAccounts, "codex", "staging")
				if err := os.MkdirAll(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(accounts, "codex", "staging"), victim
			},
		},
		{
			name: "provider junction inside accounts",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) (string, string) {
				accounts := filepath.Join(env.dataDir, "accounts")
				realProvider := filepath.Join(accounts, "claude-code")
				if err := os.MkdirAll(realProvider, 0o700); err != nil {
					t.Fatal(err)
				}
				providerLink := filepath.Join(accounts, "codex")
				makeForcedOnboardingJunction(t, realProvider, providerLink)
				victim := filepath.Join(realProvider, "staging")
				if err := os.MkdirAll(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(providerLink, "staging"), victim
			},
		},
		{
			name: "target junction inside provider",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) (string, string) {
				provider := filepath.Join(env.dataDir, "accounts", "codex")
				victim := filepath.Join(provider, "live-victim")
				if err := os.MkdirAll(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				targetLink := filepath.Join(provider, "staging")
				makeForcedOnboardingJunction(t, victim, targetLink)
				return targetLink, victim
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			if err := env.a.st.EnsureProviderForKind("codex"); err != nil {
				t.Fatal(err)
			}
			if err := env.a.st.EnsureProviderForKind("claude-code"); err != nil {
				t.Fatal(err)
			}
			configDir, victimDir := tt.setup(t, env)
			canary := filepath.Join(victimDir, "live.canary")
			if err := os.WriteFile(canary, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := env.a.st.CreateLLMAccount(store.LLMAccount{
				ID: "live-victim", ProviderID: "claude-code", Label: "live victim",
				ConfigDir: victimDir, Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
			if err := env.a.st.CreateLLMAccount(store.LLMAccount{
				ID: "staging", ProviderID: "codex", Label: "staging",
				ConfigDir: configDir, Enabled: false,
			}); err != nil {
				t.Fatal(err)
			}
			env.setState(t, store.OnboardingState{
				Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", ProviderID: "codex",
				AccountID: "staging", Revision: 1,
			})
			before := env.state(t)

			rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
			if after := env.state(t); after != before {
				t.Fatalf("junction cleanup changed state: before=%+v after=%+v", before, after)
			}
			requireAccountPresent(t, env, "codex", "staging", true)
			requireAccountPresent(t, env, "claude-code", "live-victim", true)
			if contents, err := os.ReadFile(canary); err != nil || string(contents) != "keep" {
				t.Fatalf("junction cleanup damaged victim: contents=%q err=%v", contents, err)
			}
		})
	}
}

func makeForcedOnboardingJunction(t *testing.T, target, link string) {
	t.Helper()
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create forced junction %s -> %s: %v (%s)", link, target, err, output)
	}
}

func TestOnboardingCleanupRemovesOnlyExactOwnedAccountDirectory(t *testing.T) {
	root := t.TempDir()
	target := accountConfigDir(root, "codex", "owned")
	sibling := accountConfigDir(root, "codex", "keep")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	err := removeOwnedOnboardingAccount(root, store.OnboardingStagingAccount{
		AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
	})
	if err != nil {
		t.Fatalf("removeOwnedOnboardingAccount() = %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("target remains: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling removed: %v", err)
	}
	if err := removeOwnedOnboardingAccount(root, store.OnboardingStagingAccount{
		AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
	}); err != nil {
		t.Fatalf("missing target retry = %v", err)
	}
}

func TestOnboardingCleanupRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "do-not-delete")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	tests := []struct {
		name  string
		owned store.OnboardingStagingAccount
	}{
		{"outside", store.OnboardingStagingAccount{AccountID: "owned", ProviderKind: "codex", ConfigDir: outside}},
		{"mismatched", store.OnboardingStagingAccount{AccountID: "owned", ProviderKind: "codex", ConfigDir: filepath.Join(root, "accounts", "codex", "other")}},
		{"accounts root", store.OnboardingStagingAccount{AccountID: "..", ProviderKind: "codex", ConfigDir: filepath.Join(root, "accounts")}},
		{"kind root", store.OnboardingStagingAccount{AccountID: ".", ProviderKind: "codex", ConfigDir: filepath.Join(root, "accounts", "codex")}},
		{"unsupported kind", store.OnboardingStagingAccount{AccountID: "owned", ProviderKind: "openai", ConfigDir: filepath.Join(root, "accounts", "openai", "owned")}},
		{"partial descriptor", store.OnboardingStagingAccount{ProviderKind: "codex"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := removeOwnedOnboardingAccount(root, tt.owned); !errors.Is(err, errOnboardingUnsafeAccountPath) {
				t.Fatalf("error=%v; want errOnboardingUnsafeAccountPath", err)
			}
			if _, err := os.Stat(outside); err != nil {
				t.Fatalf("outside target damaged: %v", err)
			}
		})
	}
}

func TestOnboardingCleanupRejectsNonDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	target := accountConfigDir(root, "codex", "owned")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := removeOwnedOnboardingAccount(root, store.OnboardingStagingAccount{
		AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
	})
	if !errors.Is(err, errOnboardingUnsafeAccountPath) {
		t.Fatalf("error=%v; want errOnboardingUnsafeAccountPath", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file target damaged: %v", err)
	}
}

func TestOnboardingCleanupRejectsSymlinkEscapes(t *testing.T) {
	t.Run("target", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		target := accountConfigDir(root, "codex", "owned")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		makeOnboardingTestDirLink(t, outside, target)
		err := removeOwnedOnboardingAccount(root, store.OnboardingStagingAccount{
			AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
		})
		if !errors.Is(err, errOnboardingUnsafeAccountPath) {
			t.Fatalf("error=%v; want errOnboardingUnsafeAccountPath", err)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("outside target damaged: %v", err)
		}
	})
	t.Run("intermediate", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		accounts := filepath.Join(root, "accounts")
		if err := os.MkdirAll(accounts, 0o700); err != nil {
			t.Fatal(err)
		}
		makeOnboardingTestDirLink(t, outside, filepath.Join(accounts, "codex"))
		target := filepath.Join(accounts, "codex", "owned")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		err := removeOwnedOnboardingAccount(root, store.OnboardingStagingAccount{
			AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
		})
		if !errors.Is(err, errOnboardingUnsafeAccountPath) {
			t.Fatalf("error=%v; want errOnboardingUnsafeAccountPath", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "owned")); err != nil {
			t.Fatalf("outside target damaged: %v", err)
		}
	})
}

func TestOnboardingCleanupAllowsDataDirReachedThroughSymlink(t *testing.T) {
	parent := t.TempDir()
	realDataDir := filepath.Join(parent, "real-data")
	if err := os.MkdirAll(realDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDataDir := filepath.Join(parent, "linked-data")
	makeOnboardingTestDirLink(t, realDataDir, linkedDataDir)
	target := accountConfigDir(linkedDataDir, "codex", "owned")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedOnboardingAccount(linkedDataDir, store.OnboardingStagingAccount{
		AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
	}); err != nil {
		t.Fatalf("remove through symlinked data dir = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(realDataDir, "accounts", "codex", "owned")); !os.IsNotExist(err) {
		t.Fatalf("physical target remains: %v", err)
	}
}

func seedUnsafeOnboardingStagingAccount(
	t *testing.T,
	env *onboardingRouteTestEnv,
	accountID, configDir string,
) store.OnboardingState {
	t.Helper()
	if err := env.a.st.EnsureProviderForKind("codex"); err != nil {
		t.Fatalf("EnsureProviderForKind(codex) = %v", err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: accountID, ProviderID: "codex", Label: "unsafe staging", ConfigDir: configDir, Enabled: false,
	}); err != nil {
		t.Fatalf("CreateLLMAccount(%q) = %v", accountID, err)
	}
	state := store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", ProviderID: "codex",
		AccountID: accountID, Revision: 1,
	}
	env.setState(t, state)
	return env.state(t)
}

func TestAppOnboardingProviderUnsafeCleanupPreservesStateAccountAndTargets(t *testing.T) {
	type unsafeFixture struct {
		accountID string
		configDir string
		preserve  []string
	}
	tests := []struct {
		name  string
		setup func(*testing.T, *onboardingRouteTestEnv) unsafeFixture
	}{
		{
			name: "path escape",
			setup: func(t *testing.T, _ *onboardingRouteTestEnv) unsafeFixture {
				outside := t.TempDir()
				return unsafeFixture{accountID: "owned", configDir: outside, preserve: []string{outside}}
			},
		},
		{
			name: "accounts root broad target",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				root := filepath.Join(env.dataDir, "accounts")
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				return unsafeFixture{accountID: "..", configDir: root, preserve: []string{root}}
			},
		},
		{
			name: "provider root broad target",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				root := filepath.Join(env.dataDir, "accounts", "codex")
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				return unsafeFixture{accountID: ".", configDir: root, preserve: []string{root}}
			},
		},
		{
			name: "mismatched config directory",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				mismatch := accountConfigDir(env.dataDir, "codex", "other")
				if err := os.MkdirAll(mismatch, 0o700); err != nil {
					t.Fatal(err)
				}
				return unsafeFixture{accountID: "owned", configDir: mismatch, preserve: []string{mismatch}}
			},
		},
		{
			name: "target symlink escape",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				outside := t.TempDir()
				target := accountConfigDir(env.dataDir, "codex", "owned")
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				makeOnboardingTestDirLink(t, outside, target)
				return unsafeFixture{accountID: "owned", configDir: target, preserve: []string{target, outside}}
			},
		},
		{
			name: "provider intermediate symlink escape",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				outside := t.TempDir()
				accounts := filepath.Join(env.dataDir, "accounts")
				if err := os.MkdirAll(accounts, 0o700); err != nil {
					t.Fatal(err)
				}
				providerLink := filepath.Join(accounts, "codex")
				makeOnboardingTestDirLink(t, outside, providerLink)
				physicalTarget := filepath.Join(outside, "owned")
				if err := os.MkdirAll(physicalTarget, 0o700); err != nil {
					t.Fatal(err)
				}
				return unsafeFixture{
					accountID: "owned", configDir: filepath.Join(providerLink, "owned"),
					preserve: []string{providerLink, physicalTarget},
				}
			},
		},
		{
			name: "accounts root symlink escape",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				outside := t.TempDir()
				accountsLink := filepath.Join(env.dataDir, "accounts")
				makeOnboardingTestDirLink(t, outside, accountsLink)
				physicalTarget := filepath.Join(outside, "codex", "owned")
				if err := os.MkdirAll(physicalTarget, 0o700); err != nil {
					t.Fatal(err)
				}
				return unsafeFixture{
					accountID: "owned", configDir: filepath.Join(accountsLink, "codex", "owned"),
					preserve: []string{accountsLink, physicalTarget},
				}
			},
		},
		{
			name: "non-directory target",
			setup: func(t *testing.T, env *onboardingRouteTestEnv) unsafeFixture {
				target := accountConfigDir(env.dataDir, "codex", "owned")
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				return unsafeFixture{accountID: "owned", configDir: target, preserve: []string{target}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			fixture := tt.setup(t, env)
			before := seedUnsafeOnboardingStagingAccount(t, env, fixture.accountID, fixture.configDir)
			rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
			if after := env.state(t); after != before {
				t.Fatalf("unsafe cleanup changed state: before=%+v after=%+v", before, after)
			}
			requireAccountPresent(t, env, "codex", fixture.accountID, true)
			for _, path := range fixture.preserve {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("unsafe cleanup damaged %s: %v", path, err)
				}
			}
		})
	}
}

func makeOnboardingTestDirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("directory symlink and junction unavailable: %v (%s)", err, output)
	}
}
