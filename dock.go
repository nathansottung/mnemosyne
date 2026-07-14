package main

// dock.go — a guided, resumable mode for ingesting a stack of legacy backup
// drives through a dock, one at a time. The operator picks the Archive(s) to
// reconcile against; the view watches for a newly-inserted drive; a single job
// chain then does everything hands-off:
//
//   register/match the Volume by serial (idempotent across sessions) → capture
//   its physical identity → MIRROR-ADOPT it (hash every loose file, match by
//   CONTENT against the selected archives' cataloged source hashes) → write an
//   inventory sidecar + catalog snapshot ONTO THE DRIVE → record per-drive
//   results → "DONE — safe to eject. Insert the next drive."
//
// It is strictly READ-ONLY toward sources: the archive folders on the NAS are
// only ever hashed for comparison. The only writes are to the catalog and, via
// the AssertOutsideSources guard, the docked drive's own sidecar — never a
// source path. A drive is identified by its physical serial, so re-inserting one
// already processed is recognized and offered as a re-verify, not a re-adopt.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// dockSidecarDir is the single folder the dock writes onto each drive. The
// mirror-adoption walk skips it so the tool never re-ingests its own output.
const dockSidecarDir = "MNEMOSYNE_DOCK"

// MountInfo is one mounted volume the dock watcher can see (platform-resolved).
type MountInfo struct {
	Path      string `json:"path"`
	Label     string `json:"label,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// DockCandidate is a newly-appeared drive offered for ingest: where it mounted,
// what the OS says it is, and whether we've seen this physical drive before.
type DockCandidate struct {
	Path               string         `json:"path"`
	Label              string         `json:"label,omitempty"`
	SizeBytes          int64          `json:"size_bytes,omitempty"`
	Serial             string         `json:"serial,omitempty"`
	Model              string         `json:"model,omitempty"`
	VolumeID           int            `json:"volume_id,omitempty"`
	AlreadyProcessed   bool           `json:"already_processed"`    // known drive with mirror data for these archives → offer re-verify
	ProcessedInSession bool           `json:"processed_in_session"` // already ingested in THIS session
	PlanWork           []PlanWorkItem `json:"plan_work,omitempty"`  // compiled plans this drive can advance
}

// ---- session lifecycle -------------------------------------------------

// StartDockSession opens a session reconciling against archiveIDs, snapshotting
// the mounts present now so the watcher can diff for newly-inserted drives.
func (a *App) StartDockSession(archiveIDs []int) (*DockSession, error) {
	if len(archiveIDs) == 0 {
		return nil, fmt.Errorf("choose at least one Archive to reconcile the drives against")
	}
	for _, id := range archiveIDs {
		if a.Store.Collection(id) == nil {
			return nil, fmt.Errorf("archive %d not found", id)
		}
	}
	var baseline []string
	for _, m := range enumerateMounts() {
		baseline = append(baseline, m.Path)
	}
	ds := a.Store.AddDockSession(&DockSession{ArchiveIDs: archiveIDs, Baseline: baseline, Status: "ACTIVE"})
	a.Store.Log("dock", fmt.Sprintf("session %d started for archive(s) %v", ds.ID, archiveIDs))
	return ds, nil
}

// CloseDockSession marks a session done (the operator finished the stack).
func (a *App) CloseDockSession(id int) (*DockSession, error) {
	ds := a.Store.DockSession(id)
	if ds == nil {
		return nil, fmt.Errorf("dock session %d not found", id)
	}
	ds.Status = "CLOSED"
	a.Store.UpdateDockSession(ds)
	a.Store.Log("dock", fmt.Sprintf("session %d closed (%d drive(s))", id, len(ds.Drives)))
	return ds, nil
}

// DockCandidates returns the drives that have appeared since the session started
// (current mounts minus the baseline), each annotated with its resolved identity
// and whether it has been seen before.
func (a *App) DockCandidates(sessionID int) ([]DockCandidate, error) {
	ds := a.Store.DockSession(sessionID)
	if ds == nil {
		return nil, fmt.Errorf("dock session %d not found", sessionID)
	}
	base := map[string]bool{}
	for _, p := range ds.Baseline {
		base[p] = true
	}
	inSession := map[int]bool{}
	for _, d := range ds.Drives {
		inSession[d.VolumeID] = true
	}
	out := []DockCandidate{}
	for _, m := range enumerateMounts() {
		if base[m.Path] {
			continue // present at session start — not a freshly-docked drive
		}
		c := DockCandidate{Path: m.Path, Label: m.Label, SizeBytes: m.SizeBytes}
		if id, err := resolveDeviceIdentity(m.Path); err == nil {
			c.Serial, c.Model = id.Serial, id.Model
			if c.SizeBytes == 0 {
				c.SizeBytes = id.SizeBytes
			}
		}
		if v := a.Store.VolumeBySerial(c.Serial); v != nil {
			c.VolumeID = v.ID
			c.AlreadyProcessed = a.volumeHasMirror(v.ID, ds.ArchiveIDs)
			c.ProcessedInSession = inSession[v.ID]
			c.PlanWork = a.plansPendingForVolume(v.ID) // offer "Execute plan work from this drive"
		}
		out = append(out, c)
	}
	return out, nil
}

// ---- ingest ------------------------------------------------------------

// IngestDrive is the hands-off job chain for one docked drive. serial/label may
// be supplied by the watcher (or a test); a blank serial is resolved from the
// live device. mode "" auto-selects adopt vs re-verify (a drive already holding
// mirror data for these archives re-verifies).
//
// confirm gates the long read behind the drive-mortality check: if the SMART
// snapshot raises a failure advisory (reallocated/pending sectors &c.) and confirm
// is false, IngestDrive returns EARLY with needs_confirmation=true and the SMART
// detail — BEFORE hashing the whole drive — so the operator can copy critical data
// off a dying disk first. Re-call with confirm=true to inventory it anyway (the
// read is non-destructive; nothing is ever written to the drive).
func (a *App) IngestDrive(sessionID int, mountPath, serial, label, mode, level string, confirm bool, progress func(float64, string)) (map[string]any, error) {
	ds := a.Store.DockSession(sessionID)
	if ds == nil {
		return nil, fmt.Errorf("dock session %d not found", sessionID)
	}
	if ds.Status != "ACTIVE" {
		return nil, fmt.Errorf("dock session %d is %s — start a new session", sessionID, ds.Status)
	}
	if strings.TrimSpace(mountPath) == "" {
		return nil, fmt.Errorf("mount_path (the docked drive) required")
	}
	if fi, err := os.Stat(mountPath); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("cannot read docked drive %s", mountPath)
	}
	// Batch catalog writes across the ingest (idempotent: matched by content hash).
	a.Store.BeginBatch()
	defer a.Store.EndBatch()

	progress(0.02, "identifying drive")
	// An explicit serial (from the watcher, which already resolved the candidate,
	// or a caller) is authoritative — we trust it and skip re-probing the device.
	// Otherwise we resolve the drive's identity live from the mount.
	explicitSerial := strings.TrimSpace(serial) != ""
	serial = strings.TrimSpace(serial)
	if !explicitSerial {
		if id, err := resolveDeviceIdentity(mountPath); err == nil {
			serial = id.Serial
		}
	}
	// Match or register the Volume — idempotent by serial across sessions.
	var vol *Volume
	if serial != "" {
		vol = a.Store.VolumeBySerial(serial)
	}
	reinserted := vol != nil
	if vol == nil {
		vol = a.Store.AddVolume(Volume{Label: nonEmpty(label, mountLabel(mountPath)), Kind: "HDD",
			Serial: serial, Notes: "ingested via dock"})
	}
	// Capture / refresh the live physical identity (best-effort, non-fatal). Only
	// when the serial wasn't supplied — a supplied serial means identity is
	// already settled and re-probing a stand-in mount must not clobber it.
	if !explicitSerial {
		if _, changed := a.resolveVolumeIdentity(vol, mountPath); changed {
			a.Store.UpdateVolume(vol)
		}
	}

	// Drive-mortality snapshot — a COMPLEMENT to the content verification below,
	// never a substitute. Best-effort and silent: smartctl absent, a masked USB
	// bridge, or a permission wall just records nothing and never fails the
	// ingest. Snapshots accrue in the volume's history so trends show across
	// dock sessions.
	var health *SmartSnapshot
	if snap, herr := a.VolumeHealth(vol, mountPath); herr == nil {
		health = snap
	}

	// Pre-flight failure gate — BEFORE the long full-drive read. If SMART is
	// shouting imminent failure (reallocated/pending sectors, FAILING self-test,
	// NVMe wear-out), stop and let the operator rescue critical data off the drive
	// first. This is advisory only: the inventory read is non-destructive and never
	// writes to the drive, so confirm=true proceeds normally. A drive with no SMART
	// (smartctl absent / USB bridge) has health==nil and is never gated.
	if health != nil && health.Advisory && !confirm {
		a.Store.Log("dock", fmt.Sprintf("session %d: %s SMART advisory — %s (awaiting operator confirmation before inventory)",
			ds.ID, vol.Label, health.AdvisoryWhy))
		return map[string]any{
			"needs_confirmation": true,
			"volume_id":          vol.ID, "serial": vol.Serial, "label": vol.Label,
			"health": health,
			"message": "Warning: This drive may be failing — " + health.AdvisoryWhy + ". " +
				"Copy critical data off it first? Or continue the inventory anyway — the read is non-destructive and never writes to the drive.",
		}, nil
	}

	// A drive we already hold mirror data for is a RE-VERIFY, not a re-adopt.
	hasMirror := a.volumeHasMirror(vol.ID, ds.ArchiveIDs)
	effMode := "adopt"
	if strings.EqualFold(mode, "reverify") || (mode == "" && reinserted && hasMirror) {
		effMode = "reverify"
	}

	// A re-verify may run at a cheaper level (A census / C sample) — a path-based
	// check of the known mirror instead of the full content re-hash. Adoption and
	// level B always do the full content match.
	var drive *DockDrive
	var err error
	if effMode == "reverify" && normLevel(level) != VerifyB {
		drive, err = a.dockReverifyAtLevel(ds, mountPath, vol, normLevel(level), progress)
	} else {
		drive, err = a.mirrorAdopt(ds, mountPath, vol, effMode, health, progress)
	}
	if err != nil {
		return nil, err
	}
	drive.Letter = mountPath
	a.Store.RecordDockDrive(ds, *drive)
	a.Store.Log("dock", fmt.Sprintf("session %d: %s %s (%d matched, %d historical, %d unreadable)",
		ds.ID, effMode, vol.Label, drive.Matched, drive.Historical, drive.Unreadable))

	progress(1.0, "DONE — safe to eject")
	out := map[string]any{
		"volume_id": vol.ID, "serial": vol.Serial, "label": vol.Label, "mode": effMode,
		"reinserted": reinserted, "matched": drive.Matched, "matched_bytes": drive.MatchedBytes,
		"historical": drive.Historical, "other": drive.Other, "unreadable": drive.Unreadable,
		"note": drive.Note, "health": health,
		"message":  "DONE — safe to eject. Insert the next drive.",
		"coverage": a.archiveCoverage(ds.ArchiveIDs),
	}
	// The per-drive ingest report: role breakdown, duplicates vs. already-cataloged
	// drives, folder-overlap, and drive-mirror detection with the location verdict.
	// Built from the snapshot mirrorAdopt just stored (nil on the cheap A/C re-verify
	// path, which does not re-walk the drive).
	if snap := a.Store.VolumeSnapshot(vol.ID); snap != nil {
		out["report"] = a.driveReport(snap)
	}
	return out, nil
}

// mirrorAdopt hashes EVERY loose file on the drive in one read pass and does two
// things with each: (1) records it in the drive's SNAPSHOT — the complete offline
// inventory (tree, size, mtime, SHA-256 + BLAKE3, workflow role, best-effort EXIF
// capture time + camera serial) that lets the catalog describe the drive unplugged;
// and (2) matches it by CONTENT against the selected archives, recording matches as
// an ADOPTED-VERIFIED mirror package per archive (with a verified copy on this
// volume). It does NOT write anything onto the drive: adopted media are read-only,
// so the inventory lives ONLY in the catalog (the snapshot), unlike tool-written
// media which still carry a sidecar (see docs/ARCHITECTURE.md on this asymmetry).
func (a *App) mirrorAdopt(ds *DockSession, mountPath string, vol *Volume, mode string, health *SmartSnapshot, progress func(float64, string)) (*DockDrive, error) {
	// Source-safety: refuse to treat a registered source folder as a docked drive.
	// (We no longer write to the drive at all, but adopting a NAS source AS a drive
	// would double-count it and is never intended — read-only or not.)
	if err := a.Store.AssertOutsideSources(mountPath); err != nil {
		return nil, err
	}
	reg := a.formatRegistry() // extension → sustainability tier + workflow role

	// Content indexes from the selected archives. The dock first-pass matches by
	// BLAKE3 (the fast hot-loop hash) when the catalog has it, falling back to
	// SHA-256 for files scanned before BLAKE3 was recorded. Both keys point at the
	// same File, so a drive file need only match on one.
	currentByHash := map[string][]*File{}   // current source file(s) by SHA-256
	currentByBlake3 := map[string][]*File{} // ...and by BLAKE3 (fast path)
	archiveOfFile := map[int]int{}          // fileID -> archiveID
	histHashes := map[string]bool{}         // previously-packaged versions no longer current
	for _, aid := range ds.ArchiveIDs {
		for _, f := range a.Store.FilesOf(aid) {
			if f.Hash != "" {
				currentByHash[f.Hash] = append(currentByHash[f.Hash], f)
			}
			if f.Blake3 != "" {
				currentByBlake3[f.Blake3] = append(currentByBlake3[f.Blake3], f)
			}
			archiveOfFile[f.ID] = aid
		}
	}
	for _, c := range a.Store.Chunks(0) {
		if !containsInt(ds.ArchiveIDs, c.CollectionID) {
			continue
		}
		for _, ref := range c.Files {
			if ref.Hash == "" {
				continue
			}
			if _, cur := currentByHash[ref.Hash]; !cur {
				histHashes[ref.Hash] = true
			}
		}
	}

	// Walk the drive (skipping our own sidecar dir and OS junk), collect files.
	progress(0.06, "scanning drive")
	var paths []string
	_ = filepath.WalkDir(mountPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree — keep going
		}
		if d.IsDir() {
			if skipDockDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, p)
		return nil
	})

	// Hash all files in parallel; for EACH file both (a) record a snapshot entry
	// (role + best-effort EXIF) and (b) classify it by content against the archives.
	var mu sync.Mutex
	matchedByArchive := map[int]map[int]*File{} // archiveID -> fileID -> File
	snapFiles := make([]SnapFile, 0, len(paths))
	var matchedBytes int64
	var hist, other int
	var hashed int64
	total := len(paths)
	if total == 0 {
		total = 1
	}
	parallelHash(paths,
		func(done int) {
			progress(0.06+float64(done)/float64(total)*0.74, progStats(0, 0, int64(done), int64(len(paths)), "hashing drive files"))
		},
		func(p, sha, b3 string, size int64, mtime time.Time) {
			atomic.AddInt64(&hashed, 1)
			// Snapshot entry — classify by role, and for still-image roles pull the
			// EXIF capture time + camera body serial (bounded header read, outside the
			// lock; unparseable = empty, never an error).
			rel := driveRel(mountPath, p)
			role, crit := classifyRole(reg, rel)
			sf := SnapFile{RelPath: rel, SizeBytes: size, ModTime: mtime, Hash: sha, Blake3: b3, Role: role, Critical: crit}
			sf.ShotAt, sf.CameraSerial = a.extractMediaMeta(p, role)
			mu.Lock()
			defer mu.Unlock()
			snapFiles = append(snapFiles, sf)
			// BLAKE3 first (fast path), then SHA-256 for legacy catalog entries.
			fs, ok := currentByBlake3[b3]
			if !ok {
				fs, ok = currentByHash[sha]
			}
			if ok {
				for _, f := range fs {
					aid := archiveOfFile[f.ID]
					if matchedByArchive[aid] == nil {
						matchedByArchive[aid] = map[int]*File{}
					}
					if _, seen := matchedByArchive[aid][f.ID]; !seen {
						matchedByArchive[aid][f.ID] = f
						matchedBytes += f.SizeBytes
					}
					// Backfill media metadata onto the matched archive file if a prior
					// scan/adopt didn't capture it (e.g. cataloged before this feature).
					if f.ShotAt.IsZero() && !sf.ShotAt.IsZero() {
						f.ShotAt = sf.ShotAt
					}
					if f.Role == "" && sf.Role != "" {
						f.Role = sf.Role
					}
					if f.CameraSerial == "" && sf.CameraSerial != "" {
						f.CameraSerial = sf.CameraSerial
					}
				}
			} else if histHashes[sha] {
				hist++
			} else {
				other++
			}
		})
	unreadable := len(paths) - int(hashed)

	// Record the matches as a verified mirror package per archive.
	progress(0.84, "recording matches")
	totalMatched := 0
	for _, aid := range ds.ArchiveIDs {
		fm := matchedByArchive[aid]
		if len(fm) == 0 {
			continue
		}
		refs := make([]ChunkFileRef, 0, len(fm))
		for _, f := range fm {
			refs = append(refs, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash})
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].RelPath < refs[j].RelPath })
		a.upsertMirrorChunk(aid, vol, mountPath, refs, mode)
		totalMatched += len(fm)
	}

	d := &DockDrive{VolumeID: vol.ID, Serial: vol.Serial, Label: vol.Label, Mode: mode,
		Matched: totalMatched, MatchedBytes: matchedBytes, Historical: hist, Other: other, Unreadable: unreadable}

	// Store the drive's SNAPSHOT — the full offline inventory. NO sidecar is written
	// to the drive: adopted media are read-only, so the inventory lives in the
	// catalog alone (see this function's doc + docs/ARCHITECTURE.md).
	progress(0.92, "recording drive snapshot")
	a.Store.PutVolumeSnapshot(a.buildSnapshot(ds, vol, health, snapFiles, unreadable))

	if mode == "reverify" {
		if d.Note != "" {
			d.Note += " · "
		}
		d.Note += "re-verified (drive recognized by serial)"
	}
	return d, nil
}

// buildSnapshot assembles a VolumeSnapshot from the walk's per-file records plus
// the drive's identity and SMART state, with role rollups denormalized for the
// offline view. Files are sorted by path for a stable, browsable tree.
func (a *App) buildSnapshot(ds *DockSession, vol *Volume, health *SmartSnapshot, files []SnapFile, unreadable int) *VolumeSnapshot {
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	snap := &VolumeSnapshot{
		VolumeID: vol.ID, CapturedAt: time.Now().UTC(), SessionID: ds.ID,
		Label: vol.Label, Serial: vol.Serial, Model: vol.Model, DeviceSize: vol.DeviceSize,
		LocationID: vol.LocationID, Smart: health, Files: files, Unreadable: unreadable,
		RoleFiles: map[string]int{}, RoleBytes: map[string]int64{},
	}
	for _, f := range files {
		snap.TotalFiles++
		snap.TotalBytes += f.SizeBytes
		role := f.Role
		if role == "" {
			role = RoleOther
		}
		snap.RoleFiles[role]++
		snap.RoleBytes[role] += f.SizeBytes
	}
	return snap
}

// driveRel returns p as a forward-slashed path relative to the drive mount root,
// so snapshot paths are stable and browsable regardless of mount letter.
func driveRel(mountPath, p string) string {
	if rel, err := filepath.Rel(mountPath, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

// dockReverifyAtLevel does a cheap path-based re-verify of the mirror(s) already
// recorded for this volume at level A or C — checking each cataloged file at
// mountPath/relpath, skipping the full content re-hash. Advisory only (never
// satisfies COMPLETE or refreshes verify-due); a level-B re-verify uses the full
// content-match path (mirrorAdopt) instead.
func (a *App) dockReverifyAtLevel(ds *DockSession, mountPath string, vol *Volume, level string, progress func(float64, string)) (*DockDrive, error) {
	progress(0.1, "re-verifying ("+levelName(level)+")")
	matched, bad := 0, 0
	for _, aid := range ds.ArchiveIDs {
		for _, c := range a.Store.Chunks(aid) {
			if !c.Mirror {
				continue
			}
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
			ok, checked, b, firstBad := verifyMirrorChunk(c, mountPath, level)
			matched += checked - b
			bad += b
			now := time.Now().UTC()
			note := fmt.Sprintf("dock re-verify (%s): %d/%d ok", levelTag(level), checked-b, checked)
			if !ok {
				note += " · first bad: " + firstBad
			}
			a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: ok, Path: mountPath, Note: note, Level: level, Advisory: true})
			a.Store.UpdateCopyVerifyLevel(c, mountPath, ok, level)
		}
	}
	note := fmt.Sprintf("re-verified at level %s (advisory — only full/B satisfies protection)", level)
	if bad > 0 {
		note += fmt.Sprintf(" · %d file(s) failed the %s check", bad, levelName(level))
	}
	return &DockDrive{VolumeID: vol.ID, Serial: vol.Serial, Label: vol.Label, Mode: "reverify",
		Matched: matched, Note: note}, nil
}

// upsertMirrorChunk creates or refreshes the ADOPTED-VERIFIED mirror package for
// (archive, volume): the drive's content-matched files, with a verified copy on
// the volume. Idempotent — a re-ingest updates the same package in place.
func (a *App) upsertMirrorChunk(archiveID int, vol *Volume, mountPath string, refs []ChunkFileRef, mode string) *Chunk {
	var sum int64
	for _, r := range refs {
		sum += r.SizeBytes
	}
	now := time.Now().UTC()
	ok := true
	note := "dock mirror adopt"
	switch mode {
	case "reverify":
		note = "dock re-verify"
	case "mirror":
		note = "mirror backup written (copy-then-verified)"
	}
	for _, c := range a.Store.Chunks(archiveID) {
		if !c.Mirror {
			continue
		}
		onVol := false
		for _, cp := range c.Copies {
			if cp.VolumeID == vol.ID && !cp.Superseded {
				onVol = true
			}
		}
		if !onVol {
			continue
		}
		c.Files, c.FileCount, c.EncBytes, c.DataBytes = refs, len(refs), sum, sum
		c.WrittenDest, c.VerifiedAt, c.VerifyOK, c.Status = mountPath, &now, &ok, StatusAdoptedVerified
		a.Store.RecordCopy(c, vol.ID, mountPath, true)
		a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: true, Path: mountPath, Note: note})
		a.Store.UpdateChunk(c)
		return c
	}
	coll := a.Store.Collection(archiveID)
	safe := "ARCHIVE"
	if coll != nil {
		safe = safeName(coll.Name)
	}
	c := a.Store.AddChunk(Chunk{CollectionID: archiveID,
		Name: fmt.Sprintf("MIRROR-%s-V%d", safe, vol.ID), Status: StatusAdoptedVerified,
		MediaKind: nonEmpty(vol.Kind, "HDD"), EncBytes: sum, DataBytes: sum, FileCount: len(refs),
		HashAlg: "SHA256", Encrypted: false, Adopted: true, Mirror: true, WrittenDest: mountPath,
		Files: refs, WrittenAt: &now, VerifiedAt: &now, VerifyOK: &ok})
	a.Store.RecordCopy(c, vol.ID, mountPath, true)
	a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: true, Path: mountPath, Note: note})
	return c
}

// volumeHasMirror reports whether the volume already holds mirror data for any
// of the given archives (a current copy on a mirror package).
func (a *App) volumeHasMirror(volumeID int, archiveIDs []int) bool {
	for _, c := range a.Store.Chunks(0) {
		if !c.Mirror || !containsInt(archiveIDs, c.CollectionID) {
			continue
		}
		for _, cp := range c.Copies {
			if cp.VolumeID == volumeID && !cp.Superseded {
				return true
			}
		}
	}
	return false
}

// ---- coverage ----------------------------------------------------------

type CoverageArchive struct {
	ArchiveID    int     `json:"archive_id"`
	Name         string  `json:"name"`
	TotalFiles   int     `json:"total_files"`
	CoveredFiles int     `json:"covered_files"`
	Uncovered    int     `json:"uncovered_files"`
	Pct          float64 `json:"pct"`
	TotalBytes   int64   `json:"total_bytes"`
	CoveredBytes int64   `json:"covered_bytes"`
}

type Coverage struct {
	Archives     []CoverageArchive `json:"archives"`
	TotalFiles   int               `json:"total_files"`
	CoveredFiles int               `json:"covered_files"`
	Uncovered    int               `json:"uncovered_files"`
	Pct          float64           `json:"pct"`
	TotalBytes   int64             `json:"total_bytes"`
	CoveredBytes int64             `json:"covered_bytes"`
}

// archiveCoverage computes, for the selected archives, how many source files now
// have ≥1 verified copy anywhere (any chunk — packaged or mirror — with a
// verified copy). A file counts as covered by fileID or by content hash.
func (a *App) archiveCoverage(archiveIDs []int) Coverage {
	coveredIDs := map[int]bool{}
	coveredHashes := map[string]bool{}
	for _, c := range a.Store.Chunks(0) {
		if c.VerifiedCopyCount() == 0 {
			continue
		}
		for _, ref := range c.Files {
			if ref.FileID > 0 {
				coveredIDs[ref.FileID] = true
			}
			if ref.Hash != "" {
				coveredHashes[ref.Hash] = true
			}
		}
	}
	var cov Coverage
	for _, aid := range archiveIDs {
		coll := a.Store.Collection(aid)
		if coll == nil {
			continue
		}
		ca := CoverageArchive{ArchiveID: aid, Name: coll.Name}
		for _, f := range a.Store.FilesOf(aid) {
			ca.TotalFiles++
			ca.TotalBytes += f.SizeBytes
			if coveredIDs[f.ID] || (f.Hash != "" && coveredHashes[f.Hash]) {
				ca.CoveredFiles++
				ca.CoveredBytes += f.SizeBytes
			}
		}
		ca.Uncovered = ca.TotalFiles - ca.CoveredFiles
		if ca.TotalFiles > 0 {
			ca.Pct = round1(float64(ca.CoveredFiles) / float64(ca.TotalFiles) * 100)
		}
		cov.Archives = append(cov.Archives, ca)
		cov.TotalFiles += ca.TotalFiles
		cov.CoveredFiles += ca.CoveredFiles
		cov.TotalBytes += ca.TotalBytes
		cov.CoveredBytes += ca.CoveredBytes
	}
	cov.Uncovered = cov.TotalFiles - cov.CoveredFiles
	if cov.TotalFiles > 0 {
		cov.Pct = round1(float64(cov.CoveredFiles) / float64(cov.TotalFiles) * 100)
	}
	return cov
}

// ---- session report ----------------------------------------------------
//
// Note the deliberate asymmetry: the dock writes NO inventory sidecar onto an
// adopted drive (adopted media are read-only — the inventory lives in the catalog
// snapshot alone). Tool-WRITTEN media still carry their sidecar at seal time. See
// docs/ARCHITECTURE.md.

// SessionReportMarkdown is the exportable documentation trail: every drive's
// serial/label/contents summary plus running coverage of the selected archives.
func (a *App) SessionReportMarkdown(sessionID int) (string, error) {
	ds := a.Store.DockSession(sessionID)
	if ds == nil {
		return "", fmt.Errorf("dock session %d not found", sessionID)
	}
	cov := a.archiveCoverage(ds.ArchiveIDs)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Dock session %d\n\n", ds.ID))
	b.WriteString(fmt.Sprintf("Started %s · status **%s** · %d drive(s) processed.\n\n",
		ds.StartedAt.Format(time.RFC3339), ds.Status, len(ds.Drives)))

	b.WriteString("## Archives reconciled\n\n")
	for _, ca := range cov.Archives {
		b.WriteString(fmt.Sprintf("- **%s** — %.1f%% covered (%d/%d files with ≥1 verified copy; %d still on 0 copies)\n",
			ca.Name, ca.Pct, ca.CoveredFiles, ca.TotalFiles, ca.Uncovered))
	}
	b.WriteString(fmt.Sprintf("\n**Overall:** %.1f%% — %d of %d files covered, **%d files still on 0 copies**.\n\n",
		cov.Pct, cov.CoveredFiles, cov.TotalFiles, cov.Uncovered))

	b.WriteString("## Drives processed\n\n")
	if len(ds.Drives) == 0 {
		b.WriteString("_None yet._\n\n")
	} else {
		b.WriteString("| # | Label | Serial | Pass | Matched | Matched GB | Historical | Unreadable | Finished |\n")
		b.WriteString("|--:|-------|--------|------|--------:|-----------:|-----------:|-----------:|----------|\n")
		for i, d := range ds.Drives {
			serial := d.Serial
			if serial == "" {
				serial = "—"
			}
			fin := ""
			if d.FinishedAt != nil {
				fin = d.FinishedAt.Format("2006-01-02 15:04")
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %d | %.2f | %d | %d | %s |\n",
				i+1, mdCell(d.Label), mdCell(serial), d.Mode, d.Matched,
				float64(d.MatchedBytes)/1e9, d.Historical, d.Unreadable, fin))
		}
	}
	b.WriteString("\nMnemosyne treated every source folder as READ-ONLY: NAS paths were only hashed for comparison, never written.\n")
	return b.String(), nil
}

// ---- small helpers -----------------------------------------------------

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// mountLabel derives a fallback volume label from a mount path.
func mountLabel(mountPath string) string {
	base := filepath.Base(filepath.Clean(mountPath))
	if base == "" || base == "." || base == string(filepath.Separator) || strings.HasSuffix(base, ":") {
		return "DOCK-DRIVE"
	}
	return base
}

// safeName reduces an archive name to a compact token for a package name.
func safeName(name string) string {
	s := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, name)
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = s[:24]
	}
	if s == "" {
		s = "ARCHIVE"
	}
	return s
}

// skipDockDir lists directory names the drive walk ignores: our own sidecar and
// common OS bookkeeping that is unreadable or irrelevant.
func skipDockDir(name string) bool {
	switch name {
	case dockSidecarDir, "System Volume Information", "$RECYCLE.BIN", ".Trashes", ".Spotlight-V100", ".fseventsd":
		return true
	}
	return false
}
