package daemon

import "sync"

// processGroupManagedCLIProcess owns the Unix process group created by the start
// helper. close and terminate deliberately share one idempotent kill: both paths
// remove descendants that remain in the group, but cannot reach a process that
// deliberately escaped with setsid/setpgid.
type processGroupManagedCLIProcess struct {
	pgid int
	kill func(int) error
	once sync.Once
	err  error
}

func (p *processGroupManagedCLIProcess) terminate() error { return p.close() }

func (p *processGroupManagedCLIProcess) close() error {
	p.once.Do(func() { p.err = p.kill(p.pgid) })
	return p.err
}
