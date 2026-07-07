package main

// deviceid.go — resolve the PHYSICAL device identity behind a mounted path, so a
// Volume carries the drive's real serial/model/capacity, not just an operator's
// label. Best-effort and non-fatal everywhere: a masked serial (common behind
// USB-SATA bridges and docks) or a missing tool yields an empty field, never an
// error the caller must handle. No CGO — each platform shells one native tool
// (Get-Disk on Windows via PowerShell/CIM, lsblk on Linux, diskutil on macOS)
// and parses its structured output. The platform-specific resolver lives in
// deviceid_windows.go / deviceid_unix.go behind the shared resolveDeviceIdentity.

import "time"

// DeviceIdentity is what we could learn about the physical device behind a path.
// Any field may be empty when the OS/bridge does not report it.
type DeviceIdentity struct {
	Serial    string `json:"serial,omitempty"`
	Model     string `json:"model,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Bus       string `json:"bus,omitempty"`  // e.g. "USB", "SATA", "NVMe" — informs the caveat note
	Note      string `json:"note,omitempty"` // caveat, e.g. serial may be the bridge's
}

// resolved reports whether we learned anything worth recording.
func (d DeviceIdentity) resolved() bool {
	return d.Serial != "" || d.Model != "" || d.SizeBytes > 0
}

// resolveVolumeIdentity fills a volume's Serial/Model/DeviceSize/DeviceNote from
// the device behind path, best-effort. It never overwrites a good serial with a
// blank one (a later resolve from a dock that masks the serial must not erase a
// real serial captured earlier). Returns the identity it found and whether the
// volume was updated. Caller persists via Store.UpdateVolume.
func (a *App) resolveVolumeIdentity(v *Volume, path string) (DeviceIdentity, bool) {
	id, err := resolveDeviceIdentity(path)
	if err != nil || !id.resolved() {
		return id, false
	}
	changed := false
	if id.Serial != "" && id.Serial != v.Serial {
		v.Serial, changed = id.Serial, true
	}
	if id.Model != "" && id.Model != v.Model {
		v.Model, changed = id.Model, true
	}
	if id.SizeBytes > 0 && id.SizeBytes != v.DeviceSize {
		v.DeviceSize, changed = id.SizeBytes, true
	}
	if id.Note != "" && id.Note != v.DeviceNote {
		v.DeviceNote, changed = id.Note, true
	}
	if changed {
		now := time.Now().UTC()
		v.DeviceAt = &now
	}
	return id, changed
}

// busCaveat returns the storage-industry caveat for a bus type: USB/1394 bridges
// frequently report the bridge chip's serial (or none) rather than the drive's.
func busCaveat(bus string) string {
	switch bus {
	case "USB", "1394", "SD", "MMC":
		return "reported via a " + bus + " bridge — the serial may be the bridge/enclosure's, not the drive's"
	}
	return ""
}
