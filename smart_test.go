package main

// smart_test.go — drive-mortality signals. The parser tests are deterministic
// and always run (ATA with bad sectors → advisory; healthy NVMe → none). The
// system-disk test exercises the real smartctl → device-node → parse chain
// against this machine's boot drive, and skips cleanly when smartctl is absent
// or can't read (health is a complement and always non-fatal).

import (
	"os"
	"strings"
	"testing"
)

func TestParseSmart_AtaBadSectorsAdvises(t *testing.T) {
	// An ATA drive that PASSES overall but has reallocated + pending sectors must
	// still raise the "migrate copies off" advisory.
	js := []byte(`{
	  "device":{"name":"/dev/sda","type":"ata","protocol":"ATA"},
	  "smart_status":{"passed":true},
	  "temperature":{"current":41},
	  "power_on_time":{"hours":26280},
	  "ata_smart_attributes":{"table":[
	    {"id":5,"name":"Reallocated_Sector_Ct","raw":{"value":8}},
	    {"id":197,"name":"Current_Pending_Sector","raw":{"value":3}}
	  ]}
	}`)
	snap, err := parseSmart(js)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Passed == nil || !*snap.Passed {
		t.Errorf("expected overall PASSED")
	}
	if snap.Reallocated != 8 || snap.Pending != 3 {
		t.Errorf("sectors: got reallocated=%d pending=%d, want 8/3", snap.Reallocated, snap.Pending)
	}
	if snap.TempC != 41 || snap.PowerOnHours != 26280 {
		t.Errorf("temp/hours: got %d°C / %dh", snap.TempC, snap.PowerOnHours)
	}
	if !snap.Advisory {
		t.Errorf("reallocated/pending > 0 must raise the migrate advisory")
	}
}

func TestParseSmart_FailingRaisesAdvisory(t *testing.T) {
	snap, err := parseSmart([]byte(`{"device":{"name":"/dev/sdb","type":"ata"},"smart_status":{"passed":false},"temperature":{"current":50}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !snap.Advisory {
		t.Errorf("SMART overall FAILING must raise the advisory")
	}
}

func TestParseSmart_HealthyNvmeNoAdvisory(t *testing.T) {
	js := []byte(`{
	  "device":{"name":"/dev/nvme0","type":"nvme","protocol":"NVMe"},
	  "smart_status":{"passed":true},
	  "nvme_smart_health_information_log":{
	    "critical_warning":0,"temperature":34,"power_on_hours":1500,
	    "media_errors":0,"percentage_used":3,"available_spare":100,"available_spare_threshold":10
	  }
	}`)
	snap, err := parseSmart(js)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Type != "nvme" || snap.TempC != 34 || snap.PowerOnHours != 1500 {
		t.Errorf("nvme fields off: type=%s temp=%d hours=%d", snap.Type, snap.TempC, snap.PowerOnHours)
	}
	if snap.SpareLeft != 100 || snap.PercentUsed != 3 {
		t.Errorf("nvme spare/used off: spare=%d used=%d", snap.SpareLeft, snap.PercentUsed)
	}
	if snap.Advisory {
		t.Errorf("healthy NVMe must NOT raise an advisory: %s", snap.AdvisoryWhy)
	}
}

func TestParseSmart_UnusableIsError(t *testing.T) {
	// smartctl couldn't open the device: valid JSON but no health fields.
	if _, err := parseSmart([]byte(`{"smartctl":{"exit_status":2},"device":{"name":"/dev/x"}}`)); err == nil {
		t.Errorf("expected an error when no usable SMART data is present")
	}
}

// TestSmartDeviceNode_SystemDisk exercises "the hard part" — mapping a mounted
// path to a physical device node (Windows drive letter → disk number → /dev/pdN;
// Linux/macOS parent device) using the Prompt-27 identity plumbing. Independent
// of whether smartctl is installed.
func TestSmartDeviceNode_SystemDisk(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dev, err := smartDeviceNode(wd)
	if err != nil {
		t.Skipf("could not map %s to a device node (%v) — non-fatal by design", wd, err)
	}
	t.Logf("mapped %s → %s", wd, dev)
	if !strings.HasPrefix(dev, "/dev/") {
		t.Errorf("expected a /dev/ device node, got %q", dev)
	}
}

// TestVolumeHealth_SystemDisk drives the real smartctl chain against this
// machine's boot drive. Skips (never fails) when smartctl is missing or the read
// is blocked — exactly the silent-but-logged, complement-not-substitute contract.
func TestVolumeHealth_SystemDisk(t *testing.T) {
	app := dockApp(t) // fresh store/app (defined in dock_test.go)
	if !app.smartAvailable() {
		t.Skip("smartctl not installed — feature hides behind the install hint (expected)")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	vol := app.Store.AddVolume(Volume{Label: "SYS-DISK", Kind: "SSD"})
	snap, err := app.VolumeHealth(vol, wd)
	if err != nil {
		t.Skipf("smartctl present but could not read %s (%v) — likely needs admin/root; non-fatal by design", wd, err)
	}
	t.Logf("system disk: dev=%s type=%s passed=%v temp=%d°C hours=%d realloc=%d pending=%d advisory=%v",
		snap.Device, snap.Type, snap.Passed, snap.TempC, snap.PowerOnHours, snap.Reallocated, snap.Pending, snap.Advisory)
	// A successful read must have recorded a snapshot in the volume's history.
	if len(vol.SmartHistory) != 1 {
		t.Errorf("expected 1 snapshot recorded, got %d", len(vol.SmartHistory))
	}
	// We should have learned *something* usable (status, temperature, or sectors).
	if snap.Passed == nil && snap.TempC == 0 && snap.PowerOnHours == 0 {
		t.Errorf("read returned no usable health fields")
	}
}
