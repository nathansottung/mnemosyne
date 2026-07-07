package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func versApp(t *testing.T) *App {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return &App{DataDir: filepath.Dir(st.path), Store: st}
}

// TestUpsertFileRetainsVersions proves a hash change appends the OLD version to
// history instead of discarding it, and that an unchanged rescan adds nothing.
func TestUpsertFileRetainsVersions(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("A")
	fo := a.Store.AddFolder(coll.ID, "/src")
	up := func(hash string, size int64) *File {
		return a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "x.txt",
			SizeBytes: size, HashAlg: "SHA256", Hash: hash, ModTime: time.Now().UTC()})
	}

	f := up("aaa", 10)
	if len(f.Versions) != 0 || f.FirstSeen.IsZero() {
		t.Fatalf("first insert: want 0 versions and a first_seen, got %d / %v", len(f.Versions), f.FirstSeen)
	}
	// Same content again → nothing retained.
	up("aaa", 10)
	if len(f.Versions) != 0 {
		t.Fatalf("unchanged rescan should retain nothing, got %d", len(f.Versions))
	}
	// Content changes → the OLD version is retained, current advances.
	up("bbb", 12)
	if len(f.Versions) != 1 || f.Versions[0].Hash != "aaa" || f.Versions[0].SupersededAt.IsZero() {
		t.Fatalf("after change: want 1 retained (aaa, superseded), got %+v", f.Versions)
	}
	if f.Hash != "bbb" || f.SizeBytes != 12 {
		t.Fatalf("current should now be bbb/12, got %s/%d", f.Hash, f.SizeBytes)
	}
	// A third distinct version → history [aaa, bbb], newest current ccc.
	up("ccc", 13)
	if len(f.Versions) != 2 || f.Versions[0].Hash != "aaa" || f.Versions[1].Hash != "bbb" {
		t.Fatalf("history should be [aaa,bbb], got %+v", f.Versions)
	}
}

// TestVersionsRetainedCap proves the cap forgets only the OLDEST pointers.
func TestVersionsRetainedCap(t *testing.T) {
	a := versApp(t)
	a.Store.SetVersionsRetained(1)
	coll := a.Store.AddCollection("A")
	fo := a.Store.AddFolder(coll.ID, "/src")
	up := func(hash string) *File {
		return a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "x", HashAlg: "SHA256", Hash: hash})
	}
	up("h1")
	up("h2")
	f := up("h3")
	if len(f.Versions) != 1 || f.Versions[0].Hash != "h2" {
		t.Fatalf("cap=1 should keep only the most recent superseded (h2), got %+v", f.Versions)
	}
	if f.Hash != "h3" {
		t.Fatalf("current should be h3, got %s", f.Hash)
	}
}

// TestFileVersionsLocate proves each retained version is located to the package
// and volume that still hold its content-addressed bytes.
func TestFileVersionsLocate(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("Photos")
	fo := a.Store.AddFolder(coll.ID, "/src")
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	f := a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "img.raw", SizeBytes: 100, HashAlg: "SHA256", Hash: "hashA", ModTime: base})
	// force a change to hashB
	a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "img.raw", SizeBytes: 120, HashAlg: "SHA256", Hash: "hashB", ModTime: base.AddDate(0, 1, 0)})

	vA := a.Store.AddVolume(Volume{Label: "LTO-0007", Kind: "TAPE", Location: "vault"})
	vB := a.Store.AddVolume(Volume{Label: "LTO-0008", Kind: "TAPE", Location: "vault"})
	tr := true
	a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: "NSP-C0003", Status: "VERIFIED",
		Files:  []ChunkFileRef{{FileID: f.ID, RelPath: "img.raw", Hash: "hashA"}},
		Copies: []Copy{{VolumeID: vA.ID, VerifyOK: &tr}}})
	a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: "NSP-C0009", Status: "VERIFIED",
		Files:  []ChunkFileRef{{FileID: f.ID, RelPath: "img.raw", Hash: "hashB"}},
		Copies: []Copy{{VolumeID: vB.ID, VerifyOK: &tr}}})

	vs := a.Store.FileVersions(f.ID)
	if len(vs) != 2 {
		t.Fatalf("want 2 versions, got %d", len(vs))
	}
	if vs[0].Version != "v1" || vs[0].Current || vs[0].Hash != "hashA" {
		t.Fatalf("v1 should be the superseded hashA, got %+v", vs[0])
	}
	if vs[1].Version != "v2" || !vs[1].Current || vs[1].Hash != "hashB" {
		t.Fatalf("v2 should be the current hashB, got %+v", vs[1])
	}
	if len(vs[0].Packages) != 1 || vs[0].Packages[0].Chunk != "NSP-C0003" || vs[0].Packages[0].Volumes[0].Label != "LTO-0007" {
		t.Fatalf("v1 should locate in NSP-C0003 on LTO-0007, got %+v", vs[0].Packages)
	}
	if len(vs[1].Packages) != 1 || vs[1].Packages[0].Chunk != "NSP-C0009" {
		t.Fatalf("v2 should locate in NSP-C0009, got %+v", vs[1].Packages)
	}
	// The one-line locator reads as documented.
	line := vs[0].LocatorLine()
	if want := "v1 · 2024-03-01 · in NSP-C0003 on tape LTO-0007"; line != want {
		t.Fatalf("locator line:\n got %q\nwant %q", line, want)
	}
	// The reusable prior-version helper finds the right version by hash.
	if got := a.priorVersionLocator(f.ID, "hashA"); got != line {
		t.Fatalf("priorVersionLocator(hashA) = %q, want %q", got, line)
	}
}

// TestReconcileModifiedShowsPriorVersion proves a MODIFIED drift row carries the
// retained prior version inline as its restore source.
func TestReconcileModifiedShowsPriorVersion(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("D")
	srcDir := t.TempDir()
	doc := filepath.Join(srcDir, "doc.txt")
	if err := os.WriteFile(doc, []byte("original content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noprog := func(float64, string) {}
	if _, err := a.ScanFolder(coll.ID, srcDir, noprog); err != nil {
		t.Fatalf("scan: %v", err)
	}
	files := a.Store.FilesOf(coll.ID)
	if len(files) != 1 {
		t.Fatalf("expected 1 scanned file, got %d", len(files))
	}
	f := files[0]
	v1hash := f.Hash

	// Make v1 "backed up": a verified package on a tape holding its bytes.
	vol := a.Store.AddVolume(Volume{Label: "LTO-0007", Kind: "TAPE", Location: "vault"})
	tr := true
	a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: "D-C0001", Status: "VERIFIED",
		Files:  []ChunkFileRef{{FileID: f.ID, RelPath: "doc.txt", Hash: v1hash}},
		Copies: []Copy{{VolumeID: vol.ID, VerifyOK: &tr}}})

	// Modify on disk, then reconcile.
	if err := os.WriteFile(doc, []byte("edited content — now different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := a.ReconcileCollection(coll.ID, noprog)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var mod *DriftItem
	for i := range rep.Items {
		if rep.Items[i].State == "MODIFIED" && rep.Items[i].Path == "doc.txt" {
			mod = &rep.Items[i]
		}
	}
	if mod == nil {
		t.Fatalf("expected a MODIFIED item for doc.txt, got %+v", rep.Items)
	}
	if mod.FileID != f.ID || mod.PriorHash != v1hash {
		t.Fatalf("MODIFIED should carry file_id/prior_hash, got %+v", mod)
	}
	if !strings.Contains(mod.PriorVersion, "v1") || !strings.Contains(mod.PriorVersion, "D-C0001") || !strings.Contains(mod.PriorVersion, "LTO-0007") {
		t.Fatalf("prior version locator should name v1 / package / tape, got %q", mod.PriorVersion)
	}
	// And the file now genuinely has a retained version.
	if len(a.Store.FileByID(f.ID).Versions) != 1 {
		t.Fatalf("reconcile should have retained the prior version")
	}
}

// TestSelectVersionAsOf proves the "as of <date>" selector picks the version
// current at that instant, and index/default work.
func TestSelectVersionAsOf(t *testing.T) {
	mk := func(idx int, current bool, first, sup string) FileVersionView {
		v := FileVersionView{Index: idx, Version: "v" + string(rune('0'+idx)), Current: current, Hash: "h" + string(rune('0'+idx))}
		if first != "" {
			tt, _ := time.Parse("2006-01-02", first)
			v.FirstSeen = &tt
		}
		if sup != "" {
			tt, _ := time.Parse("2006-01-02", sup)
			v.SupersededAt = &tt
		}
		return v
	}
	views := []FileVersionView{
		mk(1, false, "2024-01-01", "2024-06-01"),
		mk(2, true, "2024-06-01", ""),
	}
	as := func(date string) FileVersionView {
		tt, _ := time.Parse("2006-01-02", date)
		v, err := selectVersion(views, VersionSelector{AsOf: &tt})
		if err != nil {
			t.Fatalf("as of %s: %v", date, err)
		}
		return v
	}
	if as("2024-03-15").Hash != "h1" {
		t.Errorf("as of 2024-03-15 should be v1")
	}
	if as("2024-09-01").Hash != "h2" {
		t.Errorf("as of 2024-09-01 should be v2")
	}
	// Before the file existed → error.
	early, _ := time.Parse("2006-01-02", "2023-01-01")
	if _, err := selectVersion(views, VersionSelector{AsOf: &early}); err == nil {
		t.Error("as of a date before v1 existed should error")
	}
	// Default selector → newest.
	def, err := selectVersion(views, VersionSelector{})
	if err != nil || def.Hash != "h2" {
		t.Errorf("default should pick current v2, got %v (%v)", def.Hash, err)
	}
	// Index selector.
	byIdx, err := selectVersion(views, VersionSelector{Index: 1})
	if err != nil || byIdx.Hash != "h1" {
		t.Errorf("index 1 should pick v1, got %v (%v)", byIdx.Hash, err)
	}
}

// TestRetainedVersionsMD proves the Recovery Kit note appears only when a file
// has multiple versions and names them.
func TestRetainedVersionsMD(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("A")
	fo := a.Store.AddFolder(coll.ID, "/src")
	// Single-version file → no note.
	a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "solo.txt", HashAlg: "SHA256", Hash: "h1"})
	if md := a.retainedVersionsMD(); md != "" {
		t.Fatalf("no multi-version files → empty note, got %q", md)
	}
	// Two-version file → note names the path and both versions.
	a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "doc.txt", HashAlg: "SHA256", Hash: "a"})
	a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "doc.txt", HashAlg: "SHA256", Hash: "b"})
	md := a.retainedVersionsMD()
	if md == "" || !strings.Contains(md, "doc.txt") || !strings.Contains(md, "v1") || !strings.Contains(md, "v2") {
		t.Fatalf("note should list doc.txt with v1 and v2:\n%s", md)
	}
	if strings.Contains(md, "solo.txt") {
		t.Errorf("single-version files should not appear in the note")
	}
}
