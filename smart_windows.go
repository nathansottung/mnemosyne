//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// smartDeviceNode maps a mounted path to the smartctl device argument on
// Windows: drive letter → partition → disk Number → /dev/pd<N> (smartmontools'
// physical-drive syntax). Reuses driveLetter from deviceid_windows.go — the same
// identity plumbing that resolves serial/model. Read-only; PowerShell one-shot.
func smartDeviceNode(path string) (string, error) {
	letter, err := driveLetter(path)
	if err != nil {
		return "", err
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop';`+
		`(@(Get-Partition -DriveLetter %s | Get-Disk)[0]).Number`, letter)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", fmt.Errorf("resolving disk number for %s: %v", letter, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("unexpected disk number %q for %s", strings.TrimSpace(string(out)), letter)
	}
	return fmt.Sprintf("/dev/pd%d", n), nil
}
