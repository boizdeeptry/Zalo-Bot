package daemon

import (
	"errors"
	"testing"
)

func TestProcessGroupLifetimeKillsExactlyOnceAndPreservesError(t *testing.T) {
	wantErr := errors.New("kill process group test failure")
	calls := 0
	gotPGID := 0
	var process managedCLIProcessLifetime = &processGroupManagedCLIProcess{
		pgid: 42,
		kill: func(pgid int) error {
			calls++
			gotPGID = pgid
			return wantErr
		},
	}

	if err := process.terminate(); !errors.Is(err, wantErr) {
		t.Fatalf("terminate error = %v; want %v", err, wantErr)
	}
	if err := process.close(); !errors.Is(err, wantErr) {
		t.Fatalf("close error = %v; want cached %v", err, wantErr)
	}
	if calls != 1 || gotPGID != 42 {
		t.Fatalf("kill calls/pgid = %d/%d; want 1/42", calls, gotPGID)
	}
}
