package main

// sourceless_test.go — an Archive whose file list is the deduped union of the media
// adopted into it (no source folder), and whose 3-2-1 offsite math derives from
// each volume's Location. The scenario: two "drives" (folders) with an overlapping
// file, adopted into two Locations (one offsite). The shared file must show up once
// in the union but as two copies across two locations, with offsite satisfied.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel string, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Of(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestSourcelessArchiveUnionAndLocations(t *testing.T) {
	app := dockApp(t)
	st := app.Store

	// A SOURCELESS archive — no source folders; scan/drift disabled.
	arch := st.AddCollectionKind("Scattered Drives", ArchiveSourceless)
	if !arch.IsSourceless() {
		t.Fatal("archive should be sourceless")
	}

	// Two locations: one onsite, one offsite.
	shoebox := st.AddLocation("Shoe Box #1", false, "hall closet")
	grandma := st.AddLocation("Grandma's house", true, "across town")

	// Two drives (volumes), one in each location.
	driveA := st.AddVolume(Volume{Label: "USB-A", Kind: "HDD"})
	driveB := st.AddVolume(Volume{Label: "USB-B", Kind: "HDD"})
	if err := st.SetVolumeLocation(driveA.ID, shoebox.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVolumeLocation(driveB.ID, grandma.ID); err != nil {
		t.Fatal(err)
	}

	// Two folder-"drives" with ONE overlapping file (same content "SHARED"),
	// plus one unique file each. Union by hash = {SHARED, ONLY-A, ONLY-B} = 3.
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, dirA, "photos/shared.jpg", "SHARED-CONTENT")
	writeFile(t, dirA, "photos/only-a.jpg", "ONLY-ON-A")
	// Same content on B but under a DIFFERENT path — still one union entry by hash.
	writeFile(t, dirB, "backup/dup.jpg", "SHARED-CONTENT")
	writeFile(t, dirB, "backup/only-b.jpg", "ONLY-ON-B")

	ra, err := app.AdoptFolder(dirA, arch.ID, driveA.ID, nil)
	if err != nil {
		t.Fatalf("adopt A: %v", err)
	}
	if ra["new_to_union"].(int) != 2 {
		t.Errorf("drive A should add 2 new files, got %v", ra["new_to_union"])
	}

	rb, err := app.AdoptFolder(dirB, arch.ID, driveB.ID, nil)
	if err != nil {
		t.Fatalf("adopt B: %v", err)
	}
	// Duplicate detection ACROSS drives: only "only-b" is new; the shared file is
	// already in the union.
	if rb["new_to_union"].(int) != 1 {
		t.Errorf("drive B should add exactly 1 new file (only-b), got %v", rb["new_to_union"])
	}
	if rb["duplicate_in_union"].(int) != 1 {
		t.Errorf("drive B should detect exactly 1 duplicate (shared), got %v", rb["duplicate_in_union"])
	}

	// The union IS the truth: 3 distinct files.
	if n := st.CollectionFileCount(arch.ID); n != 3 {
		t.Fatalf("union should be 3 distinct files, got %d", n)
	}

	// Find the shared file's ID (the File whose hash matches "SHARED-CONTENT").
	sharedHash := sha256Of("SHARED-CONTENT")
	sharedID := 0
	for _, f := range st.FilesOf(arch.ID) {
		if f.Hash == sharedHash {
			sharedID = f.ID
		}
	}
	if sharedID == 0 {
		t.Fatal("shared file not found in the union")
	}

	dims := st.fileDimsForCollection(arch.ID)
	shared := dims[sharedID]
	if shared.copies != 2 {
		t.Errorf("shared file should have 2 copies (one per drive), got %d", shared.copies)
	}
	if shared.locations != 2 {
		t.Errorf("shared file should span 2 locations, got %d", shared.locations)
	}
	if shared.offsite != 1 {
		t.Errorf("shared file should have exactly 1 offsite copy (Grandma's house), got %d", shared.offsite)
	}
	// Offsite requirement (the archive's default 3-2-1 Standard needs 1) is met for
	// the shared file — it has an offsite copy at Grandma's house.
	if shared.offsite < 1 {
		t.Errorf("offsite requirement not satisfied: have %d offsite copies, need ≥1", shared.offsite)
	}

	// A unique file lives on one drive only → 1 copy, 1 location, not offsite-covered
	// unless it happens to be on the offsite drive.
	for _, f := range st.FilesOf(arch.ID) {
		if f.Hash == sha256Of("ONLY-ON-A") {
			d := dims[f.ID]
			if d.copies != 1 || d.locations != 1 || d.offsite != 0 {
				t.Errorf("only-a should be 1 copy / 1 location / 0 offsite (onsite drive), got copies=%d loc=%d off=%d", d.copies, d.locations, d.offsite)
			}
		}
	}
}

// TestLocationOffsiteFlipRehomesVolumes proves offsite math reads through the
// Location: flipping a Location's Offsite re-homes every volume in it at once.
func TestLocationOffsiteFlipRehomesVolumes(t *testing.T) {
	st := dockApp(t).Store
	loc := st.AddLocation("Closet", false, "")
	v := st.AddVolume(Volume{Label: "V1", Kind: "HDD"})
	if err := st.SetVolumeLocation(v.ID, loc.ID); err != nil {
		t.Fatal(err)
	}
	locs := map[int]*Location{loc.ID: loc}
	if volumeOffsite(st.Volume(v.ID), locs) {
		t.Error("volume in an onsite location must read as onsite")
	}
	st.UpdateLocation(loc.ID, "Closet", true, "moved offsite")
	locs[loc.ID] = st.Location(loc.ID)
	if !volumeOffsite(st.Volume(v.ID), locs) {
		t.Error("flipping the location offsite must make the volume read as offsite")
	}
}
