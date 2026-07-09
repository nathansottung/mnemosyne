//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX struct filled by
// GlobalMemoryStatusEx. dwLength must be set to the struct size before the call.
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// systemMemory reads physical RAM via kernel32!GlobalMemoryStatusEx (no cgo).
func systemMemory() MemInfo {
	var m memoryStatusEx
	m.dwLength = uint32(unsafe.Sizeof(m))
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return MemInfo{}
	}
	return MemInfo{TotalBytes: int64(m.ullTotalPhys), AvailableBytes: int64(m.ullAvailPhys)}
}
