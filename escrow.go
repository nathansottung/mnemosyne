package main

// escrow.go — "the archive preserves its own reader."
//
// An Escrow Bundle is a self-contained folder that travels in the Recovery Kit
// (always) and, optionally, onto each finalized volume (config escrow_on_media).
// It is belt-and-suspenders, NOT a dependency: the three-tool restore
// (par2 → gpg → tar) never needs Mnemosyne, and the compiled static binaries run
// on a compatible OS without a compiler. The bundle carries, in descending order
// of practical usefulness:
//
//   (a) Mnemosyne's own static binaries (the practical escrow) + source tarball
//       (the audit trail and recompile path) + SHA-256SUMS.
//   (b) source tarballs of the restore toolchain — par2cmdline + GnuPG — whose
//       GPL licenses permit redistribution WITH source. LICENSES.md states each
//       component's terms. IBM LTFS and anything non-redistributable are never
//       included (they are simply absent from escrow_manifest.json).
//   (c) optionally, source tarballs of the format readers the archive's census
//       actually references (e.g. LibRaw for RAW photos).
//
// Two acquisition paths feed the bundle (nothing here ever reaches the network
// while WRITING a bundle — it only assembles what is already on disk):
//   - Mnemosyne's source tarball is embedded by the release build (or, in a
//     source checkout, regenerated from the working tree via `git archive`).
//   - Binaries + toolchain/reader source are fetched once into an on-disk cache
//     (FetchEscrowCache / the /api/escrow/fetch endpoint) and reused thereafter.
//
// Budget honestly: a plan reports the byte size BEFORE anything is written, so a
// space-constrained medium (a nearly-full DVD-R) is skipped gracefully with a
// note rather than failing the seal.

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// escrowBundleDir is the single folder the bundle is written into, under the
// Recovery Kit or a volume's seal sidecar.
const escrowBundleDir = "escrow-bundle"

// escrowRepo is the GitHub "owner/repo" the release artifacts are fetched from —
// it MUST match the repository that .github/workflows/release.yml publishes to, or
// the download-and-cache path 404s. This is the release repo, not the Go module
// path (which is an internal import identifier).
const escrowRepo = "nathansottung/mnemosyne"

// escrowBinTargets are the platforms whose static binaries make up the practical
// escrow. Kept to the three OS families named in the spec (amd64 + Apple-silicon
// arm64) so the bundle stays in the ~15–120 MB budget.
var escrowBinTargets = []struct{ GOOS, GOARCH string }{
	{"windows", "amd64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
	{"darwin", "amd64"},
}

//go:embed escrow/mnemosyne-src.tar.gz
var embeddedMnemosyneSource []byte

//go:embed escrow_manifest.json
var escrowManifestJSON []byte

// embeddedSourceThreshold separates a real release-embedded source tarball from
// the tiny dev placeholder committed to the repo. A genuine `git archive` of the
// tree is far larger than a few KB.
const embeddedSourceThreshold = 4096

// escrow bundle modes (config escrow_on_media). Recovery Kit always writes the
// full bundle; per-volume writes honour this policy.
const (
	EscrowFull         = "full"          // binaries + all source tarballs (+ readers if enabled)
	EscrowBinariesOnly = "binaries-only" // binaries + SHA-256SUMS only — the practical escrow, smallest
	EscrowOff          = "off"           // no bundle on media
)

// normEscrowMode normalises the configured policy to full / binaries-only / off,
// defaulting to binaries-only (the space-conscious default).
func normEscrowMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case EscrowFull, "all", "source":
		return EscrowFull
	case EscrowOff, "none", "no":
		return EscrowOff
	default:
		return EscrowBinariesOnly
	}
}

// escrowCacheDir resolves where fetched binaries + source tarballs live: the
// configured override, else <DataDir>/escrow-cache.
func (a *App) escrowCacheDir() string {
	if d := strings.TrimSpace(a.LoadConfig().EscrowCacheDir); d != "" {
		return d
	}
	return filepath.Join(a.DataDir, "escrow-cache")
}

// ---- registry ------------------------------------------------------------

// escrowArtifact is one redistributable source package the registry knows how to
// obtain and how to license.
type escrowArtifact struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	File        string   `json:"file"`
	URL         string   `json:"url"`
	SHA256      string   `json:"sha256"`
	License     string   `json:"license"`
	LicenseNote string   `json:"license_note"`
	Exts        []string `json:"exts,omitempty"` // readers only: census extensions that trigger inclusion
}

type escrowManifest struct {
	Toolchain []escrowArtifact `json:"toolchain"`
	Readers   []escrowArtifact `json:"readers"`
}

func loadEscrowManifest() escrowManifest {
	var m escrowManifest
	_ = json.Unmarshal(escrowManifestJSON, &m)
	return m
}

// ---- components ----------------------------------------------------------

// escrowComponent is a resolved item ready to place in a bundle. Exactly one of
// data / cachePath supplies its bytes; Present is false when the source could not
// be located (it is then reported as missing, never fatal).
type escrowComponent struct {
	Kind        string `json:"kind"`    // mnemosyne-source | mnemosyne-binary | mnemosyne-sums | toolchain-source | reader-source
	Name        string `json:"name"`    // human label
	File        string `json:"file"`    // destination filename
	Subdir      string `json:"subdir"`  // bundle subfolder
	Bytes       int64  `json:"bytes"`   // size when present
	Present     bool   `json:"present"` // available to write
	Source      string `json:"source"`  // provenance description
	License     string `json:"license,omitempty"`
	LicenseNote string `json:"license_note,omitempty"`
	URL         string `json:"url,omitempty"` // where to fetch it if missing

	data      []byte // in-memory bytes (mnemosyne source); nil when cachePath set
	cachePath string // on-disk source; "" when data set
}

// mnemosyneSourceComponent resolves the app's own source tarball: a real
// release-embedded tarball if present, else a fresh `git archive` of the working
// tree, else the dev placeholder (still emitted, clearly labelled).
func mnemosyneSourceComponent(version string) escrowComponent {
	file := "mnemosyne-src-" + fsSafe(version) + ".tar.gz"
	c := escrowComponent{Kind: "mnemosyne-source", Name: "Mnemosyne source", File: file,
		Subdir: "mnemosyne", License: "MIT", LicenseNote: "Mnemosyne itself — see LICENSE inside the tarball.", Present: true}
	if len(embeddedMnemosyneSource) >= embeddedSourceThreshold {
		c.data, c.Bytes, c.Source = embeddedMnemosyneSource, int64(len(embeddedMnemosyneSource)), "embedded at release build"
		return c
	}
	if b, err := gitArchiveSource(version); err == nil && len(b) > 0 {
		c.data, c.Bytes, c.Source = b, int64(len(b)), "generated from working tree via `git archive`"
		return c
	}
	// Last resort: the placeholder. Still "present" so full bundles are never
	// silently missing the recompile path — the file itself explains the gap.
	c.data, c.Bytes, c.Source = embeddedMnemosyneSource, int64(len(embeddedMnemosyneSource)), "DEV PLACEHOLDER — no release embed and no git tree; recompile from the tagged release"
	return c
}

// gitArchiveSource produces a reproducible-ish .tar.gz of the tracked source at
// HEAD. Best-effort: any failure (no git, not a repo) returns an error and the
// caller falls back.
func gitArchiveSource(version string) ([]byte, error) {
	prefix := "mnemosyne-" + fsSafe(version) + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "archive", "--format=tar.gz", "--prefix="+prefix, "HEAD")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git archive: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// mnemosyneBinaryComponents finds cached release binaries (as shipped .zip
// artifacts) for each target platform, plus the release SHA-256SUMS if cached.
func mnemosyneBinaryComponents(cacheDir, version string) []escrowComponent {
	var comps []escrowComponent
	verDir := filepath.Join(cacheDir, fsSafe(version))
	for _, t := range escrowBinTargets {
		file := fmt.Sprintf("mnemosyne-%s-%s.zip", t.GOOS, t.GOARCH)
		name := fmt.Sprintf("Mnemosyne binary %s/%s", t.GOOS, t.GOARCH)
		c := escrowComponent{Kind: "mnemosyne-binary", Name: name, File: file, Subdir: "mnemosyne",
			License: "MIT", URL: fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", escrowRepo, version, file)}
		if p, sz, ok := cachedFile(verDir, file); ok {
			c.Present, c.cachePath, c.Bytes, c.Source = true, p, sz, "cached release artifact"
		}
		comps = append(comps, c)
	}
	// The release's own checksum file over all zips.
	sums := "SHA-256SUMS.txt"
	c := escrowComponent{Kind: "mnemosyne-sums", Name: "Release SHA-256SUMS", File: sums, Subdir: "mnemosyne",
		URL: fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", escrowRepo, version, sums)}
	if p, sz, ok := cachedFile(verDir, sums); ok {
		c.Present, c.cachePath, c.Bytes, c.Source = true, p, sz, "cached release artifact"
	}
	comps = append(comps, c)
	return comps
}

// toolchainComponents resolves par2cmdline + GnuPG source from the cache.
func toolchainComponents(cacheDir string, m escrowManifest) []escrowComponent {
	var comps []escrowComponent
	for _, a := range m.Toolchain {
		comps = append(comps, artifactComponent(cacheDir, a, "toolchain-source", "restore-toolchain"))
	}
	return comps
}

// readerComponents resolves reader source (LibRaw, …) for only those readers
// whose extensions actually appear in the archive's census — so the bundle
// carries a decoder for the RAW files it holds, not every format on earth.
func readerComponents(cacheDir string, m escrowManifest, census Census) []escrowComponent {
	present := map[string]bool{}
	for _, r := range census.Rows {
		present[normExt(r.Ext)] = true
	}
	var comps []escrowComponent
	for _, a := range m.Readers {
		hit := false
		for _, e := range a.Exts {
			if present[normExt(e)] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		comps = append(comps, artifactComponent(cacheDir, a, "reader-source", "format-readers"))
	}
	return comps
}

// artifactComponent turns a registry entry into a resolved component (cache hit
// or a "missing, fetch from URL" placeholder).
func artifactComponent(cacheDir string, a escrowArtifact, kind, subdir string) escrowComponent {
	c := escrowComponent{Kind: kind, Name: a.Name + " " + a.Version, File: a.File, Subdir: subdir,
		License: a.License, LicenseNote: a.LicenseNote, URL: a.URL}
	if p, sz, ok := cachedFile(cacheDir, a.File); ok {
		c.Present, c.cachePath, c.Bytes, c.Source = true, p, sz, "cached source tarball"
	}
	return c
}

// cachedFile returns (path, size, true) when name exists and is non-empty in dir.
func cachedFile(dir, name string) (string, int64, bool) {
	p := filepath.Join(dir, name)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
		return p, fi.Size(), true
	}
	return "", 0, false
}

// ---- plan ----------------------------------------------------------------

// EscrowPlan is the pre-write budget: exactly which components a given mode would
// write, the total size, and what is missing (with fetch URLs) — reported before
// a single byte lands so a small medium can be skipped honestly.
type EscrowPlan struct {
	Mode           string            `json:"mode"`
	IncludeReaders bool              `json:"include_readers"`
	Version        string            `json:"version"`
	Components     []escrowComponent `json:"components"`
	PresentBytes   int64             `json:"present_bytes"` // what will actually be written (present only)
	MissingCount   int               `json:"missing_count"` // components not yet cached
	MissingNames   []string          `json:"missing_names,omitempty"`
	Skipped        bool              `json:"skipped"` // true when mode == off
}

// planEscrow assembles the component list for a mode without touching the
// network. census drives reader selection; includeReaders gates part (c).
func (a *App) planEscrow(mode string, includeReaders bool, census Census) EscrowPlan {
	mode = normEscrowMode(mode)
	version := appVersion
	plan := EscrowPlan{Mode: mode, IncludeReaders: includeReaders, Version: version}
	if mode == EscrowOff {
		plan.Skipped = true
		return plan
	}
	cache := a.escrowCacheDir()
	m := loadEscrowManifest()

	// Binaries + release checksums are in every non-off bundle: the practical escrow.
	comps := mnemosyneBinaryComponents(cache, version)
	// Full adds the recompile/audit trail: source tarballs.
	if mode == EscrowFull {
		comps = append(comps, mnemosyneSourceComponent(version))
		comps = append(comps, toolchainComponents(cache, m)...)
		if includeReaders {
			comps = append(comps, readerComponents(cache, m, census)...)
		}
	}
	plan.Components = comps
	for _, c := range comps {
		if c.Present {
			plan.PresentBytes += c.Bytes
		} else {
			plan.MissingCount++
			plan.MissingNames = append(plan.MissingNames, c.Name)
		}
	}
	return plan
}

// estimatedBundleBytes adds a small fixed allowance for the generated docs
// (README, LICENSES, MANIFEST, SHA-256SUMS) on top of the payload.
func (p EscrowPlan) estimatedBundleBytes() int64 { return p.PresentBytes + 32*1024 }

// ---- write ---------------------------------------------------------------

// WriteEscrowBundle assembles the bundle under destDir/escrow-bundle from the
// plan's present components and writes the generated ESCROW_README, LICENSES.md,
// MANIFEST.json, and a SHA-256SUMS over every file. It never fails over a missing
// component — those are recorded in the returned summary and the MANIFEST so the
// gap is visible, not silent. Returns nil summary with skipped=true for mode off.
func (a *App) WriteEscrowBundle(destDir string, plan EscrowPlan, progress func(float64, string)) (map[string]any, error) {
	if plan.Skipped || plan.Mode == EscrowOff {
		return map[string]any{"skipped": true, "reason": "escrow_on_media=off"}, nil
	}
	if err := a.Store.AssertOutsideSources(destDir); err != nil {
		return nil, err
	}
	root := filepath.Join(destDir, escrowBundleDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if progress == nil {
		progress = func(float64, string) {}
	}

	var written []map[string]any
	sums := &strings.Builder{}
	var writtenBytes int64
	n := len(plan.Components)
	for i, c := range plan.Components {
		if !c.Present {
			continue
		}
		sub := filepath.Join(root, c.Subdir)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return nil, err
		}
		dst := filepath.Join(sub, c.File)
		sum, err := a.placeEscrowFile(c, dst)
		if err != nil {
			return nil, fmt.Errorf("writing %s: %w", c.File, err)
		}
		rel := filepath.ToSlash(filepath.Join(c.Subdir, c.File))
		fmt.Fprintf(sums, "%s  %s\n", sum, rel)
		writtenBytes += c.Bytes
		written = append(written, map[string]any{"file": rel, "bytes": c.Bytes, "kind": c.Kind, "source": c.Source})
		progress(0.1+0.7*float64(i+1)/float64(n+1), "escrow: "+c.File)
	}

	progress(0.85, "escrow: docs")
	docs := map[string][]byte{
		"ESCROW_README.md": []byte(escrowReadmeMD(plan)),
		"LICENSES.md":      []byte(escrowLicensesMD(plan)),
	}
	manifest, _ := json.MarshalIndent(map[string]any{
		"mnemosyne_escrow_bundle": 1, "generated_utc": time.Now().UTC().Format(time.RFC3339),
		"version": plan.Version, "mode": plan.Mode, "include_readers": plan.IncludeReaders,
		"written": written, "missing": plan.MissingNames, "present_bytes": writtenBytes,
	}, "", "  ")
	docs["MANIFEST.json"] = manifest
	// Keep doc filenames sorted for a stable SHA-256SUMS.
	names := make([]string, 0, len(docs))
	for name := range docs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), docs[name], 0o644); err != nil {
			return nil, err
		}
		h := sha256.Sum256(docs[name])
		fmt.Fprintf(sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	if err := os.WriteFile(filepath.Join(root, "SHA-256SUMS"), []byte(sums.String()), 0o644); err != nil {
		return nil, err
	}
	progress(1.0, "escrow: done")

	return map[string]any{
		"dir": root, "mode": plan.Mode, "bytes": writtenBytes,
		"components": len(written), "missing": plan.MissingNames, "missing_count": plan.MissingCount,
	}, nil
}

// placeEscrowFile copies a component's bytes (from memory or cache) to dst and
// returns its SHA-256 for the checksum file.
func (a *App) placeEscrowFile(c escrowComponent, dst string) (string, error) {
	h := sha256.New()
	if c.data != nil {
		if err := os.WriteFile(dst, c.data, 0o644); err != nil {
			return "", err
		}
		h.Write(c.data)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	in, err := os.Open(c.cachePath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---- fetch (the download-and-cache path) ---------------------------------

// FetchEscrowCache downloads whatever a full-mode plan is still missing —
// release binaries + checksums for the running version, plus toolchain and (when
// enabled) reader source tarballs — into the escrow cache, verifying sha256 when
// the registry pins one. It is the ONLY escrow path that touches the network, and
// is invoked explicitly (never while writing a bundle). Returns per-file results.
func (a *App) FetchEscrowCache(includeReaders bool, census Census, progress func(float64, string)) (map[string]any, error) {
	if progress == nil {
		progress = func(float64, string) {}
	}
	version := appVersion
	if !looksLikeReleaseTag(version) {
		return nil, fmt.Errorf("this build reports version %q, not a release tag (vMAJOR.MINOR.PATCH) — release binaries can only be fetched for a tagged build; the bundle still ships whatever is already cached", version)
	}
	cache := a.escrowCacheDir()
	verDir := filepath.Join(cache, fsSafe(version))
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		return nil, err
	}
	m := loadEscrowManifest()

	// Assemble the full download list: binaries+sums into verDir, source into cache.
	type dl struct {
		url, dest, sha256 string
	}
	var list []dl
	for _, c := range mnemosyneBinaryComponents(cache, version) {
		if !c.Present {
			list = append(list, dl{c.URL, filepath.Join(verDir, c.File), ""})
		}
	}
	for _, a := range m.Toolchain {
		if _, _, ok := cachedFile(cache, a.File); !ok {
			list = append(list, dl{a.URL, filepath.Join(cache, a.File), a.SHA256})
		}
	}
	if includeReaders {
		for _, c := range readerComponents(cache, m, census) {
			if c.Present {
				continue
			}
			// look up the pinned sha for this reader
			sha := ""
			for _, r := range m.Readers {
				if r.File == c.File {
					sha = r.SHA256
				}
			}
			list = append(list, dl{c.URL, c.dest(cache), sha})
		}
	}

	var results []map[string]any
	fetched, failed := 0, 0
	for i, d := range list {
		progress(float64(i)/float64(len(list)+1), "fetch: "+filepath.Base(d.dest))
		err := fetchEscrowArtifact(d.url, d.dest, d.sha256)
		r := map[string]any{"file": filepath.Base(d.dest), "url": d.url}
		if err != nil {
			r["error"], failed = err.Error(), failed+1
		} else {
			fi, _ := os.Stat(d.dest)
			if fi != nil {
				r["bytes"] = fi.Size()
			}
			fetched++
		}
		results = append(results, r)
	}
	progress(1.0, "fetch: done")
	a.Store.Log("escrow", fmt.Sprintf("cache fetch %s — %d fetched, %d failed", version, fetched, failed))
	return map[string]any{"version": version, "fetched": fetched, "failed": failed, "results": results, "cache_dir": cache}, nil
}

// dest resolves a reader component's cache path (readers live at the cache root).
func (c escrowComponent) dest(cacheDir string) string { return filepath.Join(cacheDir, c.File) }

// fetchEscrowArtifact downloads url to dest (atomically via a .part file),
// verifying the SHA-256 when want is non-empty.
func fetchEscrowArtifact(url, dest, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	got := hex.EncodeToString(h.Sum(nil))
	if want != "" && !strings.EqualFold(got, want) {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return os.Rename(tmp, dest)
}

// looksLikeReleaseTag reports whether v is a vMAJOR… tag we can build release URLs
// from (dev builds default to "0.9.0-dev" with no leading v).
func looksLikeReleaseTag(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "v") && len(v) > 1
}

// fsSafe makes a version string safe as a path segment.
func fsSafe(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "dev"
	}
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	return r.Replace(s)
}

// gunzipPeek is a tiny helper used by tests to confirm a tarball is a real gzip
// (kept here so the placeholder-vs-real distinction has a single definition).
func gunzipPeek(b []byte) bool {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return false
	}
	defer zr.Close()
	_, err = io.CopyN(io.Discard, zr, 1)
	return err == nil || err == io.EOF
}

// zipEntryCount is a small helper for tests/inspection of cached binary zips.
func zipEntryCount(path string) (int, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return len(r.File), nil
}
