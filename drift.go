package main

// drift.go — inventory reconciliation. Compare a collection's source folders as
// they are NOW against what its chunks actually hold, and classify every path:
// UNCHANGED, NEW, MODIFIED, MISSING, MOVED. Different file types drift for
// different reasons, so the per-extension table is the headline and sidecar
// types (e.g. .xmp) are muted out of the alarm totals.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// chunkVolumes renders a chunk's restore-from pointers: which volumes (label +
// location) hold a copy, plus any spanned-segment volumes.
func chunkVolumes(c *Chunk, volm map[int]*Volume) []string {
	var out []string
	for _, cp := range c.Copies {
		label := fmt.Sprintf("vol#%d", cp.VolumeID)
		loc := ""
		if v := volm[cp.VolumeID]; v != nil {
			label, loc = v.Label, v.Location
		}
		s := label
		if loc != "" {
			s += " (" + loc + ")"
		}
		if cp.VerifyOK != nil && *cp.VerifyOK {
			s += " ✓"
		}
		out = append(out, s)
	}
	seen := map[int]bool{}
	for _, sg := range c.Segments {
		if sg.VolumeID != 0 && !seen[sg.VolumeID] {
			seen[sg.VolumeID] = true
			if v := volm[sg.VolumeID]; v != nil {
				out = append(out, fmt.Sprintf("%s (holds seg %d)", v.Label, sg.Index))
			}
		}
	}
	return out
}

// ReconcileCollection rescans the source folders and compares against the
// chunked state, persisting a DriftReport. The rescan refreshes the File table
// (via the shared parallel pool) so a later Plan naturally re-backs-up NEW and
// MODIFIED files, while the comparison uses the chunked hashes as the reference.
func (a *App) ReconcileCollection(collectionID int, progress func(float64, string)) (*DriftReport, error) {
	cfg := a.LoadConfig()
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	folders := a.Store.FoldersOf(collectionID)
	if len(folders) == 0 {
		return nil, fmt.Errorf("archive %q has no scanned folders — scan one first", coll.Name)
	}
	infoExt := map[string]bool{}
	for _, e := range cfg.DriftInformational {
		infoExt[strings.ToLower(strings.TrimSpace(e))] = true
	}
	folderPath := map[int]string{}
	for _, fo := range folders {
		folderPath[fo.ID] = fo.Path
	}

	// Snapshot the File table BEFORE the rescan overwrites hashes — used only as a
	// legacy fallback for chunks planned before ChunkFileRef.Hash existed.
	preHash := map[int]string{}
	fileFolder := map[int]int{}
	fileRel := map[int]string{}
	for _, f := range a.Store.FilesOf(collectionID) {
		preHash[f.ID], fileFolder[f.ID], fileRel[f.ID] = f.Hash, f.FolderID, f.RelPath
	}
	volm := map[int]*Volume{}
	for _, v := range a.Store.Volumes() {
		volm[v.ID] = v
	}

	type backed struct {
		abs, rel, hash, chunk string
		vols                  []string
	}
	backedByPath := map[string]*backed{}
	backedByHash := map[string][]*backed{}
	for _, c := range a.Store.Chunks(collectionID) {
		if c.Status == "FAILED" {
			continue
		}
		vols := chunkVolumes(c, volm)
		for _, cf := range c.Files {
			h := cf.Hash
			if h == "" {
				h = preHash[cf.FileID]
			}
			rel := cf.RelPath
			if rel == "" {
				rel = fileRel[cf.FileID]
			}
			abs := filepath.Join(folderPath[fileFolder[cf.FileID]], filepath.FromSlash(rel))
			b := &backed{abs: abs, rel: rel, hash: h, chunk: c.Name, vols: vols}
			backedByPath[abs] = b
			if h != "" {
				backedByHash[h] = append(backedByHash[h], b)
			}
		}
	}

	// Walk + hash the current disk (and refresh the File table).
	progress(0.02, "listing files")
	var paths []string
	perFolder := map[string]int{}
	for _, fo := range folders {
		_ = filepath.WalkDir(fo.Path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				paths = append(paths, p)
				perFolder[p] = fo.ID
			}
			return nil
		})
	}
	total := len(paths)
	type cur struct {
		hash string
		size int64
	}
	curByAbs := map[string]*cur{}
	var mu sync.Mutex
	parallelHash(paths, func(dd int) {
		if total > 0 {
			progress(0.05+0.7*float64(dd)/float64(total), fmt.Sprintf("hashed %d/%d", dd, total))
		}
	}, func(p, h string, size int64) {
		mu.Lock()
		curByAbs[p] = &cur{hash: h, size: size}
		mu.Unlock()
		fol := perFolder[p]
		if rel, e := filepath.Rel(folderPath[fol], p); e == nil {
			a.Store.UpsertFile(File{CollectionID: collectionID, FolderID: fol,
				RelPath: filepath.ToSlash(rel), SizeBytes: size, HashAlg: "SHA256", Hash: h})
		}
	})
	a.Store.Flush()

	progress(0.82, "comparing")
	counts := map[string]int{}
	info := map[string]int{}
	extAgg := map[string]*ExtDrift{}
	var items []DriftItem
	add := func(it DriftItem) {
		it.Ext = strings.ToLower(filepath.Ext(it.Path))
		it.Informational = infoExt[it.Ext]
		if it.State == "NEW" || it.State == "MODIFIED" {
			it.NeedsBackup = true
		}
		e := extAgg[it.Ext]
		if e == nil {
			e = &ExtDrift{Ext: it.Ext, Informational: it.Informational}
			extAgg[it.Ext] = e
		}
		switch it.State {
		case "MISSING":
			e.Missing++
		case "MODIFIED":
			e.Modified++
		case "NEW":
			e.New++
		case "MOVED":
			e.Moved++
		}
		if it.Informational {
			info[strings.ToLower(it.State)]++
		} else {
			counts[strings.ToLower(it.State)]++
		}
		items = append(items, it)
	}

	missing := map[string]*backed{}
	for abs, b := range backedByPath {
		if curByAbs[abs] == nil {
			missing[abs] = b
		}
	}
	usedMissing := map[string]bool{}
	relOf := func(abs string) string {
		if rel, e := filepath.Rel(folderPath[perFolder[abs]], abs); e == nil {
			return filepath.ToSlash(rel)
		}
		return abs
	}

	for abs, c := range curByAbs {
		if b := backedByPath[abs]; b != nil {
			if b.hash == c.hash {
				counts["unchanged"]++
			} else {
				add(DriftItem{State: "MODIFIED", Path: b.rel, Hash: c.hash, Chunk: b.chunk, Volumes: b.vols})
			}
			continue
		}
		// NEW unless its content matches a now-missing backed-up file (MOVED).
		var src *backed
		for _, cand := range backedByHash[c.hash] {
			if missing[cand.abs] != nil && !usedMissing[cand.abs] {
				src = cand
				break
			}
		}
		if src != nil {
			usedMissing[src.abs] = true
			add(DriftItem{State: "MOVED", Path: relOf(abs), Hash: c.hash, MovedFrom: src.rel})
		} else {
			add(DriftItem{State: "NEW", Path: relOf(abs), Hash: c.hash})
		}
	}
	for abs, b := range missing {
		if usedMissing[abs] {
			continue
		}
		add(DriftItem{State: "MISSING", Path: b.rel, Hash: b.hash, Chunk: b.chunk, Volumes: b.vols})
	}

	byExt := make([]ExtDrift, 0, len(extAgg))
	for _, e := range extAgg {
		byExt = append(byExt, *e)
	}
	sort.Slice(byExt, func(i, j int) bool { return byExt[i].Ext < byExt[j].Ext })
	sort.Slice(items, func(i, j int) bool {
		if items[i].State != items[j].State {
			return items[i].State < items[j].State
		}
		return items[i].Path < items[j].Path
	})

	infoTotal := info["new"] + info["modified"] + info["missing"] + info["moved"]
	rep := &DriftReport{At: time.Now().UTC(), CollectionID: collectionID,
		Counts: counts, InfoCounts: info, ByExt: byExt, Items: items}
	a.Store.ReplaceDriftReport(rep)
	a.Store.Log("reconcile", fmt.Sprintf("archive %d: %d change(s), %d informational", collectionID, rep.Changes(), infoTotal))
	progress(1.0, fmt.Sprintf("%d changes · %d informational", rep.Changes(), infoTotal))
	return rep, nil
}
