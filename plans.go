package main

// plans.go — the keystone. A Plan authors a FUTURE file structure against drive
// SNAPSHOTS while the drives sit unplugged, then executes it later, serial-bound
// and in ANY order, over weeks.
//
// The whole design is content-addressed: every in-scope file is mapped BY HASH to
// a planned destination path (template routes + Events + EXIF supply the tokens),
// so a hash that lives on several drives is copied exactly ONCE — the first drive
// to carry it satisfies it; later drives merely confirm. Manual drag overrides win
// over the template and survive template edits. A dry-run compile gate refuses
// unless every file is routed (or parked) and no conflicts remain in scope.
// Execution is copy-then-hash-verify into the planned tree, re-checking each source
// file against its snapshot hash as it reads (a mismatch loudly flags a drive that
// drifted from its snapshot and skips the file — never guessed), resumable across
// restarts via the persisted Satisfied set. Sources are strictly read-only and are
// never wiped; the destination is only ever created under DestinationRoot.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- store CRUD --------------------------------------------------------

func (s *Store) AddPlan(p *Plan) *Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.ID = s.next("plan")
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.Status == "" {
		p.Status = PlanDraft
	}
	s.c.Plans = append(s.c.Plans, p)
	_ = s.save()
	return p
}

func (s *Store) Plans() []*Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Plan, len(s.c.Plans))
	copy(out, s.c.Plans)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) Plan(id int) *Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.c.Plans {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Store) UpdatePlan(p *Plan) { s.mu.Lock(); defer s.mu.Unlock(); _ = s.save() }

func (s *Store) DeletePlan(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.c.Plans[:0]
	for _, p := range s.c.Plans {
		if p.ID != id {
			out = append(out, p)
		}
	}
	s.c.Plans = out
	_ = s.save()
}

// ---- mapping (hash → planned path) -------------------------------------

// PlanDrive is one drive that physically holds a plan file (by its snapshot).
type PlanDrive struct {
	VolumeID int    `json:"volume_id"`
	Serial   string `json:"serial,omitempty"`
	Label    string `json:"label,omitempty"`
	RelPath  string `json:"rel_path"`
}

// PlanFile is one unique content hash in a plan's scope: where it lives (across
// drives), what it would be routed to, and whether it's already satisfied.
type PlanFile struct {
	Hash       string      `json:"hash"`
	RelPath    string      `json:"rel_path"` // representative source path (for {orig_name})
	SizeBytes  int64       `json:"size_bytes"`
	Role       string      `json:"role,omitempty"`
	Planned    string      `json:"planned,omitempty"` // "" = unrouted
	Overridden bool        `json:"overridden,omitempty"`
	Parked     bool        `json:"parked,omitempty"`
	Satisfied  bool        `json:"satisfied,omitempty"`
	Drives     []PlanDrive `json:"drives"`
}

// planFiles enumerates a plan's file universe from the drive snapshots, deduped by
// content hash, each enriched with its planned destination path. For a COMPILED/
// EXECUTING plan the frozen Mapping is authoritative; for a DRAFT the path is
// routed live (manual overrides always win).
func (a *App) planFiles(plan *Plan) []PlanFile {
	frozen := plan.Status == PlanCompiled || plan.Status == PlanExecuting

	// In-scope hashes (empty archive scope = every snapshot file).
	useScope := len(plan.ArchiveIDs) > 0
	scope := map[string]bool{}
	if useScope {
		for _, aid := range plan.ArchiveIDs {
			for _, f := range a.Store.FilesOf(aid) {
				if f.Hash != "" {
					scope[f.Hash] = true
				}
			}
		}
	}
	// hash → event / role, from archive files (content-addressed metadata).
	hashEvent := map[string]int{}
	hashRole := map[string]string{}
	for _, f := range a.Store.AllFiles() {
		if f.Hash == "" {
			continue
		}
		if f.EventID != 0 && hashEvent[f.Hash] == 0 {
			hashEvent[f.Hash] = f.EventID
		}
		if f.Role != "" && hashRole[f.Hash] == "" {
			hashRole[f.Hash] = f.Role
		}
	}

	type acc struct {
		pf     PlanFile
		shotAt time.Time
		serial string
	}
	byHash := map[string]*acc{}
	for _, snap := range a.Store.VolumeSnapshots() {
		for _, sf := range snap.Files {
			if sf.Hash == "" || (useScope && !scope[sf.Hash]) {
				continue
			}
			ac := byHash[sf.Hash]
			if ac == nil {
				ac = &acc{pf: PlanFile{Hash: sf.Hash, RelPath: sf.RelPath, SizeBytes: sf.SizeBytes, Role: sf.Role},
					shotAt: sf.ShotAt, serial: sf.CameraSerial}
				byHash[sf.Hash] = ac
			} else {
				if sf.RelPath < ac.pf.RelPath { // lexically-smallest = stable representative
					ac.pf.RelPath = sf.RelPath
				}
				if ac.pf.Role == "" {
					ac.pf.Role = sf.Role
				}
				if ac.shotAt.IsZero() {
					ac.shotAt = sf.ShotAt
				}
				if ac.serial == "" {
					ac.serial = sf.CameraSerial
				}
			}
			ac.pf.Drives = append(ac.pf.Drives, PlanDrive{VolumeID: snap.VolumeID, Serial: snap.Serial, Label: snap.Label, RelPath: sf.RelPath})
		}
	}

	tmpl := a.Store.Template(plan.TemplateID)
	reg := a.formatRegistry()
	events := map[int]*Event{}
	for _, e := range a.Store.Events(0) {
		events[e.ID] = e
	}

	out := make([]PlanFile, 0, len(byHash))
	for h, ac := range byHash {
		pf := ac.pf
		pf.Parked = plan.Parked[h]
		pf.Satisfied = plan.Satisfied[h]
		if ov, ok := plan.Overrides[h]; ok {
			pf.Planned, pf.Overridden = ov, true
		} else if frozen {
			pf.Planned = plan.Mapping[h]
		} else if tmpl != nil {
			role := pf.Role
			if role == "" {
				if role = hashRole[h]; role == "" {
					role, _ = classifyRole(reg, pf.RelPath)
				}
			}
			f := &File{RelPath: pf.RelPath, Role: role, ShotAt: ac.shotAt, CameraSerial: ac.serial, EventID: hashEvent[h]}
			if dest, ok := routeForFile(tmpl, reg, f, events[hashEvent[h]]); ok {
				pf.Planned = dest
			}
		}
		out = append(out, pf)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Planned != out[j].Planned {
			return out[i].Planned < out[j].Planned
		}
		return out[i].RelPath < out[j].RelPath
	})
	return out
}

// PlanUnrouted lists the files a plan cannot place — the bucket the operator clears
// by editing routes, dragging, or parking before the plan can compile.
func (a *App) planUnrouted(files []PlanFile) []PlanFile {
	var out []PlanFile
	for _, f := range files {
		if f.Planned == "" && !f.Parked {
			out = append(out, f)
		}
	}
	return out
}

// planStatusSeverity ranks a plan node's rollup: PENDING outranks SATISFIED so a
// folder with any un-copied file shows as pending.
func planStatusSeverity(s string) int {
	if s == "PENDING" {
		return 1
	}
	return 0
}

// planTree computes one browsable level of a plan's VIRTUAL destination tree —
// built entirely from the mapping, never the disk — colored by satisfied/pending.
// dirPath "" is the destination root. Unrouted/parked files are excluded (they live
// in the Unrouted bucket, not the tree).
func (a *App) planTree(plan *Plan, dirPath string) TreemapResult {
	files := a.planFiles(plan)
	res := TreemapResult{Name: plan.Name, Path: dirPath, ColorBy: "plan", StatusBytes: map[string]int64{}}
	P := strings.TrimRight(filepath.ToSlash(dirPath), "/")
	nP := normPath(P)
	atRoot := P == ""

	children := map[string]*treemapAgg{}
	get := func(key, name, cpath string, isDir bool) *treemapAgg {
		ag := children[key]
		if ag == nil {
			ag = &treemapAgg{name: name, path: cpath, isDir: isDir, worstSev: -1, statusBytes: map[string]int64{}}
			children[key] = ag
		}
		return ag
	}
	for _, f := range files {
		if f.Planned == "" || f.Parked {
			continue
		}
		full := filepath.ToSlash(f.Planned)
		var childName, childPath string
		var childIsDir bool
		if atRoot {
			if i := strings.IndexByte(full, '/'); i >= 0 {
				childName, childPath, childIsDir = full[:i], full[:i], true
			} else {
				childName, childPath, childIsDir = full, full, false
			}
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
		}
		st := "PENDING"
		if f.Satisfied {
			st = "SATISFIED"
		}
		res.Size += f.SizeBytes
		res.Files++
		res.StatusBytes[st] += f.SizeBytes
		ag := get(normPath(childPath), childName, childPath, childIsDir)
		ag.size += f.SizeBytes
		ag.statusBytes[st] += f.SizeBytes
		if childIsDir {
			ag.files++
		} else {
			ag.files = 1
		}
		if sv := planStatusSeverity(st); sv > ag.worstSev {
			ag.worstSev, ag.worst = sv, st
		}
	}

	all := make([]*treemapAgg, 0, len(children))
	for _, ag := range children {
		all = append(all, ag)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].size != all[j].size {
			return all[i].size > all[j].size
		}
		return all[i].name < all[j].name
	})
	for _, ag := range all {
		res.Children = append(res.Children, ag.node())
	}
	res.Crumbs = snapshotCrumbs(plan.Name, P)
	return res
}

// ---- overrides (drag) & park -------------------------------------------

// uncompileLocked-style helper: any edit to a compiled plan reverts it to DRAFT so
// the frozen mapping can't drift from the operator's intent (recompile required).
func (a *App) touchPlanDraft(plan *Plan) {
	if plan.Status == PlanCompiled {
		plan.Status = PlanDraft
		plan.Mapping = nil
		plan.CompiledAt = nil
	}
}

// SetOverride records (or clears) a manual placement for a hash — the persist of a
// drag in the virtual tree. Empty dest clears the override. Reverts a compiled plan
// to draft. dest is a planned rel path (folder ending in "/" appends the filename).
func (a *App) SetOverride(planID int, hash, dest string) error {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return fmt.Errorf("plan not found")
	}
	dest = strings.TrimSpace(filepath.ToSlash(dest))
	if plan.Overrides == nil {
		plan.Overrides = map[string]string{}
	}
	a.touchPlanDraft(plan)
	if dest == "" {
		delete(plan.Overrides, hash)
	} else {
		if strings.HasSuffix(dest, "/") { // dropped onto a folder — keep the filename
			dest += path.Base(planRelForHash(a, plan, hash))
		}
		plan.Overrides[hash] = dest
	}
	a.Store.UpdatePlan(plan)
	return nil
}

// MoveFolder re-homes every file currently planned under srcPrefix to dstPrefix (a
// folder drag), writing per-hash overrides so it survives template edits.
func (a *App) MoveFolder(planID int, srcPrefix, dstPrefix string) (int, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return 0, fmt.Errorf("plan not found")
	}
	srcPrefix = strings.Trim(filepath.ToSlash(srcPrefix), "/")
	dstPrefix = strings.Trim(filepath.ToSlash(dstPrefix), "/")
	if srcPrefix == "" {
		return 0, fmt.Errorf("source folder required")
	}
	files := a.planFiles(plan)
	if plan.Overrides == nil {
		plan.Overrides = map[string]string{}
	}
	a.touchPlanDraft(plan)
	n := 0
	for _, f := range files {
		if f.Planned == "" {
			continue
		}
		p := strings.Trim(f.Planned, "/")
		if p == srcPrefix || strings.HasPrefix(p, srcPrefix+"/") {
			rest := strings.TrimPrefix(p, srcPrefix)
			plan.Overrides[f.Hash] = strings.Trim(dstPrefix+rest, "/")
			n++
		}
	}
	a.Store.UpdatePlan(plan)
	return n, nil
}

// SetParked marks/unmarks a hash as parked (deliberately left out of this move).
func (a *App) SetParked(planID int, hash string, parked bool) error {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return fmt.Errorf("plan not found")
	}
	if plan.Parked == nil {
		plan.Parked = map[string]bool{}
	}
	a.touchPlanDraft(plan)
	if parked {
		plan.Parked[hash] = true
	} else {
		delete(plan.Parked, hash)
	}
	a.Store.UpdatePlan(plan)
	return nil
}

func planRelForHash(a *App, plan *Plan, hash string) string {
	for _, f := range a.planFiles(plan) {
		if f.Hash == hash {
			if f.Planned != "" {
				return f.Planned
			}
			return f.RelPath
		}
	}
	return hash
}

// ---- compile gate + report ---------------------------------------------

// DriveWorkload is one source drive's share of a compiled plan.
type DriveWorkload struct {
	VolumeID        int    `json:"volume_id"`
	Serial          string `json:"serial,omitempty"`
	Label           string `json:"label"`
	Files           int    `json:"files"`
	Bytes           int64  `json:"bytes"`
	TopOverlapLabel string `json:"top_overlap_label,omitempty"`
	TopOverlapFiles int    `json:"top_overlap_files,omitempty"`
}

// PlanReport is the dry-run compile summary: whether the plan can compile, the
// totals, per-source-drive workload, and how much is saved by cross-drive overlap.
type PlanReport struct {
	PlanID      int             `json:"plan_id"`
	Compilable  bool            `json:"compilable"`
	Files       int             `json:"files"`
	Bytes       int64           `json:"bytes"`
	Unrouted    int             `json:"unrouted"`
	Parked      int             `json:"parked"`
	Conflicts   int             `json:"conflicts"`
	DedupeFiles int             `json:"dedupe_files"`
	DedupeBytes int64           `json:"dedupe_bytes"`
	Drives      []DriveWorkload `json:"drives"`
	Notes       []string        `json:"notes,omitempty"`
}

// scopeOpenConflicts counts unresolved conflicts across the plan's archive scope.
func (a *App) scopeOpenConflicts(plan *Plan) int {
	if len(plan.ArchiveIDs) == 0 {
		return a.Store.OpenConflictCount(0)
	}
	n := 0
	for _, aid := range plan.ArchiveIDs {
		n += a.Store.OpenConflictCount(aid)
	}
	return n
}

// planReportFrom builds the compile report from an already-computed file list.
func (a *App) planReportFrom(plan *Plan, files []PlanFile) PlanReport {
	rep := PlanReport{PlanID: plan.ID}
	rep.Conflicts = a.scopeOpenConflicts(plan)

	drive := map[int]*DriveWorkload{}
	label := map[int]string{}
	pair := map[int]map[int]int{} // volID → peerVolID → shared planned files
	for _, f := range files {
		if f.Parked {
			rep.Parked++
			continue
		}
		if f.Planned == "" {
			rep.Unrouted++
			continue
		}
		rep.Files++
		rep.Bytes += f.SizeBytes
		if len(f.Drives) > 1 {
			rep.DedupeFiles++
			rep.DedupeBytes += f.SizeBytes
		}
		for _, d := range f.Drives {
			w := drive[d.VolumeID]
			if w == nil {
				w = &DriveWorkload{VolumeID: d.VolumeID, Serial: d.Serial, Label: d.Label}
				drive[d.VolumeID] = w
				label[d.VolumeID] = d.Label
			}
			w.Files++
			w.Bytes += f.SizeBytes
		}
		for i := 0; i < len(f.Drives); i++ {
			for j := 0; j < len(f.Drives); j++ {
				if i == j {
					continue
				}
				m := pair[f.Drives[i].VolumeID]
				if m == nil {
					m = map[int]int{}
					pair[f.Drives[i].VolumeID] = m
				}
				m[f.Drives[j].VolumeID]++
			}
		}
	}
	for id, w := range drive {
		for peer, n := range pair[id] {
			if n > w.TopOverlapFiles {
				w.TopOverlapFiles, w.TopOverlapLabel = n, label[peer]
			}
		}
		rep.Drives = append(rep.Drives, *w)
	}
	sort.Slice(rep.Drives, func(i, j int) bool {
		if rep.Drives[i].Files != rep.Drives[j].Files {
			return rep.Drives[i].Files > rep.Drives[j].Files
		}
		return rep.Drives[i].Label < rep.Drives[j].Label
	})
	rep.Compilable = rep.Unrouted == 0 && rep.Conflicts == 0
	for _, w := range rep.Drives {
		note := fmt.Sprintf("%s owes %s files / %s", orDash(w.Label), commaInt(w.Files), humanBytes(w.Bytes))
		if w.TopOverlapFiles > 0 {
			note += fmt.Sprintf(" · %s satisfied by overlap with %s", commaInt(w.TopOverlapFiles), w.TopOverlapLabel)
		}
		rep.Notes = append(rep.Notes, note)
	}
	return rep
}

// PlanPreview returns the live compile report for a plan without freezing anything.
func (a *App) PlanPreview(planID int) (PlanReport, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return PlanReport{}, fmt.Errorf("plan not found")
	}
	return a.planReportFrom(plan, a.planFiles(plan)), nil
}

// CompilePlan runs the dry-run gate: it compiles only when unrouted == 0 (parked
// excepted) and no unresolved conflicts sit in scope. On success it FREEZES the
// hash → path mapping so the plan is stable against later template edits.
func (a *App) CompilePlan(planID int) (PlanReport, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return PlanReport{}, fmt.Errorf("plan not found")
	}
	// Recompute from scratch (draft view) even if previously compiled.
	if plan.Status == PlanCompiled {
		plan.Status = PlanDraft
		plan.Mapping = nil
	}
	files := a.planFiles(plan)
	rep := a.planReportFrom(plan, files)
	if !rep.Compilable {
		a.Store.UpdatePlan(plan)
		return rep, nil
	}
	mapping := make(map[string]string, rep.Files)
	for _, f := range files {
		if f.Parked || f.Planned == "" {
			continue
		}
		mapping[f.Hash] = f.Planned
	}
	now := time.Now().UTC()
	plan.Mapping, plan.Status, plan.CompiledAt = mapping, PlanCompiled, &now
	if plan.Satisfied == nil {
		plan.Satisfied = map[string]bool{}
	}
	a.Store.UpdatePlan(plan)
	a.Store.Log("plan", fmt.Sprintf("plan %q compiled: %d files (%s), %d de-duped",
		plan.Name, rep.Files, humanBytes(rep.Bytes), rep.DedupeFiles))
	return rep, nil
}

// ---- destination pre-adopt ---------------------------------------------

// AdoptDestination scans an already-partially-populated destination root and marks
// any plan hash already present there as satisfied (by content), so an interrupted
// or hand-started move isn't redone. Read-only toward the destination.
func (a *App) AdoptDestination(planID int, progress func(float64, string)) (map[string]any, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return nil, fmt.Errorf("plan not found")
	}
	if len(plan.Mapping) == 0 {
		return nil, fmt.Errorf("compile the plan before adopting its destination")
	}
	root := plan.DestinationRoot
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return map[string]any{"present": 0, "note": "destination does not exist yet — nothing to adopt"}, nil
	}
	want := map[string]bool{}
	for h := range plan.Mapping {
		want[h] = true
	}
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDockDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if plan.Satisfied == nil {
		plan.Satisfied = map[string]bool{}
	}
	present := 0
	parallelHash(paths, func(int) {}, func(_, sha, _ string, _ int64, _ time.Time) {
		if want[sha] && !plan.Satisfied[sha] {
			plan.Satisfied[sha] = true
			present++
		}
	})
	a.Store.UpdatePlan(plan)
	a.Store.Log("plan", fmt.Sprintf("plan %q: adopted %d already-present destination file(s)", plan.Name, present))
	return map[string]any{"present": present, "coverage": a.PlanCoverage(plan)}, nil
}

// ---- execution ---------------------------------------------------------

// PlanExecResult is one drive's execution outcome.
type PlanExecResult struct {
	PlanID       int          `json:"plan_id"`
	VolumeID     int          `json:"volume_id"`
	Drive        string       `json:"drive"`
	Copied       int          `json:"copied"`
	Confirmed    int          `json:"confirmed"` // already satisfied by an earlier drive
	Bytes        int64        `json:"bytes"`
	DriveDiffers int          `json:"drive_differs"` // source bytes != snapshot hash — skipped to review
	Unreadable   int          `json:"unreadable"`
	Coverage     PlanCoverage `json:"coverage"`
	Message      string       `json:"message"`
}

// ExecutePlanFromDrive runs the compiled plan's pending work that THIS docked drive
// can supply. It copies each pending file the drive holds into its planned path via
// copy-then-hash-verify, re-checking the source against its snapshot hash as it
// reads (a mismatch flags the drive as differing from its snapshot and skips the
// file). Any-order and resumable: a hash already satisfied by an earlier drive is
// confirmed, not recopied. Sources are read-only; only the destination is written.
func (a *App) ExecutePlanFromDrive(planID int, mountPath, serial string, progress func(float64, string)) (*PlanExecResult, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return nil, fmt.Errorf("plan not found")
	}
	if plan.Status != PlanCompiled && plan.Status != PlanExecuting {
		return nil, fmt.Errorf("plan %q is %s — compile it before executing", plan.Name, plan.Status)
	}
	if strings.TrimSpace(plan.DestinationRoot) == "" {
		return nil, fmt.Errorf("plan has no destination_root")
	}
	if fi, err := os.Stat(mountPath); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("cannot read source drive %s", mountPath)
	}
	// The destination is WRITTEN — it must never resolve inside a registered source.
	if err := a.Store.AssertOutsideSources(plan.DestinationRoot); err != nil {
		return nil, err
	}

	serial = strings.TrimSpace(serial)
	var vol *Volume
	if serial != "" {
		vol = a.Store.VolumeBySerial(serial)
	}
	if vol == nil {
		if id, err := resolveDeviceIdentity(mountPath); err == nil && id.Serial != "" {
			vol = a.Store.VolumeBySerial(id.Serial)
		}
	}
	if vol == nil {
		return nil, fmt.Errorf("this drive isn't a known volume — dock-ingest it first so it has a snapshot")
	}
	snap := a.Store.VolumeSnapshot(vol.ID)
	if snap == nil {
		return nil, fmt.Errorf("no snapshot for %s — dock-ingest the drive first", vol.Label)
	}

	if err := os.MkdirAll(plan.DestinationRoot, 0o755); err != nil {
		return nil, err
	}
	a.ensurePlanDestVolume(plan)
	a.Store.BeginBatch()
	defer a.Store.EndBatch()
	if plan.Status == PlanCompiled {
		plan.Status = PlanExecuting
	}
	if plan.Satisfied == nil {
		plan.Satisfied = map[string]bool{}
	}

	res := &PlanExecResult{PlanID: plan.ID, VolumeID: vol.ID, Drive: vol.Label}
	th := &throttler{bps: a.LoadConfig().ThrottleMbps * 1e6, start: time.Now()}
	var refs []ChunkFileRef
	// Deterministic order so resume is stable and progress reads sensibly.
	work := append([]SnapFile(nil), snap.Files...)
	sort.Slice(work, func(i, j int) bool { return work[i].RelPath < work[j].RelPath })
	total := len(work)
	for i, sf := range work {
		if sf.Hash == "" {
			continue
		}
		planned, ok := plan.Mapping[sf.Hash]
		if !ok {
			continue // not in this plan (parked / unrouted / out of scope)
		}
		progress(0.02+float64(i)/float64(maxInt(total, 1))*0.94, progStats(res.Bytes, 0, int64(res.Copied), int64(total), "executing "+sf.RelPath))
		if plan.Satisfied[sf.Hash] {
			res.Confirmed++ // an earlier drive already placed this content
			continue
		}
		src := filepath.Join(mountPath, filepath.FromSlash(sf.RelPath))
		dest := filepath.Join(plan.DestinationRoot, filepath.FromSlash(planned))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			res.Unreadable++
			continue
		}
		tmp := dest + mirrorTmpSuffix
		streamHash, n, err := mirrorCopyFile(src, tmp, th, func(int64) {})
		if err != nil {
			_ = os.Remove(tmp)
			res.Unreadable++
			continue
		}
		// Re-verify the SOURCE against its snapshot hash. A mismatch means the drive
		// no longer matches what we cataloged — flag loudly and skip; never guess.
		if streamHash != sf.Hash {
			_ = os.Remove(tmp)
			res.DriveDiffers++
			a.Store.Log("plan", fmt.Sprintf("⚠ %s DIFFERS FROM ITS SNAPSHOT at %s — skipped to review", vol.Label, sf.RelPath))
			continue
		}
		// Copy-then-verify: read the destination back before it gets its real name.
		if rb, rerr := hashFileHex(tmp); rerr != nil || rb != streamHash {
			_ = os.Remove(tmp)
			res.Unreadable++
			continue
		}
		if err := atomicRename(tmp, dest); err != nil {
			_ = os.Remove(tmp)
			res.Unreadable++
			continue
		}
		plan.Satisfied[sf.Hash] = true
		res.Copied++
		res.Bytes += n
		refs = append(refs, ChunkFileRef{RelPath: planned, SizeBytes: n, Hash: sf.Hash})
		if res.Copied%25 == 0 {
			a.Store.UpdatePlan(plan) // periodic persist → resumable mid-drive
		}
	}

	a.recordPlanDestCopies(plan, refs)
	if a.PlanCoverage(plan).Pct >= 100 {
		now := time.Now().UTC()
		plan.Status, plan.ClosedAt = PlanClosed, &now
	}
	a.Store.UpdatePlan(plan)
	res.Coverage = a.PlanCoverage(plan)
	res.Message = fmt.Sprintf("%s: copied %d, confirmed %d, %d differ from snapshot. Plan %q %.0f%% complete.",
		vol.Label, res.Copied, res.Confirmed, res.DriveDiffers, plan.Name, res.Coverage.Pct)
	if len(res.Coverage.RemainingDrives) > 0 {
		res.Message += " Remaining files live on: " + strings.Join(res.Coverage.RemainingDrives, ", ") + "."
	}
	a.Store.Log("plan", res.Message)
	return res, nil
}

// ensurePlanDestVolume lazily creates the volume the reorganized copies are recorded
// on (so protection/coverage count the destination), once per plan.
func (a *App) ensurePlanDestVolume(plan *Plan) {
	if plan.DestVolumeID != 0 && a.Store.Volume(plan.DestVolumeID) != nil {
		return
	}
	v := a.Store.AddVolume(Volume{Label: plan.Name + " — destination", Kind: "HDD",
		Notes: "reorganized destination for plan #" + fmt.Sprint(plan.ID)})
	plan.DestVolumeID = v.ID
}

// recordPlanDestCopies records the just-copied files as verified copies on the
// destination volume, per in-scope archive (content-addressed by hash), so coverage
// credits the reorganized destination. Source drives keep their historical copies —
// nothing is ever removed. Best-effort: with no archive scope there's nothing to
// credit and it no-ops.
func (a *App) recordPlanDestCopies(plan *Plan, refs []ChunkFileRef) {
	if len(refs) == 0 || plan.DestVolumeID == 0 || len(plan.ArchiveIDs) == 0 {
		return
	}
	vol := a.Store.Volume(plan.DestVolumeID)
	if vol == nil {
		return
	}
	byHash := map[string]ChunkFileRef{}
	for _, r := range refs {
		byHash[r.Hash] = r
	}
	for _, aid := range plan.ArchiveIDs {
		var ar []ChunkFileRef
		for _, f := range a.Store.FilesOf(aid) {
			if r, ok := byHash[f.Hash]; ok {
				ar = append(ar, ChunkFileRef{FileID: f.ID, RelPath: r.RelPath, SizeBytes: r.SizeBytes, Hash: f.Hash})
			}
		}
		if len(ar) > 0 {
			sort.Slice(ar, func(i, j int) bool { return ar[i].RelPath < ar[j].RelPath })
			a.upsertMirrorChunk(aid, vol, plan.DestinationRoot, ar, "mirror")
		}
	}
}

// ---- coverage ----------------------------------------------------------

// PlanCoverage is the plan's progress and where the remaining work still lives.
type PlanCoverage struct {
	Total           int      `json:"total"`
	Satisfied       int      `json:"satisfied"`
	Remaining       int      `json:"remaining"`
	Pct             float64  `json:"pct"`
	RemainingDrives []string `json:"remaining_drives,omitempty"`
}

// PlanCoverage computes how much of a compiled plan is done and which drives still
// hold unsatisfied content.
func (a *App) PlanCoverage(plan *Plan) PlanCoverage {
	cov := PlanCoverage{Total: len(plan.Mapping)}
	for h := range plan.Mapping {
		if plan.Satisfied[h] {
			cov.Satisfied++
		}
	}
	cov.Remaining = cov.Total - cov.Satisfied
	if cov.Total > 0 {
		cov.Pct = round1(float64(cov.Satisfied) / float64(cov.Total) * 100)
	}
	seen := map[string]bool{}
	for _, snap := range a.Store.VolumeSnapshots() {
		for _, sf := range snap.Files {
			if _, ok := plan.Mapping[sf.Hash]; ok && !plan.Satisfied[sf.Hash] {
				name := snap.Label
				if name == "" {
					name = fmt.Sprintf("vol#%d", snap.VolumeID)
				}
				if !seen[name] {
					seen[name] = true
					cov.RemainingDrives = append(cov.RemainingDrives, name)
				}
				break
			}
		}
	}
	sort.Strings(cov.RemainingDrives)
	return cov
}

// ClosePlan finalizes a plan (final report), leaving sources untouched.
func (a *App) ClosePlan(planID int) (*Plan, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return nil, fmt.Errorf("plan not found")
	}
	now := time.Now().UTC()
	plan.Status, plan.ClosedAt = PlanClosed, &now
	a.Store.UpdatePlan(plan)
	return plan, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PlanWorkItem is a compiled/executing plan that a specific docked drive can make
// progress on — surfaced by the dock watcher as "Execute plan work from this drive."
type PlanWorkItem struct {
	PlanID  int    `json:"plan_id"`
	Name    string `json:"name"`
	Pending int    `json:"pending"`
}

// plansPendingForVolume returns the plans this volume (by its snapshot) still has
// unsatisfied work for — the hook the dock uses to offer execution when the drive
// with pending work is inserted.
func (a *App) plansPendingForVolume(volumeID int) []PlanWorkItem {
	snap := a.Store.VolumeSnapshot(volumeID)
	if snap == nil {
		return nil
	}
	have := map[string]bool{}
	for _, sf := range snap.Files {
		if sf.Hash != "" {
			have[sf.Hash] = true
		}
	}
	var out []PlanWorkItem
	for _, p := range a.Store.Plans() {
		if p.Status != PlanCompiled && p.Status != PlanExecuting {
			continue
		}
		pending := 0
		for h := range p.Mapping {
			if have[h] && !p.Satisfied[h] {
				pending++
			}
		}
		if pending > 0 {
			out = append(out, PlanWorkItem{PlanID: p.ID, Name: p.Name, Pending: pending})
		}
	}
	return out
}
