package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"agentdc/internal/ipc"
)

func addAppMemorySubjectMessage(
	t *testing.T,
	s *Store,
	threadID, direction, uid, zaloMsgID string,
) int64 {
	t.Helper()
	if err := s.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: threadID, Direction: direction, Author: "display-only",
		AuthorUID: uid, ZaloMsgID: zaloMsgID, Body: "private content",
		CreatedAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("add subject message: %v", err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM zalo_messages
WHERE thread_id = ? AND zalo_msgid = ?`, threadID, zaloMsgID).Scan(&id); err != nil {
		t.Fatalf("read subject message ID: %v", err)
	}
	return id
}

func TestAppInboundMemorySubjectUsesExactDurableInboundIdentity(t *testing.T) {
	s := newStore(t)
	for _, thread := range []ipc.ZaloThreadInfo{
		{ID: "group-a", Name: "Group A", ThreadType: ipc.ZaloThreadGroup},
		{ID: "group-b", Name: "Group B", ThreadType: ipc.ZaloThreadGroup},
	} {
		if err := s.UpsertZaloThreadInfo(thread); err != nil {
			t.Fatal(err)
		}
	}
	wantID := addAppMemorySubjectMessage(t, s, "group-a", ipc.ZaloIn, "u-1", "msg-a")
	addAppMemorySubjectMessage(t, s, "group-a", ipc.ZaloOut, "model-uid", "msg-out")
	addAppMemorySubjectMessage(t, s, "group-b", ipc.ZaloIn, "u-1", "msg-b")

	got, err := s.AppInboundMemorySubject("group-a", "msg-a")
	if err != nil {
		t.Fatal(err)
	}
	if got != (AppMemorySubject{MessageID: wantID, UID: "u-1", ThreadType: ipc.ZaloThreadGroup}) {
		t.Fatalf("subject = %#v", got)
	}

	for _, zaloMsgID := range []string{"msg-out", "msg-b", "missing"} {
		got, err := s.AppInboundMemorySubject("group-a", zaloMsgID)
		if err != nil {
			t.Fatalf("resolve %q: %v", zaloMsgID, err)
		}
		if got != (AppMemorySubject{}) {
			t.Fatalf("untrusted %q subject = %#v; want zero outcome", zaloMsgID, got)
		}
	}
}

func TestAppInboundMemorySubjectDoesNotInventGroupUID(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Group A", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	wantID := addAppMemorySubjectMessage(t, s, "group-a", ipc.ZaloIn, "", "msg-group")

	got, err := s.AppInboundMemorySubject("group-a", "msg-group")
	if err != nil {
		t.Fatal(err)
	}
	if got != (AppMemorySubject{MessageID: wantID, ThreadType: ipc.ZaloThreadGroup}) {
		t.Fatalf("group subject = %#v; want durable row without invented UID", got)
	}
}

func TestAppInboundMemorySubjectFallsBackOnlyForPrivateThread(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "user-1", Name: "User", ThreadType: ipc.ZaloThreadUser,
	}); err != nil {
		t.Fatal(err)
	}
	wantID := addAppMemorySubjectMessage(t, s, "user-1", ipc.ZaloIn, "", "msg-user")

	got, err := s.AppInboundMemorySubject("user-1", "msg-user")
	if err != nil {
		t.Fatal(err)
	}
	if got != (AppMemorySubject{
		MessageID: wantID, UID: "user-1", ThreadType: ipc.ZaloThreadUser,
	}) {
		t.Fatalf("private subject = %#v", got)
	}
}

func TestAppInboundMemorySubjectValidatesOpaqueIDsWithoutLeakingThem(t *testing.T) {
	s := newStore(t)
	secret := strings.Repeat("s", maxZaloMemoryLen+1)
	for _, input := range [][2]string{{"", "msg"}, {"thread", ""}, {secret, "msg"}, {"thread", secret}} {
		_, err := s.AppInboundMemorySubject(input[0], input[1])
		if !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("AppInboundMemorySubject(%d,%d) error = %v; want ErrAppMemoryInvalid",
				len(input[0]), len(input[1]), err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validation error leaked opaque ID: %v", err)
		}
	}
}
