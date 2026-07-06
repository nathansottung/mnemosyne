//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

func diskFree(path string) (int64, error) {
	k := syscall.NewLazyDLL("kernel32.dll")
	proc := k.NewProc("GetDiskFreeSpaceExW")
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	r, _, e := proc.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0, e
	}
	return int64(free), nil
}
