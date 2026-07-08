package main

// inference.go — "point at an organized tree and learn its shape." Given a NAS
// photos root that a human already sorted into {year}/{event_type}/{event}, this
// detects that pattern, proposes it as a saved routing template, and HARVESTS an
// Event from every leaf event-folder — {name, event_type, year, capture-date
// range from member EXIF}. Harvested events then act as magnets (events.go),
// pulling matching cataloged strays in by capture date. Read-only toward the tree.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// segRoles a path position can play in an organized tree.
const (
	segYear      = "year"
	segEventType = "event_type"
	segEvent     = "event"
	segOther     = "other"
)

// InferredStructure is the outcome of pointing at a tree: the detected pattern,
// per-depth segment roles, the harvested events, and a confidence in the fit.
type InferredStructure struct {
	Root         string          `json:"root"`
	Pattern      string          `json:"pattern"`       // e.g. "{year}/{event_type}/{event}"
	SegmentRoles []string        `json:"segment_roles"` // role per depth position
	Depth        int             `json:"depth"`
	LeafFolders  int             `json:"leaf_folders"`
	Events       []ProposedEvent `json:"events"` // harvested, one per leaf (FileIDs empty — not cataloged)
	Sample       []string        `json:"sample"` // example leaf paths (relative)
	Confidence   float64         `json:"confidence"`
	Note         string          `json:"note,omitempty"`
}

// isYearSeg reports whether a segment looks like a plausible capture year.
func isYearSeg(s string) bool {
	if len(s) != 4 {
		return false
	}
	y := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		y = y*10 + int(r-'0')
	}
	return y >= 1900 && y <= 2100
}

func yearOf(s string) int {
	if !isYearSeg(s) {
		return 0
	}
	return atoiSafe(s)
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// InferStructure walks an organized tree and returns its detected structure with
// harvested events. Leaf folders are directories that directly contain files.
func (a *App) InferStructure(root string) (*InferredStructure, error) {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("not a readable folder: %s", root)
	}
	reg := a.formatRegistry()
	vocab := a.eventVocabulary()

	// Group files by their immediate parent directory (leaf event-folders).
	type leaf struct {
		rel   string
		abs   string
		files []string
	}
	leaves := map[string]*leaf{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDockDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		dir := filepath.Dir(p)
		rel, e := filepath.Rel(root, dir)
		if e != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil // loose files at the root have no event-folder to harvest
		}
		lf := leaves[rel]
		if lf == nil {
			lf = &leaf{rel: rel, abs: dir}
			leaves[rel] = lf
		}
		lf.files = append(lf.files, p)
		return nil
	})
	if len(leaves) == 0 {
		return &InferredStructure{Root: root, Note: "no nested event-folders found — nothing to infer"}, nil
	}

	// Modal depth: the segment count most leaves share is the structure we detect.
	depthCount := map[int]int{}
	for rel := range leaves {
		depthCount[len(strings.Split(rel, "/"))]++
	}
	depth, best := 0, -1
	for d, n := range depthCount {
		if n > best || (n == best && d > depth) {
			depth, best = d, n
		}
	}

	// Classify each position by how often it looks like a year / a vocabulary type.
	yearHits := make([]int, depth)
	typeHits := make([]int, depth)
	atDepth := 0
	for _, lf := range leaves {
		segs := strings.Split(lf.rel, "/")
		if len(segs) != depth {
			continue
		}
		atDepth++
		for i, s := range segs {
			if isYearSeg(s) {
				yearHits[i]++
			}
			if guessEventType(s, vocab) != "" {
				typeHits[i]++
			}
		}
	}
	roles := make([]string, depth)
	yearPos, typePos := -1, -1
	for i := 0; i < depth; i++ {
		// The deepest segment is ALWAYS the event itself — never a year/type, even
		// though an event name ("Smith Family") may contain a vocabulary keyword.
		if i == depth-1 {
			roles[i] = segEvent
			continue
		}
		frac := func(hits []int) float64 {
			if atDepth == 0 {
				return 0
			}
			return float64(hits[i]) / float64(atDepth)
		}
		switch {
		case yearPos < 0 && frac(yearHits) >= 0.5:
			roles[i], yearPos = segYear, i
		case typePos < 0 && frac(typeHits) >= 0.5:
			roles[i], typePos = segEventType, i
		default:
			roles[i] = segOther
		}
	}

	inf := &InferredStructure{Root: root, Depth: depth, SegmentRoles: roles,
		LeafFolders: atDepth, Pattern: patternFromRoles(roles)}

	// Harvest one event per leaf at the modal depth.
	rels := make([]string, 0, len(leaves))
	for rel := range leaves {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var confSum float64
	for _, rel := range rels {
		lf := leaves[rel]
		segs := strings.Split(rel, "/")
		if len(segs) != depth {
			continue
		}
		name := segs[depth-1]
		year := 0
		if yearPos >= 0 {
			year = yearOf(segs[yearPos])
		}
		etype := ""
		if typePos >= 0 {
			etype = guessEventType(segs[typePos], vocab)
		}
		if etype == "" {
			etype = guessEventType(name, vocab)
		}
		start, end, imgCount, bytes := leafExifRange(reg, lf.files)
		if year == 0 && !start.IsZero() {
			year = start.Year()
		}
		inf.Events = append(inf.Events, ProposedEvent{
			Name: name, EventType: etype, Year: year,
			CaptureStart: start, CaptureEnd: end,
			FolderHint: rel, FileCount: imgCount, Bytes: bytes,
		})
		if len(inf.Sample) < 5 {
			inf.Sample = append(inf.Sample, rel)
		}
		// Confidence contribution: 1 if this leaf fit year+type positions cleanly.
		hit := 0.0
		if yearPos < 0 || isYearSeg(segs[yearPos]) {
			hit += 0.5
		}
		if typePos < 0 || guessEventType(segs[typePos], vocab) != "" {
			hit += 0.5
		}
		confSum += hit
	}
	if n := len(inf.Events); n > 0 {
		inf.Confidence = round1(confSum / float64(n))
	}
	return inf, nil
}

// patternFromRoles renders the detected role sequence as a token pattern with a
// trailing slash (a destination folder).
func patternFromRoles(roles []string) string {
	parts := make([]string, len(roles))
	for i, r := range roles {
		switch r {
		case segYear:
			parts[i] = "{year}"
		case segEventType:
			parts[i] = "{event_type}"
		case segEvent:
			parts[i] = "{event}"
		default:
			parts[i] = "*"
		}
	}
	return strings.Join(parts, "/") + "/"
}

// leafExifRange reads the EXIF capture dates of the image files directly in a leaf
// and returns [min,max], the image count, and total bytes. Non-images and
// unparseable files are ignored. Bounded per-file header read (see exif.go).
func leafExifRange(reg map[string]FormatEntry, files []string) (start, end time.Time, imgCount int, bytes int64) {
	for _, p := range files {
		role, _ := classifyRole(reg, p)
		if role != RoleRAW && role != RoleEditedExport {
			continue
		}
		shot, _ := extractShotMeta(p)
		if shot.IsZero() {
			continue
		}
		imgCount++
		if fi, err := os.Stat(p); err == nil {
			bytes += fi.Size()
		}
		if start.IsZero() || shot.Before(start) {
			start = shot
		}
		if end.IsZero() || shot.After(end) {
			end = shot
		}
	}
	return start, end, imgCount, bytes
}

// ProposeTemplateFromInference turns a detected structure into a saveable Template
// (not persisted here). RAW/SIDECAR/CATALOG follow the detected pattern; edited
// exports and video get sensible siblings the operator can tweak.
func (a *App) ProposeTemplateFromInference(inf *InferredStructure, name string) *Template {
	if strings.TrimSpace(name) == "" {
		name = "Inferred structure"
	}
	base := inf.Pattern
	if base == "" {
		base = "{year}/{event_type}/{event}/"
	}
	return &Template{
		Name:       name,
		EventTypes: append([]string(nil), a.eventVocabulary()...),
		Routes: map[string]string{
			RoleRAW:          base,
			RoleSidecar:      base,
			RoleCatalog:      base,
			RoleEditedExport: base,
			RoleVideo:        strings.TrimRight(base, "/") + "/video/",
		},
	}
}
