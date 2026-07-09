package main

// conflicts.go — "same logical file, different bytes," resolved WITHOUT a source
// of truth. When a sourceless archive's union gains two files that are the same
// logical file but disagree on content, we must decide whether that is legitimate
// (two cameras at one wedding) or a real problem (a corrupted or mistakenly-edited
// duplicate). This file classifies each collision and, for the true ones, opens a
// human review item. Nothing is ever auto-preferred and nothing is discarded:
// resolutions feed the existing per-file version-retention history (File.Versions).
//
// Classes (from EXIF, best-effort):
//   (a) same filename+timestamp, DIFFERENT camera body serial → second shooter.
//       NOT a conflict: both kept; the plan disambiguates their destination names.
//   (b) same capture metadata AND same camera serial, different hash → TRUE conflict.
//   (c) same path/name, no EXIF to arbitrate, different hash → TRUE conflict.

import (
	"path"
	"sort"
	"strings"
	"time"
)

// DetectedConflict is one true collision found by a scan, before it is reconciled
// against the stored review queue. Signature makes reconciliation idempotent.
type DetectedConflict struct {
	Class     string
	Key       string
	RelPath   string
	Role      string
	RawAlert  bool
	Signature string
	FileIDs   []int
}

// ConflictScan summarizes a detection pass for the ingest report.
type ConflictScan struct {
	New           int `json:"new"`            // newly-opened true conflicts
	Open          int `json:"open"`           // total open conflicts in the archive after this pass
	TrueConflicts int `json:"true_conflicts"` // true collisions detected this pass (b + c)
	SecondShooter int `json:"second_shooter"` // class-(a) groups auto-passed (legitimate multi-body)
}

// DetectConflicts scans an archive's files, classifies collisions, and reconciles
// true conflicts (classes b & c) into the review queue. Class-(a) second-shooter
// groups are counted for the report but never queued. Idempotent.
func (a *App) DetectConflicts(collectionID int) ConflictScan {
	files := a.Store.FilesOf(collectionID)

	// Group by logical identity: EXIF files by filename+capture-time (so a frame that
	// landed on two drives lines up regardless of folder), the rest by path/name.
	exif := map[string][]*File{}
	byPath := map[string][]*File{}
	for _, f := range files {
		if !f.ShotAt.IsZero() {
			k := strings.ToLower(path.Base(f.RelPath)) + "|" + f.ShotAt.UTC().Format(time.RFC3339)
			exif[k] = append(exif[k], f)
		} else {
			byPath[normPath(f.RelPath)] = append(byPath[normPath(f.RelPath)], f)
		}
	}

	var detected []DetectedConflict
	secondShooter := 0
	for k, members := range exif {
		if distinctHashes(members) < 2 {
			continue
		}
		// Partition by camera body. Different bodies = legitimately different frames
		// (second shooter); the SAME body producing two hashes is a true conflict.
		bySerial := map[string][]*File{}
		serials := map[string]bool{}
		for _, f := range members {
			bySerial[f.CameraSerial] = append(bySerial[f.CameraSerial], f)
			if f.CameraSerial != "" {
				serials[f.CameraSerial] = true
			}
		}
		if len(serials) >= 2 {
			secondShooter++ // (a): multiple distinct bodies — auto-pass, note in report
		}
		for _, sub := range bySerial {
			if distinctHashes(sub) >= 2 {
				detected = append(detected, buildDetected(collectionID, ClassSameMeta, k, sub))
			}
		}
	}
	for k, members := range byPath {
		if distinctHashes(members) >= 2 {
			detected = append(detected, buildDetected(collectionID, ClassNoEXIF, k, members))
		}
	}

	added, open := a.Store.ReconcileConflicts(collectionID, detected)
	return ConflictScan{New: added, Open: open, TrueConflicts: len(detected), SecondShooter: secondShooter}
}

// buildDetected assembles a DetectedConflict from a same-logical-file group: one
// representative file per distinct hash, a stable signature, and a RAW alert if any
// of the disagreeing files is a camera raw original (which should never differ).
func buildDetected(collectionID int, class, key string, members []*File) DetectedConflict {
	// One representative File per distinct hash, ordered by path for stability.
	seen := map[string]*File{}
	for _, f := range members {
		if f.Hash == "" {
			continue
		}
		if _, ok := seen[f.Hash]; !ok {
			seen[f.Hash] = f
		}
	}
	reps := make([]*File, 0, len(seen))
	for _, f := range seen {
		reps = append(reps, f)
	}
	sort.Slice(reps, func(i, j int) bool {
		if reps[i].RelPath != reps[j].RelPath {
			return reps[i].RelPath < reps[j].RelPath
		}
		return reps[i].Hash < reps[j].Hash
	})
	ids := make([]int, len(reps))
	hashes := make([]string, len(reps))
	role, raw := "", false
	for i, f := range reps {
		ids[i] = f.ID
		hashes[i] = f.Hash
		if f.Role == RoleOriginals {
			raw = true
		}
		if role == "" || f.Role == RoleOriginals {
			role = f.Role
		}
	}
	sort.Strings(hashes)
	rel := ""
	if len(reps) > 0 {
		rel = reps[0].RelPath
	}
	sig := itoaSafe(collectionID) + "|" + key + "|" + strings.Join(hashes, ",")
	return DetectedConflict{Class: class, Key: key, RelPath: rel, Role: role, RawAlert: raw,
		Signature: sig, FileIDs: ids}
}

// distinctHashes counts the distinct non-empty content hashes in a file group.
func distinctHashes(files []*File) int {
	set := map[string]bool{}
	for _, f := range files {
		if f.Hash != "" {
			set[f.Hash] = true
		}
	}
	return len(set)
}

// ---- store: reconcile, query, resolve ----------------------------------

// ReconcileConflicts folds a detection pass into the stored review queue: new
// signatures open a conflict, still-present OPEN ones refresh, and signatures that
// vanished (files removed/resolved) drop their OPEN rows. RESOLVED conflicts are
// never re-opened — the human decision stands. Returns (newly opened, open total).
func (s *Store) ReconcileConflicts(collectionID int, detected []DetectedConflict) (added, openTotal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	bySig := map[string]*Conflict{}
	for _, c := range s.c.Conflicts {
		if c.CollectionID == collectionID {
			bySig[c.Signature] = c
		}
	}
	detectedSigs := map[string]bool{}
	for _, d := range detected {
		detectedSigs[d.Signature] = true
		if ex := bySig[d.Signature]; ex != nil {
			if ex.Status == ConflictOpen { // refresh live details; leave RESOLVED alone
				ex.FileIDs, ex.RelPath, ex.Role, ex.RawAlert, ex.Class = d.FileIDs, d.RelPath, d.Role, d.RawAlert, d.Class
			}
			continue
		}
		s.c.Conflicts = append(s.c.Conflicts, &Conflict{
			ID: s.next("conflict"), CollectionID: collectionID, Class: d.Class, Key: d.Key,
			RelPath: d.RelPath, Role: d.Role, RawAlert: d.RawAlert, Signature: d.Signature,
			FileIDs: d.FileIDs, Status: ConflictOpen, CreatedAt: now,
		})
		added++
	}
	// Drop OPEN conflicts in this collection that no longer reproduce (files changed).
	kept := s.c.Conflicts[:0]
	for _, c := range s.c.Conflicts {
		if c.CollectionID == collectionID && c.Status == ConflictOpen && !detectedSigs[c.Signature] {
			continue
		}
		kept = append(kept, c)
	}
	s.c.Conflicts = kept
	for _, c := range s.c.Conflicts {
		if c.CollectionID == collectionID && c.Status == ConflictOpen {
			openTotal++
		}
	}
	_ = s.save()
	return added, openTotal
}

// OpenConflictCount returns the number of unresolved conflicts (collectionID 0 =
// every archive) — the gate the plan checks before compiling placements.
func (s *Store) OpenConflictCount(collectionID int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.c.Conflicts {
		if c.Status == ConflictOpen && (collectionID == 0 || c.CollectionID == collectionID) {
			n++
		}
	}
	return n
}

// Conflict returns one conflict by ID (live record), or nil.
func (s *Store) Conflict(id int) *Conflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.c.Conflicts {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// ResolveConflict records a human decision. CANONICAL folds every non-canonical
// version into the winner's retained history (File.Versions) and removes the loser
// File rows — content stays findable by hash in whatever package holds it, so
// nothing is discarded. KEEP-BOTH leaves both files independent (the plan renames
// them on placement). Idempotent-safe: an already-resolved conflict is left alone.
func (s *Store) ResolveConflict(id int, resolution string, canonicalFileID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c *Conflict
	for _, x := range s.c.Conflicts {
		if x.ID == id {
			c = x
			break
		}
	}
	if c == nil {
		return errString("conflict not found")
	}
	if c.Status == ConflictResolved {
		return nil
	}
	now := time.Now().UTC()
	switch resolution {
	case ResolveKeepBoth:
		c.Resolution = ResolveKeepBoth
	case ResolveCanonical:
		if canonicalFileID == 0 && len(c.FileIDs) > 0 {
			canonicalFileID = c.FileIDs[0]
		}
		var canonical *File
		for _, f := range s.c.Files {
			if f.ID == canonicalFileID {
				canonical = f
				break
			}
		}
		if canonical == nil {
			return errString("canonical file not found in this conflict")
		}
		remove := map[int]bool{}
		for _, fid := range c.FileIDs {
			if fid == canonicalFileID {
				continue
			}
			for _, f := range s.c.Files {
				if f.ID != fid {
					continue
				}
				// Retain the loser's content as an alternate version of the winner.
				canonical.Versions = append(canonical.Versions, FileVersion{
					Hash: f.Hash, HashAlg: f.HashAlg, SizeBytes: f.SizeBytes,
					ModTime: f.ModTime, FirstSeen: f.FirstSeen, SupersededAt: now,
				})
				remove[fid] = true
			}
		}
		if len(remove) > 0 {
			kept := s.c.Files[:0]
			for _, f := range s.c.Files {
				if !remove[f.ID] {
					kept = append(kept, f)
				}
			}
			s.c.Files = kept
			s.fileIdx = nil // rebuilt lazily; union rows shifted
		}
		c.Resolution, c.Canonical = ResolveCanonical, canonicalFileID
	default:
		return errString("resolution must be CANONICAL or KEEP-BOTH")
	}
	c.Status, c.ResolvedAt = ConflictResolved, &now
	_ = s.save()
	return nil
}

// ---- store: review-queue views -----------------------------------------

// ConflictFileView is one side of a conflict: the file's identifying facts plus
// where its bytes physically live (drive + location), for the side-by-side review.
type ConflictFileView struct {
	FileID       int       `json:"file_id"`
	RelPath      string    `json:"rel_path"`
	SizeBytes    int64     `json:"size_bytes"`
	ModTime      time.Time `json:"mtime,omitempty"`
	HashPrefix   string    `json:"hash_prefix"`
	Role         string    `json:"role,omitempty"`
	ShotAt       time.Time `json:"shot_at,omitempty"`
	CameraSerial string    `json:"camera_serial,omitempty"`
	Locations    []string  `json:"locations,omitempty"`
}

// ConflictView is a review-queue row: the conflict plus each disagreeing version
// with its locations, for rendering both sides side by side.
type ConflictView struct {
	*Conflict
	Files []ConflictFileView `json:"versions"`
}

// ConflictViews returns the review queue (collectionID 0 = all; openOnly filters to
// unresolved), each conflict enriched with its versions' facts and physical homes.
func (s *Store) ConflictViews(collectionID int, openOnly bool) []ConflictView {
	s.mu.Lock()
	defer s.mu.Unlock()

	files := map[int]*File{}
	for _, f := range s.c.Files {
		files[f.ID] = f
	}
	vols := map[int]*Volume{}
	for _, v := range s.c.Volumes {
		vols[v.ID] = v
	}
	locs := s.locationsMapLocked()
	// fileID → distinct "DRIVE (Location)" strings from every non-FAILED copy.
	fileLoc := map[int]map[string]bool{}
	for _, ch := range s.c.Chunks {
		if ch.Status == "FAILED" {
			continue
		}
		var homes []string
		for _, cp := range ch.Copies {
			if cp.Superseded {
				continue
			}
			label := "vol#" + itoaSafe(cp.VolumeID)
			locName := ""
			if v := vols[cp.VolumeID]; v != nil {
				if v.Label != "" {
					label = v.Label
				}
				if v.LocationID != 0 {
					if l := locs[v.LocationID]; l != nil {
						locName = l.Name
					}
				}
				if locName == "" {
					locName = strings.TrimSpace(v.Location)
				}
			}
			if locName != "" {
				label += " (" + locName + ")"
			}
			homes = append(homes, label)
		}
		if len(homes) == 0 {
			continue
		}
		for _, cf := range ch.Files {
			m := fileLoc[cf.FileID]
			if m == nil {
				m = map[string]bool{}
				fileLoc[cf.FileID] = m
			}
			for _, h := range homes {
				m[h] = true
			}
		}
	}

	out := []ConflictView{}
	for _, c := range s.c.Conflicts {
		if collectionID != 0 && c.CollectionID != collectionID {
			continue
		}
		if openOnly && c.Status != ConflictOpen {
			continue
		}
		cv := ConflictView{Conflict: c}
		for _, fid := range c.FileIDs {
			f := files[fid]
			if f == nil {
				continue
			}
			hp := f.Hash
			if len(hp) > 12 {
				hp = hp[:12]
			}
			var homes []string
			for h := range fileLoc[fid] {
				homes = append(homes, h)
			}
			sort.Strings(homes)
			cv.Files = append(cv.Files, ConflictFileView{
				FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, ModTime: f.ModTime,
				HashPrefix: hp, Role: f.Role, ShotAt: f.ShotAt, CameraSerial: f.CameraSerial, Locations: homes,
			})
		}
		out = append(out, cv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status == ConflictOpen // open first
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// errString is a tiny error helper so this file needs no fmt import.
type errString string

func (e errString) Error() string { return string(e) }
