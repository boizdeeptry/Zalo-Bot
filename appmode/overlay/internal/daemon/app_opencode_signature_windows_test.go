//go:build windows

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOpenCodeAuthenticodeGateRequiresTrustedExpectedSigner(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("pinned OpenCode manifest is Windows amd64 only")
	}
	original := openCodeAuthenticodeSignerSubject
	t.Cleanup(func() { openCodeAuthenticodeSignerSubject = original })

	tests := []struct {
		name    string
		subject string
		err     error
		wantErr bool
	}{
		{name: "exact signer", subject: "Anomaly Innovations, Inc"},
		{name: "signer within subject", subject: "CN=Anomaly Innovations, Inc, O=Anomaly Innovations, Inc"},
		{name: "wrong signer", subject: "Another Publisher", wantErr: true},
		{name: "case changed signer", subject: "anomaly innovations, inc", wantErr: true},
		{name: "trust failure", err: errors.New("untrusted raw detail"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			openCodeAuthenticodeSignerSubject = func(string) (string, error) {
				return test.subject, test.err
			}
			err := verifyOpenCodePlatformSignature(`C:\owned\opencode.exe`)
			if test.wantErr && !errors.Is(err, ErrOpenCodeBinaryRejected) {
				t.Fatalf("error = %v; want binary rejected", err)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
			if err != nil && (containsErrorText(err, test.subject) || containsErrorText(err, "untrusted raw detail")) {
				t.Fatalf("signature error leaked raw detail: %v", err)
			}
		})
	}
}

func TestOpenCodeAuthenticodeNativeUsesSilentOfflineTrustAndAlwaysClosesState(t *testing.T) {
	originalVerify := openCodeWinVerifyTrust
	originalSubject := openCodeWinTrustSignerSubject
	t.Cleanup(func() {
		openCodeWinVerifyTrust = originalVerify
		openCodeWinTrustSignerSubject = originalSubject
	})

	var actions []uint32
	openCodeWinVerifyTrust = func(_ windows.HWND, _ *windows.GUID, data *windows.WinTrustData) error {
		actions = append(actions, data.StateAction)
		if data.UIChoice != windows.WTD_UI_NONE || data.RevocationChecks != windows.WTD_REVOKE_NONE {
			t.Fatalf("interactive or network-dependent trust policy: %+v", data)
		}
		if data.ProvFlags&windows.WTD_CACHE_ONLY_URL_RETRIEVAL == 0 {
			t.Fatalf("cache-only trust flag missing: %#x", data.ProvFlags)
		}
		if data.StateAction == windows.WTD_STATEACTION_VERIFY {
			data.StateData = windows.Handle(42)
		}
		return nil
	}
	openCodeWinTrustSignerSubject = func(state windows.Handle) (string, error) {
		if state != windows.Handle(42) {
			t.Fatalf("state = %v; want 42", state)
		}
		return openCodeExpectedSignerSubject, nil
	}

	subject, err := queryOpenCodeAuthenticodeSigner(`C:\owned\opencode.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if subject != openCodeExpectedSignerSubject {
		t.Fatalf("subject = %q", subject)
	}
	want := []uint32{windows.WTD_STATEACTION_VERIFY, windows.WTD_STATEACTION_CLOSE}
	if !slices.Equal(actions, want) {
		t.Fatalf("state actions = %v; want %v", actions, want)
	}
}

func TestOpenCodeAuthenticodeNativeClosesStateAfterTrustFailure(t *testing.T) {
	originalVerify := openCodeWinVerifyTrust
	originalSubject := openCodeWinTrustSignerSubject
	t.Cleanup(func() {
		openCodeWinVerifyTrust = originalVerify
		openCodeWinTrustSignerSubject = originalSubject
	})

	var actions []uint32
	signerCalled := false
	openCodeWinVerifyTrust = func(_ windows.HWND, _ *windows.GUID, data *windows.WinTrustData) error {
		actions = append(actions, data.StateAction)
		if data.StateAction == windows.WTD_STATEACTION_VERIFY {
			data.StateData = windows.Handle(7)
			return errors.New("bad trust raw detail")
		}
		return nil
	}
	openCodeWinTrustSignerSubject = func(windows.Handle) (string, error) {
		signerCalled = true
		return "", nil
	}

	_, err := queryOpenCodeAuthenticodeSigner(`C:\owned\opencode.exe`)
	if !errors.Is(err, ErrOpenCodeBinaryRejected) {
		t.Fatalf("error = %v; want binary rejected", err)
	}
	if signerCalled {
		t.Fatal("signer was queried after trust failed")
	}
	want := []uint32{windows.WTD_STATEACTION_VERIFY, windows.WTD_STATEACTION_CLOSE}
	if !slices.Equal(actions, want) {
		t.Fatalf("state actions = %v; want %v", actions, want)
	}
}

func TestOpenCodeSmokeRootLockPreventsReplacementAndDeletesExactDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentdc-opencode-smoke-root-lock")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOpenCodeSmokeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	if err := os.Rename(root, root+"-moved"); err == nil {
		t.Fatal("locked root was renamed")
	}
	if err := lock.remove(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("exact locked root remains: %v", err)
	}
}

func TestOpenCodeVerifiedBinaryLockBlocksMutationUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode-owned-copy.exe")
	if err := os.WriteFile(path, []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOpenCodeVerifiedBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = lock.Close()
		}
	})

	moved := path + ".moved"
	if err := os.WriteFile(path, []byte("overwritten"), 0o600); err == nil {
		t.Fatal("locked verified binary was overwritten")
	}
	if err := os.Rename(path, moved); err == nil {
		t.Fatal("locked verified binary was renamed")
	}
	if err := os.Remove(path); err == nil {
		t.Fatal("locked verified binary was deleted")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "verified" {
		t.Fatalf("locked binary changed: content=%q error=%v", content, err)
	}

	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if err := os.WriteFile(path, []byte("overwritten"), 0o600); err != nil {
		t.Fatalf("overwrite remained blocked after close: %v", err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("rename remained blocked after close: %v", err)
	}
	if err := os.Remove(moved); err != nil {
		t.Fatalf("delete remained blocked after close: %v", err)
	}
}

func TestOpenCodeWinTrustSignerLayoutsRejectShortNativeStructures(t *testing.T) {
	signer := &openCodeCryptProviderSigner{}
	if validOpenCodeCryptProviderSigner(signer) {
		t.Fatal("zero-sized signer structure was accepted")
	}
	signer.size = uint32(unsafe.Sizeof(*signer))
	if !validOpenCodeCryptProviderSigner(signer) {
		t.Fatal("full signer structure was rejected")
	}
	certificate := &openCodeCryptProviderCert{}
	if validOpenCodeCryptProviderCert(certificate) {
		t.Fatal("zero-sized certificate structure was accepted")
	}
	certificate.size = uint32(unsafe.Sizeof(*certificate))
	if !validOpenCodeCryptProviderCert(certificate) {
		t.Fatal("full certificate structure was rejected")
	}
}

func containsErrorText(err error, text string) bool {
	return text != "" && strings.Contains(err.Error(), text)
}
