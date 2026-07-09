package main

// incremental_test.go — the "Back up changes" workflow. The load-bearing promise is
// that each run copies ONLY the delta and is stateless (recomputed from the catalog),
// under both bases: "not yet on this volume" and "not fully protected". We prove
// only-the-delta lands by pinning an unchanged file's mtime and confirming a second
// run never touches it, and we confirm the named session history reads back correctly.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func scanInto(t *testing.T, app *App, collID int, root string) {
	t.Helper()
	if _, err := app.ScanFolder(collID, root, func(float64, string) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

// Base "volume": a second run copies only files whose content isn't already on that
// volume (new + modified) — never the unchanged ones.
func TestIncremental_VolumeBaseOnlyDelta(t *testing.T) {
	app := newSetupApp(t)
	coll := app.Store.AddCollectionKind("Work", ArchiveSourced)
	src := t.TempDir()
	writeFile(t, src, "a.txt", "alpha")
	writeFile(t, src, "b.txt", "bravo")
	writeFile(t, src, "c.txt", "charlie")
	scanInto(t, app, coll.ID, src)

	vol := app.Store.AddVolume(Volume{Label: "ARCH-01", Kind: "HDD"})
	dest := filepath.Join(t.TempDir(), "mirror")

	// First run: everything lands (nothing on the volume yet).
	r1, err := app.BackupChanges(coll.ID, nil, vol.ID, BaseVolume, ModeMirror, dest, 0, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Files != 3 {
		t.Fatalf("first run copied %d files, want 3", r1.Files)
	}
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dest, n)); err != nil {
			t.Errorf("expected %s mirrored: %v", n, err)
		}
	}

	// Pin b.txt's landed copy to an old mtime so we can prove run 2 never rewrites it.
	old := time.Now().Add(-3 * time.Hour)
	bDest := filepath.Join(dest, "b.txt")
	if err := os.Chtimes(bDest, old, old); err != nil {
		t.Fatal(err)
	}

	// Change the source: modify a.txt, add d.txt; b.txt and c.txt untouched.
	writeFile(t, src, "a.txt", "alpha-EDITED")
	writeFile(t, src, "d.txt", "delta")
	scanInto(t, app, coll.ID, src)

	// Preview should see exactly the two-file delta.
	d, err := app.BackupDeltaPreview(coll.ID, nil, vol.ID, BaseVolume, ModeMirror, dest)
	if err != nil {
		t.Fatal(err)
	}
	if d.Files != 2 {
		t.Fatalf("delta preview = %d files, want 2 (a modified + d new)", d.Files)
	}

	// Second run: only the delta copies.
	r2, err := app.BackupChanges(coll.ID, nil, vol.ID, BaseVolume, ModeMirror, dest, 0, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Files != 2 {
		t.Fatalf("second run copied %d files, want 2 (only the delta)", r2.Files)
	}
	// The untouched file must not have been rewritten (mtime preserved).
	if fi, err := os.Stat(bDest); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("b.txt was rewritten by the incremental run (mtime changed) — delta not respected")
	}
	// The edited file must now hold the new bytes.
	if got, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(got) != "alpha-EDITED" {
		t.Errorf("a.txt not updated to the edited content, got %q", got)
	}

	// A third run with no changes is a no-op that records no session.
	before := len(app.Store.BackupSessions(coll.ID))
	r3, err := app.BackupChanges(coll.ID, nil, vol.ID, BaseVolume, ModeMirror, dest, 0, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Files != 0 {
		t.Errorf("no-change run copied %d files, want 0", r3.Files)
	}
	if after := len(app.Store.BackupSessions(coll.ID)); after != before {
		t.Errorf("no-op run recorded a session (%d→%d)", before, after)
	}

	// Session history: two runs, newest first, with a formatted name.
	hist := app.Store.BackupSessions(coll.ID)
	if len(hist) != 2 {
		t.Fatalf("history = %d sessions, want 2", len(hist))
	}
	if hist[0].Files != 2 || hist[1].Files != 3 {
		t.Errorf("history file counts = %d,%d, want 2,3 (newest first)", hist[0].Files, hist[1].Files)
	}
	if hist[0].Name == "" || hist[0].VolumeLabel != "ARCH-01" {
		t.Errorf("session name/label wrong: %+v", hist[0])
	}
}

// Base "protection": the delta is only files below COMPLETE — files already fully
// protected (per profile) are excluded even though their content changed nowhere.
func TestIncremental_ProtectionBaseExcludesComplete(t *testing.T) {
	app := newSetupApp(t)
	coll := app.Store.AddCollectionKind("Photos", ArchiveSourced)
	// single-copy profile: one verified copy = COMPLETE.
	if err := app.Store.SetAssignment(coll.ID, "", "single-copy"); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeFile(t, src, "1.txt", "one")
	writeFile(t, src, "2.txt", "two")
	scanInto(t, app, coll.ID, src)

	vol := app.Store.AddVolume(Volume{Label: "ARCH-02", Kind: "HDD"})
	dest := filepath.Join(t.TempDir(), "mirror")

	if _, err := app.BackupChanges(coll.ID, nil, vol.ID, BaseProtection, ModeMirror, dest, 0, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	// Both files now have one verified copy → COMPLETE under single-copy.
	st := app.Store.FileStatuses(coll.ID)
	for _, f := range app.Store.FilesOf(coll.ID) {
		if st[f.ID] != StatusComplete {
			t.Fatalf("%s status = %q, want COMPLETE after one copy under single-copy profile", f.RelPath, st[f.ID])
		}
	}

	// Add a new, unprotected file.
	writeFile(t, src, "3.txt", "three")
	scanInto(t, app, coll.ID, src)

	// Protection-base delta must be exactly the one file that isn't COMPLETE.
	d, err := app.BackupDeltaPreview(coll.ID, nil, vol.ID, BaseProtection, ModeMirror, dest)
	if err != nil {
		t.Fatal(err)
	}
	if d.Files != 1 {
		t.Fatalf("protection delta = %d, want 1 (only the unprotected new file)", d.Files)
	}
	r, err := app.BackupChanges(coll.ID, nil, vol.ID, BaseProtection, ModeMirror, dest, 0, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 1 {
		t.Errorf("protection run copied %d, want 1", r.Files)
	}
	if r.Base != BaseProtection {
		t.Errorf("session base = %q, want protection", r.Base)
	}
}

// Sessions feed Home's periodic-backup recognition natively (no snapshot heuristic).
func TestIncremental_FeedsHomeRecognition(t *testing.T) {
	app := newSetupApp(t)
	coll := app.Store.AddCollectionKind("Client", ArchiveSourced)
	src := t.TempDir()
	writeFile(t, src, "x.txt", "ex")
	scanInto(t, app, coll.ID, src)
	vol := app.Store.AddVolume(Volume{Label: "ARCH-09", Kind: "HDD"})
	dest := filepath.Join(t.TempDir(), "m")
	if _, err := app.BackupChanges(coll.ID, nil, vol.ID, BaseVolume, ModeMirror, dest, 0, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	home := app.HomeOverview(nil)
	found := false
	for _, inc := range home.Incremental {
		if inc.VolumeID == vol.ID && inc.ArchiveID == coll.ID {
			found = true
			if inc.Badge == "" {
				t.Error("recognized backup has no badge")
			}
		}
	}
	if !found {
		t.Error("a backup session did not surface as a recognized backup on Home")
	}
}
