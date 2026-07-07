package main

// smart.go — drive-mortality signals per volume, via smartmontools (`smartctl`).
//
// This is a COMPLEMENT to hash verification, never a substitute. SMART tells you
// a drive is statistically more likely to DIE soon; it tells you nothing about
// whether the bytes already written are intact — only the custody-chain hashes
// prove that. A "PASSED" drive can still hold a bit-rotted file, and a "FAILING"
// drive's verified copies may still read back perfectly. So health reads only
// ever RAISE an advisory to migrate copies; they never mark data good or bad.
//
// Doctrine: never in the write path. Health is read on the volume view and dock
// ingest only, with a timeout, and any failure is silent-but-logged. When
// smartctl is absent the whole feature hides behind an install hint.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

const smartInstallHint = "smartctl (smartmontools) not found — drive-health signals are hidden. " +
	"Install it: Windows — smartmontools.org installer (or `choco install smartmontools`); " +
	"Linux — `apt install smartmontools`; macOS — `brew install smartmontools`. " +
	"SMART is a mortality hint that COMPLEMENTS hash verification; it never replaces it."

// smartctlBin resolves the smartctl binary (config Tools override, then PATH).
func (a *App) smartctlBin() (string, error) { return a.tool("smartctl") }

// smartAvailable reports whether the drive-health feature can run at all.
func (a *App) smartAvailable() bool { _, err := a.smartctlBin(); return err == nil }

// smartRaw is the slice of `smartctl -j -a` we care about, spanning ATA + NVMe.
type smartRaw struct {
	Device struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int64 `json:"hours"`
	} `json:"power_on_time"`
	AtaAttrs *struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	Nvme *struct {
		CriticalWarning         int   `json:"critical_warning"`
		Temperature             int   `json:"temperature"`
		PowerOnHours            int64 `json:"power_on_hours"`
		MediaErrors             int64 `json:"media_errors"`
		PercentageUsed          int   `json:"percentage_used"`
		AvailableSpare          int   `json:"available_spare"`
		AvailableSpareThreshold int   `json:"available_spare_threshold"`
	} `json:"nvme_smart_health_information_log"`
}

// parseSmart extracts a SmartSnapshot from `smartctl -j -a` output. It returns an
// error only when nothing usable was reported (device open failed, not a JSON
// health report) — a normal non-zero smartctl exit still yields good data.
func parseSmart(b []byte) (SmartSnapshot, error) {
	var raw smartRaw
	if err := json.Unmarshal(b, &raw); err != nil {
		return SmartSnapshot{}, fmt.Errorf("smartctl output was not JSON: %w", err)
	}
	snap := SmartSnapshot{Device: raw.Device.Name, Type: raw.Device.Type}
	if snap.Type == "" {
		snap.Type = raw.Device.Protocol
	}
	usable := false
	if raw.SmartStatus != nil {
		p := raw.SmartStatus.Passed
		snap.Passed = &p
		usable = true
	}
	if raw.Temperature != nil && raw.Temperature.Current > 0 {
		snap.TempC = raw.Temperature.Current
		usable = true
	}
	if raw.PowerOnTime != nil {
		snap.PowerOnHours = raw.PowerOnTime.Hours
	}
	if raw.AtaAttrs != nil {
		for _, at := range raw.AtaAttrs.Table {
			switch at.ID {
			case 5: // Reallocated_Sector_Ct
				snap.Reallocated = at.Raw.Value
			case 197: // Current_Pending_Sector
				snap.Pending = at.Raw.Value
			}
		}
		usable = true
	}
	if raw.Nvme != nil {
		if snap.TempC == 0 {
			snap.TempC = raw.Nvme.Temperature
		}
		if snap.PowerOnHours == 0 {
			snap.PowerOnHours = raw.Nvme.PowerOnHours
		}
		snap.MediaErrors = raw.Nvme.MediaErrors
		snap.PercentUsed = raw.Nvme.PercentageUsed
		snap.SpareLeft = raw.Nvme.AvailableSpare
		snap.SpareThresh = raw.Nvme.AvailableSpareThreshold
		usable = true
	}
	if !usable {
		return SmartSnapshot{}, fmt.Errorf("smartctl reported no usable SMART data (device may need admin/root or a -d type)")
	}
	evaluateAdvisory(&snap, raw)
	return snap, nil
}

// evaluateAdvisory sets the "migrate copies off this volume" flag. It fires on
// the classic imminent-failure signals: reallocated/pending sectors > 0 or SMART
// overall = FAILING (ATA), and the NVMe equivalents (critical warning, media
// errors, spare below threshold, endurance exhausted).
func evaluateAdvisory(snap *SmartSnapshot, raw smartRaw) {
	var why []string
	if snap.Passed != nil && !*snap.Passed {
		why = append(why, "SMART overall self-assessment = FAILING")
	}
	if snap.Reallocated > 0 {
		why = append(why, fmt.Sprintf("%d reallocated sector(s)", snap.Reallocated))
	}
	if snap.Pending > 0 {
		why = append(why, fmt.Sprintf("%d pending sector(s)", snap.Pending))
	}
	if raw.Nvme != nil {
		if raw.Nvme.CriticalWarning != 0 {
			why = append(why, "NVMe critical warning set")
		}
		if snap.MediaErrors > 0 {
			why = append(why, fmt.Sprintf("%d NVMe media error(s)", snap.MediaErrors))
		}
		if snap.SpareThresh > 0 && snap.SpareLeft > 0 && snap.SpareLeft < snap.SpareThresh {
			why = append(why, fmt.Sprintf("available spare %d%% below threshold %d%%", snap.SpareLeft, snap.SpareThresh))
		}
		if snap.PercentUsed >= 100 {
			why = append(why, "rated endurance exhausted (100%+ used)")
		}
	}
	if len(why) > 0 {
		snap.Advisory = true
		snap.AdvisoryWhy = "Migrate copies off this volume — " + joinList(why)
	}
}

// VolumeHealth reads the drive behind path, records a snapshot in the volume's
// history, and returns it. Errors are returned to the caller AND logged; callers
// on non-critical paths (dock ingest) ignore the error. Never called in a write
// path. Read-only toward the device.
func (a *App) VolumeHealth(vol *Volume, path string) (*SmartSnapshot, error) {
	bin, err := a.smartctlBin()
	if err != nil {
		return nil, err // feature hidden — caller surfaces the install hint
	}
	dev, err := smartDeviceNode(path)
	if err != nil {
		a.Store.Log("smart", fmt.Sprintf("%s: cannot map %s to a device: %v", vol.Label, path, err))
		return nil, fmt.Errorf("could not map %s to a physical device: %w", path, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// -a: all SMART data; -j: JSON. A non-zero exit is NORMAL (smartctl encodes
	// disk-health bits in its exit code), so we parse stdout regardless of err.
	out, runErr := exec.CommandContext(ctx, bin, "-j", "-a", dev).Output()
	snap, perr := parseSmart(out)
	if perr != nil {
		a.Store.Log("smart", fmt.Sprintf("%s (%s): %v", vol.Label, dev, perr))
		return nil, perr
	}
	snap.At = time.Now().UTC()
	if snap.Device == "" {
		snap.Device = dev
	}
	if runErr != nil {
		// Keep the exit note for the trail without treating it as a hard failure.
		snap.Note = "smartctl exit: " + runErr.Error()
	}
	a.Store.AppendSmartSnapshot(vol, snap)
	logNote := "OK"
	if snap.Advisory {
		logNote = "ADVISORY: " + snap.AdvisoryWhy
	}
	a.Store.Log("smart", fmt.Sprintf("%s (%s): health read — %s", vol.Label, dev, logNote))
	return &snap, nil
}

func joinList(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "; "
		}
		out += x
	}
	return out
}
