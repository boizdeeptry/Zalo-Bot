package daemon

import (
	"context"
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

func TestManagedCLIProcessQuiesceWaitsForProcessGroupZero(t *testing.T) {
	kills := 0
	probes := 0
	process := &processGroupManagedCLIProcess{
		pgid: 42,
		kill: func(int) error {
			kills++
			return nil
		},
		active: func(int) (bool, error) {
			probes++
			return probes < 3, nil
		},
		poll: func(context.Context) error { return nil },
	}

	if err := process.quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if kills != 1 || probes != 3 {
		t.Fatalf("kill/probe calls = %d/%d; want 1/3", kills, probes)
	}
	if err := process.close(); err != nil {
		t.Fatalf("close after quiesce = %v", err)
	}
	if kills != 1 {
		t.Fatalf("kill calls after close = %d; want 1", kills)
	}
}

func TestManagedCLIProcessQuiesceHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	process := &processGroupManagedCLIProcess{
		pgid:   42,
		kill:   func(int) error { return nil },
		active: func(int) (bool, error) { return true, nil },
		poll: func(ctx context.Context) error {
			return ctx.Err()
		},
	}

	if err := process.quiesce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("quiesce error = %v; want context.Canceled", err)
	}
}
