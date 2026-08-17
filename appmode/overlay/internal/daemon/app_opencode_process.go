package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	openCodeArtifactDepthLimit     = 16
	openCodeArtifactEntryLimit     = 4096
	openCodeArtifactFileLimit      = 1 << 20
	openCodeArtifactAggregateLimit = 16 << 20
	openCodeArtifactReadBatchSize  = 64
	openCodeSessionCandidateLimit  = 16
	openCodeCleanupTimeout         = 5 * time.Second
)

var (
	ErrOpenCodeProcessFailed    = errors.New("OpenCode synthetic process failed")
	ErrOpenCodeOfflineBootstrap = errors.New("OpenCode offline bootstrap unavailable")
	ErrOpenCodeCleanupFailed    = errors.New("OpenCode synthetic cleanup failed")
	ErrOpenCodeUnsafeArtifact   = errors.New("unsafe OpenCode artifact")
	ErrOpenCodeUnexpectedAnswer = errors.New("unexpected OpenCode synthetic answer")

	openCodeManagedQuiescenceAvailable = openCodeManagedQuiescenceSupported
	openCodeReadDirBatch               = func(directory *os.File, count int) ([]os.DirEntry, error) {
		return directory.ReadDir(count)
	}
)

// managedCLIProcessQuiescer is stronger than the lifetime contract used by
// ordinary providers: it proves the owned process tree has no active members.
// The synthetic spike must have this proof before inspecting or deleting files.
type managedCLIProcessQuiescer interface {
	managedCLIProcessLifetime
	quiesce(context.Context) error
}

type openCodeProcessOutcome struct {
	stdout    []byte
	stderr    []byte
	quiescent bool
	err       error
}

type openCodeSyntheticCapability struct {
	rootBase string
	nonce    io.Reader
	execute  func(context.Context, *exec.Cmd) openCodeProcessOutcome
}

type openCodeBoundedDrain struct {
	mu         sync.Mutex
	limit      int
	buffer     []byte
	overflow   bool
	overflowCh chan struct{}
	once       sync.Once
}

func newOpenCodeBoundedDrain(limit int) *openCodeBoundedDrain {
	if limit < 0 {
		limit = 0
	}
	return &openCodeBoundedDrain{
		limit:      limit,
		buffer:     make([]byte, 0, limit),
		overflowCh: make(chan struct{}),
	}
}

// Write deliberately reports the whole write after the retention cap. This
// keeps draining the child pipe so an oversized child cannot deadlock cleanup.
func (d *openCodeBoundedDrain) Write(payload []byte) (int, error) {
	d.mu.Lock()
	remaining := d.limit - len(d.buffer)
	if remaining > len(payload) {
		remaining = len(payload)
	}
	if remaining > 0 {
		d.buffer = append(d.buffer, payload[:remaining]...)
	}
	overflow := remaining < len(payload)
	if overflow {
		d.overflow = true
	}
	d.mu.Unlock()
	if overflow {
		d.once.Do(func() { close(d.overflowCh) })
	}
	return len(payload), nil
}

func (d *openCodeBoundedDrain) bytes() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.buffer...)
}

func (d *openCodeBoundedDrain) overflowed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.overflow
}

func newOpenCodeCommand(spec openCodeSpikeSpec) *exec.Cmd {
	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = append([]string(nil), spec.Env...)
	cmd.Stdin = strings.NewReader(spec.Stdin)
	return cmd
}

func runOpenCodeManagedCommand(ctx context.Context, cmd *exec.Cmd) openCodeProcessOutcome {
	if ctx == nil || cmd == nil || !openCodeManagedQuiescenceAvailable() {
		// The support check happens before startManagedCLIProcess, so no child
		// exists and the caller can safely remove its untouched owned root.
		return openCodeProcessOutcome{quiescent: true, err: ErrOpenCodeContainmentUnavailable}
	}
	if err := ctx.Err(); err != nil {
		return openCodeProcessOutcome{quiescent: true, err: err}
	}
	stdoutDrain := newOpenCodeBoundedDrain(openCodeOutputLimit)
	stderrDrain := newOpenCodeBoundedDrain(openCodeOutputLimit)
	cmd.Stdout = stdoutDrain
	cmd.Stderr = stderrDrain
	process, err := startManagedCLIProcess(cmd, nil)
	if err != nil {
		// No Process means nothing executed. A partial start may have been
		// cleaned best-effort by the platform helper, but it did not return the
		// quiescence proof required before inspecting or deleting artifacts.
		if cmd.Process != nil {
			return openCodeProcessOutcome{err: ErrOpenCodeCleanupFailed}
		}
		if err := ctx.Err(); err != nil {
			return openCodeProcessOutcome{quiescent: true, err: err}
		}
		return openCodeProcessOutcome{quiescent: true, err: ErrOpenCodeProcessFailed}
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	quiescer, ok := process.(managedCLIProcessQuiescer)
	if !ok {
		_ = process.terminate()
		<-waitDone
		_ = process.close()
		return openCodeProcessOutcome{err: ErrOpenCodeContainmentUnavailable}
	}

	trigger := ctx.Err()
	var waitErr error
	waitObserved := false
	if trigger == nil {
		select {
		case waitErr = <-waitDone:
			waitObserved = true
		case <-ctx.Done():
			trigger = ctx.Err()
		case <-stdoutDrain.overflowCh:
			trigger = ErrOpenCodeOutputTooLarge
		case <-stderrDrain.overflowCh:
			trigger = ErrOpenCodeOutputTooLarge
		}
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), openCodeCleanupTimeout)
	quiesceErr := quiescer.quiesce(cleanupCtx)
	if !waitObserved {
		select {
		case waitErr = <-waitDone:
			waitObserved = true
		case <-cleanupCtx.Done():
			_ = cmd.Process.Kill()
		}
	}
	closeErr := process.close()
	cancel()

	outcome := openCodeProcessOutcome{
		stdout: stdoutDrain.bytes(),
		stderr: stderrDrain.bytes(),
	}
	if quiesceErr != nil || closeErr != nil || !waitObserved {
		outcome.err = ErrOpenCodeCleanupFailed
		return outcome
	}
	outcome.quiescent = true
	if trigger == nil {
		trigger = ctx.Err()
	}
	if trigger != nil {
		outcome.err = trigger
		return outcome
	}
	if stdoutDrain.overflowed() || stderrDrain.overflowed() {
		outcome.err = ErrOpenCodeOutputTooLarge
		return outcome
	}
	if waitErr != nil {
		outcome.err = classifyOpenCodeProcessFailure(outcome.stderr)
	}
	return outcome
}

func openCodeManagedQuiescenceSupported() bool {
	switch runtime.GOOS {
	case "windows", "aix", "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "solaris":
		return true
	default:
		return false
	}
}

func runOpenCodeSyntheticSpike(ctx context.Context, capability openCodeSyntheticCapability, binary string) (string, error) {
	if ctx == nil || !validOpenCodeBase(capability.rootBase) {
		return "", ErrOpenCodeContainmentUnavailable
	}
	root, err := os.MkdirTemp(capability.rootBase, "agentdc-opencode-spike-")
	if err != nil {
		return "", ErrOpenCodeCleanupFailed
	}
	if err := createOpenCodeOwnedDirectories(root); err != nil {
		_ = os.RemoveAll(root)
		return "", ErrOpenCodeCleanupFailed
	}
	nonceSource := capability.nonce
	if nonceSource == nil {
		nonceSource = rand.Reader
	}
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(nonceSource, nonce); err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, nil, ErrOpenCodeUnsafeSpec)
	}
	title := "agentdc-spike-" + base64.RawURLEncoding.EncodeToString(nonce)
	execute := capability.execute
	if execute == nil {
		execute = runOpenCodeManagedCommand
	}
	forbidden := [][]byte{[]byte(syntheticOpenCodePrompt), []byte(title)}

	discoverySpec, err := newOpenCodeModelDiscoverySpec(root, binary)
	if err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	discovery := execute(ctx, newOpenCodeCommand(discoverySpec))
	forbidden = addOpenCodePrivatePatterns(forbidden, discovery)
	if !discovery.quiescent {
		return "", ErrOpenCodeCleanupFailed
	}
	if discovery.err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, sanitizeOpenCodeProcessError(discovery.err))
	}
	if err := ctx.Err(); err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	models, err := parseOpenCodeFreeModels(bytes.NewReader(discovery.stdout))
	if err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	model, err := selectOpenCodeSpikeModel(models)
	if err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	if empty, err := openCodeDirectoryEmpty(filepath.Join(root, "work")); err != nil || !empty {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, ErrOpenCodeUnsafeArtifact)
	}
	runSpec, err := newOpenCodeSpikeSpec(root, binary, model)
	if err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	runSpec.Args = append(runSpec.Args, "--title", title)
	run := execute(ctx, newOpenCodeCommand(runSpec))
	var privatePatternErr error
	forbidden, privatePatternErr = addOpenCodeRunPrivatePatterns(forbidden, run)
	if !run.quiescent {
		return "", ErrOpenCodeCleanupFailed
	}
	if run.err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, errors.Join(sanitizeOpenCodeProcessError(run.err), privatePatternErr))
	}
	if privatePatternErr != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, privatePatternErr)
	}
	if err := ctx.Err(); err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	result, err := parseOpenCodeSpikeNDJSON(bytes.NewReader(run.stdout))
	if err != nil {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, err)
	}
	forbidden = append(forbidden, []byte(result.SessionID))
	if result.Text != "OK" {
		return "", finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, ErrOpenCodeUnexpectedAnswer)
	}
	if err := finishOpenCodeOwnedRoot(capability.rootBase, root, forbidden, nil); err != nil {
		return "", err
	}
	return result.Text, nil
}

func sanitizeOpenCodeProcessError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrOpenCodeOutputTooLarge):
		return ErrOpenCodeOutputTooLarge
	case errors.Is(err, ErrOpenCodeOfflineBootstrap):
		return ErrOpenCodeOfflineBootstrap
	case errors.Is(err, ErrOpenCodeContainmentUnavailable):
		return ErrOpenCodeContainmentUnavailable
	case errors.Is(err, ErrOpenCodeCleanupFailed):
		return ErrOpenCodeCleanupFailed
	default:
		return ErrOpenCodeProcessFailed
	}
}

func classifyOpenCodeProcessFailure(stderr []byte) error {
	lower := bytes.ToLower(stderr)
	for _, marker := range [][]byte{
		[]byte("enotcached"),
		[]byte("only-if-cached"),
		[]byte("offline mode"),
		[]byte("offline bootstrap"),
	} {
		if bytes.Contains(lower, marker) {
			return ErrOpenCodeOfflineBootstrap
		}
	}
	return ErrOpenCodeProcessFailed
}

func addOpenCodePrivatePatterns(patterns [][]byte, outcome openCodeProcessOutcome) [][]byte {
	patterns = appendNonemptyOpenCodePattern(patterns, outcome.stderr)
	if outcome.err != nil {
		patterns = appendNonemptyOpenCodePattern(patterns, []byte(outcome.err.Error()))
	}
	return patterns
}

func addOpenCodeRunPrivatePatterns(patterns [][]byte, outcome openCodeProcessOutcome) ([][]byte, error) {
	patterns = addOpenCodePrivatePatterns(patterns, outcome)
	patterns = appendNonemptyOpenCodePattern(patterns, outcome.stdout)
	sessionIDs, err := openCodeSessionCandidates(outcome.stdout)
	for _, sessionID := range sessionIDs {
		patterns = appendNonemptyOpenCodePattern(patterns, sessionID)
	}
	if err != nil {
		// On adversarial malformed output, a generic prefix keeps the artifact
		// scan fail-closed without retaining an unbounded candidate set.
		patterns = appendNonemptyOpenCodePattern(patterns, []byte("ses"))
	}
	return patterns, err
}

func openCodeSessionCandidates(payload []byte) ([][]byte, error) {
	result := make([][]byte, 0, openCodeSessionCandidateLimit)
	add := func(candidate []byte) error {
		if !validOpenCodeSessionCandidate(candidate) {
			return nil
		}
		for _, existing := range result {
			if bytes.Equal(existing, candidate) {
				return nil
			}
		}
		if len(result) >= openCodeSessionCandidateLimit {
			return openCodeOutputLimitError()
		}
		result = append(result, append([]byte(nil), candidate...))
		return nil
	}
	if err := scanOpenCodeSessionTokens(payload, add); err != nil {
		return result, err
	}
	if err := scanOpenCodeEscapedJSONStrings(payload, add); err != nil {
		return result, err
	}
	return result, nil
}

func scanOpenCodeSessionTokens(payload []byte, add func([]byte) error) error {
	for start := 0; start+3 <= len(payload); start++ {
		if !bytes.Equal(payload[start:start+3], []byte("ses")) ||
			start > 0 && openCodeIdentifierByte(payload[start-1]) {
			continue
		}
		end := start + 3
		for end < len(payload) && end-start < openCodeIdentifierMax && openCodeIdentifierByte(payload[end]) {
			end++
		}
		if err := add(payload[start:end]); err != nil {
			return err
		}
		start = end - 1
	}
	return nil
}

func scanOpenCodeEscapedJSONStrings(payload []byte, add func([]byte) error) error {
	escapedStrings := 0
	start := -1
	escaped := false
	hasEscape := false
	for index := 0; index < len(payload); index++ {
		character := payload[index]
		if start < 0 {
			if character == '"' {
				start = index
				escaped = false
				hasEscape = false
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			hasEscape = true
			continue
		}
		if character != '"' {
			continue
		}
		if hasEscape {
			escapedStrings++
			if escapedStrings > openCodeJSONTokenLimit {
				return openCodeOutputLimitError()
			}
			var decoded string
			if err := json.Unmarshal(payload[start:index+1], &decoded); err != nil {
				return openCodeProtocolError()
			}
			if err := scanOpenCodeSessionTokens([]byte(decoded), add); err != nil {
				return err
			}
		}
		start = -1
	}
	if start >= 0 {
		return openCodeProtocolError()
	}
	return nil
}

func validOpenCodeSessionCandidate(candidate []byte) bool {
	if len(candidate) < 3 || len(candidate) > openCodeIdentifierMax || !bytes.HasPrefix(candidate, []byte("ses")) {
		return false
	}
	for _, character := range candidate {
		if !openCodeIdentifierByte(character) {
			return false
		}
	}
	return true
}

func openCodeIdentifierByte(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.'
}

func appendNonemptyOpenCodePattern(patterns [][]byte, pattern []byte) [][]byte {
	if len(pattern) == 0 {
		return patterns
	}
	return append(patterns, append([]byte(nil), pattern...))
}

func createOpenCodeOwnedDirectories(root string) error {
	for _, name := range []string{"work", "data", "config", "cache", "state", "home", "temp", "npm-cache", "npm-prefix"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func validOpenCodeBase(base string) bool {
	if !validOpenCodeAbsolutePath(base, true) {
		return false
	}
	info, err := os.Lstat(base)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func openCodeDirectoryEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	return len(entries) == 0, err
}

func finishOpenCodeOwnedRoot(base, root string, forbidden [][]byte, prior error) error {
	cleanupErr := auditAndRemoveOpenCodeRoot(base, root, forbidden)
	return errors.Join(prior, cleanupErr)
}

func auditAndRemoveOpenCodeRoot(base, root string, forbidden [][]byte) error {
	if !validOpenCodeCleanupRoot(base, root) {
		return ErrOpenCodeCleanupFailed
	}
	auditErr := auditOpenCodeOwnedRoot(root, forbidden)
	removeErr := os.RemoveAll(root)
	if removeErr != nil {
		return errors.Join(auditErr, ErrOpenCodeCleanupFailed)
	}
	return auditErr
}

func validOpenCodeCleanupRoot(base, root string) bool {
	if !validOpenCodeBase(base) || !validOpenCodeAbsolutePath(root, true) {
		return false
	}
	relative, err := filepath.Rel(base, root)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return filepath.Dir(relative) == "." && strings.HasPrefix(filepath.Base(relative), "agentdc-opencode-spike-")
}

func auditOpenCodeOwnedRoot(path string, forbidden [][]byte) error {
	if !validOpenCodeAbsolutePath(path, true) {
		return ErrOpenCodeUnsafeArtifact
	}
	rootInfo, err := os.Lstat(path)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return ErrOpenCodeUnsafeArtifact
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return ErrOpenCodeUnsafeArtifact
	}
	auditor := openCodeArtifactAuditor{root: root, forbidden: forbidden}
	auditErr := auditor.readDirectory(".", 0)
	closeErr := root.Close()
	if auditErr != nil || closeErr != nil {
		return ErrOpenCodeUnsafeArtifact
	}
	return nil
}

type openCodeArtifactAuditor struct {
	root      *os.Root
	forbidden [][]byte
	entries   int
	aggregate int64
}

func (a *openCodeArtifactAuditor) readDirectory(relative string, depth int) (result error) {
	directory, err := a.root.Open(relative)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	for {
		batch, readErr := openCodeReadDirBatch(directory, openCodeArtifactReadBatchSize)
		for _, entry := range batch {
			a.entries++
			childDepth := depth + 1
			if a.entries > openCodeArtifactEntryLimit || childDepth > openCodeArtifactDepthLimit {
				return ErrOpenCodeUnsafeArtifact
			}
			child := path.Join(relative, entry.Name())
			info, err := a.root.Lstat(child)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return ErrOpenCodeUnsafeArtifact
			}
			if info.IsDir() {
				if err := a.readDirectory(child, childDepth); err != nil {
					return err
				}
				continue
			}
			if err := a.readRegularFile(child, entry.Name(), info); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (a *openCodeArtifactAuditor) readRegularFile(relative, name string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > openCodeArtifactFileLimit {
		return ErrOpenCodeUnsafeArtifact
	}
	a.aggregate += info.Size()
	if a.aggregate > openCodeArtifactAggregateLimit || openCodeDatabaseArtifact(name) {
		return ErrOpenCodeUnsafeArtifact
	}
	content, err := a.root.ReadFile(relative)
	if err != nil || int64(len(content)) != info.Size() {
		return ErrOpenCodeUnsafeArtifact
	}
	for _, pattern := range a.forbidden {
		if len(pattern) > 0 && bytes.Contains(content, pattern) {
			return ErrOpenCodeUnsafeArtifact
		}
	}
	return nil
}

func openCodeDatabaseArtifact(name string) bool {
	lower := strings.ToLower(name)
	return lower == "opencode.db" || strings.HasPrefix(lower, "opencode.db-")
}
