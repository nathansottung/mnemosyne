package main

// events.go — Events are the unit people think in: "the Henderson wedding," not
// "these 8,000 files." This file turns raw files into proposed Events three ways
// and then makes Events first-class:
//
//   - clustering: on a chaotic (sourceless) archive, group files by EXIF
//     capture-date density into bursts and propose one Event per burst, pre-named
//     from the members' most common folder and typed by folder keywords;
//   - magnets: a saved Event's capture-date range pulls in still-unassigned files
//     whose shot dates fall inside it (suggested in groups, accept/reject);
//   - rollup: protection status aggregated per Event ("this wedding: 1 copy — at
//     risk"), reusing the same six-status model as the folder tree.
//
// Membership is one field on the File (File.EventID); nothing here rewrites media.

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// defaultEventVocabulary seeds a new template's editable event-type list and the
// keyword set the type-guesser matches folder names against.
var defaultEventVocabulary = []string{
	"wedding", "engagement", "baptism", "boudoir", "religious event", "family", "portrait", "other",
}

// Clustering defaults: a burst is ≥minFiles frames whose total span is ≤maxSpan
// days, with no internal gap larger than maxSpan days. Tunable per call.
const (
	defaultClusterMinFiles = 12
	defaultClusterSpanDays = 3
)

// ProposedEvent is a candidate Event the operator confirms/renames/merges before
// it is saved. FileIDs are its members (by capture-date proximity).
type ProposedEvent struct {
	Name         string    `json:"name"`
	EventType    string    `json:"event_type"`
	Year         int       `json:"year"`
	CaptureStart time.Time `json:"capture_start"`
	CaptureEnd   time.Time `json:"capture_end"`
	FolderHint   string    `json:"folder_hint"`
	FileIDs      []int     `json:"file_ids"`
	FileCount    int       `json:"file_count"`
	Bytes        int64     `json:"bytes"`
}

// ClusterEvents groups a collection's dated files into capture-date bursts and
// proposes an Event per burst. minFiles/spanDays default when ≤0. Files without an
// EXIF ShotAt cannot be clustered (there is no date to cluster on) and are skipped.
func (a *App) ClusterEvents(collectionID, minFiles, spanDays int) []ProposedEvent {
	if minFiles <= 0 {
		minFiles = defaultClusterMinFiles
	}
	if spanDays <= 0 {
		spanDays = defaultClusterSpanDays
	}
	span := time.Duration(spanDays) * 24 * time.Hour

	dated := []*File{}
	for _, f := range a.Store.FilesOf(collectionID) {
		if !f.ShotAt.IsZero() && f.EventID == 0 {
			dated = append(dated, f)
		}
	}
	sort.Slice(dated, func(i, j int) bool { return dated[i].ShotAt.Before(dated[j].ShotAt) })

	var out []ProposedEvent
	flush := func(run []*File) {
		if len(run) < minFiles {
			return
		}
		out = append(out, a.proposeFromRun(run))
	}
	var run []*File
	for _, f := range dated {
		if len(run) == 0 {
			run = []*File{f}
			continue
		}
		prev := run[len(run)-1].ShotAt
		start := run[0].ShotAt
		// Break the burst on a big internal gap OR when the span would exceed the cap.
		if f.ShotAt.Sub(prev) > span || f.ShotAt.Sub(start) > span {
			flush(run)
			run = []*File{f}
			continue
		}
		run = append(run, f)
	}
	flush(run)
	return out
}

// proposeFromRun builds a ProposedEvent from a burst of files: name from the most
// common containing folder, type guessed from that name, year/range from the dates.
func (a *App) proposeFromRun(run []*File) ProposedEvent {
	folder := mostCommonFolder(run)
	name := folder
	if name == "" {
		name = run[0].ShotAt.Format("2006-01-02") + " shoot"
	}
	var bytes int64
	ids := make([]int, 0, len(run))
	for _, f := range run {
		ids = append(ids, f.ID)
		bytes += f.SizeBytes
	}
	start, end := run[0].ShotAt, run[len(run)-1].ShotAt
	return ProposedEvent{
		Name: name, EventType: guessEventType(name, a.eventVocabulary()),
		Year: start.Year(), CaptureStart: start, CaptureEnd: end,
		FolderHint: folder, FileIDs: ids, FileCount: len(run), Bytes: bytes,
	}
}

// SuggestGroup is one accept/reject-able cluster of unassigned files a magnet
// pulls toward an Event (grouped by containing folder for a legible decision).
type SuggestGroup struct {
	FolderHint string   `json:"folder_hint"`
	FileIDs    []int    `json:"file_ids"`
	FileCount  int      `json:"file_count"`
	Bytes      int64    `json:"bytes"`
	Sample     []string `json:"sample"`
}

// SuggestForEvent finds still-unassigned files whose EXIF capture date falls in an
// Event's range and returns them grouped by folder — the Event acting as a magnet.
// Harvested Events (a date range, no members yet) thus adopt matching strays.
func (a *App) SuggestForEvent(eventID int) []SuggestGroup {
	ev := a.Store.Event(eventID)
	if ev == nil || ev.CaptureStart.IsZero() || ev.CaptureEnd.IsZero() {
		return nil
	}
	lo, hi := ev.CaptureStart, ev.CaptureEnd
	byFolder := map[string]*SuggestGroup{}
	for _, f := range a.Store.AllFiles() {
		if f.EventID != 0 || f.ShotAt.IsZero() {
			continue
		}
		if ev.CollectionID != 0 && f.CollectionID != ev.CollectionID {
			continue
		}
		if f.ShotAt.Before(lo) || f.ShotAt.After(hi) {
			continue
		}
		dir := path.Dir(f.RelPath)
		g := byFolder[dir]
		if g == nil {
			g = &SuggestGroup{FolderHint: dir}
			byFolder[dir] = g
		}
		g.FileIDs = append(g.FileIDs, f.ID)
		g.FileCount++
		g.Bytes += f.SizeBytes
		if len(g.Sample) < 3 {
			g.Sample = append(g.Sample, path.Base(f.RelPath))
		}
	}
	out := make([]SuggestGroup, 0, len(byFolder))
	for _, g := range byFolder {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FileCount != out[j].FileCount {
			return out[i].FileCount > out[j].FileCount
		}
		return out[i].FolderHint < out[j].FolderHint
	})
	return out
}

// eventVocabulary returns the type vocabulary from the (built-in or first) template,
// falling back to the default seed. Used to type-guess proposed events.
func (a *App) eventVocabulary() []string {
	for _, t := range a.Store.Templates() {
		if len(t.EventTypes) > 0 {
			return t.EventTypes
		}
	}
	return defaultEventVocabulary
}

// guessEventType matches a name/folder against the vocabulary by keyword (longest
// match wins so "religious event" beats "event"); returns "" when nothing matches.
func guessEventType(name string, vocab []string) string {
	low := strings.ToLower(name)
	best, bestLen := "", 0
	for _, v := range vocab {
		key := strings.ToLower(strings.TrimSpace(v))
		if key == "" || key == "other" {
			continue
		}
		// Match the vocabulary word, or its singular-ish stem (weddings→wedding).
		if strings.Contains(low, key) || strings.Contains(low, strings.TrimSuffix(key, "s")) {
			if len(key) > bestLen {
				best, bestLen = v, len(key)
			}
		}
	}
	return best
}

// mostCommonFolder returns the most frequent immediate containing-folder name among
// a set of files (the human label a burst most likely deserves).
func mostCommonFolder(files []*File) string {
	count := map[string]int{}
	for _, f := range files {
		dir := path.Dir(f.RelPath)
		name := path.Base(dir)
		if name == "." || name == "/" || name == "" {
			continue
		}
		count[name]++
	}
	best, bestN := "", 0
	for name, n := range count {
		if n > bestN || (n == bestN && name < best) {
			best, bestN = name, n
		}
	}
	return best
}

// ---- per-Event protection rollup ---------------------------------------

// EventRollup is one Event's aggregated protection: the worst status among its
// member files, a human detail, per-status counts, and totals.
type EventRollup struct {
	EventID   int            `json:"event_id"`
	Name      string         `json:"name"`
	EventType string         `json:"event_type,omitempty"`
	Year      int            `json:"year,omitempty"`
	Status    string         `json:"status"`
	Detail    string         `json:"detail"`
	Counts    map[string]int `json:"counts"`
	Files     int            `json:"files"`
	Bytes     int64          `json:"bytes"`
}

// EventRollups computes the protection rollup for every event (optionally scoped to
// a collection). It reuses the six-status per-file model, aggregated by EventID —
// a new grouping axis parallel to the folder tree. One lock; O(files + chunks).
func (s *Store) EventRollups(collectionID int) []EventRollup {
	s.mu.Lock()
	defer s.mu.Unlock()

	vols := map[int]*Volume{}
	for _, v := range s.c.Volumes {
		vols[v.ID] = v
	}
	locs := s.locationsMapLocked()
	fileCopies := map[int]map[string]physCopy{}
	for _, ch := range s.c.Chunks {
		if ch.Status == "FAILED" {
			continue
		}
		pcs := chunkPhysCopies(ch, vols, locs)
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
		folderPath[fo.ID] = filepath.ToSlash(fo.Path)
	}

	type agg struct {
		counts  map[string]int
		files   int
		bytes   int64
		example map[string]string
	}
	byEvent := map[int]*agg{}
	for _, f := range s.c.Files {
		if f.EventID == 0 {
			continue
		}
		if collectionID != 0 && f.CollectionID != collectionID {
			continue
		}
		st, detail, _ := s.fileProtectionLocked(f, fileCopies, folderPath)
		a := byEvent[f.EventID]
		if a == nil {
			a = &agg{counts: map[string]int{}, example: map[string]string{}}
			byEvent[f.EventID] = a
		}
		a.counts[st]++
		a.files++
		a.bytes += f.SizeBytes
		if _, ok := a.example[st]; !ok {
			a.example[st] = detail
		}
	}

	out := []EventRollup{}
	for _, ev := range s.c.Events {
		if collectionID != 0 && ev.CollectionID != collectionID {
			continue
		}
		a := byEvent[ev.ID]
		r := EventRollup{EventID: ev.ID, Name: ev.Name, EventType: ev.EventType, Year: ev.Year,
			Counts: map[string]int{}, Status: StatusUnassigned}
		if a != nil {
			r.Counts = a.counts
			r.Files = a.files
			r.Bytes = a.bytes
			r.Status = worstStatus(a.counts)
			r.Detail = a.example[r.Status]
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Year != out[j].Year {
			return out[i].Year > out[j].Year
		}
		return out[i].Name < out[j].Name
	})
	return out
}
