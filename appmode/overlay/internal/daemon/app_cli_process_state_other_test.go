//go:build !windows

package daemon

func cliTestProcessRunning(pid int) bool {
	_, ok := processStart(pid)
	return ok
}
