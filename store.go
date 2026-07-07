package main

// store.go — the catalog.
//
// v1 deliberately uses a single JSON file with atomic writes instead of
// SQLite: zero CGO, zero external services, trivially inspectable with
// any text editor 30 years from now. The Store interface surface is
// small; swapping in SQLite (modernc.org/sqlite, pure Go) later touches
// only this file.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Collection struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Folder struct {
	ID           int    `json:"id"`
	CollectionID int    `json:"collection_id"`
	Path         string `json:"path"`
}

type File struct {
	ID           int    `json:"id"`
	CollectionID int    `json:"collection_id"`
	FolderID     int    `json:"folder_id"`
	RelPath      string `json:"rel_path"`
	SizeBytes    int64  `json:"size_bytes"`
	HashAlg      string `json:"hash_alg"`
	Hash         string `json:"hash"`
}

type ChunkFileRef struct {
	FileID    int    `json:"file_id"`
	RelPath   string `json:"rel_path"`
	SizeBytes int64  `json:"size_bytes"`
	Hash      string `json:"hash,omitempty"` // backed-up hash at plan time (for drift/reconcile)
}

type Chunk struct {
	ID              int                `json:"id"`
	CollectionID    int                `json:"collection_id"`
	Name            string             `json:"name"`
	Status          string             `json:"status"` // PLANNED BUILDING STAGED WRITING WRITTEN VERIFIED FAILED
	MediaKind       string             `json:"media_kind"`
	TargetBytes     int64              `json:"target_bytes"`
	DataBytes       int64              `json:"data_bytes"`
	EncBytes        int64              `json:"enc_bytes"`
	FileCount       int                `json:"file_count"`
	SrcRoot         string             `json:"src_root"`
	HashAlg         string             `json:"hash_alg"`
	TarHash         string             `json:"tar_hash"`
	EncHash         string             `json:"enc_hash"` // hash of the payload file as written to media (ciphertext when encrypted, tar when not)
	Encrypted       bool               `json:"encrypted"`
	KeyRef          string             `json:"key_ref"`
	PrivateManifest bool               `json:"private_manifest,omitempty"` // medium carries manifest.json.gpg, no plaintext listing
	Par2            int                `json:"par2_redundancy"`
	StagedDir       string             `json:"staged_dir"`
	WrittenDest     string             `json:"written_dest"`
	VerifyOK        *bool              `json:"verify_ok"`
	Error           string             `json:"error"`
	Files           []ChunkFileRef     `json:"files"`
	BuildTimings    map[string]float64 `json:"build_timings,omitempty"` // per-stage seconds: tar, hash, encrypt/stage, par2
	VerifyEvents    []VerifyEvent      `json:"verify_events,omitempty"` // append-only integrity-check log
	RingStats       *RingStats         `json:"ring_stats,omitempty"`    // last write's ring-buffer telemetry
	Spanned         bool               `json:"spanned"`                 // payload split across several media
	Segments        []Segment          `json:"segments,omitempty"`      // one per medium/tape when Spanned
	Copies          []Copy             `json:"copies,omitempty"`        // physical copies of this chunk on registered volumes
	CreatedAt       time.Time          `json:"created_at"`
	WrittenAt       *time.Time         `json:"written_at,omitempty"`
	VerifiedAt      *time.Time         `json:"verified_at,omitempty"`
}

// Segment is one medium's worth of a spanned chunk: a byte range of the
// finished payload (or the par2 set on its own tape when Par2 is set). The
// per-segment Hash is the SHA-256 of exactly those bytes as written, so each
// tape's read-back proves it holds its verified share; concatenating the
// segments in order reproduces the payload (whose whole-file hash is EncHash).
type Segment struct {
	Index    int    `json:"index"`               // 1-based
	Bytes    int64  `json:"bytes"`               // length of this segment
	Hash     string `json:"hash"`                // SHA-256 of the bytes as written (filled at write time)
	Status   string `json:"status"`              // PENDING WRITING WRITTEN VERIFIED FAILED
	Dest     string `json:"dest"`                // base destination mount last used for this segment
	VolumeID int    `json:"volume_id,omitempty"` // registered volume this segment's tape belongs to
	Par2     bool   `json:"par2,omitempty"`      // this "segment" is the par2 set on its own tape
}

// Volume is a physical medium the operator can hold and locate: a tape, a
// drive, a disc. Barcodes come straight off a USB scanner (which types like a
// keyboard). This is the "where do the Smiths' photos physically live?" record.
type Volume struct {
	ID        int       `json:"id"`
	Label     string    `json:"label"`
	Barcode   string    `json:"barcode"`
	Kind      string    `json:"kind"` // TAPE HDD SSD OPTICAL OTHER
	Location  string    `json:"location"`
	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
}

// Copy is one physical instance of a chunk on a registered volume. Two verified
// copies on volumes in different locations is the redundancy goal.
type Copy struct {
	VolumeID       int        `json:"volume_id"`
	Path           string     `json:"path"`
	WrittenAt      *time.Time `json:"written_at,omitempty"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	VerifyOK       *bool      `json:"verify_ok,omitempty"`
}

// VerifyEvent is one integrity check of a chunk's payload against its
// recorded enc_hash — logged by write read-back, media verify, burn verify,
// and verify campaigns. Append-only history; media is never modified.
type VerifyEvent struct {
	At   time.Time `json:"at"`
	OK   bool      `json:"ok"`
	Path string    `json:"path"`
	Note string    `json:"note"` // e.g. "write read-back", "media verify", "burn verify", "campaign"
}

// UnmarshalJSON defaults Encrypted to true when the field is absent, so
// catalogs written before encryption became optional (every chunk was
// encrypted) load with the correct meaning.
func (c *Chunk) UnmarshalJSON(b []byte) error {
	type alias Chunk
	aux := &struct {
		Encrypted *bool `json:"encrypted"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(b, aux); err != nil {
		return err
	}
	c.Encrypted = aux.Encrypted == nil || *aux.Encrypted
	return nil
}

type KeyMeta struct { // secrets are NEVER here — keystore files only
	Ref         string    `json:"key_ref"`
	Fingerprint string    `json:"fingerprint"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
}

type Job struct {
	ID        int       `json:"id"`
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	Status    string    `json:"status"` // RUNNING COMPLETED FAILED
	Progress  float64   `json:"progress"`
	CreatedAt time.Time `json:"created_at"`
}

type Audit struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
}

// DriftItem is one file that differs between the source folders now and what
// the collection's chunks hold. MISSING/MODIFIED carry a restore pointer
// (which chunk + which volumes hold the backed-up version).
type DriftItem struct {
	State         string   `json:"state"` // NEW MODIFIED MISSING MOVED (UNCHANGED is counted, not listed)
	Path          string   `json:"path"`
	Ext           string   `json:"ext"`
	Hash          string   `json:"hash,omitempty"`
	MovedFrom     string   `json:"moved_from,omitempty"`
	Chunk         string   `json:"chunk,omitempty"`   // backing chunk for MISSING/MODIFIED
	Volumes       []string `json:"volumes,omitempty"` // "LABEL (location)" restore-from pointers
	NeedsBackup   bool     `json:"needs_backup,omitempty"`
	Informational bool     `json:"informational,omitempty"`
}

// ExtDrift is the per-file-type headline row (".NEF: 2 missing, 0 modified").
type ExtDrift struct {
	Ext           string `json:"ext"`
	Missing       int    `json:"missing"`
	Modified      int    `json:"modified"`
	New           int    `json:"new"`
	Moved         int    `json:"moved"`
	Informational bool   `json:"informational"`
}

// DriftReport is the persisted result of a Rescan & compare for one collection.
type DriftReport struct {
	At           time.Time      `json:"at"`
	CollectionID int            `json:"collection_id"`
	Counts       map[string]int `json:"counts"`      // alarm totals: unchanged,new,modified,missing,moved
	InfoCounts   map[string]int `json:"info_counts"` // informational-extension totals (excluded from alarms)
	ByExt        []ExtDrift     `json:"by_ext"`
	Items        []DriftItem    `json:"items"` // only the changed files (not UNCHANGED)
}

// Changes returns the number of non-informational changes (the alarm total).
func (r *DriftReport) Changes() int {
	if r == nil {
		return 0
	}
	return r.Counts["new"] + r.Counts["modified"] + r.Counts["missing"] + r.Counts["moved"]
}

type catalog struct {
	NextID      map[string]int `json:"next_id"`
	Collections []*Collection  `json:"collections"`
	Folders     []*Folder      `json:"folders"`
	Files       []*File        `json:"files"`
	Chunks      []*Chunk       `json:"chunks"`
	Keys        []*KeyMeta     `json:"keys"`
	BurnQueues  []*BurnQueue   `json:"burn_queues"`
	Volumes     []*Volume      `json:"volumes"`
	Drift       []*DriftReport `json:"drift"` // latest reconcile report per collection
	Audit       []Audit        `json:"audit"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	c       catalog
	lastBak string // YYYYMMDD of the most recent daily backup written
	jobs    struct {
		mu   sync.Mutex
		next int
		rows []*Job
	}
}

func OpenStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "catalog.json")}
	s.c.NextID = map[string]int{}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.c); err != nil {
			return nil, fmt.Errorf("catalog.json is damaged: %w", err)
		}
	}
	if s.c.NextID == nil {
		s.c.NextID = map[string]int{}
	}
	// Reboot recovery: a disc caught mid-burn/mid-verify when the process
	// died is unknowable — the physical disc may be a coaster. Send it back
	// to PENDING so the operator re-burns on a fresh blank.
	recovered := false
	for _, q := range s.c.BurnQueues {
		for _, d := range q.Discs {
			if d.Status == "BURNING" || d.Status == "VERIFYING" {
				d.Detail = "reset to PENDING after restart (was " + d.Status + ") — the disc may be a coaster; re-burn on a fresh blank"
				d.Status = "PENDING"
				recovered = true
			}
		}
	}
	// Interrupted-job recovery: jobs are in-memory, so a chunk left mid-flight
	// (BUILDING/WRITING) when the process died is the only trace of an orphaned
	// job. Reset each to its prior stable state with an explanatory error, the
	// same spirit as the burn-queue recovery above.
	for _, c := range s.c.Chunks {
		switch c.Status {
		case "BUILDING":
			c.Status, c.Error = "PLANNED", "interrupted by shutdown mid-build — re-run Build"
			recovered = true
		case "WRITING":
			c.Status, c.Error = "STAGED", "interrupted by shutdown mid-write — re-run Write"
			recovered = true
		}
		// A spanned segment caught mid-write is an unknown partial file on the
		// medium; send it back to PENDING so the operator re-writes that tape.
		for i := range c.Segments {
			if c.Segments[i].Status == "WRITING" || c.Segments[i].Status == "WRITTEN" {
				c.Segments[i].Status = "PENDING"
				recovered = true
			}
		}
	}
	// Migration: pre-Volumes catalogs recorded only written_dest. Attach that as
	// a Copy on an auto-created "(unregistered)" volume so old data keeps working
	// and shows up in the Volumes/redundancy views.
	for _, c := range s.c.Chunks {
		if c.WrittenDest != "" && len(c.Copies) == 0 {
			v := s.ensureUnregisteredLocked()
			c.Copies = append(c.Copies, Copy{VolumeID: v.ID, Path: c.WrittenDest,
				WrittenAt: c.WrittenAt, LastVerifiedAt: c.VerifiedAt, VerifyOK: c.VerifyOK})
			recovered = true
		}
	}
	if recovered {
		_ = s.save()
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(&s.c, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dailyBackup(b)
	return nil
}

// dailyBackup writes catalog.json.bak-YYYYMMDD once per calendar day (best
// effort — a backup must never fail a save) and prunes to the newest 14.
func (s *Store) dailyBackup(b []byte) {
	day := time.Now().Format("20060102")
	if s.lastBak == day {
		return
	}
	s.lastBak = day
	bak := s.path + ".bak-" + day
	if _, err := os.Stat(bak); err != nil {
		_ = os.WriteFile(bak, b, 0o644)
	}
	matches, _ := filepath.Glob(s.path + ".bak-*")
	sort.Strings(matches) // YYYYMMDD suffix sorts chronologically
	for len(matches) > 14 {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
}

func (s *Store) next(kind string) int {
	s.c.NextID[kind]++
	return s.c.NextID[kind]
}

func (s *Store) Log(action, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Audit = append(s.c.Audit, Audit{At: time.Now().UTC(), Action: action, Detail: detail})
	_ = s.save()
}

// ---- collections / folders / files -----------------------------------

func (s *Store) AddCollection(name string) *Collection {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Collection{ID: s.next("collection"), Name: name, CreatedAt: time.Now().UTC()}
	s.c.Collections = append(s.c.Collections, c)
	_ = s.save()
	return c
}

func (s *Store) Collections() []*Collection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Collection{}, s.c.Collections...)
}

func (s *Store) Collection(id int) *Collection {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.c.Collections {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *Store) AddFolder(collectionID int, path string) *Folder {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.c.Folders { // idempotent per (collection, path)
		if f.CollectionID == collectionID && f.Path == path {
			return f
		}
	}
	f := &Folder{ID: s.next("folder"), CollectionID: collectionID, Path: path}
	s.c.Folders = append(s.c.Folders, f)
	_ = s.save()
	return f
}

func (s *Store) FoldersOf(collectionID int) []*Folder {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Folder
	for _, f := range s.c.Folders {
		if f.CollectionID == collectionID {
			out = append(out, f)
		}
	}
	return out
}

// UpsertFile replaces a prior entry for (collection, folder, rel_path).
func (s *Store) UpsertFile(f File) *File {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.c.Files {
		if e.CollectionID == f.CollectionID && e.FolderID == f.FolderID && e.RelPath == f.RelPath {
			e.SizeBytes, e.HashAlg, e.Hash = f.SizeBytes, f.HashAlg, f.Hash
			return e
		}
	}
	nf := f
	nf.ID = s.next("file")
	s.c.Files = append(s.c.Files, &nf)
	return &nf
}

func (s *Store) Flush() { s.mu.Lock(); defer s.mu.Unlock(); _ = s.save() }

func (s *Store) FilesOf(collectionID int) []*File {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*File
	for _, f := range s.c.Files {
		if f.CollectionID == collectionID {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

func (s *Store) FileByID(id int) *File {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.c.Files {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// Search answers "which chunk / which medium holds this file?"
func (s *Store) Search(q string, limit int) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	q = strings.ToLower(q)
	loc := map[int]*Chunk{} // fileID -> chunk
	for _, ch := range s.c.Chunks {
		for _, cf := range ch.Files {
			loc[cf.FileID] = ch
		}
	}
	vol := map[int]*Volume{}
	for _, v := range s.c.Volumes {
		vol[v.ID] = v
	}
	var out []map[string]any
	for _, f := range s.c.Files {
		if q != "" && !strings.Contains(strings.ToLower(f.RelPath), q) {
			continue
		}
		row := map[string]any{"file_id": f.ID, "rel_path": f.RelPath, "size_bytes": f.SizeBytes, "hash": f.Hash}
		if ch, ok := loc[f.ID]; ok {
			row["chunk"] = ch.Name
			row["chunk_status"] = ch.Status
			row["written_dest"] = ch.WrittenDest
			row["key_ref"] = ch.KeyRef
			// "which volumes, verified when?" — the whole point of the feature.
			copies := make([]map[string]any, 0, len(ch.Copies))
			for _, cp := range ch.Copies {
				e := map[string]any{"path": cp.Path, "verify_ok": cp.VerifyOK, "last_verified_at": cp.LastVerifiedAt}
				if v := vol[cp.VolumeID]; v != nil {
					e["volume_label"], e["location"], e["kind"], e["barcode"] = v.Label, v.Location, v.Kind, v.Barcode
				}
				copies = append(copies, e)
			}
			row["copies"] = copies
			row["verified_copies"] = ch.VerifiedCopyCount()
		}
		out = append(out, row)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ---- chunks -----------------------------------------------------------

func (s *Store) AddChunk(c Chunk) *Chunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	nc := c
	nc.ID = s.next("chunk")
	nc.CreatedAt = time.Now().UTC()
	s.c.Chunks = append(s.c.Chunks, &nc)
	_ = s.save()
	return &nc
}

func (s *Store) Chunk(id int) *Chunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.c.Chunks {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *Store) Chunks(collectionID int) []*Chunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Chunk
	for _, c := range s.c.Chunks {
		if collectionID == 0 || c.CollectionID == collectionID {
			out = append(out, c)
		}
	}
	return out
}

// ChunkedFileHashes maps fileID -> the hash recorded when it was chunked (from
// ChunkFileRef). A file whose current hash still matches is genuinely backed up;
// a mismatch means the on-disk version changed and needs re-chunking.
func (s *Store) ChunkedFileHashes(collectionID int) map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[int]string{}
	for _, c := range s.c.Chunks {
		if c.CollectionID == collectionID && c.Status != "FAILED" {
			for _, cf := range c.Files {
				m[cf.FileID] = cf.Hash // later (newer) chunk wins for a re-chunked file
			}
		}
	}
	return m
}

// ReplaceDriftReport stores r as the latest report for its collection.
func (s *Store) ReplaceDriftReport(r *DriftReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.c.Drift[:0]
	for _, d := range s.c.Drift {
		if d.CollectionID != r.CollectionID {
			out = append(out, d)
		}
	}
	s.c.Drift = append(out, r)
	_ = s.save()
}

func (s *Store) DriftReport(collectionID int) *DriftReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.c.Drift {
		if d.CollectionID == collectionID {
			return d
		}
	}
	return nil
}

func (s *Store) DriftReports() []*DriftReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*DriftReport{}, s.c.Drift...)
}

func (s *Store) ChunkedFileIDs(collectionID int) map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[int]bool{}
	for _, c := range s.c.Chunks {
		if c.CollectionID == collectionID && c.Status != "FAILED" {
			for _, cf := range c.Files {
				m[cf.FileID] = true
			}
		}
	}
	return m
}

func (s *Store) UpdateChunk(c *Chunk) { s.mu.Lock(); defer s.mu.Unlock(); _ = s.save() }

// AppendVerifyEvent records one integrity check and persists. Callers set any
// status/verified_at fields on c first; this single save captures them too.
func (s *Store) AppendVerifyEvent(c *Chunk, ev VerifyEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.VerifyEvents = append(c.VerifyEvents, ev)
	_ = s.save()
}

// ---- volumes + copies --------------------------------------------------

func (s *Store) ensureUnregisteredLocked() *Volume {
	for _, v := range s.c.Volumes {
		if v.Label == "(unregistered)" {
			return v
		}
	}
	v := &Volume{ID: s.next("volume"), Label: "(unregistered)", Kind: "OTHER",
		Notes: "auto-created for media written before volumes were tracked", CreatedAt: time.Now().UTC()}
	s.c.Volumes = append(s.c.Volumes, v)
	return v
}

func (s *Store) EnsureUnregistered() *Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.ensureUnregisteredLocked()
	_ = s.save()
	return v
}

func (s *Store) AddVolume(v Volume) *Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	nv := v
	nv.ID = s.next("volume")
	nv.CreatedAt = time.Now().UTC()
	if nv.Kind == "" {
		nv.Kind = "OTHER"
	}
	s.c.Volumes = append(s.c.Volumes, &nv)
	_ = s.save()
	return &nv
}

func (s *Store) Volumes() []*Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Volume{}, s.c.Volumes...)
}

func (s *Store) Volume(id int) *Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.c.Volumes {
		if v.ID == id {
			return v
		}
	}
	return nil
}

func (s *Store) VolumeByBarcode(barcode string) *Volume {
	barcode = strings.TrimSpace(barcode)
	if barcode == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.c.Volumes {
		if strings.EqualFold(v.Barcode, barcode) {
			return v
		}
	}
	return nil
}

func (s *Store) UpdateVolume(v *Volume) { s.mu.Lock(); defer s.mu.Unlock(); _ = s.save() }

// RecordCopy adds or refreshes the copy of chunk c on the given volume and
// persists. verifiedOK reflects the read-back that just happened.
func (s *Store) RecordCopy(c *Chunk, volumeID int, path string, verifiedOK bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	ok := verifiedOK
	for i := range c.Copies {
		if c.Copies[i].VolumeID == volumeID {
			c.Copies[i].Path = path
			c.Copies[i].LastVerifiedAt = &now
			c.Copies[i].VerifyOK = &ok
			_ = s.save()
			return
		}
	}
	c.Copies = append(c.Copies, Copy{VolumeID: volumeID, Path: path, WrittenAt: &now, LastVerifiedAt: &now, VerifyOK: &ok})
	_ = s.save()
}

// UpdateCopyVerify refreshes the last_verified_at/verify_ok of the copy whose
// Path matches (or the sole copy) after a verify against that medium.
func (s *Store) UpdateCopyVerify(c *Chunk, path string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	matched := false
	for i := range c.Copies {
		p := c.Copies[i].Path
		// base-mount vs chunk-subfolder both match (one is a prefix of the other)
		if p != "" && path != "" && (strings.HasPrefix(p, path) || strings.HasPrefix(path, p)) {
			v := ok
			c.Copies[i].LastVerifiedAt, c.Copies[i].VerifyOK = &now, &v
			matched = true
		}
	}
	if !matched && len(c.Copies) == 1 {
		v := ok
		c.Copies[0].LastVerifiedAt, c.Copies[0].VerifyOK = &now, &v
	}
	_ = s.save()
}

// VerifiedCopyCount returns how many copies last verified OK (spanned chunks
// count as one copy once fully written+verified).
func (c *Chunk) VerifiedCopyCount() int {
	n := 0
	for _, cp := range c.Copies {
		if cp.VerifyOK != nil && *cp.VerifyOK {
			n++
		}
	}
	return n
}

// ---- keys (metadata only) ----------------------------------------------

func (s *Store) AddKeyMeta(k KeyMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Keys = append(s.c.Keys, &k)
	_ = s.save()
}
func (s *Store) KeyMetas() []*KeyMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*KeyMeta{}, s.c.Keys...)
}

// ---- jobs (in-memory; a restart clears the board, catalog is truth) ----

func (s *Store) NewJob(kind, label string) *Job {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	s.jobs.next++
	j := &Job{ID: s.jobs.next, Kind: kind, Label: label, Status: "RUNNING", CreatedAt: time.Now().UTC()}
	s.jobs.rows = append(s.jobs.rows, j)
	return j
}

func (s *Store) SetJob(id int, progress float64, label, status string) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	for _, j := range s.jobs.rows {
		if j.ID == id {
			if progress >= 0 {
				j.Progress = progress
			}
			if label != "" {
				j.Label = label
			}
			if status != "" {
				j.Status = status
			}
		}
	}
}

func (s *Store) Jobs() []*Job {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	out := append([]*Job{}, s.jobs.rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}
