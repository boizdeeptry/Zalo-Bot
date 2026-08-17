//go:build !windows

package daemon

import (
	"io"
	"os"
	"sort"
)

type openCodeLockedSmokeRoot struct {
	path string
	file *os.File
}

func openCodePlatformBinaryPathSafe(string) bool {
	return true
}

func lockOpenCodeVerifiedBinary(path string) (io.Closer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, openCodeBinaryRejectedError("lock")
	}
	return file, nil
}

func lockOpenCodeSmokeRoot(path string) (openCodeSmokeRootLock, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		_ = file.Close()
		return nil, ErrOpenCodeCleanupFailed
	}
	return &openCodeLockedSmokeRoot{path: path, file: file}, nil
}

func (locked *openCodeLockedSmokeRoot) remove() error {
	if locked == nil || locked.file == nil {
		return ErrOpenCodeCleanupFailed
	}
	if err := os.Remove(locked.path); err != nil {
		return ErrOpenCodeCleanupFailed
	}
	return nil
}

func (locked *openCodeLockedSmokeRoot) entries() ([]string, error) {
	if locked == nil || locked.file == nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	entries, err := os.ReadDir(locked.path)
	if err != nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Name()
	}
	sort.Strings(result)
	return result, nil
}

func (locked *openCodeLockedSmokeRoot) Close() error {
	if locked == nil || locked.file == nil {
		return nil
	}
	err := locked.file.Close()
	locked.file = nil
	if err != nil {
		return ErrOpenCodeCleanupFailed
	}
	return nil
}

func verifyOpenCodePlatformSignature(string) error {
	return ErrOpenCodeContainmentUnavailable
}

var _ openCodeSmokeRootLock = (*openCodeLockedSmokeRoot)(nil)
