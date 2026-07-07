package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func censusApp(t *testing.T) *App {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return &App{DataDir: filepath.Dir(st.path), Store: st}
}

func TestFormatCensusTiers(t *testing.T) {
	app := censusApp(t)
	coll := app.Store.AddCollection("Photos")
	fo := app.Store.AddFolder(coll.ID, "/src")
	add := func(rel string, size int64) {
		app.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: rel, SizeBytes: size, HashAlg: "SHA256", Hash: "x"})
	}
	add("a.jpg", 100)
	add("b.jpg", 100)
	add("c.jpg", 100) // 300 OPEN
	add("d.nef", 100)
	add("e.nef", 100) // 200 DOCUMENTED
	add("f.lrcat", 100)
	add("g.xyz", 100)

	c := app.FormatCensus(coll.ID)
	if c.TotalFiles != 7 || c.TotalBytes != 700 {
		t.Fatalf("totals wrong: %d files, %d bytes", c.TotalFiles, c.TotalBytes)
	}
	// OPEN(300) + DOCUMENTED(200) = 500 safe of 700 → ~71.4%.
	if c.SafeBytes != 500 || c.SafePct < 71 || c.SafePct > 72 {
		t.Fatalf("safe wrong: %d bytes, %.1f%%", c.SafeBytes, c.SafePct)
	}
	// Rows sorted by bytes desc → .jpg (300) first.
	if c.Rows[0].Ext != ".jpg" {
		t.Fatalf("rows should sort by bytes desc, first is %q", c.Rows[0].Ext)
	}
	tier := map[string]CensusRow{}
	for _, r := range c.Rows {
		tier[r.Ext] = r
	}
	if tier[".nef"].Tier != TierDocumented || tier[".nef"].Migration == "" {
		t.Errorf(".nef should be DOCUMENTED with a migration, got %+v", tier[".nef"])
	}
	if !hasReader(tier[".nef"].Readers, "libraw") {
		t.Errorf(".nef readers should include libraw, got %v", tier[".nef"].Readers)
	}
	if tier[".lrcat"].Tier != TierAtRisk {
		t.Errorf(".lrcat should be AT-RISK, got %q", tier[".lrcat"].Tier)
	}
	if tier[".xyz"].Tier != TierUnknown {
		t.Errorf(".xyz should be UNKNOWN, got %q", tier[".xyz"].Tier)
	}
	// The media-carried reference names readers and stays non-alarmist.
	ref := readersReference(c)
	if !strings.Contains(ref, "libraw") || !strings.Contains(strings.ToLower(ref), "advisory") {
		t.Error("readers reference should name readers and state it is advisory")
	}
}

func TestFormatRegistryUserOverride(t *testing.T) {
	app := censusApp(t)
	// A user formats.json overrides an embedded entry and adds a new one.
	user := `{"_comment":"mine",".xyz":{"tier":"OPEN","rationale":"my open format"},".nef":{"tier":"AT-RISK","rationale":"I disagree"}}`
	if err := os.WriteFile(filepath.Join(app.DataDir, "formats.json"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := app.formatRegistry()
	if reg[".xyz"].Tier != TierOpen {
		t.Errorf("user override should add .xyz OPEN, got %q", reg[".xyz"].Tier)
	}
	if reg[".nef"].Tier != TierAtRisk {
		t.Errorf("user override should win for .nef, got %q", reg[".nef"].Tier)
	}
	// The embedded default still applies where the user is silent.
	if reg[".dng"].Tier != TierOpen {
		t.Errorf(".dng should stay OPEN from defaults, got %q", reg[".dng"].Tier)
	}
}

func hasReader(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
