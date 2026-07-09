//go:build darwin

package main

import "syscall"

// systemMemory reads physical RAM via sysctl (no cgo). Total is hw.memsize (a raw
// 8-byte little-endian value). "Available right now" is approximated from the VM
// page counters — free + inactive (reclaimable) pages × page size — which is the
// closest CGO-free analogue to what Activity Monitor calls available memory.
func systemMemory() MemInfo {
	var mi MemInfo
	if raw, err := syscall.Sysctl("hw.memsize"); err == nil {
		// Sysctl returns the value as a string of raw bytes; decode little-endian.
		var v uint64
		for i := 0; i < len(raw) && i < 8; i++ {
			v |= uint64(raw[i]) << (8 * uint(i))
		}
		mi.TotalBytes = int64(v)
	}
	pageSize, err1 := syscall.SysctlUint32("hw.pagesize")
	free, err2 := syscall.SysctlUint32("vm.page_free_count")
	inactive, err3 := syscall.SysctlUint32("vm.page_inactive_count")
	if err1 == nil && err2 == nil && err3 == nil {
		mi.AvailableBytes = (int64(free) + int64(inactive)) * int64(pageSize)
	}
	return mi
}
