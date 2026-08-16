//go:build !windows

package daemon

func appPersonaPlatformNFC(string) bool { return false }

func appPersonaPlatformPathSafe(string, bool) bool { return false }

func appPersonaPlatformOrdinalIgnoreCaseEqual(string, string) bool { return false }
