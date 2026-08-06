//go:build !windows

package daemon

import "os"

func replaceAppFile(source, destination string) error {
	return os.Rename(source, destination)
}
