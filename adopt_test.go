package main

// adopt_test.go — bringing pre-existing media into the catalog. Covers a
// hand-made tar+par2 folder (no manifest), the "deep adopt" TOC enumeration,
// idempotency, and — the key safety property — that adopting one of Mnemosyne's
// OWN written chunks (copied elsewhere) is detected as a duplicate, not
// re-cataloged.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// makeLegacyTar hand-builds a legacy archive folder (payload + par2, no manifest)
// under mountDir, the way `tar` + `par2` on the command line would.
func makeLegacyTar(t *testing.T, tools map[string]string, mountDir, name string, files map[string][]byte) {
	t.Helper()
	src := t.TempDir()
	for rel, data := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	payload := filepath.Join(mountDir, name+".tar")
	if err := run(tools["tar"], "", "--format=posix", "-cf", payload, "-C", src, "."); err != nil {
		t.Fatalf("hand-tar: %v", err)
	}
	if err := runPar2Create(tools["par2"], "", 5, payload+".par2", payload); err != nil {
		t.Fatalf("hand-par2: %v", err)
	}
}

func adoptedList(t *testing.T, res map[string]any, key string) []map[string]any {
	t.Helper()
	v, ok := res[key].([]map[string]any)
	if !ok {
		t.Fatalf("result[%q] not a list: %v", key, res[key])
	}
	return v
}

func TestIntegration_AdoptHandMadeTar(t *testing.T) {
	s := newIT(t)
	s.setConfig(nil)
	coll := s.app.Store.AddCollection("Legacy")
	vol := s.app.Store.AddVolume(Volume{Label: "OLD-HDD-1", Kind: "HDD", Location: "closet"})

	mount := t.TempDir()
	makeLegacyTar(t, s.tools, mount, "vacation-2009", map[string][]byte{
		"photos/a.jpg": []byte("not really a jpg, but bytes\n"),
		"notes.txt":    []byte("hand-made legacy archive\n"),
	})

	// Shallow adopt: cataloged, but contents are unenumerated (no manifest).
	res, err := s.app.AdoptMedia(mount, coll.ID, vol.ID, false, noProg)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := adoptedList(t, res, "adopted"); len(got) != 1 {
		t.Fatalf("expected 1 adopted, got %d (%v)", len(got), res)
	}

	var c *Chunk
	for _, cc := range s.app.Store.Chunks(0) {
		if cc.Name == "vacation-2009" {
			c = cc
		}
	}
	if c == nil {
		t.Fatal("adopted package not in catalog")
	}
	if c.Status != StatusAdoptedVerified || !c.Adopted {
		t.Errorf("expected ADOPTED-VERIFIED/adopted, got status=%s adopted=%v", c.Status, c.Adopted)
	}
	if c.Encrypted {
		t.Errorf(".tar payload should be inferred plaintext")
	}
	if !c.ListingUnknown || c.FileCount != 0 {
		t.Errorf("manifest-less adopt should be listing-unknown with 0 files, got unknown=%v files=%d", c.ListingUnknown, c.FileCount)
	}
	if c.VerifiedCopyCount() != 1 {
		t.Errorf("adopt should record one verified copy, got %d", c.VerifiedCopyCount())
	}

	// Idempotent: a second adopt of the same medium skips it as a duplicate.
	res2, err := s.app.AdoptMedia(mount, coll.ID, vol.ID, false, noProg)
	if err != nil {
		t.Fatalf("re-adopt: %v", err)
	}
	if got := adoptedList(t, res2, "adopted"); len(got) != 0 {
		t.Errorf("re-adopt should adopt nothing, got %d", len(got))
	}
	if got := adoptedList(t, res2, "skipped_duplicate"); len(got) != 1 {
		t.Errorf("re-adopt should skip 1 duplicate, got %d", len(got))
	}
}

func TestIntegration_DeepAdoptEnumeratesContents(t *testing.T) {
	s := newIT(t)
	s.setConfig(nil)
	coll := s.app.Store.AddCollection("Legacy")
	vol := s.app.Store.AddVolume(Volume{Label: "OLD-HDD-2", Kind: "HDD", Location: "closet"})

	mount := t.TempDir()
	makeLegacyTar(t, s.tools, mount, "docs-archive", map[string][]byte{
		"invoices/2010.pdf": []byte("pretend pdf bytes\n"),
		"readme.md":         []byte("# hand-made\n"),
	})

	// Deep adopt streams the tar TOC to populate the file list without extracting.
	res, err := s.app.AdoptMedia(mount, coll.ID, vol.ID, true, noProg)
	if err != nil {
		t.Fatalf("deep adopt: %v", err)
	}
	if got := adoptedList(t, res, "adopted"); len(got) != 1 {
		t.Fatalf("expected 1 adopted, got %d", len(got))
	}
	var c *Chunk
	for _, cc := range s.app.Store.Chunks(0) {
		if cc.Name == "docs-archive" {
			c = cc
		}
	}
	if c == nil {
		t.Fatal("adopted package missing")
	}
	if c.ListingUnknown || c.FileCount < 2 {
		t.Errorf("deep adopt should enumerate ≥2 files and be listing-known, got unknown=%v files=%d", c.ListingUnknown, c.FileCount)
	}
	// The TOC-derived listing has paths but no source hashes.
	rels := map[string]bool{}
	for _, f := range c.Files {
		rels[filepath.ToSlash(f.RelPath)] = true
		if f.Hash != "" {
			t.Errorf("TOC listing must not fabricate hashes: %s has %s", f.RelPath, f.Hash)
		}
	}
	found := false
	for r := range rels {
		if filepath.Base(r) == "readme.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected readme.md in TOC listing, got %v", rels)
	}
}

func TestIntegration_AdoptOwnChunkIsDuplicate(t *testing.T) {
	s := newIT(t)
	s.setConfig(nil)
	// Build + write one of Mnemosyne's own plaintext packages via the real API.
	src := s.makeSource(map[string][]byte{"work/report.txt": []byte("native package content\n")})
	c := s.scanPlanBuild(src, 1, false, 10)
	pid := int(c["id"].(float64))
	name := c["name"].(string)
	dest := t.TempDir()
	vid := s.addVolume("NATIVE-VOL", "office")
	s.job(s.obj("POST", "/api/chunks/"+strconv.Itoa(pid)+"/write", map[string]any{"dest_dir": dest, "volume_id": vid}))

	// Copy the written package folder to a "found elsewhere" mount and adopt it.
	elsewhere := t.TempDir()
	copyDirT(t, filepath.Join(dest, name), filepath.Join(elsewhere, name))

	coll := s.app.Store.AddCollection("Rediscovered")
	adoptVol := s.app.Store.AddVolume(Volume{Label: "FOUND-DISK", Kind: "HDD", Location: "attic"})
	res, err := s.app.AdoptMedia(elsewhere, coll.ID, adoptVol.ID, false, noProg)
	if err != nil {
		t.Fatalf("adopt own chunk: %v", err)
	}
	if got := adoptedList(t, res, "adopted"); len(got) != 0 {
		t.Errorf("adopting our own written chunk must NOT re-catalog it, got %d adopted", len(got))
	}
	dups := adoptedList(t, res, "skipped_duplicate")
	if len(dups) != 1 {
		t.Fatalf("expected 1 skipped-duplicate, got %d (%v)", len(dups), res)
	}
	if dups[0]["duplicate_of"] != name {
		t.Errorf("duplicate should point at the original %s, got %v", name, dups[0]["duplicate_of"])
	}
}

// ---- tiny test helpers -------------------------------------------------

func copyDirT(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		sp, dp := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDirT(t, sp, dp)
			continue
		}
		b, err := os.ReadFile(sp)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dp, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
