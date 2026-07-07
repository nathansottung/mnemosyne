//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// smartDeviceNode maps a mounted path to the smartctl device argument: on Linux
// the parent block device (/dev/sda) found via lsblk; on macOS the whole disk
// (/dev/diskN) via diskutil. Reuses the deviceid_unix.go plumbing. Read-only.
func smartDeviceNode(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if p, e := filepath.EvalSymlinks(abs); e == nil {
		abs = p
	}
	switch runtime.GOOS {
	case "linux":
		return smartNodeLinux(abs)
	case "darwin":
		return smartNodeDarwin(abs)
	}
	return "", fmt.Errorf("device mapping not supported on %s", runtime.GOOS)
}

// smartNodeLinux finds the top-level disk whose subtree owns abs's mountpoint and
// returns /dev/<name> (the whole disk, which is what smartctl wants).
func smartNodeLinux(abs string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-o", "NAME,MOUNTPOINT,TYPE").Output()
	if err != nil {
		return "", fmt.Errorf("lsblk: %v", err)
	}
	var doc struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return "", fmt.Errorf("parsing lsblk JSON: %w", err)
	}
	best := -1
	name := ""
	for _, disk := range doc.BlockDevices {
		if l := deepestMountMatch(disk, abs); l > best {
			best, name = l, disk.Name
		}
	}
	if best < 0 || name == "" {
		return "", fmt.Errorf("no block device mounts %s", abs)
	}
	return "/dev/" + name, nil
}

// smartNodeDarwin follows "Part of Whole" to the physical disk id.
func smartNodeDarwin(abs string) (string, error) {
	info, err := diskutilInfo(abs)
	if err != nil {
		return "", err
	}
	whole := firstNonEmpty(info["Part of Whole"], info["Device Identifier"])
	if whole == "" {
		return "", fmt.Errorf("diskutil gave no whole-disk id for %s", abs)
	}
	return "/dev/" + strings.TrimPrefix(whole, "/dev/"), nil
}
