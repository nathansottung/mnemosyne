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
}

type Chunk struct {
	ID           int            `json:"id"`
	CollectionID int            `json:"collection_id"`
	Name         string         `json:"name"`
	Status       string         `json:"status"` // PLANNED BUILDING STAGED WRITING WRITTEN VERIFIED FAILED
	MediaKind    string         `json:"media_kind"`
	TargetBytes  int64          `json:"target_bytes"`
	DataBytes    int64          `json:"data_bytes"`
	EncBytes     int64          `json:"enc_bytes"`
	FileCount    int            `json:"file_count"`
	SrcRoot      string         `json:"src_root"`
	HashAlg      string         `json:"hash_alg"`
	TarHash      string         `json:"tar_hash"`
	EncHash      string         `json:"enc_hash"`
	KeyRef       string         `json:"key_ref"`
	Par2         int            `json:"par2_redundancy"`
	StagedDir    string         `json:"staged_dir"`
	WrittenDest  string         `json:"written_dest"`
	VerifyOK     *bool          `json:"verify_ok"`
	Error        string         `json:"error"`
	Files        []ChunkFileRef `json:"files"`
	CreatedAt    time.Time      `json:"created_at"`
	WrittenAt    *time.Time     `json:"written_at,omitempty"`
	VerifiedAt   *time.Time     `json:"verified_at,omitempty"`
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

type catalog struct {
	NextID      map[string]int `json:"next_id"`
	Collections []*Collection  `json:"collections"`
	Folders     []*Folder      `json:"folders"`
	Files       []*File        `json:"files"`
	Chunks      []*Chunk       `json:"chunks"`
	Keys        []*KeyMeta     `json:"keys"`
	Audit       []Audit        `json:"audit"`
}

type Store struct {
	mu   sync.Mutex
	path string
	c    catalog
	jobs struct {
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
	return os.Rename(tmp, s.path)
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

func (s *Store) Collections() []*Collection { s.mu.Lock(); defer s.mu.Unlock(); return append([]*Collection{}, s.c.Collections...) }

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

// ---- keys (metadata only) ----------------------------------------------

func (s *Store) AddKeyMeta(k KeyMeta) { s.mu.Lock(); defer s.mu.Unlock(); s.c.Keys = append(s.c.Keys, &k); _ = s.save() }
func (s *Store) KeyMetas() []*KeyMeta { s.mu.Lock(); defer s.mu.Unlock(); return append([]*KeyMeta{}, s.c.Keys...) }

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
