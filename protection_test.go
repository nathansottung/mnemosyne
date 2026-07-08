package main

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// buildPhotographyArchive sets up the canonical example: an Archive
// "Photography" on 3-2-1 Standard (the auto-assigned default), a subfolder
// "To-Delete-2020" explicitly on Pre-Deletion Hold, and one package holding a
// file in each, copied to three verified volumes across two media kinds with one
// offsite. Returns the store, collection id, the offsite volume, and the two
// folder paths.
func buildPhotographyArchive(t *testing.T) (*Store, int, *Volume, string, string) {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	coll := st.AddCollection("Photography")
	root := "/srv/photography"
	fo := st.AddFolder(coll.ID, root)

	shootDir := filepath.ToSlash(filepath.Join(root, "shoot1"))
	delDir := filepath.ToSlash(filepath.Join(root, "To-Delete-2020"))
	st.SetAssignment(coll.ID, delDir, "pre-deletion-hold")

	f1 := st.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "shoot1/img1.nef", SizeBytes: 100})
	f2 := st.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "To-Delete-2020/old.nef", SizeBytes: 100})

	volA := st.AddVolume(Volume{Label: "ARCH-A", Kind: "HDD"})               // onsite HDD
	volB := st.AddVolume(Volume{Label: "TAPE-B", Kind: "TAPE"})              // onsite TAPE
	volC := st.AddVolume(Volume{Label: "OFF-C", Kind: "HDD", Offsite: true}) // offsite HDD

	ch := st.AddChunk(Chunk{CollectionID: coll.ID, Name: "PHOTO-C0001", Status: "WRITTEN",
		Files: []ChunkFileRef{{FileID: f1.ID}, {FileID: f2.ID}}})
	st.RecordCopy(ch, volA.ID, "A:/PHOTO-C0001", true)
	st.RecordCopy(ch, volB.ID, "B:/PHOTO-C0001", true)
	st.RecordCopy(ch, volC.ID, "C:/PHOTO-C0001", true)

	return st, coll.ID, volC, shootDir, delDir
}

func nodeByPath(res ProtectionResult, p string) *ProtectionNode {
	for _, n := range res.Nodes {
		if normPath(n.Path) == normPath(p) {
			return n
		}
	}
	return nil
}

func TestCanonicalPhotographyExample(t *testing.T) {
	st, cid, volC, shootDir, delDir := buildPhotographyArchive(t)

	res := st.Protection(cid)
	if res.ArchiveProfile == nil || res.ArchiveProfile.ID != DefaultProfileID {
		t.Fatalf("archive should default to 3-2-1 Standard, got %+v", res.ArchiveProfile)
	}
	// Photography file (shoot1): 3 copies / 2 kinds / 1 offsite against 3-2-1 → COMPLETE.
	if n := nodeByPath(res, shootDir); n == nil || n.Status != StatusComplete {
		t.Fatalf("shoot1 should be COMPLETE, got %v", n)
	}
	// To-Delete-2020 file against Pre-Deletion Hold (needs 4 copies) → PARTIAL 3/4.
	del := nodeByPath(res, delDir)
	if del == nil || del.Status != StatusPartial {
		t.Fatalf("To-Delete-2020 should be PARTIAL, got %v", del)
	}
	if del.ProfileID != "pre-deletion-hold" {
		t.Fatalf("To-Delete-2020 should resolve Pre-Deletion Hold, got %q", del.ProfileID)
	}
	if want := "3/4 copies"; !strings.Contains(del.Detail, want) {
		t.Fatalf("To-Delete-2020 detail should mention %q, got %q", want, del.Detail)
	}
	if res.Summary[StatusComplete] != 1 || res.Summary[StatusPartial] != 1 {
		t.Fatalf("expected 1 COMPLETE + 1 PARTIAL, got %v", res.Summary)
	}

	// Flip the offsite volume to onsite and recompute: both files lose their one
	// offsite copy; the Photography file drops to PARTIAL "0/1 offsite".
	volC.Offsite = false
	st.UpdateVolume(volC)
	res = st.Protection(cid)
	n := nodeByPath(res, shootDir)
	if n == nil || n.Status != StatusPartial {
		t.Fatalf("after offsite→onsite, shoot1 should be PARTIAL, got %v", n)
	}
	if want := "0/1 offsite"; !strings.Contains(n.Detail, want) {
		t.Fatalf("shoot1 detail should mention %q, got %q", want, n.Detail)
	}
	if res.Summary[StatusPartial] != 2 {
		t.Fatalf("both files should now be PARTIAL, got %v", res.Summary)
	}
}

func TestProfileResolutionNearestAncestor(t *testing.T) {
	st, cid, _, _, delDir := buildPhotographyArchive(t)
	// The To-Delete node's own explicit assignment wins over the archive default.
	if p := st.resolveProfileLocked(cid, path.Join(delDir, "deep/nested/x.nef")); p == nil || p.ID != "pre-deletion-hold" {
		t.Fatalf("nested file under To-Delete should inherit Pre-Deletion Hold, got %v", p)
	}
	// A sibling outside To-Delete falls back to the archive-level 3-2-1 Standard.
	if p := st.resolveProfileLocked(cid, "/srv/photography/other/y.nef"); p == nil || p.ID != DefaultProfileID {
		t.Fatalf("file outside To-Delete should resolve archive default, got %v", p)
	}
}

func TestBuiltinProfilesImmutableAndInUseGuard(t *testing.T) {
	st, cid, _, _, _ := buildPhotographyArchive(t)
	if err := st.UpdateProfile(Profile{ID: DefaultProfileID, Name: "hacked"}); err == nil {
		t.Fatal("editing a built-in profile should be refused")
	}
	if err := st.DeleteProfile(DefaultProfileID); err == nil {
		t.Fatal("deleting a built-in profile should be refused")
	}
	// A custom profile in use cannot be deleted until reassigned.
	custom := st.AddProfile(Profile{Name: "Custom Two", RequiredCopies: 2, RequiredDistinctMediaKinds: 1, VerifyDueMonths: 12})
	st.SetAssignment(cid, "/srv/photography/sub", custom.ID)
	if err := st.DeleteProfile(custom.ID); err == nil {
		t.Fatal("deleting an in-use profile should be refused with its users")
	}
	st.SetAssignment(cid, "/srv/photography/sub", "") // clear
	if err := st.DeleteProfile(custom.ID); err != nil {
		t.Fatalf("deleting an unused custom profile should succeed, got %v", err)
	}
}
