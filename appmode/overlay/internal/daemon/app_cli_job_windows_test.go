//go:build windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type testJobProcessList struct {
	assigned uint32
	inList   uint32
	pids     [64]uintptr
}

func TestQueryCLIJobActiveProcessesUsesBasicAccountingInformation(t *testing.T) {
	original := queryCLIJobInformation
	t.Cleanup(func() { queryCLIJobInformation = original })

	queryCLIJobInformation = func(
		job windows.Handle,
		class int32,
		information uintptr,
		length uint32,
		_ *uint32,
	) error {
		if job != windows.Handle(42) {
			t.Fatalf("job = %d; want 42", job)
		}
		if class != windows.JobObjectBasicAccountingInformation {
			t.Fatalf("information class = %d; want %d", class, windows.JobObjectBasicAccountingInformation)
		}
		if want := uint32(unsafe.Sizeof(cliJobBasicAccountingInformation{})); length != want {
			t.Fatalf("information length = %d; want %d", length, want)
		}
		info := (*cliJobBasicAccountingInformation)(unsafe.Pointer(information))
		info.activeProcesses = 7
		return nil
	}

	got, err := queryWindowsCLIJobActiveProcesses(windows.Handle(42))

	if err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Fatalf("active processes = %d; want 7", got)
	}
}

func TestWindowsManagedCLIProcessQuiesceWaitsForZeroThenClosesOnce(t *testing.T) {
	originalTerminate := terminateCLIJob
	originalQuery := queryCLIJobActiveProcesses
	originalPoll := pollCLIJobQuiescence
	originalClose := closeCLIJobHandle
	t.Cleanup(func() {
		terminateCLIJob = originalTerminate
		queryCLIJobActiveProcesses = originalQuery
		pollCLIJobQuiescence = originalPoll
		closeCLIJobHandle = originalClose
	})

	var events []string
	terminateCLIJob = func(windows.Handle, uint32) error {
		events = append(events, "terminate")
		return nil
	}
	counts := []uint32{2, 1, 0}
	queryCLIJobActiveProcesses = func(windows.Handle) (uint32, error) {
		count := counts[0]
		counts = counts[1:]
		events = append(events, fmt.Sprintf("query:%d", count))
		return count, nil
	}
	pollCLIJobQuiescence = func(context.Context) error {
		events = append(events, "poll")
		return nil
	}
	closes := 0
	closeCLIJobHandle = func(windows.Handle) error {
		closes++
		events = append(events, "close")
		return nil
	}
	process := &windowsManagedCLIProcess{job: windows.Handle(42)}

	if err := process.quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := process.close(); err != nil {
		t.Fatal(err)
	}

	want := "terminate,query:2,poll,query:1,poll,query:0,close"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events = %q; want %q", got, want)
	}
	if closes != 1 {
		t.Fatalf("job handle closes = %d; want 1", closes)
	}
}

func TestWindowsManagedCLIProcessQuiesceClosesAfterQueryError(t *testing.T) {
	wantErr := errors.New("query job test failure")
	events := installCLIJobQuiesceTestHooks(t)
	queryCLIJobActiveProcesses = func(windows.Handle) (uint32, error) {
		*events = append(*events, "query")
		return 0, wantErr
	}

	err := (&windowsManagedCLIProcess{job: windows.Handle(42)}).quiesce(t.Context())

	if !errors.Is(err, wantErr) {
		t.Fatalf("quiesce error = %v; want %v", err, wantErr)
	}
	if got, want := strings.Join(*events, ","), "terminate,query,close"; got != want {
		t.Fatalf("events = %q; want %q", got, want)
	}
}

func TestWindowsManagedCLIProcessQuiesceClosesAfterTerminateError(t *testing.T) {
	wantErr := errors.New("terminate job test failure")
	events := installCLIJobQuiesceTestHooks(t)
	terminateCLIJob = func(windows.Handle, uint32) error {
		*events = append(*events, "terminate")
		return wantErr
	}

	err := (&windowsManagedCLIProcess{job: windows.Handle(42)}).quiesce(t.Context())

	if !errors.Is(err, wantErr) {
		t.Fatalf("quiesce error = %v; want %v", err, wantErr)
	}
	if got, want := strings.Join(*events, ","), "terminate,close"; got != want {
		t.Fatalf("events = %q; want %q", got, want)
	}
}

func TestWindowsManagedCLIProcessQuiesceClosesOnContextDeadline(t *testing.T) {
	events := installCLIJobQuiesceTestHooks(t)
	queryCLIJobActiveProcesses = func(windows.Handle) (uint32, error) {
		*events = append(*events, "query")
		return 1, nil
	}
	pollCLIJobQuiescence = waitForCLIJobQuiescencePoll
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	err := (&windowsManagedCLIProcess{job: windows.Handle(42)}).quiesce(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quiesce error = %v; want context deadline", err)
	}
	if got, want := strings.Join(*events, ","), "terminate,query,close"; got != want {
		t.Fatalf("events = %q; want %q", got, want)
	}
}

func installCLIJobQuiesceTestHooks(t *testing.T) *[]string {
	t.Helper()
	originalTerminate := terminateCLIJob
	originalQuery := queryCLIJobActiveProcesses
	originalPoll := pollCLIJobQuiescence
	originalClose := closeCLIJobHandle
	t.Cleanup(func() {
		terminateCLIJob = originalTerminate
		queryCLIJobActiveProcesses = originalQuery
		pollCLIJobQuiescence = originalPoll
		closeCLIJobHandle = originalClose
	})

	events := []string{}
	terminateCLIJob = func(windows.Handle, uint32) error {
		events = append(events, "terminate")
		return nil
	}
	queryCLIJobActiveProcesses = func(windows.Handle) (uint32, error) {
		events = append(events, "query")
		return 0, nil
	}
	pollCLIJobQuiescence = func(context.Context) error {
		events = append(events, "poll")
		return nil
	}
	closeCLIJobHandle = func(windows.Handle) error {
		events = append(events, "close")
		return nil
	}
	return &events
}

func TestCLIJobContainsDetachedGrandchild(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node không có trên PATH")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "tree.js")
	pidFile := filepath.Join(dir, "child.pid")
	js := `const {spawn}=require("child_process");const fs=require("fs");` +
		`const c=spawn(process.execPath,["-e","setInterval(()=>{},1e9)"],{stdio:"ignore",detached:true});` +
		`c.unref();fs.writeFileSync(process.argv[2],String(c.pid));setInterval(()=>{},1e9);`
	if err := os.WriteFile(script, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script, pidFile)
	process, err := startManagedCLIProcess(cmd, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	grandchild := readGrandchildPID(t, pidFile)
	t.Cleanup(func() {
		_ = process.terminate()
		_ = killPidTree(grandchild, "test-cleanup", slog.New(slog.DiscardHandler))
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})

	job := process.(*windowsManagedCLIProcess).job
	var list testJobProcessList
	if err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)),
		uint32(unsafe.Sizeof(list)),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pid := range list.pids[:list.inList] {
		if pid == uintptr(grandchild) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("detached grandchild %d absent from job pids %v", grandchild, list.pids[:list.inList])
	}
	if err := process.terminate(); err != nil {
		t.Fatal(err)
	}
	<-done
	if stillAlive(grandchild, 3*time.Second) {
		t.Fatalf("job-contained detached grandchild %d survived job close", grandchild)
	}
}

// A Windows Job Object contains descendants even when Node creates a detached
// process. Both cancellation and normal root completion must close that Job so
// no detached descendant is left behind.
func TestWindowsCLIRunKillsDetachedTree(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node không có trên PATH")
	}
	for _, tc := range []struct {
		name       string
		parentMode string
		cancel     bool
	}{
		{name: "context canceled", parentMode: "wait", cancel: true},
		{name: "root completes", parentMode: "exit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "tree.js")
			pidFile := filepath.Join(dir, "child.pid")
			js := `const {spawn}=require("child_process");const fs=require("fs");` +
				`const c=spawn(process.execPath,["-e","setInterval(()=>{},1e9)"],{stdio:"ignore",detached:true});` +
				`c.unref();fs.writeFileSync(process.argv[2],String(c.pid));` +
				`if(process.argv[3]!=="exit")setInterval(()=>{},1e9);`
			if err := os.WriteFile(script, []byte(js), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = runCLIProcess(ctx, exec.Command(node, script, pidFile, tc.parentMode), nil, nil, slog.New(slog.DiscardHandler))
			}()

			grandchild := readGrandchildPID(t, pidFile)
			t.Cleanup(func() { _ = killPidTree(grandchild, "test-cleanup", slog.New(slog.DiscardHandler)) })
			if tc.cancel {
				cancel()
			}
			<-done
			if stillAlive(grandchild, 3*time.Second) {
				t.Fatalf("detached grandchild %d survived Windows Job completion", grandchild)
			}
		})
	}
}

func recordCLIJobCloses(t *testing.T) *atomic.Int32 {
	t.Helper()
	original := closeCLIJobHandle
	var calls atomic.Int32
	closeCLIJobHandle = func(handle windows.Handle) error {
		calls.Add(1)
		return original(handle)
	}
	t.Cleanup(func() { closeCLIJobHandle = original })
	return &calls
}

func TestCLIJobClosesHandleWhenProcessStartFails(t *testing.T) {
	closes := recordCLIJobCloses(t)
	missing := filepath.Join(t.TempDir(), "missing-cli.exe")

	_, err := runCLIProcess(t.Context(), exec.Command(missing), nil, nil, slog.New(slog.DiscardHandler))

	if err == nil {
		t.Fatal("runCLIProcess start error = nil; want missing executable error")
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("job handle closes = %d; want 1 after start failure", got)
	}
}

func TestCLIJobAssignmentFailureKillsAndReapsSuspendedProcess(t *testing.T) {
	closes := recordCLIJobCloses(t)
	wantErr := errors.New("assign job test failure")
	originalAssign := assignCLIProcessToJob
	assignCLIProcessToJob = func(windows.Handle, uint32) error { return wantErr }
	t.Cleanup(func() { assignCLIProcessToJob = originalAssign })
	cmd := exec.Command(os.Args[0], "-test.run=^$")

	_, err := runCLIProcess(context.Background(), cmd, nil, nil, slog.New(slog.DiscardHandler))

	if !errors.Is(err, wantErr) {
		t.Fatalf("runCLIProcess assignment error = %v; want %v", err, wantErr)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("assignment failure did not reap child: state=%v", cmd.ProcessState)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("job handle closes = %d; want 1 after assignment failure", got)
	}
}

func TestOpenCodeManagedCommandTreatsPartialStartFailureAsUnproven(t *testing.T) {
	wantErr := errors.New("assign job synthetic test failure")
	originalAssign := assignCLIProcessToJob
	assignCLIProcessToJob = func(windows.Handle, uint32) error { return wantErr }
	t.Cleanup(func() { assignCLIProcessToJob = originalAssign })

	outcome := runOpenCodeManagedCommand(t.Context(), exec.Command(os.Args[0], "-test.run=^$"))

	if !errors.Is(outcome.err, ErrOpenCodeCleanupFailed) || outcome.quiescent {
		t.Fatalf("partial-start outcome = %#v; want unproven cleanup", outcome)
	}
}

func TestCLIJobResumeFailureTerminatesAndReapsAssignedProcess(t *testing.T) {
	closes := recordCLIJobCloses(t)
	wantErr := errors.New("resume thread test failure")
	originalResume := resumeCLIProcess
	resumeCLIProcess = func(uint32) error { return wantErr }
	t.Cleanup(func() { resumeCLIProcess = originalResume })
	cmd := exec.Command(os.Args[0], "-test.run=^$")

	_, err := runCLIProcess(context.Background(), cmd, nil, nil, slog.New(slog.DiscardHandler))

	if !errors.Is(err, wantErr) {
		t.Fatalf("runCLIProcess resume error = %v; want %v", err, wantErr)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("resume failure did not reap child: state=%v", cmd.ProcessState)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("job handle closes = %d; want 1 after resume failure", got)
	}
}

func TestCLIJobClosesHandleAfterNormalCompletion(t *testing.T) {
	closes := recordCLIJobCloses(t)

	_, err := runCLIProcess(context.Background(), exec.Command(os.Args[0], "-test.run=^$"), nil, nil, slog.New(slog.DiscardHandler))

	if err != nil {
		t.Fatalf("runCLIProcess normal completion = %v; want nil", err)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("job handle closes = %d; want 1 after normal completion", got)
	}
}
