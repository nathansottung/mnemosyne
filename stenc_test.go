package main

// stenc_test.go — drive-level (hardware) tape AES awareness. The parser is the
// tested unit (deterministic across a couple of stenc output shapes); the live
// SPIN/SPOUT chain needs the physical drive and is never exercised here. The
// awareness contract — optional, OS-aware, non-fatal, never silent — is asserted
// against the "tool absent" path (which is what runs on any CI box without stenc).

import (
	"runtime"
	"strings"
	"testing"
)

func TestParseStenc_On(t *testing.T) {
	out := []byte(`Status for /dev/nst0
--------------------------------------------------
Drive Encryption:      on
Decryption Mode:       on
Encryption Method:     AES-256-GCM
Key Instance Counter:  3
`)
	st, err := parseStenc(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !st.Encrypting || !st.Decrypting || !st.KeyLoaded {
		t.Errorf("expected encrypting+decrypting+key: got %+v", st)
	}
	if st.Method != "AES-256-GCM" {
		t.Errorf("method = %q, want AES-256-GCM", st.Method)
	}
}

func TestParseStenc_Off(t *testing.T) {
	out := []byte(`Status for /dev/nst0
--------------------------------------------------
Drive Encryption:      off
Decryption Mode:       off
Encryption Method:     none
Key Instance Counter:  0
`)
	st, err := parseStenc(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Encrypting || st.Decrypting || st.KeyLoaded {
		t.Errorf("expected all-off: got %+v", st)
	}
	if st.Method != "" {
		t.Errorf("method should be empty when none, got %q", st.Method)
	}
}

func TestParseStenc_KeyLoadedNotEncrypting(t *testing.T) {
	// A drive can hold a key for reading (decryption) while not encrypting writes.
	out := []byte(`Drive Encryption:      off
Decryption Mode:       on
Key Instance Counter:  2
`)
	st, err := parseStenc(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Encrypting {
		t.Error("must not report encrypting when Drive Encryption is off")
	}
	if !st.Decrypting || !st.KeyLoaded {
		t.Errorf("expected decrypting + key loaded: got %+v", st)
	}
}

func TestParseStenc_Unrecognised(t *testing.T) {
	if _, err := parseStenc([]byte("bash: stenc: command not found\n")); err == nil {
		t.Error("non-status output must be an error, not a silent empty status")
	}
	if _, err := parseStenc([]byte("   ")); err == nil {
		t.Error("empty output must be an error")
	}
}

func TestEncValueOnAndFirstInt(t *testing.T) {
	on := []string{"on", "on (aes-256-gcm)", "enabled", "encrypting", "mixed", "1", "yes"}
	for _, v := range on {
		if !encValueOn(v) {
			t.Errorf("encValueOn(%q) should be true", v)
		}
	}
	off := []string{"", "off", "off, raw when reading", "disabled", "none", "raw"}
	for _, v := range off {
		if encValueOn(v) {
			t.Errorf("encValueOn(%q) should be false", v)
		}
	}
	if firstInt("Key Instance Counter:  3") != 3 {
		t.Error("firstInt should find 3")
	}
	if firstInt("none") != -1 {
		t.Error("firstInt of a no-digit string should be -1")
	}
}

func TestStencInstallHintIsOSAware(t *testing.T) {
	h := stencInstallHint()
	if h == "" {
		t.Fatal("install hint must never be empty")
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.Contains(h, "vendor") {
			t.Errorf("Windows hint should point at vendor tools: %q", h)
		}
	case "linux":
		if !strings.Contains(h, "stenc") {
			t.Errorf("Linux hint should name stenc/install: %q", h)
		}
	}
	// Every platform's message must reaffirm gpg as the portable layer OR name the
	// vendor-tools fallback — the "awareness, not dependence" throughline.
	if !strings.Contains(h, "gpg") && !strings.Contains(h, "vendor tools") && !strings.Contains(h, "outside") && !strings.Contains(h, "OUTSIDE") {
		t.Errorf("hint should frame the layer as outside gpg: %q", h)
	}
}

// TestStencAvailability asserts the OS gate: stenc is Linux-only, so on any other
// platform the feature is cleanly unavailable (never an error, just hidden).
func TestStencAvailability(t *testing.T) {
	app := dockApp(t)
	st := app.StencStatus()
	if runtime.GOOS != "linux" {
		if st["available"] != false {
			t.Errorf("stenc must be unavailable off Linux, got %v", st)
		}
		if st["hint"] == nil {
			t.Error("an unavailable stenc must carry an install/why hint")
		}
	}
}

// TestNoteTapeDriveEncryption_NonFatalWhenAbsent proves the write-path hook is
// harmless when stenc isn't installed: it must never flag a volume or panic.
func TestNoteTapeDriveEncryption_NonFatalWhenAbsent(t *testing.T) {
	app := dockApp(t)
	if app.stencAvailable() {
		t.Skip("stenc installed here — the absent-tool path can't be exercised")
	}
	vol := app.Store.AddVolume(Volume{Label: "LTO-TEST", Kind: "TAPE"})
	app.noteTapeDriveEncryption(vol.ID) // must be a no-op, never panic
	if got := app.Store.Volume(vol.ID); got.DriveEncrypted {
		t.Error("with stenc absent, a write must NOT flag the volume drive-encrypted")
	}
}

// TestSetDriveKey_RequiresTool proves the advanced action refuses cleanly when the
// tool is absent, rather than pretending to have changed the drive.
func TestSetDriveKey_RequiresTool(t *testing.T) {
	app := dockApp(t)
	if app.stencAvailable() {
		t.Skip("stenc installed here — the absent-tool path can't be exercised")
	}
	if err := app.SetDriveKey("", "/tmp/whatever.key", 1); err == nil {
		t.Error("SetDriveKey must error when stenc is unavailable")
	}
	if err := app.ClearDriveKey(""); err == nil {
		t.Error("ClearDriveKey must error when stenc is unavailable")
	}
}
