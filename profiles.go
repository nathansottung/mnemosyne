package main

// profiles.go — user-customizable protection Profiles and the canonical
// six-status protection model.
//
// The real 3-2-1 rule has THREE dimensions, and a Profile expresses all three:
//   copies       — how many independent physical copies (the "3")
//   media kinds  — across how many DISTINCT kinds of medium (the "2")
//   offsite      — how many of those copies live somewhere else (the "1")
//
// Only Level-B-verified copies count toward requirements. With tiered
// verification (see verify_levels.go), a copy's VerifyOK/LastVerifiedAt are set
// only by a full-content (level-B) pass — advisory A/C checks never touch them —
// so keying qualification on VerifyOK==true is exactly "Level-B-verified".
// Offsite is a property of the Volume, not the Profile, because
// "is this copy in another building?" is a fact about the physical medium.

import (
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Profile is a named protection policy. Built-ins are immutable but duplicatable
// as a starting point for custom ones. MediaKindsAllowed empty means "any kind".
type Profile struct {
	ID                         string   `json:"id"`
	Name                       string   `json:"name"`
	Description                string   `json:"description"`
	RequiredCopies             int      `json:"required_copies"`
	RequiredDistinctMediaKinds int      `json:"required_distinct_media_kinds"`
	RequiredOffsiteCopies      int      `json:"required_offsite_copies"`
	MediaKindsAllowed          []string `json:"media_kinds_allowed"` // empty = any
	VerifyDueMonths            int      `json:"verify_due_months"`
	Builtin                    bool     `json:"builtin"`
}

// Assignment binds a profile to an Archive (Path "") or a folder path within it.
// Resolution is nearest-ancestor-wins: a folder without its own assignment
// inherits the closest ancestor's, ultimately the Archive's.
type Assignment struct {
	CollectionID int    `json:"collection_id"`
	Path         string `json:"path"` // "" = archive-level; else a folder path within the archive
	ProfileID    string `json:"profile_id"`
}

// DefaultProfileID is assigned to every new Archive — the canonical 3-2-1 rule.
const DefaultProfileID = "3-2-1-standard"

// builtinProfiles are the three shipped profiles. Kept canonical on every store
// open (they are immutable); users duplicate one to start a custom profile.
func builtinProfiles() []*Profile {
	return []*Profile{
		{ID: "single-copy", Name: "Single Copy",
			Description:    "low-value / re-creatable data",
			RequiredCopies: 1, RequiredDistinctMediaKinds: 1, RequiredOffsiteCopies: 0,
			VerifyDueMonths: 12, Builtin: true},
		{ID: DefaultProfileID, Name: "3-2-1 Standard",
			Description:    "the canonical rule; default for new Archives",
			RequiredCopies: 3, RequiredDistinctMediaKinds: 2, RequiredOffsiteCopies: 1,
			VerifyDueMonths: 12, Builtin: true},
		{ID: "pre-deletion-hold", Name: "Pre-Deletion Hold",
			Description:    "over-protect data whose SOURCE is about to be deleted",
			RequiredCopies: 4, RequiredDistinctMediaKinds: 2, RequiredOffsiteCopies: 1,
			VerifyDueMonths: 6, Builtin: true},
	}
}

// The six canonical protection statuses. Every file gets exactly one; a folder
// takes the worst status among its descendant files.
const (
	StatusUnassigned   = "UNASSIGNED"    // no profile resolves
	StatusNotBackedUp  = "NOT_BACKED_UP" // 0 qualifying copies
	StatusPartial      = "PARTIAL"       // some protection, at least one dimension short
	StatusComplete     = "COMPLETE"      // all three dimensions met, all verifies current
	StatusOverComplete = "OVER_COMPLETE" // exceeds requirements
	StatusOutOfPolicy  = "OUT_OF_POLICY" // disallowed media, stale verify, or invalidated compliance
)

// AllStatuses is the canonical order (best → worst) for stable UI rendering.
var AllStatuses = []string{
	StatusComplete, StatusOverComplete, StatusPartial,
	StatusNotBackedUp, StatusOutOfPolicy, StatusUnassigned,
}

// statusSeverity ranks statuses for folder worst-of-children aggregation: higher
// is worse (less protected). OVER_COMPLETE is better than COMPLETE, so a folder
// mixing the two reports COMPLETE.
func statusSeverity(s string) int {
	switch s {
	case StatusOverComplete:
		return 0
	case StatusComplete:
		return 1
	case StatusUnassigned:
		return 2
	case StatusPartial:
		return 3
	case StatusOutOfPolicy:
		return 4
	case StatusNotBackedUp:
		return 5
	}
	return 2
}

// slugify turns a profile name into a stable, url-safe id fragment.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// pathIsAncestor reports whether anc names an ancestor of (or equals) p. An empty
// anc is the archive-level root and is an ancestor of everything. Comparison is
// path-normalised (case-folded on Windows, forward-slashed).
func pathIsAncestor(anc, p string) bool {
	if strings.TrimSpace(anc) == "" {
		return true
	}
	a, b := normPath(anc), normPath(p)
	return a == b || strings.HasPrefix(b, a+"/")
}

// containsFold reports whether list contains s (case-insensitive).
func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

// isVerifyStale reports whether a verification at t is older than months. A nil
// time (never recorded) counts as stale; months <= 0 disables staleness.
func isVerifyStale(t *time.Time, months int) bool {
	if months <= 0 {
		return false
	}
	if t == nil {
		return true
	}
	return t.Before(time.Now().AddDate(0, -months, 0))
}

// ---- status derivation --------------------------------------------------

// physCopy is one independent physical instance of a package's payload, as it
// bears on protection: which media kind(s) it touches, whether it is offsite,
// and when it last verified. A non-spanned copy touches one volume (one kind); a
// spanned copy touches its segment volumes (usually all one kind).
type physCopy struct {
	kinds      map[string]bool
	offsite    bool
	verifiedAt *time.Time
}

// chunkPhysCopies returns the verified physical copies of a chunk, keyed by a
// signature that dedups the same volume across chunks. Only VerifyOK copies (and
// fully-verified spanned chunks) qualify — the "counts toward requirements" bar.
func chunkPhysCopies(ch *Chunk, vols map[int]*Volume) map[string]physCopy {
	out := map[string]physCopy{}
	if ch.Spanned {
		// A spanned chunk is ONE logical copy spread across its segment media.
		allVerified := len(ch.Segments) > 0
		kinds := map[string]bool{}
		offsite := false
		var segVols []int
		for _, sg := range ch.Segments {
			if sg.Status != "VERIFIED" {
				allVerified = false
			}
			if v := vols[sg.VolumeID]; v != nil {
				kinds[v.Kind] = true
				if v.Offsite {
					offsite = true
				}
				segVols = append(segVols, sg.VolumeID)
			}
		}
		if allVerified && len(kinds) > 0 {
			sort.Ints(segVols)
			parts := make([]string, len(segVols))
			for i, id := range segVols {
				parts[i] = strconv.Itoa(id)
			}
			out["span:"+strings.Join(parts, "-")] = physCopy{kinds: kinds, offsite: offsite, verifiedAt: ch.VerifiedAt}
		}
		return out
	}
	for _, cp := range ch.Copies {
		if cp.Superseded || cp.VerifyOK == nil || !*cp.VerifyOK {
			continue
		}
		v := vols[cp.VolumeID]
		if v == nil {
			continue
		}
		out[strconv.Itoa(cp.VolumeID)] = physCopy{
			kinds:      map[string]bool{v.Kind: true},
			offsite:    v.Offsite,
			verifiedAt: cp.LastVerifiedAt,
		}
	}
	return out
}

// fileDims are a file's protection numbers, evaluated across the three 3-2-1
// dimensions plus the two out-of-policy triggers.
type fileDims struct {
	copies     int
	kinds      int
	offsite    int
	disallowed bool // a qualifying copy sits on a disallowed media kind
	stale      bool // a qualifying copy's verify is older than verify_due_months
}

func dimsFromCopies(cps map[string]physCopy, prof *Profile) fileDims {
	d := fileDims{}
	kindSet := map[string]bool{}
	for _, pc := range cps {
		d.copies++
		if pc.offsite {
			d.offsite++
		}
		if isVerifyStale(pc.verifiedAt, prof.VerifyDueMonths) {
			d.stale = true
		}
		for k := range pc.kinds {
			kindSet[k] = true
			if len(prof.MediaKindsAllowed) > 0 && !containsFold(prof.MediaKindsAllowed, k) {
				d.disallowed = true
			}
		}
	}
	d.kinds = len(kindSet)
	return d
}

// statusFor derives the single canonical status for a resolved profile + dims.
func statusFor(prof *Profile, d fileDims) string {
	if prof == nil {
		return StatusUnassigned
	}
	if d.copies == 0 {
		return StatusNotBackedUp
	}
	if d.disallowed || d.stale {
		return StatusOutOfPolicy
	}
	metC := d.copies >= prof.RequiredCopies
	metK := d.kinds >= prof.RequiredDistinctMediaKinds
	metO := d.offsite >= prof.RequiredOffsiteCopies
	if metC && metK && metO {
		if d.copies > prof.RequiredCopies || d.kinds > prof.RequiredDistinctMediaKinds || d.offsite > prof.RequiredOffsiteCopies {
			return StatusOverComplete
		}
		return StatusComplete
	}
	return StatusPartial
}

// statusDetail is the human breakdown the UI shows next to the status, e.g.
// "2/3 copies · kinds ok · 0/1 offsite". For met dimensions it reads "… ok".
func statusDetail(prof *Profile, d fileDims, status string) string {
	switch status {
	case StatusUnassigned:
		return "no profile assigned"
	case StatusNotBackedUp:
		return "0 qualifying copies"
	case StatusOutOfPolicy:
		reasons := []string{}
		if d.disallowed {
			reasons = append(reasons, "copy on disallowed media")
		}
		if d.stale {
			reasons = append(reasons, "verify overdue")
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "compliance invalidated by a profile/assignment change")
		}
		return strings.Join(reasons, " · ")
	}
	dim := func(have, req int, label string) string {
		if have >= req {
			return label + " ok"
		}
		return strconv.Itoa(have) + "/" + strconv.Itoa(req) + " " + label
	}
	return strings.Join([]string{
		dim(d.copies, prof.RequiredCopies, "copies"),
		dim(d.kinds, prof.RequiredDistinctMediaKinds, "kinds"),
		dim(d.offsite, prof.RequiredOffsiteCopies, "offsite"),
	}, " · ")
}

// ---- per-collection computation -----------------------------------------

// ProtectionNode is one directory in an archive's protection tree.
type ProtectionNode struct {
	Path        string         `json:"path"`         // full logical path
	Name        string         `json:"name"`         // last segment, for display
	Depth       int            `json:"depth"`        // segments below the scanned root
	ProfileID   string         `json:"profile_id"`   // resolved profile ("" = unassigned)
	ProfileName string         `json:"profile_name"` // resolved profile name
	Inherited   bool           `json:"inherited"`    // true = inherited from an ancestor, not explicit here
	Status      string         `json:"status"`       // worst status among descendant files
	Detail      string         `json:"detail"`       // breakdown for the worst status
	Files       int            `json:"files"`        // files in this subtree
	Counts      map[string]int `json:"counts"`       // status -> file count in subtree
}

// ProtectionSummary is the persisted, lightweight per-collection status tally —
// what the dashboard reads and the recompute job diffs for its "newly PARTIAL /
// OUT_OF_POLICY" toast.
type ProtectionSummary struct {
	CollectionID int            `json:"collection_id"`
	At           time.Time      `json:"at"`
	Files        map[string]int `json:"files"` // status -> file count
}

// ProtectionResult is the full computed view for one collection.
type ProtectionResult struct {
	CollectionID   int               `json:"collection_id"`
	ArchiveProfile *Profile          `json:"archive_profile"` // resolved archive-level profile (nil = unassigned)
	Summary        map[string]int    `json:"summary"`         // file status counts
	FolderCounts   map[string]int    `json:"folder_counts"`   // emitted-folder status counts
	Nodes          []*ProtectionNode `json:"nodes"`
	TotalFiles     int               `json:"total_files"`
}

type dirAgg struct {
	root      string // the scanned folder root this dir belongs under
	counts    map[string]int
	files     int
	example   map[string]string // status -> a representative detail
	profile   *Profile
	inherited bool
}

// Protection computes the six-status protection view for one collection: a
// per-file status derived against each file's RESOLVED profile across all three
// 3-2-1 dimensions, aggregated up the directory tree (folders take the worst
// status among their descendants). Acquires the store lock and reads the catalog
// directly, like Search.
func (s *Store) Protection(collectionID int) ProtectionResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	vols := map[int]*Volume{}
	for _, v := range s.c.Volumes {
		vols[v.ID] = v
	}
	// fileID -> deduped verified physical copies (across every non-FAILED chunk).
	fileCopies := map[int]map[string]physCopy{}
	for _, ch := range s.c.Chunks {
		if ch.CollectionID != collectionID || ch.Status == "FAILED" {
			continue
		}
		pcs := chunkPhysCopies(ch, vols)
		if len(pcs) == 0 {
			continue
		}
		for _, cf := range ch.Files {
			m := fileCopies[cf.FileID]
			if m == nil {
				m = map[string]physCopy{}
				fileCopies[cf.FileID] = m
			}
			for sig, pc := range pcs {
				m[sig] = pc
			}
		}
	}

	folderPath := map[int]string{}
	for _, fo := range s.c.Folders {
		if fo.CollectionID == collectionID {
			folderPath[fo.ID] = filepath.ToSlash(fo.Path)
		}
	}

	summary := map[string]int{}
	dirs := map[string]*dirAgg{}
	total := 0
	profCache := map[string]*Profile{}
	resolve := func(p string) *Profile {
		if v, ok := profCache[p]; ok {
			return v
		}
		v := s.resolveProfileLocked(collectionID, p)
		profCache[p] = v
		return v
	}

	for _, f := range s.c.Files {
		if f.CollectionID != collectionID {
			continue
		}
		root := folderPath[f.FolderID]
		full := filepath.ToSlash(filepath.Join(root, f.RelPath))
		fileDir := path.Dir(full)
		prof := resolve(fileDir)
		dims := dimsFromCopies(fileCopies[f.ID], prof)
		st := statusFor(prof, dims)
		detail := statusDetail(prof, dims, st)
		summary[st]++
		total++
		for _, d := range dirAncestors(fileDir, root) {
			ag := dirs[d]
			if ag == nil {
				dp := resolve(d)
				ag = &dirAgg{root: root, counts: map[string]int{}, example: map[string]string{}, profile: dp}
				ag.inherited = !s.hasExplicitAssignmentLocked(collectionID, d)
				dirs[d] = ag
			}
			ag.counts[st]++
			ag.files++
			if _, seen := ag.example[st]; !seen {
				ag.example[st] = detail
			}
		}
	}

	// Emit nodes: every scanned root, everything within two levels of a root, and
	// any directory carrying its own explicit assignment (so assigned subfolders
	// are always visible however deep).
	folderCounts := map[string]int{}
	nodes := make([]*ProtectionNode, 0, len(dirs))
	for dp, ag := range dirs {
		depth := segDepth(dp, ag.root)
		if depth > 2 && ag.inherited {
			continue
		}
		st := worstStatus(ag.counts)
		node := &ProtectionNode{
			Path: dp, Name: path.Base(dp), Depth: depth,
			Inherited: ag.inherited, Status: st, Detail: ag.example[st],
			Files: ag.files, Counts: ag.counts,
		}
		if ag.profile != nil {
			node.ProfileID, node.ProfileName = ag.profile.ID, ag.profile.Name
		}
		nodes = append(nodes, node)
		folderCounts[st]++
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })

	return ProtectionResult{
		CollectionID:   collectionID,
		ArchiveProfile: s.resolveProfileLocked(collectionID, ""),
		Summary:        summary,
		FolderCounts:   folderCounts,
		Nodes:          nodes,
		TotalFiles:     total,
	}
}

// fileProtectionLocked derives the six-status + detail + resolved profile for one
// file, given precomputed volume/copy/folder-path maps. Caller holds s.mu. Shared
// by the protection view and search so both speak the same six-status language.
func (s *Store) fileProtectionLocked(f *File, fileCopies map[int]map[string]physCopy, folderPath map[int]string) (string, string, *Profile) {
	root := folderPath[f.FolderID]
	full := filepath.ToSlash(filepath.Join(root, f.RelPath))
	prof := s.resolveProfileLocked(f.CollectionID, path.Dir(full))
	dims := dimsFromCopies(fileCopies[f.ID], prof)
	st := statusFor(prof, dims)
	return st, statusDetail(prof, dims, st), prof
}

func (s *Store) hasExplicitAssignmentLocked(collectionID int, p string) bool {
	np := normPath(p)
	for _, a := range s.c.Assignments {
		if a.CollectionID == collectionID && normPath(a.Path) == np {
			return true
		}
	}
	return false
}

// worstStatus returns the highest-severity status present in a counts map.
func worstStatus(counts map[string]int) string {
	best, bestSev := StatusComplete, -1
	for st, n := range counts {
		if n == 0 {
			continue
		}
		if sv := statusSeverity(st); sv > bestSev {
			bestSev, best = sv, st
		}
	}
	return best
}

// dirAncestors returns fileDir and each of its ancestors up to (and including)
// root — the directories whose subtree contains a file living in fileDir.
func dirAncestors(fileDir, root string) []string {
	root = filepath.ToSlash(root)
	nroot := normPath(root)
	out := []string{}
	d := fileDir
	for {
		out = append(out, d)
		if normPath(d) == nroot {
			break
		}
		parent := path.Dir(d)
		if parent == d || len(normPath(parent)) < len(nroot) {
			break
		}
		d = parent
	}
	return out
}

// segDepth counts how many path segments dir sits below root (0 = the root).
func segDepth(dir, root string) int {
	dn, rn := normPath(dir), normPath(root)
	if dn == rn {
		return 0
	}
	rest := strings.TrimPrefix(dn, rn+"/")
	if rest == dn {
		return 0
	}
	return strings.Count(rest, "/") + 1
}

// ---- persisted summaries + recompute ------------------------------------

func (s *Store) ProtectionSummaries() []*ProtectionSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*ProtectionSummary{}, s.c.Protection...)
}

func (s *Store) ProtectionSummaryOf(collectionID int) *ProtectionSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.c.Protection {
		if p.CollectionID == collectionID {
			return p
		}
	}
	return nil
}

func (s *Store) replaceProtectionSummary(sum *ProtectionSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.c.Protection[:0]
	for _, p := range s.c.Protection {
		if p.CollectionID != sum.CollectionID {
			out = append(out, p)
		}
	}
	s.c.Protection = append(out, sum)
	_ = s.save()
}

// RecomputeProtection recomputes every collection's status summary and returns
// the fresh totals plus the deltas since the last run — the numbers the UI turns
// into a "N newly PARTIAL, M newly OUT_OF_POLICY" toast. Never silent.
func (s *Store) RecomputeProtection(progress func(float64, string)) map[string]any {
	cols := s.Collections()
	prev := map[int]map[string]int{}
	for _, p := range s.ProtectionSummaries() {
		prev[p.CollectionID] = p.Files
	}
	totals := map[string]int{}
	deltas := map[string]int{}
	for i, c := range cols {
		res := s.Protection(c.ID)
		s.replaceProtectionSummary(&ProtectionSummary{CollectionID: c.ID, At: time.Now().UTC(), Files: res.Summary})
		for st, n := range res.Summary {
			totals[st] += n
		}
		old := prev[c.ID]
		for _, st := range []string{StatusPartial, StatusOutOfPolicy, StatusNotBackedUp} {
			if d := res.Summary[st] - old[st]; d > 0 {
				deltas[st] += d
			}
		}
		if progress != nil {
			progress(float64(i+1)/float64(len(cols)+1), c.Name)
		}
	}
	if progress != nil {
		progress(1, "done")
	}
	return map[string]any{"totals": totals, "deltas": deltas}
}

// recomputeJob recomputes protection as a visible Job and returns its totals +
// deltas so the caller can surface "N newly PARTIAL, M newly OUT_OF_POLICY" in a
// toast — never silent. Recompute is a fast in-memory pass, so it runs inline and
// the job row lands COMPLETED immediately (Jobs shows it happened).
func (a *App) recomputeJob() map[string]any {
	j := a.Store.NewJob("recompute", "Recompute protection status")
	res := a.Store.RecomputeProtection(func(p float64, msg string) {
		l := "Recompute protection status"
		if msg != "" {
			l += " — " + msg
		}
		a.Store.SetJob(j.ID, p, l, "")
	})
	a.Store.SetJob(j.ID, 1, "Recompute protection status", "COMPLETED")
	res["job_id"] = j.ID
	return res
}
