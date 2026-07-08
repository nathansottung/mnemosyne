package main

// exports.go — hash-keyed PORTABLE documents. The catalog's knowledge (what files
// exist, where they live, where they should go) is exported as plain JSON (+ a CSV
// twin and a printable Markdown companion) keyed by content hash, and imported back
// into a fresh catalog on another machine.
//
// Two kinds:
//   - Structure Export (per archive): every file's sha256, size, role, event,
//     capture date, every known physical location, and its planned path if a plan
//     maps it. Importing it into an empty catalog reconstructs the KNOWLEDGE (not
//     the data): search, locations, and events answer the same.
//   - Plan Export: a compiled plan, per source-drive serial — the pending/satisfied
//     work with hashes and destinations. Importing it lets a DIFFERENT machine
//     execute the move; serial binding makes that safe (a drive only advances the
//     plan when its real serial matches).
//
// Exports contain paths and hashes but ZERO file content — safe to email your
// organization scheme; it contains no images.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const exportNoContentNote = "This document lists file paths, hashes, and locations — it contains NO file content (no images, no bytes). Safe to email or print; it cannot reconstruct your data, only the knowledge of what should exist and where."

// ---- structure export types --------------------------------------------

type ExportLocation struct {
	VolumeSerial string     `json:"volume_serial,omitempty"`
	VolumeLabel  string     `json:"volume_label,omitempty"`
	Path         string     `json:"path,omitempty"`
	Location     string     `json:"location,omitempty"`
	LastVerified *time.Time `json:"last_verified,omitempty"`
}

type ExportFile struct {
	Hash        string           `json:"sha256"`
	Size        int64            `json:"size_bytes"`
	RelPath     string           `json:"rel_path"`
	Role        string           `json:"role,omitempty"`
	Event       string           `json:"event,omitempty"`
	EventType   string           `json:"event_type,omitempty"`
	CaptureDate *time.Time       `json:"capture_date,omitempty"`
	PlannedPath string           `json:"planned_path,omitempty"`
	Locations   []ExportLocation `json:"locations,omitempty"`
}

type ExportEventRef struct {
	Name         string     `json:"name"`
	EventType    string     `json:"event_type,omitempty"`
	Year         int        `json:"year,omitempty"`
	CaptureStart *time.Time `json:"capture_start,omitempty"`
	CaptureEnd   *time.Time `json:"capture_end,omitempty"`
}

type ExportVolumeRef struct {
	Serial   string `json:"serial,omitempty"`
	Label    string `json:"label"`
	Kind     string `json:"kind,omitempty"`
	Location string `json:"location,omitempty"`
}

type ExportLocationRef struct {
	Name    string `json:"name"`
	Offsite bool   `json:"offsite,omitempty"`
}

type StructureExport struct {
	Format           string              `json:"format"` // "mnemosyne-structure"
	Version          int                 `json:"version"`
	ArchiveID        int                 `json:"archive_id"`
	Archive          string              `json:"archive"`
	Kind             string              `json:"kind,omitempty"`
	GeneratedUTC     time.Time           `json:"generated_utc"`
	MnemosyneVersion string              `json:"mnemosyne_version,omitempty"`
	Note             string              `json:"note"`
	Events           []ExportEventRef    `json:"events,omitempty"`
	Volumes          []ExportVolumeRef   `json:"volumes,omitempty"`
	Locations        []ExportLocationRef `json:"locations,omitempty"`
	Files            []ExportFile        `json:"files"`
}

const structureExportVersion = 1

// StructureExport builds the portable knowledge document for an archive.
func (a *App) StructureExport(collectionID int) (StructureExport, error) {
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return StructureExport{}, fmt.Errorf("archive %d not found", collectionID)
	}
	exp := StructureExport{Format: "mnemosyne-structure", Version: structureExportVersion,
		ArchiveID: collectionID, Archive: coll.Name, Kind: coll.Kind, GeneratedUTC: time.Now().UTC(),
		MnemosyneVersion: appVersion, Note: exportNoContentNote}

	// Physical locations per file (serial + path + location name + last verified).
	fileLoc := a.fileExportLocations()
	// Planned path per hash from any compiled/executing plan.
	plannedByHash := a.plannedPathsByHash()
	// Event lookup.
	events := map[int]*Event{}
	for _, e := range a.Store.Events(0) {
		events[e.ID] = e
	}

	usedEvents := map[string]ExportEventRef{}
	usedVols := map[string]ExportVolumeRef{}
	usedLocs := map[string]ExportLocationRef{}
	for _, f := range a.Store.FilesOf(collectionID) {
		ef := ExportFile{Hash: f.Hash, Size: f.SizeBytes, RelPath: f.RelPath, Role: f.Role}
		if !f.ShotAt.IsZero() {
			t := f.ShotAt
			ef.CaptureDate = &t
		}
		if ev := events[f.EventID]; ev != nil {
			ef.Event, ef.EventType = ev.Name, ev.EventType
			usedEvents[ev.Name] = ExportEventRef{Name: ev.Name, EventType: ev.EventType, Year: ev.Year,
				CaptureStart: nz(ev.CaptureStart), CaptureEnd: nz(ev.CaptureEnd)}
		}
		if p, ok := plannedByHash[f.Hash]; ok {
			ef.PlannedPath = p
		}
		ef.Locations = fileLoc[f.ID]
		for _, l := range ef.Locations {
			if l.VolumeLabel != "" || l.VolumeSerial != "" {
				key := l.VolumeSerial + "|" + l.VolumeLabel
				usedVols[key] = ExportVolumeRef{Serial: l.VolumeSerial, Label: l.VolumeLabel, Location: l.Location}
			}
			if l.Location != "" {
				usedLocs[l.Location] = ExportLocationRef{Name: l.Location, Offsite: a.locationOffsite(l.Location)}
			}
		}
		exp.Files = append(exp.Files, ef)
	}
	sort.Slice(exp.Files, func(i, j int) bool { return exp.Files[i].RelPath < exp.Files[j].RelPath })
	for _, e := range usedEvents {
		exp.Events = append(exp.Events, e)
	}
	for _, v := range usedVols {
		exp.Volumes = append(exp.Volumes, v)
	}
	for _, l := range usedLocs {
		exp.Locations = append(exp.Locations, l)
	}
	sort.Slice(exp.Events, func(i, j int) bool { return exp.Events[i].Name < exp.Events[j].Name })
	sort.Slice(exp.Volumes, func(i, j int) bool { return exp.Volumes[i].Label < exp.Volumes[j].Label })
	sort.Slice(exp.Locations, func(i, j int) bool { return exp.Locations[i].Name < exp.Locations[j].Name })
	return exp, nil
}

// fileExportLocations maps each fileID to its known physical homes (deduped by
// serial+path), drawn from every non-FAILED chunk's current copies.
func (a *App) fileExportLocations() map[int][]ExportLocation {
	vols := map[int]*Volume{}
	for _, v := range a.Store.Volumes() {
		vols[v.ID] = v
	}
	out := map[int][]ExportLocation{}
	seen := map[int]map[string]bool{}
	for _, ch := range a.Store.Chunks(0) {
		if ch.Status == "FAILED" {
			continue
		}
		for _, cp := range ch.Copies {
			if cp.Superseded {
				continue
			}
			v := vols[cp.VolumeID]
			base := ExportLocation{LastVerified: cp.LastVerifiedAt}
			if v != nil {
				base.VolumeSerial, base.VolumeLabel = v.Serial, v.Label
				base.Location = a.volumeLocationName(v.ID)
			}
			for _, ref := range ch.Files {
				l := base
				l.Path = ref.RelPath
				key := l.VolumeSerial + "|" + l.VolumeLabel + "|" + l.Path
				if seen[ref.FileID] == nil {
					seen[ref.FileID] = map[string]bool{}
				}
				if seen[ref.FileID][key] {
					continue
				}
				seen[ref.FileID][key] = true
				out[ref.FileID] = append(out[ref.FileID], l)
			}
		}
	}
	return out
}

// plannedPathsByHash returns hash → planned path across every compiled/executing
// plan (first plan wins) — so a Structure Export can annotate "where this should go."
func (a *App) plannedPathsByHash() map[string]string {
	out := map[string]string{}
	for _, p := range a.Store.Plans() {
		if p.Status != PlanCompiled && p.Status != PlanExecuting && p.Status != PlanClosed {
			continue
		}
		for h, dest := range p.Mapping {
			if _, ok := out[h]; !ok {
				out[h] = dest
			}
		}
	}
	return out
}

func (a *App) locationOffsite(name string) bool {
	for _, l := range a.Store.Locations() {
		if l.Name == name {
			return l.Offsite
		}
	}
	return false
}

func nz(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ---- structure renderers (CSV + Markdown) ------------------------------

// StructureCSV renders the export as a flat CSV (locations joined into one cell).
func StructureCSV(exp StructureExport) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"sha256", "size_bytes", "rel_path", "role", "event", "event_type", "capture_date", "planned_path", "locations"})
	for _, f := range exp.Files {
		cap := ""
		if f.CaptureDate != nil {
			cap = f.CaptureDate.UTC().Format(time.RFC3339)
		}
		var locs []string
		for _, l := range f.Locations {
			s := l.VolumeLabel
			if l.VolumeSerial != "" {
				s += "[" + l.VolumeSerial + "]"
			}
			if l.Path != "" {
				s += ":" + l.Path
			}
			if l.Location != "" {
				s += " (" + l.Location + ")"
			}
			locs = append(locs, s)
		}
		_ = w.Write([]string{f.Hash, fmt.Sprint(f.Size), f.RelPath, f.Role, f.Event, f.EventType, cap, f.PlannedPath, strings.Join(locs, " ; ")})
	}
	w.Flush()
	return buf.Bytes()
}

// StructureMarkdown renders the printable "what should exist and where" companion,
// grouped by event — the copy that rides into the Recovery Kit.
func StructureMarkdown(exp StructureExport) string {
	var b strings.Builder
	b.WriteString("# Structure — " + exp.Archive + "\n\n")
	b.WriteString("_Generated " + exp.GeneratedUTC.Format(time.RFC3339) + " · " + fmt.Sprint(len(exp.Files)) + " file(s)._\n\n")
	b.WriteString("> " + exp.Note + "\n\n")
	b.WriteString("This is the archive's organization scheme: what files should exist, where each physically lives today, and (if a move is planned) where it should go. Every file is identified by its SHA-256, so a copy found anywhere can be matched back by hash.\n\n")

	byEvent := map[string][]ExportFile{}
	var order []string
	for _, f := range exp.Files {
		key := f.Event
		if key == "" {
			key = "(no event)"
		}
		if _, ok := byEvent[key]; !ok {
			order = append(order, key)
		}
		byEvent[key] = append(byEvent[key], f)
	}
	sort.Strings(order)
	for _, ev := range order {
		files := byEvent[ev]
		b.WriteString(fmt.Sprintf("\n## %s — %d file(s)\n\n", ev, len(files)))
		b.WriteString("| File | SHA-256 (prefix) | Where it lives | Planned path |\n")
		b.WriteString("|------|------------------|----------------|--------------|\n")
		for i, f := range files {
			if i >= 2000 {
				b.WriteString(fmt.Sprintf("_…and %d more (see the JSON/CSV export)_\n", len(files)-i))
				break
			}
			hp := f.Hash
			if len(hp) > 12 {
				hp = hp[:12]
			}
			var where []string
			for _, l := range f.Locations {
				w := l.VolumeLabel
				if l.Location != "" {
					w += " (" + l.Location + ")"
				}
				where = append(where, w)
			}
			w := strings.Join(where, ", ")
			if w == "" {
				w = "—"
			}
			pp := f.PlannedPath
			if pp == "" {
				pp = "—"
			}
			b.WriteString(fmt.Sprintf("| %s | `%s` | %s | %s |\n", mdCell(f.RelPath), hp, mdCell(w), mdCell(pp)))
		}
	}
	return b.String()
}

// ---- structure import --------------------------------------------------

// ImportStructure reconstructs an archive's KNOWLEDGE (not its data) from a
// Structure Export into the current catalog: the archive, its events, the physical
// volumes/locations, every file (hash/role/event/capture), and content-addressed
// copies so search and locations answer exactly as on the source machine.
func (a *App) ImportStructure(exp StructureExport) (map[string]any, error) {
	if exp.Format != "mnemosyne-structure" {
		return nil, fmt.Errorf("not a Mnemosyne structure export")
	}
	a.Store.BeginBatch()
	defer a.Store.EndBatch()

	coll := a.Store.AddCollectionKind(exp.Archive, nonEmpty(exp.Kind, ArchiveSourceless))

	// Locations (by name) and volumes (by serial/label).
	locID := map[string]int{}
	for _, l := range exp.Locations {
		locID[l.Name] = a.Store.AddLocation(l.Name, l.Offsite, "imported from structure export").ID
	}
	volBySerial := map[string]*Volume{}
	volByLabel := map[string]*Volume{}
	for _, v := range exp.Volumes {
		nv := a.Store.AddVolume(Volume{Label: v.Label, Serial: v.Serial, Kind: nonEmpty(v.Kind, "HDD"),
			LocationID: locID[v.Location], Location: v.Location, Notes: "imported from structure export"})
		if v.Serial != "" {
			volBySerial[v.Serial] = nv
		}
		volByLabel[v.Label] = nv
	}
	// Events (by name).
	evID := map[string]int{}
	for _, e := range exp.Events {
		ev := a.Store.AddEvent(&Event{Name: e.Name, EventType: e.EventType, Year: e.Year,
			CaptureStart: derefT(e.CaptureStart), CaptureEnd: derefT(e.CaptureEnd),
			CollectionID: coll.ID, Source: "IMPORTED"})
		evID[e.Name] = ev.ID
	}

	// Files → the union, then event membership.
	items := make([]unionFile, 0, len(exp.Files))
	for _, f := range exp.Files {
		uf := unionFile{RelPath: f.RelPath, Hash: f.Hash, Size: f.Size, Role: f.Role}
		if f.CaptureDate != nil {
			uf.ShotAt = *f.CaptureDate
		}
		items = append(items, uf)
	}
	ids, _ := a.Store.UpsertUnionFiles(coll.ID, items)
	byHashFileID := map[string]int{}
	for i, f := range exp.Files {
		byHashFileID[f.Hash] = ids[i]
		if f.Event != "" {
			if eid := evID[f.Event]; eid != 0 {
				a.Store.AssignFilesToEvent([]int{ids[i]}, eid)
			}
		}
	}

	// Content-addressed copies: one mirror chunk per volume, listing the files that
	// volume holds — so search/locations show the same homes.
	type volRefs struct {
		vol  *Volume
		refs []ChunkFileRef
	}
	byVol := map[string]*volRefs{}
	for i, f := range exp.Files {
		for _, l := range f.Locations {
			v := volBySerial[l.VolumeSerial]
			if v == nil {
				v = volByLabel[l.VolumeLabel]
			}
			if v == nil {
				continue
			}
			key := fmt.Sprint(v.ID)
			vr := byVol[key]
			if vr == nil {
				vr = &volRefs{vol: v}
				byVol[key] = vr
			}
			vr.refs = append(vr.refs, ChunkFileRef{FileID: ids[i], RelPath: nonEmpty(l.Path, f.RelPath), SizeBytes: f.Size, Hash: f.Hash})
		}
	}
	for _, vr := range byVol {
		sort.Slice(vr.refs, func(i, j int) bool { return vr.refs[i].RelPath < vr.refs[j].RelPath })
		a.upsertMirrorChunk(coll.ID, vr.vol, "(imported)", vr.refs, "mirror")
	}

	a.Store.Log("export", fmt.Sprintf("imported structure %q: %d file(s), %d event(s), %d volume(s)",
		exp.Archive, len(exp.Files), len(exp.Events), len(exp.Volumes)))
	return map[string]any{"archive_id": coll.ID, "archive": coll.Name,
		"files": len(exp.Files), "events": len(exp.Events), "volumes": len(exp.Volumes)}, nil
}

func derefT(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ---- plan export / import ----------------------------------------------

type ExportPlanFile struct {
	Hash      string `json:"sha256"`
	SrcRel    string `json:"src_rel"`
	Size      int64  `json:"size_bytes"`
	Planned   string `json:"planned"`
	Satisfied bool   `json:"satisfied,omitempty"`
}

type ExportPlanDrive struct {
	Serial string           `json:"serial,omitempty"`
	Label  string           `json:"label"`
	Files  []ExportPlanFile `json:"files"`
}

type PlanExport struct {
	Format           string            `json:"format"` // "mnemosyne-plan"
	Version          int               `json:"version"`
	Name             string            `json:"name"`
	DestinationRoot  string            `json:"destination_root"`
	GeneratedUTC     time.Time         `json:"generated_utc"`
	MnemosyneVersion string            `json:"mnemosyne_version,omitempty"`
	Note             string            `json:"note"`
	TemplateName     string            `json:"template_name,omitempty"`
	TemplateRoutes   map[string]string `json:"template_routes,omitempty"`
	Drives           []ExportPlanDrive `json:"drives"`
}

const planExportVersion = 1

// PlanExport builds the portable, serial-bound plan document: per source-drive
// serial, the files that drive can supply with their hashes, source paths, planned
// destinations, and whether each is already satisfied.
func (a *App) PlanExport(planID int) (PlanExport, error) {
	plan := a.Store.Plan(planID)
	if plan == nil {
		return PlanExport{}, fmt.Errorf("plan not found")
	}
	if len(plan.Mapping) == 0 {
		return PlanExport{}, fmt.Errorf("compile the plan before exporting it")
	}
	exp := PlanExport{Format: "mnemosyne-plan", Version: planExportVersion, Name: plan.Name,
		DestinationRoot: plan.DestinationRoot, GeneratedUTC: time.Now().UTC(),
		MnemosyneVersion: appVersion, Note: exportNoContentNote}
	if t := a.Store.Template(plan.TemplateID); t != nil {
		exp.TemplateName, exp.TemplateRoutes = t.Name, t.Routes
	}
	for _, snap := range a.Store.VolumeSnapshots() {
		var files []ExportPlanFile
		for _, sf := range snap.Files {
			dest, ok := plan.Mapping[sf.Hash]
			if !ok {
				continue
			}
			files = append(files, ExportPlanFile{Hash: sf.Hash, SrcRel: sf.RelPath, Size: sf.SizeBytes,
				Planned: dest, Satisfied: plan.Satisfied[sf.Hash]})
		}
		if len(files) == 0 {
			continue
		}
		sort.Slice(files, func(i, j int) bool { return files[i].SrcRel < files[j].SrcRel })
		exp.Drives = append(exp.Drives, ExportPlanDrive{Serial: snap.Serial, Label: snap.Label, Files: files})
	}
	sort.Slice(exp.Drives, func(i, j int) bool { return exp.Drives[i].Label < exp.Drives[j].Label })
	return exp, nil
}

// ImportPlan reconstructs a compiled plan (and the per-serial snapshots it needs to
// execute) from a Plan Export, so a DIFFERENT machine can carry the move out. Serial
// binding keeps it safe: a drive only advances the plan when its real serial matches
// what the export recorded.
func (a *App) ImportPlan(exp PlanExport) (map[string]any, error) {
	if exp.Format != "mnemosyne-plan" {
		return nil, fmt.Errorf("not a Mnemosyne plan export")
	}
	a.Store.BeginBatch()
	defer a.Store.EndBatch()

	// A template to display the routes (create if absent by name).
	var tmpl *Template
	for _, t := range a.Store.Templates() {
		if t.Name == exp.TemplateName {
			tmpl = t
			break
		}
	}
	if tmpl == nil {
		tmpl = a.Store.AddTemplate(&Template{Name: nonEmpty(exp.TemplateName, "Imported plan template"),
			Routes: exp.TemplateRoutes, EventTypes: append([]string(nil), defaultEventVocabulary...)})
	}

	mapping := map[string]string{}
	satisfied := map[string]bool{}
	for _, d := range exp.Drives {
		// Ensure a volume (by serial) + a snapshot so ExecutePlanFromDrive can run.
		var vol *Volume
		if d.Serial != "" {
			vol = a.Store.VolumeBySerial(d.Serial)
		}
		if vol == nil {
			vol = a.Store.AddVolume(Volume{Label: nonEmpty(d.Label, "imported drive"), Serial: d.Serial,
				Kind: "HDD", Notes: "imported from plan export"})
		}
		var sfs []SnapFile
		var total int64
		for _, f := range d.Files {
			sfs = append(sfs, SnapFile{RelPath: f.SrcRel, Hash: f.Hash, SizeBytes: f.Size})
			mapping[f.Hash] = f.Planned
			if f.Satisfied {
				satisfied[f.Hash] = true
			}
			total += f.Size
		}
		a.Store.PutVolumeSnapshot(&VolumeSnapshot{VolumeID: vol.ID, Serial: d.Serial, Label: d.Label,
			Files: sfs, TotalFiles: len(sfs), TotalBytes: total})
	}

	now := time.Now().UTC()
	plan := a.Store.AddPlan(&Plan{Name: exp.Name, TemplateID: tmpl.ID, DestinationRoot: exp.DestinationRoot,
		Status: PlanCompiled, Mapping: mapping, Satisfied: satisfied, CompiledAt: &now})
	a.Store.Log("export", fmt.Sprintf("imported plan %q: %d file(s) across %d drive(s)",
		exp.Name, len(mapping), len(exp.Drives)))
	return map[string]any{"plan_id": plan.ID, "name": plan.Name,
		"files": len(mapping), "drives": len(exp.Drives), "coverage": a.PlanCoverage(plan)}, nil
}

// exportJSON marshals any export value with indentation (stable, human-diffable).
func exportJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
