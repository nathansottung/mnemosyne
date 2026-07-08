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

// routeTokens is the complete, deliberately tiny token vocabulary. More than this
// is a knob we don't ship — the editor lists exactly these.
var routeTokens = []string{"{year}", "{date}", "{event_type}", "{event}", "{camera_serial}", "{orig_name}"}

// tokenValues resolves every token for one file given its event (may be nil).
// Empty strings mark tokens that cannot be filled (no date, no event, …); a route
// referencing an empty token is treated as unroutable in the preview.
func tokenValues(f *File, ev *Event) map[string]string {
	v := map[string]string{
		"{year}": "", "{date}": "", "{event_type}": "", "{event}": "",
		"{camera_serial}": f.CameraSerial, "{orig_name}": path.Base(f.RelPath),
	}
	if !f.ShotAt.IsZero() {
		v["{year}"] = f.ShotAt.Format("2006")
		v["{date}"] = f.ShotAt.Format("2006-01-02")
	}
	if ev != nil {
		if ev.Year > 0 {
			v["{year}"] = itoaSafe(ev.Year)
		}
		v["{event_type}"] = ev.EventType
		v["{event}"] = ev.Name
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
// collide (two sources → one destination) and await conflict review, plus a few
// example placements.
type RoutePreview struct {
	TemplateID      int              `json:"template_id"`
	Total           int              `json:"total"`
	Placed          int              `json:"placed"`
	NoRoute         int              `json:"no_route"`
	Conflicts       int              `json:"conflicts"`
	Examples        []RoutePlacement `json:"examples"`
	NoRouteExamples []string         `json:"no_route_examples,omitempty"`
}

// RoutePreview computes the consequence preview for a template over a collection
// (0 = every archive). Pure planning — no file is touched.
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

	res := RoutePreview{TemplateID: t.ID, Total: len(files)}
	destCount := map[string]int{}
	type pending struct {
		fid  int
		src  string
		dest string
		role string
	}
	var placements []pending
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
		destCount[normPath(dest)]++
		placements = append(placements, pending{f.ID, f.RelPath, dest, role})
	}
	for _, p := range placements {
		if destCount[normPath(p.dest)] > 1 {
			res.Conflicts++
		}
		if len(res.Examples) < 5 {
			res.Examples = append(res.Examples, RoutePlacement{FileID: p.fid, Src: p.src, Dest: p.dest, Role: p.role})
		}
	}
	return res
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
