//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package daemon

import (
	"log/slog"
	"os/exec"
)

type directManagedCLIProcess struct {
	pid    int
	logger *slog.Logger
}

// startManagedCLIProcess is the explicit unsupported-platform fallback. It
// starts the root directly and delegates termination to killPidTree, whose
// unsupported-platform implementation returns an error; no containment or kill
// guarantee is made here.
func startManagedCLIProcess(cmd *exec.Cmd, logger *slog.Logger) (managedCLIProcessLifetime, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &directManagedCLIProcess{pid: cmd.Process.Pid, logger: logger}, nil
}

func (p *directManagedCLIProcess) terminate() error {
	return killPidTree(p.pid, "llm-cli", p.logger)
}

func (*directManagedCLIProcess) close() error { return nil }
