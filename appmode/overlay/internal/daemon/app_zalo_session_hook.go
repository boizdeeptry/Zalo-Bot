package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

const (
	appZaloErrorStateUpdate = "session_state_update_failed"
	appZaloErrorMemoryRead  = "memory_read_failed"
	appZaloErrorMemoryApply = "memory_apply_failed"
)

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
	generation            int64
	contextTokens         int64
	messageCursor         int64
	memorySubjectUID      string
	memorySubjectRevision int64
	memoryCommonRevision  int64
	memoryRevision        int64
	lessonsRevision       int64
	ready                 bool
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
	session               store.ZaloCLISession
	prompt                string
	bootstrapPrompt       string
	completionCursor      int64
	bootstrapCursor       int64
	memorySubject         store.AppMemorySubject
	subjectMemory         []store.AppPromptMemoryItem
	memorySubjectUID      string
	memorySubjectRevision int64
	memoryCommonRevision  int64
	memoryRevision        int64
	lessonsRevision       int64
	memoryOK              bool
	resume                bool
}

type appZaloMemorySnapshot struct {
	common          []store.AppPromptMemoryItem
	subject         []store.AppPromptMemoryItem
	lessons         []ipc.ZaloLesson
	commonRevision  int64
	subjectRevision int64
	lessonsRevision int64
	memoryOK        bool
	lessonsOK       bool
}

type appZaloRunnerFactory func(zaloConfig, zaloRunner, string, bool) zaloRunner

type appZaloAttachmentAwareRunner interface {
	appWithZaloAttachments(bool) zaloRunner
}

// appZaloStructuredClaudeAwareRunner marks a captured turn whose pending
// session delta owns an attachment. Such a turn must reach the structured
// Claude seam: a stateless local CLI cannot consume its durable delta cursor.
type appZaloStructuredClaudeAwareRunner interface {
	appWithZaloStructuredClaudeRequirement(bool) zaloRunner
}

type appZaloTurnSnapshot struct {
	history        []ipc.ZaloMessage
	directFiles    []ipc.ZaloAttachment
	files          []ipc.ZaloAttachment
	hasAttachments bool
	highWater      int64
	highWaterKnown bool
	resumeDelta    appZaloResumeDeltaSnapshot
}

type appZaloResumeDeltaSnapshot struct {
	delta           []store.ZaloDeltaMessage
	claudeSessionID string
	generation      int64
	messageCursor   int64
	turnCount       int64
	highWater       int64
	known           bool
	hasBasis        bool
}

// appZaloStructuredAnswerRunner adapts the upstream single-string runner hook to
// the overlay's structured session seam. It receives the trusted turn snapshot
// captured inside the thread gate and ignores the upstream legacy-Memory prompt.
// It intentionally exposes only Run: exposing appRunZaloSession here would let
// the upstream appRunZalo path bypass Run and rebuild inputs from a later store read.
type appZaloStructuredAnswerRunner struct {
	a                *api
	run              appZaloStructuredRunner
	zc               zaloConfig
	threadID         string
	question         string
	currentZaloMsgID string
	history          []ipc.ZaloMessage
	directFiles      []ipc.ZaloAttachment
	files            []ipc.ZaloAttachment
	highWater        int64
	highWaterKnown   bool
	resumeDelta      appZaloResumeDeltaSnapshot
	claudeBinding    appZaloClaudeBinding
}

func (r *appZaloStructuredAnswerRunner) Run(
	ctx context.Context,
	_ string,
	step func(string),
) (string, error) {
	history := appZaloCloneHistory(r.history)
	directFiles := slices.Clone(r.directFiles)
	files := slices.Clone(r.files)
	pz := r.zc
	pz.Memory = nil
	if lessons, lessonsErr := r.a.st.ZaloLessons(zaloLessonLines); lessonsErr == nil {
		pz.Lessons = lessons
	}
	pz.overlayFor = zaloOverlayPath(r.zc.OverlayDir, r.threadID)
	return r.a.appRunZaloStructured(
		ctx,
		r.run,
		pz,
		r.threadID,
		r.question,
		r.currentZaloMsgID,
		history,
		retrieve(r.question, r.zc.KBRoots),
		files,
		directFiles,
		step,
		"",
		r.highWater,
		r.highWaterKnown,
		r.resumeDelta.clone(),
		r.claudeBinding,
	)
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
	return a.appAnswerZaloWithRunnerFactory(
		deps, threadID, question, reply, files, a.appZaloRunner,
	)
}

func (a *api) appAnswerZaloWithRunnerFactory(
	deps *zaloDeps,
	threadID, question string,
	reply ipc.ZaloOutboxDraft,
	files []ipc.ZaloAttachment,
	route appZaloRunnerFactory,
) error {
	if deps == nil || deps.run == nil {
		return errors.New("zalo session answer dependencies are required")
	}
	if route == nil {
		return errors.New("zalo session runner factory is required")
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
	base := context.WithValue(context.Background(), appZaloCompletionContextKey{}, carrier)
	ctx, cancel := context.WithTimeout(base, deps.cfg.Timeout)
	defer cancel()
	step := func(text string) { a.zlog.add(ipc.ZaloLogStep, threadID, text) }

	run := route(deps.cfg, deps.run, threadID, len(files) > 0)
	effectiveConfig := deps.cfg
	var binding appZaloClaudeBinding
	run, effectiveConfig, binding, err = a.appResolveZaloSessionRoute(
		ctx, run, effectiveConfig, threadID,
	)
	if err != nil {
		return err
	}
	snapshot := a.appCaptureZaloTurnSnapshot(effectiveConfig, threadID, files)
	if aware, ok := run.(appZaloAttachmentAwareRunner); ok {
		run = aware.appWithZaloAttachments(snapshot.hasAttachments)
	}
	if aware, ok := run.(appZaloStructuredClaudeAwareRunner); ok {
		run = aware.appWithZaloStructuredClaudeRequirement(
			appZaloDeltaHasAttachments(snapshot.resumeDelta.delta),
		)
	}
	if structured, ok := run.(appZaloStructuredRunner); ok {
		run = &appZaloStructuredAnswerRunner{
			a:                a,
			run:              structured,
			zc:               effectiveConfig,
			threadID:         threadID,
			question:         question,
			currentZaloMsgID: appZaloCurrentMsgID(reply.ReplyQuote),
			history:          snapshot.history,
			directFiles:      snapshot.directFiles,
			files:            snapshot.files,
			highWater:        snapshot.highWater,
			highWaterKnown:   snapshot.highWaterKnown,
			resumeDelta:      snapshot.resumeDelta,
			claudeBinding:    binding,
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

func (a *api) appResolveZaloSessionRoute(
	ctx context.Context,
	run zaloRunner,
	zc zaloConfig,
	threadID string,
) (zaloRunner, zaloConfig, appZaloClaudeBinding, error) {
	resolver, ok := run.(appZaloSessionRouteResolver)
	if !ok {
		return run, zc, appZaloClaudeBinding{}, nil
	}
	var current *store.ZaloCLISession
	session, err := a.st.ZaloCLISession(threadID)
	if err == nil {
		current = &session
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, zc, appZaloClaudeBinding{}, err
	}
	resolved, binding := resolver.appResolveZaloSessionRoute(ctx, current)
	if binding.Model != "" {
		zc.Model = binding.Model
	}
	return resolved, zc, binding, nil
}

func (a *api) appCaptureZaloTurnSnapshot(
	zc zaloConfig,
	threadID string,
	currentFiles []ipc.ZaloAttachment,
) appZaloTurnSnapshot {
	history, err := a.st.ZaloMessages(threadID, zaloHistoryTurns)
	if err != nil {
		a.logger.Warn("llm route: không đọc được lịch sử, lượt này đi thẳng local CLI",
			"thread", threadID, "err", err)
		return appZaloTurnSnapshot{
			directFiles: slices.Clone(currentFiles),
			files:       slices.Clone(currentFiles), hasAttachments: true,
		}
	}
	history = appZaloCloneHistory(history)
	directFiles := slices.Clone(currentFiles)
	files := mergeZaloFiles(slices.Clone(directFiles), history)
	highWater := appZaloHistoryCursor(history)
	resumeDelta, deltaErr := a.appCaptureZaloResumeDelta(zc, threadID, highWater)
	if deltaErr != nil {
		a.logger.Warn("zalo session: could not capture pending delta; cursor will stay conservative",
			"thread", threadID, "err", deltaErr)
	}
	return appZaloTurnSnapshot{
		history: history, directFiles: directFiles, files: files,
		hasAttachments: slices.ContainsFunc(files, func(at ipc.ZaloAttachment) bool {
			return at.Path != ""
		}) || appZaloDeltaHasAttachments(resumeDelta.delta) || !resumeDelta.known,
		highWater:      highWater,
		highWaterKnown: true,
		resumeDelta:    resumeDelta,
	}
}

func appZaloCloneHistory(history []ipc.ZaloMessage) []ipc.ZaloMessage {
	out := slices.Clone(history)
	for i := range out {
		out[i].Attachments = slices.Clone(out[i].Attachments)
	}
	return out
}

func (a *api) appCaptureZaloResumeDelta(
	zc zaloConfig,
	threadID string,
	highWater int64,
) (appZaloResumeDeltaSnapshot, error) {
	session, err := a.st.ZaloCLISession(threadID)
	if errors.Is(err, store.ErrNotFound) {
		return appZaloResumeDeltaSnapshot{known: true}, nil
	}
	if err != nil {
		return appZaloResumeDeltaSnapshot{}, err
	}
	// A fresh or rotating session takes the bounded bootstrap path. Its full
	// prompt intentionally remains the latest-10 history contract.
	if session.TurnCount == 0 || appZaloShouldRotate(
		session, zc.Model, appZaloPromptFingerprint(zc, threadID),
	) {
		return appZaloResumeDeltaSnapshot{known: true}, nil
	}
	snapshot := appZaloResumeDeltaSnapshot{
		claudeSessionID: session.ClaudeSessionID,
		generation:      session.Generation,
		messageCursor:   session.MessageCursor,
		turnCount:       session.TurnCount,
		highWater:       highWater,
		known:           true,
		hasBasis:        true,
	}
	if highWater <= session.MessageCursor {
		return snapshot, nil
	}
	delta, err := a.st.ZaloMessagesAfter(threadID, session.MessageCursor, 100)
	if err != nil {
		return appZaloResumeDeltaSnapshot{}, err
	}
	snapshot.delta = appZaloCloneDelta(
		appZaloDeltaThroughHighWater(delta, highWater),
	)
	return snapshot, nil
}

func (s appZaloResumeDeltaSnapshot) clone() appZaloResumeDeltaSnapshot {
	s.delta = appZaloCloneDelta(s.delta)
	return s
}

func (s appZaloResumeDeltaSnapshot) usableFor(
	session store.ZaloCLISession,
	highWater int64,
) bool {
	return s.known && s.hasBasis && s.highWater == highWater &&
		s.claudeSessionID == session.ClaudeSessionID &&
		s.generation == session.Generation &&
		s.messageCursor == session.MessageCursor &&
		s.turnCount == session.TurnCount
}

func appZaloCloneDelta(delta []store.ZaloDeltaMessage) []store.ZaloDeltaMessage {
	out := slices.Clone(delta)
	for i := range out {
		out[i].Message.Attachments = slices.Clone(out[i].Message.Attachments)
	}
	return out
}

func appZaloDeltaHasAttachments(delta []store.ZaloDeltaMessage) bool {
	return slices.ContainsFunc(delta, func(item store.ZaloDeltaMessage) bool {
		return slices.ContainsFunc(item.Message.Attachments, func(file ipc.ZaloAttachment) bool {
			return file.Path != ""
		})
	})
}

// appZaloMergeRenderedDeltaFiles keeps attachments paired with the durable
// transcript rows visible in this prompt. Direct files for the current turn stay
// first. A pinned current inbound row is visible even when it is beyond the
// bounded contiguous prefix, so its attachments are included without advancing
// the consumed cursor. No maxZaloFiles cap is applied because consuming a body
// while omitting its file would split one durable event.
func appZaloMergeRenderedDeltaFiles(
	files []ipc.ZaloAttachment,
	delta []store.ZaloDeltaMessage,
	consumedCursor int64,
	currentZaloMsgID string,
) []ipc.ZaloAttachment {
	out := slices.Clone(files)
	seen := make(map[string]struct{}, len(out))
	for _, file := range out {
		if file.Path != "" {
			seen[file.Path] = struct{}{}
		}
	}
	for _, item := range delta {
		message := item.Message
		isPinnedCurrent := currentZaloMsgID != "" && message.Direction == ipc.ZaloIn &&
			message.ZaloMsgID == currentZaloMsgID
		if item.ID > consumedCursor && !isPinnedCurrent {
			continue
		}
		for _, file := range message.Attachments {
			if file.Path == "" {
				continue
			}
			if _, exists := seen[file.Path]; exists {
				continue
			}
			seen[file.Path] = struct{}{}
			out = append(out, file)
		}
	}
	return out
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
	var binding appZaloClaudeBinding
	var err error
	run, zc, binding, err = a.appResolveZaloSessionRoute(ctx, run, zc, threadID)
	if err != nil {
		return "", err
	}
	structured, ok := run.(appZaloStructuredRunner)
	if !ok {
		return run.Run(ctx, buildConsultPrompt(zc, question, history, found, files...), step)
	}
	highWater := appZaloHistoryCursor(history)
	resumeDelta, err := a.appCaptureZaloResumeDelta(zc, threadID, highWater)
	if err != nil {
		a.logger.Warn("zalo session: could not capture pending delta; cursor will stay conservative",
			"thread", threadID, "err", err)
	}
	return a.appRunZaloStructured(
		ctx, structured, zc, threadID, question, currentZaloMsgID,
		history, found, files, slices.Clone(files), step, "", highWater, true, resumeDelta,
		binding,
	)
}

func (a *api) appRunZaloStructured(
	ctx context.Context,
	run appZaloStructuredRunner,
	zc zaloConfig,
	threadID, question, currentZaloMsgID string,
	history []ipc.ZaloMessage,
	found []passage,
	files, directFiles []ipc.ZaloAttachment,
	step func(string),
	bootstrapOverride string,
	turnHighWater int64,
	turnHighWaterKnown bool,
	resumeDelta appZaloResumeDeltaSnapshot,
	claudeBinding appZaloClaudeBinding,
) (string, error) {
	selection, err := a.appSelectZaloSession(
		zc, threadID, question, currentZaloMsgID, history, found, files, directFiles,
		bootstrapOverride, turnHighWater, turnHighWaterKnown, resumeDelta, claudeBinding,
	)
	if err != nil {
		return "", err
	}
	result, runErr := run.appRunZaloSession(ctx, appZaloSessionRunInput{
		SessionID:         selection.session.ClaudeSessionID,
		Prompt:            selection.prompt,
		StatelessPrompt:   selection.bootstrapPrompt,
		Resume:            selection.resume,
		RecoverySessionID: uuid.NewString(),
		BootstrapPrompt:   selection.bootstrapPrompt,
	}, step)
	if runErr != nil {
		a.appInvalidateFailedZaloRecovery(selection.session, result)
		return "", runErr
	}
	result.Answer = a.appProcessZaloMemoryAnswer(result.Answer, selection)
	if !result.SessionAdvanced {
		return result.Answer, nil
	}

	recovered := result.Recovered
	freshRecovery := false
	if recovered && result.SessionID != "" && result.SessionID != selection.session.ClaudeSessionID {
		replaced, replacedOK := a.appReplaceRecoveredZaloSession(selection.session, result.SessionID)
		if !replacedOK {
			return result.Answer, nil
		}
		selection.session = replaced
		freshRecovery = true
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
	memorySubjectUID := selection.memorySubjectUID
	memorySubjectRevision := selection.memorySubjectRevision
	memoryCommonRevision := selection.memoryCommonRevision
	if freshRecovery && !selection.memoryOK {
		memorySubjectUID = ""
		memorySubjectRevision = 0
		memoryCommonRevision = 0
	}
	if carrier, ok := ctx.Value(appZaloCompletionContextKey{}).(*appZaloCompletionCarrier); ok {
		carrier.set(appZaloCompletion{
			generation: selection.session.Generation, contextTokens: contextTokens,
			messageCursor:         completionCursor,
			memorySubjectUID:      memorySubjectUID,
			memorySubjectRevision: memorySubjectRevision,
			memoryCommonRevision:  memoryCommonRevision,
			memoryRevision:        selection.memoryRevision,
			lessonsRevision:       selection.lessonsRevision, ready: true,
		})
	}
	return result.Answer, nil
}

func (a *api) appProcessZaloMemoryAnswer(
	raw string,
	selection appZaloSessionSelection,
) string {
	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok {
		return raw
	}
	if len(operations) == 0 || !selection.memoryOK || selection.memorySubject.UID == "" {
		return sanitized
	}
	allowedTargetIDs := make(map[int64]struct{}, len(selection.subjectMemory))
	for _, item := range selection.subjectMemory {
		allowedTargetIDs[item.ID] = struct{}{}
	}
	converted := make([]store.AppMemoryOperation, 0, len(operations))
	for _, operation := range operations {
		converted = append(converted, store.AppMemoryOperation{
			Action: operation.Action, MemoryKey: operation.MemoryKey,
			Value: operation.Value, Category: operation.Category,
			Confidence: operation.Confidence, TargetID: operation.TargetID,
		})
	}
	if _, err := a.st.ApplyAppMemoryOperations(store.AppMemoryApplyInput{
		ThreadID: selection.session.ThreadID, SubjectUID: selection.memorySubject.UID,
		SourceMessageID:  selection.memorySubject.MessageID,
		AllowedTargetIDs: allowedTargetIDs, Operations: converted, Now: time.Now(),
	}); err != nil {
		a.logger.Warn("zalo session: Memory operation apply failed",
			"thread", selection.session.ThreadID, "code", appZaloErrorMemoryApply,
			"error_type", fmt.Sprintf("%T", err))
	}
	return sanitized
}

func (a *api) appSelectZaloSession(
	zc zaloConfig,
	threadID, question, currentZaloMsgID string,
	history []ipc.ZaloMessage,
	found []passage,
	files, directFiles []ipc.ZaloAttachment,
	bootstrapOverride string,
	turnHighWater int64,
	turnHighWaterKnown bool,
	resumeDelta appZaloResumeDeltaSnapshot,
	claudeBinding appZaloClaudeBinding,
) (appZaloSessionSelection, error) {
	_ = bootstrapOverride
	fingerprint := appZaloPromptFingerprint(zc, threadID)
	memorySubject := a.appResolveZaloMemorySubject(threadID, currentZaloMsgID)
	snapshot := a.appLoadZaloMemorySnapshot(threadID, memorySubject.UID)
	bootstrapConfig := zc
	bootstrapConfig.Memory = nil
	bootstrapConfig.Lessons = nil
	if snapshot.lessonsOK {
		bootstrapConfig.Lessons = snapshot.lessons
	}
	bootstrapCursor := int64(0)
	if turnHighWaterKnown {
		bootstrapCursor = turnHighWater
	}
	bootstrap := buildConsultPrompt(bootstrapConfig, question, history, found, files...)
	if snapshot.memoryOK {
		fullMemory := appZaloRenderMemoryRefresh(&appZaloMemoryRefresh{
			Common: snapshot.common, Subject: snapshot.subject,
			ReplaceCommon: true, ReplaceSubject: true,
			CommonRevision:  snapshot.commonRevision,
			SubjectRevision: snapshot.subjectRevision,
		})
		if fullMemory != "" {
			bootstrap = strings.TrimRight(bootstrap, "\r\n") + "\n\n" + fullMemory
		}
	}
	bootstrap = appZaloAppendMemoryContract(bootstrap)
	bootstrapSelection := func(session store.ZaloCLISession) appZaloSessionSelection {
		selection := appZaloSessionSelection{
			session: session, prompt: bootstrap, bootstrapPrompt: bootstrap,
			completionCursor: bootstrapCursor, bootstrapCursor: bootstrapCursor,
			memorySubject: memorySubject, subjectMemory: snapshot.subject,
			memoryRevision: session.MemoryRevision, memoryOK: snapshot.memoryOK,
		}
		if snapshot.memoryOK {
			selection.memorySubjectUID = memorySubject.UID
			selection.memorySubjectRevision = snapshot.subjectRevision
			selection.memoryCommonRevision = snapshot.commonRevision
		}
		if snapshot.lessonsOK {
			selection.lessonsRevision = snapshot.lessonsRevision
		} else {
			selection.lessonsRevision = session.LessonsRevision
		}
		return selection
	}
	session, err := a.st.ZaloCLISession(threadID)
	if errors.Is(err, store.ErrNotFound) {
		created, createErr := a.st.CreateZaloCLISession(store.ZaloCLISession{
			ThreadID: threadID, ClaudeSessionID: uuid.NewString(), Model: zc.Model,
			PromptFingerprint: fingerprint,
			ClaudeAccountID:   claudeBinding.AccountID,
			ClaudeConfigDir:   claudeBinding.ConfigDir,
		})
		if createErr == nil {
			return bootstrapSelection(created), nil
		}
		// Another process may have inserted the mapping. Reload once and never
		// overwrite the generation it won.
		session, err = a.appReloadZaloSessionAfterConflict(
			zc, threadID, fingerprint, claudeBinding,
		)
		if err != nil {
			return appZaloSessionSelection{}, err
		}
	} else if err != nil {
		return appZaloSessionSelection{}, err
	}

	if appZaloShouldRotate(session, zc.Model, fingerprint) ||
		appZaloClaudeBindingChanged(session, claudeBinding) {
		next := session
		next.ClaudeSessionID = uuid.NewString()
		next.Model = zc.Model
		next.PromptFingerprint = fingerprint
		next.ClaudeAccountID = claudeBinding.AccountID
		next.ClaudeConfigDir = claudeBinding.ConfigDir
		next.ContextTokens = 0
		next.TurnCount = 0
		next.MemoryRevision = 0
		next.LessonsRevision = 0
		next.RotateBeforeNext = false
		next.LastError = ""
		replaced, replaceErr := a.st.ReplaceZaloCLISession(session.Generation, next)
		if replaceErr == nil {
			return bootstrapSelection(replaced), nil
		}
		if !errors.Is(replaceErr, store.ErrZaloCLISessionConflict) {
			return appZaloSessionSelection{}, replaceErr
		}
		// A concurrent generation is authoritative. Reload exactly once and use
		// it without attempting another replacement.
		session, err = a.appReloadZaloSessionAfterConflict(
			zc, threadID, fingerprint, claudeBinding,
		)
		if err != nil {
			return appZaloSessionSelection{}, err
		}
	}
	if session.TurnCount == 0 {
		return bootstrapSelection(session), nil
	}

	var delta []store.ZaloDeltaMessage
	if turnHighWaterKnown && turnHighWater > session.MessageCursor &&
		resumeDelta.usableFor(session, turnHighWater) {
		delta = appZaloCloneDelta(resumeDelta.delta)
	}
	refresh := &appZaloMemoryRefresh{}
	memorySubjectUID := session.MemorySubjectUID
	memorySubjectRevision := session.MemorySubjectRevision
	memoryCommonRevision := session.MemoryCommonRevision
	if snapshot.memoryOK {
		memorySubjectUID = memorySubject.UID
		memorySubjectRevision = snapshot.subjectRevision
		memoryCommonRevision = snapshot.commonRevision
		refresh.ReplaceSubject = memorySubject.UID != session.MemorySubjectUID ||
			snapshot.subjectRevision != session.MemorySubjectRevision
		refresh.ReplaceCommon = snapshot.commonRevision != session.MemoryCommonRevision
		refresh.Subject = snapshot.subject
		refresh.Common = snapshot.common
		refresh.SubjectRevision = snapshot.subjectRevision
		refresh.CommonRevision = snapshot.commonRevision
	} else if memorySubject.UID != session.MemorySubjectUID {
		// The next speaker is trusted, but their snapshot is unreadable. Remove
		// the resident prior speaker rather than exposing it across the turn.
		// Persist an empty subject cursor so every later trusted speaker must
		// refresh from a readable authoritative snapshot.
		memorySubjectUID = ""
		memorySubjectRevision = 0
		refresh.ReplaceSubject = true
		refresh.Subject = nil
		refresh.SubjectRevision = 0
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
	if !refresh.ReplaceCommon && !refresh.ReplaceSubject && !refresh.ReplaceLessons {
		refresh = nil
	}
	promptInput := appZaloSessionPromptInput{
		Config: zc, Question: question, CurrentZaloMsgID: currentZaloMsgID,
		History: history, Delta: delta, Found: found,
		Files: slices.Clone(directFiles), MemoryRefresh: refresh,
	}
	// Select the bounded contiguous transcript before deriving its file set. The
	// files section does not affect transcript selection, so rebuilding with only
	// consumed (plus pinned-current) row attachments must keep the cursor stable.
	deltaPrompt := buildAppZaloDeltaPromptResult(promptInput)
	promptInput.Files = appZaloMergeRenderedDeltaFiles(
		directFiles, delta, deltaPrompt.ConsumedCursor, currentZaloMsgID,
	)
	finalPrompt := buildAppZaloDeltaPromptResult(promptInput)
	if finalPrompt.ConsumedCursor != deltaPrompt.ConsumedCursor {
		return appZaloSessionSelection{}, errors.New(
			"zalo session: delta prompt selection changed while attaching files",
		)
	}
	deltaPrompt = finalPrompt
	completionCursor := session.MessageCursor
	if deltaPrompt.ConsumedCursor > completionCursor {
		completionCursor = deltaPrompt.ConsumedCursor
	}
	return appZaloSessionSelection{
		session: session, prompt: deltaPrompt.Prompt, bootstrapPrompt: bootstrap,
		completionCursor: completionCursor, bootstrapCursor: bootstrapCursor,
		memorySubject: memorySubject, subjectMemory: snapshot.subject,
		memorySubjectUID:      memorySubjectUID,
		memorySubjectRevision: memorySubjectRevision,
		memoryCommonRevision:  memoryCommonRevision,
		memoryRevision:        session.MemoryRevision, lessonsRevision: lessonsRevision,
		memoryOK: snapshot.memoryOK, resume: true,
	}, nil
}

// appZaloDeltaThroughHighWater keeps the oldest-first durable delta inside the
// exact message boundary captured with this turn's history and files. IDs are
// monotonically increasing, so the first row above the bound ends the prefix.
func appZaloDeltaThroughHighWater(
	delta []store.ZaloDeltaMessage,
	highWater int64,
) []store.ZaloDeltaMessage {
	end := len(delta)
	for i, item := range delta {
		if item.ID > highWater {
			end = i
			break
		}
	}
	return slices.Clone(delta[:end])
}

func (a *api) appResolveZaloMemorySubject(threadID, currentZaloMsgID string) store.AppMemorySubject {
	if strings.TrimSpace(currentZaloMsgID) == "" {
		return store.AppMemorySubject{}
	}
	subject, err := a.st.AppInboundMemorySubject(threadID, currentZaloMsgID)
	if err != nil {
		a.logger.Warn("zalo session: could not resolve trusted Memory subject",
			"thread", threadID, "code", appZaloErrorMemoryRead,
			"error_type", fmt.Sprintf("%T", err))
		return store.AppMemorySubject{}
	}
	return subject
}

func (a *api) appLoadZaloMemorySnapshot(threadID, subjectUID string) appZaloMemorySnapshot {
	var snapshot appZaloMemorySnapshot
	memory, err := a.st.AppPromptMemoryForSubject(
		threadID, subjectUID, zaloMemoryLines, time.Now(),
	)
	if err != nil {
		a.logger.Warn("zalo session: could not read Memory snapshot",
			"thread", threadID, "code", appZaloErrorMemoryRead,
			"error_type", fmt.Sprintf("%T", err))
	} else {
		snapshot.common = memory.Common
		snapshot.subject = memory.Subject
		snapshot.commonRevision = memory.CommonRevision
		snapshot.subjectRevision = memory.SubjectRevision
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
	claudeBinding appZaloClaudeBinding,
) (store.ZaloCLISession, error) {
	session, err := a.st.ZaloCLISession(threadID)
	if err != nil {
		return store.ZaloCLISession{}, err
	}
	if appZaloShouldRotate(session, zc.Model, fingerprint) ||
		appZaloClaudeBindingChanged(session, claudeBinding) {
		return store.ZaloCLISession{}, errAppZaloSessionStateUpdate
	}
	return session, nil
}

func appZaloClaudeBindingChanged(
	session store.ZaloCLISession,
	binding appZaloClaudeBinding,
) bool {
	switch binding.State {
	case appZaloClaudeBindingUnavailable:
		return session.ClaudeAccountID != "" || session.ClaudeConfigDir != ""
	case appZaloClaudeBindingSelected:
		return session.ClaudeAccountID != binding.AccountID ||
			session.ClaudeConfigDir != binding.ConfigDir
	default:
		return false
	}
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
		pending.memorySubjectUID, pending.memorySubjectRevision, pending.memoryCommonRevision,
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
