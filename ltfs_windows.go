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
	getDriveType := k.NewProc("GetDriveTypeW")
	setErrorMode := k.NewProc("SetErrorMode")

	// Suppress the "no disk in drive" popup for not-ready removable drives.
	const semFailCriticalErrors = 0x0001
	prev, _, _ := setErrorMode.Call(uintptr(semFailCriticalErrors))
	defer setErrorMode.Call(prev)

	// Drive-type constants (winbase.h). We only probe local media: LTFS is never a
	// NETWORK drive, and calling GetVolumeInformationW on a disconnected mapped
	// network drive can BLOCK for ~20s on the SMB timeout — which used to hang
	// Preflight (and the Settings page). So skip anything not fixed/removable.
	const driveRemovable, driveFixed = 2, 3

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
		if dt, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(rp))); dt != driveRemovable && dt != driveFixed {
			continue // network / optical / unknown — never LTFS, and possibly slow to probe
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
