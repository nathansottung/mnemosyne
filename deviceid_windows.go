//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// resolveDeviceIdentity maps a mounted path to its physical disk via a single
// PowerShell/CIM one-shot: DriveLetter -> Get-Partition -> Get-Disk, emitted as
// JSON (ConvertTo-Json). No CGO, no WMI COM plumbing — just shell one command
// and parse it. Purely read-only. Any failure (optical/tape drives have no
// partition, removable not ready, PowerShell absent) returns an error the
// caller treats as non-fatal.
func resolveDeviceIdentity(path string) (DeviceIdentity, error) {
	letter, err := driveLetter(path)
	if err != nil {
		return DeviceIdentity{}, err
	}
	// One statement, structured output. @(...)[0] guards against an array; casts
	// force the BusType enum and (possibly padded) serial to plain strings so the
	// JSON is stable across PowerShell versions.
	script := fmt.Sprintf(`$ErrorActionPreference='Stop';`+
		`$d=@(Get-Partition -DriveLetter %s | Get-Disk)[0];`+
		`[pscustomobject]@{Serial=[string]$d.SerialNumber;Model=[string]$d.Model;`+
		`Friendly=[string]$d.FriendlyName;Size=[int64]$d.Size;Bus=[string]$d.BusType}|ConvertTo-Json -Compress`, letter)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("Get-Disk for %s: failed to resolve device (%v)", letter, err)
	}
	var raw struct {
		Serial, Model, Friendly, Bus string
		Size                         int64
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &raw); err != nil {
		return DeviceIdentity{}, fmt.Errorf("parsing Get-Disk output: %w", err)
	}
	model := strings.TrimSpace(raw.Model)
	if model == "" {
		model = strings.TrimSpace(raw.Friendly)
	}
	id := DeviceIdentity{
		Serial:    strings.TrimSpace(raw.Serial),
		Model:     model,
		SizeBytes: raw.Size,
		Bus:       strings.TrimSpace(raw.Bus),
	}
	id.Note = busCaveat(id.Bus)
	return id, nil
}

// driveLetter extracts a single A–Z drive letter from a path ("E:\\photos" -> "E").
func driveLetter(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	vol := filepath.VolumeName(abs) // "E:" for a lettered drive, UNC root otherwise
	if len(vol) == 2 && vol[1] == ':' {
		c := vol[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return strings.ToUpper(string(c)), nil
		}
	}
	return "", fmt.Errorf("no drive letter in %q (device identity needs a lettered mount, not a UNC/network path)", path)
}
