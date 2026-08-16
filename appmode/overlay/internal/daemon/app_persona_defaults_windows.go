//go:build windows

package daemon

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var appPersonaIsNormalizedString = windows.NewLazySystemDLL("normaliz.dll").NewProc("IsNormalizedString")
var appPersonaCompareStringOrdinal = windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareStringOrdinal")

func appPersonaPlatformNFC(value string) bool {
	encoded, err := windows.UTF16FromString(value)
	if err != nil || len(encoded) == 0 {
		return false
	}
	result, _, _ := appPersonaIsNormalizedString.Call(
		uintptr(1),
		uintptr(unsafe.Pointer(&encoded[0])),
		uintptr(len(encoded)-1),
	)
	return result != 0
}

func appPersonaPlatformOrdinalIgnoreCaseEqual(left, right string) bool {
	leftUTF16, leftErr := windows.UTF16FromString(left)
	rightUTF16, rightErr := windows.UTF16FromString(right)
	if leftErr != nil || rightErr != nil || len(leftUTF16) == 0 || len(rightUTF16) == 0 {
		return false
	}
	result, _, _ := appPersonaCompareStringOrdinal.Call(
		uintptr(unsafe.Pointer(&leftUTF16[0])), uintptr(len(leftUTF16)-1),
		uintptr(unsafe.Pointer(&rightUTF16[0])), uintptr(len(rightUTF16)-1),
		uintptr(1),
	)
	const cstrEqual = 2
	return result == cstrEqual
}

func appPersonaPlatformPathSafe(path string, wantDirectory bool) bool {
	volume := filepath.VolumeName(path)
	if strings.Contains(strings.TrimPrefix(path, volume), ":") {
		return false
	}
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := windows.GetFileAttributes(encoded)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		attributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		return false
	}
	isDirectory := attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	return isDirectory == wantDirectory
}
