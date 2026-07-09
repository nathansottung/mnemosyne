package main

// quarantine_test.go — the never-delete, made-usable contract:
//   1. inside managed territory a mark→move stages into _deleted (structure preserved),
//      shows the protection consequence, pulls the copy from accounting, and un-quarantine
//      round-trips both bytes and the copy record;
//   2. the action is absent on adopted/unmanaged media and refused for source roots;
//   3. a hand-emptied _deleted reconciles gracefully to HUMAN-REMOVED, history retained.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// quarantineScenario builds a managed destination root by executing a real plan, then
// records a second, independent verified copy offsite — so quarantining the placed
// RAWs demonstrably drops them from 2 copies to 1. Returns the app, the managed
// destination root, and the archive ID.
func quarantineScenario(t *testing.T) (*App, string, int) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{DataDir: dataDir, Store: store}

	// A drive of two RAWs, snapshotted (roles = RAW so the template routes them).
	d1 := t.TempDir()
	writeTree(t, d1, map[string]string{"a.nef": "AAA", "b.nef": "BBB"})
	vol := app.Store.AddVolume(Volume{Label: "DRIVE-01", Kind: "HDD", Serial: "SER1"})
	var sfs []SnapFile
	var total int64
	_ = filepath.WalkDir(d1, func(p string, dd os.DirEntry, e error) error {
		if e != nil || dd.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(d1, p)
		sha, herr := hashFileHex(p)
		if herr != nil {
			t.Fatal(herr)
		}
		st, _ := os.Stat(p)
		sfs = append(sfs, SnapFile{RelPath: filepath.ToSlash(rel), Hash: sha, SizeBytes: st.Size(), Role: RoleOriginals})
		total += st.Size()
		return nil
	})
	app.Store.PutVolumeSnapshot(&VolumeSnapshot{VolumeID: vol.ID, Serial: "SER1", Label: "DRIVE-01",
		Files: sfs, TotalFiles: len(sfs), TotalBytes: total})

	// Adopt into a sourceless archive and group as an Event so the consequence reads
	// "Smith Wedding originals".
	coll := app.Store.AddCollectionKind("Smith Wedding", ArchiveSourceless)
	var items []unionFile
	for _, sf := range sfs {
		items = append(items, unionFile{RelPath: sf.RelPath, Hash: sf.Hash, Size: sf.SizeBytes, Role: RoleOriginals})
	}
	ids, _ := app.Store.UpsertUnionFiles(coll.ID, items)
	ev := app.Store.AddEvent(&Event{CollectionID: coll.ID, Name: "Smith Wedding"})
	app.Store.AssignFilesToEvent(ids, ev.ID)

	// A plan reorganizes them into a fresh managed destination root, then executes.
	tmpl := app.Store.AddTemplate(&Template{Name: "Move", Routes: map[string]string{RoleOriginals: "raw/{orig_name}"}})
	destRoot := filepath.Join(t.TempDir(), "NAS")
	plan := app.Store.AddPlan(&Plan{Name: "NAS Move", TemplateID: tmpl.ID, ArchiveIDs: []int{coll.ID}, DestinationRoot: destRoot})
	if _, err := app.CompilePlan(plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ExecutePlanFromDrive(plan.ID, d1, "SER1", func(float64, string) {}); err != nil {
		t.Fatal(err)
	}

	// A second, independent verified copy offsite → so quarantine drops 2 → 1.
	var mrefs []ChunkFileRef
	for _, f := range app.Store.FilesOf(coll.ID) {
		mrefs = append(mrefs, ChunkFileRef{FileID: f.ID, RelPath: "raw/" + filepath.Base(f.RelPath), SizeBytes: f.SizeBytes, Hash: f.Hash})
	}
	v2 := app.Store.AddVolume(Volume{Label: "OFFSITE", Kind: "HDD"})
	app.upsertMirrorChunk(coll.ID, v2, "/offsite", mrefs, "mirror")

	return app, destRoot, coll.ID
}

// destMirrorFileCount is the file count of the plan's destination mirror package (the
// copy quarantine pulls from / restores to). -1 if not found.
func destMirrorFileCount(app *App, collID int, root string) int {
	for _, c := range app.Store.Chunks(collID) {
		if c.Mirror && normPath(c.WrittenDest) == normPath(root) {
			return c.FileCount
		}
	}
	return -1
}

func TestQuarantine_ManagedTerritoryRoundTrip(t *testing.T) {
	app, destRoot, collID := quarantineScenario(t)
	rawDir := filepath.Join(destRoot, "raw")

	// The protection consequence is computed BEFORE anything moves.
	cons, err := app.QuarantineConsequence(rawDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cons.IsDir || cons.FileCount != 2 {
		t.Fatalf("consequence: is_dir=%v files=%d, want dir with 2 files", cons.IsDir, cons.FileCount)
	}
	if len(cons.Warnings) != 1 || !strings.Contains(cons.Warnings[0], "Smith Wedding originals to 1 copy") {
		t.Fatalf("consequence warnings = %v, want one 'Smith Wedding originals to 1 copy'", cons.Warnings)
	}
	if cons.MinCopiesAfter != 1 {
		t.Errorf("min copies after = %d, want 1", cons.MinCopiesAfter)
	}

	// Quarantine the folder → moved into _deleted, structure preserved, originals gone.
	e, err := app.Quarantine(rawDir, "", "duplicate export")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rawDir); !os.IsNotExist(err) {
		t.Error("the quarantined folder must no longer exist at its original path")
	}
	if b, err := os.ReadFile(filepath.Join(destRoot, "_deleted", "raw", "a.nef")); err != nil || string(b) != "AAA" {
		t.Errorf("staged bytes missing/wrong under _deleted: %q err=%v", b, err)
	}
	// The copy is pulled from protection accounting: the dest mirror package is emptied.
	if n := destMirrorFileCount(app, collID, e.Root); n != 0 {
		t.Errorf("dest mirror package should be emptied by quarantine, has %d files", n)
	}
	if e.By != "user" || e.Reason != "duplicate export" || e.Status != QuarantineActive {
		t.Errorf("entry record wrong: by=%q reason=%q status=%q", e.By, e.Reason, e.Status)
	}

	// Un-quarantine → reverse move restores bytes AND re-credits the copy.
	if _, err := app.UnQuarantine(e.ID); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(rawDir, "a.nef")); err != nil || string(b) != "AAA" {
		t.Errorf("un-quarantine must restore the original file: %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "_deleted", "raw", "a.nef")); !os.IsNotExist(err) {
		t.Error("staged bytes should be gone from _deleted after un-quarantine")
	}
	if n := destMirrorFileCount(app, collID, e.Root); n != 2 {
		t.Errorf("dest mirror package should be re-credited to 2 files, has %d", n)
	}
	if ee := app.Store.QuarantineEntry(e.ID); ee == nil || ee.Status != QuarantineRestored {
		t.Errorf("entry should be RESTORED, got %+v", ee)
	}
}

func TestQuarantine_AbsentOnAdoptedAndRefusedForSources(t *testing.T) {
	app, destRoot, _ := quarantineScenario(t)

	// Eligible inside managed territory.
	if !app.Store.QuarantineEligible(filepath.Join(destRoot, "raw", "a.nef")) {
		t.Error("a file inside managed territory should be quarantine-eligible")
	}

	// Absent on adopted / unmanaged media — a plain volume the tool did not populate.
	adopted := t.TempDir()
	writeTree(t, adopted, map[string]string{"x.jpg": "X"})
	if app.Store.QuarantineEligible(filepath.Join(adopted, "x.jpg")) {
		t.Error("adopted/unmanaged media must not be quarantine-eligible")
	}
	if _, err := app.Quarantine(filepath.Join(adopted, "x.jpg"), "", ""); err == nil {
		t.Error("quarantine outside managed territory must be refused")
	}

	// Refused for a registered source root — the read-only guard forbids it.
	srcColl := app.Store.AddCollection("Live Photos")
	srcDir := t.TempDir()
	writeTree(t, srcDir, map[string]string{"orig.nef": "ORIG"})
	app.Store.AddFolder(srcColl.ID, srcDir)
	if app.Store.QuarantineEligible(filepath.Join(srcDir, "orig.nef")) {
		t.Error("a source root must never be quarantine-eligible")
	}
	_, err := app.Quarantine(filepath.Join(srcDir, "orig.nef"), "", "")
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Errorf("quarantine of a source file must be refused as read-only source, got %v", err)
	}
	// And the source file is untouched.
	if b, _ := os.ReadFile(filepath.Join(srcDir, "orig.nef")); string(b) != "ORIG" {
		t.Error("a refused quarantine must never move the source file")
	}
}

func TestQuarantine_ReconcileHumanRemoved(t *testing.T) {
	app, destRoot, _ := quarantineScenario(t)
	e, err := app.Quarantine(filepath.Join(destRoot, "raw"), "", "")
	if err != nil {
		t.Fatal(err)
	}

	// The user empties _deleted by hand — the tool never does this itself.
	if err := os.RemoveAll(filepath.Join(destRoot, "_deleted")); err != nil {
		t.Fatal(err)
	}
	if n := app.ReconcileQuarantine(); n != 1 {
		t.Fatalf("reconcile should mark 1 entry human-removed, got %d", n)
	}
	ee := app.Store.QuarantineEntry(e.ID)
	if ee == nil || ee.Status != QuarantineHumanGone || ee.RemovedAt == nil {
		t.Fatalf("entry should be HUMAN-REMOVED with a timestamp, got %+v", ee)
	}
	// History retained; restoring vanished bytes is refused.
	if _, err := app.UnQuarantine(e.ID); err == nil {
		t.Error("un-quarantine of a human-removed entry must fail")
	}
	// Idempotent — a second reconcile is a no-op.
	if n := app.ReconcileQuarantine(); n != 0 {
		t.Errorf("second reconcile should be a no-op, got %d", n)
	}
}
