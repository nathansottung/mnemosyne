package main

// home.go — the Home overview answers ONE question in a single screen: "what does
// this tool know about ALL my data?" — and it must read correctly no matter how
// organized the user is. A NAS-centric user (source folders, packages, drift), a
// shoebox user (a stack of adopted drives and nothing else), and a mixed user all
// get a truthful picture:
//
//   1. Totals — distinct content known (deduped by hash across archives AND drive
//      snapshots), bytes, drives/volumes (connected now vs known only from an offline
//      snapshot), locations, last activity.
//   2. Your data — every archive as a card with its protection rollup, PLUS a
//      distinct card for UNGROUPED data: content known only from ingested drives that
//      belongs to no archive yet. Grouping only ever organizes the CATALOG; it never
//      moves a byte on any drive — the card says so.
//   3. Health — under-protected, drift alarms, verify-due, failing-SMART volumes,
//      unresolved conflicts; each a click-through.
//   4. Incremental-backup recognition — an adopted drive that is a near-subset of a
//      SOURCED archive (high content overlap) is characterized as a backup of that
//      archive, not mystery data; several such drives read as "periodic backups."
//
// Everything here is computed from the catalog (pure and testable); the only live
// probe — which volumes are connected right now — is injected by the handler so the
// core stays deterministic under test.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// incrementalContainThreshold: a drive whose content is at least this fraction inside
// one SOURCED archive is read as a backup OF that archive rather than unknown data.
const incrementalContainThreshold = 0.85

// HomeTotals is the top strip: what the tool knows, in totals.
type HomeTotals struct {
	FilesKnown       int    `json:"files_known"` // distinct content across archives + drive snapshots (deduped by hash)
	BytesKnown       int64  `json:"bytes_known"`
	Archives         int    `json:"archives"`
	VolumesKnown     int    `json:"volumes_known"`
	VolumesOnline    int    `json:"volumes_online"`   // connected right now (live probe; 0 when not determined)
	VolumesSnapshot  int    `json:"volumes_snapshot"` // known from an offline inventory snapshot and NOT currently connected
	Locations        int    `json:"locations"`
	LastActivityAt   string `json:"last_activity_at,omitempty"`
	LastActivityWhat string `json:"last_activity_what,omitempty"`
}

// HomeArchiveCard is one archive in the "Your data" panel.
type HomeArchiveCard struct {
	ID           int            `json:"id"`
	Name         string         `json:"name"`
	Sourceless   bool           `json:"sourceless"`
	Files        int            `json:"files"`
	Bytes        int64          `json:"bytes"`
	Protection   map[string]int `json:"protection"` // status -> file count
	UnderProt    int            `json:"under_protected"`
	Protected    int            `json:"protected"`
	DriftChanges int            `json:"drift_changes"`
}

// HomeUngrouped is the distinct card for content known only from drives, in no
// archive yet.
type HomeUngrouped struct {
	Files  int      `json:"files"`
	Bytes  int64    `json:"bytes"`
	Drives int      `json:"drives"`
	Labels []string `json:"labels,omitempty"` // a few contributing drive labels
}

// HomeIncremental characterizes an adopted drive as a backup of a sourced archive.
type HomeIncremental struct {
	VolumeID    int     `json:"volume_id"`
	Label       string  `json:"label"`
	ArchiveID   int     `json:"archive_id"`
	ArchiveName string  `json:"archive_name"`
	MatchFiles  int     `json:"match_files"`
	ContainPct  float64 `json:"contain_pct"`
	AsOf        string  `json:"as_of,omitempty"` // newest file mtime on the drive ≈ when this backup was taken
	Periodic    bool    `json:"periodic"`        // 2+ adopted drives back up the same archive
	Badge       string  `json:"badge"`           // ready-to-show one-liner
}

// HomeHealth is the health strip — every field a click-through in the UI.
type HomeHealth struct {
	UnderProtected int `json:"under_protected"`
	DriftAlarms    int `json:"drift_alarms"`
	DriftArchives  int `json:"drift_archives"`
	VerifyDue      int `json:"verify_due"`
	FailingVolumes int `json:"failing_volumes"`
	OpenConflicts  int `json:"open_conflicts"`
}

// HomeData is the whole overview payload.
type HomeData struct {
	Empty       bool              `json:"empty"` // truly nothing known yet → getting-started
	Totals      HomeTotals        `json:"totals"`
	Archives    []HomeArchiveCard `json:"archives"`
	Ungrouped   *HomeUngrouped    `json:"ungrouped,omitempty"`
	Incremental []HomeIncremental `json:"incremental,omitempty"`
	Health      HomeHealth        `json:"health"`
}

// HomeOverview computes the whole Home picture from the catalog. onlineVolumeIDs is
// the set of volumes connected RIGHT NOW (may be nil in tests / when not probed).
func (a *App) HomeOverview(onlineVolumeIDs map[int]bool) HomeData {
	archives := a.Store.Collections()
	snapshots := a.Store.VolumeSnapshots()
	volumes := a.Store.Volumes()
	chunks := a.Store.Chunks(0)

	var data HomeData
	data.Totals.Archives = len(archives)
	data.Totals.VolumesKnown = len(volumes)
	data.Totals.Locations = len(a.Store.Locations())
	data.Totals.VolumesOnline = len(onlineVolumeIDs)

	// ---- grouped-hash index + per-archive hash sets ----
	// "grouped" content = anything an archive knows: its current files, retained prior
	// versions, and packaged bytes. A drive file matching any of these is not mystery
	// data. Per-archive SHA sets drive the incremental-backup containment test.
	groupedSha := map[string]bool{}
	groupedB3 := map[string]bool{}
	archiveSha := map[int]map[string]bool{}
	archiveB3 := map[int]map[string]bool{}
	for _, coll := range archives {
		sset := map[string]bool{}
		bset := map[string]bool{}
		for _, f := range a.Store.FilesOf(coll.ID) {
			if f.Hash != "" {
				sset[f.Hash] = true
				groupedSha[f.Hash] = true
			}
			if f.Blake3 != "" {
				bset[f.Blake3] = true
				groupedB3[f.Blake3] = true
			}
			for _, v := range f.Versions {
				if v.Hash != "" {
					sset[v.Hash] = true
					groupedSha[v.Hash] = true
				}
			}
		}
		archiveSha[coll.ID] = sset
		archiveB3[coll.ID] = bset
	}
	for _, c := range chunks {
		for _, ref := range c.Files {
			if ref.Hash == "" {
				continue
			}
			groupedSha[ref.Hash] = true
			if s := archiveSha[c.CollectionID]; s != nil {
				s[ref.Hash] = true
			}
		}
	}

	// ---- totals: distinct content known across archives + drives ----
	seen := map[string]bool{}
	addContent := func(hash string, size int64) {
		if hash == "" {
			data.Totals.FilesKnown++
			data.Totals.BytesKnown += size
			return
		}
		if !seen[hash] {
			seen[hash] = true
			data.Totals.FilesKnown++
			data.Totals.BytesKnown += size
		}
	}
	for _, coll := range archives {
		for _, f := range a.Store.FilesOf(coll.ID) {
			addContent(f.Hash, f.SizeBytes)
		}
	}
	for _, snap := range snapshots {
		for _, sf := range snap.Files {
			addContent(sf.Hash, sf.SizeBytes)
		}
	}

	// ---- volumes: snapshot-only (offline) vs connected now ----
	for _, snap := range snapshots {
		if !onlineVolumeIDs[snap.VolumeID] {
			data.Totals.VolumesSnapshot++
		}
	}

	// ---- last activity ----
	if la := a.Store.LastAudit(); la != nil {
		data.Totals.LastActivityAt = la.At.Format(time.RFC3339)
		what := la.Action
		if la.Detail != "" {
			what = la.Detail
		}
		data.Totals.LastActivityWhat = what
	}

	// ---- archive cards + protection rollup ----
	for _, coll := range archives {
		prot := a.Store.Protection(coll.ID)
		card := HomeArchiveCard{ID: coll.ID, Name: coll.Name, Sourceless: coll.IsSourceless(),
			Protection: prot.Summary}
		for _, f := range a.Store.FilesOf(coll.ID) {
			card.Files++
			card.Bytes += f.SizeBytes
		}
		card.UnderProt = prot.Summary[StatusNotBackedUp] + prot.Summary[StatusPartial] + prot.Summary[StatusOutOfPolicy]
		card.Protected = prot.Summary[StatusComplete] + prot.Summary[StatusOverComplete]
		if dr := a.Store.DriftReport(coll.ID); dr != nil {
			card.DriftChanges = dr.Changes()
		}
		data.Archives = append(data.Archives, card)
		data.Health.UnderProtected += card.UnderProt
	}

	// ---- ungrouped data: drive content in no archive ----
	ungroupedSeen := map[string]bool{}
	ungroupedVols := map[int]string{}
	ung := &HomeUngrouped{}
	for _, snap := range snapshots {
		for _, sf := range snap.Files {
			grouped := (sf.Hash != "" && groupedSha[sf.Hash]) || (sf.Blake3 != "" && groupedB3[sf.Blake3])
			if grouped {
				continue
			}
			ungroupedVols[snap.VolumeID] = nonEmpty(snap.Label, fmt.Sprintf("vol#%d", snap.VolumeID))
			key := sf.Hash
			if key == "" {
				key = fmt.Sprintf("v%d|%s", snap.VolumeID, sf.RelPath)
			}
			if !ungroupedSeen[key] {
				ungroupedSeen[key] = true
				ung.Files++
				ung.Bytes += sf.SizeBytes
			}
		}
	}
	if ung.Files > 0 {
		ung.Drives = len(ungroupedVols)
		labels := make([]string, 0, len(ungroupedVols))
		for _, l := range ungroupedVols {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		if len(labels) > 4 {
			labels = labels[:4]
		}
		ung.Labels = labels
		data.Ungrouped = ung
	}

	// ---- incremental-backup recognition ----
	// Two feeds, merged: content-containment against adopted-drive snapshots, PLUS the
	// explicit incremental "back up changes" sessions (which name their target volume
	// and archive directly — no heuristic needed).
	data.Incremental = a.detectIncrementalBackups(archives, snapshots, archiveSha, archiveB3)
	data.Incremental = append(data.Incremental, a.sessionIncrementals(archives, data.Incremental)...)
	sort.Slice(data.Incremental, func(i, j int) bool {
		if data.Incremental[i].ArchiveName != data.Incremental[j].ArchiveName {
			return data.Incremental[i].ArchiveName < data.Incremental[j].ArchiveName
		}
		return data.Incremental[i].AsOf < data.Incremental[j].AsOf
	})

	// ---- remaining health signals ----
	drifted := map[int]bool{}
	for _, dr := range a.Store.DriftReports() {
		if ch := dr.Changes(); ch > 0 {
			data.Health.DriftAlarms += ch
			drifted[dr.CollectionID] = true
		}
	}
	data.Health.DriftArchives = len(drifted)
	months := a.LoadConfig().VerifyDueMonths
	if months <= 0 {
		months = 12
	}
	cutoff := time.Now().AddDate(0, -months, 0)
	for _, c := range chunks {
		if chunkVerifyDue(c, cutoff) {
			data.Health.VerifyDue++
		}
	}
	for _, v := range volumes {
		if volumeSmartFailing(v) {
			data.Health.FailingVolumes++
		}
	}
	data.Health.OpenConflicts = a.Store.OpenConflictCount(0)

	data.Empty = len(archives) == 0 && len(volumes) == 0 && len(snapshots) == 0
	return data
}

// detectIncrementalBackups characterizes adopted drives that are near-subsets of a
// SOURCED archive. Several drives backing up the same archive read as "periodic
// backups"; one reads as "a backup." Purely descriptive — nothing is moved.
func (a *App) detectIncrementalBackups(archives []*Collection, snapshots []*VolumeSnapshot,
	archiveSha map[int]map[string]bool, archiveB3 map[int]map[string]bool) []HomeIncremental {
	sourced := map[int]*Collection{}
	for _, c := range archives {
		if !c.IsSourceless() {
			sourced[c.ID] = c
		}
	}
	if len(sourced) == 0 {
		return nil
	}
	var out []HomeIncremental
	perArchive := map[int]int{} // archiveID -> how many drives back it up
	for _, snap := range snapshots {
		if snap.TotalFiles < 3 {
			continue
		}
		bestAID, bestMatch := 0, 0
		var bestMax time.Time
		for aid := range sourced {
			sset, bset := archiveSha[aid], archiveB3[aid]
			matched := 0
			var maxMT time.Time
			for _, sf := range snap.Files {
				if (sf.Hash != "" && sset[sf.Hash]) || (sf.Blake3 != "" && bset[sf.Blake3]) {
					matched++
					if sf.ModTime.After(maxMT) {
						maxMT = sf.ModTime
					}
				}
			}
			if matched > bestMatch {
				bestAID, bestMatch, bestMax = aid, matched, maxMT
			}
		}
		if bestAID == 0 {
			continue
		}
		contain := float64(bestMatch) / float64(snap.TotalFiles)
		if contain < incrementalContainThreshold {
			continue
		}
		perArchive[bestAID]++
		e := HomeIncremental{VolumeID: snap.VolumeID, Label: nonEmpty(snap.Label, fmt.Sprintf("vol#%d", snap.VolumeID)),
			ArchiveID: bestAID, ArchiveName: sourced[bestAID].Name, MatchFiles: bestMatch,
			ContainPct: round1(contain * 100)}
		if !bestMax.IsZero() {
			e.AsOf = bestMax.Format("2006-01-02")
		}
		out = append(out, e)
	}
	// Second pass: set periodic + badge text now that per-archive counts are known.
	for i := range out {
		out[i].Periodic = perArchive[out[i].ArchiveID] >= 2
		if out[i].Periodic {
			out[i].Badge = fmt.Sprintf("looks like periodic backups of %s", out[i].ArchiveName)
		} else {
			out[i].Badge = fmt.Sprintf("looks like a backup of %s", out[i].ArchiveName)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ArchiveName != out[j].ArchiveName {
			return out[i].ArchiveName < out[j].ArchiveName
		}
		return out[i].AsOf < out[j].AsOf
	})
	return out
}

// sessionIncrementals turns explicit incremental "back up changes" sessions into
// recognized backups — natively, since each session already names its volume and the
// SOURCED archive it copied from. Skips (volume,archive) pairs already recognized by
// the snapshot-containment feed so a drive is never listed twice. Periodic when a
// volume received 2+ sessions, or when 2+ volumes back the same archive.
func (a *App) sessionIncrementals(archives []*Collection, existing []HomeIncremental) []HomeIncremental {
	sourced := map[int]*Collection{}
	for _, c := range archives {
		if !c.IsSourceless() {
			sourced[c.ID] = c
		}
	}
	if len(sourced) == 0 {
		return nil
	}
	type key struct{ vol, arch int }
	agg := map[key]*HomeIncremental{}
	sessCount := map[key]int{}
	archVols := map[int]map[int]bool{}
	for _, s := range a.Store.BackupSessions(0) {
		c := sourced[s.CollectionID]
		if c == nil {
			continue
		}
		k := key{s.VolumeID, s.CollectionID}
		e := agg[k]
		if e == nil {
			e = &HomeIncremental{VolumeID: s.VolumeID, Label: nonEmpty(s.VolumeLabel, fmt.Sprintf("vol#%d", s.VolumeID)),
				ArchiveID: s.CollectionID, ArchiveName: c.Name, ContainPct: 100}
			agg[k] = e
		}
		e.MatchFiles += s.Files
		if d := s.At.Format("2006-01-02"); d > e.AsOf {
			e.AsOf = d
		}
		sessCount[k]++
		if archVols[s.CollectionID] == nil {
			archVols[s.CollectionID] = map[int]bool{}
		}
		archVols[s.CollectionID][s.VolumeID] = true
	}
	seen := map[key]bool{}
	for _, e := range existing {
		seen[key{e.VolumeID, e.ArchiveID}] = true
	}
	var out []HomeIncremental
	for k, e := range agg {
		if seen[k] {
			continue
		}
		e.Periodic = sessCount[k] >= 2 || len(archVols[k.arch]) >= 2
		if e.Periodic {
			e.Badge = "looks like periodic backups of " + e.ArchiveName
		} else {
			e.Badge = "looks like a backup of " + e.ArchiveName
		}
		out = append(out, *e)
	}
	return out
}

// chunkVerifyDue mirrors the UI's verify-due rule server-side: a VERIFIED (or
// ADOPTED-VERIFIED) package whose most recent OK verify is older than the window (or
// never recorded) is due for a re-check.
func chunkVerifyDue(c *Chunk, cutoff time.Time) bool {
	if c.Status != "VERIFIED" && c.Status != StatusAdoptedVerified {
		return false
	}
	var latest time.Time
	for _, ev := range c.VerifyEvents {
		if ev.OK && ev.At.After(latest) {
			latest = ev.At
		}
	}
	if latest.IsZero() && c.VerifyOK != nil && *c.VerifyOK && c.VerifiedAt != nil {
		latest = *c.VerifiedAt
	}
	if latest.IsZero() {
		return true
	}
	return latest.Before(cutoff)
}

// volumeSmartFailing reports whether a volume's latest SMART reading is failing or
// carries a migrate-off advisory.
func volumeSmartFailing(v *Volume) bool {
	if v == nil || len(v.SmartHistory) == 0 {
		return false
	}
	last := v.SmartHistory[len(v.SmartHistory)-1]
	return last.Advisory || (last.Passed != nil && !*last.Passed)
}

// onlineProbe caches the connected-volume probe briefly. Resolving device identity
// per mount can shell out (Get-Disk / lsblk), so the dashboard must not re-probe on
// every load; a few seconds is fresh enough for "connected right now."
var onlineProbe struct {
	mu  sync.Mutex
	at  time.Time
	val map[int]bool
}

// onlineVolumeIDsCached returns the connected-volume set, refreshing at most every
// few seconds. Handler-only; HomeOverview itself takes the set as a parameter so it
// stays pure under test.
func (a *App) onlineVolumeIDsCached() map[int]bool {
	onlineProbe.mu.Lock()
	if onlineProbe.val != nil && time.Since(onlineProbe.at) < 5*time.Second {
		v := onlineProbe.val
		onlineProbe.mu.Unlock()
		return v
	}
	onlineProbe.mu.Unlock()
	v := a.onlineVolumeIDs()
	onlineProbe.mu.Lock()
	onlineProbe.val, onlineProbe.at = v, time.Now()
	onlineProbe.mu.Unlock()
	return v
}

// onlineVolumeIDs is the live probe (handler-only, never under test): the registered
// volumes whose drives are connected right now, matched by device serial. Best-effort
// and non-fatal — an undetectable mount just isn't counted.
func (a *App) onlineVolumeIDs() map[int]bool {
	out := map[int]bool{}
	for _, m := range enumerateMounts() {
		id, err := resolveDeviceIdentity(m.Path)
		if err != nil || strings.TrimSpace(id.Serial) == "" {
			continue
		}
		if v := a.Store.VolumeBySerial(id.Serial); v != nil {
			out[v.ID] = true
		}
	}
	return out
}
