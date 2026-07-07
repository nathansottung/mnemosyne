//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// detectLTFSMounts returns drive roots (e.g. "T:\\") whose filesystem name is
// "LTFS" — an LTO tape mounted through an LTFS driver. Best-effort and purely
// informational: any failure yields no mounts and never surfaces an error.
// Same no-CGO, NewLazyDLL style as diskfree_windows.go.
func detectLTFSMounts() []string {
	k := syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives := k.NewProc("GetLogicalDrives")
	getVolumeInformation := k.NewProc("GetVolumeInformationW")
	setErrorMode := k.NewProc("SetErrorMode")

	// Suppress the "no disk in drive" popup for not-ready removable drives.
	const semFailCriticalErrors = 0x0001
	prev, _, _ := setErrorMode.Call(uintptr(semFailCriticalErrors))
	defer setErrorMode.Call(prev)

	mask, _, _ := getLogicalDrives.Call()
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + ":\\"
		rp, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		fsName := make([]uint16, 261) // MAX_PATH + 1
		r, _, _ := getVolumeInformation.Call(
			uintptr(unsafe.Pointer(rp)),
			0, 0, // lpVolumeNameBuffer, nVolumeNameSize
			0, 0, 0, // serial, maxComponentLength, fileSystemFlags
			uintptr(unsafe.Pointer(&fsName[0])),
			uintptr(len(fsName)),
		)
		if r == 0 {
			continue // drive not ready / not readable — skip silently
		}
		if syscall.UTF16ToString(fsName) == "LTFS" {
			out = append(out, root)
		}
	}
	return out
}
