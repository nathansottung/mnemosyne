package main

// incremental.go — "Back up what's new." An explicit incremental workflow layered
// over the existing delta machinery. It never keeps "last backup" bookkeeping: every
// run RECOMPUTES the delta from the catalog, so an interrupted or skipped run just
// self-heals on the next run. Two plainly-labeled bases:
//
//   - "volume"     → everything not yet on THIS volume (hashes absent from that
//                    volume's copies) — for rotating per-drive incrementals;
//   - "protection" → everything not fully protected (files below COMPLETE per their
//                    profile) — for topping up overall protection.
//
// Output follows the destination: mirror-style plain files (drives; the default,
// copy-then-verified and preserving tree) or package-style (tape/optical; media-sized
// packages planned from the delta, written/verified through the normal Packages
// engine). Either way each landed file records a verified Copy and refreshes the
// volume inventory sidecar, and the run is recorded as a named BackupSession that
// shows in Backup History and feeds Home's periodic-backup recognition.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup bases and modes (small closed sets; the UI labels them in plain words).
const (
	BaseVolume     = "volume"     // everything not yet on THIS volume
	BaseProtection = "protection" // everything not fully protected (below COMPLETE)
	ModeMirror     = "mirror"     // plain files (drives)
	ModePackage    = "package"    // media-sized packages (tape/optical)
)

// DeltaRole is one row of the per-role breakdown shown before a run.
type DeltaRole struct {
	Role  string `json:"role"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// BackupDelta is the "what would this run do" preview: counts, bytes, per-role
// breakdown, the chosen mode, and a fit check against the destination's free space.
type BackupDelta struct {
	CollectionID int         `json:"collection_id"`
	VolumeID     int         `json:"volume_id"`
	Base         string      `json:"base"`
	Mode         string      `json:"mode"`
	Files        int         `json:"files"`
	Bytes        int64       `json:"bytes"`
	Roles        []DeltaRole `json:"roles"`
	Dest         string      `json:"dest,omitempty"`
	FreeBytes    int64       `json:"free_bytes,omitempty"`
	FreeKnown    bool        `json:"free_known"`
	Fits         bool        `json:"fits"`
	AlreadyDone  bool        `json:"already_current"` // true when the delta is empty — nothing to do
}

// normBase / normMode clamp to the known values (default: volume base, mirror mode).
func normBase(v string) string {
	if strings.EqualFold(v, BaseProtection) {
		return BaseProtection
	}
	return BaseVolume
}

// defaultModeForVolume picks mirror for spinning/solid drives, package for tape/optical.
func defaultModeForVolume(v *Volume) string {
	switch strings.ToUpper(strings.TrimSpace(v.Kind)) {
	case "TAPE", "OPTICAL":
		return ModePackage
	default:
		return ModeMirror
	}
}

func normMode(v string, vol *Volume) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ModeMirror:
		return ModeMirror
	case ModePackage:
		return ModePackage
	default:
		return defaultModeForVolume(vol)
	}
}

// hashesOnVolume is the set of content hashes already recorded as a current copy on
// a volume — the "volume" base subtracts these.
func (a *App) hashesOnVolume(volumeID int) map[string]bool {
	set := map[string]bool{}
	for _, c := range a.Store.Chunks(0) {
		on := false
		for _, cp := range c.Copies {
			if cp.VolumeID == volumeID && !cp.Superseded {
				on = true
				break
			}
		}
		if !on {
			continue
		}
		for _, r := range c.Files {
			if r.Hash != "" {
				set[r.Hash] = true
			}
		}
	}
	return set
}

// incrementalDelta computes the delta file set for a base over a collection, scoped
// to folderIDs (empty = whole archive). Only files with a resolvable source folder
// are eligible (they must be locatable on disk to copy). Stateless: recomputed every
// call, so it self-heals across interrupted/skipped runs.
func (a *App) incrementalDelta(collectionID int, folderIDs []int, volumeID int, base string) ([]*File, error) {
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	folderPath := map[int]string{}
	for _, f := range a.Store.FoldersOf(collectionID) {
		folderPath[f.ID] = f.Path
	}
	want := map[int]bool{}
	for _, id := range folderIDs {
		want[id] = true
	}
	inScope := func(f *File) bool {
		if len(want) > 0 && !want[f.FolderID] {
			return false
		}
		return folderPath[f.FolderID] != "" // must be locatable on disk
	}

	var eligible func(f *File) bool
	switch normBase(base) {
	case BaseProtection:
		statuses := a.Store.FileStatuses(collectionID)
		eligible = func(f *File) bool {
			st := statuses[f.ID]
			return st != StatusComplete && st != StatusOverComplete
		}
	default: // volume
		onVol := a.hashesOnVolume(volumeID)
		eligible = func(f *File) bool { return f.Hash == "" || !onVol[f.Hash] }
	}

	var out []*File
	for _, f := range a.Store.FilesOf(collectionID) {
		if inScope(f) && eligible(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
}

// roleBreakdown tallies files+bytes per discipline-neutral role for the preview.
func (a *App) roleBreakdown(files []*File) []DeltaRole {
	reg := a.formatRegistry()
	agg := map[string]*DeltaRole{}
	order := []string{}
	for _, f := range files {
		role := f.Role
		if role == "" {
			role, _ = classifyRole(reg, f.RelPath)
		}
		if role == "" {
			role = "OTHER"
		}
		r := agg[role]
		if r == nil {
			r = &DeltaRole{Role: role}
			agg[role] = r
			order = append(order, role)
		}
		r.Files++
		r.Bytes += f.SizeBytes
	}
	sort.Strings(order)
	out := make([]DeltaRole, 0, len(order))
	for _, k := range order {
		out = append(out, *agg[k])
	}
	return out
}

// BackupDeltaPreview computes the "what would this do" summary — counts, bytes,
// per-role breakdown, resolved mode, and a fit check against destDir's free space.
func (a *App) BackupDeltaPreview(collectionID int, folderIDs []int, volumeID int, base, mode, destDir string) (*BackupDelta, error) {
	// A not-yet-registered volume (id 0) is a valid preview target: it holds nothing,
	// so the "volume" base is the whole in-scope set. Mode then defaults to the passed
	// hint (or mirror), since there's no volume kind to read.
	vol := a.Store.Volume(volumeID)
	files, err := a.incrementalDelta(collectionID, folderIDs, volumeID, base)
	if err != nil {
		return nil, err
	}
	resolvedMode := ModeMirror
	if vol != nil {
		resolvedMode = normMode(mode, vol)
	} else if strings.EqualFold(strings.TrimSpace(mode), ModePackage) {
		resolvedMode = ModePackage
	}
	d := &BackupDelta{CollectionID: collectionID, VolumeID: volumeID, Base: normBase(base),
		Mode: resolvedMode, Dest: destDir, Files: len(files)}
	for _, f := range files {
		d.Bytes += f.SizeBytes
	}
	d.Roles = a.roleBreakdown(files)
	d.AlreadyDone = len(files) == 0
	if strings.TrimSpace(destDir) != "" {
		if free, ferr := diskFree(destDir); ferr == nil {
			d.FreeBytes, d.FreeKnown = free, true
			d.Fits = free >= d.Bytes
		}
	}
	return d, nil
}

// BackupChangesResult is the outcome of a run (also the API/job return shape).
type BackupChangesResult struct {
	SessionID int      `json:"session_id"`
	Base      string   `json:"base"`
	Mode      string   `json:"mode"`
	Files     int      `json:"files"`
	Bytes     int64    `json:"bytes"`
	Changed   int      `json:"changed"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Dest      string   `json:"dest,omitempty"`
	Sidecar   string   `json:"sidecar,omitempty"`
	Planned   []string `json:"planned_packages,omitempty"` // package mode: the PLANNED chunks to build & write
	Name      string   `json:"name,omitempty"`
	Message   string   `json:"message,omitempty"`
}

// BackupChanges runs an incremental backup of the delta onto a volume. Mirror mode
// copies plain files (copy-then-verified, tree preserved) and records verified
// copies + refreshes the sidecar; package mode plans media-sized packages from the
// delta for the normal build/write engine. Either way a named BackupSession is
// recorded (unless the delta was empty).
func (a *App) BackupChanges(collectionID int, folderIDs []int, volumeID int, base, mode, destDir string, throttleMbps float64, progress func(float64, string)) (*BackupChangesResult, error) {
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	vol := a.Store.Volume(volumeID)
	if vol == nil {
		return nil, fmt.Errorf("volume %d not found", volumeID)
	}
	base, mode = normBase(base), normMode(mode, vol)

	files, err := a.incrementalDelta(collectionID, folderIDs, volumeID, base)
	if err != nil {
		return nil, err
	}
	res := &BackupChangesResult{Base: base, Mode: mode, Dest: destDir}
	if len(files) == 0 {
		res.Message = "Already current — nothing new to back up to " + vol.Label + "."
		progress(1.0, res.Message)
		return res, nil
	}

	if mode == ModePackage {
		planned, err := a.planDeltaPackages(coll, files, vol, progress)
		if err != nil {
			return nil, err
		}
		for _, c := range planned {
			res.Planned = append(res.Planned, c.Name)
			res.Files += c.FileCount
			res.Bytes += c.DataBytes
		}
		res.Message = fmt.Sprintf("Planned %d package(s) from %d changed file(s) — build & write them from Packages to land verified copies on %s.", len(planned), res.Files, vol.Label)
		a.recordBackupSession(collectionID, vol, base, mode, res.Files, res.Bytes, folderIDs, "", res)
		progress(1.0, res.Message)
		return res, nil
	}

	// ---- mirror mode ----
	if strings.TrimSpace(destDir) == "" {
		return nil, fmt.Errorf("dest_dir (the mirror target on the volume) required for a drive backup")
	}
	if err := a.Store.AssertOutsideSources(destDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}

	a.Store.BeginBatch()
	defer a.Store.EndBatch()

	folderPath := map[int]string{}
	for _, f := range a.Store.FoldersOf(collectionID) {
		folderPath[f.ID] = f.Path
	}
	usedFolders := map[int]bool{}
	var totalBytes int64
	for _, f := range files {
		usedFolders[f.FolderID] = true
		totalBytes += f.SizeBytes
	}
	if free, err := diskFree(destDir); err == nil && free < totalBytes {
		return nil, fmt.Errorf("destination too small: need %.1f GB, free %.1f GB", float64(totalBytes)/1e9, float64(free)/1e9)
	}
	label := mirrorFolderLabels(usedFolders, folderPath)
	multi := len(usedFolders) > 1

	if throttleMbps <= 0 {
		throttleMbps = a.LoadConfig().ThrottleMbps
	}
	th := &throttler{bps: throttleMbps * 1e6, start: time.Now()}
	var doneBytes int64
	lastTick := time.Now()
	report := func(msg string) {
		frac := 0.0
		if totalBytes > 0 {
			frac = float64(doneBytes) / float64(totalBytes)
		}
		progress(0.02+frac*0.92, progStats(doneBytes, totalBytes, int64(res.Files), int64(len(files)), msg))
	}

	refs := make([]ChunkFileRef, 0, len(files))
	for i, f := range files {
		srcPath := filepath.Join(folderPath[f.FolderID], filepath.FromSlash(f.RelPath))
		mrel := f.RelPath
		if multi {
			mrel = label[f.FolderID] + "/" + f.RelPath
		}
		destPath := filepath.Join(destDir, filepath.FromSlash(mrel))
		if i%25 == 0 || len(files) < 50 {
			report(fmt.Sprintf("backing up %d/%d — %s", i+1, len(files), f.RelPath))
		}
		streamHash, n, cerr := copyVerifyToDest(srcPath, destPath, th, func(d int64) {
			doneBytes += d
			if time.Since(lastTick) > 700*time.Millisecond {
				lastTick = time.Now()
				report(fmt.Sprintf("backing up %d/%d — %s", i+1, len(files), f.RelPath))
			}
		})
		if cerr != nil {
			res.Failed++
			continue
		}
		if streamHash != f.Hash {
			res.Changed++ // faithful copy of the CURRENT source, which drifted from the catalog
		}
		sample, _ := sampleHashHex(destPath)
		refs = append(refs, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: n, Hash: streamHash, SampleHash: sample})
		res.Files++
		res.Bytes += n
	}

	if len(refs) > 0 {
		a.mergeMirrorRefs(collectionID, vol, destDir, refs)
	}
	progress(0.96, "refreshing volume inventory sidecar")
	if sc, err := a.writeVolumeInventory(destDir, vol); err == nil {
		res.Sidecar = sc
	}
	if res.Files > 0 {
		a.recordBackupSession(collectionID, vol, base, mode, res.Files, res.Bytes, folderIDs, destDir, res)
	}
	a.Store.Log("incremental", fmt.Sprintf("%s → %s (%s, base=%s): %d files (%.1f GB), %d changed, %d failed",
		coll.Name, vol.Label, destDir, base, res.Files, float64(res.Bytes)/1e9, res.Changed, res.Failed))
	progress(1.0, fmt.Sprintf("backed up %d changed file(s) to %s", res.Files, vol.Label))
	return res, nil
}

// recordBackupSession appends the named history record and stamps its name onto res.
func (a *App) recordBackupSession(collectionID int, vol *Volume, base, mode string, files int, bytes int64, folderIDs []int, dest string, res *BackupChangesResult) {
	now := time.Now().UTC()
	name := fmt.Sprintf("Incremental to %s — %d file(s), %s, %s", vol.Label, files, humanBytes(bytes), now.Format("2006-01-02"))
	s := a.Store.AddBackupSession(&BackupSession{
		CollectionID: collectionID, VolumeID: vol.ID, VolumeLabel: vol.Label,
		Base: base, Mode: mode, Files: files, Bytes: bytes, FolderIDs: folderIDs, At: now, Name: name, Dest: dest,
	})
	res.SessionID, res.Name = s.ID, name
}

// mergeMirrorRefs unions a delta's verified refs into the volume's mirror chunk
// (replacing entries for the same file, adding new ones) so the chunk always
// reflects the FULL set of files now on that volume — the incremental analogue of
// upsertMirrorChunk (which replaces wholesale for a full mirror).
func (a *App) mergeMirrorRefs(archiveID int, vol *Volume, destDir string, deltaRefs []ChunkFileRef) {
	now := time.Now().UTC()
	ok := true
	for _, c := range a.Store.Chunks(archiveID) {
		if !c.Mirror {
			continue
		}
		onVol := false
		for _, cp := range c.Copies {
			if cp.VolumeID == vol.ID && !cp.Superseded {
				onVol = true
				break
			}
		}
		if !onVol {
			continue
		}
		merged := map[int]ChunkFileRef{}
		for _, r := range c.Files {
			merged[r.FileID] = r
		}
		for _, r := range deltaRefs {
			merged[r.FileID] = r
		}
		refs := make([]ChunkFileRef, 0, len(merged))
		var sum int64
		for _, r := range merged {
			refs = append(refs, r)
			sum += r.SizeBytes
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].RelPath < refs[j].RelPath })
		c.Files, c.FileCount, c.EncBytes, c.DataBytes = refs, len(refs), sum, sum
		c.WrittenDest, c.VerifiedAt, c.VerifyOK, c.Status = destDir, &now, &ok, StatusAdoptedVerified
		a.Store.RecordCopy(c, vol.ID, destDir, true)
		a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: true, Path: destDir, Note: "incremental mirror backup"})
		a.Store.UpdateChunk(c)
		return
	}
	// No mirror chunk on this volume yet — create one from the delta (same shape as a
	// first full mirror).
	a.upsertMirrorChunk(archiveID, vol, destDir, deltaRefs, "mirror")
}

// planDeltaPackages plans media-sized PLANNED packages from an explicit delta file
// set (for tape/optical incrementals), grouped by source root and batched by the
// volume's payload budget — the same shape Plan produces, but over the delta rather
// than the whole archive. The packages are then built & written by the normal engine.
func (a *App) planDeltaPackages(coll *Collection, files []*File, vol *Volume, progress func(float64, string)) ([]*Chunk, error) {
	cfg := a.LoadConfig()
	par2 := a.effectiveIntegrity(coll.ID).Par2Redundancy
	mediaKind := nonEmpty(vol.Kind, "LTO8")
	target := MediaPresets[mediaKind]
	if target <= 0 {
		target = MediaPresets["LTO8"]
	}
	payload := int64(float64(target) / (1 + float64(par2)/100) * 0.985)

	folders := map[int]string{}
	for _, f := range a.Store.FoldersOf(coll.ID) {
		folders[f.ID] = f.Path
	}
	byRoot := map[int][]*File{}
	var big []*File
	for _, f := range files {
		if f.SizeBytes > payload {
			big = append(big, f)
			continue
		}
		byRoot[f.FolderID] = append(byRoot[f.FolderID], f)
	}
	safe := safeName(coll.Name)
	if safe == "" {
		safe = "COLL"
	}
	seq := len(a.Store.Chunks(coll.ID))
	var out []*Chunk
	fids := make([]int, 0, len(byRoot))
	for fid := range byRoot {
		fids = append(fids, fid)
	}
	sort.Ints(fids)
	for _, fid := range fids {
		group := byRoot[fid]
		sort.Slice(group, func(i, j int) bool { return group[i].RelPath < group[j].RelPath })
		var batch []ChunkFileRef
		var size int64
		flush := func() {
			if len(batch) == 0 {
				return
			}
			seq++
			c := a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: fmt.Sprintf("%s-INC%04d", safe, seq),
				Status: "PLANNED", MediaKind: mediaKind, TargetBytes: target, DataBytes: size,
				FileCount: len(batch), SrcRoot: folders[fid], HashAlg: "SHA256", Par2: par2,
				Files: append([]ChunkFileRef{}, batch...)})
			out = append(out, c)
			batch, size = nil, 0
		}
		for _, f := range group {
			if len(batch) > 0 && size+f.SizeBytes > payload {
				flush()
			}
			batch = append(batch, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash})
			size += f.SizeBytes
		}
		flush()
	}
	sort.Slice(big, func(i, j int) bool { return big[i].RelPath < big[j].RelPath })
	for _, f := range big {
		seq++
		segs := planSegments(f.SizeBytes+4096, target)
		c := a.Store.AddChunk(Chunk{CollectionID: coll.ID, Name: fmt.Sprintf("%s-INC%04d", safe, seq),
			Status: "PLANNED", MediaKind: mediaKind, TargetBytes: target, DataBytes: f.SizeBytes,
			FileCount: 1, SrcRoot: folders[f.FolderID], HashAlg: "SHA256", Par2: par2, Spanned: true, Segments: segs,
			Files: []ChunkFileRef{{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash}}})
		out = append(out, c)
	}
	_ = cfg
	return out, nil
}
