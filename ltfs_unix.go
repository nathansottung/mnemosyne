//go:build !windows

package main

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// detectLTFSMounts returns mount points whose filesystem type is "ltfs".
// Best-effort and purely informational: any failure yields no mounts.
// Prefers /proc/mounts (Linux); falls back to parsing mount(8) (macOS/BSD).
func detectLTFSMounts() []string {
	if list, ok := ltfsFromProcMounts(); ok {
		return list
	}
	return ltfsFromMountCmd()
}

// ltfsFromProcMounts parses /proc/mounts: "dev mountpoint fstype opts ...".
// The bool reports whether /proc/mounts was readable (authoritative source).
func ltfsFromProcMounts() ([]string, bool) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 3 && fields[2] == "ltfs" {
			out = append(out, unescapeMount(fields[1]))
		}
	}
	return out, true
}

// ltfsFromMountCmd parses mount(8) output, e.g. macOS:
// "devname on /Volumes/tape (ltfs, local, ...)" or Linux:
// "devname on /mnt/tape type ltfs (rw,...)".
func ltfsFromMountCmd() []string {
	raw, err := exec.Command("mount").CombinedOutput()
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		low := strings.ToLower(line)
		if !strings.Contains(low, " type ltfs") && !strings.Contains(low, "(ltfs,") && !strings.Contains(low, "(ltfs)") {
			continue
		}
		idx := strings.Index(line, " on ")
		if idx < 0 {
			continue
		}
		rest := line[idx+4:]
		mp := rest
		if j := strings.Index(rest, " ("); j >= 0 {
			mp = rest[:j]
		} else if j := strings.Index(rest, " type "); j >= 0 {
			mp = rest[:j]
		}
		if mp = strings.TrimSpace(mp); mp != "" {
			out = append(out, mp)
		}
	}
	return out
}

// unescapeMount decodes the octal escapes /proc/mounts uses for spaces etc.
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
