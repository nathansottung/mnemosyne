package main

// volume_identity_test.go — physical device identity capture, the next-barcode
// scheme, and printable-label generation. The label + barcode-scheme tests are
// pure Go and always run; the device-identity test exercises the real platform
// tool on the local system disk and skips cleanly (identity is non-fatal by
// design) when the OS can't resolve it — e.g. a CI container on tmpfs.

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNextBarcode(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := st.NextBarcode("NSP"); got != "NSP-0001" {
		t.Fatalf("empty store: want NSP-0001, got %s", got)
	}
	st.AddVolume(Volume{Label: "a", Barcode: "NSP-0001"})
	st.AddVolume(Volume{Label: "b", Barcode: "NSP-0003"}) // gap: max is 3
	st.AddVolume(Volume{Label: "c", Barcode: "ABC-9"})    // different scheme, ignored for NSP
	st.AddVolume(Volume{Label: "d"})                      // no barcode, ignored
	if got := st.NextBarcode("NSP"); got != "NSP-0004" {
		t.Errorf("want NSP-0004 (max+1), got %s", got)
	}
	if got := st.NextBarcode("ABC"); got != "ABC-0010" {
		t.Errorf("want ABC-0010, got %s", got)
	}
	if got := st.NextBarcode(""); got != "NSP-0004" { // blank prefix defaults to NSP
		t.Errorf("blank prefix should default to NSP: got %s", got)
	}
}

func TestVolumeLabelHTML(t *testing.T) {
	v := &Volume{ID: 7, Label: "NSP-0007", Kind: "TAPE", Barcode: "NSP-0007",
		Serial: "WD-ABC123", Model: "HGST 8TB", DeviceSize: 8_001_563_222_016, Location: "office safe"}
	page, err := volumeLabelHTML(v, v.Barcode, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NSP-0007", "WD-ABC123", "HGST 8TB", "office safe", "8.0 TB"} {
		if !strings.Contains(page, want) {
			t.Errorf("label missing %q", want)
		}
	}
	// Both a Code128 barcode and a QR image, inlined as data URIs.
	if n := strings.Count(page, "data:image/png;base64,"); n < 2 {
		t.Errorf("expected a Code128 and a QR image (2 data URIs), got %d", n)
	}
	// A volume with no barcode falls back to a VOL-<id> Code128.
	v2 := &Volume{ID: 42, Label: "nobar"}
	h2, err := volumeLabelHTML(v2, v2.Barcode, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h2, "VOL-42") {
		t.Errorf("expected fallback VOL-42 barcode text")
	}
}

// TestDeviceIdentityAndLabel_SystemDisk resolves the physical device behind the
// working directory (the local system disk) and generates a label for it. It
// skips — never fails — when the OS/tool can't resolve identity, matching the
// non-fatal contract.
func TestDeviceIdentityAndLabel_SystemDisk(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	id, err := resolveDeviceIdentity(wd)
	if err != nil || !id.resolved() {
		t.Skipf("device identity unavailable for %s (err=%v) — non-fatal by design", wd, err)
	}
	t.Logf("system disk resolved: serial=%q model=%q size=%d bus=%q", id.Serial, id.Model, id.SizeBytes, id.Bus)

	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{DataDir: t.TempDir(), Store: st}
	v := st.AddVolume(Volume{Label: "SYS-DISK", Kind: "SSD"})
	if _, changed := app.resolveVolumeIdentity(v, wd); !changed {
		t.Fatal("resolveVolumeIdentity reported no change though identity resolved")
	}
	st.UpdateVolume(v)
	if v.DeviceAt == nil {
		t.Error("DeviceAt should be stamped after a successful resolve")
	}
	if v.Serial == "" && v.Model == "" && v.DeviceSize == 0 {
		t.Error("volume gained no identity fields despite a resolved device")
	}

	page, err := volumeLabelHTML(v, v.Barcode, "", "")
	if err != nil {
		t.Fatalf("label generation for the system disk failed: %v", err)
	}
	for _, want := range []string{"data:image/png;base64,", "SYS-DISK", "VOL-" + strconv.Itoa(v.ID)} {
		if !strings.Contains(page, want) {
			t.Errorf("system-disk label missing %q", want)
		}
	}
	if v.Model != "" && !strings.Contains(page, v.Model) {
		t.Errorf("label should show the resolved model %q", v.Model)
	}
}
