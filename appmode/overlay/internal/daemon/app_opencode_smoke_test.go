package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const openCodeSmokeCandidateSizeLimit = 512 << 20

type openCodeLocalSmokeDependencies struct {
	lockBinary      func(string) (io.Closer, error)
	hashFile        func(string) ([32]byte, error)
	verifySignature func(string) error
	version         func(context.Context, string) (string, error)
	run             func(context.Context, openCodeSyntheticCapability, string) (string, error)
}

type openCodeSmokeRootOwnership struct {
	path      string
	lock      openCodeSmokeRootLock
	candidate string
}

func runVerifiedLocalOpenCodeSmoke(ctx context.Context, binary string) (string, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", ErrOpenCodeContainmentUnavailable
	}
	return runVerifiedLocalOpenCodeSmokeWithDependencies(ctx, binary, openCodeLocalSmokeDependencies{
		lockBinary:      lockOpenCodeVerifiedBinary,
		hashFile:        hashOpenCodeSmokeCandidate,
		verifySignature: verifyOpenCodePlatformSignature,
		version:         readOpenCodeSmokeVersion,
		run:             runOpenCodeSyntheticSpike,
	})
}

func TestOpenCodeLocalSmokeDefaultGateIsUnavailableOutsideWindowsAMD64(t *testing.T) {
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		t.Skip("supported signed-manifest platform")
	}
	_, err := runVerifiedLocalOpenCodeSmoke(t.Context(), filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, ErrOpenCodeContainmentUnavailable) {
		t.Fatalf("error = %v; want containment unavailable", err)
	}
}

func runVerifiedLocalOpenCodeSmokeWithDependencies(
	ctx context.Context,
	binary string,
	dependencies openCodeLocalSmokeDependencies,
) (string, error) {
	if ctx == nil || dependencies.lockBinary == nil || dependencies.hashFile == nil ||
		dependencies.verifySignature == nil || dependencies.version == nil || dependencies.run == nil {
		return "", ErrOpenCodeContainmentUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validOpenCodeSmokeSource(binary) {
		return "", openCodeBinaryRejectedError("path")
	}

	ownership, err := newOpenCodeSmokeRootOwnership()
	if err != nil {
		return "", ErrOpenCodeCleanupFailed
	}
	root := ownership.path
	candidate := filepath.Join(root, "opencode-"+openCodePinnedVersion+".exe")
	ownership.candidate = candidate
	if err := copyOpenCodeSmokeCandidate(ctx, binary, candidate); err != nil {
		return "", finishOpenCodeSmokeRoot(ownership, nil, true, err)
	}
	locked, err := dependencies.lockBinary(candidate)
	if err != nil {
		return "", finishOpenCodeSmokeRoot(ownership, nil, true, openCodeBinaryRejectedError("lock"))
	}

	digest, err := dependencies.hashFile(candidate)
	if err != nil || subtle.ConstantTimeCompare(digest[:], openCodePinnedSHA256[:]) != 1 {
		return "", finishOpenCodeSmokeRoot(ownership, locked, true, openCodeBinaryRejectedError("hash"))
	}
	if err := dependencies.verifySignature(candidate); err != nil {
		return "", finishOpenCodeSmokeRoot(
			ownership,
			locked,
			true,
			sanitizeOpenCodeSmokeGateError(err, "signature"),
		)
	}
	version, err := dependencies.version(ctx, candidate)
	if err != nil {
		return "", finishOpenCodeSmokeRoot(
			ownership,
			locked,
			!errors.Is(err, ErrOpenCodeCleanupFailed),
			sanitizeOpenCodeSmokeGateError(err, "version"),
		)
	}
	if !exactOpenCodeSmokeVersion(version) {
		return "", finishOpenCodeSmokeRoot(ownership, locked, true, openCodeBinaryRejectedError("version"))
	}
	if err := ctx.Err(); err != nil {
		return "", finishOpenCodeSmokeRoot(ownership, locked, true, err)
	}

	answer, err := dependencies.run(ctx, openCodeSyntheticCapability{rootBase: root}, candidate)
	if err != nil {
		return "", finishOpenCodeSmokeRoot(
			ownership,
			locked,
			!errors.Is(err, ErrOpenCodeCleanupFailed),
			sanitizeOpenCodeProcessError(err),
		)
	}
	if answer != "OK" {
		return "", finishOpenCodeSmokeRoot(ownership, locked, true, ErrOpenCodeUnexpectedAnswer)
	}
	if err := finishOpenCodeSmokeRoot(ownership, locked, true, nil); err != nil {
		return "", err
	}
	return answer, nil
}

func newOpenCodeSmokeRootOwnership() (*openCodeSmokeRootOwnership, error) {
	return newOpenCodeSmokeRootOwnershipWithDependencies(
		func() (string, error) {
			return os.MkdirTemp(filepath.Clean(os.TempDir()), "agentdc-opencode-smoke-")
		},
		lockOpenCodeSmokeRoot,
	)
}

func newOpenCodeSmokeRootOwnershipWithDependencies(
	createRoot func() (string, error),
	lockRoot func(string) (openCodeSmokeRootLock, error),
) (*openCodeSmokeRootOwnership, error) {
	if createRoot == nil || lockRoot == nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	root, err := createRoot()
	if err != nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	lock, err := lockRoot(root)
	if err != nil || lock == nil {
		if lock != nil {
			_ = lock.Close()
		}
		_ = os.Remove(root)
		return nil, ErrOpenCodeCleanupFailed
	}
	return &openCodeSmokeRootOwnership{path: root, lock: lock}, nil
}

func TestNewOpenCodeSmokeRootOwnershipRemovesFreshRootWhenLockFails(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "agentdc-opencode-smoke-lock-failure")
	sibling := filepath.Join(parent, "operator-owned.txt")
	if err := os.WriteFile(sibling, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	ownership, err := newOpenCodeSmokeRootOwnershipWithDependencies(
		func() (string, error) {
			return root, os.Mkdir(root, 0o700)
		},
		func(string) (openCodeSmokeRootLock, error) {
			return nil, ErrOpenCodeCleanupFailed
		},
	)
	if !errors.Is(err, ErrOpenCodeCleanupFailed) {
		t.Fatalf("error = %v; want cleanup failed", err)
	}
	if ownership != nil {
		t.Fatal("ownership returned after root lock failed")
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Fatalf("fresh smoke root remained after lock failure: %v", statErr)
	}
	payload, readErr := os.ReadFile(sibling)
	if readErr != nil || string(payload) != "preserve" {
		t.Fatalf("sibling was changed during exact-root cleanup: payload=%q error=%v", payload, readErr)
	}
}

func validOpenCodeSmokeSource(path string) bool {
	if !validOpenCodeAbsolutePath(path, false) || !openCodePlatformBinaryPathSafe(path) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func copyOpenCodeSmokeCandidate(ctx context.Context, sourcePath, candidatePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return openCodeBinaryRejectedError("copy source")
	}
	destination, err := os.OpenFile(candidatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		_ = source.Close()
		return ErrOpenCodeCleanupFailed
	}
	written, copyErr := io.Copy(destination, io.LimitReader(source, openCodeSmokeCandidateSizeLimit+1))
	sourceCloseErr := source.Close()
	syncErr := destination.Sync()
	destinationCloseErr := destination.Close()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if copyErr != nil || sourceCloseErr != nil || syncErr != nil || destinationCloseErr != nil ||
		written == 0 || written > openCodeSmokeCandidateSizeLimit {
		return openCodeBinaryRejectedError("copy")
	}
	return nil
}

func hashOpenCodeSmokeCandidate(path string) ([32]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, openCodeBinaryRejectedError("hash")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return [32]byte{}, openCodeBinaryRejectedError("hash")
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func exactOpenCodeSmokeVersion(output string) bool {
	switch {
	case strings.HasSuffix(output, "\r\n"):
		output = strings.TrimSuffix(output, "\r\n")
	case strings.HasSuffix(output, "\n"):
		output = strings.TrimSuffix(output, "\n")
	}
	return output == openCodePinnedVersion
}

func newOpenCodeVersionSpec(root, binary string) (openCodeSpikeSpec, error) {
	spec, err := newOpenCodeBaseSpec(root, binary)
	if err != nil {
		return openCodeSpikeSpec{}, err
	}
	spec.Args = []string{"--version"}
	return spec, nil
}

func readOpenCodeSmokeVersion(ctx context.Context, binary string) (string, error) {
	if ctx == nil || !validOpenCodeBase(filepath.Dir(binary)) {
		return "", ErrOpenCodeContainmentUnavailable
	}
	base := filepath.Dir(binary)
	root, err := os.MkdirTemp(base, "agentdc-opencode-spike-")
	if err != nil {
		return "", ErrOpenCodeCleanupFailed
	}
	if err := createOpenCodeOwnedDirectories(root); err != nil {
		_ = os.RemoveAll(root)
		return "", ErrOpenCodeCleanupFailed
	}
	spec, err := newOpenCodeVersionSpec(root, binary)
	if err != nil {
		return "", finishOpenCodeOwnedRoot(base, root, nil, err)
	}
	outcome := runOpenCodeManagedCommand(ctx, newOpenCodeCommand(spec))
	if !outcome.quiescent {
		return "", ErrOpenCodeCleanupFailed
	}
	forbidden := addOpenCodePrivatePatterns(nil, outcome)
	if outcome.err != nil {
		return "", finishOpenCodeOwnedRoot(
			base,
			root,
			forbidden,
			sanitizeOpenCodeProcessError(outcome.err),
		)
	}
	if err := finishOpenCodeOwnedRoot(base, root, forbidden, nil); err != nil {
		return "", err
	}
	return string(outcome.stdout), nil
}

func finishOpenCodeSmokeRoot(
	ownership *openCodeSmokeRootOwnership,
	lockedBinary io.Closer,
	quiescent bool,
	prior error,
) error {
	if ownership == nil || ownership.lock == nil {
		return errors.Join(prior, ErrOpenCodeCleanupFailed)
	}
	if !quiescent {
		return errors.Join(prior, closeOpenCodeSmokeOwnership(ownership, lockedBinary), ErrOpenCodeCleanupFailed)
	}
	auditErr := auditOpenCodeSmokeRoot(ownership)
	var binaryCloseErr error
	if lockedBinary != nil {
		binaryCloseErr = lockedBinary.Close()
	}
	contentsErr := removeOpenCodeSmokeRootContents(ownership)
	var markErr error
	if contentsErr == nil {
		markErr = ownership.lock.remove()
	}
	lockCloseErr := ownership.lock.Close()
	_, pathErr := os.Lstat(ownership.path)
	rootAbsent := os.IsNotExist(pathErr)
	if binaryCloseErr != nil || contentsErr != nil || markErr != nil || lockCloseErr != nil || !rootAbsent {
		return errors.Join(prior, auditErr, ErrOpenCodeCleanupFailed)
	}
	return errors.Join(prior, auditErr)
}

func auditOpenCodeSmokeRoot(ownership *openCodeSmokeRootOwnership) error {
	if ownership == nil || ownership.lock == nil || ownership.candidate == "" ||
		filepath.Dir(ownership.candidate) != ownership.path {
		return ErrOpenCodeUnsafeArtifact
	}
	entries, err := ownership.lock.entries()
	if err != nil || len(entries) != 1 || entries[0] != filepath.Base(ownership.candidate) {
		return ErrOpenCodeUnsafeArtifact
	}
	info, err := os.Lstat(ownership.candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > openCodeSmokeCandidateSizeLimit ||
		!openCodePlatformBinaryPathSafe(ownership.candidate) {
		return ErrOpenCodeUnsafeArtifact
	}
	return nil
}

func removeOpenCodeSmokeRootContents(ownership *openCodeSmokeRootOwnership) error {
	if ownership == nil || ownership.lock == nil {
		return ErrOpenCodeCleanupFailed
	}
	entries, err := ownership.lock.entries()
	if err != nil {
		return ErrOpenCodeCleanupFailed
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(ownership.path, entry)); err != nil {
			return ErrOpenCodeCleanupFailed
		}
	}
	remaining, err := ownership.lock.entries()
	if err != nil || len(remaining) != 0 {
		return ErrOpenCodeCleanupFailed
	}
	return nil
}

func closeOpenCodeSmokeOwnership(ownership *openCodeSmokeRootOwnership, lockedBinary io.Closer) error {
	var binaryCloseErr error
	if lockedBinary != nil {
		binaryCloseErr = lockedBinary.Close()
	}
	lockCloseErr := ownership.lock.Close()
	if binaryCloseErr != nil || lockCloseErr != nil {
		return ErrOpenCodeCleanupFailed
	}
	return nil
}

func sanitizeOpenCodeSmokeGateError(err error, category string) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrOpenCodeContainmentUnavailable):
		return ErrOpenCodeContainmentUnavailable
	case errors.Is(err, ErrOpenCodeCleanupFailed):
		return ErrOpenCodeCleanupFailed
	default:
		return openCodeBinaryRejectedError(category)
	}
}

func TestOpenCodeLocalSmoke(t *testing.T) {
	binary := os.Getenv("AGENTDC_OPENCODE_SPIKE_BINARY")
	if binary == "" {
		t.Skip("explicit synthetic spike binary not supplied")
	}
	got, err := runVerifiedLocalOpenCodeSmoke(t.Context(), binary)
	if err != nil {
		t.Fatal(err)
	}
	if got != "OK" {
		t.Fatalf("answer = %q", got)
	}
}

func TestOpenCodeLocalSmokeGateRejectsUnsafePathBeforeVerification(t *testing.T) {
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	for _, path := range []string{"", "opencode.exe", filepath.Clean(t.TempDir()) + string(filepath.Separator) + "missing.exe"} {
		_, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), path, deps)
		if !errors.Is(err, ErrOpenCodeBinaryRejected) {
			t.Fatalf("path %q: error = %v; want binary rejected", path, err)
		}
	}
	if calls != (openCodeSmokeGateCalls{}) {
		t.Fatalf("unsafe paths reached verification or execution: %+v", calls)
	}
}

func TestOpenCodeLocalSmokeGateRejectsDirectoryAndSymlinkBeforeVerification(t *testing.T) {
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	directory := t.TempDir()
	if _, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), directory, deps); !errors.Is(err, ErrOpenCodeBinaryRejected) {
		t.Fatalf("directory error = %v; want binary rejected", err)
	}

	target := writeOpenCodeSmokeFixture(t)
	link := filepath.Join(t.TempDir(), "opencode-link.exe")
	if err := os.Symlink(target, link); err != nil {
		t.Logf("symlink creation unavailable: %v", err)
	} else if _, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), link, deps); !errors.Is(err, ErrOpenCodeBinaryRejected) {
		t.Fatalf("symlink error = %v; want binary rejected", err)
	}
	if calls != (openCodeSmokeGateCalls{}) {
		t.Fatalf("unsafe file type reached verification or execution: %+v", calls)
	}
}

func TestOpenCodeLocalSmokeGateStopsCanceledContextBeforeCopyOrVerification(t *testing.T) {
	binary := writeOpenCodeSmokeFixture(t)
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := runVerifiedLocalOpenCodeSmokeWithDependencies(ctx, binary, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want canceled", err)
	}
	if calls != (openCodeSmokeGateCalls{}) {
		t.Fatalf("canceled gate reached verification or execution: %+v", calls)
	}
}

func TestOpenCodeLocalSmokeGateRejectsHashBeforeTrustOrProcess(t *testing.T) {
	binary := writeOpenCodeSmokeFixture(t)
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	deps.hashFile = func(string) ([32]byte, error) {
		calls.hash++
		return [32]byte{1}, nil
	}

	_, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), binary, deps)
	if !errors.Is(err, ErrOpenCodeBinaryRejected) {
		t.Fatalf("error = %v; want binary rejected", err)
	}
	if calls != (openCodeSmokeGateCalls{lock: 1, hash: 1}) {
		t.Fatalf("hash mismatch reached trust or process: %+v", calls)
	}
}

func TestOpenCodeLocalSmokeGateRejectsSignatureBeforeAnyProcess(t *testing.T) {
	binary := writeOpenCodeSmokeFixture(t)
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	deps.verifySignature = func(string) error {
		calls.signature++
		return ErrOpenCodeBinaryRejected
	}

	_, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), binary, deps)
	if !errors.Is(err, ErrOpenCodeBinaryRejected) {
		t.Fatalf("error = %v; want binary rejected", err)
	}
	want := openCodeSmokeGateCalls{lock: 1, hash: 1, signature: 1}
	if calls != want {
		t.Fatalf("signature mismatch reached a process: %+v; want %+v", calls, want)
	}
}

func TestOpenCodeLocalSmokeGateRejectsVersionBeforeSyntheticRun(t *testing.T) {
	binary := writeOpenCodeSmokeFixture(t)
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	deps.version = func(context.Context, string) (string, error) {
		calls.version++
		return "1.18.17\n", nil
	}

	_, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), binary, deps)
	if !errors.Is(err, ErrOpenCodeBinaryRejected) {
		t.Fatalf("error = %v; want binary rejected", err)
	}
	want := openCodeSmokeGateCalls{lock: 1, hash: 1, signature: 1, version: 1}
	if calls != want {
		t.Fatalf("version mismatch reached synthetic run: %+v; want %+v", calls, want)
	}
}

func TestOpenCodeLocalSmokeGateAcceptsOnlyPinnedBinary(t *testing.T) {
	binary := writeOpenCodeSmokeFixture(t)
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)

	got, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), binary, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got != "OK" {
		t.Fatalf("answer = %q", got)
	}
	want := openCodeSmokeGateCalls{lock: 1, hash: 1, signature: 1, version: 1, run: 1}
	if calls != want {
		t.Fatalf("gate calls = %+v; want %+v", calls, want)
	}
}

func TestOpenCodeLocalSmokeGateVerifiesAndExecutesOnlyOwnedCopy(t *testing.T) {
	binary := writeOpenCodeSmokeFixture(t)
	calls := openCodeSmokeGateCalls{}
	deps := successfulOpenCodeSmokeDependencies(&calls)
	var lockedPath, hashedPath, signedPath, versionPath, runPath string
	deps.lockBinary = func(path string) (io.Closer, error) {
		calls.lock++
		lockedPath = path
		return io.NopCloser(strings.NewReader("")), nil
	}
	deps.hashFile = func(path string) ([32]byte, error) {
		calls.hash++
		hashedPath = path
		return openCodePinnedSHA256, nil
	}
	deps.verifySignature = func(path string) error {
		calls.signature++
		signedPath = path
		return nil
	}
	deps.version = func(_ context.Context, path string) (string, error) {
		calls.version++
		versionPath = path
		return openCodePinnedVersion, nil
	}
	deps.run = func(_ context.Context, _ openCodeSyntheticCapability, path string) (string, error) {
		calls.run++
		runPath = path
		return "OK", nil
	}

	if _, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), binary, deps); err != nil {
		t.Fatal(err)
	}
	if lockedPath == binary || lockedPath == "" {
		t.Fatalf("operator path was used directly: source=%q locked=%q", binary, lockedPath)
	}
	for name, path := range map[string]string{
		"hash": hashedPath, "signature": signedPath, "version": versionPath, "run": runPath,
	} {
		if path != lockedPath {
			t.Fatalf("%s path = %q; want owned copy %q", name, path, lockedPath)
		}
	}
	if _, err := os.Stat(filepath.Dir(lockedPath)); !os.IsNotExist(err) {
		t.Fatalf("owned smoke root remained after success: %v", err)
	}
}

func TestOpenCodeLocalSmokeVersionProbeUsesManagedSpecWithExactEnvironment(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "opencode.exe")
	spec, err := newOpenCodeVersionSpec(root, binary)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Args, []string{"--version"}) || spec.Stdin != "" {
		t.Fatalf("version probe args/stdin = %#v/%q", spec.Args, spec.Stdin)
	}
	command := newOpenCodeCommand(spec)
	if !slices.Equal(command.Environ(), spec.Env) {
		t.Fatalf("command environment drifted: got=%q want=%q", command.Environ(), spec.Env)
	}
	if command.Dir != filepath.Join(root, "work") {
		t.Fatalf("command dir = %q", command.Dir)
	}
	if strings.Contains(strings.Join(command.Args, "\x00"), syntheticOpenCodePrompt) {
		t.Fatal("synthetic prompt leaked into version probe")
	}
}

func TestOpenCodeLocalSmokeGateRefusesCleanupWhenOwnedRootIdentityChanged(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "agentdc-opencode-smoke-replaced")
	if err := os.WriteFile(root, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOpenCodeSmokeRoot(root)
	if !errors.Is(err, ErrOpenCodeCleanupFailed) {
		t.Fatalf("error = %v; want cleanup failed", err)
	}
	if lock != nil {
		_ = lock.Close()
		t.Fatal("replacement file received a directory ownership lock")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("replaced root was removed: %v", err)
	}
}

func TestOpenCodeLocalSmokeGateRejectsAndRemovesUnexpectedOuterArtifacts(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		payload  string
	}{
		{name: "database", filename: "opencode.db", payload: "sqlite"},
		{name: "wal", filename: "opencode.db-wal", payload: "wal"},
		{name: "prompt", filename: "bootstrap.log", payload: syntheticOpenCodePrompt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := writeOpenCodeSmokeFixture(t)
			calls := openCodeSmokeGateCalls{}
			deps := successfulOpenCodeSmokeDependencies(&calls)
			var ownedRoot string
			deps.run = func(_ context.Context, capability openCodeSyntheticCapability, _ string) (string, error) {
				calls.run++
				ownedRoot = capability.rootBase
				if err := os.WriteFile(filepath.Join(ownedRoot, test.filename), []byte(test.payload), 0o600); err != nil {
					t.Fatal(err)
				}
				return "OK", nil
			}

			_, err := runVerifiedLocalOpenCodeSmokeWithDependencies(t.Context(), binary, deps)
			if !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
				t.Fatalf("error = %v; want unsafe artifact", err)
			}
			if ownedRoot == "" {
				t.Fatal("synthetic runner did not receive an owned root")
			}
			if _, statErr := os.Stat(ownedRoot); !os.IsNotExist(statErr) {
				t.Fatalf("unsafe outer root remained: %v", statErr)
			}
		})
	}
}

func TestOpenCodeLocalSmokeGateDetectsRootReplacementAfterHandleClose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentdc-opencode-smoke-close-race")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "opencode-"+openCodePinnedVersion+".exe")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o500); err != nil {
		t.Fatal(err)
	}
	lock := &replacingOpenCodeSmokeRootLock{path: root}
	ownership := &openCodeSmokeRootOwnership{path: root, lock: lock, candidate: candidate}

	err := finishOpenCodeSmokeRoot(
		ownership,
		io.NopCloser(strings.NewReader("")),
		true,
		nil,
	)
	if !errors.Is(err, ErrOpenCodeCleanupFailed) {
		t.Fatalf("error = %v; want cleanup failed", err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		t.Fatalf("replacement directory was not preserved: %v", statErr)
	}
}

type replacingOpenCodeSmokeRootLock struct {
	path string
}

func (lock *replacingOpenCodeSmokeRootLock) entries() ([]string, error) {
	entries, err := os.ReadDir(lock.path)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Name()
	}
	return result, nil
}

func (*replacingOpenCodeSmokeRootLock) remove() error {
	return nil
}

func (lock *replacingOpenCodeSmokeRootLock) Close() error {
	if err := os.Remove(lock.path); err != nil {
		return err
	}
	return os.Mkdir(lock.path, 0o700)
}

type openCodeSmokeGateCalls struct {
	lock      int
	hash      int
	signature int
	version   int
	run       int
}

func successfulOpenCodeSmokeDependencies(calls *openCodeSmokeGateCalls) openCodeLocalSmokeDependencies {
	return openCodeLocalSmokeDependencies{
		lockBinary: func(string) (io.Closer, error) {
			calls.lock++
			return io.NopCloser(strings.NewReader("")), nil
		},
		hashFile: func(string) ([32]byte, error) {
			calls.hash++
			return openCodePinnedSHA256, nil
		},
		verifySignature: func(string) error {
			calls.signature++
			return nil
		},
		version: func(context.Context, string) (string, error) {
			calls.version++
			return openCodePinnedVersion + "\n", nil
		},
		run: func(context.Context, openCodeSyntheticCapability, string) (string, error) {
			calls.run++
			return "OK", nil
		},
	}
}

func writeOpenCodeSmokeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.exe")
	if err := os.WriteFile(path, []byte("not executed"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
