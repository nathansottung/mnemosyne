package main

// snapshot.go — reading a drive's SNAPSHOT back out. Two things live here:
//
//  1. driveReport — the per-drive ingest report: role breakdown, how much of the
//     drive is already on OTHER cataloged drives ("6,212 of 8,431 files already
//     exist on DRIVE-02"), which folders substantially overlap another drive, and
//     whether this drive is a near-exact MIRROR of another — with the location-aware
//     verdict (two mirrors in the SAME place = redundancy at risk; different places
//     = a healthy offsite pair).
//
//  2. snapshotTreemap — the same squarified treemap the archive view uses, but
//     computed from a drive's stored snapshot instead of the disk, colored by file
//     ROLE. This is what lets the Volumes view browse an UNPLUGGED drive's full
//     tree. Like treemap.go, it never touches the disk — only the catalog.
//
// Everything here reads snapshots only; nothing writes.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Overlap thresholds. A FOLDER ≥90% hash-shared with another drive is flagged as
// overlap; two DRIVES ≥98% content-identical (Jaccard over deduped file hashes)
// are flagged a MIRROR pair.
const (
	folderOverlapThreshold = 0.90
	driveMirrorThreshold   = 0.98
)

// RoleStat is one workflow-role row of a drive's content (RAW, VIDEO, …).
type RoleStat struct {
	Role     string `json:"role"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Critical bool   `json:"critical,omitempty"` // CATALOG role — losing it loses edit/organizational state
}

// DrivePeer is another cataloged drive this one shares content with. Shared* count
// THIS drive's files whose content (by hash) also lives on the peer; ContainPct is
// shared/this-total. IdenticalPct is the symmetric Jaccard (for mirror detection).
type DrivePeer struct {
	VolumeID     int     `json:"volume_id"`
	Label        string  `json:"label"`
	Location     string  `json:"location,omitempty"`
	SharedFiles  int     `json:"shared_files"`
	SharedBytes  int64   `json:"shared_bytes"`
	ContainPct   float64 `json:"contain_pct"`
	IdenticalPct float64 `json:"identical_pct"`
	Mirror       bool    `json:"mirror,omitempty"`
}

// FolderOverlap is one folder on this drive that is largely duplicated on a peer.
type FolderOverlap struct {
	Path      string  `json:"path"`
	PeerLabel string  `json:"peer_label"`
	Files     int     `json:"files"`
	SharedPct float64 `json:"shared_pct"`
}

// MirrorVerdict is the location-aware call when this drive mirrors another: two
// copies in the SAME location are redundancy at risk (one incident loses both);
// copies in DIFFERENT locations are a healthy offsite pair.
type MirrorVerdict struct {
	PeerVolumeID int     `json:"peer_volume_id"`
	PeerLabel    string  `json:"peer_label"`
	IdenticalPct float64 `json:"identical_pct"`
	SameLocation bool    `json:"same_location"`
	Location     string  `json:"location,omitempty"`
	PeerLocation string  `json:"peer_location,omitempty"`
	AtRisk       bool    `json:"at_risk"`
	Verdict      string  `json:"verdict"`
}

// DriveReport is the full post-ingest report for one drive.
type DriveReport struct {
	VolumeID       int             `json:"volume_id"`
	Label          string          `json:"label"`
	CapturedAt     string          `json:"captured_at,omitempty"`
	TotalFiles     int             `json:"total_files"`
	TotalBytes     int64           `json:"total_bytes"`
	Unreadable     int             `json:"unreadable,omitempty"`
	Roles          []RoleStat      `json:"roles"`
	DuplicateOf    *DrivePeer      `json:"duplicate_of,omitempty"` // peer sharing the most files with this drive
	Peers          []DrivePeer     `json:"peers,omitempty"`        // every peer with any shared content, most-shared first
	FolderOverlaps []FolderOverlap `json:"folder_overlaps,omitempty"`
	Mirror         *MirrorVerdict  `json:"mirror,omitempty"`
	Notes          []string        `json:"notes,omitempty"` // ready-to-show one-liners
}

// driveReport builds the per-drive ingest report for a stored snapshot, comparing
// it against every OTHER drive's snapshot in the catalog.
func (a *App) driveReport(snap *VolumeSnapshot) DriveReport {
	rep := DriveReport{VolumeID: snap.VolumeID, Label: snap.Label,
		TotalFiles: snap.TotalFiles, TotalBytes: snap.TotalBytes, Unreadable: snap.Unreadable}
	if !snap.CapturedAt.IsZero() {
		rep.CapturedAt = snap.CapturedAt.Format("2006-01-02 15:04")
	}

	// Role breakdown (sorted by bytes desc).
	reg := a.formatRegistry()
	for _, role := range snap.roleList() {
		rep.Roles = append(rep.Roles, RoleStat{Role: role,
			Files: snap.RoleFiles[role], Bytes: snap.RoleBytes[role], Critical: roleCritical(reg, role)})
	}
	sort.Slice(rep.Roles, func(i, j int) bool { return rep.Roles[i].Bytes > rep.Roles[j].Bytes })
	for _, r := range rep.Roles {
		if r.Critical && r.Files > 0 {
			rep.Notes = append(rep.Notes, fmt.Sprintf("⚠ %d CATALOG file(s) (%s) — edit/organizational state, not the photos; keep the originals, they are the archive.",
				r.Files, r.Role))
		}
	}

	// This drive's own hash sets: dedup set (for Jaccard) + per-file list preserved
	// in snap.Files (for the "N of M files" count and folder overlap).
	thisSet := map[string]bool{}
	for _, f := range snap.Files {
		if f.Hash != "" {
			thisSet[f.Hash] = true
		}
	}

	// Compare against every other drive snapshot.
	for _, peer := range a.Store.VolumeSnapshots() {
		if peer.VolumeID == snap.VolumeID || len(peer.Files) == 0 {
			continue
		}
		peerSet := map[string]bool{}
		for _, f := range peer.Files {
			if f.Hash != "" {
				peerSet[f.Hash] = true
			}
		}
		// How many of THIS drive's files (by content) already live on the peer.
		shared, sharedBytes := 0, int64(0)
		for _, f := range snap.Files {
			if f.Hash != "" && peerSet[f.Hash] {
				shared++
				sharedBytes += f.SizeBytes
			}
		}
		if shared == 0 {
			continue
		}
		inter := 0
		for h := range thisSet {
			if peerSet[h] {
				inter++
			}
		}
		union := len(thisSet) + len(peerSet) - inter
		jaccard := 0.0
		if union > 0 {
			jaccard = float64(inter) / float64(union)
		}
		p := DrivePeer{VolumeID: peer.VolumeID, Label: peer.Label, Location: a.volumeLocationName(peer.VolumeID),
			SharedFiles: shared, SharedBytes: sharedBytes,
			ContainPct: pct(shared, snap.TotalFiles), IdenticalPct: round1(jaccard * 100),
			Mirror: jaccard >= driveMirrorThreshold}
		rep.Peers = append(rep.Peers, p)
	}
	sort.Slice(rep.Peers, func(i, j int) bool {
		if rep.Peers[i].SharedFiles != rep.Peers[j].SharedFiles {
			return rep.Peers[i].SharedFiles > rep.Peers[j].SharedFiles
		}
		return rep.Peers[i].Label < rep.Peers[j].Label
	})

	if len(rep.Peers) > 0 {
		top := rep.Peers[0]
		rep.DuplicateOf = &top
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s of %s files already exist on %s.",
			commaInt(top.SharedFiles), commaInt(snap.TotalFiles), top.Label))
		rep.FolderOverlaps = folderOverlaps(snap, a.peerHashSet(top.VolumeID), top.Label)
		for _, fo := range rep.FolderOverlaps {
			rep.Notes = append(rep.Notes, fmt.Sprintf("Folder “%s” is %.0f%% already on %s.", fo.Path, fo.SharedPct, fo.PeerLabel))
		}
	}

	// Mirror detection: the most content-identical peer at/above the threshold.
	rep.Mirror = a.mirrorVerdict(snap, rep.Peers)
	if rep.Mirror != nil {
		rep.Notes = append(rep.Notes, rep.Mirror.Verdict)
	}
	return rep
}

// mirrorVerdict returns the location-aware MIRROR call for the most-identical peer
// at/above the mirror threshold, or nil if this drive mirrors nothing.
func (a *App) mirrorVerdict(snap *VolumeSnapshot, peers []DrivePeer) *MirrorVerdict {
	var best *DrivePeer
	for i := range peers {
		if peers[i].Mirror && (best == nil || peers[i].IdenticalPct > best.IdenticalPct) {
			best = &peers[i]
		}
	}
	if best == nil {
		return nil
	}
	thisLoc := a.volumeLocationName(snap.VolumeID)
	peerLoc := a.volumeLocationName(best.VolumeID)
	same := a.sameLocation(snap.VolumeID, best.VolumeID)
	mv := &MirrorVerdict{PeerVolumeID: best.VolumeID, PeerLabel: best.Label, IdenticalPct: best.IdenticalPct,
		SameLocation: same, Location: thisLoc, PeerLocation: peerLoc}
	locName := thisLoc
	if locName == "" {
		locName = "the same place"
	}
	if same {
		mv.AtRisk = true
		mv.Verdict = fmt.Sprintf("⚠ MIRROR of %s (%.0f%% identical), but redundancy is AT RISK — both copies are in %s. One incident there loses both.",
			best.Label, best.IdenticalPct, locName)
	} else {
		mv.Verdict = fmt.Sprintf("✓ MIRROR of %s (%.0f%% identical) — a healthy offsite pair (%s vs %s).",
			best.Label, best.IdenticalPct, orDash(thisLoc), orDash(peerLoc))
	}
	return mv
}

// folderOverlaps flags each folder on this drive whose files are ≥ the threshold
// already present (by hash) on the given peer.
func folderOverlaps(snap *VolumeSnapshot, peerSet map[string]bool, peerLabel string) []FolderOverlap {
	type fc struct{ total, shared int }
	byFolder := map[string]*fc{}
	for _, f := range snap.Files {
		dir := filepath.ToSlash(filepath.Dir(f.RelPath))
		if dir == "." {
			dir = "(root)"
		}
		c := byFolder[dir]
		if c == nil {
			c = &fc{}
			byFolder[dir] = c
		}
		c.total++
		if f.Hash != "" && peerSet[f.Hash] {
			c.shared++
		}
	}
	var out []FolderOverlap
	for dir, c := range byFolder {
		if c.total == 0 {
			continue
		}
		frac := float64(c.shared) / float64(c.total)
		if frac >= folderOverlapThreshold {
			out = append(out, FolderOverlap{Path: dir, PeerLabel: peerLabel, Files: c.total, SharedPct: round1(frac * 100)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// peerHashSet returns the deduped content-hash set of a peer drive's snapshot.
func (a *App) peerHashSet(volumeID int) map[string]bool {
	set := map[string]bool{}
	if s := a.Store.VolumeSnapshot(volumeID); s != nil {
		for _, f := range s.Files {
			if f.Hash != "" {
				set[f.Hash] = true
			}
		}
	}
	return set
}

// sameLocation reports whether two volumes currently live in the same assigned
// location. Unassigned (LocationID 0) is treated as unknown — never "same".
func (a *App) sameLocation(volA, volB int) bool {
	va, vb := a.Store.Volume(volA), a.Store.Volume(volB)
	if va == nil || vb == nil || va.LocationID == 0 || vb.LocationID == 0 {
		return false
	}
	return va.LocationID == vb.LocationID
}

// volumeLocationName returns a volume's current location name (falling back to the
// free-text Location, then empty).
func (a *App) volumeLocationName(volumeID int) string {
	v := a.Store.Volume(volumeID)
	if v == nil {
		return ""
	}
	if v.LocationID != 0 {
		if l := a.Store.Location(v.LocationID); l != nil {
			return l.Name
		}
	}
	return strings.TrimSpace(v.Location)
}

// roleList returns the roles present in a snapshot (stable order).
func (s *VolumeSnapshot) roleList() []string {
	var roles []string
	for r := range s.RoleFiles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return roles
}

// roleCritical reports whether a role is the CRITICAL catalog role. It checks the
// registry so a user override that marks a role critical is honored, with a
// constant fallback for the built-in CATALOG role.
func roleCritical(reg map[string]FormatEntry, role string) bool {
	if role == RoleProject {
		return true
	}
	for _, e := range reg {
		if e.Role == role && e.Critical {
			return true
		}
	}
	return false
}

// ---- snapshot treemap (offline, colored by role) -----------------------

// roleSeverity ranks roles for the folder worst-of-children rollup so the most
// consequential role in a folder wins its color: PROJECT-FILES (critical) >
// ORIGINALS > DELIVERABLES > SIDECARS > OTHER.
func roleSeverity(role string) int {
	switch role {
	case RoleProject:
		return 4
	case RoleOriginals:
		return 3
	case RoleDeliverables:
		return 2
	case RoleSidecars:
		return 1
	}
	return 0
}

// snapshotTreemap computes one zoom level of a drive's treemap from its stored
// snapshot — no disk access — colored by file role. dirPath "" is the drive root.
func snapshotTreemap(snap *VolumeSnapshot, dirPath string) TreemapResult {
	res := TreemapResult{Path: dirPath, ColorBy: "role", StatusBytes: map[string]int64{}}
	if snap == nil {
		return res
	}
	res.Name = snap.Label

	P := strings.TrimRight(filepath.ToSlash(dirPath), "/")
	nP := normPath(P)
	atRoot := P == ""

	children := map[string]*treemapAgg{}
	get := func(key, name, cpath string, isDir bool) *treemapAgg {
		a := children[key]
		if a == nil {
			a = &treemapAgg{name: name, path: cpath, isDir: isDir, worstSev: -1, statusBytes: map[string]int64{}}
			children[key] = a
		}
		return a
	}

	for _, f := range snap.Files {
		full := filepath.ToSlash(f.RelPath)
		var childName, childPath, childKey string
		var childIsDir bool
		if atRoot {
			rest := full
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				childName, childPath, childIsDir = rest[:i], rest[:i], true
			} else {
				childName, childPath, childIsDir = rest, full, false
			}
			childKey = normPath(childPath)
		} else {
			nFull := normPath(full)
			if nFull != nP && !strings.HasPrefix(nFull, nP+"/") {
				continue
			}
			if len(full) <= len(P)+1 {
				continue
			}
			rest := full[len(P)+1:]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				childName, childPath, childIsDir = rest[:i], P+"/"+rest[:i], true
			} else {
				childName, childPath, childIsDir = rest, full, false
			}
			childKey = normPath(childPath)
		}

		st := f.Role
		if st == "" {
			st = RoleOther
		}
		res.Size += f.SizeBytes
		res.Files++
		res.StatusBytes[st] += f.SizeBytes

		agg := get(childKey, childName, childPath, childIsDir)
		agg.size += f.SizeBytes
		agg.statusBytes[st] += f.SizeBytes
		if childIsDir {
			agg.files++
		} else {
			agg.files = 1
		}
		if sv := roleSeverity(st); sv > agg.worstSev {
			agg.worstSev, agg.worst = sv, st
		}
	}

	// Materialize, sort by size desc, fold the small tail into "other" (same policy
	// as the archive treemap).
	all := make([]*treemapAgg, 0, len(children))
	for _, a := range children {
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].size != all[j].size {
			return all[i].size > all[j].size
		}
		return all[i].name < all[j].name
	})
	threshold := int64(float64(res.Size) * treemapMinFraction)
	var other *treemapAgg
	for i, a := range all {
		if i < treemapMaxChildren && a.size >= threshold {
			res.Children = append(res.Children, a.node())
			continue
		}
		if other == nil {
			other = &treemapAgg{name: "other", isDir: true, worstSev: -1, statusBytes: map[string]int64{}}
		}
		other.size += a.size
		other.files += a.files
		for k, v := range a.statusBytes {
			other.statusBytes[k] += v
			if sv := roleSeverity(k); sv > other.worstSev {
				other.worstSev, other.worst = sv, k
			}
		}
		res.Folded++
	}
	if other != nil {
		n := other.node()
		n.Other, n.HasChildren = true, false
		res.Children = append(res.Children, n)
	}

	res.Crumbs = snapshotCrumbs(res.Name, P)
	return res
}

// snapshotCrumbs builds the zoom breadcrumb for a snapshot treemap: the drive
// itself, then each path segment down to P.
func snapshotCrumbs(driveName, P string) []TreemapCrumb {
	crumbs := []TreemapCrumb{{Name: driveName, Path: ""}}
	if P == "" {
		return crumbs
	}
	cur := ""
	for _, seg := range strings.Split(P, "/") {
		if seg == "" {
			continue
		}
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		crumbs = append(crumbs, TreemapCrumb{Name: seg, Path: cur})
	}
	return crumbs
}

// ---- small helpers -----------------------------------------------------

func pct(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return round1(float64(n) / float64(total) * 100)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unassigned"
	}
	return s
}

// commaInt renders an int with thousands separators ("8431" → "8,431").
func commaInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
