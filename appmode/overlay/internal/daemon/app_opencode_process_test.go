package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenCodeSpikeBoundedDrainContinuesAfterCap(t *testing.T) {
	drain := newOpenCodeBoundedDrain(openCodeOutputLimit)
	payload := bytes.Repeat([]byte("x"), openCodeOutputLimit+257)

	n, err := drain.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("first drain write = %d, %v; want %d, nil", n, err, len(payload))
	}
	n, err = drain.Write([]byte("still-draining"))
	if err != nil || n != len("still-draining") {
		t.Fatalf("post-cap drain write = %d, %v; want %d, nil", n, err, len("still-draining"))
	}
	if !drain.overflowed() {
		t.Fatal("bounded drain did not report overflow")
	}
	if got := len(drain.bytes()); got != openCodeOutputLimit {
		t.Fatalf("retained bytes = %d; want %d", got, openCodeOutputLimit)
	}
}

func TestOpenCodeSpikeCommandUsesOnlyReviewedSpec(t *testing.T) {
	root := t.TempDir()
	spec, err := newOpenCodeSpikeSpec(root, filepath.Join(root, "opencode.exe"), openCodeTestModel)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "ambient-secret")
	t.Setenv("HTTPS_PROXY", "http://ambient.invalid")
	cmd := newOpenCodeCommand(spec)
	if !slices.Equal(cmd.Args[1:], spec.Args) || cmd.Path != spec.Binary || cmd.Dir != spec.WorkDir {
		t.Fatalf("command = path %q dir %q args %#v", cmd.Path, cmd.Dir, cmd.Args)
	}
	if !slices.Equal(cmd.Environ(), spec.Env) {
		t.Fatalf("command env = %#v; want %#v", cmd.Environ(), spec.Env)
	}
	if cmd.Cancel != nil {
		t.Fatal("command unexpectedly delegates lifetime to exec.CommandContext")
	}
	stdin, err := io.ReadAll(cmd.Stdin)
	if err != nil || string(stdin) != syntheticOpenCodePrompt {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
	if strings.Contains(strings.Join(cmd.Args, "\x00"), syntheticOpenCodePrompt) {
		t.Fatal("prompt leaked into process argv")
	}
}

func TestOpenCodeSpikeArtifactAuditAcceptsOnlyBoundedOwnedRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "safe.log"), []byte("category-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := auditOpenCodeOwnedRoot(root, [][]byte{[]byte(syntheticOpenCodePrompt), []byte("ses_secret")}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeSpikeArtifactAuditRejectsUnsafeEntries(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"database": func(t *testing.T, root string) {
			t.Helper()
			mustWriteOpenCodeTestFile(t, filepath.Join(root, "data", "opencode.db-wal"), []byte("x"))
		},
		"database journal": func(t *testing.T, root string) {
			t.Helper()
			mustWriteOpenCodeTestFile(t, filepath.Join(root, "data", "opencode.db-journal"), []byte("x"))
		},
		"prompt leak": func(t *testing.T, root string) {
			t.Helper()
			mustWriteOpenCodeTestFile(t, filepath.Join(root, "log", "opencode.log"), []byte("prefix "+syntheticOpenCodePrompt+" suffix"))
		},
		"file cap": func(t *testing.T, root string) {
			t.Helper()
			path := filepath.Join(root, "large.bin")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate((1 << 20) + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"depth cap": func(t *testing.T, root string) {
			t.Helper()
			path := root
			for i := 0; i < 17; i++ {
				path = filepath.Join(path, fmt.Sprintf("d%02d", i))
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"entry cap": func(t *testing.T, root string) {
			t.Helper()
			for i := 0; i <= openCodeArtifactEntryLimit; i++ {
				mustWriteOpenCodeTestFile(t, filepath.Join(root, "entries", fmt.Sprintf("%04d", i)), nil)
			}
		},
		"aggregate cap": func(t *testing.T, root string) {
			t.Helper()
			for i := 0; i <= openCodeArtifactAggregateLimit/openCodeArtifactFileLimit; i++ {
				path := filepath.Join(root, "aggregate", fmt.Sprintf("%02d", i))
				mustWriteOpenCodeTestFile(t, path, nil)
				if err := os.Truncate(path, openCodeArtifactFileLimit); err != nil {
					t.Fatal(err)
				}
			}
		},
		"symlink": func(t *testing.T, root string) {
			t.Helper()
			target := filepath.Join(root, "target")
			mustWriteOpenCodeTestFile(t, target, []byte("x"))
			if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			prepare(t, root)
			err := auditOpenCodeOwnedRoot(root, [][]byte{[]byte(syntheticOpenCodePrompt)})
			if !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
				t.Fatalf("audit error = %v; want unsafe artifact", err)
			}
		})
	}
}

func TestOpenCodeSpikeArtifactAuditReadsDirectoriesInBoundedBatches(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= openCodeArtifactEntryLimit; i++ {
		mustWriteOpenCodeTestFile(t, filepath.Join(root, "entries", fmt.Sprintf("%04d", i)), nil)
	}
	original := openCodeReadDirBatch
	requests := make([]int, 0)
	openCodeReadDirBatch = func(directory *os.File, count int) ([]os.DirEntry, error) {
		requests = append(requests, count)
		return original(directory, count)
	}
	t.Cleanup(func() { openCodeReadDirBatch = original })

	err := auditOpenCodeOwnedRoot(root, nil)

	if !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
		t.Fatalf("entry overflow error = %v; want unsafe artifact", err)
	}
	if len(requests) == 0 {
		t.Fatal("artifact audit made no bounded directory reads")
	}
	for _, count := range requests {
		if count <= 0 || count > openCodeArtifactReadBatchSize {
			t.Fatalf("directory read batch = %d; want 1..%d", count, openCodeArtifactReadBatchSize)
		}
	}
}

func TestOpenCodeSpikeArtifactCleanupRefusesBroadOrForeignTargets(t *testing.T) {
	base := t.TempDir()
	sibling := filepath.Join(base, "foreign")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{base, sibling, filepath.Dir(base)} {
		if err := auditAndRemoveOpenCodeRoot(base, target, nil); !errors.Is(err, ErrOpenCodeCleanupFailed) {
			t.Fatalf("cleanup %q error = %v; want cleanup failure", target, err)
		}
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("foreign target was changed: %v", err)
	}
}

func TestOpenCodeSpikeArtifactCleanupRemovesExactRootOnAuditFailure(t *testing.T) {
	base := t.TempDir()
	root, err := os.MkdirTemp(base, "agentdc-opencode-spike-")
	if err != nil {
		t.Fatal(err)
	}
	mustWriteOpenCodeTestFile(t, filepath.Join(root, "opencode.db"), []byte("x"))
	err = auditAndRemoveOpenCodeRoot(base, root, nil)
	if !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
		t.Fatalf("cleanup error = %v; want unsafe artifact", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("owned root survived cleanup: %v", statErr)
	}
}

func TestRunOpenCodeSyntheticSpikeDiscoversRunsAndRemovesRoot(t *testing.T) {
	base := t.TempDir()
	var roots []string
	var calls [][]string
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
		execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
			root := filepath.Dir(cmd.Dir)
			roots = append(roots, root)
			calls = append(calls, append([]string(nil), cmd.Args[1:]...))
			if slices.Contains(cmd.Args, "models") {
				return openCodeProcessOutcome{stdout: []byte("opencode/mimo-v2.5-free\n"), quiescent: true}
			}
			mustWriteOpenCodeTestFile(t, filepath.Join(root, "data", "opencode", "log", "opencode.log"), []byte("category-only"))
			return openCodeProcessOutcome{stdout: []byte(validOpenCodeStream("ses_owned", "OK")), quiescent: true}
		},
	}
	answer, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))
	if err != nil || answer != "OK" {
		t.Fatalf("answer = %q, %v", answer, err)
	}
	if len(calls) != 2 || len(roots) != 2 || roots[0] != roots[1] {
		t.Fatalf("calls/roots = %#v / %#v", calls, roots)
	}
	joined := strings.Join(append(calls[0], calls[1]...), "\x00")
	if strings.Contains(joined, syntheticOpenCodePrompt) || strings.Contains(joined, "session") || !strings.Contains(joined, "--title") {
		t.Fatalf("unsafe or incomplete argv = %q", joined)
	}
	if _, statErr := os.Stat(roots[0]); !os.IsNotExist(statErr) {
		t.Fatalf("owned root survived success: %v", statErr)
	}
}

func TestRunOpenCodeSyntheticSpikeSanitizesFailureAndCleans(t *testing.T) {
	base := t.TempDir()
	var root string
	calls := 0
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x31}, 16)),
		execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
			calls++
			root = filepath.Dir(cmd.Dir)
			if calls == 1 {
				return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
			}
			return openCodeProcessOutcome{stderr: []byte("RAW_PROCESS_SECRET"), quiescent: true, err: errors.New("RAW_PROCESS_SECRET")}
		},
	}
	_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))
	if !errors.Is(err, ErrOpenCodeProcessFailed) || strings.Contains(err.Error(), "RAW_PROCESS_SECRET") {
		t.Fatalf("failure was not sanitized: %v", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("owned root survived process failure: %v", statErr)
	}
}

func TestRunOpenCodeSyntheticSpikeRejectsArtifactAndStillRemovesRoot(t *testing.T) {
	base := t.TempDir()
	var root string
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x41}, 16)),
		execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
			root = filepath.Dir(cmd.Dir)
			if slices.Contains(cmd.Args, "models") {
				return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
			}
			mustWriteOpenCodeTestFile(t, filepath.Join(root, "data", "opencode.db-shm"), []byte("x"))
			return openCodeProcessOutcome{stdout: []byte(validOpenCodeStream("ses_artifact", "OK")), quiescent: true}
		},
	}
	_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))
	if !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
		t.Fatalf("artifact error = %v", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("owned root survived unsafe artifact: %v", statErr)
	}
}

func TestRunOpenCodeSyntheticSpikeKeepsRootWhenQuiescenceIsUnproven(t *testing.T) {
	base := t.TempDir()
	var root string
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x51}, 16)),
		execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
			root = filepath.Dir(cmd.Dir)
			return openCodeProcessOutcome{quiescent: false, err: errors.New("query failed")}
		},
	}
	_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))
	if !errors.Is(err, ErrOpenCodeCleanupFailed) {
		t.Fatalf("cleanup error = %v", err)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("unproven root was removed: %v", statErr)
	}
	if removeErr := os.RemoveAll(root); removeErr != nil {
		t.Fatal(removeErr)
	}
}

func TestRunOpenCodeSyntheticSpikeFailurePathsAuditAndRemove(t *testing.T) {
	tests := []struct {
		name       string
		outcome    openCodeProcessOutcome
		want       error
		artifact   string
		wantUnsafe bool
	}{
		{name: "cancel before first event", outcome: openCodeProcessOutcome{quiescent: true, err: context.Canceled}, want: context.Canceled},
		{name: "empty result", outcome: openCodeProcessOutcome{quiescent: true}, want: ErrOpenCodeInvalidProtocol},
		{name: "malformed result", outcome: openCodeProcessOutcome{stdout: []byte(`{"sessionID":"ses_malformed"}`), quiescent: true}, want: ErrOpenCodeInvalidProtocol, artifact: "ses_malformed", wantUnsafe: true},
		{name: "unexpected answer", outcome: openCodeProcessOutcome{stdout: []byte(validOpenCodeStream("ses_wrong", "NO")), quiescent: true}, want: ErrOpenCodeUnexpectedAnswer},
		{name: "cancel after session event", outcome: openCodeProcessOutcome{stdout: []byte(openCodeStepStart("ses_cancelled", "msg_1", "prt_1", 1, "") + "\n"), quiescent: true, err: context.Canceled}, want: context.Canceled, artifact: "ses_cancelled", wantUnsafe: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			var root string
			calls := 0
			capability := openCodeSyntheticCapability{
				rootBase: base,
				nonce:    bytes.NewReader(bytes.Repeat([]byte{0x61}, 16)),
				execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
					calls++
					root = filepath.Dir(cmd.Dir)
					if calls == 1 {
						return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
					}
					if test.artifact != "" {
						mustWriteOpenCodeTestFile(t, filepath.Join(root, "data", "opencode", "log", "opencode.log"), []byte(test.artifact))
					}
					return test.outcome
				},
			}
			_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
			if got := errors.Is(err, ErrOpenCodeUnsafeArtifact); got != test.wantUnsafe {
				t.Fatalf("unsafe artifact = %v; want %v (error %v)", got, test.wantUnsafe, err)
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("owned root survived failure: %v", statErr)
			}
		})
	}
}

func TestRunOpenCodeSyntheticSpikeDoesNotStartRunAfterCancellation(t *testing.T) {
	base := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x71}, 16)),
		execute: func(_ context.Context, _ *exec.Cmd) openCodeProcessOutcome {
			calls++
			cancel()
			return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
		},
	}

	_, err := runOpenCodeSyntheticSpike(ctx, capability, filepath.Join(base, "opencode.exe"))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context canceled", err)
	}
	if calls != 1 {
		t.Fatalf("process calls = %d; want discovery only", calls)
	}
	entries, readErr := os.ReadDir(base)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("owned base after cancel = %v, %v; want empty", entries, readErr)
	}
}

func TestRunOpenCodeSyntheticSpikeMissingBinaryRemovesFreshRoot(t *testing.T) {
	base := t.TempDir()
	_, err := runOpenCodeSyntheticSpike(t.Context(), openCodeSyntheticCapability{rootBase: base}, filepath.Join(base, "missing-opencode.exe"))
	if !errors.Is(err, ErrOpenCodeProcessFailed) {
		t.Fatalf("missing binary error = %v; want process failure", err)
	}
	entries, readErr := os.ReadDir(base)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("owned base after start failure = %v, %v; want empty", entries, readErr)
	}
}

func TestRunOpenCodeSyntheticSpikeUnsupportedPlatformDoesNotSpawnOrRetainRoot(t *testing.T) {
	original := openCodeManagedQuiescenceAvailable
	openCodeManagedQuiescenceAvailable = func() bool { return false }
	t.Cleanup(func() { openCodeManagedQuiescenceAvailable = original })
	base := t.TempDir()

	_, err := runOpenCodeSyntheticSpike(t.Context(), openCodeSyntheticCapability{rootBase: base}, filepath.Join(base, "missing-opencode.exe"))

	if !errors.Is(err, ErrOpenCodeContainmentUnavailable) {
		t.Fatalf("unsupported error = %v; want containment unavailable", err)
	}
	entries, readErr := os.ReadDir(base)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("owned base after unsupported preflight = %v, %v; want empty", entries, readErr)
	}
}

func TestRunOpenCodeSyntheticSpikeBoundsSessionLeakCandidates(t *testing.T) {
	base := t.TempDir()
	var output strings.Builder
	for i := 0; i <= openCodeSessionCandidateLimit; i++ {
		fmt.Fprintf(&output, "ses_candidate_%02d ", i)
	}
	calls := 0
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x73}, 16)),
		execute: func(_ context.Context, _ *exec.Cmd) openCodeProcessOutcome {
			calls++
			if calls == 1 {
				return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
			}
			return openCodeProcessOutcome{stdout: []byte(output.String()), quiescent: true}
		},
	}

	_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))

	if !errors.Is(err, ErrOpenCodeOutputTooLarge) {
		t.Fatalf("candidate overflow error = %v; want output too large", err)
	}
	entries, readErr := os.ReadDir(base)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("owned base after candidate overflow = %v, %v; want empty", entries, readErr)
	}
}

func TestOpenCodeSessionCandidateScanHasBoundedAllocations(t *testing.T) {
	var payload strings.Builder
	payload.WriteByte('{')
	for i := 0; i < openCodeJSONTokenLimit; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(&payload, `"field_%04d":0`, i)
	}
	payload.WriteByte('}')
	raw := []byte(payload.String())
	var scanErr error
	allocations := testing.AllocsPerRun(3, func() {
		_, scanErr = openCodeSessionCandidates(raw)
	})
	if scanErr != nil {
		t.Fatalf("candidate scan error = %v", scanErr)
	}
	if allocations > 128 {
		t.Fatalf("candidate scan allocations = %.0f; want <= 128", allocations)
	}
}

func TestOpenCodeSessionCandidateRepeatedMatchAllocationsAreBounded(t *testing.T) {
	raw := bytes.Repeat([]byte(`"ses_repeat" `), 20_000)
	var scanErr error
	allocations := testing.AllocsPerRun(3, func() {
		_, scanErr = openCodeSessionCandidates(raw)
	})
	if scanErr != nil {
		t.Fatalf("repeated candidate scan error = %v", scanErr)
	}
	if allocations > 128 {
		t.Fatalf("repeated candidate scan allocations = %.0f; want <= 128", allocations)
	}
}

func TestOpenCodeSessionCandidateScanRejectsMalformedEscapedStrings(t *testing.T) {
	tests := map[string]string{
		"unterminated escaped quotes": `"` + strings.Repeat(`\"`, 256),
		"invalid JSON escape":         `{"sessionID":"\q\u0073es_secret"}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := openCodeSessionCandidates([]byte(payload))
			if !errors.Is(err, ErrOpenCodeInvalidProtocol) {
				t.Fatalf("candidate scan error = %v; want invalid protocol", err)
			}
		})
	}
}

func TestOpenCodeSessionCandidateScanAcceptsJSONEscapedSlash(t *testing.T) {
	candidates, err := openCodeSessionCandidates([]byte(`{"path":"a\/b","sessionID":"\u0073es_slash"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(candidates, func(candidate []byte) bool { return string(candidate) == "ses_slash" }) {
		t.Fatalf("candidates = %q; want decoded session", candidates)
	}
}

func TestRunOpenCodeSyntheticSpikeAuditsDecodedEscapedSessionOnCancel(t *testing.T) {
	base := t.TempDir()
	const decodedSession = "ses_escaped_cancel"
	escaped := strings.ReplaceAll(openCodeStepStart(decodedSession, "msg_1", "prt_1", 1, ""), decodedSession, `\u0073es_escaped_cancel`)
	calls := 0
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x74}, 16)),
		execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
			calls++
			if calls == 1 {
				return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
			}
			root := filepath.Dir(cmd.Dir)
			mustWriteOpenCodeTestFile(t, filepath.Join(root, "data", "opencode", "log", "opencode.log"), []byte(decodedSession))
			return openCodeProcessOutcome{stdout: []byte(escaped + "\n"), quiescent: true, err: context.Canceled}
		},
	}

	_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))

	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
		t.Fatalf("escaped-session cancel error = %v; want cancel + unsafe artifact", err)
	}
	entries, readErr := os.ReadDir(base)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("owned base after escaped-session cancel = %v, %v; want empty", entries, readErr)
	}
}

func TestRunOpenCodeSyntheticSpikeRejectsDiscoveryWorkspaceMutation(t *testing.T) {
	base := t.TempDir()
	var root string
	capability := openCodeSyntheticCapability{
		rootBase: base,
		nonce:    bytes.NewReader(bytes.Repeat([]byte{0x72}, 16)),
		execute: func(_ context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
			root = filepath.Dir(cmd.Dir)
			mustWriteOpenCodeTestFile(t, filepath.Join(cmd.Dir, "project-config.json"), []byte("unexpected"))
			return openCodeProcessOutcome{stdout: []byte(openCodeTestModel + "\n"), quiescent: true}
		},
	}

	_, err := runOpenCodeSyntheticSpike(t.Context(), capability, filepath.Join(base, "opencode.exe"))

	if !errors.Is(err, ErrOpenCodeUnsafeArtifact) {
		t.Fatalf("workspace mutation error = %v; want unsafe artifact", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("owned root survived workspace mutation: %v", statErr)
	}
}

func TestOpenCodeSpikeManagedCommandDrainsOverflowAndReaps(t *testing.T) {
	cmd := openCodeHelperCommand(t, "noisy")
	outcome := runOpenCodeManagedCommand(t.Context(), cmd)
	if !errors.Is(outcome.err, ErrOpenCodeOutputTooLarge) || !outcome.quiescent {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(outcome.stdout) > openCodeOutputLimit || len(outcome.stderr) > openCodeOutputLimit ||
		len(outcome.stdout) != openCodeOutputLimit && len(outcome.stderr) != openCodeOutputLimit {
		t.Fatalf("retained stdout/stderr = %d/%d; want both bounded and at least one capped", len(outcome.stdout), len(outcome.stderr))
	}
}

func TestOpenCodeSpikeManagedCommandCancelReaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	outcome := runOpenCodeManagedCommand(ctx, openCodeHelperCommand(t, "block"))
	if !errors.Is(outcome.err, context.DeadlineExceeded) || !outcome.quiescent {
		t.Fatalf("cancel outcome = %#v", outcome)
	}
}

func TestOpenCodeSpikeManagedCommandPreCanceledContextWinsBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := runOpenCodeManagedCommand(ctx, exec.Command(filepath.Join(t.TempDir(), "missing.exe")))
	if !errors.Is(outcome.err, context.Canceled) || !outcome.quiescent {
		t.Fatalf("pre-canceled outcome = %#v", outcome)
	}
}

func TestOpenCodeSpikeManagedCommandClassifiesOfflineBootstrapWithoutRawError(t *testing.T) {
	outcome := runOpenCodeManagedCommand(t.Context(), openCodeHelperCommand(t, "offline"))
	if !errors.Is(outcome.err, ErrOpenCodeOfflineBootstrap) || !outcome.quiescent {
		t.Fatalf("offline outcome = %#v", outcome)
	}
	if strings.Contains(outcome.err.Error(), "RAW_OFFLINE_SECRET") {
		t.Fatalf("offline error leaked stderr: %v", outcome.err)
	}
}

func TestOpenCodeSpikeProcessHelper(t *testing.T) {
	if os.Getenv("AGENTDC_OPENCODE_HELPER") != "1" {
		return
	}
	switch os.Getenv("AGENTDC_OPENCODE_HELPER_MODE") {
	case "noisy":
		payload := bytes.Repeat([]byte("x"), openCodeOutputLimit+4096)
		var group sync.WaitGroup
		group.Add(2)
		go func() { defer group.Done(); _, _ = os.Stdout.Write(payload) }()
		go func() { defer group.Done(); _, _ = os.Stderr.Write(payload) }()
		group.Wait()
		os.Exit(0)
	case "block":
		_, _ = fmt.Fprint(os.Stdout, "started")
		for {
			time.Sleep(time.Hour)
		}
	case "offline":
		_, _ = fmt.Fprint(os.Stderr, "npm ERR! code ENOTCACHED RAW_OFFLINE_SECRET")
		os.Exit(17)
	default:
		os.Exit(2)
	}
}

func openCodeHelperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenCodeSpikeProcessHelper$")
	cmd.Env = append(os.Environ(), "AGENTDC_OPENCODE_HELPER=1", "AGENTDC_OPENCODE_HELPER_MODE="+mode)
	return cmd
}

func mustWriteOpenCodeTestFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
