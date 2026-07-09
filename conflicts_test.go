package main

// conflicts_test.go — fabricate all three collision classes in a sourceless
// union and verify: (a) second-shooter frames auto-pass (no queue item), (b) &
// (c) open true conflicts, a plan REFUSES to compile while they're open and
// ACCEPTS once resolved, a keep-both resolution compiles to two placements, and a
// canonical resolution folds the loser into the winner's retained version history.

import (
	"testing"
	"time"
)

func TestConflictClasses_DetectResolveAndPlanGate(t *testing.T) {
	app := dockApp(t)
	coll := app.Store.AddCollectionKind("Union", ArchiveSourceless)
	t1 := time.Date(2019, 6, 15, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2019, 6, 15, 11, 0, 0, 0, time.UTC)

	app.Store.UpsertUnionFiles(coll.ID, []unionFile{
		// (a) second shooter — same name+timestamp, DIFFERENT body → NOT a conflict.
		{RelPath: "shoot/DSC1.jpg", Hash: "a1", Size: 100, ShotAt: t1, Role: RoleDeliverables, CameraSerial: "CAM-A"},
		{RelPath: "shoot/DSC1.jpg", Hash: "a2", Size: 101, ShotAt: t1, Role: RoleDeliverables, CameraSerial: "CAM-B"},
		// (b) same metadata AND same body, different bytes → TRUE conflict.
		{RelPath: "shoot/DSC2.jpg", Hash: "b1", Size: 200, ShotAt: t2, Role: RoleDeliverables, CameraSerial: "CAM-X"},
		{RelPath: "shoot/DSC2.jpg", Hash: "b2", Size: 201, ShotAt: t2, Role: RoleDeliverables, CameraSerial: "CAM-X"},
		// (b-RAW) a RAW original that disagrees — should carry the RAW alert.
		{RelPath: "raw/DSC9.nef", Hash: "r1", Size: 900, ShotAt: t2, Role: RoleOriginals, CameraSerial: "CAM-X"},
		{RelPath: "raw/DSC9.nef", Hash: "r2", Size: 901, ShotAt: t2, Role: RoleOriginals, CameraSerial: "CAM-X"},
		// (c) same path/name, NO EXIF, different bytes → TRUE conflict.
		{RelPath: "docs/report.pdf", Hash: "c1", Size: 300},
		{RelPath: "docs/report.pdf", Hash: "c2", Size: 301},
	})

	scan := app.DetectConflicts(coll.ID)
	if scan.SecondShooter != 1 {
		t.Errorf("second-shooter groups = %d, want 1 (the two-body DSC1)", scan.SecondShooter)
	}
	if scan.TrueConflicts != 3 || scan.Open != 3 {
		t.Fatalf("true conflicts = %d, open = %d; want 3/3 (DSC2, DSC9.nef, report.pdf)", scan.TrueConflicts, scan.Open)
	}

	views := app.Store.ConflictViews(coll.ID, true)
	if len(views) != 3 {
		t.Fatalf("review queue should hold 3 conflicts, got %d", len(views))
	}
	byPath := map[string]ConflictView{}
	for _, v := range views {
		byPath[v.RelPath] = v
		// The second shooter must never be queued.
		if v.RelPath == "shoot/DSC1.jpg" {
			t.Errorf("second-shooter DSC1 must NOT be a conflict")
		}
		if len(v.Files) != 2 {
			t.Errorf("conflict %s should show 2 versions, got %d", v.RelPath, len(v.Files))
		}
	}
	if byPath["shoot/DSC2.jpg"].Class != ClassSameMeta {
		t.Errorf("DSC2 class = %q, want SAME-META", byPath["shoot/DSC2.jpg"].Class)
	}
	if byPath["docs/report.pdf"].Class != ClassNoEXIF {
		t.Errorf("report.pdf class = %q, want NO-EXIF", byPath["docs/report.pdf"].Class)
	}
	if !byPath["raw/DSC9.nef"].RawAlert {
		t.Errorf("RAW conflict must carry the raw alert")
	}

	// A plan REFUSES to compile while true conflicts are open.
	tmpl := app.Store.Templates()[0] // built-in Photographer
	if pv := app.RoutePreview(tmpl, coll.ID); !pv.Blocked || pv.UnresolvedConflicts != 3 {
		t.Fatalf("plan should be blocked by 3 conflicts, got blocked=%v n=%d", pv.Blocked, pv.UnresolvedConflicts)
	}

	// Resolve: DSC2 keep-both, DSC9 + report.pdf canonical (first version wins).
	dsc2 := byPath["shoot/DSC2.jpg"]
	if err := app.Store.ResolveConflict(dsc2.ID, ResolveKeepBoth, 0); err != nil {
		t.Fatal(err)
	}
	pdf := byPath["docs/report.pdf"]
	canonicalPDF := pdf.FileIDs[0]
	if err := app.Store.ResolveConflict(pdf.ID, ResolveCanonical, canonicalPDF); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.ResolveConflict(byPath["raw/DSC9.nef"].ID, ResolveCanonical, byPath["raw/DSC9.nef"].FileIDs[0]); err != nil {
		t.Fatal(err)
	}

	// Canonical folded the loser into the winner's retained history; nothing lost.
	win := app.Store.FileByID(canonicalPDF)
	if win == nil || len(win.Versions) != 1 {
		t.Fatalf("canonical winner should retain 1 alternate version, got %+v", win)
	}
	// The two canonical losers were removed from the current file set (8 → 6).
	if n := len(app.Store.FilesOf(coll.ID)); n != 6 {
		t.Errorf("after 2 canonical resolutions want 6 current files, got %d", n)
	}

	// With everything resolved, the plan ACCEPTS.
	if pv := app.RoutePreview(tmpl, coll.ID); pv.Blocked || pv.UnresolvedConflicts != 0 {
		t.Fatalf("plan should accept after resolution, got blocked=%v n=%d", pv.Blocked, pv.UnresolvedConflicts)
	}

	// Keep-both compiles to TWO placements: assign both DSC2 versions to an event so
	// they route, and confirm the destination collision is auto-disambiguated.
	ev := app.Store.AddEvent(&Event{Name: "Smith", EventType: "wedding", Year: 2019, CollectionID: coll.ID})
	app.Store.AssignFilesToEvent(dsc2.FileIDs, ev.ID)
	pv := app.RoutePreview(tmpl, coll.ID)
	if pv.Placed != 2 {
		t.Errorf("keep-both DSC2 should compile to 2 placements, got %d", pv.Placed)
	}
	if pv.Disambiguated < 1 {
		t.Errorf("the destination collision should be auto-disambiguated, got %d", pv.Disambiguated)
	}
	// The two placements must be distinct destinations.
	dests := map[string]bool{}
	for _, e := range pv.Examples {
		dests[e.Dest] = true
	}
	if len(dests) != 2 {
		t.Errorf("expected 2 distinct destinations, got %v", dests)
	}
}

// Re-detection must never re-open a resolved conflict (the human decision stands).
func TestConflictResolution_NotReopened(t *testing.T) {
	app := dockApp(t)
	coll := app.Store.AddCollectionKind("U2", ArchiveSourceless)
	app.Store.UpsertUnionFiles(coll.ID, []unionFile{
		{RelPath: "x/report.pdf", Hash: "h1", Size: 10},
		{RelPath: "x/report.pdf", Hash: "h2", Size: 11},
	})
	if s := app.DetectConflicts(coll.ID); s.Open != 1 {
		t.Fatalf("want 1 open conflict, got %d", s.Open)
	}
	c := app.Store.ConflictViews(coll.ID, true)[0]
	if err := app.Store.ResolveConflict(c.ID, ResolveKeepBoth, 0); err != nil {
		t.Fatal(err)
	}
	// Re-running detection on the same (unchanged) union must not re-open it.
	if s := app.DetectConflicts(coll.ID); s.Open != 0 || s.New != 0 {
		t.Errorf("resolved conflict must not re-open: open=%d new=%d", s.Open, s.New)
	}
}
