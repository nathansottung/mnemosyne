package main

// schema_test.go — the forward-compatibility guarantee (see currentSchemaVersion
// and docs/CONTRIBUTING.md "Schema versioning"). Three cases:
//   1. a checked-in schema-1 catalog round-trips (load → save → reload) losing nothing;
//   2. a pre-versioning catalog migrates up AND its exact bytes are backed up first;
//   3. a catalog from a NEWER schema opens read-only and never overwrites the file.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogSchema1RoundTrip(t *testing.T) {
	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "catalog_schema1.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	catPath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catPath, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	s1, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if ro, _ := s1.ReadOnly(); ro {
		t.Fatal("a schema-1 fixture must not open read-only")
	}
	if s1.c.SchemaVersion != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", s1.c.SchemaVersion, currentSchemaVersion)
	}
	colls, chunks, keys, vols := s1.Collections(), s1.Chunks(0), s1.KeyMetas(), s1.Volumes()

	// A current-schema fixture must NOT trigger a migration backup.
	if baks, _ := filepath.Glob(catPath + ".pre-schema-*"); len(baks) != 0 {
		t.Errorf("no migration expected at current schema, but a backup was written: %v", baks)
	}

	// Force a clean re-save (no data mutation), then reload from disk.
	s1.mu.Lock()
	werr := s1.writeCatalog()
	s1.mu.Unlock()
	if werr != nil {
		t.Fatalf("save: %v", werr)
	}
	// Byte-identical persistence proves the struct captured every field — nothing dropped.
	if after, _ := os.ReadFile(catPath); !bytes.Equal(fixture, after) {
		t.Errorf("catalog.json changed across load→save — a field may have been lost or reshaped")
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(s2.Collections()) != len(colls) || len(s2.Chunks(0)) != len(chunks) ||
		len(s2.KeyMetas()) != len(keys) || len(s2.Volumes()) != len(vols) {
		t.Fatalf("entity counts changed across save/reload (colls %d, chunks %d, keys %d, vols %d)",
			len(s2.Collections()), len(s2.Chunks(0)), len(s2.KeyMetas()), len(s2.Volumes()))
	}
	// Spot-check deep values survive intact.
	c := s2.Chunk(chunks[0].ID)
	if c == nil || c.Name != "PKG-C0001" || len(c.Files) != 2 || len(c.Copies) != 1 {
		t.Fatalf("chunk data not preserved: %+v", c)
	}
	if c.Files[1].RelPath != "b/c.nef" || c.Files[1].Hash != "h2" {
		t.Errorf("nested file refs not preserved: %+v", c.Files)
	}
	if c.KeyRef != "K-AB12CD34" || c.Copies[0].Path != "T:/PKG-C0001" {
		t.Errorf("chunk key/copy not preserved: keyref=%q copies=%+v", c.KeyRef, c.Copies)
	}
}

func TestCatalogLegacyMigratesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	// A pre-versioning catalog: no schema_version field (reads as 0).
	legacy := []byte(`{"next_id":{"collection":1},"collections":[{"id":1,"name":"Old"}]}`)
	catPath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catPath, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.c.SchemaVersion != currentSchemaVersion {
		t.Errorf("legacy catalog must migrate to v%d, got %d", currentSchemaVersion, s.c.SchemaVersion)
	}
	// The exact pre-migration bytes must be backed up before we touch anything.
	baks, _ := filepath.Glob(catPath + ".pre-schema-v0-*")
	if len(baks) == 0 {
		t.Fatal("migration must write a pre-schema backup of the old catalog")
	}
	if got, _ := os.ReadFile(baks[0]); !bytes.Equal(got, legacy) {
		t.Error("the migration backup must be the EXACT pre-migration bytes")
	}
	// Data survives the migration and now persists stamped at the current version.
	if len(s.Collections()) != 1 || s.Collections()[0].Name != "Old" {
		t.Errorf("data lost in migration: %+v", s.Collections())
	}
	if on, _ := os.ReadFile(catPath); !bytes.Contains(on, []byte(`"schema_version": 1`)) {
		t.Error("migrated catalog on disk must be stamped at schema 1")
	}
}

func TestCatalogNewerSchemaIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	newer := []byte(`{"schema_version":999,"collections":[{"id":1,"name":"Future"}]}`)
	catPath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catPath, newer, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ro, why := s.ReadOnly()
	if !ro {
		t.Fatal("a newer-schema catalog must open read-only")
	}
	if !strings.Contains(why, "newer") || !strings.Contains(why, "Upgrade") {
		t.Errorf("read-only reason should explain the newer-version refusal clearly: %q", why)
	}
	// Reads still work.
	if len(s.Collections()) != 1 {
		t.Error("read-only viewing must still work")
	}
	// Writes are refused and must NOT overwrite the file.
	s.mu.Lock()
	werr := s.writeCatalog()
	s.mu.Unlock()
	if werr == nil {
		t.Error("writeCatalog must refuse when the catalog is read-only")
	}
	// A mutating op whose save error is ignored must still leave the DISK untouched.
	s.AddCollection("should-not-persist")
	if after, _ := os.ReadFile(catPath); !bytes.Equal(after, newer) {
		t.Error("a newer-schema catalog must never be rewritten on disk")
	}
}
