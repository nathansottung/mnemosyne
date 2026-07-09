package main

// events_test.go — the Templates + Events deliverables end-to-end:
//   1. infer a template + harvest events from a synthetic ORGANIZED tree;
//   2. cluster a synthetic CHAOTIC drive (adopted into a sourceless archive) into
//      capture-date-burst events, exercising real EXIF flow into archive files;
//   3. prove a harvested Event acts as a MAGNET: an EXIF-dated stray whose date
//      falls in the event's range is suggested into it.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeExifJPEG writes a unique EXIF JPEG (unique serial ⇒ unique bytes/hash) with
// the given capture time to root/rel.
func writeExifJPEG(t *testing.T, root, rel string, shot time.Time, serial string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	dt := shot.Format("2006:01:02 15:04:05")
	if err := os.WriteFile(p, buildExifJPEG(dt, serial), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInferStructure_OrganizedTree(t *testing.T) {
	app := dockApp(t)
	root := t.TempDir()
	// A human-sorted photos root: {year}/{event_type}/{event}/frames.jpg
	base := time.Date(2019, 6, 15, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		writeExifJPEG(t, root, fmt.Sprintf("2019/wedding/Smith Wedding/DSC%03d.jpg", i),
			base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("HEN-%d", i))
	}
	sm := time.Date(2019, 8, 2, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		writeExifJPEG(t, root, fmt.Sprintf("2019/portrait/Smith Family/IMG%03d.jpg", i),
			sm.Add(time.Duration(i)*time.Minute), fmt.Sprintf("SMI-%d", i))
	}

	inf, err := app.InferStructure(root)
	if err != nil {
		t.Fatalf("infer: %v", err)
	}
	if inf.Pattern != "{year}/{event_type}/{event}/" {
		t.Errorf("detected pattern = %q, want {year}/{event_type}/{event}/", inf.Pattern)
	}
	if len(inf.Events) != 2 {
		t.Fatalf("want 2 harvested events, got %d: %+v", len(inf.Events), inf.Events)
	}
	byName := map[string]ProposedEvent{}
	for _, e := range inf.Events {
		byName[e.Name] = e
	}
	hen, ok := byName["Smith Wedding"]
	if !ok {
		t.Fatalf("Smith Wedding not harvested; got %v", byName)
	}
	if hen.EventType != "wedding" {
		t.Errorf("Smith type = %q, want wedding", hen.EventType)
	}
	if hen.Year != 2019 {
		t.Errorf("Smith year = %d, want 2019", hen.Year)
	}
	if hen.CaptureStart.IsZero() || hen.CaptureStart.Year() != 2019 || hen.CaptureStart.Month() != 6 {
		t.Errorf("Smith capture range not harvested from EXIF: %v", hen.CaptureStart)
	}
	if smf := byName["Smith Family"]; smf.EventType != "portrait" {
		t.Errorf("Smith Family type = %q, want portrait", smf.EventType)
	}

	// The proposed template routes RAW/exports/video off the detected pattern.
	tmpl := app.ProposeTemplateFromInference(inf, "NAS Photos")
	if tmpl.Routes[RoleOriginals] != "{year}/{event_type}/{event}/" {
		t.Errorf("proposed RAW route = %q", tmpl.Routes[RoleOriginals])
	}
}

func TestClusterEvents_ChaoticDrive(t *testing.T) {
	app := dockApp(t)
	// A sourceless archive fed by adopting a chaotic drive of loose EXIF JPEGs.
	coll := app.Store.AddCollectionKind("Shoebox", ArchiveSourceless)
	vol := app.Store.AddVolume(Volume{Label: "CHAOS-1", Kind: "HDD", Serial: "CHAOS-SER"})

	drive := t.TempDir()
	// Burst A: a wedding weekend, 13 frames across ~a day.
	wed := time.Date(2019, 6, 15, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 13; i++ {
		writeExifJPEG(t, drive, fmt.Sprintf("Smith Wedding/DSC%03d.jpg", i),
			wed.Add(time.Duration(i)*90*time.Minute), fmt.Sprintf("W-%d", i))
	}
	// Burst B: a baptism, months later, 12 frames in one afternoon.
	bap := time.Date(2020, 1, 10, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		writeExifJPEG(t, drive, fmt.Sprintf("Baby Baptism/IMG%03d.jpg", i),
			bap.Add(time.Duration(i)*10*time.Minute), fmt.Sprintf("B-%d", i))
	}

	if _, err := app.AdoptFolder(drive, coll.ID, vol.ID, func(float64, string) {}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	// EXIF must have flowed onto the archive files during adoption.
	dated := 0
	for _, f := range app.Store.FilesOf(coll.ID) {
		if !f.ShotAt.IsZero() {
			dated++
		}
	}
	if dated != 25 {
		t.Fatalf("expected 25 dated files after adopt, got %d", dated)
	}

	props := app.ClusterEvents(coll.ID, 12, 3)
	if len(props) != 2 {
		t.Fatalf("want 2 clustered events (wedding, baptism), got %d: %+v", len(props), props)
	}
	byType := map[string]ProposedEvent{}
	for _, p := range props {
		byType[p.EventType] = p
	}
	if w, ok := byType["wedding"]; !ok || w.FileCount != 13 || w.Name != "Smith Wedding" {
		t.Errorf("wedding burst wrong: %+v", byType["wedding"])
	}
	if b, ok := byType["baptism"]; !ok || b.FileCount != 12 || b.Name != "Baby Baptism" {
		t.Errorf("baptism burst wrong: %+v", byType["baptism"])
	}
}

func TestMagnet_SuggestsStrayIntoHarvestedEvent(t *testing.T) {
	app := dockApp(t)
	coll := app.Store.AddCollectionKind("Union", ArchiveSourceless)

	// A harvested event: the Smith wedding weekend, 2019-06-15..16 (no members).
	ev := app.Store.AddEvent(&Event{
		Name: "Smith Wedding", EventType: "wedding", Year: 2019,
		CaptureStart: time.Date(2019, 6, 15, 0, 0, 0, 0, time.UTC),
		CaptureEnd:   time.Date(2019, 6, 16, 23, 59, 59, 0, time.UTC),
		CollectionID: coll.ID, Source: "HARVESTED",
	})

	// Two cataloged strays: one shot inside the range (should be magneted), one well
	// outside it (should not).
	inRange := time.Date(2019, 6, 15, 15, 30, 0, 0, time.UTC)
	outRange := time.Date(2021, 3, 1, 9, 0, 0, 0, time.UTC)
	app.Store.UpsertUnionFiles(coll.ID, []unionFile{
		{RelPath: "loose/from_camera/DSC900.jpg", Hash: "hash-in", Size: 111, Role: RoleDeliverables, ShotAt: inRange},
		{RelPath: "loose/other/DSC001.jpg", Hash: "hash-out", Size: 222, Role: RoleDeliverables, ShotAt: outRange},
	})

	groups := app.SuggestForEvent(ev.ID)
	if len(groups) != 1 {
		t.Fatalf("want 1 suggestion group (the in-range folder), got %d: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.FileCount != 1 || g.FolderHint != "loose/from_camera" {
		t.Errorf("wrong suggestion group: %+v", g)
	}

	// Accepting the suggestion sets membership; the stray now belongs to the event.
	n := app.Store.AssignFilesToEvent(g.FileIDs, ev.ID)
	if n != 1 {
		t.Fatalf("assign should move 1 file, got %d", n)
	}
	members := app.Store.FilesOfEvent(ev.ID)
	if len(members) != 1 || members[0].RelPath != "loose/from_camera/DSC900.jpg" {
		t.Errorf("event membership wrong after accept: %+v", members)
	}
	// The out-of-range stray stays unassigned and is no longer suggested.
	if again := app.SuggestForEvent(ev.ID); len(again) != 0 {
		t.Errorf("no more suggestions expected after accepting the only in-range group, got %+v", again)
	}
}
