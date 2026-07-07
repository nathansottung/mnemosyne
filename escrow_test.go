package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func escrowApp(t *testing.T) *App {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return &App{DataDir: filepath.Dir(st.path), Store: st}
}

// seedEscrowCache writes non-empty placeholder files into the default escrow
// cache so binaries/toolchain/reader components resolve as "present". The bundle
// writer only copies bytes, so the exact content is irrelevant here.
func seedEscrowCache(t *testing.T, a *App, opts struct{ binaries, toolchain, readers bool }) {
	t.Helper()
	cache := a.escrowCacheDir()
	verDir := filepath.Join(cache, fsSafe(appVersion))
	write := func(dir, name string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake-"+name+"-payload"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if opts.binaries {
		for _, tg := range escrowBinTargets {
			write(verDir, "mnemosyne-"+tg.GOOS+"-"+tg.GOARCH+".zip")
		}
		write(verDir, "SHA-256SUMS.txt")
	}
	m := loadEscrowManifest()
	if opts.toolchain {
		for _, art := range m.Toolchain {
			write(cache, art.File)
		}
	}
	if opts.readers {
		for _, art := range m.Readers {
			write(cache, art.File)
		}
	}
}

func TestNormEscrowMode(t *testing.T) {
	cases := map[string]string{
		"": EscrowBinariesOnly, "  ": EscrowBinariesOnly, "garbage": EscrowBinariesOnly,
		"full": EscrowFull, "FULL": EscrowFull, "source": EscrowFull,
		"binaries-only": EscrowBinariesOnly, "off": EscrowOff, "none": EscrowOff,
	}
	for in, want := range cases {
		if got := normEscrowMode(in); got != want {
			t.Errorf("normEscrowMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManifestExcludesLTFS(t *testing.T) {
	// The registry must never carry non-redistributable software.
	low := strings.ToLower(string(escrowManifestJSON))
	for _, banned := range []string{"ltfs", "spectrum archive", "storeopen"} {
		// "ltfs" may appear only inside the explanatory _comment as a prohibition,
		// so assert it never appears as a component name/url.
		if strings.Contains(low, `"name": "`+banned) || strings.Contains(low, banned+".tar") {
			t.Errorf("manifest must not include %q as a component", banned)
		}
	}
	m := loadEscrowManifest()
	for _, a := range append(append([]escrowArtifact{}, m.Toolchain...), m.Readers...) {
		if strings.Contains(strings.ToLower(a.Name), "ltfs") {
			t.Errorf("manifest component %q looks like LTFS — forbidden", a.Name)
		}
		if a.License == "" || a.URL == "" || a.File == "" {
			t.Errorf("manifest component %q missing license/url/file", a.Name)
		}
	}
}

func TestPlanBinariesOnlyVsFull(t *testing.T) {
	a := escrowApp(t)
	seedEscrowCache(t, a, struct{ binaries, toolchain, readers bool }{binaries: true, toolchain: true})
	census := Census{} // no reader-triggering formats

	bin := a.planEscrow(EscrowBinariesOnly, false, census)
	if bin.MissingCount != 0 {
		t.Fatalf("binaries-only should be complete with a seeded cache, missing: %v", bin.MissingNames)
	}
	for _, c := range bin.Components {
		if c.Kind == "toolchain-source" || c.Kind == "mnemosyne-source" {
			t.Errorf("binaries-only must not include source component %q", c.Name)
		}
	}
	if bin.PresentBytes == 0 {
		t.Fatal("binaries-only should carry the binary payloads")
	}

	full := a.planEscrow(EscrowFull, false, census)
	if !hasKind(full.Components, "mnemosyne-source") {
		t.Error("full must include the Mnemosyne source tarball")
	}
	if !hasKind(full.Components, "toolchain-source") {
		t.Error("full must include restore-toolchain source")
	}
	if full.PresentBytes <= bin.PresentBytes {
		t.Error("full bundle should be larger than binaries-only")
	}
}

func TestPlanReaderSelectionAndGating(t *testing.T) {
	a := escrowApp(t)
	seedEscrowCache(t, a, struct{ binaries, toolchain, readers bool }{binaries: true, toolchain: true, readers: true})

	rawCensus := Census{Rows: []CensusRow{{Ext: ".nef"}, {Ext: ".jpg"}}}
	// readers off → no reader components even though a .nef is present.
	off := a.planEscrow(EscrowFull, false, rawCensus)
	if hasKind(off.Components, "reader-source") {
		t.Error("reader source must be gated by escrow_include_readers")
	}
	// readers on + a RAW format present → LibRaw/dcraw pulled in.
	on := a.planEscrow(EscrowFull, true, rawCensus)
	if !hasKind(on.Components, "reader-source") {
		t.Error("a .nef census with readers enabled should include a reader source")
	}
	// readers on but NO matching format → still no reader component.
	plain := a.planEscrow(EscrowFull, true, Census{Rows: []CensusRow{{Ext: ".txt"}}})
	if hasKind(plain.Components, "reader-source") {
		t.Error("no RAW/JP2 in census → no reader source, even with readers enabled")
	}
}

func TestWriteBundleAssemblesAndVerifies(t *testing.T) {
	a := escrowApp(t)
	seedEscrowCache(t, a, struct{ binaries, toolchain, readers bool }{binaries: true, toolchain: true})
	dest := t.TempDir()
	plan := a.planEscrow(EscrowFull, false, Census{})
	sum, err := a.WriteEscrowBundle(dest, plan, nil)
	if err != nil {
		t.Fatalf("WriteEscrowBundle: %v", err)
	}
	root := filepath.Join(dest, escrowBundleDir)

	// Core docs exist.
	for _, f := range []string{"ESCROW_README.md", "LICENSES.md", "MANIFEST.json", "SHA-256SUMS"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	// Binaries + source + toolchain landed in their subfolders.
	mustExist(t, filepath.Join(root, "mnemosyne", "mnemosyne-linux-amd64.zip"))
	mustExist(t, filepath.Join(root, "restore-toolchain", "par2cmdline-0.8.1.tar.gz"))
	if _, err := os.Stat(filepath.Join(root, "mnemosyne")); err != nil {
		t.Error("mnemosyne/ subdir missing")
	}

	// The philosophy note states the belt-and-suspenders doctrine and three tools.
	readme := readFile(t, filepath.Join(root, "ESCROW_README.md"))
	for _, want := range []string{"belt-and-suspenders", "par2", "gpg", "tar", "not a dependency"} {
		if !strings.Contains(readme, want) {
			t.Errorf("ESCROW_README should mention %q", want)
		}
	}
	// LICENSES states redistribution basis and the LTFS exclusion.
	lic := readFile(t, filepath.Join(root, "LICENSES.md"))
	if !strings.Contains(lic, "GPL") || !strings.Contains(strings.ToUpper(lic), "LTFS") {
		t.Error("LICENSES.md should state GPL terms and the LTFS exclusion")
	}

	// Every line in SHA-256SUMS must match the file on disk.
	verifySums(t, root)

	if b, _ := sum["bytes"].(int64); b <= 0 {
		t.Error("summary should report written bytes")
	}
}

func TestWriteBundleOffIsSkipped(t *testing.T) {
	a := escrowApp(t)
	dest := t.TempDir()
	plan := a.planEscrow(EscrowOff, false, Census{})
	sum, err := a.WriteEscrowBundle(dest, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if skipped, _ := sum["skipped"].(bool); !skipped {
		t.Error("off mode should report skipped")
	}
	if _, err := os.Stat(filepath.Join(dest, escrowBundleDir)); !os.IsNotExist(err) {
		t.Error("off mode must not create an escrow-bundle folder")
	}
}

func TestSidecarEscrowBudgetsHonestly(t *testing.T) {
	a := escrowApp(t)
	seedEscrowCache(t, a, struct{ binaries, toolchain, readers bool }{binaries: true})
	cfg := a.LoadConfig() // default binaries-only
	plan := a.planEscrow(EscrowBinariesOnly, false, Census{})
	est := plan.estimatedBundleBytes()

	// Not enough free space → skipped gracefully, nothing written, honest note.
	tight := t.TempDir()
	note := a.writeSidecarEscrow(tight, Census{}, est/2, cfg)
	if !strings.Contains(strings.ToLower(note), "skip") {
		t.Errorf("tight medium should be skipped, note: %q", note)
	}
	if _, err := os.Stat(filepath.Join(tight, escrowBundleDir)); !os.IsNotExist(err) {
		t.Error("skipped bundle must not write any files")
	}

	// Plenty of room → written, note says so, files exist.
	roomy := t.TempDir()
	note = a.writeSidecarEscrow(roomy, Census{}, est+10*1000*1000*1000, cfg)
	if !strings.Contains(note, "Wrote") {
		t.Errorf("roomy medium should write the bundle, note: %q", note)
	}
	mustExist(t, filepath.Join(roomy, escrowBundleDir, "ESCROW_README.md"))

	// Unknown free space (0) → skipped, not risked.
	unk := t.TempDir()
	note = a.writeSidecarEscrow(unk, Census{}, 0, cfg)
	if !strings.Contains(strings.ToLower(note), "skip") {
		t.Errorf("unknown free space should be skipped, note: %q", note)
	}

	// Policy off → clear note, no write.
	offCfg := cfg
	offCfg.EscrowOnMedia = EscrowOff
	note = a.writeSidecarEscrow(t.TempDir(), Census{}, est*100, offCfg)
	if !strings.Contains(strings.ToLower(note), "off") {
		t.Errorf("off policy note should mention off, got %q", note)
	}
}

func TestRecoveryKitCarriesEscrow(t *testing.T) {
	a := escrowApp(t)
	seedEscrowCache(t, a, struct{ binaries, toolchain, readers bool }{binaries: true, toolchain: true})
	out := t.TempDir()
	res, err := a.BuildRecoveryKit(out, func(float64, string) {})
	if err != nil {
		t.Fatalf("BuildRecoveryKit: %v", err)
	}
	kit := res["output_dir"].(string)
	mustExist(t, filepath.Join(kit, escrowBundleDir, "ESCROW_README.md"))
	mustExist(t, filepath.Join(kit, escrowBundleDir, "mnemosyne", "mnemosyne-windows-amd64.zip"))
	if res["escrow"] == nil {
		t.Error("kit summary should include an escrow entry")
	}
}

// ---- helpers ----

func hasKind(comps []escrowComponent, kind string) bool {
	for _, c := range comps {
		if c.Kind == kind && c.Present {
			return true
		}
	}
	return false
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func verifySums(t *testing.T, root string) {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "SHA-256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("bad SHA-256SUMS line: %q", line)
		}
		want, rel := parts[0], parts[1]
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("checksum lists %s but it is unreadable: %v", rel, err)
			continue
		}
		h := sha256.Sum256(b)
		if got := hex.EncodeToString(h[:]); got != want {
			t.Errorf("checksum mismatch for %s: got %s want %s", rel, got, want)
		}
		lines++
	}
	if lines == 0 {
		t.Error("SHA-256SUMS is empty")
	}
}
