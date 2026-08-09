package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/google/uuid"

	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

const appZaloErrorStateUpdate = "session_state_update_failed"

var appZaloProcessThreadGate appZaloThreadGate

var errAppZaloSessionStateUpdate = errors.New(appZaloErrorStateUpdate)

type appZaloStructuredRunner interface {
	appRunZaloSession(
		context.Context,
		appZaloSessionRunInput,
		func(string),
	) (appZaloRunResult, error)
}

type appZaloCompletion struct {
	generation      int64
	contextTokens   int64
	messageCursor   int64
	memoryRevision  int64
	lessonsRevision int64
	ready           bool
}

type appZaloCompletionCarrier struct {
	mu      sync.Mutex
	pending appZaloCompletion
}

func (c *appZaloCompletionCarrier) set(pending appZaloCompletion) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = pending
}

func (c *appZaloCompletionCarrier) take() (appZaloCompletion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending
	c.pending = appZaloCompletion{}
	return pending, pending.ready
}

type appZaloCompletionContextKey struct{}

type appZaloSessionSelection struct {
	session          store.ZaloCLISession
	prompt           string
	bootstrapPrompt  string
	completionCursor int64
	bootstrapCursor  int64
	memoryRevision   int64
	lessonsRevision  int64
	resume           bool
}

type appZaloMemorySnapshot struct {
	memory          []ipc.ZaloMemory
	lessons         []ipc.ZaloLesson
	memoryRevision  int64
	lessonsRevision int64
	memoryOK        bool
	lessonsOK       bool
}

// appZaloStructuredAnswerRunner keeps Task 5 testable before the guarded Task 6
// source seam is installed. Its Run method reconstructs the structured arguments
// from the durable store; after Task 6, appRunZalo sees the private structured
// method directly and this compatibility Run path is no longer selected.
type appZaloStructuredAnswerRunner struct {
	a                *api
	run              appZaloStructuredRunner
	zc               zaloConfig
	threadID         string
	question         string
	currentZaloMsgID string
	files            []ipc.ZaloAttachment
	bootstrapCursor  int64
}

func (r *appZaloStructuredAnswerRunner) Run(
	ctx context.Context,
	bootstrapPrompt string,
	step func(string),
) (string, error) {
	history, err := r.a.st.ZaloMessages(r.threadID, zaloHistoryTurns)
	bootstrapCursor := int64(0)
	if err != nil {
		history = nil
	} else {
		bootstrapCursor = appZaloHistoryCursor(history)
		if r.bootstrapCursor < bootstrapCursor {
			bootstrapCursor = r.bootstrapCursor
		}
	}
	files := mergeZaloFiles(r.files, history)
	pz := r.zc
	if memory, memoryErr := r.a.st.ZaloMemory(r.threadID, zaloMemoryLines); memoryErr == nil {
		pz.Memory = memory
	}
	if lessons, lessonsErr := r.a.st.ZaloLessons(zaloLessonLines); lessonsErr == nil {
		pz.Lessons = lessons
	}
	pz.overlayFor = zaloOverlayPath(r.zc.OverlayDir, r.threadID)
	return r.a.appRunZaloStructured(
		ctx,
		r,
		pz,
		r.threadID,
		r.question,
		r.currentZaloMsgID,
		history,
		retrieve(r.question, r.zc.KBRoots),
		files,
		step,
		bootstrapPrompt,
		bootstrapCursor,
	)
}

func (r *appZaloStructuredAnswerRunner) appRunZaloSession(
	ctx context.Context,
	in appZaloSessionRunInput,
	step func(string),
) (appZaloRunResult, error) {
	return r.run.appRunZaloSession(ctx, in, step)
}

// appAnswerZalo owns the complete per-thread critical section. The timeout is
// deliberately created only after the gate is acquired, so queueing behind an
// earlier turn does not consume this turn's Claude budget.
func (a *api) appAnswerZalo(
	deps *zaloDeps,
	threadID, question string,
	reply ipc.ZaloOutboxDraft,
	files []ipc.ZaloAttachment,
) error {
	if deps == nil || deps.run == nil {
		return errors.New("zalo session answer dependencies are required")
	}
	if a.st == nil {
		return errors.New("zalo session store is required")
	}
	if threadID == "" {
		return errAppZaloThreadIDRequired
	}
	release, err := appZaloProcessThreadGate.Acquire(
		context.Background(),
		fmt.Sprintf("%p\x00%s", a.st, threadID),
	)
	if err != nil {
		return fmt.Errorf("acquire Zalo turn gate: %w", err)
	}
	defer release()

	carrier := &appZaloCompletionCarrier{}
	bootstrapCursor, cursorErr := a.st.LatestZaloMessageID(threadID)
	if cursorErr != nil {
		a.logger.Warn("zalo session: could not capture conservative bootstrap cursor",
			"thread", threadID, "err", cursorErr)
		bootstrapCursor = 0
	}
	base := context.WithValue(context.Background(), appZaloCompletionContextKey{}, carrier)
	ctx, cancel := context.WithTimeout(base, deps.cfg.Timeout)
	defer cancel()
	step := func(text string) { a.zlog.add(ipc.ZaloLogStep, threadID, text) }

	run := deps.run
	if structured, ok := deps.run.(appZaloStructuredRunner); ok {
		run = &appZaloStructuredAnswerRunner{
			a:                a,
			run:              structured,
			zc:               deps.cfg,
			threadID:         threadID,
			question:         question,
			currentZaloMsgID: appZaloCurrentMsgID(reply.ReplyQuote),
			files:            append([]ipc.ZaloAttachment(nil), files...),
			bootstrapCursor:  bootstrapCursor,
		}
	}
	if err := a.answerZalo(ctx, deps.cfg, run, threadID, question, step, reply, files...); err != nil {
		return err
	}
	pending, ok := carrier.take()
	if !ok {
		return nil
	}
	a.appCompleteZaloTurn(threadID, pending)
	return nil
}

func (a *api) appRunZalo(
	ctx context.Context,
	run zaloRunner,
	zc zaloConfig,
	threadID, question, currentZaloMsgID string,
	history []ipc.ZaloMessage,
	found []passage,
	files []ipc.ZaloAttachment,
	step func(string),
) (string, error) {
	structured, ok := run.(appZaloStructuredRunner)
	if !ok {
		return run.Run(ctx, buildConsultPrompt(zc, question, history, found, files...), step)
	}
	return a.appRunZaloStructured(
		ctx, structured, zc, threadID, question, currentZaloMsgID,
		history, found, files, step, "", -1,
	)
}

func (a *api) appRunZaloStructured(
	ctx context.Context,
	run appZaloStructuredRunner,
	zc zaloConfig,
	threadID, question, currentZaloMsgID string,
	history []ipc.ZaloMessage,
	found []passage,
	files []ipc.ZaloAttachment,
	step func(string),
	bootstrapOverride string,
	bootstrapCursorOverride int64,
) (string, error) {
	selection, err := a.appSelectZaloSession(
		zc, threadID, question, currentZaloMsgID, history, found, files,
		bootstrapOverride, bootstrapCursorOverride,
	)
	if err != nil {
		return "", err
	}
	result, runErr := run.appRunZaloSession(ctx, appZaloSessionRunInput{
		SessionID:         selection.session.ClaudeSessionID,
		Prompt:            selection.prompt,
		Resume:            selection.resume,
		RecoverySessionID: uuid.NewString(),
		BootstrapPrompt:   selection.bootstrapPrompt,
	}, step)
	if runErr != nil {
		a.appInvalidateFailedZaloRecovery(selection.session, result)
		return "", runErr
	}

	recovered := result.Recovered
	if recovered && result.SessionID != "" && result.SessionID != selection.session.ClaudeSessionID {
		replaced, replacedOK := a.appReplaceRecoveredZaloSession(selection.session, result.SessionID)
		if !replacedOK {
			return result.Answer, nil
		}
		selection.session = replaced
	}
	prompt := selection.prompt
	previousContext := selection.session.ContextTokens
	completionCursor := selection.completionCursor
	if recovered {
		prompt = selection.bootstrapPrompt
		previousContext = 0
		completionCursor = selection.bootstrapCursor
	}
	contextTokens := appZaloObservedContext(previousContext, prompt, result)
	if carrier, ok := ctx.Value(appZaloCompletionContextKey{}).(*appZaloCompletionCarrier); ok {
		carrier.set(appZaloCompletion{
			generation: selection.session.Generation, contextTokens: contextTokens,
			messageCursor: completionCursor, memoryRevision: selection.memoryRevision,
			lessonsRevision: selection.lessonsRevision, ready: true,
		})
	}
	return result.Answer, nil
}

func (a *api) appSelectZaloSession(
	zc zaloConfig,
	threadID, question, currentZaloMsgID string,
	history []ipc.ZaloMessage,
	found []passage,
	files []ipc.ZaloAttachment,
	bootstrapOverride string,
	bootstrapCursorOverride int64,
) (appZaloSessionSelection, error) {
	fingerprint := appZaloPromptFingerprint(zc, threadID)
	snapshot := a.appLoadZaloMemorySnapshot(threadID)
	bootstrapConfig := zc
	if snapshot.memoryOK {
		bootstrapConfig.Memory = snapshot.memory
	}
	if snapshot.lessonsOK {
		bootstrapConfig.Lessons = snapshot.lessons
	}
	bootstrapCursor := appZaloHistoryCursor(history)
	if bootstrapCursorOverride >= 0 {
		bootstrapCursor = bootstrapCursorOverride
	}
	bootstrap := bootstrapOverride
	if snapshot.memoryOK || snapshot.lessonsOK || bootstrap == "" {
		bootstrap = buildConsultPrompt(bootstrapConfig, question, history, found, files...)
	}
	bootstrapMemoryRevision := int64(0)
	if snapshot.memoryOK {
		bootstrapMemoryRevision = snapshot.memoryRevision
	}
	bootstrapLessonsRevision := int64(0)
	if snapshot.lessonsOK {
		bootstrapLessonsRevision = snapshot.lessonsRevision
	}
	session, err := a.st.ZaloCLISession(threadID)
	if errors.Is(err, store.ErrNotFound) {
		created, createErr := a.st.CreateZaloCLISession(store.ZaloCLISession{
			ThreadID: threadID, ClaudeSessionID: uuid.NewString(), Model: zc.Model,
			PromptFingerprint: fingerprint,
		})
		if createErr == nil {
			return appZaloSessionSelection{
				session: created, prompt: bootstrap, bootstrapPrompt: bootstrap,
				completionCursor: bootstrapCursor, bootstrapCursor: bootstrapCursor,
				memoryRevision: bootstrapMemoryRevision, lessonsRevision: bootstrapLessonsRevision,
			}, nil
		}
		// Another process may have inserted the mapping. Reload once and never
		// overwrite the generation it won.
		session, err = a.appReloadZaloSessionAfterConflict(zc, threadID, fingerprint)
		if err != nil {
			return appZaloSessionSelection{}, err
		}
	} else if err != nil {
		return appZaloSessionSelection{}, err
	}

	if appZaloShouldRotate(session, zc.Model, fingerprint) {
		next := session
		next.ClaudeSessionID = uuid.NewString()
		next.Model = zc.Model
		next.PromptFingerprint = fingerprint
		next.ContextTokens = 0
		next.TurnCount = 0
		next.MemoryRevision = 0
		next.LessonsRevision = 0
		next.RotateBeforeNext = false
		next.LastError = ""
		replaced, replaceErr := a.st.ReplaceZaloCLISession(session.Generation, next)
		if replaceErr == nil {
			return appZaloSessionSelection{
				session: replaced, prompt: bootstrap, bootstrapPrompt: bootstrap,
				completionCursor: bootstrapCursor, bootstrapCursor: bootstrapCursor,
				memoryRevision: bootstrapMemoryRevision, lessonsRevision: bootstrapLessonsRevision,
			}, nil
		}
		if !errors.Is(replaceErr, store.ErrZaloCLISessionConflict) {
			return appZaloSessionSelection{}, replaceErr
		}
		// A concurrent generation is authoritative. Reload exactly once and use
		// it without attempting another replacement.
		session, err = a.appReloadZaloSessionAfterConflict(zc, threadID, fingerprint)
		if err != nil {
			return appZaloSessionSelection{}, err
		}
	}

	delta, err := a.st.ZaloMessagesAfter(threadID, session.MessageCursor, 100)
	if err != nil {
		return appZaloSessionSelection{}, err
	}
	refresh := &appZaloMemoryRefresh{}
	memoryRevision := session.MemoryRevision
	if snapshot.memoryOK {
		memoryRevision = snapshot.memoryRevision
		if snapshot.memoryRevision != session.MemoryRevision {
			refresh.ReplaceMemory = true
			refresh.Memory = snapshot.memory
			refresh.MemoryRevision = snapshot.memoryRevision
		}
	}
	lessonsRevision := session.LessonsRevision
	if snapshot.lessonsOK {
		lessonsRevision = snapshot.lessonsRevision
		if snapshot.lessonsRevision != session.LessonsRevision {
			refresh.ReplaceLessons = true
			refresh.Lessons = snapshot.lessons
			refresh.LessonsRevision = snapshot.lessonsRevision
		}
	}
	if !refresh.ReplaceMemory && !refresh.ReplaceLessons {
		refresh = nil
	}
	deltaPrompt := buildAppZaloDeltaPromptResult(appZaloSessionPromptInput{
		Config: zc, Question: question, CurrentZaloMsgID: currentZaloMsgID,
		History: history, Delta: delta, Found: found, Files: files, MemoryRefresh: refresh,
	})
	completionCursor := session.MessageCursor
	if deltaPrompt.ConsumedCursor > completionCursor {
		completionCursor = deltaPrompt.ConsumedCursor
	}
	return appZaloSessionSelection{
		session: session, prompt: deltaPrompt.Prompt, bootstrapPrompt: bootstrap,
		completionCursor: completionCursor, bootstrapCursor: bootstrapCursor,
		memoryRevision: memoryRevision, lessonsRevision: lessonsRevision, resume: true,
	}, nil
}

func (a *api) appLoadZaloMemorySnapshot(threadID string) appZaloMemorySnapshot {
	var snapshot appZaloMemorySnapshot
	memory, revision, err := a.st.AppPromptMemory(threadID, zaloMemoryLines)
	if err != nil {
		a.logger.Warn("zalo session: could not read thread memory snapshot",
			"thread", threadID, "error_type", fmt.Sprintf("%T", err))
	} else {
		snapshot.memory = memory
		snapshot.memoryRevision = revision
		snapshot.memoryOK = true
	}
	lessons, revision, err := a.st.AppPromptLessons(zaloLessonLines)
	if err != nil {
		a.logger.Warn("zalo session: could not read global lesson snapshot",
			"error_type", fmt.Sprintf("%T", err))
	} else {
		snapshot.lessons = lessons
		snapshot.lessonsRevision = revision
		snapshot.lessonsOK = true
	}
	return snapshot
}

func (a *api) appReplaceRecoveredZaloSession(
	current store.ZaloCLISession,
	effectiveSessionID string,
) (store.ZaloCLISession, bool) {
	next := current
	next.ClaudeSessionID = effectiveSessionID
	next.ContextTokens = 0
	next.TurnCount = 0
	next.RotateBeforeNext = false
	next.LastError = ""
	replaced, err := a.st.ReplaceZaloCLISession(current.Generation, next)
	if err == nil {
		return replaced, true
	}
	if errors.Is(err, store.ErrZaloCLISessionConflict) {
		_, _ = a.st.ZaloCLISession(current.ThreadID)
	} else if markErr := a.st.MarkZaloCLISessionForRotation(
		current.ThreadID, current.Generation, appZaloErrorStateUpdate,
	); markErr != nil {
		if errors.Is(markErr, store.ErrZaloCLISessionConflict) {
			_, _ = a.st.ZaloCLISession(current.ThreadID)
		}
		a.logger.Warn("zalo session: could not invalidate failed recovery persistence",
			"thread", current.ThreadID, "err", markErr)
	}
	a.logger.Warn("zalo session: could not persist recovered generation",
		"thread", current.ThreadID, "err", err)
	return store.ZaloCLISession{}, false
}

func (a *api) appReloadZaloSessionAfterConflict(
	zc zaloConfig,
	threadID, fingerprint string,
) (store.ZaloCLISession, error) {
	session, err := a.st.ZaloCLISession(threadID)
	if err != nil {
		return store.ZaloCLISession{}, err
	}
	if appZaloShouldRotate(session, zc.Model, fingerprint) {
		return store.ZaloCLISession{}, errAppZaloSessionStateUpdate
	}
	return session, nil
}

func (a *api) appInvalidateFailedZaloRecovery(
	session store.ZaloCLISession,
	result appZaloRunResult,
) {
	code := result.RecoveryCode
	shouldRotate := (code == appZaloErrorResumeUnusable && !result.RecoveryAttempted) ||
		(code == appZaloErrorResumeNotFound && result.RecoveryAttempted)
	if !shouldRotate {
		return
	}
	if err := a.st.MarkZaloCLISessionForRotation(session.ThreadID, session.Generation, code); err != nil {
		if errors.Is(err, store.ErrZaloCLISessionConflict) {
			_, _ = a.st.ZaloCLISession(session.ThreadID)
		}
		a.logger.Warn("zalo session: could not mark failed recovery for rotation",
			"thread", session.ThreadID, "code", code, "err", err)
	}
}

func (a *api) appCompleteZaloTurn(threadID string, pending appZaloCompletion) {
	if _, err := a.st.CompleteZaloCLITurn(
		threadID, pending.generation, pending.contextTokens, pending.messageCursor,
		pending.memoryRevision, pending.lessonsRevision,
	); err == nil {
		return
	} else if errors.Is(err, store.ErrZaloCLISessionConflict) {
		_, _ = a.st.ZaloCLISession(threadID)
		a.logger.Warn("zalo session: completion lost a generation race", "thread", threadID)
		return
	} else {
		a.logger.Warn("zalo session: could not persist turn completion", "thread", threadID, "err", err)
		if markErr := a.st.MarkZaloCLISessionForRotation(
			threadID, pending.generation, appZaloErrorStateUpdate,
		); markErr != nil {
			a.logger.Warn("zalo session: could not mark completion failure",
				"thread", threadID, "err", markErr)
		}
	}
}

func appZaloObservedContext(previous int64, prompt string, result appZaloRunResult) int64 {
	if result.UsageMeasured {
		return result.ContextTokens
	}
	bytes := int64(len(prompt))
	if result.OutputBytes > math.MaxInt64-bytes {
		bytes = math.MaxInt64
	} else {
		bytes += result.OutputBytes
	}
	estimate := bytes / 3
	if bytes%3 != 0 {
		estimate++
	}
	if previous > math.MaxInt64-estimate {
		return math.MaxInt64
	}
	return previous + estimate
}

func appZaloHistoryCursor(history []ipc.ZaloMessage) int64 {
	var cursor int64
	for _, message := range history {
		if message.ID > cursor {
			cursor = message.ID
		}
	}
	return cursor
}

func appZaloCurrentMsgID(raw json.RawMessage) string {
	var quote map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &quote) != nil {
		return ""
	}
	encoded, ok := quote["msgId"]
	if !ok {
		return ""
	}
	var msgID string
	if json.Unmarshal(encoded, &msgID) != nil {
		return ""
	}
	return strings.TrimSpace(msgID)
}
