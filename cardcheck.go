package main

// cardcheck.go — "Is this card already backed up?" Plug in an SD/CF/USB card, and
// Mnemosyne reads it (NEVER writes, NEVER registers it as a source) and checks every
// file BY CONTENT against the ENTIRE known inventory: your archives (and their retained
// prior versions), packaged chunks and the volumes their copies live on, and the
// offline snapshots of every drive you've inventoried. The answer is the one a
// photographer actually wants before reformatting: which frames are already safe, which
// are new, and — for the safe ones — where they already live.
//
// Read-only by construction: it only os.Opens files to hash them, mutates nothing in
// the catalog, and adds no source root. Matching is by SHA-256 (the universal on-media
// hash) with BLAKE3 as a fallback, so a file counts as "backed up" the instant its
// content is found anywhere in the inventory — regardless of its name or folder on the card.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxKnownLocs caps how many places we remember per content hash (bounds memory on
// huge libraries; a few is plenty to reassure "it's on the NAS and a backup drive").
const maxKnownLocs = 3

// CardFileResult is one file on the card and whether its content is already known.
type CardFileResult struct {
	Rel       string   `json:"rel"`
	Size      int64    `json:"size"`
	Hash      string   `json:"hash,omitempty"`
	Role      string   `json:"role,omitempty"`
	BackedUp  bool     `json:"backed_up"`
	Locations []string `json:"locations,omitempty"` // where it already lives (for backed-up files)
}

// CardExtRow is a per-type tally of the card, split into new vs total.
type CardExtRow struct {
	Ext      string `json:"ext"`
	Total    int    `json:"total"`
	New      int    `json:"new"`
	NewBytes int64  `json:"new_bytes"`
}

// CardCheckResult is the whole verdict.
type CardCheckResult struct {
	MountPath     string           `json:"mount_path"`
	TotalFiles    int              `json:"total_files"`
	TotalBytes    int64            `json:"total_bytes"`
	BackedUpFiles int              `json:"backed_up_files"`
	BackedUpBytes int64            `json:"backed_up_bytes"`
	NewFiles      int              `json:"new_files"`
	NewBytes      int64            `json:"new_bytes"`
	Skipped       int              `json:"skipped"` // unreadable files (I/O error while hashing)
	SafeToFormat  bool             `json:"safe_to_format"`
	Where         []string         `json:"where,omitempty"`         // distinct places the already-backed-up files live
	New           []CardFileResult `json:"new,omitempty"`           // the not-yet-backed-up files (capped)
	NewTruncated  int              `json:"new_truncated,omitempty"` // how many more new files beyond the cap
	Sample        []CardFileResult `json:"sample,omitempty"`        // a few backed-up examples, with where
	ByExt         []CardExtRow     `json:"by_ext,omitempty"`
}

// knownContent builds an index from every content hash the catalog knows (SHA-256 AND
// BLAKE3) to a few human labels of where it lives — the whole inventory, in one map.
func (a *App) knownContent() map[string][]string {
	idx := map[string][]string{}
	add := func(hash, loc string) {
		if hash == "" {
			return
		}
		cur := idx[hash]
		if loc == "" {
			if cur == nil {
				idx[hash] = []string{} // presence with no label
			}
			return
		}
		if len(cur) >= maxKnownLocs {
			return
		}
		for _, l := range cur {
			if l == loc {
				return
			}
		}
		idx[hash] = append(cur, loc)
	}

	volLabel := map[int]string{}
	for _, v := range a.Store.Volumes() {
		volLabel[v.ID] = nonEmpty(v.Label, fmt.Sprintf("vol#%d", v.ID))
	}

	// Archives — current files and their retained prior versions.
	for _, coll := range a.Store.Collections() {
		loc := "Archive: " + coll.Name
		for _, f := range a.Store.FilesOf(coll.ID) {
			add(f.Hash, loc)
			add(f.Blake3, loc)
			for _, v := range f.Versions {
				add(v.Hash, loc+" (earlier version)") // retained prior versions keep only SHA-256
			}
		}
	}

	// Packaged chunks — a ref's content lives on the volumes the chunk's current
	// copies (and spanned segments) sit on; else name the archive.
	for _, c := range a.Store.Chunks(0) {
		var locs []string
		seen := map[int]bool{}
		addVol := func(vid int) {
			if vid == 0 || seen[vid] {
				return
			}
			seen[vid] = true
			locs = append(locs, volLabel[vid])
		}
		for _, cp := range c.Copies {
			if !cp.Superseded {
				addVol(cp.VolumeID)
			}
		}
		for _, sg := range c.Segments {
			addVol(sg.VolumeID)
		}
		if len(locs) == 0 {
			if coll := a.Store.Collection(c.CollectionID); coll != nil {
				locs = []string{"Archive: " + coll.Name}
			}
		}
		for _, ref := range c.Files {
			if len(locs) == 0 {
				add(ref.Hash, "")
				continue
			}
			for _, l := range locs {
				add(ref.Hash, l)
			}
		}
	}

	// Offline snapshots — every drive you've inventoried ("adopted").
	for _, snap := range a.Store.VolumeSnapshots() {
		loc := "Drive: " + nonEmpty(snap.Label, fmt.Sprintf("vol#%d", snap.VolumeID))
		for _, sf := range snap.Files {
			add(sf.Hash, loc)
			add(sf.Blake3, loc)
		}
	}
	return idx
}

// cardSkipDir hides OS bookkeeping and the tool's own sidecars from a card walk.
func cardSkipDir(name string) bool {
	if strings.HasPrefix(name, ".") || skipBrowseDir(name) {
		return true
	}
	switch name {
	case dockSidecarDir, sealSidecarDir:
		return true
	}
	return false
}

// CardCheck reads a mounted card/drive and reports, per file, whether its content is
// already somewhere in the inventory. Read-only: it hashes files and touches nothing.
func (a *App) CardCheck(mountPath string, progress func(float64, string)) (*CardCheckResult, error) {
	info, err := os.Stat(mountPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("not a readable folder/drive: %s", mountPath)
	}
	progress(0.01, "reading inventory")
	idx := a.knownContent()

	progress(0.03, "listing files on the card")
	var paths []string
	_ = filepath.WalkDir(mountPath, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep going
		}
		if d.IsDir() {
			if p != mountPath && cardSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		paths = append(paths, p)
		return nil
	})

	reg := a.formatRegistry()
	res := &CardCheckResult{MountPath: mountPath}
	extAgg := map[string]*CardExtRow{}
	whereSet := map[string]bool{}
	var mu sync.Mutex
	total := len(paths)
	if total == 0 {
		return res, nil
	}

	parallelHash(paths, func(done int) {
		progress(0.05+0.9*float64(done)/float64(total), progStats(0, 0, int64(done), int64(total), "hashing card files"))
	}, func(p, sha, b3 string, size int64, _ time.Time) {
		rel, e := filepath.Rel(mountPath, p)
		if e != nil {
			rel = filepath.Base(p)
		}
		rel = filepath.ToSlash(rel)
		role, _ := classifyRole(reg, rel)
		// Backed up if the content (sha, else blake3) is known anywhere.
		locs, ok := idx[sha]
		if !ok && b3 != "" {
			locs, ok = idx[b3]
		}

		mu.Lock()
		defer mu.Unlock()
		res.TotalFiles++
		res.TotalBytes += size
		ext := strings.ToLower(path.Ext(rel))
		er := extAgg[ext]
		if er == nil {
			er = &CardExtRow{Ext: ext}
			extAgg[ext] = er
		}
		er.Total++
		if ok {
			res.BackedUpFiles++
			res.BackedUpBytes += size
			for _, l := range locs {
				whereSet[l] = true
			}
			if len(res.Sample) < 8 {
				res.Sample = append(res.Sample, CardFileResult{Rel: rel, Size: size, Hash: sha, Role: role, BackedUp: true, Locations: append([]string(nil), locs...)})
			}
		} else {
			res.NewFiles++
			res.NewBytes += size
			er.New++
			er.NewBytes += size
			if len(res.New) < 500 {
				res.New = append(res.New, CardFileResult{Rel: rel, Size: size, Hash: sha, Role: role})
			} else {
				res.NewTruncated++
			}
		}
	})

	res.Skipped = total - res.TotalFiles // files that failed to hash (I/O errors)
	res.SafeToFormat = res.TotalFiles > 0 && res.NewFiles == 0

	for l := range whereSet {
		res.Where = append(res.Where, l)
	}
	sort.Strings(res.Where)
	if len(res.Where) > 10 {
		res.Where = res.Where[:10]
	}
	for _, er := range extAgg {
		res.ByExt = append(res.ByExt, *er)
	}
	sort.Slice(res.ByExt, func(i, j int) bool {
		if res.ByExt[i].New != res.ByExt[j].New {
			return res.ByExt[i].New > res.ByExt[j].New // most-new types first
		}
		return res.ByExt[i].Ext < res.ByExt[j].Ext
	})
	sort.Slice(res.New, func(i, j int) bool { return res.New[i].Rel < res.New[j].Rel })

	progress(1.0, fmt.Sprintf("checked %d file(s): %d already backed up, %d new", res.TotalFiles, res.BackedUpFiles, res.NewFiles))
	return res, nil
}
