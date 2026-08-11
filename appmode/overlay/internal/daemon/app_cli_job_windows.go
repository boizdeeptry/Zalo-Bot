//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	assignCLIProcessToJob = assignProcessToCLIJob
	resumeCLIProcess      = resumeSuspendedCLIProcess
	terminateCLIJob       = windows.TerminateJobObject
	closeCLIJobHandle     = windows.CloseHandle
)

type windowsManagedCLIProcess struct {
	job      windows.Handle
	closeOne sync.Once
	closeErr error
}

func (p *windowsManagedCLIProcess) terminate() error {
	terminateErr := terminateCLIJob(p.job, 1)
	return errors.Join(terminateErr, p.close())
}

func (p *windowsManagedCLIProcess) close() error {
	p.closeOne.Do(func() { p.closeErr = closeCLIJobHandle(p.job) })
	return p.closeErr
}

// startManagedCLIProcess prevents the process from executing before it belongs
// to the kill-on-close Job Object. CREATE_SUSPENDED closes the race where a root
// process could spawn a detached child between cmd.Start and job assignment.
func startManagedCLIProcess(cmd *exec.Cmd, _ *slog.Logger) (managedCLIProcessLifetime, error) {
	job, err := newKillOnCloseCLIJob()
	if err != nil {
		return nil, fmt.Errorf("create CLI job: %w", err)
	}

	var attr syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	attr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.SysProcAttr = &attr

	if err := cmd.Start(); err != nil {
		closeErr := closeCLIJobHandle(job)
		return nil, errors.Join(err, wrapCLIJobCloseError(closeErr))
	}
	pid := uint32(cmd.Process.Pid)
	if err := assignCLIProcessToJob(job, pid); err != nil {
		return nil, abortUnassignedCLIProcess(cmd, job, fmt.Errorf("assign CLI process to job: %w", err))
	}
	if err := resumeCLIProcess(pid); err != nil {
		return nil, abortAssignedCLIProcess(cmd, job, fmt.Errorf("resume CLI process: %w", err))
	}
	return &windowsManagedCLIProcess{job: job}, nil
}

func newKillOnCloseCLIJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		closeErr := closeCLIJobHandle(job)
		return 0, errors.Join(err, wrapCLIJobCloseError(closeErr))
	}
	return job, nil
}

func assignProcessToCLIJob(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		pid,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(job, process)
}

func resumeSuspendedCLIProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return err
			}
			defer windows.CloseHandle(thread)
			for {
				previous, err := windows.ResumeThread(thread)
				if err != nil {
					return err
				}
				if previous <= 1 {
					return nil
				}
			}
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return fmt.Errorf("primary thread for pid %d not found", pid)
			}
			return err
		}
	}
}

func abortUnassignedCLIProcess(cmd *exec.Cmd, job windows.Handle, cause error) error {
	closeErr := closeCLIJobHandle(job)
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	return errors.Join(
		cause,
		wrapCLIJobCloseError(closeErr),
		wrapCLIKillError(killErr),
		wrapCLIWaitError(waitErr),
	)
}

func abortAssignedCLIProcess(cmd *exec.Cmd, job windows.Handle, cause error) error {
	terminateErr := terminateCLIJob(job, 1)
	closeErr := closeCLIJobHandle(job)
	waitErr := cmd.Wait()
	return errors.Join(
		cause,
		wrapCLITerminateJobError(terminateErr),
		wrapCLIJobCloseError(closeErr),
		wrapCLIWaitError(waitErr),
	)
}

func wrapCLIJobCloseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close CLI job: %w", err)
}

func wrapCLIKillError(err error) error {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return fmt.Errorf("kill suspended CLI process: %w", err)
}

func wrapCLITerminateJobError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("terminate CLI job: %w", err)
}

func wrapCLIWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil // termination is the expected exit path during fail-closed cleanup
	}
	return fmt.Errorf("reap CLI process: %w", err)
}
