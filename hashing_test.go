package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHashFileBothMatchesSHA256 proves the dual hasher returns the SAME SHA-256 a
// stranger would compute with sha256sum, plus a non-empty, distinct BLAKE3.
func TestHashFileBothMatchesSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	data := []byte("the quick brown fox\n")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, b3, err := hashFileBoth(p)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if sha != hex.EncodeToString(want[:]) {
		t.Fatalf("sha256 mismatch: got %s want %s", sha, hex.EncodeToString(want[:]))
	}
	if len(b3) != 64 || b3 == sha {
		t.Fatalf("blake3 should be a distinct 64-hex string, got %q", b3)
	}
}

// TestScanRecordsBlake3 proves a scan stores BLAKE3 alongside SHA-256 in the
// catalog (computed in the same pass).
func TestScanRecordsBlake3(t *testing.T) {
	a := versApp(t) // helper from versions_test.go: App on a temp store
	coll := a.Store.AddCollection("H")
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ScanFolder(coll.ID, src, func(float64, string) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	files := a.Store.FilesOf(coll.ID)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if len(f.Hash) != 64 {
		t.Fatalf("SHA-256 should be present, got %q", f.Hash)
	}
	if len(f.Blake3) != 64 {
		t.Fatalf("BLAKE3 should be recorded in the same scan pass, got %q", f.Blake3)
	}
	if f.Blake3 == f.Hash {
		t.Fatal("BLAKE3 and SHA-256 must differ")
	}
}

// TestBlake3NeverOnMedia is the guard for the architectural rule: no on-medium
// artifact may carry BLAKE3. A written package manifest (the struct that reaches
// a disc/tape) must contain SHA-256 but never the catalog's BLAKE3.
func TestBlake3NeverOnMedia(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("M")
	fo := a.Store.AddFolder(coll.ID, "/src")
	f := a.Store.UpsertFile(File{CollectionID: coll.ID, FolderID: fo.ID, RelPath: "x.dat",
		SizeBytes: 3, HashAlg: "SHA256", Hash: "aa11", Blake3: "beefb3beef"})
	// A package referencing that file, as it would be written to media.
	c := &Chunk{Name: "PKG-1", CollectionID: coll.ID, HashAlg: "SHA256", TarHash: "t", EncHash: "e",
		Files: []ChunkFileRef{{FileID: f.ID, RelPath: "x.dat", SizeBytes: 3, Hash: "aa11"}}}
	dir := t.TempDir()
	if err := writeManifest(dir, c, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "PKG-1.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(b))
	if strings.Contains(body, "blake3") || strings.Contains(body, "beefb3") {
		t.Fatalf("BLAKE3 must NEVER appear in an on-medium manifest:\n%s", b)
	}
	if !strings.Contains(body, "aa11") {
		t.Fatal("the on-media SHA-256 should be present in the manifest")
	}
}
