package main

// templates.go — a routing template is a tiny document: per file role, ONE
// destination pattern built from a fixed, small token set. This file expands those
// tokens against real files and computes the "consequence preview" the editor
// shows live — how many files a template would place, how many match no route, and
// how many collide and await conflict review. It PLANS placements; it never moves a
// byte (exceptions are handled by drag-in-tree, not here).

import (
	"path"
	"sort"
	"strings"
)

// routeTokens is the complete, deliberately tiny token vocabulary — discipline-
// neutral. The grouping tokens come in aliased pairs so every persona's templates
// read naturally over the SAME underlying Event model: {event_type}/{category} both
// resolve to the grouping's type, and {event}/{collection}/{session}/{project} all
// resolve to its name. The editor lists exactly these; more is a knob we don't ship.
var routeTokens = []string{
	"{year}", "{date}",
	"{event_type}", "{category}",
	"{event}", "{collection}", "{session}", "{project}",
	"{camera_serial}", "{orig_name}",
}

// grouping-name aliases all resolve to Event.Name; type aliases to Event.EventType.
var groupingNameTokens = []string{"{event}", "{collection}", "{session}", "{project}"}
var groupingTypeTokens = []string{"{event_type}", "{category}"}

// tokenValues resolves every token for one file given its event (may be nil).
// Empty strings mark tokens that cannot be filled (no date, no grouping, …); a route
// referencing an empty token is treated as unroutable in the preview.
func tokenValues(f *File, ev *Event) map[string]string {
	v := map[string]string{
		"{year}": "", "{date}": "",
		"{camera_serial}": f.CameraSerial, "{orig_name}": path.Base(f.RelPath),
	}
	for _, t := range groupingTypeTokens {
		v[t] = ""
	}
	for _, t := range groupingNameTokens {
		v[t] = ""
	}
	if !f.ShotAt.IsZero() {
		v["{year}"] = f.ShotAt.Format("2006")
		v["{date}"] = f.ShotAt.Format("2006-01-02")
	}
	if ev != nil {
		if ev.Year > 0 {
			v["{year}"] = itoaSafe(ev.Year)
		}
		for _, t := range groupingTypeTokens {
			v[t] = ev.EventType
		}
		for _, t := range groupingNameTokens {
			v[t] = ev.Name
		}
	}
	return v
}

// expandRoute fills a pattern's tokens for a file, appending the original filename
// when the pattern is a directory (ends in "/"). It returns ok=false when the
// pattern uses a token this file cannot fill — the file then "matches no route".
func expandRoute(pattern string, f *File, ev *Event) (string, bool) {
	if strings.TrimSpace(pattern) == "" {
		return "", false
	}
	vals := tokenValues(f, ev)
	out := pattern
	for _, tok := range routeTokens {
		if !strings.Contains(out, tok) {
			continue
		}
		val := vals[tok]
		if strings.TrimSpace(val) == "" {
			return "", false // a required token can't be filled → unroutable
		}
		out = strings.ReplaceAll(out, tok, safePathSeg(val))
	}
	out = strings.ReplaceAll(out, "//", "/")
	if strings.HasSuffix(pattern, "/") {
		out = strings.TrimRight(out, "/") + "/" + safePathSeg(path.Base(f.RelPath))
	}
	return out, true
}

// routeForFile picks the destination pattern for a file's role and expands it.
// roleOf falls back to extension classification when the stored role is blank.
func routeForFile(t *Template, reg map[string]FormatEntry, f *File, ev *Event) (dest string, routed bool) {
	role := f.Role
	if role == "" {
		role, _ = classifyRole(reg, f.RelPath)
	}
	pattern := t.Routes[role]
	if pattern == "" {
		return "", false
	}
	return expandRoute(pattern, f, ev)
}

// RoutePlacement is one file's planned destination in the preview.
type RoutePlacement struct {
	FileID int    `json:"file_id"`
	Src    string `json:"src"`
	Dest   string `json:"dest"`
	Role   string `json:"role"`
}

// RoutePreview is the template editor's live consequence summary against the real
// catalog: how many files the template places, how many match no route, how many
// destination collisions were auto-disambiguated (second-shooter / keep-both →
// distinct placements), and — the compile gate — how many UNRESOLVED true content
// conflicts sit in scope. Blocked is true whenever such conflicts exist: the plan
// refuses to compile until they are resolved in the Conflicts queue.
type RoutePreview struct {
	TemplateID          int              `json:"template_id"`
	Total               int              `json:"total"`
	Placed              int              `json:"placed"`
	NoRoute             int              `json:"no_route"`
	Disambiguated       int              `json:"disambiguated"`
	Blocked             bool             `json:"blocked"`
	UnresolvedConflicts int              `json:"unresolved_conflicts"`
	CollectionID        int              `json:"collection_id"`
	Examples            []RoutePlacement `json:"examples"`
	NoRouteExamples     []string         `json:"no_route_examples,omitempty"`
}

// RoutePreview computes the consequence preview for a template over a collection
// (0 = every archive). Pure planning — no file is touched. It REFUSES (Blocked) to
// compile while unresolved true conflicts exist in scope, and auto-disambiguates
// two files that would land on one destination so keep-both/second-shooter files
// compile to two distinct placements rather than a naming collision.
func (a *App) RoutePreview(t *Template, collectionID int) RoutePreview {
	reg := a.formatRegistry()
	events := map[int]*Event{}
	for _, e := range a.Store.Events(0) {
		events[e.ID] = e
	}
	var files []*File
	if collectionID == 0 {
		files = a.Store.AllFiles()
	} else {
		files = a.Store.FilesOf(collectionID)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })

	res := RoutePreview{TemplateID: t.ID, Total: len(files), CollectionID: collectionID}
	res.UnresolvedConflicts = a.Store.OpenConflictCount(collectionID)
	res.Blocked = res.UnresolvedConflicts > 0

	type pending struct {
		fid    int
		src    string
		dest   string
		role   string
		serial string
	}
	byDest := map[string][]*pending{}
	var placements []*pending
	for _, f := range files {
		role := f.Role
		if role == "" {
			role, _ = classifyRole(reg, f.RelPath)
		}
		dest, ok := routeForFile(t, reg, f, events[f.EventID])
		if !ok {
			res.NoRoute++
			if len(res.NoRouteExamples) < 5 {
				res.NoRouteExamples = append(res.NoRouteExamples, f.RelPath)
			}
			continue
		}
		res.Placed++
		p := &pending{f.ID, f.RelPath, dest, role, f.CameraSerial}
		placements = append(placements, p)
		byDest[normPath(dest)] = append(byDest[normPath(dest)], p)
	}

	// Disambiguate destination collisions: the first keeps the name; each subsequent
	// gets {camera_serial} (when distinct) or an ordinal appended before the
	// extension — so two legitimate files compile to two placements, not a collision.
	for _, group := range byDest {
		if len(group) < 2 {
			continue
		}
		usedSerial := map[string]bool{}
		for i, p := range group {
			if i == 0 {
				if p.serial != "" {
					usedSerial[p.serial] = true
				}
				continue
			}
			var suffix string
			if p.serial != "" && !usedSerial[p.serial] {
				suffix, usedSerial[p.serial] = safePathSeg(p.serial), true
			} else {
				suffix = itoaSafe(i + 1)
			}
			p.dest = disambiguateDest(p.dest, suffix)
			res.Disambiguated++
		}
	}

	for _, p := range placements {
		if len(res.Examples) < 5 {
			res.Examples = append(res.Examples, RoutePlacement{FileID: p.fid, Src: p.src, Dest: p.dest, Role: p.role})
		}
	}
	return res
}

// disambiguateDest appends a suffix before the file extension: a/b/DSC1.jpg +
// "CAM-2" → a/b/DSC1-CAM-2.jpg.
func disambiguateDest(dest, suffix string) string {
	ext := path.Ext(dest)
	return strings.TrimSuffix(dest, ext) + "-" + suffix + ext
}

// safePathSeg keeps a token value legible as a path segment: trims, collapses
// whitespace, and drops characters that would break a path. Not a security control
// (nothing is written) — just keeps preview destinations clean.
func safePathSeg(s string) string {
	s = strings.TrimSpace(s)
	repl := func(r rune) rune {
		switch r {
		case ':', '*', '?', '"', '<', '>', '|', '\\':
			return '-'
		}
		return r
	}
	return strings.Map(repl, s)
}

// itoaSafe renders a non-negative int without importing strconv here.
func itoaSafe(n int) string {
	if n <= 0 {
		return ""
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
