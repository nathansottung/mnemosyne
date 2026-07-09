package main

// generalize_test.go — photography is one profile among peers. This proves the
// discipline-neutral role taxonomy and starter templates work end-to-end for a
// MUSIC project: stems classify as ORIGINALS, masters as DELIVERABLES, the .als
// project file as CRITICAL PROJECT-FILES, and the Musician starter template routes
// each to its own destination.

import (
	"testing"
	"time"
)

func TestMusicianProject_RolesCriticalAndRouting(t *testing.T) {
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{DataDir: dataDir, Store: store}
	reg := app.formatRegistry()

	// (1) A synthetic music project's file kinds classify into the neutral taxonomy,
	// and ONLY the project file is CRITICAL.
	cases := []struct {
		rel      string
		wantRole string
		wantCrit bool
	}{
		{"Blue Album/stems/kick.wav", RoleOriginals, false},
		{"Blue Album/stems/vocal.aiff", RoleOriginals, false},
		{"Blue Album/masters/song.flac", RoleDeliverables, false},
		{"Blue Album/masters/song.mp3", RoleDeliverables, false},
		{"Blue Album/project/song.als", RoleProject, true}, // the irreplaceable arrangement state
		{"Blue Album/tracklist.cue", RoleSidecars, false},
	}
	for _, c := range cases {
		role, crit := classifyRole(reg, c.rel)
		if role != c.wantRole || crit != c.wantCrit {
			t.Errorf("%s → role %q critical %v, want %q %v", c.rel, role, crit, c.wantRole, c.wantCrit)
		}
	}
	// roleCritical (registry-honoring) agrees the .als is critical; a stem is not.
	if !roleCritical(reg, RoleProject) {
		t.Error("PROJECT-FILES should be the CRITICAL role")
	}

	// (2) The Musician starter template is seeded, groups by "Project", and routes
	// stems / masters / project files to three distinct destinations.
	var musician *Template
	for _, tm := range app.Store.Templates() {
		if tm.Name == "Musician" {
			musician = tm
			break
		}
	}
	if musician == nil {
		t.Fatal("the Musician starter template should be seeded on a fresh catalog")
	}
	if musician.GroupNoun != "Project" {
		t.Errorf("Musician GroupNoun = %q, want Project", musician.GroupNoun)
	}

	ev := &Event{Name: "Blue Album", Year: 2024}
	shot := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	route := func(rel, role string) string {
		f := &File{RelPath: rel, Role: role, ShotAt: shot}
		dest, ok := routeForFile(musician, reg, f, ev)
		if !ok {
			t.Fatalf("Musician template did not route %s (role %s)", rel, role)
		}
		return dest
	}
	if got := route("kick.wav", RoleOriginals); got != "music/2024/Blue Album/stems/kick.wav" {
		t.Errorf("stem route = %q, want music/2024/Blue Album/stems/kick.wav", got)
	}
	if got := route("song.flac", RoleDeliverables); got != "music/2024/Blue Album/masters/song.flac" {
		t.Errorf("master route = %q, want music/2024/Blue Album/masters/song.flac", got)
	}
	if got := route("song.als", RoleProject); got != "music/2024/Blue Album/project/song.als" {
		t.Errorf("project-file route = %q, want music/2024/Blue Album/project/song.als", got)
	}

	// (3) The SAME roles land on catalog files when the project is ingested through
	// the real scan pipeline (extension → role, ffprobe absent = empty dates, no error).
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"stems/kick.wav":    "KICK",
		"stems/vocal.aiff":  "VOX",
		"masters/song.flac": "FLACDATA",
		"masters/song.mp3":  "MP3DATA",
		"project/song.als":  "ABLETON",
	})
	coll := app.Store.AddCollection("Blue Album")
	if _, err := app.ScanFolder(coll.ID, src, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	byRel := map[string]*File{}
	for _, f := range app.Store.FilesOf(coll.ID) {
		byRel[f.RelPath] = f
	}
	if f := byRel["stems/kick.wav"]; f == nil || f.Role != RoleOriginals {
		t.Errorf("ingested kick.wav = %+v, want role ORIGINALS", f)
	}
	if f := byRel["masters/song.flac"]; f == nil || f.Role != RoleDeliverables {
		t.Errorf("ingested song.flac = %+v, want role DELIVERABLES", f)
	}
	if f := byRel["project/song.als"]; f == nil || f.Role != RoleProject {
		t.Errorf("ingested song.als = %+v, want role PROJECT-FILES", f)
	}
}
