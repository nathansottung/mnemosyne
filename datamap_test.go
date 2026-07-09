package main

// datamap_test.go — the "where your data lives" honesty surface must tell the truth:
// name every place the tool writes with the real, live path; list the two things it
// never writes to; carry the enforcement sentence; and point at tests that actually
// exist. Since this screen is a PROMISE to the user, the test also confirms the
// referenced guard tests are real functions in the tree — a broken pointer here would
// be dishonest, not just a typo.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func findLoc(list []DataLocation, name string) *DataLocation {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func TestDataMap_WritesAndNever(t *testing.T) {
	app := newSetupApp(t)
	// A registered source root + a staging + two keystores = a realistic config.
	coll := app.Store.AddCollectionKind("Work", ArchiveSourced)
	srcRoot := t.TempDir()
	app.Store.AddFolder(coll.ID, srcRoot)
	ks1, ks2 := t.TempDir(), t.TempDir()
	if _, err := app.SaveConfig(map[string]any{
		"staging_dir": t.TempDir(), "keystore_paths": []any{ks1, ks2},
	}); err != nil {
		t.Fatal(err)
	}

	dm := app.DataMap()

	// Every documented write category is present.
	for _, name := range []string{"The catalog", "Settings", "Keystores", "Staging",
		"Destinations you choose", "Inventory & seal sidecars", "Quarantine folders"} {
		if findLoc(dm.Writes, name) == nil {
			t.Errorf("writes list missing %q", name)
		}
	}
	// The catalog row carries the REAL path and mentions the daily backups.
	cat := findLoc(dm.Writes, "The catalog")
	if cat.Path != filepath.Join(app.DataDir, "catalog.json") {
		t.Errorf("catalog path = %q, want %q", cat.Path, filepath.Join(app.DataDir, "catalog.json"))
	}
	if !strings.Contains(strings.ToLower(cat.Note), "daily backup") {
		t.Errorf("catalog note should mention daily backups: %q", cat.Note)
	}
	// Config path is the real one.
	if cfgRow := findLoc(dm.Writes, "Settings"); cfgRow.Path != app.configPath() {
		t.Errorf("config path = %q, want %q", cfgRow.Path, app.configPath())
	}
	// Keystores reflect config and are not flagged missing (we have two).
	ksRow := findLoc(dm.Writes, "Keystores")
	if len(ksRow.Paths) != 2 || ksRow.Missing {
		t.Errorf("keystores row = %+v, want 2 paths, not missing", ksRow)
	}
	// The sidecar row states the exact promise, verbatim in spirit.
	sc := findLoc(dm.Writes, "Inventory & seal sidecars")
	for _, must := range []string{"never to drives you adopt", "never to your source folders"} {
		if !strings.Contains(sc.What, must) {
			t.Errorf("sidecar row must say %q; got %q", must, sc.What)
		}
	}
	if !strings.Contains(findLoc(dm.Writes, "Quarantine folders").What, QuarantineDir) {
		t.Error("quarantine row should name the _deleted folder")
	}

	// The inverse list names exactly the two never-written targets, with the source
	// root reflected.
	src := findLoc(dm.Never, "Your source folders")
	if src == nil || len(src.Paths) != 1 || src.Paths[0] != srcRoot {
		t.Errorf("never→sources = %+v, want the one registered root %q", src, srcRoot)
	}
	if findLoc(dm.Never, "Drives you adopt") == nil {
		t.Error("never list must include adopted drives")
	}
	if strings.TrimSpace(dm.Enforcement) == "" {
		t.Error("enforcement sentence must be present")
	}
	if !strings.Contains(strings.ToLower(dm.Enforcement), "refus") {
		t.Errorf("enforcement should describe the refusal: %q", dm.Enforcement)
	}
}

func TestDataMap_MissingFlags(t *testing.T) {
	app := newSetupApp(t)
	// Fresh: no staging, no keystores → both flagged missing with a warning.
	dm := app.DataMap()
	if st := findLoc(dm.Writes, "Staging"); !st.Missing || st.Warn == "" {
		t.Errorf("empty staging should be flagged missing with a warning: %+v", st)
	}
	if ks := findLoc(dm.Writes, "Keystores"); !ks.Missing || ks.Warn == "" {
		t.Errorf("no keystores should be flagged missing (needs %d): %+v", MinKeystores, ks)
	}
}

// The "verify this claim" pointers must reference tests that actually exist — a dead
// pointer on an honesty screen is worse than none.
func TestDataMap_VerifyPointersAreReal(t *testing.T) {
	app := newSetupApp(t)
	dm := app.DataMap()
	if len(dm.VerifyTests) == 0 || strings.TrimSpace(dm.VerifyNote) == "" {
		t.Fatal("verify note + tests must be present")
	}
	// Map each referenced symbol → the file it claims to live in, and confirm it does.
	refs := map[string]string{
		"TestIntegration_SourceSafetyRefusals":               "integration_test.go",
		"TestMirror_RefusesSourceDest":                       "mirror_test.go",
		"TestQuarantine_AbsentOnAdoptedAndRefusedForSources": "quarantine_test.go",
		"AssertOutsideSources":                               "store.go",
	}
	joined := strings.Join(dm.VerifyTests, "\n")
	for sym, file := range refs {
		if !strings.Contains(joined, sym) {
			t.Errorf("verify_tests should reference %q", sym)
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("referenced file %q not readable: %v", file, err)
			continue
		}
		if !strings.Contains(string(b), sym) {
			t.Errorf("%q claims %q lives in %s, but it isn't there", "verify_tests", sym, file)
		}
	}
}

// The UI wires the screen in: route, entry-point links, and the per-job writes line.
func TestWhereScreenWiredInUI(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("ui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, need := range []string{
		"async function vWhere(", "where:vWhere", `href="#where"`, // route + at least one link
		"function jobWrites(", "const JOB_WRITES=", // jobs name what they write
		"/api/data-map",
	} {
		if !strings.Contains(html, need) {
			t.Errorf("UI missing %q", need)
		}
	}
	// The three required entry points each link to #where: Home footer, Settings,
	// first-run summary. Count the links — expect at least three.
	if n := strings.Count(html, `href="#where"`); n < 3 {
		t.Errorf("expected #where linked from Home, Settings, and setup summary (≥3 links), found %d", n)
	}
}
