package main

// cardcheck_test.go — the "is this card already backed up?" check. It must match by
// CONTENT (not name/path) against the whole inventory, split the card into
// already-safe vs new, name where the safe ones live, give a correct safe-to-format
// verdict, and — critically — touch nothing (read-only: no source root, no ingest).

import "testing"

func hasStr(list []string, sub string) bool {
	for _, s := range list {
		if s == sub {
			return true
		}
	}
	return false
}

func TestCardCheck_BackedUpVsNew(t *testing.T) {
	app := newSetupApp(t)
	coll := app.Store.AddCollectionKind("Wedding", ArchiveSourced)
	src := t.TempDir()
	writeFile(t, src, "photo1.jpg", "AAA-content")
	writeFile(t, src, "photo2.jpg", "BBB-content")
	writeFile(t, src, "photo3.jpg", "CCC-content")
	scanInto(t, app, coll.ID, src)

	beforeSources := len(app.Store.SourceRoots())
	beforeFiles := len(app.Store.FilesOf(coll.ID))

	// The card: two frames whose CONTENT is already archived (different names/paths),
	// plus one genuinely new frame.
	card := t.TempDir()
	writeFile(t, card, "DCIM/100CANON/IMG_0001.JPG", "AAA-content") // == photo1
	writeFile(t, card, "DCIM/100CANON/IMG_0002.JPG", "BBB-content") // == photo2
	writeFile(t, card, "DCIM/100CANON/IMG_0003.JPG", "NEW-unsaved") // new

	res, err := app.CardCheck(card, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalFiles != 3 {
		t.Fatalf("total = %d, want 3", res.TotalFiles)
	}
	if res.BackedUpFiles != 2 || res.NewFiles != 1 {
		t.Fatalf("backed=%d new=%d, want 2 backed / 1 new", res.BackedUpFiles, res.NewFiles)
	}
	if res.SafeToFormat {
		t.Error("safe_to_format must be false while a new file exists")
	}
	// The new file is named so the user knows what to copy off.
	if len(res.New) != 1 || res.New[0].Rel != "DCIM/100CANON/IMG_0003.JPG" {
		t.Errorf("new list = %+v, want the one unsaved frame", res.New)
	}
	// Backed-up frames say WHERE they already live.
	if len(res.Sample) == 0 || !hasStr(res.Sample[0].Locations, "Archive: Wedding") {
		t.Errorf("backed-up sample missing its location: %+v", res.Sample)
	}
	if !hasStr(res.Where, "Archive: Wedding") {
		t.Errorf("where-summary should include the archive: %v", res.Where)
	}

	// READ-ONLY: the card was neither registered as a source nor ingested.
	if got := len(app.Store.SourceRoots()); got != beforeSources {
		t.Errorf("card check registered a source root (%d → %d)", beforeSources, got)
	}
	if got := len(app.Store.FilesOf(coll.ID)); got != beforeFiles {
		t.Errorf("card check mutated the catalog (files %d → %d)", beforeFiles, got)
	}
}

func TestCardCheck_SafeToFormat(t *testing.T) {
	app := newSetupApp(t)
	coll := app.Store.AddCollectionKind("Trip", ArchiveSourced)
	src := t.TempDir()
	writeFile(t, src, "a.jpg", "one")
	writeFile(t, src, "b.jpg", "two")
	scanInto(t, app, coll.ID, src)

	// A card holding only content that's already archived → safe to reformat.
	card := t.TempDir()
	writeFile(t, card, "IMG_1.JPG", "one")
	writeFile(t, card, "IMG_2.JPG", "two")
	res, err := app.CardCheck(card, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SafeToFormat || res.NewFiles != 0 || res.BackedUpFiles != 2 {
		t.Fatalf("expected safe-to-format with 2 backed/0 new, got %+v", res)
	}
}

// Content on an inventoried (adopted) drive counts too — not just the NAS archive.
func TestCardCheck_MatchesDriveSnapshot(t *testing.T) {
	app := newSetupApp(t)
	coll := app.Store.AddCollectionKind("Archive", ArchiveSourceless)
	vol := app.Store.AddVolume(Volume{Label: "OLD-DRIVE-7", Kind: "HDD"})
	// A snapshot of a drive we inventoried, holding a known frame by content hash.
	sha := "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabb"
	app.Store.PutVolumeSnapshot(&VolumeSnapshot{
		VolumeID: vol.ID, Label: "OLD-DRIVE-7", TotalFiles: 1,
		Files: []SnapFile{{RelPath: "2019/x.nef", Hash: sha, SizeBytes: 100}},
	})
	_ = coll

	idx := app.knownContent()
	if locs, ok := idx[sha]; !ok || !hasStr(locs, "Drive: OLD-DRIVE-7") {
		t.Errorf("known-content index missing the snapshot frame: %v (ok=%v)", idx[sha], ok)
	}
}
