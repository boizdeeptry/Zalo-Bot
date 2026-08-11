package daemon

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

const appZaloThreadGateTestTimeout = time.Second

func TestAppZaloThreadGateSerializesSameThread(t *testing.T) {
	var gate appZaloThreadGate
	firstRelease, err := gate.Acquire(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}

	secondEntered := make(chan func(), 1)
	secondErr := make(chan error, 1)
	go func() {
		release, acquireErr := gate.Acquire(context.Background(), "thread-a")
		if acquireErr != nil {
			secondErr <- acquireErr
			return
		}
		secondEntered <- release
	}()
	waitForAppZaloThreadGateRefs(t, &gate, "thread-a", 2)

	select {
	case release := <-secondEntered:
		release()
		t.Fatal("second turn entered before the first turn released")
	case err := <-secondErr:
		t.Fatalf("second acquire failed: %v", err)
	default:
	}

	firstRelease()
	select {
	case release := <-secondEntered:
		release()
	case err := <-secondErr:
		t.Fatalf("second acquire failed after release: %v", err)
	case <-time.After(appZaloThreadGateTestTimeout):
		t.Fatal("second turn did not enter after the first turn released")
	}
	waitForAppZaloThreadGateLen(t, &gate, 0)
}

func TestAppZaloThreadGateAllowsDifferentThreadsConcurrently(t *testing.T) {
	var gate appZaloThreadGate
	releaseA, err := gate.Acquire(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}

	threadBEntered := make(chan func(), 1)
	threadBErr := make(chan error, 1)
	go func() {
		release, acquireErr := gate.Acquire(context.Background(), "thread-b")
		if acquireErr != nil {
			threadBErr <- acquireErr
			return
		}
		threadBEntered <- release
	}()

	var releaseB func()
	select {
	case releaseB = <-threadBEntered:
	case err := <-threadBErr:
		t.Fatalf("thread-b acquire failed: %v", err)
	case <-time.After(appZaloThreadGateTestTimeout):
		t.Fatal("thread-b was blocked while thread-a was held")
	}

	releaseA()
	releaseB()
	waitForAppZaloThreadGateLen(t, &gate, 0)
}

func TestAppZaloThreadGateCanceledWaiterIsReclaimed(t *testing.T) {
	var gate appZaloThreadGate
	holderRelease, err := gate.Acquire(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}

	waitContext, cancel := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() {
		_, acquireErr := gate.Acquire(waitContext, "thread-a")
		waiterErr <- acquireErr
	}()
	waitForAppZaloThreadGateRefs(t, &gate, "thread-a", 2)
	cancel()

	select {
	case err := <-waiterErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v; want context.Canceled", err)
		}
	case <-time.After(appZaloThreadGateTestTimeout):
		t.Fatal("canceled waiter did not return")
	}
	waitForAppZaloThreadGateRefs(t, &gate, "thread-a", 1)

	holderRelease()
	waitForAppZaloThreadGateLen(t, &gate, 0)
}

func TestAppZaloThreadGateReleaseIsIdempotent(t *testing.T) {
	var gate appZaloThreadGate
	release, err := gate.Acquire(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}

	release()
	release()
	waitForAppZaloThreadGateLen(t, &gate, 0)
}

func TestAppZaloThreadGateRejectsEmptyThreadID(t *testing.T) {
	var gate appZaloThreadGate
	release, err := gate.Acquire(context.Background(), "")
	if err == nil {
		if release != nil {
			release()
		}
		t.Fatal("empty thread ID was accepted")
	}
	if release != nil {
		t.Fatal("empty thread ID returned a release function")
	}
	if got := gate.Len(); got != 0 {
		t.Fatalf("gate length = %d after empty thread ID; want 0", got)
	}
}

func waitForAppZaloThreadGateLen(t *testing.T, gate *appZaloThreadGate, want int) {
	t.Helper()
	deadline := time.Now().Add(appZaloThreadGateTestTimeout)
	for {
		if got := gate.Len(); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate length = %d; want %d", gate.Len(), want)
		}
		runtime.Gosched()
	}
}

func waitForAppZaloThreadGateRefs(t *testing.T, gate *appZaloThreadGate, threadID string, want int) {
	t.Helper()
	deadline := time.Now().Add(appZaloThreadGateTestTimeout)
	for {
		gate.mu.Lock()
		got := 0
		if entry := gate.entries[threadID]; entry != nil {
			got = entry.refs
		}
		gate.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate refs for %q = %d; want %d", threadID, got, want)
		}
		runtime.Gosched()
	}
}
