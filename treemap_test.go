package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func tmApp(t *testing.T) *App {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return &App{DataDir: filepath.Dir(st.path), Store: st}
}

// childByName finds an emitted child by display name.
func childByName(res TreemapResult, name string) *TreemapNode {
	for i := range res.Children {
		if res.Children[i].Name == name {
			return &res.Children[i]
		}
	}
	return nil
}

func TestTreemapLevelsAndSizes(t *testing.T) {
	a := tmApp(t)
	coll := a.Store.AddCollection("Photos")
	fo := a.Store.AddFolder(coll.ID, "/src")
	add := func(rel string, size int64) {
		a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: rel, SizeBytes: size, HashAlg: "SHA256", Hash: rel})
	}
	add("trip/a.nef", 100)
	add("trip/b.nef", 50)
	add("trip/raw/c.nef", 25)
	add("docs/readme.txt", 5)

	// Archive root → the scanned folder is the sole child, carrying all bytes.
	root := a.Store.Treemap(coll.ID, "", "")
	if root.Size != 180 || root.Files != 4 {
		t.Fatalf("root totals wrong: %d bytes / %d files", root.Size, root.Files)
	}
	if len(root.Crumbs) != 1 || root.Crumbs[0].Path != "" {
		t.Fatalf("root crumb should be just the archive, got %+v", root.Crumbs)
	}
	src := childByName(root, "src")
	if src == nil || src.Size != 180 || !src.IsDir || !src.HasChildren {
		t.Fatalf("root child 'src' wrong: %+v", src)
	}

	// Zoom into /src → trip (dir, 175) and docs (dir, 5).
	lvl := a.Store.Treemap(coll.ID, src.Path, "")
	trip := childByName(lvl, "trip")
	docs := childByName(lvl, "docs")
	if trip == nil || trip.Size != 175 || !trip.IsDir {
		t.Fatalf("trip wrong: %+v", trip)
	}
	if docs == nil || docs.Size != 5 {
		t.Fatalf("docs wrong: %+v", docs)
	}
	if len(lvl.Crumbs) != 2 {
		t.Fatalf("expected 2 crumbs at /src, got %+v", lvl.Crumbs)
	}

	// Zoom into trip → files a.nef(100), b.nef(50) and the subdir raw(25).
	tl := a.Store.Treemap(coll.ID, trip.Path, "")
	af := childByName(tl, "a.nef")
	raw := childByName(tl, "raw")
	if af == nil || af.IsDir || af.Size != 100 {
		t.Fatalf("a.nef should be a 100-byte file leaf: %+v", af)
	}
	if raw == nil || !raw.IsDir || raw.Size != 25 || !raw.HasChildren {
		t.Fatalf("raw should be a 25-byte dir: %+v", raw)
	}
}

func TestTreemapWorstStatusRollupAndDrift(t *testing.T) {
	a := tmApp(t)
	coll := a.Store.AddCollection("D")
	fo := a.Store.AddFolder(coll.ID, "/src")
	add := func(rel string, size int64) {
		a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: rel, SizeBytes: size, HashAlg: "SHA256", Hash: rel})
	}
	add("trip/a.nef", 100) // MODIFIED
	add("trip/b.nef", 100) // UNCHANGED (not in report)
	add("docs/x.txt", 10)  // MISSING

	// No report yet → drift requested but unavailable, falls back to protection.
	pre := a.Store.Treemap(coll.ID, "", "drift")
	if pre.DriftAvailable || pre.ColorBy != "protection" {
		t.Fatalf("with no report, drift must be unavailable & fall back to protection: %+v", pre.ColorBy)
	}

	a.Store.ReplaceDriftReport(&DriftReport{At: time.Now().UTC(), CollectionID: coll.ID,
		Items: []DriftItem{
			{State: "MODIFIED", Path: "trip/a.nef"},
			{State: "MISSING", Path: "docs/x.txt"},
		}})

	res := a.Store.Treemap(coll.ID, "/src", "drift")
	if !res.DriftAvailable || res.ColorBy != "drift" {
		t.Fatalf("drift should now be available and active: %+v", res)
	}
	trip := childByName(res, "trip")
	docs := childByName(res, "docs")
	// trip mixes MODIFIED + UNCHANGED → worst is MODIFIED.
	if trip == nil || trip.Status != "MODIFIED" {
		t.Fatalf("trip worst drift status should be MODIFIED, got %+v", trip)
	}
	// docs is MISSING.
	if docs == nil || docs.Status != "MISSING" {
		t.Fatalf("docs should be MISSING, got %+v", docs)
	}
	// Legend at /src level: MODIFIED 100 + UNCHANGED 100 + MISSING 10.
	if res.StatusBytes["MODIFIED"] != 100 || res.StatusBytes["UNCHANGED"] != 100 || res.StatusBytes["MISSING"] != 10 {
		t.Fatalf("level legend wrong: %+v", res.StatusBytes)
	}
}

func TestTreemapFoldsSmallChildren(t *testing.T) {
	a := tmApp(t)
	coll := a.Store.AddCollection("Big")
	fo := a.Store.AddFolder(coll.ID, "/src")
	// One dominant folder + many tiny sibling folders that must fold into "other".
	a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "big/whale.bin", SizeBytes: 10_000_000, HashAlg: "SHA256", Hash: "whale"})
	for i := 0; i < 500; i++ {
		rel := fmt.Sprintf("tiny%03d/f.txt", i)
		a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: rel, SizeBytes: 10, HashAlg: "SHA256", Hash: rel})
	}
	res := a.Store.Treemap(coll.ID, "/src", "")
	if len(res.Children) > treemapMaxChildren+1 {
		t.Fatalf("level should be folded to <= %d + other, got %d", treemapMaxChildren, len(res.Children))
	}
	var other *TreemapNode
	for i := range res.Children {
		if res.Children[i].Other {
			other = &res.Children[i]
		}
	}
	if other == nil {
		t.Fatal("expected a folded 'other' block")
	}
	if other.HasChildren {
		t.Error("'other' block must not be zoomable")
	}
	if res.Folded == 0 {
		t.Error("Folded count should report how many were collapsed")
	}
	// Sanity: total bytes preserved across the fold (big + 500*10).
	if res.Size != 10_000_000+500*10 {
		t.Fatalf("level total wrong after folding: %d", res.Size)
	}
}
