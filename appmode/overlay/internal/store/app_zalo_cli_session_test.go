package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"agentdc/internal/ipc"
)

func TestZaloCLISessionLifecycleIsIndependentPerThread(t *testing.T) {
	s := newStore(t)

	if _, err := s.ZaloCLISession("user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ZaloCLISession(missing) = %v; want ErrNotFound", err)
	}

	user, err := s.CreateZaloCLISession(ZaloCLISession{
		ThreadID:          "user-1",
		ClaudeSessionID:   "claude-user-1",
		Generation:        77,
		Model:             "claude-haiku",
		PromptFingerprint: "persona-v1",
		MessageCursor:     7,
	})
	if err != nil {
		t.Fatalf("CreateZaloCLISession(user): %v", err)
	}
	if user.Generation != 1 {
		t.Fatalf("created generation = %d; want 1", user.Generation)
	}
	if user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() {
		t.Fatalf("created timestamps must be populated: %#v", user)
	}

	group, err := s.CreateZaloCLISession(ZaloCLISession{
		ThreadID:          "group-1",
		ClaudeSessionID:   "claude-group-1",
		Model:             "claude-sonnet",
		PromptFingerprint: "persona-v1",
	})
	if err != nil {
		t.Fatalf("CreateZaloCLISession(group): %v", err)
	}
	if group.ClaudeSessionID == user.ClaudeSessionID {
		t.Fatal("different user/group threads shared a Claude session")
	}

	next := user
	next.ClaudeSessionID = "claude-user-2"
	next.Model = "claude-sonnet"
	next.PromptFingerprint = "persona-v2"
	next.ContextTokens = 0
	next.TurnCount = 0
	next.MessageCursor = 4
	next.RotateBeforeNext = false
	next.LastError = ""
	if _, err := s.ReplaceZaloCLISession(0, next); !errors.Is(err, ErrZaloCLISessionConflict) {
		t.Fatalf("ReplaceZaloCLISession(stale) = %v; want ErrZaloCLISessionConflict", err)
	}
	replaced, err := s.ReplaceZaloCLISession(1, next)
	if err != nil {
		t.Fatalf("ReplaceZaloCLISession: %v", err)
	}
	if replaced.Generation != 2 || replaced.ClaudeSessionID != "claude-user-2" || replaced.MessageCursor != 7 {
		t.Fatalf("replaced session = %#v; want generation 2, claude-user-2, cursor still 7", replaced)
	}

	unchangedGroup, err := s.ZaloCLISession("group-1")
	if err != nil {
		t.Fatalf("ZaloCLISession(group): %v", err)
	}
	if unchangedGroup.Generation != 1 || unchangedGroup.ClaudeSessionID != "claude-group-1" {
		t.Fatalf("group session changed with user session: %#v", unchangedGroup)
	}

	completed, err := s.CompleteZaloCLITurn("user-1", 2, 8_192, 12)
	if err != nil {
		t.Fatalf("CompleteZaloCLITurn(first): %v", err)
	}
	if completed.ContextTokens != 8_192 || completed.TurnCount != 1 || completed.MessageCursor != 12 {
		t.Fatalf("completed session = %#v; want tokens=8192 turn=1 cursor=12", completed)
	}
	completed, err = s.CompleteZaloCLITurn("user-1", 2, 9_000, 5)
	if err != nil {
		t.Fatalf("CompleteZaloCLITurn(lower cursor): %v", err)
	}
	if completed.ContextTokens != 9_000 || completed.TurnCount != 2 || completed.MessageCursor != 12 {
		t.Fatalf("completed session = %#v; want tokens=9000 turn=2 cursor still 12", completed)
	}
	if _, err := s.CompleteZaloCLITurn("user-1", 1, 10_000, 13); !errors.Is(err, ErrZaloCLISessionConflict) {
		t.Fatalf("CompleteZaloCLITurn(stale) = %v; want ErrZaloCLISessionConflict", err)
	}

	if err := s.MarkZaloCLISessionForRotation("user-1", 1, "stale"); !errors.Is(err, ErrZaloCLISessionConflict) {
		t.Fatalf("MarkZaloCLISessionForRotation(stale) = %v; want ErrZaloCLISessionConflict", err)
	}
	if err := s.MarkZaloCLISessionForRotation("user-1", 2, "context threshold"); err != nil {
		t.Fatalf("MarkZaloCLISessionForRotation: %v", err)
	}
	got, err := s.ZaloCLISession("user-1")
	if err != nil {
		t.Fatalf("ZaloCLISession(user): %v", err)
	}
	if !got.RotateBeforeNext || got.LastError != "context threshold" {
		t.Fatalf("marked session = %#v; want rotation with last error", got)
	}
}

func TestZaloMessagesAfterReturnsContentDeltaOldestFirstWithAttachments(t *testing.T) {
	s := newStore(t)
	if got, err := s.LatestZaloMessageID("thread-1"); err != nil || got != 0 {
		t.Fatalf("LatestZaloMessageID(empty) = %d, %v; want 0, nil", got, err)
	}

	add := func(threadID, body string, attachment bool) int64 {
		t.Helper()
		m := ipc.ZaloMessage{
			ThreadID:  threadID,
			Direction: "in",
			Author:    "Khach",
			Body:      body,
			CreatedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC),
		}
		if attachment {
			m.Attachments = []ipc.ZaloAttachment{{Kind: "chat.photo", Path: `D:\Zalo\photo.jpg`, Title: "photo.jpg"}}
		}
		if err := s.AddZaloMessage(m); err != nil {
			t.Fatalf("AddZaloMessage(%q): %v", body, err)
		}
		id, err := s.LatestZaloMessageID(threadID)
		if err != nil {
			t.Fatalf("LatestZaloMessageID(%q): %v", threadID, err)
		}
		return id
	}

	firstID := add("thread-1", "mot", true)
	add("other-thread", "khong duoc lo vao", false)
	secondID := add("thread-1", "hai", false)
	thirdID := add("thread-1", "ba", false)

	all, err := s.ZaloMessagesAfter("thread-1", 0, 0)
	if err != nil {
		t.Fatalf("ZaloMessagesAfter(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ZaloMessagesAfter(all) length = %d; want 3", len(all))
	}
	if all[0].ID != firstID || all[1].ID != secondID || all[2].ID != thirdID {
		t.Fatalf("delta IDs = %d, %d, %d; want %d, %d, %d", all[0].ID, all[1].ID, all[2].ID, firstID, secondID, thirdID)
	}
	if len(all[0].Message.Attachments) != 1 || all[0].Message.Attachments[0].Title != "photo.jpg" {
		t.Fatalf("first delta attachments = %#v; want photo.jpg", all[0].Message.Attachments)
	}

	delta, err := s.ZaloMessagesAfter("thread-1", firstID, 10)
	if err != nil {
		t.Fatalf("ZaloMessagesAfter(cursor): %v", err)
	}
	if len(delta) != 2 || delta[0].Message.Body != "hai" || delta[1].Message.Body != "ba" {
		t.Fatalf("delta after %d = %#v; want hai then ba", firstID, delta)
	}
	if latest, err := s.LatestZaloMessageID("thread-1"); err != nil || latest != thirdID {
		t.Fatalf("LatestZaloMessageID = %d, %v; want %d, nil", latest, err, thirdID)
	}

	for i := 0; i < 105; i++ {
		add("thread-1", fmt.Sprintf("bulk-%03d", i), false)
	}
	capped, err := s.ZaloMessagesAfter("thread-1", thirdID, 1_000)
	if err != nil {
		t.Fatalf("ZaloMessagesAfter(capped): %v", err)
	}
	if len(capped) != 100 {
		t.Fatalf("capped delta length = %d; want 100", len(capped))
	}
	for i := 1; i < len(capped); i++ {
		if capped[i-1].ID >= capped[i].ID {
			t.Fatalf("delta is not oldest first at %d: %d then %d", i, capped[i-1].ID, capped[i].ID)
		}
	}
}
