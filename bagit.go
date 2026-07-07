package main

// bagit.go — BagIt (RFC 8493) without the trap.
//
// Two things, kept distinct:
//
//  1. Every package gets BagIt-format tag files written BESIDE its payload
//     (bagit.txt, bag-info.txt, manifest-sha256.txt). A curator or an
//     institutional ingest pipeline can read a standard, parseable manifest of
//     exactly what the package preserves — "institutional legibility for free" —
//     without Mnemosyne changing anything about how the data is stored.
//
//  2. A conformant-bag EXPORT action materializes a fully valid BagIt bag (data/
//     payload + manifest + tagmanifest) for handoff to a repository that ingests
//     bags. The COMPARISON.md ("why not restic/borg/Bacula/dar/Canister") rides
//     along inside it.
//
// The trap BagIt usually sets is that adopting it reshapes your storage into a
// data/ tree you can only navigate through bag tooling. Mnemosyne refuses that:
// the storage format stays a plain tar that yields your ORIGINAL tree on
// extraction. BagIt here is a *description* layer, never the storage layer.
//
// Every checksum here is SHA-256 — the only hash allowed on media (see hashing.go).

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed docs/COMPARISON.md
var comparisonMD []byte

const bagItDeclaration = "BagIt-Version: 1.0\nTag-File-Character-Encoding: UTF-8\n"

// bagOxum returns the BagIt Payload-Oxum "octetstream sum": total bytes "." file
// count over the source files a package preserves.
func bagOxum(files []ChunkFileRef) (bytes int64, count int) {
	for _, f := range files {
		bytes += f.SizeBytes
		count++
	}
	return
}

// bagManifestLines renders BagIt manifest lines ("<sha256>  data/<relpath>") for
// a package's source files, sorted by path for a stable, diffable manifest. Files
// with no recorded SHA-256 (legacy/adopted-without-manifest) are skipped and
// reported so the manifest never lists an unverifiable entry.
func bagManifestLines(files []ChunkFileRef) (manifest string, skipped int) {
	rows := make([]string, 0, len(files))
	for _, f := range files {
		if f.Hash == "" {
			skipped++
			continue
		}
		rows = append(rows, fmt.Sprintf("%s  data/%s", f.Hash, filepath.ToSlash(f.RelPath)))
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n") + "\n", skipped
}

// bagInfo builds a bag-info.txt describing what the package preserves. The
// External-Description states plainly that the payload lives in the package's tar
// and that a fully conformant bag comes from the export action — so a reader is
// never misled into treating the beside-the-package tags as a validatable bag.
func bagInfo(c *Chunk, oxumBytes int64, oxumCount int, conformant bool) string {
	desc := "Mnemosyne package. The payload lives in " + payloadName(c) +
		" (a plain POSIX tar); this manifest describes the original files it preserves. " +
		"For a fully conformant BagIt bag with a data/ payload tree, use Mnemosyne's bag export."
	if conformant {
		desc = "Conformant BagIt bag exported by Mnemosyne. data/ holds this package's " +
			"artifacts (the tar payload, par2 parity, package manifest, and RESTORE.txt). " +
			"Extract data/" + payloadName(c) + " with tar to recover the original tree."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Source-Organization: Mnemosyne\n")
	fmt.Fprintf(&b, "Bagging-Date: %s\n", nowDate())
	fmt.Fprintf(&b, "External-Identifier: %s\n", c.Name)
	fmt.Fprintf(&b, "External-Description: %s\n", desc)
	fmt.Fprintf(&b, "Payload-Oxum: %d.%d\n", oxumBytes, oxumCount)
	fmt.Fprintf(&b, "Bag-Software-Agent: Mnemosyne %s\n", appVersion)
	if c.Encrypted {
		fmt.Fprintf(&b, "Mnemosyne-Encryption: OpenPGP symmetric AES-256; key_ref %s (secret not in this bag)\n", c.KeyRef)
	}
	fmt.Fprintf(&b, "Mnemosyne-Payload-SHA256: %s\n", c.EncHash)
	return b.String()
}

func nowDate() string { return time.Now().UTC().Format("2006-01-02") }

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// writeBagItTags writes the BagIt-format tag files beside a package's payload.
// Best-effort and never fatal to a build — these are a legibility layer, not part
// of the custody chain. manifest-sha256.txt lists the SOURCE files the package
// preserves (as data/<relpath>), matching what the conformant export materializes.
func writeBagItTags(dir string, c *Chunk) {
	oxB, oxN := bagOxum(c.Files)
	manifest, _ := bagManifestLines(c.Files)
	info := bagInfo(c, oxB, oxN, false)
	tags := map[string]string{
		"bagit.txt":           bagItDeclaration,
		"bag-info.txt":        info,
		"manifest-sha256.txt": manifest,
	}
	// tagmanifest over the tag files, for bag tooling that expects it.
	names := make([]string, 0, len(tags))
	for n := range tags {
		names = append(names, n)
	}
	sort.Strings(names)
	var tm strings.Builder
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(tags[n]), 0o644); err != nil {
			return
		}
		fmt.Fprintf(&tm, "%s  %s\n", sha256Hex([]byte(tags[n])), n)
	}
	_ = os.WriteFile(filepath.Join(dir, "tagmanifest-sha256.txt"), []byte(tm.String()), 0o644)
}

// ---- conformant bag export ------------------------------------------------

// ExportBag writes a fully conformant BagIt bag for an archive: outputDir/<name>-bag
// with a data/ payload holding each package's artifacts (tar payload, par2 set,
// manifest, RESTORE.txt), a manifest-sha256.txt over data/, bagit.txt,
// bag-info.txt, tagmanifest-sha256.txt, and the COMPARISON.md. Package artifacts
// are copied from each chunk's staged folder; packages not staged locally are
// recorded as skipped (their bytes live only on media). Never writes into a source.
func (a *App) ExportBag(collectionID int, outputDir string, progress func(float64, string)) (map[string]any, error) {
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("output_dir required")
	}
	if err := a.Store.AssertOutsideSources(outputDir); err != nil {
		return nil, err
	}
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	if progress == nil {
		progress = func(float64, string) {}
	}
	bagRoot := filepath.Join(outputDir, fsSafe(coll.Name)+"-bag")
	dataDir := filepath.Join(bagRoot, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	chunks := a.Store.Chunks(collectionID)
	var manifestLines []string
	var skipped []string
	var payloadBytes int64
	payloadCount := 0

	addFile := func(rel string, data []byte) error {
		dst := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		manifestLines = append(manifestLines, fmt.Sprintf("%s  data/%s", sha256Hex(data), rel))
		payloadBytes += int64(len(data))
		payloadCount++
		return nil
	}
	copyFile := func(rel, srcPath string) error {
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		return addFile(rel, data)
	}

	for i, c := range chunks {
		progress(0.1+0.7*float64(i)/float64(len(chunks)+1), "bagging "+c.Name)
		if c.StagedDir == "" {
			skipped = append(skipped, c.Name+" (not staged locally — artifacts are only on media)")
			continue
		}
		entries, err := os.ReadDir(c.StagedDir)
		if err != nil {
			skipped = append(skipped, c.Name+" (staged folder unreadable)")
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// BagIt manages its own tag files at the bag root — don't nest the
			// package's own BagIt tags inside data/.
			if isBagItTagFile(e.Name()) {
				continue
			}
			rel := c.Name + "/" + e.Name()
			if err := copyFile(rel, filepath.Join(c.StagedDir, e.Name())); err != nil {
				return nil, fmt.Errorf("copying %s: %w", rel, err)
			}
		}
	}

	// Payload manifest (over data/), sorted.
	sort.Strings(manifestLines)
	manifest := strings.Join(manifestLines, "\n")
	if manifest != "" {
		manifest += "\n"
	}

	progress(0.85, "bag tags")
	info := exportBagInfo(coll.Name, payloadBytes, payloadCount, len(chunks), len(skipped))
	tagFiles := map[string]string{
		"bagit.txt":           bagItDeclaration,
		"bag-info.txt":        info,
		"manifest-sha256.txt": manifest,
		"COMPARISON.md":       string(comparisonMD),
	}
	names := make([]string, 0, len(tagFiles))
	for n := range tagFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	var tm strings.Builder
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bagRoot, n), []byte(tagFiles[n]), 0o644); err != nil {
			return nil, err
		}
		fmt.Fprintf(&tm, "%s  %s\n", sha256Hex([]byte(tagFiles[n])), n)
	}
	if err := os.WriteFile(filepath.Join(bagRoot, "tagmanifest-sha256.txt"), []byte(tm.String()), 0o644); err != nil {
		return nil, err
	}

	a.Store.Log("bagit", fmt.Sprintf("%s: exported bag (%d file(s), %d package(s), %d skipped)", coll.Name, payloadCount, len(chunks), len(skipped)))
	progress(1.0, "done")
	return map[string]any{
		"bag": bagRoot, "payload_files": payloadCount, "payload_bytes": payloadBytes,
		"packages": len(chunks), "skipped": skipped, "conformant": len(skipped) == 0,
	}, nil
}

func exportBagInfo(name string, payloadBytes int64, payloadCount, pkgs, skipped int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source-Organization: Mnemosyne\n")
	fmt.Fprintf(&b, "Bagging-Date: %s\n", nowDate())
	fmt.Fprintf(&b, "External-Identifier: %s\n", name)
	fmt.Fprintf(&b, "External-Description: Conformant BagIt export of Mnemosyne archive %q. "+
		"data/ holds each package's artifacts (plain-tar payload, par2 parity, manifest, RESTORE.txt). "+
		"Extract any data/<package>/<name>.tar to recover the original tree — no Mnemosyne required. "+
		"See COMPARISON.md for why this format over restic/borg/Bacula/dar/Canister.\n", name)
	fmt.Fprintf(&b, "Payload-Oxum: %d.%d\n", payloadBytes, payloadCount)
	fmt.Fprintf(&b, "Bag-Count: %d package(s)\n", pkgs)
	if skipped > 0 {
		fmt.Fprintf(&b, "Mnemosyne-Skipped-Packages: %d (artifacts only on media, not staged locally)\n", skipped)
	}
	fmt.Fprintf(&b, "Bag-Software-Agent: Mnemosyne %s\n", appVersion)
	return b.String()
}

// isBagItTagFile reports whether a filename is a BagIt tag file (so the export
// doesn't copy a package's own BagIt tags down into the bag's data/ payload).
func isBagItTagFile(name string) bool {
	switch name {
	case "bagit.txt", "bag-info.txt", "manifest-sha256.txt", "tagmanifest-sha256.txt":
		return true
	}
	return false
}
