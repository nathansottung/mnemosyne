package main

// dock_test.go — the guided dock ingest, end-to-end with folders standing in
// for drives (serials supplied explicitly, as the watcher would after resolving
// a real device). Two "drives" are ingested sequentially and one is re-inserted;
// we assert content-match coverage grows, the re-insert is recognized by serial
// (re-verify, not a duplicate), the inventory sidecar lands on the DRIVE, and the
// NAS source is never written to. No external tools — dock needs none.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dockApp(t *testing.T) *App {
	t.Helper()
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return &App{DataDir: dataDir, Store: store}
}

// writeTree writes rel->content files under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDockIngest_TwoDrivesSequentialAndReinsert(t *testing.T) {
	app := dockApp(t)

	// A source archive on the "NAS": three cataloged source files.
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"a.txt":      "alpha content\n",
		"sub/b.txt":  "bravo content in a subfolder\n",
		"deep/c.txt": "charlie, the third file\n",
	})
	coll := app.Store.AddCollection("Photos")
	if n, err := app.ScanFolder(coll.ID, src, func(float64, string) {}); err != nil || n != 3 {
		t.Fatalf("scan: n=%d err=%v", n, err)
	}

	// Drive A holds copies of two of the source files, plus one foreign file.
	driveA := t.TempDir()
	writeTree(t, driveA, map[string]string{
		"a.txt":            "alpha content\n",                // matches
		"photos/b.txt":     "bravo content in a subfolder\n", // matches (path differs, content matches)
		"random/foreign.x": "not part of any archive\n",      // other/unrecognized
	})

	ds, err := app.StartDockSession([]int{coll.ID})
	if err != nil {
		t.Fatalf("StartDockSession: %v", err)
	}

	// --- Ingest drive A ---
	rA, err := app.IngestDrive(ds.ID, driveA, "SERIAL-A", "DRIVE-A", "", func(float64, string) {})
	if err != nil {
		t.Fatalf("ingest A: %v", err)
	}
	if got := rA["matched"].(int); got != 2 {
		t.Errorf("drive A matched: want 2, got %d", got)
	}
	if got := rA["other"].(int); got < 1 {
		t.Errorf("drive A should report ≥1 unrecognized file, got %d", got)
	}
	if rA["mode"] != "adopt" {
		t.Errorf("drive A should be an adopt, got %v", rA["mode"])
	}
	covA := rA["coverage"].(Coverage)
	if covA.CoveredFiles != 2 || covA.TotalFiles != 3 {
		t.Errorf("after A: want 2/3 covered, got %d/%d", covA.CoveredFiles, covA.TotalFiles)
	}
	// The inventory sidecar must be on the DRIVE, not the NAS source.
	if _, err := os.Stat(filepath.Join(driveA, dockSidecarDir, "INVENTORY.md")); err != nil {
		t.Errorf("expected inventory sidecar on drive A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driveA, dockSidecarDir, "catalog_snapshot.json")); err != nil {
		t.Errorf("expected catalog snapshot on drive A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, dockSidecarDir)); err == nil {
		t.Error("source (NAS) must NEVER be written to — found a sidecar there")
	}
	volA := app.Store.VolumeBySerial("SERIAL-A")
	if volA == nil {
		t.Fatal("drive A should have registered a volume matchable by serial")
	}

	// --- Ingest drive B (the missing third file) ---
	driveB := t.TempDir()
	writeTree(t, driveB, map[string]string{"archive/c.txt": "charlie, the third file\n"})
	rB, err := app.IngestDrive(ds.ID, driveB, "SERIAL-B", "DRIVE-B", "", func(float64, string) {})
	if err != nil {
		t.Fatalf("ingest B: %v", err)
	}
	if got := rB["matched"].(int); got != 1 {
		t.Errorf("drive B matched: want 1, got %d", got)
	}
	covB := rB["coverage"].(Coverage)
	if covB.CoveredFiles != 3 || covB.Pct != 100 {
		t.Errorf("after B: want 3/3 (100%%), got %d/%d (%.1f%%)", covB.CoveredFiles, covB.TotalFiles, covB.Pct)
	}

	// --- Re-insert drive A (a different mount path, SAME serial) ---
	driveA2 := t.TempDir()
	writeTree(t, driveA2, map[string]string{
		"a.txt":        "alpha content\n",
		"photos/b.txt": "bravo content in a subfolder\n",
	})
	rR, err := app.IngestDrive(ds.ID, driveA2, "SERIAL-A", "DRIVE-A", "", func(float64, string) {})
	if err != nil {
		t.Fatalf("re-insert A: %v", err)
	}
	if rR["reinserted"] != true {
		t.Errorf("re-inserted drive A should be recognized by serial: %v", rR["reinserted"])
	}
	if rR["mode"] != "reverify" {
		t.Errorf("re-inserted drive should re-verify, not re-adopt: got %v", rR["mode"])
	}
	if int(rR["volume_id"].(int)) != volA.ID {
		t.Errorf("re-insert must reuse the same volume %d, got %v", volA.ID, rR["volume_id"])
	}

	// The session must list exactly two drives (A re-verified in place, not duped).
	ds = app.Store.DockSession(ds.ID)
	if len(ds.Drives) != 2 {
		t.Fatalf("session should hold 2 drives (A, B), got %d", len(ds.Drives))
	}
	var aRow *DockDrive
	for i := range ds.Drives {
		if ds.Drives[i].VolumeID == volA.ID {
			aRow = &ds.Drives[i]
		}
	}
	if aRow == nil || aRow.Mode != "reverify" {
		t.Errorf("drive A's row should now read re-verify: %+v", aRow)
	}

	// Exportable session report carries the documentation trail.
	report, err := app.SessionReportMarkdown(ds.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERIAL-A", "SERIAL-B", "100.0% covered", "Drives processed", "READ-ONLY"} {
		if !strings.Contains(report, want) {
			t.Errorf("session report missing %q", want)
		}
	}

	// Source files remain pristine (read-only toward sources).
	if b, err := os.ReadFile(filepath.Join(src, "a.txt")); err != nil || string(b) != "alpha content\n" {
		t.Errorf("source file must be untouched, got %q err=%v", b, err)
	}
}
