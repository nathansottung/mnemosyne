//go:build !windows && !linux && !darwin

package main

// systemMemory is the fallback for platforms without a CGO-free probe here — it
// reports "unknown" (zeros), and the UI hides the live memory line accordingly.
func systemMemory() MemInfo { return MemInfo{} }
