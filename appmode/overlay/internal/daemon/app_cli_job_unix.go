//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
	"time"
)

// startManagedCLIProcess creates a new process group in the child before exec.
// Ordinary descendants inherit that group and are killed with it; a descendant
// that deliberately calls setsid/setpgid is outside this containment boundary.
func startManagedCLIProcess(cmd *exec.Cmd, _ *slog.Logger) (managedCLIProcessLifetime, error) {
	var attr syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	attr.Setpgid = true
	cmd.SysProcAttr = &attr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processGroupManagedCLIProcess{
		pgid:   cmd.Process.Pid,
		kill:   killCLIProcessGroup,
		active: cliProcessGroupActive,
		poll:   pollCLIProcessGroup,
	}, nil
}

func killCLIProcessGroup(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("kill CLI process group: invalid pgid %d", pgid)
	}
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("kill CLI process group %d: %w", pgid, err)
}

func cliProcessGroupActive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, fmt.Errorf("inspect CLI process group: invalid pgid %d", pgid)
	}
	err := syscall.Kill(-pgid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, fmt.Errorf("inspect CLI process group %d: %w", pgid, err)
}

func pollCLIProcessGroup(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
