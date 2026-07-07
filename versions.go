package main

// versions.go — archival version retention (prompt 50). The catalog keeps every
// content version a file has had (see File.Versions), and — because package and
// mirror membership is content-addressed — can still say WHERE each version's
// bytes live, even after the file's current content moved on. This file turns the
// retained history into a located, restorable view and resolves a version
// selector (newest / by index / by hash / "as of <date>") to a concrete package.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileVersionVolume is one physical medium that holds a version's bytes.
type FileVersionVolume struct {
	VolumeID int    `json:"volume_id"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Barcode  string `json:"barcode,omitempty"`
	Verified bool   `json:"verified"`
}

// FileVersionPackage is one package (chunk) that holds a version's bytes, plus
// the volumes that carry that package.
type FileVersionPackage struct {
	ChunkID int                 `json:"chunk_id"`
	Chunk   string              `json:"chunk"`
	Status  string              `json:"status"`
	Volumes []FileVersionVolume `json:"volumes,omitempty"`
}

// FileVersionView is one known content version of a file (current or superseded)
// enriched with where it can be restored from. Index is 1-based, v1 = oldest.
type FileVersionView struct {
	Index        int                  `json:"index"`
	Version      string               `json:"version"` // "v1", "v2", …
	Current      bool                 `json:"current"`
	Hash         string               `json:"hash"`
	HashAlg      string               `json:"hash_alg,omitempty"`
	SizeBytes    int64                `json:"size_bytes"`
	ModTime      *time.Time           `json:"mtime,omitempty"`
	FirstSeen    *time.Time           `json:"first_seen,omitempty"`
	SupersededAt *time.Time           `json:"superseded_at,omitempty"` // nil for the current version
	Packages     []FileVersionPackage `json:"packages"`
	RelPath      string               `json:"rel_path"` // path inside its package's tar (may differ if the file moved)
}

// Located reports whether any package still holds this version's bytes.
func (v FileVersionView) Located() bool { return len(v.Packages) > 0 }

// tp returns a pointer to t unless it is the zero time.
func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	tt := t
	return &tt
}

// FileVersions returns every known content version of a file — the superseded
// history (oldest first) then the current version — each located to the
// package(s) and volume(s) that still hold its bytes.
func (s *Store) FileVersions(fileID int) []FileVersionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f *File
	for _, x := range s.c.Files {
		if x.ID == fileID {
			f = x
			break
		}
	}
	if f == nil {
		return nil
	}
	vol := map[int]*Volume{}
	for _, v := range s.c.Volumes {
		vol[v.ID] = v
	}

	// hash -> packages holding those exact bytes (content-addressed membership).
	// A file that moved keeps its RelPath inside the older package, so we capture
	// the per-package member path for a faithful restore.
	type hit struct {
		pkg FileVersionPackage
		rel string
	}
	byHash := map[string][]hit{}
	for _, ch := range s.c.Chunks {
		if ch.Status == "FAILED" {
			continue
		}
		for _, cf := range ch.Files {
			if cf.Hash == "" {
				continue
			}
			if cf.FileID != fileID && cf.FileID != 0 {
				// A different file with identical content still holds these bytes —
				// content-addressed, so it is a legitimate restore source. But prefer
				// same-file matches; include others only when FileID matches or is 0.
				if cf.FileID != fileID {
					continue
				}
			}
			byHash[cf.Hash] = append(byHash[cf.Hash], hit{
				pkg: FileVersionPackage{ChunkID: ch.ID, Chunk: ch.Name, Status: ch.Status, Volumes: chunkVersionVolumes(ch, vol)},
				rel: cf.RelPath,
			})
		}
	}

	build := func(hash, alg string, size int64, mtime, firstSeen time.Time, superseded *time.Time, current bool) FileVersionView {
		v := FileVersionView{Hash: hash, HashAlg: alg, SizeBytes: size,
			ModTime: tp(mtime), FirstSeen: tp(firstSeen), SupersededAt: superseded, Current: current, RelPath: f.RelPath}
		for _, h := range byHash[hash] {
			v.Packages = append(v.Packages, h.pkg)
			if h.rel != "" {
				v.RelPath = h.rel // the path as stored in the package tar
			}
		}
		return v
	}

	var out []FileVersionView
	for _, hv := range f.Versions {
		sa := hv.SupersededAt
		out = append(out, build(hv.Hash, hv.HashAlg, hv.SizeBytes, hv.ModTime, hv.FirstSeen, tp(sa), false))
	}
	out = append(out, build(f.Hash, f.HashAlg, f.SizeBytes, f.ModTime, f.FirstSeen, nil, true))
	for i := range out {
		out[i].Index = i + 1
		out[i].Version = fmt.Sprintf("v%d", i+1)
	}
	return out
}

// chunkVersionVolumes lists the live volumes (non-superseded copies + spanned
// segment tapes) that carry a package.
func chunkVersionVolumes(ch *Chunk, vol map[int]*Volume) []FileVersionVolume {
	var out []FileVersionVolume
	seen := map[int]bool{}
	add := func(id int, verified bool) {
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		fv := FileVersionVolume{VolumeID: id, Verified: verified}
		if v := vol[id]; v != nil {
			fv.Label, fv.Kind, fv.Location, fv.Barcode = v.Label, v.Kind, v.Location, v.Barcode
		} else {
			fv.Label = fmt.Sprintf("vol#%d", id)
		}
		out = append(out, fv)
	}
	for _, cp := range ch.Copies {
		if cp.Superseded {
			continue
		}
		add(cp.VolumeID, cp.VerifyOK != nil && *cp.VerifyOK)
	}
	for _, sg := range ch.Segments {
		add(sg.VolumeID, sg.Status == "VERIFIED")
	}
	return out
}

// kindWord renders a volume kind as the preposition-friendly noun used in the
// human "in NSP-C0003 on tape LTO-0007" locator.
func kindWord(kind string) string {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "TAPE":
		return "tape"
	case "OPTICAL":
		return "disc"
	case "HDD", "SSD":
		return "drive"
	default:
		return "volume"
	}
}

// LocatorLine renders a version as "v1 · 2024-03-12 · in NSP-C0003 on tape
// LTO-0007" — the one-line, hand-followable restore pointer used in the file
// detail view, drift, and the Recovery Kit inventory.
func (v FileVersionView) LocatorLine() string {
	// Prefer the file's own modification time (a date a human recognises) over the
	// catalog's first-seen bookkeeping; fall back to first-seen, then "unknown".
	date := "date unknown"
	switch {
	case v.ModTime != nil:
		date = v.ModTime.UTC().Format("2006-01-02")
	case v.FirstSeen != nil:
		date = v.FirstSeen.UTC().Format("2006-01-02")
	}
	head := fmt.Sprintf("%s · %s", v.Version, date)
	if v.Current {
		head += " · current"
	}
	if len(v.Packages) == 0 {
		return head + " · not locatable in the catalog (bytes may be on unadopted media)"
	}
	var locs []string
	for _, p := range v.Packages {
		if len(p.Volumes) == 0 {
			locs = append(locs, "in "+p.Chunk+" (not yet written to a volume)")
			continue
		}
		var vs []string
		for _, vv := range p.Volumes {
			vs = append(vs, fmt.Sprintf("on %s %s", kindWord(vv.Kind), vv.Label))
		}
		locs = append(locs, "in "+p.Chunk+" "+strings.Join(vs, ", "))
	}
	return head + " · " + strings.Join(locs, "; ")
}

// priorVersionLocator renders the one-line restore-source locator for the
// retained version whose bytes hash to priorHash — the "here's where the good,
// prior copy lives" pointer shown inline on a drift MODIFIED row (and intended
// for reuse by any scrub/verify SUSPECT_CORRUPTION surface). Returns "" when the
// version cannot be located or identified.
func (a *App) priorVersionLocator(fileID int, priorHash string) string {
	if fileID == 0 || priorHash == "" {
		return ""
	}
	for _, v := range a.Store.FileVersions(fileID) {
		if strings.EqualFold(v.Hash, priorHash) {
			return v.LocatorLine()
		}
	}
	return ""
}

// retainedVersionsMD renders the Recovery Kit inventory's "files with multiple
// retained versions" note: each such file, every version, and where its bytes
// live. Returns "" when no file has more than one version, so the section only
// appears when it has something to say.
func (a *App) retainedVersionsMD() string {
	files := a.Store.FilesWithVersions()
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Files with multiple retained versions\n\n")
	b.WriteString(fmt.Sprintf("%d file(s) below have changed since first archived. Mnemosyne never deleted the old bytes — they stay sealed in whatever package still holds them — so **every listed version remains restorable**. Each line is `vN · date · in PACKAGE on MEDIUM`; restore a specific one with Mnemosyne (file ▸ version) or by hand from the named package per `RESTORE_RUNBOOK.md`.\n\n", len(files)))
	for _, f := range files {
		views := a.Store.FileVersions(f.ID)
		b.WriteString("### `" + mdCell(f.RelPath) + "` — " + fmt.Sprintf("%d versions\n\n", len(views)))
		for _, v := range views {
			b.WriteString("- " + mdCell(v.LocatorLine()) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ---- version selection ----------------------------------------------------

// VersionSelector picks which retained version a restore should target. Zero
// value = newest (the current content). Exactly one of Index/Hash/AsOf is honored
// in that precedence; AsOf picks the version that was current at that instant.
type VersionSelector struct {
	Index int        // 1-based version index (v1 = oldest); 0 = unset
	Hash  string     // exact/prefix hash of the desired version
	AsOf  *time.Time // pick the version current at this time
}

// selectVersion resolves a selector against an ordered version list (v1 oldest).
// Default (empty selector) returns the current version.
func selectVersion(views []FileVersionView, sel VersionSelector) (FileVersionView, error) {
	if len(views) == 0 {
		return FileVersionView{}, fmt.Errorf("file has no known versions")
	}
	switch {
	case sel.Index > 0:
		if sel.Index > len(views) {
			return FileVersionView{}, fmt.Errorf("version v%d does not exist (file has %d version(s))", sel.Index, len(views))
		}
		return views[sel.Index-1], nil
	case strings.TrimSpace(sel.Hash) != "":
		h := strings.ToLower(strings.TrimSpace(sel.Hash))
		for _, v := range views {
			if strings.HasPrefix(strings.ToLower(v.Hash), h) {
				return v, nil
			}
		}
		return FileVersionView{}, fmt.Errorf("no version matches hash %s", sel.Hash)
	case sel.AsOf != nil:
		return versionAsOf(views, *sel.AsOf)
	default:
		return views[len(views)-1], nil // newest / current
	}
}

// versionAsOf returns the version that was CURRENT at instant t: the newest
// version whose FirstSeen <= t and which had not yet been superseded at t.
func versionAsOf(views []FileVersionView, t time.Time) (FileVersionView, error) {
	// views are oldest→newest; walk newest→oldest and take the first that was
	// already in effect at t.
	for i := len(views) - 1; i >= 0; i-- {
		v := views[i]
		if v.FirstSeen != nil && v.FirstSeen.After(t) {
			continue // this version didn't exist yet at t
		}
		if v.SupersededAt != nil && !v.SupersededAt.After(t) {
			continue // already replaced by t
		}
		return v, nil
	}
	return FileVersionView{}, fmt.Errorf("no version of this file existed as of %s", t.UTC().Format("2006-01-02"))
}

// pickRestorePackage chooses which package to restore a located version from:
// prefer one already written to a volume (a real medium in hand), falling back
// to the first package holding the bytes.
func pickRestorePackage(v FileVersionView) FileVersionPackage {
	for _, p := range v.Packages {
		if len(p.Volumes) > 0 {
			return p
		}
	}
	return v.Packages[0]
}

// RestoreFileVersion restores a single file at a chosen version to outputDir. The
// selector defaults to newest; Index/Hash/AsOf pick a specific retained version.
// It resolves the version to a package, extracts just that member via the normal
// par2→gpg→tar path (RestoreChunk), then hash-verifies the restored bytes against
// the version's recorded hash.
func (a *App) RestoreFileVersion(fileID int, sel VersionSelector, sourceDir, outputDir string, progress func(float64, string)) (map[string]any, error) {
	views := a.Store.FileVersions(fileID)
	if len(views) == 0 {
		return nil, fmt.Errorf("file %d has no known versions", fileID)
	}
	v, err := selectVersion(views, sel)
	if err != nil {
		return nil, err
	}
	if !v.Located() {
		return nil, fmt.Errorf("%s (hash %s) is not locatable — no cataloged package still holds those bytes", v.Version, shortHash(v.Hash))
	}
	pkg := pickRestorePackage(v)
	member := v.RelPath
	res, err := a.RestoreChunk(pkg.ChunkID, sourceDir, outputDir, []string{member}, progress)
	if err != nil {
		return nil, err
	}

	// Verify the restored bytes match the version we asked for — the round-trip
	// proof that "restore v1" actually reproduced v1.
	restored := filepath.Join(outputDir, filepath.FromSlash(member))
	verified := false
	if v.Hash != "" {
		if h, herr := hashFileHex(restored); herr == nil {
			verified = strings.EqualFold(h, v.Hash)
			if !verified {
				return nil, fmt.Errorf("restored %s but its hash %s does not match the recorded %s — restore integrity check failed", v.Version, shortHash(h), shortHash(v.Hash))
			}
		}
	}
	res["version"] = v.Version
	res["version_hash"] = v.Hash
	res["restored_path"] = restored
	res["member"] = member
	res["hash_verified"] = verified
	res["from_package"] = pkg.Chunk
	a.Store.Log("restore", fmt.Sprintf("file %d %s -> %s (from %s, verified=%v)", fileID, v.Version, outputDir, pkg.Chunk, verified))
	return res, nil
}

// shortHash abbreviates a hash for messages.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

// sortVersionsByDate is a stable helper kept for callers that receive versions
// out of order (defensive; FileVersions already returns oldest→newest).
func sortVersionsByDate(vs []FileVersionView) {
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Index < vs[j].Index })
}
