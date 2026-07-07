//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// enumerateMounts lists removable/external mount points the dock watcher should
// see: on macOS the entries under /Volumes; on Linux the auto-mount roots
// (/media, /run/media/<user>, /mnt). Best-effort — capacity via statfs, label
// from the mount's basename. The excluded roots keep the system disk out of the
// "newly-inserted drive" list.
func enumerateMounts() []MountInfo {
	var roots []string
	switch runtime.GOOS {
	case "darwin":
		roots = childDirs("/Volumes")
	default: // linux and other unix
		roots = childDirs("/media")
		for _, u := range childDirs("/run/media") { // /run/media/<user>/<label>
			roots = append(roots, childDirs(u)...)
		}
		roots = append(roots, childDirs("/mnt")...)
	}
	var out []MountInfo
	seen := map[string]bool{}
	for _, r := range roots {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, MountInfo{Path: r, Label: filepath.Base(r), SizeBytes: fsTotalBytes(r)})
	}
	return out
}

// childDirs returns the immediate subdirectories of dir (absolute paths).
func childDirs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// fsTotalBytes returns the total capacity of the filesystem holding path.
func fsTotalBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Blocks) * int64(st.Bsize)
}
