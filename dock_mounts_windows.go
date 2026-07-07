//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// enumerateMounts lists ready fixed + removable drive letters with their volume
// label and total capacity — the dock watcher's view of "what is mounted now".
// Same no-CGO, NewLazyDLL style as diskfree_windows.go / ltfs_windows.go.
func enumerateMounts() []MountInfo {
	k := syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives := k.NewProc("GetLogicalDrives")
	getVolumeInformation := k.NewProc("GetVolumeInformationW")
	getDriveType := k.NewProc("GetDriveTypeW")
	getDiskFreeSpaceEx := k.NewProc("GetDiskFreeSpaceExW")
	setErrorMode := k.NewProc("SetErrorMode")

	const semFailCriticalErrors = 0x0001
	prev, _, _ := setErrorMode.Call(uintptr(semFailCriticalErrors))
	defer setErrorMode.Call(prev)

	const driveRemovable, driveFixed = 2, 3

	mask, _, _ := getLogicalDrives.Call()
	var out []MountInfo
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + ":\\"
		rp, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		dt, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(rp)))
		if dt != driveRemovable && dt != driveFixed {
			continue // skip optical/network/RAM — dock ingest targets disks
		}
		volName := make([]uint16, 261) // MAX_PATH + 1
		r, _, _ := getVolumeInformation.Call(
			uintptr(unsafe.Pointer(rp)),
			uintptr(unsafe.Pointer(&volName[0])), uintptr(len(volName)),
			0, 0, 0, 0, 0)
		if r == 0 {
			continue // not ready (e.g. empty card reader) — skip silently
		}
		var freeAvail, total, totalFree uint64
		getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(rp)),
			uintptr(unsafe.Pointer(&freeAvail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
		out = append(out, MountInfo{Path: root, Label: syscall.UTF16ToString(volName), SizeBytes: int64(total)})
	}
	return out
}
