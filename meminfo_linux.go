//go:build linux

package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// systemMemory reads /proc/meminfo. MemTotal is physical RAM; MemAvailable is the
// kernel's own estimate of what a new allocation could use without swapping (the
// right "free right now" figure — better than MemFree, which ignores reclaimable
// cache). Both are reported in kB.
func systemMemory() MemInfo {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemInfo{}
	}
	defer f.Close()
	var mi MemInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			mi.TotalBytes = kb * 1024
		case "MemAvailable:":
			mi.AvailableBytes = kb * 1024
		}
	}
	return mi
}
