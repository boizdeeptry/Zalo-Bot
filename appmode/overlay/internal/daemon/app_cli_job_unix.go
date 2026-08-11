//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
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
		pgid: cmd.Process.Pid,
		kill: killCLIProcessGroup,
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
