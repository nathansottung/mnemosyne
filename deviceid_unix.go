//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// resolveDeviceIdentity maps a mounted path to its physical device: Linux shells
// `lsblk -J` (structured JSON) and finds the disk whose subtree owns the path's
// mountpoint; macOS shells `diskutil info`. No CGO, read-only, non-fatal on any
// failure (tool absent, path not on a block device, network mount).
func resolveDeviceIdentity(path string) (DeviceIdentity, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if p, e := filepath.EvalSymlinks(abs); e == nil {
		abs = p
	}
	switch runtime.GOOS {
	case "linux":
		return resolveLinux(abs)
	case "darwin":
		return resolveDarwin(abs)
	}
	return DeviceIdentity{}, fmt.Errorf("device identity not supported on %s", runtime.GOOS)
}

// ---- Linux: lsblk -------------------------------------------------------

// flexInt unmarshals a JSON number OR a quoted number (lsblk -b emits either
// depending on version) into an int64.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil // unparseable size is non-fatal; leave zero
	}
	*n = flexInt(v)
	return nil
}

type lsblkNode struct {
	Name       string      `json:"name"`
	Serial     string      `json:"serial"`
	Model      string      `json:"model"`
	Size       flexInt     `json:"size"`
	Mountpoint string      `json:"mountpoint"`
	Type       string      `json:"type"`
	Tran       string      `json:"tran"` // transport: usb, sata, nvme, …
	Children   []lsblkNode `json:"children"`
}

func resolveLinux(abs string) (DeviceIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-b",
		"-o", "NAME,SERIAL,MODEL,SIZE,MOUNTPOINT,TYPE,TRAN").Output()
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("lsblk: %v", err)
	}
	var doc struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return DeviceIdentity{}, fmt.Errorf("parsing lsblk JSON: %w", err)
	}
	best := -1
	var pick lsblkNode
	// Each top-level node is a physical disk; SERIAL/MODEL/TRAN live there. Find
	// the disk whose subtree holds the longest mountpoint matching abs.
	for _, disk := range doc.BlockDevices {
		if l := deepestMountMatch(disk, abs); l > best {
			best, pick = l, disk
		}
	}
	if best < 0 {
		return DeviceIdentity{}, fmt.Errorf("no block device mounts %s", abs)
	}
	id := DeviceIdentity{
		Serial:    strings.TrimSpace(pick.Serial),
		Model:     strings.TrimSpace(pick.Model),
		SizeBytes: int64(pick.Size),
		Bus:       strings.ToUpper(strings.TrimSpace(pick.Tran)),
	}
	id.Note = busCaveat(id.Bus)
	return id, nil
}

// deepestMountMatch returns the length of the longest mountpoint at or under
// node that is a path-prefix of abs, or -1 if none matches.
func deepestMountMatch(node lsblkNode, abs string) int {
	best := -1
	if mountMatches(node.Mountpoint, abs) && len(node.Mountpoint) > best {
		best = len(node.Mountpoint)
	}
	for _, ch := range node.Children {
		if l := deepestMountMatch(ch, abs); l > best {
			best = l
		}
	}
	return best
}

func mountMatches(mp, abs string) bool {
	if mp == "" {
		return false
	}
	if abs == mp {
		return true
	}
	return strings.HasPrefix(abs, strings.TrimRight(mp, "/")+"/")
}

// ---- macOS: diskutil ----------------------------------------------------

var diskutilBytes = regexp.MustCompile(`\((\d+) Bytes\)`)

func resolveDarwin(abs string) (DeviceIdentity, error) {
	info, err := diskutilInfo(abs)
	if err != nil {
		return DeviceIdentity{}, err
	}
	// "Part of Whole: disk2" points at the physical disk, which carries the
	// media name (model) and full capacity; the volume node often does not.
	if whole := info["Part of Whole"]; whole != "" {
		if wi, e := diskutilInfo(whole); e == nil {
			info = wi
		}
	}
	id := DeviceIdentity{}
	if m := firstNonEmpty(info["Device / Media Name"], info["Media Name"]); m != "" {
		id.Model = m
	}
	for _, k := range []string{"Disk Size", "Total Size", "Volume Used Space"} {
		if v := info[k]; v != "" {
			if b := diskutilBytes.FindStringSubmatch(v); b != nil {
				id.SizeBytes, _ = strconv.ParseInt(b[1], 10, 64)
				break
			}
		}
	}
	// diskutil rarely exposes a hardware serial for the volume; capture it when
	// present under any of the keys different macOS versions have used.
	id.Serial = firstNonEmpty(info["Disk / Partition UUID"], info["Volume UUID"])
	if strings.Contains(strings.ToLower(info["Protocol"]), "usb") {
		id.Bus = "USB"
		id.Note = busCaveat("USB")
	}
	if !id.resolved() {
		return id, fmt.Errorf("diskutil reported no identity for %s", abs)
	}
	return id, nil
}

func diskutilInfo(target string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "diskutil", "info", target).Output()
	if err != nil {
		return nil, fmt.Errorf("diskutil info %s: %v", target, err)
	}
	m := map[string]string{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		parts := strings.SplitN(string(line), ":", 2)
		if len(parts) != 2 {
			continue
		}
		m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return m, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
