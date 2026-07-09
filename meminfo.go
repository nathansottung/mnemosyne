package main

// meminfo.go — CGO-free physical-memory detection, so the Performance settings can
// tell the user "your machine: 128 GB total, 96 GB free right now" and cap the RAM
// buffer at a sane fraction of what actually exists. Each OS reads it its own way
// (Windows GlobalMemoryStatusEx, Linux /proc/meminfo, macOS sysctl) — see the
// build-tagged files. All pure syscalls / file reads; no cgo, keeping the
// one-static-binary bargain.

// MemInfo is total and currently-available physical RAM in bytes. Zero fields mean
// "could not determine" (the UI then hides the live numbers rather than lying).
type MemInfo struct {
	TotalBytes     int64 `json:"total_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// SystemMemory returns the machine's physical memory, best-effort. Never panics;
// an undetectable platform yields a zero MemInfo.
func SystemMemory() MemInfo { return systemMemory() }
