package daemon

import (
	"context"
	"errors"
	"sync"
)

// processGroupManagedCLIProcess owns the Unix process group created by the start
// helper. close and terminate deliberately share one idempotent kill: both paths
// remove descendants that remain in the group, but cannot reach a process that
// deliberately escaped with setsid/setpgid.
type processGroupManagedCLIProcess struct {
	pgid   int
	kill   func(int) error
	active func(int) (bool, error)
	poll   func(context.Context) error
	once   sync.Once
	err    error
}

func (p *processGroupManagedCLIProcess) terminate() error { return p.close() }

func (p *processGroupManagedCLIProcess) close() error {
	p.once.Do(func() { p.err = p.kill(p.pgid) })
	return p.err
}

func (p *processGroupManagedCLIProcess) quiesce(ctx context.Context) error {
	if err := p.close(); err != nil {
		return err
	}
	if p.active == nil || p.poll == nil {
		return errors.New("managed process-group quiescence unavailable")
	}
	for {
		active, err := p.active(p.pgid)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if err := p.poll(ctx); err != nil {
			return err
		}
	}
}
