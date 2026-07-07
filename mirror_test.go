package main

// mirror_test.go — native mirror backup: copy an archive's files to a volume as
// plain files (copy-then-verify, .mnemo_tmp → atomic rename), recorded as verified
// file-level copies that coverage counts, with the source tree preserved and the
// source never written to. Also: concurrent multi-volume mirroring and idempotent
// re-mirror. No external tools needed.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// mirrorChunksFor returns the archive's mirror chunks that have a current copy on
// the given volume.
func mirrorChunksFor(app *App, collID, volID int) []*Chunk {
	var out []*Chunk
	for _, c := range app.Store.Chunks(collID) {
		if !c.Mirror {
			continue
		}
		for _, cp := range c.Copies {
			if cp.VolumeID == volID && !cp.Superseded {
				out = append(out, c)
			}
		}
	}
	return out
}

func noProgMirror(float64, string) {}

func TestMirror_CopyVerifyTreeAndCoverage(t *testing.T) {
	app := dockApp(t)
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"a.txt":          "alpha\n",
		"sub/b.txt":      "bravo in a subfolder\n",
		"sub/deep/c.bin": "charlie deep binary payload\n",
	})
	coll := app.Store.AddCollection("Photos")
	if n, err := app.ScanFolder(coll.ID, src, noProgMirror); err != nil || n != 3 {
		t.Fatalf("scan: n=%d err=%v", n, err)
	}
	vol := app.Store.AddVolume(Volume{Label: "MIR-A", Kind: "HDD"})
	dest := t.TempDir()

	res, err := app.MirrorToVolume(coll.ID, nil, dest, vol.ID, 0, noProgMirror)
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if res.Mirrored != 3 || res.Failed != 0 || res.Skipped != 0 {
		t.Fatalf("result: mirrored=%d failed=%d skipped=%d, want 3/0/0", res.Mirrored, res.Failed, res.Skipped)
	}

	// Plain files, source tree preserved, byte-identical to source.
	for _, rel := range []string{"a.txt", "sub/b.txt", "sub/deep/c.bin"} {
		sp := filepath.Join(src, filepath.FromSlash(rel))
		dp := filepath.Join(dest, filepath.FromSlash(rel))
		want, _ := hashFileHex(sp)
		got, err := hashFileHex(dp)
		if err != nil {
			t.Errorf("mirrored file missing: %s (%v)", rel, err)
			continue
		}
		if want != got {
			t.Errorf("mirror %s hash mismatch", rel)
		}
	}
	// No staging temp files survive an atomic-rename mirror.
	_ = filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == mirrorTmpSuffix {
			t.Errorf("leftover temp file: %s", p)
		}
		if err == nil && !d.IsDir() && len(p) > len(mirrorTmpSuffix) && p[len(p)-len(mirrorTmpSuffix):] == mirrorTmpSuffix {
			t.Errorf("leftover .mnemo_tmp: %s", p)
		}
		return nil
	})

	// Recorded as a verified file-level copy on the volume (same record adoption
	// produces), so coverage counts it.
	mc := mirrorChunksFor(app, coll.ID, vol.ID)
	if len(mc) != 1 {
		t.Fatalf("expected exactly 1 mirror chunk on the volume, got %d", len(mc))
	}
	if mc[0].Status != StatusAdoptedVerified || mc[0].VerifiedCopyCount() != 1 || mc[0].FileCount != 3 {
		t.Errorf("mirror chunk: status=%s verified=%d files=%d", mc[0].Status, mc[0].VerifiedCopyCount(), mc[0].FileCount)
	}
	if cov := res.Coverage; cov.CoveredFiles != 3 || cov.Pct != 100 {
		t.Errorf("coverage: %d/%d (%.0f%%), want 3/3 100%%", cov.CoveredFiles, cov.TotalFiles, cov.Pct)
	}

	// Inventory sidecar on the volume, and the source is untouched.
	if _, err := os.Stat(filepath.Join(dest, dockSidecarDir, "INVENTORY.md")); err != nil {
		t.Errorf("expected inventory sidecar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, dockSidecarDir, "catalog_snapshot.json")); err != nil {
		t.Errorf("expected catalog snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, dockSidecarDir)); err == nil {
		t.Error("source must NEVER be written to — found a sidecar there")
	}
	if b, err := os.ReadFile(filepath.Join(src, "a.txt")); err != nil || string(b) != "alpha\n" {
		t.Errorf("source file must be pristine, got %q err=%v", b, err)
	}
}

func TestMirror_RefusesSourceDest(t *testing.T) {
	app := dockApp(t)
	src := t.TempDir()
	writeTree(t, src, map[string]string{"x.txt": "hi\n"})
	coll := app.Store.AddCollection("A")
	if _, err := app.ScanFolder(coll.ID, src, noProgMirror); err != nil {
		t.Fatal(err)
	}
	vol := app.Store.AddVolume(Volume{Label: "V", Kind: "HDD"})
	// Mirroring back into the source root must be refused up front.
	_, err := app.MirrorToVolume(coll.ID, nil, filepath.Join(src, "mirror"), vol.ID, 0, noProgMirror)
	if err == nil {
		t.Fatal("expected refusal to mirror into a source root")
	}
}

func TestMirror_ConcurrentMultiVolume(t *testing.T) {
	app := dockApp(t)
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.txt": "a\n", "b.txt": "bb\n", "c.txt": "ccc\n"})
	coll := app.Store.AddCollection("Multi")
	if _, err := app.ScanFolder(coll.ID, src, noProgMirror); err != nil {
		t.Fatal(err)
	}
	volA := app.Store.AddVolume(Volume{Label: "MV-A", Kind: "HDD", Location: "office"})
	volB := app.Store.AddVolume(Volume{Label: "MV-B", Kind: "HDD", Location: "off-site"})
	destA, destB := t.TempDir(), t.TempDir()

	// Two mirror jobs at once — one per volume (v1's simultaneous multi-volume copy).
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = app.MirrorToVolume(coll.ID, nil, destA, volA.ID, 0, noProgMirror)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = app.MirrorToVolume(coll.ID, nil, destB, volB.ID, 0, noProgMirror)
	}()
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent mirror %d: %v", i, e)
		}
	}
	// Both volumes hold a verified mirror; the archive now has two copies of each
	// file across two locations.
	if len(mirrorChunksFor(app, coll.ID, volA.ID)) != 1 || len(mirrorChunksFor(app, coll.ID, volB.ID)) != 1 {
		t.Errorf("expected one mirror chunk per volume")
	}
	for _, rel := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(destA, rel)); err != nil {
			t.Errorf("A missing %s", rel)
		}
		if _, err := os.Stat(filepath.Join(destB, rel)); err != nil {
			t.Errorf("B missing %s", rel)
		}
	}
	if cov := app.archiveCoverage([]int{coll.ID}); cov.Pct != 100 {
		t.Errorf("coverage should be 100%% with mirrors, got %.0f%%", cov.Pct)
	}
}

func TestMirror_IdempotentAndMultiFolderTree(t *testing.T) {
	app := dockApp(t)
	// Two source folders scanned into one archive.
	src1 := t.TempDir()
	src2 := t.TempDir()
	writeTree(t, src1, map[string]string{"one.txt": "1\n"})
	writeTree(t, src2, map[string]string{"two.txt": "2\n"})
	coll := app.Store.AddCollection("TwoRoots")
	if _, err := app.ScanFolder(coll.ID, src1, noProgMirror); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ScanFolder(coll.ID, src2, noProgMirror); err != nil {
		t.Fatal(err)
	}
	vol := app.Store.AddVolume(Volume{Label: "MF", Kind: "HDD"})
	dest := t.TempDir()

	res, err := app.MirrorToVolume(coll.ID, nil, dest, vol.ID, 0, noProgMirror)
	if err != nil || res.Mirrored != 2 {
		t.Fatalf("mirror multi-folder: mirrored=%d err=%v", res.Mirrored, err)
	}
	// With multiple source roots each folder gets its own subtree (no collision).
	base1 := safeName(filepath.Base(src1))
	base2 := safeName(filepath.Base(src2))
	if _, err := os.Stat(filepath.Join(dest, base1, "one.txt")); err != nil {
		t.Errorf("expected %s/one.txt: %v", base1, err)
	}
	if _, err := os.Stat(filepath.Join(dest, base2, "two.txt")); err != nil {
		t.Errorf("expected %s/two.txt: %v", base2, err)
	}

	// Re-mirror refreshes the SAME chunk, not a duplicate.
	before := len(mirrorChunksFor(app, coll.ID, vol.ID))
	if _, err := app.MirrorToVolume(coll.ID, nil, dest, vol.ID, 500, noProgMirror); err != nil {
		t.Fatalf("re-mirror: %v", err)
	}
	after := len(mirrorChunksFor(app, coll.ID, vol.ID))
	if before != 1 || after != 1 {
		t.Errorf("re-mirror must be idempotent: before=%d after=%d", before, after)
	}
}
