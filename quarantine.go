package main

// quarantine.go — "never delete, made usable."
//
// The regret-proofing model. Mnemosyne has no delete button and never will: the
// worst thing a user can do to a file through this tool is QUARANTINE it, which
// MOVES it (bytes intact, structure preserved) into a staging folder and stops
// counting it toward protection. Removing quarantined bytes for good is a manual
// act the user performs in their own file manager — the tool only ever stages.
//
// The territory rule (enforced by the same read-only-source guard that keeps the
// tool out of source data): quarantine exists ONLY inside MANAGED TERRITORY — the
// destination roots Mnemosyne itself populated via Plans. On adopted media and on
// source roots the action does not exist: there is nothing here the tool created,
// so it will not move anything. managedRootFor + AssertOutsideSources are the two
// gates every quarantine/un-quarantine passes.
//
// A quarantined file at <root>/<rel> moves to <root>/_deleted/<rel> — the original
// relative path is preserved verbatim beneath _deleted, which is exactly what makes
// un-quarantine a plain reverse move. The catalog copies that lived at those paths
// (recorded on the plan's destination volume as mirror packages) are pulled from
// the accounting on quarantine and restored on un-quarantine, so protection math is
// always truthful — and the protection CONSEQUENCE of a quarantine is shown BEFORE
// it is confirmed ("this drops Smith Wedding originals to 1 copy — proceed?").
//
// The tool NEVER empties _deleted. If the user manually clears it, the next scan
// (any QuarantineView / ReconcileQuarantine pass) notices the bytes are gone, marks
// the entry HUMAN-REMOVED, and keeps the history — it never silently forgets.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// QuarantineDir is the staging folder created directly under a managed destination
// root. Its contents are never removed by Mnemosyne.
const QuarantineDir = "_deleted"

// Quarantine entry statuses.
const (
	QuarantineActive    = "QUARANTINED"   // staged in _deleted, reversible
	QuarantineRestored  = "RESTORED"      // un-quarantined back to its original path
	QuarantineHumanGone = "HUMAN-REMOVED" // the user emptied _deleted by hand; bytes gone, history kept
)

// QuarantinedRef is one catalog copy that was pulled from protection accounting when
// its bytes were quarantined, kept whole so un-quarantine restores it EXACTLY (same
// package, same ref) rather than guessing.
type QuarantinedRef struct {
	ChunkID int          `json:"chunk_id"`
	Ref     ChunkFileRef `json:"ref"`
}

// QuarantineEntry is one staged file or folder. It records who initiated the move
// (implicit: the user), when, an optional reason, the original location (relative to
// the managed root, so structure is preserved and reversal is a straight move), and
// the catalog copies removed from accounting. Append-only in spirit: an entry is
// never deleted, only transitioned (QUARANTINED → RESTORED | HUMAN-REMOVED).
type QuarantineEntry struct {
	ID          int              `json:"id"`
	Root        string           `json:"root"`         // managed destination root (slash form)
	OriginalRel string           `json:"original_rel"` // path relative to Root before quarantine
	IsDir       bool             `json:"is_dir,omitempty"`
	SizeBytes   int64            `json:"size_bytes"`
	FileCount   int              `json:"file_count"`
	By          string           `json:"by"` // who initiated (implicit: "user")
	At          time.Time        `json:"at"`
	Reason      string           `json:"reason,omitempty"`
	Status      string           `json:"status"`
	RestoredAt  *time.Time       `json:"restored_at,omitempty"`
	RemovedAt   *time.Time       `json:"removed_at,omitempty"` // detected human-removed on reconcile
	Refs        []QuarantinedRef `json:"refs,omitempty"`       // catalog copies removed from accounting, for exact restore
}

// quarantinePath is the absolute staged location of an entry: <root>/_deleted/<rel>.
func (e *QuarantineEntry) quarantinePath() string {
	return filepath.Join(filepath.FromSlash(e.Root), QuarantineDir, filepath.FromSlash(e.OriginalRel))
}

// originalPath is the absolute location the entry came from (and returns to on
// un-quarantine): <root>/<rel>.
func (e *QuarantineEntry) originalPath() string {
	return filepath.Join(filepath.FromSlash(e.Root), filepath.FromSlash(e.OriginalRel))
}

// managedRoot is one destination root Mnemosyne populated, plus the volume its
// reorganized copies are credited to.
type managedRoot struct {
	Root         string // slash form
	DestVolumeID int
}

// ManagedRoots returns the destination roots Mnemosyne itself populated via Plans —
// the ONLY territory quarantine operates in. A plan qualifies once it has begun
// realizing its destination (a destination volume, execution progress, or a
// terminal status); a draft/compiled-but-never-run plan names a root the tool has
// not yet touched, so it is not managed territory. Deduped by normalized root.
func (s *Store) ManagedRoots() []managedRoot {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]int{} // normRoot -> index into out
	var out []managedRoot
	for _, p := range s.c.Plans {
		root := strings.TrimSpace(p.DestinationRoot)
		if root == "" {
			continue
		}
		populated := p.DestVolumeID != 0 || len(p.Satisfied) > 0 ||
			p.Status == PlanExecuting || p.Status == PlanClosed
		if !populated {
			continue
		}
		np := normPath(root)
		if i, ok := seen[np]; ok {
			if out[i].DestVolumeID == 0 && p.DestVolumeID != 0 {
				out[i].DestVolumeID = p.DestVolumeID
			}
			continue
		}
		seen[np] = len(out)
		out = append(out, managedRoot{Root: filepath.ToSlash(root), DestVolumeID: p.DestVolumeID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// QuarantineTerritory resolves the managed root a path belongs to, refusing anything
// outside managed territory. It is the single gate: it enforces the read-only-source
// invariant (a source root can never be quarantined), rejects paths the tool did not
// populate (adopted media, unmanaged folders — the action simply does not apply), and
// rejects the _deleted staging area itself (you cannot quarantine the quarantine).
func (s *Store) QuarantineTerritory(absPath string) (root string, destVolumeID int, err error) {
	if strings.TrimSpace(absPath) == "" {
		return "", 0, fmt.Errorf("no path given")
	}
	if e := s.AssertOutsideSources(absPath); e != nil {
		// A registered source root — read-only by the core invariant. The action does
		// not exist here.
		return "", 0, fmt.Errorf("quarantine never touches source data: %s is a read-only source", absPath)
	}
	np := normPath(absPath)
	for _, mr := range s.ManagedRoots() {
		rp := normPath(mr.Root)
		if rp == "" {
			continue
		}
		if np == rp {
			return "", 0, fmt.Errorf("cannot quarantine a managed root itself: %s", absPath)
		}
		if strings.HasPrefix(np, rp+"/") {
			// Inside managed territory. Refuse the _deleted staging tree.
			del := rp + "/" + strings.ToLower(QuarantineDir)
			if np == del || strings.HasPrefix(np, del+"/") {
				return "", 0, fmt.Errorf("%s is already in quarantine staging", absPath)
			}
			return mr.Root, mr.DestVolumeID, nil
		}
	}
	return "", 0, fmt.Errorf("not managed territory: quarantine applies only to destinations Mnemosyne populated (adopted media and sources are never moved)")
}

// QuarantineEligible reports whether a path can be quarantined — the predicate the UI
// uses to decide whether the action even appears. On adopted media and source roots
// it is false, so the action is absent there.
func (s *Store) QuarantineEligible(absPath string) bool {
	_, _, err := s.QuarantineTerritory(absPath)
	return err == nil
}

// ---- consequence preview ----------------------------------------------

// ConsequenceGroup is one Event/Role bucket of files a quarantine would touch, with
// the protection copy count before and after (worst case across the group).
type ConsequenceGroup struct {
	Label        string `json:"label"` // "Smith Wedding originals" etc.
	Files        int    `json:"files"`
	CopiesBefore int    `json:"copies_before"` // min across the group
	CopiesAfter  int    `json:"copies_after"`  // min across the group, after this copy is pulled
}

// QuarantineConsequence is what a user is shown BEFORE confirming: how much would
// move, and — per affected Event/Role — what it does to protection. Warnings are the
// plain-language sentences ("this drops Smith Wedding originals to 1 copy").
type QuarantineConsequence struct {
	Path       string             `json:"path"`
	Root       string             `json:"root"`
	Rel        string             `json:"rel"`
	IsDir      bool               `json:"is_dir"`
	FileCount  int                `json:"file_count"`
	TotalBytes int64              `json:"total_bytes"`
	Groups     []ConsequenceGroup `json:"groups,omitempty"`
	Warnings   []string           `json:"warnings,omitempty"`
	// MinCopiesAfter is the worst-case remaining copy count across every cataloged file
	// touched (−1 when nothing cataloged is affected — a bytes-only move).
	MinCopiesAfter int `json:"min_copies_after"`
}

// hashVolumesLocked maps content hash -> set of volume IDs that hold a VERIFIED,
// non-superseded copy of it — the same "qualifying copy" notion protection uses.
// Caller holds s.mu.
func (s *Store) hashVolumesLocked() map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, ch := range s.c.Chunks {
		if ch.Status == "FAILED" {
			continue
		}
		var vols []int
		for _, cp := range ch.Copies {
			if cp.Superseded || cp.VerifyOK == nil || !*cp.VerifyOK {
				continue
			}
			vols = append(vols, cp.VolumeID)
		}
		if len(vols) == 0 {
			continue
		}
		for _, cf := range ch.Files {
			if cf.Hash == "" {
				continue
			}
			set := out[cf.Hash]
			if set == nil {
				set = map[int]bool{}
				out[cf.Hash] = set
			}
			for _, v := range vols {
				set[v] = true
			}
		}
	}
	return out
}

// quarantineRefsLocked finds the catalog copies on destVolumeID whose planned rel
// path falls under relPrefix (a file: exact match; a folder: prefix at a segment
// boundary). Returns the removed-ref descriptors and the set of affected hashes.
// Caller holds s.mu.
func (s *Store) quarantineRefsLocked(destVolumeID int, relPrefix string, isDir bool) ([]QuarantinedRef, map[string]bool) {
	relPrefix = strings.Trim(filepath.ToSlash(relPrefix), "/")
	var refs []QuarantinedRef
	hashes := map[string]bool{}
	if destVolumeID == 0 {
		return refs, hashes
	}
	for _, ch := range s.c.Chunks {
		if !ch.Mirror {
			continue
		}
		onVol := false
		for _, cp := range ch.Copies {
			if cp.VolumeID == destVolumeID && !cp.Superseded {
				onVol = true
				break
			}
		}
		if !onVol {
			continue
		}
		for _, cf := range ch.Files {
			r := strings.Trim(filepath.ToSlash(cf.RelPath), "/")
			match := r == relPrefix
			if isDir {
				match = r == relPrefix || strings.HasPrefix(r, relPrefix+"/")
			}
			if !match {
				continue
			}
			refs = append(refs, QuarantinedRef{ChunkID: ch.ID, Ref: cf})
			if cf.Hash != "" {
				hashes[cf.Hash] = true
			}
		}
	}
	return refs, hashes
}

// QuarantineConsequence computes the protection impact of quarantining absPath,
// without moving anything. Groups affected cataloged files by Event + Role and spells
// out any copy-count drop. Errors if the path is not eligible territory or is missing.
func (a *App) QuarantineConsequence(absPath string) (QuarantineConsequence, error) {
	root, destVol, err := a.Store.QuarantineTerritory(absPath)
	if err != nil {
		return QuarantineConsequence{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return QuarantineConsequence{}, fmt.Errorf("cannot read %s: %w", absPath, err)
	}
	rel, err := filepath.Rel(filepath.FromSlash(root), absPath)
	if err != nil {
		return QuarantineConsequence{}, err
	}
	rel = filepath.ToSlash(rel)
	res := QuarantineConsequence{Path: absPath, Root: root, Rel: rel, IsDir: info.IsDir(), MinCopiesAfter: -1}

	// Count what's on disk (works whether or not the catalog knows these files).
	if info.IsDir() {
		_ = filepath.WalkDir(absPath, func(p string, d os.DirEntry, e error) error {
			if e != nil || d.IsDir() {
				return nil
			}
			if st, se := d.Info(); se == nil {
				res.TotalBytes += st.Size()
			}
			res.FileCount++
			return nil
		})
	} else {
		res.FileCount = 1
		res.TotalBytes = info.Size()
	}

	a.Store.mu.Lock()
	defer a.Store.mu.Unlock()
	_, hashes := a.Store.quarantineRefsLocked(destVol, rel, info.IsDir())
	if len(hashes) == 0 {
		return res, nil // bytes-only move — nothing cataloged is affected
	}
	hashVols := a.Store.hashVolumesLocked()

	// Group affected files by Event name + Role, tracking the min copies before/after.
	events := map[int]string{}
	for _, ev := range a.Store.c.Events {
		events[ev.ID] = ev.Name
	}
	type grp struct {
		files  int
		before int
		after  int
	}
	byLabel := map[string]*grp{}
	order := []string{}
	for _, f := range a.Store.c.Files {
		if f.Hash == "" || !hashes[f.Hash] {
			continue
		}
		before := len(hashVols[f.Hash])
		after := before
		if hashVols[f.Hash][destVol] {
			after = before - 1
		}
		if after < 0 {
			after = 0
		}
		label := consequenceLabel(events[f.EventID], f.Role)
		g := byLabel[label]
		if g == nil {
			g = &grp{before: before, after: after}
			byLabel[label] = g
			order = append(order, label)
		}
		g.files++
		if before < g.before {
			g.before = before
		}
		if after < g.after {
			g.after = after
		}
		if res.MinCopiesAfter < 0 || after < res.MinCopiesAfter {
			res.MinCopiesAfter = after
		}
	}
	sort.Strings(order)
	for _, label := range order {
		g := byLabel[label]
		res.Groups = append(res.Groups, ConsequenceGroup{Label: label, Files: g.files,
			CopiesBefore: g.before, CopiesAfter: g.after})
		if g.after < g.before {
			res.Warnings = append(res.Warnings, quarantineWarning(label, g.after))
		}
	}
	return res, nil
}

// consequenceLabel names an affected group for the human sentence, e.g. "Smith
// Wedding RAWs", "Smith Wedding files", or "RAWs" / "files" when no Event.
func consequenceLabel(eventName, role string) string {
	eventName = strings.TrimSpace(eventName)
	roleWord := roleNoun(role)
	switch {
	case eventName != "" && roleWord != "":
		return eventName + " " + roleWord
	case eventName != "":
		return eventName + " files"
	case roleWord != "":
		return roleWord
	default:
		return "files"
	}
}

// roleNoun turns a File.Role into a plural noun for the consequence sentence.
func roleNoun(role string) string {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case RoleOriginals:
		return "originals"
	case RoleDeliverables:
		return "deliverables"
	case RoleSidecars:
		return "sidecars"
	case RoleProject:
		return "project files"
	case "", RoleOther:
		return ""
	default:
		return strings.ToLower(role) + " files"
	}
}

func quarantineWarning(label string, copiesAfter int) string {
	switch copiesAfter {
	case 0:
		return fmt.Sprintf("this drops %s to 0 copies — the last copy Mnemosyne tracks", label)
	case 1:
		return fmt.Sprintf("this drops %s to 1 copy", label)
	default:
		return fmt.Sprintf("this drops %s to %d copies", label, copiesAfter)
	}
}

// ---- the moves ---------------------------------------------------------

// Quarantine stages absPath into <root>/_deleted/<rel>, records the move (who/when/
// reason) and the catalog copies it pulls from protection accounting, and writes an
// audit line. It is the ONLY way this tool "removes" anything — and even this only
// moves bytes; it never deletes. reason is optional; by defaults to "user".
func (a *App) Quarantine(absPath, by, reason string) (*QuarantineEntry, error) {
	root, destVol, err := a.Store.QuarantineTerritory(absPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", absPath, err)
	}
	rel, err := filepath.Rel(filepath.FromSlash(root), absPath)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	// Tally bytes/files before the move so the record stands on its own.
	var size int64
	fileCount := 0
	if info.IsDir() {
		_ = filepath.WalkDir(absPath, func(p string, d os.DirEntry, e error) error {
			if e != nil || d.IsDir() {
				return nil
			}
			if st, se := d.Info(); se == nil {
				size += st.Size()
			}
			fileCount++
			return nil
		})
	} else {
		size, fileCount = info.Size(), 1
	}

	dest := filepath.Join(filepath.FromSlash(root), QuarantineDir, filepath.FromSlash(rel))
	if _, e := os.Stat(dest); e == nil {
		return nil, fmt.Errorf("a quarantined copy of %s already exists in %s", rel, QuarantineDir)
	}
	if e := os.MkdirAll(filepath.Dir(dest), 0o755); e != nil {
		return nil, e
	}
	if e := os.Rename(absPath, dest); e != nil {
		return nil, fmt.Errorf("staging %s failed: %w", rel, e)
	}

	// Pull the affected catalog copies out of protection accounting (bytes are still
	// safe in _deleted; they simply no longer live where the copy record says).
	a.Store.mu.Lock()
	refs, _ := a.Store.quarantineRefsLocked(destVol, rel, info.IsDir())
	a.Store.removeRefsLocked(refs)
	e := &QuarantineEntry{
		ID: a.Store.next("quarantine"), Root: filepath.ToSlash(root), OriginalRel: rel,
		IsDir: info.IsDir(), SizeBytes: size, FileCount: fileCount,
		By: nonEmpty(by, "user"), At: time.Now().UTC(), Reason: strings.TrimSpace(reason),
		Status: QuarantineActive, Refs: refs,
	}
	a.Store.c.Quarantine = append(a.Store.c.Quarantine, e)
	_ = a.Store.save()
	a.Store.mu.Unlock()

	a.Store.Log("quarantine", fmt.Sprintf("quarantined %s (%d file(s), %s) under %s by %s%s",
		rel, fileCount, humanBytesQ(size), root, e.By, reasonSuffix(reason)))
	return e, nil
}

// UnQuarantine reverses a quarantine: it moves the staged bytes back to their original
// path and re-credits the catalog copies that were pulled. Refuses if the original
// path is now occupied (never clobbers) or the staged bytes are gone (human-removed).
func (a *App) UnQuarantine(id int) (*QuarantineEntry, error) {
	e := a.Store.QuarantineEntry(id)
	if e == nil {
		return nil, fmt.Errorf("quarantine entry not found")
	}
	if e.Status != QuarantineActive {
		return nil, fmt.Errorf("entry is %s, not restorable", e.Status)
	}
	src := e.quarantinePath()
	dst := e.originalPath()
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("staged bytes are gone (removed by hand?): %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("original path is occupied, refusing to overwrite: %s", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, fmt.Errorf("restoring %s failed: %w", e.OriginalRel, err)
	}
	// Best-effort tidy of now-empty staging directories (never touches _deleted's root).
	pruneEmptyDirs(filepath.Join(filepath.FromSlash(e.Root), QuarantineDir), filepath.Dir(src))

	a.Store.mu.Lock()
	a.Store.restoreRefsLocked(e.Refs)
	now := time.Now().UTC()
	e.Status, e.RestoredAt = QuarantineRestored, &now
	_ = a.Store.save()
	a.Store.mu.Unlock()

	a.Store.Log("quarantine", fmt.Sprintf("un-quarantined %s under %s", e.OriginalRel, e.Root))
	return e, nil
}

// removeRefsLocked strips the given refs from their mirror packages and updates the
// package tallies, so protection stops counting the quarantined copy. Caller holds
// s.mu. The bytes still exist (in _deleted) — only the "it lives HERE" record is
// pulled; nothing about other volumes' copies changes.
func (s *Store) removeRefsLocked(refs []QuarantinedRef) {
	byChunk := map[int]map[string]bool{}
	for _, r := range refs {
		m := byChunk[r.ChunkID]
		if m == nil {
			m = map[string]bool{}
			byChunk[r.ChunkID] = m
		}
		m[r.Ref.RelPath] = true
	}
	for _, ch := range s.c.Chunks {
		drop := byChunk[ch.ID]
		if drop == nil {
			continue
		}
		kept := ch.Files[:0:0]
		var sum int64
		for _, cf := range ch.Files {
			if drop[cf.RelPath] {
				continue
			}
			kept = append(kept, cf)
			sum += cf.SizeBytes
		}
		ch.Files, ch.FileCount, ch.DataBytes, ch.EncBytes = kept, len(kept), sum, sum
	}
}

// restoreRefsLocked re-adds previously removed refs to their mirror packages (deduped
// by rel path) and refreshes the tallies. Caller holds s.mu. A package that has since
// vanished is skipped — the reverse move still succeeds; the copy simply isn't
// re-credited (a later scan re-adopts it).
func (s *Store) restoreRefsLocked(refs []QuarantinedRef) {
	byChunk := map[int][]ChunkFileRef{}
	for _, r := range refs {
		byChunk[r.ChunkID] = append(byChunk[r.ChunkID], r.Ref)
	}
	for _, ch := range s.c.Chunks {
		add := byChunk[ch.ID]
		if len(add) == 0 {
			continue
		}
		have := map[string]bool{}
		for _, cf := range ch.Files {
			have[cf.RelPath] = true
		}
		for _, cf := range add {
			if !have[cf.RelPath] {
				ch.Files = append(ch.Files, cf)
				have[cf.RelPath] = true
			}
		}
		var sum int64
		for _, cf := range ch.Files {
			sum += cf.SizeBytes
		}
		ch.FileCount, ch.DataBytes, ch.EncBytes = len(ch.Files), sum, sum
	}
}

// ---- listing, view, reconcile -----------------------------------------

// QuarantineEntry returns one entry by ID (nil if unknown).
func (s *Store) QuarantineEntry(id int) *QuarantineEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.c.Quarantine {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// Quarantined returns every quarantine entry, newest first.
func (s *Store) Quarantined() []*QuarantineEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]*QuarantineEntry(nil), s.c.Quarantine...)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// ReconcileQuarantine is the graceful "the user emptied _deleted by hand" pass: every
// still-active entry whose staged bytes have vanished is marked HUMAN-REMOVED (history
// retained, never silently dropped). Idempotent; returns how many it reconciled.
func (a *App) ReconcileQuarantine() int {
	a.Store.mu.Lock()
	defer a.Store.mu.Unlock()
	n := 0
	now := time.Now().UTC()
	for _, e := range a.Store.c.Quarantine {
		if e.Status != QuarantineActive {
			continue
		}
		if _, err := os.Stat(e.quarantinePath()); err != nil {
			e.Status, e.RemovedAt = QuarantineHumanGone, &now
			n++
		}
	}
	if n > 0 {
		_ = a.Store.save()
	}
	return n
}

// The standing sentence shown on the Quarantine view — the promise, restated every
// time the user looks at what is staged.
const QuarantineStandingNote = "Removing these permanently is a manual act you perform in your file manager — this tool has no delete button and never will."

// QuarantineViewEntry is a listed entry enriched for display (age + human bytes).
type QuarantineViewEntry struct {
	*QuarantineEntry
	AgeDays    int    `json:"age_days"`
	HumanBytes string `json:"human_bytes"`
	Exists     bool   `json:"exists"` // are the staged bytes still on disk?
}

// QuarantineView is the Quarantine screen's payload: every entry (contents, age,
// bytes), the total staged bytes still on disk, and the standing note. It reconciles
// first, so a hand-emptied _deleted is reflected before anything is shown.
type QuarantineView struct {
	Entries      []QuarantineViewEntry `json:"entries"`
	ActiveCount  int                   `json:"active_count"`
	ActiveBytes  int64                 `json:"active_bytes"`
	StandingNote string                `json:"standing_note"`
	Roots        []string              `json:"roots"`
}

// QuarantineView builds the view, reconciling human-removed entries first.
func (a *App) QuarantineViewData() QuarantineView {
	a.ReconcileQuarantine()
	v := QuarantineView{StandingNote: QuarantineStandingNote}
	for _, mr := range a.Store.ManagedRoots() {
		v.Roots = append(v.Roots, mr.Root)
	}
	now := time.Now().UTC()
	for _, e := range a.Store.Quarantined() {
		_, exists := os.Stat(e.quarantinePath())
		ve := QuarantineViewEntry{QuarantineEntry: e,
			AgeDays: int(now.Sub(e.At).Hours() / 24), HumanBytes: humanBytesQ(e.SizeBytes),
			Exists: exists == nil}
		v.Entries = append(v.Entries, ve)
		if e.Status == QuarantineActive {
			v.ActiveCount++
			if ve.Exists {
				v.ActiveBytes += e.SizeBytes
			}
		}
	}
	return v
}

// ---- small helpers -----------------------------------------------------

func reasonSuffix(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return " — " + reason
}

// pruneEmptyDirs removes directories from leaf up to (but not including) stop, while
// they are empty. Best-effort; the first non-empty dir (or an error) stops it.
func pruneEmptyDirs(stop, leaf string) {
	stopN := normPath(stop)
	for {
		ln := normPath(leaf)
		if ln == "" || ln == stopN || !strings.HasPrefix(ln, stopN+"/") {
			return
		}
		if err := os.Remove(leaf); err != nil {
			return // not empty, or gone — stop
		}
		leaf = filepath.Dir(leaf)
	}
}

// humanBytesQ renders a byte count compactly for quarantine display.
func humanBytesQ(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
