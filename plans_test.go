package main

// plans_test.go — the keystone end-to-end: author a plan against two overlapping
// drive SNAPSHOTS (drives "unplugged"), compile it, then EXECUTE in reverse order
// across a simulated process restart, and confirm a byte-perfect, de-duplicated
// destination plus correct coverage math the whole way.

import (
	"os"
	"path/filepath"
	"testing"
)

// makeSnapshotDrive registers a volume and stores a snapshot for a real folder,
// hashing every file — standing in for a dock-ingested drive.
func makeSnapshotDrive(t *testing.T, app *App, mountPath, serial, label string) *Volume {
	t.Helper()
	vol := app.Store.AddVolume(Volume{Label: label, Kind: "HDD", Serial: serial})
	var sfs []SnapFile
	var total int64
	_ = filepath.WalkDir(mountPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(mountPath, p)
		sha, herr := hashFileHex(p)
		if herr != nil {
			t.Fatal(herr)
		}
		st, _ := os.Stat(p)
		sfs = append(sfs, SnapFile{RelPath: filepath.ToSlash(rel), Hash: sha, SizeBytes: st.Size(), Role: RoleEditedExport})
		total += st.Size()
		return nil
	})
	app.Store.PutVolumeSnapshot(&VolumeSnapshot{VolumeID: vol.ID, Serial: serial, Label: label,
		Files: sfs, TotalFiles: len(sfs), TotalBytes: total})
	return vol
}

func TestPlan_ExecuteReverseAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{DataDir: dataDir, Store: store}

	// Two overlapping "drives": shared/s.jpg is byte-identical on both.
	d1, d2 := t.TempDir(), t.TempDir()
	writeTree(t, d1, map[string]string{"a.jpg": "AAA", "b.jpg": "BBB", "shared/s.jpg": "SHARED"})
	writeTree(t, d2, map[string]string{"shared/s.jpg": "SHARED", "c.jpg": "CCC"})
	makeSnapshotDrive(t, app, d1, "SER1", "DRIVE-01")
	makeSnapshotDrive(t, app, d2, "SER2", "DRIVE-02")

	tmpl := app.Store.AddTemplate(&Template{Name: "Move", Routes: map[string]string{RoleEditedExport: "photos/{orig_name}"}})
	destRoot := filepath.Join(t.TempDir(), "NAS") // fresh — does not exist yet
	plan := app.Store.AddPlan(&Plan{Name: "NAS Move", TemplateID: tmpl.ID, DestinationRoot: destRoot})

	// --- compile (dry-run gate) ---
	rep, err := app.CompilePlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Compilable || rep.Unrouted != 0 {
		t.Fatalf("plan should compile cleanly, got %+v", rep)
	}
	if rep.Files != 4 { // a, b, s, c — deduped by hash
		t.Fatalf("plan should map 4 unique files, got %d", rep.Files)
	}
	if rep.DedupeFiles != 1 { // shared/s.jpg lives on both drives
		t.Errorf("dedupe savings should be 1 (the shared file), got %d", rep.DedupeFiles)
	}
	wl := map[string]int{}
	for _, d := range rep.Drives {
		wl[d.Label] = d.Files
	}
	if wl["DRIVE-01"] != 3 || wl["DRIVE-02"] != 2 {
		t.Errorf("workload wrong: DRIVE-01=%d (want 3), DRIVE-02=%d (want 2)", wl["DRIVE-01"], wl["DRIVE-02"])
	}

	// --- execute DRIVE-02 first (reverse order) ---
	r2, err := app.ExecutePlanFromDrive(plan.ID, d2, "SER2", func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Copied != 2 || r2.DriveDiffers != 0 {
		t.Fatalf("DRIVE-02 should copy 2 files cleanly, got %+v", r2)
	}
	if r2.Coverage.Satisfied != 2 || r2.Coverage.Pct != 50 {
		t.Errorf("after DRIVE-02: want 2/4 (50%%), got %d/%d (%.0f%%)", r2.Coverage.Satisfied, r2.Coverage.Total, r2.Coverage.Pct)
	}
	if len(r2.Coverage.RemainingDrives) != 1 || r2.Coverage.RemainingDrives[0] != "DRIVE-01" {
		t.Errorf("remaining work should live on DRIVE-01, got %v", r2.Coverage.RemainingDrives)
	}

	// --- simulate a process restart: reopen the store from the same data dir ---
	store2, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	app2 := &App{DataDir: dataDir, Store: store2}

	// --- execute DRIVE-01: shared file already satisfied → confirmed, not recopied ---
	r1, err := app2.ExecutePlanFromDrive(plan.ID, d1, "SER1", func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Copied != 2 {
		t.Fatalf("DRIVE-01 should copy 2 new files (a,b), got %d", r1.Copied)
	}
	if r1.Confirmed != 1 {
		t.Errorf("DRIVE-01 should CONFIRM the shared file already placed, got %d", r1.Confirmed)
	}
	if r1.Coverage.Satisfied != 4 || r1.Coverage.Pct != 100 {
		t.Errorf("after DRIVE-01: want 4/4 (100%%), got %d/%d (%.0f%%)", r1.Coverage.Satisfied, r1.Coverage.Total, r1.Coverage.Pct)
	}

	// --- byte-perfect, de-duplicated destination ---
	want := map[string]string{"photos/a.jpg": "AAA", "photos/b.jpg": "BBB", "photos/s.jpg": "SHARED", "photos/c.jpg": "CCC"}
	for rel, body := range want {
		b, err := os.ReadFile(filepath.Join(destRoot, filepath.FromSlash(rel)))
		if err != nil || string(b) != body {
			t.Errorf("destination %s = %q (err %v), want %q", rel, b, err, body)
		}
	}
	// The plan closed itself at 100%.
	if p := app2.Store.Plan(plan.ID); p.Status != PlanClosed {
		t.Errorf("plan should close at 100%%, status = %s", p.Status)
	}

	// Source drives were never modified (read-only): originals intact.
	if b, _ := os.ReadFile(filepath.Join(d1, "a.jpg")); string(b) != "AAA" {
		t.Error("source drive must remain untouched")
	}
}

// A source that no longer matches its snapshot must be flagged and skipped, never
// silently copied.
func TestPlan_DriveDiffersFromSnapshot(t *testing.T) {
	app := dockApp(t)
	d1 := t.TempDir()
	writeTree(t, d1, map[string]string{"x.jpg": "ORIGINAL"})
	makeSnapshotDrive(t, app, d1, "SERX", "DRIVE-X")
	tmpl := app.Store.AddTemplate(&Template{Name: "M", Routes: map[string]string{RoleEditedExport: "out/{orig_name}"}})
	dest := filepath.Join(t.TempDir(), "out-root")
	plan := app.Store.AddPlan(&Plan{Name: "P", TemplateID: tmpl.ID, DestinationRoot: dest})
	if _, err := app.CompilePlan(plan.ID); err != nil {
		t.Fatal(err)
	}
	// Corrupt the source AFTER snapshotting — its bytes no longer match the hash.
	if err := os.WriteFile(filepath.Join(d1, "x.jpg"), []byte("TAMPERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := app.ExecutePlanFromDrive(plan.ID, d1, "SERX", func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r.DriveDiffers != 1 || r.Copied != 0 {
		t.Errorf("tampered source must be flagged and skipped, got %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dest, "out", "x.jpg")); !os.IsNotExist(err) {
		t.Error("a differing file must NOT be written to the destination")
	}
}
