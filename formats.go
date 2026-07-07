package main

// formats.go — a format-sustainability registry + per-archive census.
//
// The registry rates a file EXTENSION's long-term readability by two criteria,
// and two only: (1) is the format publicly documented, and (2) are there
// multiple independent, healthy open-source readers? Vendor financial health is
// explicitly NOT a criterion. Tiers are ADVISORY — the app never suggests
// deleting anything; it just tells you which bytes are safest to still open in
// 2050, and (for documented-proprietary formats) names the reader projects and
// any sensible migration, so the knowledge travels WITH the media.
//
// The default registry is embedded (formats.json). Users override/extend it by
// dropping a formats.json in the data dir; entries merge by extension, theirs
// winning.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed formats.json
var defaultFormatsJSON []byte

// Format tiers, best-readable to least.
const (
	TierOpen       = "OPEN"                   // public spec, multiple independent readers
	TierDocumented = "DOCUMENTED-PROPRIETARY" // closed spec, healthy open-source readers
	TierAtRisk     = "AT-RISK"                // single-vendor, weak/no open readers
	TierUnknown    = "UNKNOWN"                // not in the registry
)

// FormatEntry is one extension's sustainability record.
type FormatEntry struct {
	Tier      string   `json:"tier"`
	Rationale string   `json:"rationale"`
	Readers   []string `json:"readers,omitempty"`
	Migration string   `json:"migration,omitempty"`
}

// normExt lowercases an extension and ensures a single leading dot.
func normExt(e string) string {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return ""
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return e
}

// parseRegistry unmarshals a registry blob tolerantly: keys beginning with "_"
// (e.g. the "_comment" doc field) are skipped, and a malformed entry is dropped
// rather than failing the whole file.
func parseRegistry(b []byte, into map[string]FormatEntry) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	for k, v := range raw {
		if strings.HasPrefix(k, "_") {
			continue
		}
		var e FormatEntry
		if json.Unmarshal(v, &e) == nil && e.Tier != "" {
			into[normExt(k)] = e
		}
	}
}

// formatRegistry returns the effective registry: embedded defaults overlaid with
// the user's data-dir formats.json (if present).
func (a *App) formatRegistry() map[string]FormatEntry {
	reg := map[string]FormatEntry{}
	parseRegistry(defaultFormatsJSON, reg)
	if b, err := os.ReadFile(filepath.Join(a.DataDir, "formats.json")); err == nil {
		parseRegistry(b, reg)
	}
	return reg
}

// lookupFormat returns the entry for an extension, or an UNKNOWN placeholder.
func lookupFormat(reg map[string]FormatEntry, ext string) FormatEntry {
	if e, ok := reg[normExt(ext)]; ok {
		if e.Tier == "" {
			e.Tier = TierUnknown
		}
		return e
	}
	return FormatEntry{Tier: TierUnknown, Rationale: "Not in the format registry — longevity unrated."}
}

// ---- census -------------------------------------------------------------

// extTally accumulates file counts + bytes per extension.
type extTally struct {
	count      map[string]int
	bytes      map[string]int64
	total      int
	totalBytes int64
}

func newExtTally() *extTally {
	return &extTally{count: map[string]int{}, bytes: map[string]int64{}}
}

func (t *extTally) add(rel string, size int64) {
	e := pathExt(rel)
	if e == "" {
		e = "(no ext)"
	}
	t.count[e]++
	t.bytes[e] += size
	t.total++
	t.totalBytes += size
}

// CensusRow is one extension in the census.
type CensusRow struct {
	Ext       string   `json:"ext"`
	Count     int      `json:"count"`
	Bytes     int64    `json:"bytes"`
	Tier      string   `json:"tier"`
	Rationale string   `json:"rationale"`
	Readers   []string `json:"readers,omitempty"`
	Migration string   `json:"migration,omitempty"`
}

// Census is a format breakdown for an archive (CollectionID 0 = all archives).
type Census struct {
	CollectionID int              `json:"collection_id"`
	Rows         []CensusRow      `json:"rows"`
	TotalFiles   int              `json:"total_files"`
	TotalBytes   int64            `json:"total_bytes"`
	TierBytes    map[string]int64 `json:"tier_bytes"`
	TierFiles    map[string]int   `json:"tier_files"`
	// SafeBytes = OPEN + DOCUMENTED-PROPRIETARY (formats with independent readers).
	SafeBytes int64   `json:"safe_bytes"`
	SafePct   float64 `json:"safe_pct"`
}

// censusFromTally turns an ext tally into a Census (rows sorted by bytes desc).
func (a *App) censusFromTally(t *extTally) Census {
	reg := a.formatRegistry()
	c := Census{Rows: make([]CensusRow, 0, len(t.count)),
		TotalFiles: t.total, TotalBytes: t.totalBytes,
		TierBytes: map[string]int64{}, TierFiles: map[string]int{}}
	for ext, n := range t.count {
		e := lookupFormat(reg, ext)
		c.Rows = append(c.Rows, CensusRow{Ext: ext, Count: n, Bytes: t.bytes[ext],
			Tier: e.Tier, Rationale: e.Rationale, Readers: e.Readers, Migration: e.Migration})
		c.TierBytes[e.Tier] += t.bytes[ext]
		c.TierFiles[e.Tier] += n
		if e.Tier == TierOpen || e.Tier == TierDocumented {
			c.SafeBytes += t.bytes[ext]
		}
	}
	sort.Slice(c.Rows, func(i, j int) bool {
		if c.Rows[i].Bytes != c.Rows[j].Bytes {
			return c.Rows[i].Bytes > c.Rows[j].Bytes
		}
		return c.Rows[i].Ext < c.Rows[j].Ext
	})
	if c.TotalBytes > 0 {
		c.SafePct = float64(c.SafeBytes) / float64(c.TotalBytes) * 100
	}
	return c
}

// FormatCensus computes the census for a collection (0 = all archives).
func (a *App) FormatCensus(collectionID int) Census {
	c := a.censusFromTally(a.Store.ExtTally(collectionID))
	c.CollectionID = collectionID
	return c
}

// censusFromRefs builds a census from a set of catalog file refs — used to
// describe exactly what a physical volume holds, for its sidecar inventory.
func (a *App) censusFromRefs(refs []ChunkFileRef) Census {
	t := newExtTally()
	for _, r := range refs {
		t.add(r.RelPath, r.SizeBytes)
	}
	return a.censusFromTally(t)
}

// formatCensusMD renders the census as Markdown (summary table + the plain-text
// readers reference) for the Recovery Kit and volume inventories.
func formatCensusMD(c Census) string {
	var b strings.Builder
	b.WriteString("# Format sustainability\n\n")
	b.WriteString(fmt.Sprintf("**%.0f%% of these bytes are in OPEN or DOCUMENTED formats** — readable with independent, open-source tools. %d file(s), %s.\n\n",
		c.SafePct, c.TotalFiles, humanBytes(c.TotalBytes)))
	b.WriteString("Tiers rate a *format's* long-term readability by two things only: is it publicly documented, and are there multiple independent open-source readers? Vendor health is NOT a factor. Tiers are advisory — nothing here suggests deleting an original.\n\n")
	b.WriteString("| Type | Files | Size | Tier |\n|---|---:|---:|---|\n")
	for _, r := range c.Rows {
		b.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s |\n", r.Ext, r.Count, humanBytes(r.Bytes), r.Tier))
	}
	b.WriteString("\n```\n")
	b.WriteString(readersReference(c))
	b.WriteString("```\n")
	return b.String()
}

// readersReference renders a plain-text "how to read these formats" reference for
// the tiers present in a census — carried into the Recovery Kit and volume
// inventories so the media are self-describing.
func readersReference(c Census) string {
	var b strings.Builder
	b.WriteString("How to read these formats\n")
	b.WriteString("=========================\n\n")
	b.WriteString("Tiers rate a format's long-term readability by whether it is publicly\n")
	b.WriteString("documented and has multiple independent open-source readers — NOT by vendor\n")
	b.WriteString("health. Tiers are advisory; nothing here suggests deleting originals.\n\n")
	for _, tier := range []string{TierOpen, TierDocumented, TierAtRisk, TierUnknown} {
		var rows []CensusRow
		for _, r := range c.Rows {
			if r.Tier == tier {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			continue
		}
		b.WriteString(tier + "\n" + strings.Repeat("-", len(tier)) + "\n")
		for _, r := range rows {
			b.WriteString("  " + r.Ext + " — " + r.Rationale + "\n")
			if len(r.Readers) > 0 {
				b.WriteString("    readers: " + strings.Join(r.Readers, ", ") + "\n")
			}
			if r.Migration != "" {
				b.WriteString("    migration: " + r.Migration + "\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
