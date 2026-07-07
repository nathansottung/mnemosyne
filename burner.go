package main

// burner.go — the optical burn queue.
//
// Optical burns can't stream through the RAM ring like tape/HDD: you feed
// one blank disc, click, wait, label it, repeat. So a burn is a persistent
// QUEUE of discs (one per chunk) that survives reboots — if the machine dies
// mid-burn, store open resets that disc to PENDING and the operator re-burns
// on a fresh blank. v1 advances only on an explicit "burn next" click; we do
// NOT try to auto-detect disc insertion.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type BurnDisc struct {
	ChunkID  int        `json:"chunk_id"`
	Status   string     `json:"status"` // PENDING BURNING VERIFYING DONE FAILED
	Detail   string     `json:"detail"`
	BurnedAt *time.Time `json:"burned_at,omitempty"`
}

type BurnQueue struct {
	ID        int         `json:"id"`
	Name      string      `json:"name"`
	MediaKind string      `json:"media_kind"`
	CreatedAt time.Time   `json:"created_at"`
	Discs     []*BurnDisc `json:"discs"`
}

// isOpen reports whether the queue still has work — any disc not yet DONE.
// Chunks in open queues are excluded when gathering a new queue.
func (q *BurnQueue) isOpen() bool {
	for _, d := range q.Discs {
		if d.Status != "DONE" {
			return true
		}
	}
	return false
}

// ---- store persistence (catalog.json, same flat-file style) ------------

func (s *Store) AddBurnQueue(q BurnQueue) *BurnQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	nq := q
	nq.ID = s.next("burnqueue")
	nq.CreatedAt = time.Now().UTC()
	s.c.BurnQueues = append(s.c.BurnQueues, &nq)
	_ = s.save()
	return &nq
}

func (s *Store) BurnQueues() []*BurnQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*BurnQueue{}, s.c.BurnQueues...)
}

func (s *Store) BurnQueue(id int) *BurnQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.c.BurnQueues {
		if q.ID == id {
			return q
		}
	}
	return nil
}

func (s *Store) UpdateBurnQueue(q *BurnQueue) { s.mu.Lock(); defer s.mu.Unlock(); _ = s.save() }

// ChunksInOpenBurnQueues answers "which chunks are already queued for burning?"
func (s *Store) ChunksInOpenBurnQueues() map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[int]bool{}
	for _, q := range s.c.BurnQueues {
		if !q.isOpen() {
			continue
		}
		for _, d := range q.Discs {
			m[d.ChunkID] = true
		}
	}
	return m
}

// ---- queue operations --------------------------------------------------

// CreateBurnQueue gathers every burnable chunk of one media kind (STAGED,
// WRITTEN, or VERIFIED) not already sitting in an open queue, one disc each.
func (a *App) CreateBurnQueue(collectionID int, mediaKind, name string) (*BurnQueue, error) {
	mediaKind = strings.TrimSpace(mediaKind)
	if mediaKind == "" {
		return nil, fmt.Errorf("media_kind required")
	}
	inOpen := a.Store.ChunksInOpenBurnQueues()
	var discs []*BurnDisc
	for _, c := range a.Store.Chunks(collectionID) {
		if c.MediaKind != mediaKind || inOpen[c.ID] {
			continue
		}
		switch c.Status {
		case "STAGED", "WRITTEN", "VERIFIED":
			discs = append(discs, &BurnDisc{ChunkID: c.ID, Status: "PENDING"})
		}
	}
	if len(discs) == 0 {
		return nil, fmt.Errorf("no burnable %s packages (STAGED/WRITTEN/VERIFIED, not already queued) found", mediaKind)
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s burn %s", mediaKind, time.Now().UTC().Format("2006-01-02"))
	}
	q := a.Store.AddBurnQueue(BurnQueue{Name: name, MediaKind: mediaKind, Discs: discs})
	a.Store.Log("burnqueue", fmt.Sprintf("%s: %d disc(s) of %s", q.Name, len(discs), mediaKind))
	return q, nil
}

// BurnNext burns the first PENDING disc: shell out to the configured burn
// command (with {SRC}/{LABEL} substituted), then optionally hash-verify the
// burned payload against the chunk's enc_hash before marking DONE.
func (a *App) BurnNext(id int, progress func(float64, string)) (map[string]any, error) {
	cfg := a.LoadConfig()
	if strings.TrimSpace(cfg.BurnCommand) == "" {
		return nil, fmt.Errorf("burn_command is not configured (Settings)")
	}
	q := a.Store.BurnQueue(id)
	if q == nil {
		return nil, fmt.Errorf("burn queue %d not found", id)
	}
	var disc *BurnDisc
	for _, d := range q.Discs {
		if d.Status == "PENDING" {
			disc = d
			break
		}
	}
	if disc == nil {
		return nil, fmt.Errorf("no PENDING discs left in %q", q.Name)
	}
	set := func(st, detail string) { disc.Status, disc.Detail = st, detail; a.Store.UpdateBurnQueue(q) }

	c := a.Store.Chunk(disc.ChunkID)
	if c == nil {
		set("FAILED", fmt.Sprintf("package %d not found in catalog", disc.ChunkID))
		return nil, errors.New(disc.Detail)
	}
	if c.StagedDir == "" {
		set("FAILED", "package has no staged_dir — build it before burning")
		return nil, errors.New(disc.Detail)
	}
	if _, err := os.Stat(c.StagedDir); err != nil {
		set("FAILED", "staged folder is unreadable: "+c.StagedDir)
		return nil, errors.New(disc.Detail)
	}

	set("BURNING", "")
	progress(0.05, "burning "+c.Name)
	// Private media: the burn command copies the whole staged folder, so move the
	// plaintext manifest out of it during the burn (only the .gpg gets burned),
	// then restore it. Kept on the same volume so the rename is atomic.
	if c.PrivateManifest {
		plain := filepath.Join(c.StagedDir, c.Name+".manifest.json")
		if _, err := os.Stat(plain); err == nil {
			hidden := filepath.Join(cfg.StagingDir, "."+c.Name+".manifest.hidden")
			if os.Rename(plain, hidden) == nil {
				defer os.Rename(hidden, plain)
			}
		}
	}
	cmdline := strings.ReplaceAll(cfg.BurnCommand, "{SRC}", c.StagedDir)
	cmdline = strings.ReplaceAll(cmdline, "{LABEL}", c.Name)
	if err := runShell(cmdline); err != nil {
		set("FAILED", "burn command failed: "+err.Error())
		return nil, err
	}

	// Optical discs aren't picked from the volume list, so a burned disc lands on
	// the "(unregistered)" volume — the operator can register/relabel it there.
	copyPath := strings.TrimSpace(cfg.BurnVerifyMount)
	if copyPath == "" {
		copyPath = "optical disc " + c.Name
	}
	// recordBurnCopy records the burn result on the disc's copy and re-derives the
	// package status. A bad burn fails the DISC (a coaster — re-burn), and marks
	// only THIS copy failed; it never marks the package FAILED while staging or
	// another verified copy survives.
	recordBurnCopy := func(ok bool) {
		a.Store.RecordCopy(c, a.Store.EnsureUnregistered().ID, copyPath, ok)
		a.refreshChunkStatus(c)
		a.Store.UpdateChunk(c)
	}

	verified := false
	if mount := strings.TrimSpace(cfg.BurnVerifyMount); mount != "" {
		set("VERIFYING", "")
		progress(0.7, "verify "+c.Name)
		payload := findPayload(mount, c)
		if payload == "" {
			set("FAILED", fmt.Sprintf("burned disc unreadable: no payload (%s, flat or in a %s/ folder) found (is the disc mounted at %s?)",
				strings.Join(payloadNameCandidates(c), " or "), c.Name, mount))
			a.Store.AppendVerifyEvent(c, VerifyEvent{At: time.Now().UTC(), OK: false, Path: mount, Note: "burn verify: unreadable"})
			recordBurnCopy(false)
			return nil, errors.New(disc.Detail)
		}
		h, err := hashFileHex(payload)
		if err != nil {
			set("FAILED", "cannot hash burned payload: "+err.Error())
			a.Store.AppendVerifyEvent(c, VerifyEvent{At: time.Now().UTC(), OK: false, Path: payload, Note: "burn verify: " + err.Error()})
			recordBurnCopy(false)
			return nil, err
		}
		if h != c.EncHash {
			set("FAILED", fmt.Sprintf("verify mismatch — disc=%s expected=%s (bad burn; re-burn on a fresh blank)", h, c.EncHash))
			a.Store.AppendVerifyEvent(c, VerifyEvent{At: time.Now().UTC(), OK: false, Path: payload, Note: "burn verify: hash mismatch"})
			recordBurnCopy(false)
			return nil, errors.New(disc.Detail)
		}
		a.Store.AppendVerifyEvent(c, VerifyEvent{At: time.Now().UTC(), OK: true, Path: payload, Note: "burn verify"})
		verified = true
	}

	now := time.Now().UTC()
	disc.BurnedAt = &now
	set("DONE", "")
	// verify_ok reflects whether a mount read-back actually happened.
	recordBurnCopy(verified)
	a.Store.Log("burn", fmt.Sprintf("%s burned in %q", c.Name, q.Name))
	progress(1.0, "done")
	return map[string]any{"queue": q.Name, "chunk": c.Name, "status": "DONE"}, nil
}

// ResetBurnQueue returns every FAILED disc to PENDING for a retry.
func (a *App) ResetBurnQueue(id int) (int, error) {
	q := a.Store.BurnQueue(id)
	if q == nil {
		return 0, fmt.Errorf("burn queue %d not found", id)
	}
	n := 0
	for _, d := range q.Discs {
		if d.Status == "FAILED" {
			d.Status, d.Detail = "PENDING", "retry after reset"
			n++
		}
	}
	a.Store.UpdateBurnQueue(q)
	a.Store.Log("burnqueue", fmt.Sprintf("%s: reset %d failed disc(s) to PENDING", q.Name, n))
	return n, nil
}

// runShell runs a burn command line through the platform shell. Exit code 0
// (nil error) is success; anything else fails the disc with the tail of output.
func runShell(cmdline string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", cmdline)
	} else {
		cmd = exec.Command("sh", "-c", cmdline)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t := string(out)
		if len(t) > 700 {
			t = t[len(t)-700:]
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(t))
	}
	return nil
}
