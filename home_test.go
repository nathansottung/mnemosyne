package main

// home_test.go — the Home overview must read correctly for THREE very different
// users, from the same code path: a NAS-centric user (source archive, no shoebox
// drives), a shoebox user (a stack of adopted drives and no archive), and a mixed
// user (an archive plus drives — some of which are periodic backups of it, some of
// which are unorganized). Uses makeSnapshotDrive (plans_test.go) and writeTree.

import (
	"strings"
	"testing"
)

func newHomeApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &App{DataDir: dir, Store: store}
}

// A NAS user: one sourced archive, no adopted drives. Home is archive-centric with
// no ungrouped card and no incremental badges.
func TestHome_NASOnly(t *testing.T) {
	app := newHomeApp(t)
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.jpg": "AAA", "b.jpg": "BBB", "c.jpg": "CCC"})
	coll := app.Store.AddCollection("NAS Photos")
	if _, err := app.ScanFolder(coll.ID, src, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}

	d := app.HomeOverview(nil)
	if d.Empty {
		t.Fatal("a user with an archive must not read as empty")
	}
	if d.Totals.Archives != 1 || d.Totals.FilesKnown != 3 {
		t.Errorf("totals wrong: archives=%d files=%d, want 1 / 3", d.Totals.Archives, d.Totals.FilesKnown)
	}
	if d.Totals.VolumesKnown != 0 || d.Totals.VolumesSnapshot != 0 {
		t.Errorf("NAS-only has no drives, got %+v", d.Totals)
	}
	if d.Ungrouped != nil {
		t.Errorf("NAS-only has no ungrouped drive data, got %+v", d.Ungrouped)
	}
	if len(d.Archives) != 1 || d.Archives[0].Files != 3 || d.Archives[0].Sourceless {
		t.Errorf("archive card wrong: %+v", d.Archives)
	}
	if len(d.Incremental) != 0 {
		t.Errorf("no snapshots → no incremental, got %+v", d.Incremental)
	}
}

// A shoebox user: three adopted drives, no archive. Home shows the drive totals and
// a single ungrouped card covering everything; no archive cards, no incremental.
func TestHome_ShoeboxOnly(t *testing.T) {
	app := newHomeApp(t)
	d1 := t.TempDir()
	writeTree(t, d1, map[string]string{"p1.jpg": "P1", "p2.jpg": "P2"})
	d2 := t.TempDir()
	writeTree(t, d2, map[string]string{"q1.jpg": "Q1"})
	d3 := t.TempDir()
	writeTree(t, d3, map[string]string{"r1.jpg": "R1", "r2.jpg": "R2", "r3.jpg": "R3"})
	makeSnapshotDrive(t, app, d1, "S1", "DRIVE-1")
	makeSnapshotDrive(t, app, d2, "S2", "DRIVE-2")
	makeSnapshotDrive(t, app, d3, "S3", "DRIVE-3")

	d := app.HomeOverview(nil)
	if d.Empty {
		t.Fatal("a user with adopted drives must not read as empty")
	}
	if d.Totals.Archives != 0 {
		t.Errorf("shoebox user has no archives, got %d", d.Totals.Archives)
	}
	if d.Totals.VolumesKnown != 3 || d.Totals.VolumesSnapshot != 3 {
		t.Errorf("drives: got %+v, want 3 known / 3 snapshot", d.Totals)
	}
	if d.Totals.FilesKnown != 6 {
		t.Errorf("files_known=%d, want 6 distinct", d.Totals.FilesKnown)
	}
	if d.Ungrouped == nil || d.Ungrouped.Files != 6 || d.Ungrouped.Drives != 3 {
		t.Errorf("ungrouped wrong: %+v, want 6 files across 3 drives", d.Ungrouped)
	}
	if len(d.Incremental) != 0 {
		t.Errorf("no sourced archive → no incremental, got %+v", d.Incremental)
	}
}

// A mixed user: a sourced archive, TWO drives that are near-copies of it at
// different times (recognized as periodic backups, not mystery data), and one
// unrelated drive (ungrouped). Content shared with the archive is deduped in totals.
func TestHome_Mixed(t *testing.T) {
	app := newHomeApp(t)
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.jpg": "AAA", "b.jpg": "BBB", "c.jpg": "CCC", "d.jpg": "DDD"})
	coll := app.Store.AddCollection("NAS Master")
	if _, err := app.ScanFolder(coll.ID, src, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	// Two backup drives: a full copy and a 3-of-its-3-files subset — both entirely
	// inside the archive by content.
	b1 := t.TempDir()
	writeTree(t, b1, map[string]string{"a.jpg": "AAA", "b.jpg": "BBB", "c.jpg": "CCC", "d.jpg": "DDD"})
	b2 := t.TempDir()
	writeTree(t, b2, map[string]string{"a.jpg": "AAA", "b.jpg": "BBB", "c.jpg": "CCC"})
	makeSnapshotDrive(t, app, b1, "B1", "BACKUP-2023")
	makeSnapshotDrive(t, app, b2, "B2", "BACKUP-2024")
	// An unrelated drive → ungrouped mystery data.
	u := t.TempDir()
	writeTree(t, u, map[string]string{"x.raw": "XXX", "y.raw": "YYY", "z.raw": "ZZZ"})
	makeSnapshotDrive(t, app, u, "U1", "MYSTERY")

	d := app.HomeOverview(nil)
	if d.Empty || d.Totals.Archives != 1 {
		t.Fatalf("mixed: empty=%v archives=%d", d.Empty, d.Totals.Archives)
	}
	// Distinct content: 4 archive files + 3 unrelated = 7 (the backups dedup away).
	if d.Totals.FilesKnown != 7 {
		t.Errorf("files_known=%d, want 7 distinct", d.Totals.FilesKnown)
	}
	if d.Totals.VolumesKnown != 3 {
		t.Errorf("volumes_known=%d, want 3", d.Totals.VolumesKnown)
	}
	// Both backup drives recognized as PERIODIC backups of NAS Master.
	if len(d.Incremental) != 2 {
		t.Fatalf("want 2 recognized backups, got %d: %+v", len(d.Incremental), d.Incremental)
	}
	for _, e := range d.Incremental {
		if e.ArchiveName != "NAS Master" || !e.Periodic || !strings.Contains(e.Badge, "periodic backups of NAS Master") {
			t.Errorf("recognized-backup entry wrong: %+v", e)
		}
		if e.ContainPct < 85 {
			t.Errorf("containment should be high, got %.1f for %s", e.ContainPct, e.Label)
		}
	}
	// Only the unrelated drive is ungrouped.
	if d.Ungrouped == nil || d.Ungrouped.Files != 3 || d.Ungrouped.Drives != 1 {
		t.Errorf("ungrouped wrong: %+v, want 3 files on 1 drive (the mystery drive)", d.Ungrouped)
	}
	if len(d.Ungrouped.Labels) == 0 || d.Ungrouped.Labels[0] != "MYSTERY" {
		t.Errorf("ungrouped should name the MYSTERY drive, got %+v", d.Ungrouped.Labels)
	}
}
