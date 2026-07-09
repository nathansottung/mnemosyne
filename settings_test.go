package main

// settings_test.go — the rebuilt Settings surface. Backend guarantees the UI leans on:
// real machine memory (CGO-free), the file-listing browse used by the binary picker,
// the External-Tools catalog (required vs optional, each with a link + save key), that
// a browsed binary path actually pins a tool, the hash-acceleration toggle, and the
// new config fields' round-trip. Plus a light reachability scan of ui/index.html to
// assert every section and every setting input is present with a help line.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The machine's total memory must come back as a real, positive number (the
// Performance panel shows it and caps the buffer against it). Available is
// non-negative and never exceeds total.
func TestSystemMemory_RealNumbers(t *testing.T) {
	m := SystemMemory()
	if m.TotalBytes <= 0 {
		t.Fatalf("SystemMemory().TotalBytes = %d, want > 0 (real detection expected on %s)", m.TotalBytes, os.Getenv("GOOS"))
	}
	// A plausible floor: any machine running the tests has ≥ 256 MB.
	if m.TotalBytes < 256<<20 {
		t.Errorf("total memory implausibly small: %d bytes", m.TotalBytes)
	}
	if m.AvailableBytes < 0 || m.AvailableBytes > m.TotalBytes {
		t.Errorf("available %d out of range for total %d", m.AvailableBytes, m.TotalBytes)
	}
}

// BrowseWithFiles lists files (for the binary picker); plain Browse omits them.
func TestBrowseWithFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rclone.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := newSetupApp(t)

	withFiles, err := app.BrowseWithFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := withFiles["files"].([]map[string]any)
	if len(files) != 1 || files[0]["name"] != "rclone.exe" {
		t.Errorf("BrowseWithFiles files = %+v, want [rclone.exe]", files)
	}
	dirs, _ := withFiles["dirs"].([]map[string]any)
	if len(dirs) != 1 || dirs[0]["name"] != "sub" {
		t.Errorf("BrowseWithFiles dirs = %+v, want [sub]", dirs)
	}

	plain, err := app.Browse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain["files"]; ok {
		t.Error("plain Browse must not include files")
	}
}

// The tools catalog covers the whole required + optional set, and every row is
// renderable (has a plain description, a save key, and a download link).
func TestToolsCatalog(t *testing.T) {
	app := newSetupApp(t)
	tools := app.ToolsView()
	byName := map[string]ToolInfo{}
	for _, ti := range tools {
		byName[ti.Name] = ti
	}
	for _, req := range []string{"tar", "gpg", "par2"} {
		ti, ok := byName[req]
		if !ok || !ti.Required {
			t.Errorf("%q must be present and marked required", req)
		}
	}
	for _, opt := range []string{"smartctl", "dvdisaster", "stenc", "tape diagnostics", "ffprobe", "czkawka", "rclone", "xorriso"} {
		ti, ok := byName[opt]
		if !ok {
			t.Errorf("optional tool %q missing from catalog", opt)
			continue
		}
		if ti.Required {
			t.Errorf("%q must be optional, not required", opt)
		}
	}
	for _, ti := range tools {
		if strings.TrimSpace(ti.Adds) == "" {
			t.Errorf("tool %q has no 'what it adds' line", ti.Name)
		}
		if strings.TrimSpace(ti.SaveKey) == "" {
			t.Errorf("tool %q has no save key (browse-to-binary target)", ti.Name)
		}
		if !strings.HasPrefix(ti.Download, "http") {
			t.Errorf("tool %q has no download link, got %q", ti.Name, ti.Download)
		}
	}
	// The tape row pins into the flat tape_tool config; generic tools into the map.
	if byName["tape diagnostics"].SaveKey != "tape_tool" {
		t.Errorf("tape diagnostics save key = %q, want tape_tool", byName["tape diagnostics"].SaveKey)
	}
	if byName["rclone"].SaveKey != "tools:rclone" {
		t.Errorf("rclone save key = %q, want tools:rclone", byName["rclone"].SaveKey)
	}
}

// Browse-to-binary works: pinning a path via the Tools map makes that tool detected
// at exactly that path (the whole point of the manual-locate button).
func TestToolBrowseToBinary(t *testing.T) {
	app := newSetupApp(t)
	bin := filepath.Join(t.TempDir(), "rclone.exe")
	if err := os.WriteFile(bin, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// This is exactly what saveToolPath sends for save_key "tools:rclone".
	if _, err := app.SaveConfig(map[string]any{"tools": map[string]any{"rclone": bin}}); err != nil {
		t.Fatal(err)
	}
	var got ToolInfo
	for _, ti := range app.ToolsView() {
		if ti.Name == "rclone" {
			got = ti
		}
	}
	if !got.Detected || got.Path != bin {
		t.Errorf("after pinning, rclone = {detected:%v path:%q}, want detected at %q", got.Detected, got.Path, bin)
	}
	if got.Configured != bin {
		t.Errorf("configured path = %q, want %q", got.Configured, bin)
	}
}

// The hash-acceleration toggle actually changes what hashFileBoth computes.
func TestHashAccelToggle(t *testing.T) {
	f := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(f, []byte("some bytes to hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer setHashAccel(true) // restore process default for other tests

	setHashAccel(true)
	sha, b3, err := hashFileBoth(f)
	if err != nil || sha == "" || b3 == "" {
		t.Fatalf("accel on: sha=%q b3=%q err=%v — both hashes expected", sha, b3, err)
	}
	setHashAccel(false)
	sha2, b3off, err := hashFileBoth(f)
	if err != nil {
		t.Fatal(err)
	}
	if sha2 != sha {
		t.Errorf("SHA-256 must be identical regardless of acceleration: %q vs %q", sha, sha2)
	}
	if b3off != "" {
		t.Errorf("accel off: blake3 = %q, want empty", b3off)
	}
}

// New config fields default sanely and round-trip through save/load; a partial save
// doesn't disturb the others (the merge contract every Save button relies on).
func TestSettingsConfigFields(t *testing.T) {
	app := newSetupApp(t)
	def := app.LoadConfig()
	if !def.HashAccel {
		t.Error("HashAccel should default true")
	}
	if def.LabelSize == "" {
		t.Error("LabelSize should have a default")
	}
	if _, err := app.SaveConfig(map[string]any{
		"update_check": true, "label_size": "4in 2in", "default_profile": "single-copy",
		"hash_accel": false, "barcode_scheme": "ABC",
	}); err != nil {
		t.Fatal(err)
	}
	got := app.LoadConfig()
	if !got.UpdateCheck || got.LabelSize != "4in 2in" || got.DefaultProfile != "single-copy" || got.HashAccel {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// Saving hash_accel=false must have applied it process-wide.
	if hashAccelOn.Load() {
		t.Error("saving hash_accel=false did not apply the runtime toggle")
	}
	setHashAccel(true)
	// A partial save preserves unrelated fields.
	if _, err := app.SaveConfig(map[string]any{"barcode_scheme": "ZZZ"}); err != nil {
		t.Fatal(err)
	}
	if g := app.LoadConfig(); g.LabelSize != "4in 2in" || g.DefaultProfile != "single-copy" {
		t.Errorf("partial save clobbered unrelated fields: %+v", g)
	}
}

func TestLabelSizeParts(t *testing.T) {
	for in, want := range map[string][2]string{
		"4in 2in":       {"4in", "2in"},
		"62mm 29mm":     {"62mm", "29mm"},
		"":              {"2.25in", "1.25in"},
		"garbage":       {"2.25in", "1.25in"},
		"one two three": {"2.25in", "1.25in"},
	} {
		w, h := labelSizeParts(in)
		if w != want[0] || h != want[1] {
			t.Errorf("labelSizeParts(%q) = %q,%q want %q,%q", in, w, h, want[0], want[1])
		}
	}
}

// Reachability: the rebuilt Settings must render all eight sections in order, and
// every setting input must exist with a help line the save reads back.
func TestSettingsUIReachable(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("ui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)

	// Every section title must render (the Integrity panel's title lives in a helper
	// function, so title source-order ≠ render order — presence is what matters here).
	for _, title := range []string{
		">General<", "Storage &amp; staging", "Integrity — how hard each copy is proven",
		"Protection defaults", ">Performance<", ">External tools<", "Media &amp; labels", ">Advanced<",
	} {
		if !strings.Contains(html, title) {
			t.Errorf("Settings section missing: %q", title)
		}
	}
	// Render ORDER is fixed by the numbered build steps inside vSettings — assert they
	// appear in sequence (this is the order the sections are appended to the view).
	steps := []string{
		"// ---- 1. General ----", "// ---- 2. Storage & Staging ----", "// ---- 3. Integrity (unified) ----",
		"// ---- 4. Protection defaults ----", "// ---- 5. Performance ----", "// ---- 6. External Tools ----",
		"// ---- 7. Media & Labels ----", "// ---- 8. Advanced (disclosure in Guided/Standard) ----",
	}
	last := -1
	for _, step := range steps {
		i := strings.Index(html, step)
		if i < 0 {
			t.Errorf("Settings build step missing: %q", step)
			continue
		}
		if i < last {
			t.Errorf("Settings section out of order: %q appears before the previous one", step)
		}
		last = i
	}

	// Every setting input id must be present AND read by saveCfg / its own handler.
	for _, id := range []string{
		"st", "ks", "defprof", "rc", "di", "bg", "bm", "tm", "ha", "bs", "lsz",
		"atok", "upd", "pm", "pe", "vr", "td", "eom", "eir", "fvd", "bpc", "sbf",
		"bc", "bv", "becc", "beccd", "becccarry", "oecc",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("settings input id=%q not rendered", id)
		}
		if !strings.Contains(html, "'"+id+"'") {
			t.Errorf("settings input id=%q is rendered but never read by saveCfg", id)
		}
	}

	// Live-data anchors the panels fill in.
	for _, anchor := range []string{`id="toolsbox"`, `id="meminfo"`, `id="datadir"`, `id="stginfo"`, `id="iv_bv"`, `id="iv_hdr_badge"`, `id="ivgrid"`} {
		if !strings.Contains(html, anchor) {
			t.Errorf("expected settings anchor %q", anchor)
		}
	}

	// "Every row has a help line" — the Settings view is dense with .help lines. A
	// generous floor catches an accidental wholesale drop.
	if n := strings.Count(html, `class="help"`); n < 40 {
		t.Errorf("only %d help lines in the whole UI — the rebuilt Settings should add many", n)
	}
}
