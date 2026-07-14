package main

// stenc.go — drive-level (hardware) tape AES, handled with appropriate fear.
//
// Some operators enable an LTO drive's built-in AES encryption with `stenc`
// (SCSI SPIN/SPOUT key management). Mnemosyne neither sets nor manages it — but it
// MUST be aware of it, because a drive-encrypted tape is a different, far more
// dangerous animal than a Mnemosyne-encrypted package:
//
//   - Mnemosyne encryption (gpg) is IN the restore story: the ciphertext is a
//     `.tar.gpg` file, and anyone with the passphrase and `gpg` reads it. The QR
//     card, the paper key sheet, and the keystore all carry that passphrase.
//
//   - Drive-level AES is OUTSIDE the restore story entirely. The bytes recorded on
//     the tape are hardware ciphertext. Without the *drive's* key, loaded into a
//     compatible drive, NOTHING reads them — not gpg, not tar, not par2, not
//     another drive of the same model without the key. par2 can't even see the
//     data to repair it. A lost drive key = the tape is scrap.
//
// So the rule is: record it, and shout about it everywhere a human might pick up
// the tape — inventories, the finalize sidecar, and the Recovery Kit — with the
// one instruction that actually matters: preserve the drive key separately, or the
// tape is unrecoverable.
//
// stenc itself is an OPTIONAL, detected tool (Linux only; on Windows/macOS the
// drive key is managed by vendor tools). When present it lets Mnemosyne READ the
// drive's current encryption status (SPIN — read-only) for the Tape Drive panel,
// and — behind an explicit warning — SET/CLEAR the drive key (SPOUT). Neither is
// on the restore path; gpg remains the portable layer, and the drive key is never
// enabled silently.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const driveEncWarning = "DRIVE-LEVEL AES (e.g. stenc/LTO hardware encryption) — this medium's bytes are " +
	"hardware ciphertext that ONLY a compatible drive holding the drive key can read. It is OUTSIDE the " +
	"par2→gpg→tar restore story: gpg cannot help, par2 cannot repair what it cannot read. If the drive key " +
	"is lost, this medium is UNRECOVERABLE by any tool. Preserve the drive key separately from Mnemosyne's keystores."

// driveEncShort is the compact inventory-cell flag.
const driveEncShort = "DRIVE-ENCRYPTED (stenc/LTO hardware; drive key required — outside gpg)"

// anyDriveEncrypted reports whether any volume in the map is drive-encrypted — the
// trigger for the loud Recovery-Kit banner.
func anyDriveEncrypted(volm map[int]*Volume) bool {
	for _, v := range volm {
		if v != nil && v.DriveEncrypted {
			return true
		}
	}
	return false
}

// ---- stenc as an optional, detected tool --------------------------------

// stencInstallHint is the OS-aware "why this is hidden" message.
func stencInstallHint() string {
	switch runtime.GOOS {
	case "windows":
		return "stenc is not available on Windows — drive-level tape encryption is managed via your drive vendor's tools (e.g. IBM/HPE key managers). Mnemosyne's gpg layer is the portable one and remains in force regardless."
	case "linux":
		return "stenc (SCSI tape encryption manager) not found — drive-level AES status is hidden. Install it: `apt install stenc` (Debian/Ubuntu) or build from github.com/scsitape/stenc. It manages the DRIVE key, which is OUTSIDE Mnemosyne's par2→gpg→tar restore story; most users should leave it off."
	default:
		return "stenc (drive-level tape AES) is a Linux tool — on this platform, drive-level encryption is managed via vendor tools. Mnemosyne's gpg layer is the portable one and remains in force regardless."
	}
}

// stencBin resolves the stenc binary. It is Linux-only: on any other OS the drive
// key is a vendor concern, so detection reports "not available" with the hint.
func (a *App) stencBin() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("stenc is not available on %s — drive-level encryption is managed via vendor tools", runtime.GOOS)
	}
	return a.tool("stenc")
}

// stencAvailable reports whether the drive-encryption feature can run at all.
func (a *App) stencAvailable() bool { _, err := a.stencBin(); return err == nil }

// StencStatus is the availability summary for the UI/preflight (never probes the
// drive — that only happens on an explicit "check").
func (a *App) StencStatus() map[string]any {
	bin, err := a.stencBin()
	if err != nil {
		return map[string]any{"available": false, "supported": runtime.GOOS == "linux",
			"os": runtime.GOOS, "hint": stencInstallHint()}
	}
	return map[string]any{"available": true, "supported": true, "os": runtime.GOOS,
		"bin": bin, "device": a.tapeDevice()}
}

// DriveEncStatus is the parsed drive-level encryption state read from stenc.
type DriveEncStatus struct {
	Device     string `json:"device"`
	Encrypting bool   `json:"encrypting"` // drive is writing AES ciphertext (encryption ON)
	Decrypting bool   `json:"decrypting"` // drive will decrypt on read (a key is loaded for reading)
	KeyLoaded  bool   `json:"key_loaded"` // a drive key is currently loaded
	Method     string `json:"method"`     // reported algorithm, e.g. "AES-256-GCM" (blank/none if off)
	Raw        string `json:"raw"`        // trimmed stenc output, for the curious
}

// encValueOn interprets a stenc status value ("on"/"off"/"enabled"/…) as on/off.
func encValueOn(v string) bool {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return false
	case strings.HasPrefix(v, "on"), strings.HasPrefix(v, "enabl"), strings.HasPrefix(v, "encrypt"),
		strings.HasPrefix(v, "mixed"), v == "1", v == "yes", v == "true":
		return true
	}
	return false
}

// firstInt returns the first non-negative integer embedded in s, or -1.
func firstInt(s string) int {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			if n, err := strconv.Atoi(s[start:i]); err == nil {
				return n
			}
			start = -1
		}
	}
	if start >= 0 {
		if n, err := strconv.Atoi(s[start:]); err == nil {
			return n
		}
	}
	return -1
}

// parseStenc reads a stenc status report into a DriveEncStatus. Tolerant across
// stenc versions: it scans for the "Drive Encryption / Decryption Mode / Encryption
// Method / Key Instance Counter" lines by keyword, case-insensitively. Returns an
// error only when the output isn't a recognisable status report at all.
func parseStenc(out []byte) (DriveEncStatus, error) {
	txt := string(out)
	if strings.TrimSpace(txt) == "" {
		return DriveEncStatus{}, fmt.Errorf("stenc produced no output")
	}
	st := DriveEncStatus{Raw: strings.TrimSpace(txt)}
	usable := false
	for _, line := range strings.Split(txt, "\n") {
		raw := strings.TrimSpace(line)
		l := strings.ToLower(raw)
		val := ""
		if i := strings.IndexByte(l, ':'); i >= 0 {
			val = strings.TrimSpace(l[i+1:])
		}
		switch {
		case strings.HasPrefix(l, "drive encryption"), strings.HasPrefix(l, "encryption mode"),
			strings.HasPrefix(l, "encryption status"):
			usable = true
			if encValueOn(val) {
				st.Encrypting = true
			}
		case strings.HasPrefix(l, "decryption mode"), strings.HasPrefix(l, "drive decryption"),
			strings.HasPrefix(l, "volume decryption"):
			usable = true
			if encValueOn(val) {
				st.Decrypting = true
			}
		case strings.HasPrefix(l, "encryption method"), strings.HasPrefix(l, "encryption algorithm"),
			strings.HasPrefix(l, "algorithm"):
			usable = true
			if rawVal := colonValue(raw); rawVal != "" && !strings.EqualFold(rawVal, "none") && !strings.EqualFold(rawVal, "unknown") {
				st.Method = rawVal
			}
		case strings.HasPrefix(l, "key instance counter"), strings.HasPrefix(l, "key instance"):
			usable = true
			if firstInt(val) > 0 {
				st.KeyLoaded = true
			}
		}
		if strings.Contains(l, "key") && (strings.Contains(l, "loaded") || strings.Contains(l, "present")) {
			st.KeyLoaded = true
		}
	}
	if !usable {
		return DriveEncStatus{}, fmt.Errorf("stenc output was not a recognisable encryption-status report")
	}
	// Actively encrypting or decrypting implies a key is loaded in the drive.
	if st.Encrypting || st.Decrypting {
		st.KeyLoaded = true
	}
	return st, nil
}

// colonValue returns the trimmed text after the first ':' in a raw (case-preserved) line.
func colonValue(raw string) string {
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		return strings.TrimSpace(raw[i+1:])
	}
	return ""
}

// DriveEncryptionStatus reads the drive's current encryption state via stenc. This
// is a status QUERY (SPIN) — read-only toward the drive, no tape movement. Returns
// the tool's not-available error when stenc is absent (Linux only).
func (a *App) DriveEncryptionStatus(deviceOverride string) (*DriveEncStatus, error) {
	bin, err := a.stencBin()
	if err != nil {
		return nil, err
	}
	dev := strings.TrimSpace(deviceOverride)
	if dev == "" {
		dev = a.tapeDevice()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// `stenc -f <device>` with no set options prints the current status.
	out, runErr := exec.CommandContext(ctx, bin, "-f", dev).CombinedOutput()
	st, perr := parseStenc(out)
	if perr != nil {
		if runErr != nil {
			a.Store.Log("stenc", fmt.Sprintf("read %s: %v", dev, runErr))
			return nil, fmt.Errorf("stenc could not read %s: %w", dev, runErr)
		}
		return nil, perr
	}
	st.Device = dev
	return &st, nil
}

// SetDriveKey turns the drive's hardware AES ON with the key read from keyFile
// (SPOUT — a control command, never tape movement). This is the explicit, opt-in
// advanced action; callers MUST have shown the operator the warning first. The key
// itself is never logged, and Mnemosyne never stores it — where the key lives is
// the operator's responsibility (recorded as the volume's DriveEncNote).
func (a *App) SetDriveKey(deviceOverride, keyFile string, algorithmIndex int) error {
	bin, err := a.stencBin()
	if err != nil {
		return err
	}
	if strings.TrimSpace(keyFile) == "" {
		return fmt.Errorf("a key file is required to set the drive key")
	}
	if _, err := os.Stat(keyFile); err != nil {
		return fmt.Errorf("key file unreadable: %w", err)
	}
	if algorithmIndex <= 0 {
		algorithmIndex = 1 // stenc's default AES-256-GCM algorithm index
	}
	dev := strings.TrimSpace(deviceOverride)
	if dev == "" {
		dev = a.tapeDevice()
	}
	args := []string{"-f", dev, "-e", "on", "-a", strconv.Itoa(algorithmIndex), "-k", keyFile}
	if err := a.runStenc(bin, args, dev); err != nil {
		return err
	}
	a.Store.Log("stenc", fmt.Sprintf("DRIVE KEY SET on %s — hardware AES is now ON. This is OUTSIDE the gpg restore story; preserve the drive key separately or tapes written now are unrecoverable.", dev))
	return nil
}

// ClearDriveKey turns the drive's hardware AES OFF (SPOUT). Existing tapes written
// while it was on remain drive-encrypted — clearing the key here only stops the
// drive encrypting future writes; it never makes an already-encrypted tape readable.
func (a *App) ClearDriveKey(deviceOverride string) error {
	bin, err := a.stencBin()
	if err != nil {
		return err
	}
	dev := strings.TrimSpace(deviceOverride)
	if dev == "" {
		dev = a.tapeDevice()
	}
	if err := a.runStenc(bin, []string{"-f", dev, "-e", "off"}, dev); err != nil {
		return err
	}
	a.Store.Log("stenc", fmt.Sprintf("drive key cleared on %s — hardware AES is now OFF for future writes (already-encrypted tapes still need their drive key).", dev))
	return nil
}

// runStenc executes a stenc control command under a timeout, folding output into
// the error on failure. The argv is never logged verbatim (it may name a key file).
func (a *App) runStenc(bin string, args []string, dev string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		a.Store.Log("stenc", fmt.Sprintf("control command failed on %s: %v", dev, err))
		return fmt.Errorf("stenc failed on %s: %v: %s", dev, err, tail)
	}
	return nil
}

// noteTapeDriveEncryption is the write-path AWARENESS hook: after a package is
// written to a TAPE volume, if stenc reports the drive actively encrypting, record
// drive_encryption on the volume so every inventory and the Recovery Kit shout
// about the missing-drive-key risk. Best-effort and silent-but-logged — the status
// query is read-only, and a failure here never affects the write. Never enables
// anything; it only records what the drive is already doing.
func (a *App) noteTapeDriveEncryption(volumeID int) {
	if volumeID <= 0 || !a.stencAvailable() {
		return
	}
	v := a.Store.Volume(volumeID)
	if v == nil || !strings.EqualFold(v.Kind, "TAPE") || v.DriveEncrypted {
		return
	}
	st, err := a.DriveEncryptionStatus("")
	if err != nil || st == nil || !st.Encrypting {
		return
	}
	v.DriveEncrypted = true
	if strings.TrimSpace(v.DriveEncNote) == "" {
		v.DriveEncNote = "detected active at write time via stenc on " + st.Device + " — RECORD where the drive key lives (it is outside Mnemosyne)"
	}
	a.Store.UpdateVolume(v)
	a.Store.Log("volume", v.Label+": auto-flagged DRIVE-ENCRYPTED — stenc reported the drive encrypting during a write. The drive key is OUTSIDE the gpg restore story; preserve it separately or this tape is unrecoverable.")
}
