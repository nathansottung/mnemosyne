package main

// mirror.go — native MIRROR backup: copy an Archive's files to a Volume as PLAIN
// FILES, preserving the source tree. Mirrors are the un-encrypted, browse-anywhere
// complement to packages: packages are sealed/verified/encrypted units for tape &
// optical; mirrors are live, directly-browsable copies for spinning drives.
//
// Each file is copied with v1's copy-then-verify discipline: staged to
// <name>.mnemo_tmp, its bytes hashed as they are written, read back off the
// destination and compared to the source, then ATOMICALLY renamed into place —
// so a partially-written or corrupted file never appears under its real name. The
// result is recorded exactly like an adopted mirror (Prompt 23): a verified
// file-level Copy on the volume, via the same Chunk.Mirror record, so drift and
// coverage count it as a real copy with no special-casing. The volume inventory
// sidecar is refreshed afterward.
//
// Multiple mirror jobs run concurrently — one job per volume — which v1 proved is
// the right shape for feeding several spinning drives at once. Writer-side speed
// honors throttle_mbps (thermal control), paced across the WHOLE job.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const mirrorTmpSuffix = ".mnemo_tmp"

// throttler paces cumulative written bytes against wall-clock to hold a steady
// MB/s cap across an entire mirror job (not per-file), the same self-correcting
// scheme the ring-buffer writer uses. bps <= 0 disables pacing.
type throttler struct {
	bps     float64
	start   time.Time
	written int64
}

func (t *throttler) pace(n int) {
	if t == nil || t.bps <= 0 {
		return
	}
	t.written += int64(n)
	target := time.Duration(float64(t.written) / t.bps * float64(time.Second))
	if el := time.Since(t.start); target > el {
		time.Sleep(target - el)
	}
}

// mirrorCopyFile streams src -> dst, hashing the bytes on the way through
// (so the returned hash proves what left the source), pacing the writer, and
// fsyncing before return so a subsequent read-back sees the medium, not cache.
func mirrorCopyFile(src, dst string, th *throttler, onBytes func(int64)) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var written int64
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return "", written, werr
			}
			h.Write(buf[:n])
			written += int64(n)
			if onBytes != nil {
				onBytes(int64(n))
			}
			th.pace(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return "", written, rerr
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", written, err
	}
	if err := out.Close(); err != nil {
		return "", written, err
	}
	return hex.EncodeToString(h.Sum(nil)), written, nil
}

// MirrorResult is the per-volume outcome (also the API/job return shape).
type MirrorResult struct {
	VolumeID int      `json:"volume_id"`
	Volume   string   `json:"volume"`
	Dest     string   `json:"dest"`
	Mirrored int      `json:"mirrored"`
	Bytes    int64    `json:"bytes"`
	Changed  int      `json:"changed"` // copied, but source content differs from the catalog hash (source drifted)
	Failed   int      `json:"failed"`  // copy or verify failed; not finalized
	Skipped  int      `json:"skipped"` // source unreadable/missing
	Sidecar  string   `json:"sidecar,omitempty"`
	Coverage Coverage `json:"coverage"`
}

// MirrorToVolume copies the selected folders of an archive to destDir as plain
// files (copy-then-verify each), records a verified mirror Copy on the volume,
// and refreshes the volume inventory sidecar. One call == one volume; run several
// concurrently for multi-volume mirroring.
func (a *App) MirrorToVolume(collectionID int, folderIDs []int, destDir string, volumeID int, throttleMbps float64, progress func(float64, string)) (*MirrorResult, error) {
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	if strings.TrimSpace(destDir) == "" {
		return nil, fmt.Errorf("dest_dir (the mirror target on the volume) required")
	}
	// SOURCE-SAFETY: a mirror WRITES files + a sidecar into destDir — it must
	// never resolve inside a registered source root.
	if err := a.Store.AssertOutsideSources(destDir); err != nil {
		return nil, err
	}
	vol := a.Store.Volume(volumeID)
	if vol == nil {
		return nil, fmt.Errorf("volume %d not found", volumeID)
	}
	if throttleMbps <= 0 {
		throttleMbps = a.LoadConfig().ThrottleMbps
	}
	// Batch catalog writes across the mirror job (idempotent copy-then-verify).
	a.Store.BeginBatch()
	defer a.Store.EndBatch()

	// Resolve the source files (optionally limited to chosen folders) and the
	// folder roots they hang off.
	folderPath := map[int]string{}
	for _, f := range a.Store.FoldersOf(collectionID) {
		folderPath[f.ID] = f.Path
	}
	want := map[int]bool{}
	for _, id := range folderIDs {
		want[id] = true
	}
	var files []*File
	usedFolders := map[int]bool{}
	var totalBytes int64
	for _, f := range a.Store.FilesOf(collectionID) {
		if len(want) > 0 && !want[f.FolderID] {
			continue
		}
		if folderPath[f.FolderID] == "" {
			continue // orphan file with no source folder — cannot locate on disk
		}
		files = append(files, f)
		usedFolders[f.FolderID] = true
		totalBytes += f.SizeBytes
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no cataloged files to mirror (scan a folder into the archive first, or widen the folder selection)")
	}

	// Preserve the source tree. With a single source folder, files land at their
	// bare rel path; with several, each folder gets a unique subtree so trees from
	// different roots never collide.
	label := mirrorFolderLabels(usedFolders, folderPath)
	multi := len(usedFolders) > 1

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	// Space pre-flight against the destination (best-effort).
	if free, err := diskFree(destDir); err == nil && free < totalBytes {
		return nil, fmt.Errorf("destination too small: need %.1f GB, free %.1f GB", float64(totalBytes)/1e9, float64(free)/1e9)
	}

	res := &MirrorResult{VolumeID: vol.ID, Volume: vol.Label, Dest: destDir}
	th := &throttler{bps: throttleMbps * 1e6, start: time.Now()}
	var doneBytes, doneFiles int64
	report := func(msg string) {
		frac := 0.0
		if totalBytes > 0 {
			frac = float64(doneBytes) / float64(totalBytes)
		}
		progress(0.02+frac*0.92, progStats(doneBytes, totalBytes, doneFiles, int64(len(files)), msg))
	}

	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	refs := make([]ChunkFileRef, 0, len(files))
	lastTick := time.Now() // paces mid-file progress so live MB/s updates on big files too
	for i, f := range files {
		doneFiles = int64(i + 1) // file currently being mirrored (matches the "i+1/len" message)
		srcPath := filepath.Join(folderPath[f.FolderID], filepath.FromSlash(f.RelPath))
		mrel := f.RelPath
		if multi {
			mrel = label[f.FolderID] + "/" + f.RelPath
		}
		destPath := filepath.Join(destDir, filepath.FromSlash(mrel))
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			res.Failed++
			continue
		}
		tmp := destPath + mirrorTmpSuffix
		if i%25 == 0 || len(files) < 50 {
			report(fmt.Sprintf("mirroring %d/%d — %s", i+1, len(files), f.RelPath))
		}
		streamHash, n, err := mirrorCopyFile(srcPath, tmp, th, func(d int64) {
			doneBytes += d
			if time.Since(lastTick) > 700*time.Millisecond {
				lastTick = time.Now()
				report(fmt.Sprintf("mirroring %d/%d — %s", i+1, len(files), f.RelPath))
			}
		})
		if err != nil {
			_ = os.Remove(tmp)
			res.Skipped++ // source unreadable / mid-copy IO error
			continue
		}
		// Copy-then-verify: read the bytes back off the destination and confirm
		// they are byte-identical to the source before the file gets its real name.
		rb, rerr := hashFileHex(tmp)
		if rerr != nil || rb != streamHash {
			_ = os.Remove(tmp)
			res.Failed++
			continue
		}
		if err := atomicRename(tmp, destPath); err != nil {
			_ = os.Remove(tmp)
			res.Failed++
			continue
		}
		if streamHash != f.Hash {
			res.Changed++ // faithful copy of the CURRENT source, which drifted from the catalog
		}
		// Record the level-C sample baseline (size + first/last 4 MiB) so a cheap
		// sample re-verify has something to compare against later.
		sample, _ := sampleHashHex(destPath)
		refs = append(refs, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: n, Hash: streamHash, SampleHash: sample})
		res.Mirrored++
		res.Bytes += n
	}

	// Record the verified file-level copies exactly like an adopted mirror, so
	// drift and coverage treat them as real copies (Prompt 23 records).
	if len(refs) > 0 {
		sort.Slice(refs, func(i, j int) bool { return refs[i].RelPath < refs[j].RelPath })
		a.upsertMirrorChunk(collectionID, vol, destDir, refs, "mirror")
	}

	progress(0.96, "refreshing volume inventory sidecar")
	if sc, err := a.writeVolumeInventory(destDir, vol); err == nil {
		res.Sidecar = sc
	}
	res.Coverage = a.archiveCoverage([]int{collectionID})
	a.Store.Log("mirror", fmt.Sprintf("%s → %s (%s): %d files (%.1f GB), %d changed, %d failed, %d skipped",
		coll.Name, vol.Label, destDir, res.Mirrored, float64(res.Bytes)/1e9, res.Changed, res.Failed, res.Skipped))
	progress(1.0, fmt.Sprintf("mirrored %d file(s) to %s", res.Mirrored, vol.Label))
	return res, nil
}

// atomicRename renames tmp -> final, replacing any prior file at final so a
// re-mirror overwrites cleanly (os.Rename already replaces on unix; on Windows
// it fails if the target exists, so remove first).
func atomicRename(tmp, final string) error {
	if err := os.Rename(tmp, final); err == nil {
		return nil
	}
	_ = os.Remove(final)
	return os.Rename(tmp, final)
}

// mirrorFolderLabels assigns each source folder a unique, filesystem-safe subtree
// name (its base name, disambiguated by folder id on collision) for multi-folder
// mirrors.
func mirrorFolderLabels(used map[int]bool, folderPath map[int]string) map[int]string {
	ids := make([]int, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := map[int]string{}
	seen := map[string]bool{}
	for _, id := range ids {
		base := safeName(filepath.Base(strings.TrimRight(folderPath[id], `/\`)))
		if base == "" {
			base = fmt.Sprintf("folder-%d", id)
		}
		if seen[base] {
			base = fmt.Sprintf("%s-%d", base, id)
		}
		seen[base] = true
		out[id] = base
	}
	return out
}

// writeVolumeInventory refreshes the MNEMOSYNE_DOCK sidecar at destDir describing
// everything the catalog says this volume holds — a self-documenting inventory
// that survives the catalog. Guarded against writing into a source.
func (a *App) writeVolumeInventory(destDir string, vol *Volume) (string, error) {
	if err := a.Store.AssertOutsideSources(destDir); err != nil {
		return "", err
	}
	dir := filepath.Join(destDir, dockSidecarDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	now := time.Now().UTC()

	type invFile struct {
		RelPath string `json:"rel_path"`
		Hash    string `json:"hash"`
		Size    int64  `json:"size_bytes"`
	}
	type invArchive struct {
		ArchiveID int            `json:"archive_id"`
		Archive   string         `json:"archive"`
		Chunk     string         `json:"chunk"`
		Mirror    bool           `json:"mirror"`
		Integrity *BuildVerified `json:"integrity,omitempty"` // built packages attest their effective integrity
		Files     []invFile      `json:"files"`
	}
	var archives []invArchive
	var totalFiles int
	var totalBytes int64
	tally := newExtTally() // format census of exactly what this volume holds
	for _, c := range a.Store.Chunks(0) {
		on := false
		for _, cp := range c.Copies {
			if cp.VolumeID == vol.ID && !cp.Superseded {
				on = true
				break
			}
		}
		if !on {
			continue
		}
		files := make([]invFile, 0, len(c.Files))
		for _, ref := range c.Files {
			files = append(files, invFile{RelPath: ref.RelPath, Hash: ref.Hash, Size: ref.SizeBytes})
			totalBytes += ref.SizeBytes
			tally.add(ref.RelPath, ref.SizeBytes)
		}
		sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
		totalFiles += len(files)
		name := ""
		if coll := a.Store.Collection(c.CollectionID); coll != nil {
			name = coll.Name
		}
		archives = append(archives, invArchive{ArchiveID: c.CollectionID, Archive: name, Chunk: c.Name, Mirror: c.Mirror, Integrity: c.BuildVerified, Files: files})
	}

	census := a.censusFromTally(tally)
	snap := map[string]any{
		"mnemosyne_volume_inventory": 1, "generated_utc": now.Format(time.RFC3339),
		"volume": vol, "total_files": totalFiles, "total_bytes": totalBytes, "archives": archives,
		"formats": census,
	}
	sb, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "catalog_snapshot.json"), sb, 0o644); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# Mnemosyne — volume inventory\n\n")
	b.WriteString(fmt.Sprintf("Generated %s. This medium holds **%d file(s)** (%s) across %d archive(s).\n\n",
		now.Format(time.RFC3339), totalFiles, humanBytes(totalBytes), len(archives)))
	b.WriteString(fmt.Sprintf("- **Volume:** %s\n", vol.Label))
	if vol.Serial != "" {
		b.WriteString(fmt.Sprintf("- **Serial:** `%s`\n", vol.Serial))
	}
	b.WriteString("\nMirror files are stored as **plain files** — browse or copy them with any tool; no Mnemosyne, key, or unpack step is needed. Each was copy-then-verified (SHA-256) against its source.\n")
	for _, ar := range archives {
		kind := "package"
		if ar.Mirror {
			kind = "mirror"
		}
		b.WriteString(fmt.Sprintf("\n## %s — %s (%d file(s))\n\n", ar.Archive, kind, len(ar.Files)))
		for i, f := range ar.Files {
			if i >= 2000 {
				b.WriteString(fmt.Sprintf("_…and %d more (see catalog_snapshot.json)_\n", len(ar.Files)-i))
				break
			}
			b.WriteString(fmt.Sprintf("- `%s`\n", f.RelPath))
		}
	}
	b.WriteString("\n---\n\n")
	b.WriteString(fmt.Sprintf("**Formats:** %.0f%% of these bytes are in OPEN or DOCUMENTED formats.\n\n", census.SafePct))
	b.WriteString("```\n" + readersReference(census) + "```\n")
	if err := os.WriteFile(filepath.Join(dir, "INVENTORY.md"), []byte(b.String()), 0o644); err != nil {
		return dir, err
	}
	return dir, nil
}
