package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBagItTags(t *testing.T) {
	dir := t.TempDir()
	c := &Chunk{Name: "PKG-1", MediaKind: "HDD", EncHash: "deadbeef", HashAlg: "SHA256",
		Files: []ChunkFileRef{
			{FileID: 1, RelPath: "trip/a.nef", SizeBytes: 100, Hash: "aa"},
			{FileID: 2, RelPath: "trip/b.nef", SizeBytes: 50, Hash: "bb"},
		}}
	writeBagItTags(dir, c)

	decl, err := os.ReadFile(filepath.Join(dir, "bagit.txt"))
	if err != nil || !strings.Contains(string(decl), "BagIt-Version: 1.0") {
		t.Fatalf("bagit.txt wrong: %v / %s", err, decl)
	}
	man, err := os.ReadFile(filepath.Join(dir, "manifest-sha256.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Manifest lists source files by their ORIGINAL tree paths (no data/ prefix — the
	// sidecar/in-tar manifest describes the tree the payload tar yields), with SHA-256.
	if !strings.Contains(string(man), "aa  trip/a.nef") || !strings.Contains(string(man), "bb  trip/b.nef") {
		t.Fatalf("manifest should list tree-relative <relpath> with sha256:\n%s", man)
	}
	if strings.Contains(string(man), "data/") {
		t.Errorf("the per-package manifest must NOT use a data/ prefix (that is export-only):\n%s", man)
	}
	info, _ := os.ReadFile(filepath.Join(dir, "bag-info.txt"))
	if !strings.Contains(string(info), "Payload-Oxum: 150.2") {
		t.Fatalf("Payload-Oxum should be totalbytes.count (150.2):\n%s", info)
	}
}

func TestExportBagConformant(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("Arch")
	// A staged package: a folder with a payload + par2 + manifest + RESTORE.txt.
	staged := t.TempDir()
	writeF := func(name, content string) {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeF("PKG-1.tar", "PAYLOAD-TAR-BYTES")
	writeF("PKG-1.tar.par2", "PAR2")
	writeF("PKG-1.manifest.json", `{"name":"PKG-1"}`)
	writeF("RESTORE.txt", "how to restore")
	writeF("bagit.txt", "should be skipped") // per-package tag file must NOT nest in data/
	a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: "PKG-1", Status: "STAGED", StagedDir: staged, MediaKind: "HDD",
		Files: []ChunkFileRef{{FileID: 1, RelPath: "a.nef", SizeBytes: 5, Hash: "aa"}}})

	out := t.TempDir()
	res, err := a.ExportBag(coll.ID, out, func(float64, string) {})
	if err != nil {
		t.Fatalf("ExportBag: %v", err)
	}
	bag := res["bag"].(string)

	// Required bag structure.
	for _, f := range []string{"bagit.txt", "bag-info.txt", "manifest-sha256.txt", "tagmanifest-sha256.txt", "COMPARISON.md"} {
		if _, err := os.Stat(filepath.Join(bag, f)); err != nil {
			t.Errorf("missing bag tag file %s: %v", f, err)
		}
	}
	// data/ holds the package artifacts (but NOT the per-package bagit.txt).
	if _, err := os.Stat(filepath.Join(bag, "data", "PKG-1", "PKG-1.tar")); err != nil {
		t.Errorf("data/PKG-1/PKG-1.tar should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bag, "data", "PKG-1", "bagit.txt")); !os.IsNotExist(err) {
		t.Error("per-package bagit.txt must not be nested inside data/")
	}
	// COMPARISON.md answers the r/DataHoarder question.
	comp, _ := os.ReadFile(filepath.Join(bag, "COMPARISON.md"))
	for _, name := range []string{"restic", "borg", "Bacula", "dar", "Canister"} {
		if !strings.Contains(string(comp), name) {
			t.Errorf("COMPARISON.md should address %q", name)
		}
	}
	// Every payload manifest line must match the actual file on disk (valid bag).
	verifyBagManifest(t, bag)
	if conf, _ := res["conformant"].(bool); !conf {
		t.Error("a fully-staged archive should export a conformant bag")
	}
}

// TestExportPackageBag proves the per-package "Export as BagIt" action produces a
// conformant single-package bag whose payload manifest matches the data/ files.
func TestExportPackageBag(t *testing.T) {
	a := versApp(t)
	coll := a.Store.AddCollection("Arch")
	staged := t.TempDir()
	for name, content := range map[string]string{
		"PKG-9.tar": "PAYLOAD", "PKG-9.tar.par2": "PAR2", "PKG-9.manifest.json": `{"name":"PKG-9"}`,
		"RESTORE.txt": "how", "manifest-sha256.txt": "aa  a.nef\n", // per-package tag must not nest in data/
	} {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: "PKG-9", Status: "STAGED", StagedDir: staged, MediaKind: "HDD",
		Files: []ChunkFileRef{{FileID: 1, RelPath: "a.nef", SizeBytes: 5, Hash: "aa"}}})

	out := t.TempDir()
	res, err := a.ExportPackageBag(c.ID, out, func(float64, string) {})
	if err != nil {
		t.Fatalf("ExportPackageBag: %v", err)
	}
	bag := res["bag"].(string)
	if _, err := os.Stat(filepath.Join(bag, "data", "PKG-9", "PKG-9.tar")); err != nil {
		t.Errorf("data/PKG-9/PKG-9.tar should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bag, "data", "PKG-9", "manifest-sha256.txt")); !os.IsNotExist(err) {
		t.Error("the per-package manifest tag file must not nest inside data/")
	}
	if pkgs, _ := res["packages"].(int); pkgs != 1 {
		t.Errorf("a single-package bag should report 1 package, got %d", pkgs)
	}
	verifyBagManifest(t, bag)

	// A package that isn't staged locally can't be bagged — clear error, no panic.
	unstaged := a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: "PKG-10", Status: "PLANNED", MediaKind: "HDD"})
	if _, err := a.ExportPackageBag(unstaged.ID, out, nil); err == nil {
		t.Error("exporting an unstaged package should error")
	}
}

// verifyBagManifest checks manifest-sha256.txt against the data/ payload.
func verifyBagManifest(t *testing.T, bag string) {
	t.Helper()
	f, err := os.Open(filepath.Join(bag, "manifest-sha256.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("bad manifest line: %q", line)
		}
		data, err := os.ReadFile(filepath.Join(bag, filepath.FromSlash(parts[1])))
		if err != nil {
			t.Errorf("manifest lists %s but it is missing: %v", parts[1], err)
			continue
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != parts[0] {
			t.Errorf("checksum mismatch for %s", parts[1])
		}
		n++
	}
	if n == 0 {
		t.Error("bag manifest is empty")
	}
}
