//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	unixCLIHelperModeEnv    = "AGENTDC_TEST_UNIX_CLI_HELPER_MODE"
	unixCLIHelperPIDFileEnv = "AGENTDC_TEST_UNIX_CLI_PID_FILE"
	unixCLIHelperReleaseEnv = "AGENTDC_TEST_UNIX_CLI_RELEASE_FILE"
	unixCLIHelperChildEnv   = "AGENTDC_TEST_UNIX_CLI_CHILD"
)

func TestUnixManagedCLIProcessCreatesGroupBeforeExec(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	process, err := startManagedCLIProcess(cmd, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v; want Setpgid before exec", cmd.SysProcAttr)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process wait = %v; want nil", err)
	}
	if err := process.close(); err != nil {
		t.Fatalf("close completed process group = %v; want nil/ESRCH", err)
	}
}

func TestUnixCLIRunStopsSameProcessGroupDescendants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   string
		cancel bool
	}{
		{name: "context canceled", mode: "wait", cancel: true},
		{name: "root completes", mode: "exit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "child.pid")
			releaseFile := filepath.Join(dir, "release")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			t.Cleanup(func() { _ = writer.Close() })

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestUnixCLIProcessGroupHelper$")
			cmd.Env = append(os.Environ(),
				unixCLIHelperModeEnv+"="+tc.mode,
				unixCLIHelperPIDFileEnv+"="+pidFile,
				unixCLIHelperReleaseEnv+"="+releaseFile,
			)
			cmd.ExtraFiles = []*os.File{writer}
			rootPID := make(chan int, 1)
			result := make(chan error, 1)
			go func() {
				_, runErr := runCLIProcess(ctx, cmd, nil, rootPID, slog.New(slog.DiscardHandler))
				result <- runErr
			}()

			var root int
			select {
			case root = <-rootPID:
			case runErr := <-result:
				t.Fatalf("managed CLI failed before start notification: %v", runErr)
			case <-time.After(3 * time.Second):
				t.Fatal("managed CLI did not report its root PID")
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			child := readGrandchildPID(t, pidFile)
			t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

			rootGroup, err := unix.Getpgid(root)
			if err != nil {
				t.Fatalf("root process group: %v", err)
			}
			childGroup, err := unix.Getpgid(child)
			if err != nil {
				t.Fatalf("descendant process group: %v", err)
			}
			if childGroup != rootGroup {
				t.Fatalf("descendant PGID = %d; want root PGID %d", childGroup, rootGroup)
			}

			if err := reader.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			ready := []byte{0}
			if _, err := io.ReadFull(reader, ready); err != nil || ready[0] != 'R' {
				t.Fatalf("descendant readiness = %q, %v; want R", ready, err)
			}
			if err := os.WriteFile(releaseFile, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.cancel {
				cancel()
			}

			select {
			case runErr := <-result:
				if tc.cancel && !errors.Is(runErr, context.Canceled) {
					t.Fatalf("run error = %v; want context.Canceled", runErr)
				}
				if !tc.cancel && runErr != nil {
					t.Fatalf("run error = %v; want nil", runErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("managed CLI run did not finish")
			}

			if err := reader.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("descendant lifetime pipe read = %d, %v; want EOF", n, err)
			}
		})
	}
}

func TestUnixCLIProcessGroupHelper(t *testing.T) {
	if os.Getenv(unixCLIHelperChildEnv) == "1" {
		writer := os.NewFile(3, "unix-cli-lifetime")
		if writer == nil {
			os.Exit(2)
		}
		if _, err := writer.Write([]byte{'R'}); err != nil {
			os.Exit(2)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	mode := os.Getenv(unixCLIHelperModeEnv)
	if mode == "" {
		return
	}
	if mode != "wait" && mode != "exit" {
		unixCLIHelperFail("invalid helper mode %q", mode)
	}
	writer := os.NewFile(3, "unix-cli-lifetime")
	if writer == nil {
		unixCLIHelperFail("missing lifetime descriptor")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestUnixCLIProcessGroupHelper$")
	child.Env = append(os.Environ(), unixCLIHelperChildEnv+"=1")
	child.ExtraFiles = []*os.File{writer}
	if err := child.Start(); err != nil {
		unixCLIHelperFail("start descendant: %v", err)
	}
	if err := writer.Close(); err != nil {
		_ = child.Process.Kill()
		unixCLIHelperFail("close root lifetime descriptor: %v", err)
	}
	pid := child.Process.Pid
	if err := child.Process.Release(); err != nil {
		_ = child.Process.Kill()
		unixCLIHelperFail("release descendant: %v", err)
	}
	if err := os.WriteFile(os.Getenv(unixCLIHelperPIDFileEnv), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		unixCLIHelperFail("write descendant pid: %v", err)
	}
	releaseFile := os.Getenv(unixCLIHelperReleaseEnv)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(releaseFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			unixCLIHelperFail("release file did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mode == "wait" {
		for {
			time.Sleep(time.Hour)
		}
	}
}

func unixCLIHelperFail(format string, args ...any) {
	_, _ = fmt.Fprintln(os.Stderr, "unix CLI helper:", strings.TrimSpace(fmt.Sprintf(format, args...)))
	os.Exit(2)
}
