package main

// copy_health_test.go — the multi-copy verification model: a failed medium
// verify records the failure on THAT copy and flags the package under-protected,
// but never marks the package FAILED while another verified copy (or the staged
// payload) survives. "Re-write this copy" then restores full redundancy.

import (
	"os"
	"path/filepath"
	"testing"
)

// countUnderProtected mirrors the UI rule: a package with ≥1 current copy but
// fewer than required verified current copies is under-protected.
func underProtected(c *Chunk, required int) bool {
	return c.CurrentCopyCount() > 0 && c.VerifiedCopyCount() < required
}

func TestCopyLevelVerifyAndRewrite(t *testing.T) {
	tools := nativeTools(t)
	app, _ := newTestApp(t, tools)
	src, refs := makeSource(t)
	required := app.LoadConfig().RequiredCopies
	if required < 2 {
		required = 2
	}

	c := app.Store.AddChunk(Chunk{
		Name: "HEALTH-PKG", Status: "PLANNED", MediaKind: "CUSTOM",
		TargetBytes: 1 << 30, DataBytes: 4096, FileCount: len(refs),
		SrcRoot: src, HashAlg: "SHA256", Par2: 5, Encrypted: false,
		Files: append([]ChunkFileRef{}, refs...),
	})
	if err := app.BuildChunk(c.ID, noProg); err != nil {
		t.Fatalf("BuildChunk: %v", err)
	}

	// Two volumes in two "locations" → two copies, both verified on write.
	volA := app.Store.AddVolume(Volume{Label: "ARCH-01", Kind: "HDD", Location: "office"})
	volB := app.Store.AddVolume(Volume{Label: "TAPE-01", Kind: "TAPE", Location: "off-site"})
	mediumA := t.TempDir()
	mediumB := t.TempDir()
	if _, err := app.WriteChunk(c.ID, mediumA, 0, 0, 0, volA.ID, noProg); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if _, err := app.WriteChunk(c.ID, mediumB, 0, 0, 0, volB.ID, noProg); err != nil {
		t.Fatalf("write B: %v", err)
	}
	c = app.Store.Chunk(c.ID)
	if c.Status != "VERIFIED" || c.VerifiedCopyCount() != 2 {
		t.Fatalf("after two writes: status=%s verified=%d, want VERIFIED 2", c.Status, c.VerifiedCopyCount())
	}
	if underProtected(c, required) {
		t.Fatalf("should be protected at 2/2")
	}

	// Corrupt the payload on medium A (bit-rot / bad medium), then verify it.
	payloadA := findPayload(mediumA, c)
	if payloadA == "" {
		t.Fatal("payload on medium A not found")
	}
	corrupt(t, payloadA)

	res, err := app.VerifyChunk(c.ID, mediumA)
	if err != nil {
		t.Fatalf("VerifyChunk(A): %v", err)
	}
	if ok, _ := res["verify_ok"].(bool); ok {
		t.Fatalf("verify of corrupted medium A should fail")
	}

	c = app.Store.Chunk(c.ID)
	// (4) package still healthy, one copy FAILED, under-protected.
	if c.Status == "FAILED" {
		t.Errorf("package must NOT be FAILED — a good copy survives (status=%s)", c.Status)
	}
	if c.Status != "VERIFIED" {
		t.Errorf("package status = %s, want VERIFIED (derived from the intact copy)", c.Status)
	}
	if got := c.VerifiedCopyCount(); got != 1 {
		t.Errorf("verified copies = %d, want 1 (A failed, B intact)", got)
	}
	if !underProtected(c, required) {
		t.Errorf("package should be under-protected (1 of %d verified)", required)
	}
	if copyVerifyState(c, volA.ID) != false {
		t.Errorf("copy on ARCH-01 should be FAILED")
	}
	if copyVerifyState(c, volB.ID) != true {
		t.Errorf("copy on TAPE-01 should still be verified")
	}

	// (3) Re-write the failed copy from staging to the same volume.
	if _, err := app.RewriteCopy(c.ID, volA.ID, 0, 0, 0, noProg); err != nil {
		t.Fatalf("RewriteCopy: %v", err)
	}
	c = app.Store.Chunk(c.ID)
	if c.Status != "VERIFIED" || c.VerifiedCopyCount() != 2 {
		t.Fatalf("after re-write: status=%s verified=%d, want VERIFIED 2/2", c.Status, c.VerifiedCopyCount())
	}
	if underProtected(c, required) {
		t.Errorf("should be protected again at 2/2 after re-write")
	}
	// The failed copy is retained in history as superseded (exactly one such).
	sup := 0
	for _, cp := range c.Copies {
		if cp.Superseded {
			sup++
			if cp.VerifyOK == nil || *cp.VerifyOK {
				t.Errorf("superseded copy should record the failure (verify_ok=false)")
			}
		}
	}
	if sup != 1 {
		t.Errorf("expected exactly 1 superseded copy in history, got %d", sup)
	}
	if got := c.CurrentCopyCount(); got != 2 {
		t.Errorf("current copies = %d, want 2 (superseded excluded)", got)
	}

	// The re-written medium A now verifies and restores cleanly.
	rv, err := app.VerifyChunk(c.ID, mediumA)
	if err != nil {
		t.Fatalf("re-verify A: %v", err)
	}
	if ok, _ := rv["verify_ok"].(bool); !ok {
		t.Fatalf("re-written medium A should verify OK")
	}
	out := t.TempDir()
	if _, err := app.RestoreChunk(c.ID, filepath.Join(mediumA, c.Name), out, nil, noProg); err != nil {
		t.Fatalf("restore from re-written A: %v", err)
	}
	assertRestored(t, src, out, refs)
}

// copyVerifyState returns the current (non-superseded) copy's verify_ok for a
// volume; false if missing/unset so callers get a clear signal.
func copyVerifyState(c *Chunk, volumeID int) bool {
	for _, cp := range c.Copies {
		if cp.VolumeID == volumeID && !cp.Superseded {
			return cp.VerifyOK != nil && *cp.VerifyOK
		}
	}
	return false
}

// corrupt flips the first byte of a file so its hash no longer matches.
func corrupt(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatalf("cannot corrupt empty file %s", path)
	}
	b[0] ^= 0xFF
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
