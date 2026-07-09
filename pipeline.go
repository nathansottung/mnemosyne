package main

// pipeline.go — configuration, keystores, scanning, planning, building.
// Restore doctrine: par2 -> gpg -> tar, always runnable by hand.

import (
	"archive/tar"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MinKeystores = 2

var MediaPresets = map[string]int64{
	"LTO8": 11_000_000_000_000, "LTO9": 16_500_000_000_000,
	"BD-R25": 23_000_000_000, "BD-R50": 46_000_000_000, "BD-R100": 92_000_000_000,
	"DVD-R": 4_300_000_000, "DVD-DL": 8_100_000_000,
	"HDD": 0, "CUSTOM": 0,
}

// ---- config ------------------------------------------------------------

type Config struct {
	StagingDir            string            `json:"staging_dir"`
	KeystorePaths         []string          `json:"keystore_paths"`
	PrivateMedia          bool              `json:"private_media"` // encrypt the manifest written to media (hide filenames)
	DeleteTarAfterEncrypt bool              `json:"delete_tar_after_encrypt"`
	Par2Redundancy        int               `json:"par2_redundancy"`
	Par2ExtraArgs         string            `json:"par2_extra_args"`
	BufferGB              float64           `json:"buffer_gb"`
	BlockMB               int               `json:"block_mb"`
	ThrottleMbps          float64           `json:"throttle_mbps"` // 0 = unthrottled; caps writer-side MB/s (thermal control)
	BurnCommand           string            `json:"burn_command"`
	BurnVerifyMount       string            `json:"burn_verify_mount"`
	OpticalEcc            bool              `json:"optical_ecc"`     // note dvdisaster ECC as an intended extra layer on optical packages (docs + RESTORE.txt); par2 works regardless
	BurnEcc               string            `json:"burn_ecc"`        // dvdisaster disc-level ECC generated AFTER a burn verifies: "off" (default) | "rs02" | "rs03". Complement to par2; restore never requires it.
	BurnEccDevice         string            `json:"burn_ecc_device"` // optical device dvdisaster reads to compute the .ecc (e.g. /dev/sr0, H:); blank = derived from the burn command, else the layer is skipped
	BurnEccCarry          bool              `json:"burn_ecc_carry"`  // true = copy each disc's .ecc into the NEXT pending disc's staged folder (it rides on the next disc); false = keep it in staging
	VerifyDueMonths       int               `json:"verify_due_months"`
	RequiredCopies        int               `json:"required_copies"`                // redundancy goal; fewer verified copies = under-protected
	FinalizeVerifyDays    int               `json:"finalize_verify_days"`           // finalize: every copy on the volume must have verified within this many days
	BufferPct             float64           `json:"buffer_pct"`                     // finalize: min % of the drive left free to seal — full drives die young
	SmartBlockFinalize    bool              `json:"smart_block_finalize"`           // finalize: block when SMART data exists and is failing/advisory
	BuildVerify           string            `json:"build_verify"`                   // integrity tier: "full" (contents + round-trip), "contents", or "none" (unproven, amber)
	RoutineVerifyLevel    string            `json:"routine_verify_level"`           // default routine re-verify level: "B" (full) or "C" (sample)
	BarcodeScheme         string            `json:"barcode_scheme"`                 // prefix for auto-assigned volume barcodes, e.g. "NSP" -> NSP-0001
	TapeTool              string            `json:"tape_tool"`                      // path to a tape-diagnostics CLI (itdt/tapeinfo/sg_logs/hp_ltt); blank = auto-probe PATH
	TapeDevice            string            `json:"tape_device"`                    // tape drive device path, e.g. \\.\Tape0, /dev/nst0, /dev/sg1; blank = per-OS default
	AuthToken             string            `json:"auth_token"`                     // bearer token required on every /api call when the server binds a non-localhost address (containers); env MNEMO_AUTH_TOKEN overrides
	DriftInformational    []string          `json:"drift_informational_extensions"` // muted in reconcile, excluded from alarm totals
	VersionsRetained      int               `json:"versions_retained"`              // per-file retained content-version cap; 0 = unlimited (default). Capping forgets only the catalog pointer to old bytes, never the media.
	EscrowOnMedia         string            `json:"escrow_on_media"`                // escrow bundle written onto each finalized volume: "full" | "binaries-only" | "off" (default binaries-only, for space)
	EscrowIncludeReaders  bool              `json:"escrow_include_readers"`         // include source tarballs of the format readers the census references (e.g. LibRaw)
	EscrowCacheDir        string            `json:"escrow_cache_dir"`               // where fetched release binaries + toolchain source are cached; blank = <data>/escrow-cache
	Tools                 map[string]string `json:"tools"`
}

func defaultConfig() Config {
	// Defaults are the ARCHIVAL preset: full build-verify, 10% par2, routine level
	// B, 12-month verify window (read-back is always on).
	return Config{DeleteTarAfterEncrypt: true, Par2Redundancy: 10, Par2ExtraArgs: "-t0", BufferGB: 8, BlockMB: 64,
		VerifyDueMonths: 12, RequiredCopies: 2, FinalizeVerifyDays: 30, BufferPct: 5, SmartBlockFinalize: true,
		BuildVerify: "full", RoutineVerifyLevel: "B", BarcodeScheme: "NSP", DriftInformational: []string{".xmp"},
		EscrowOnMedia: EscrowBinariesOnly, Tools: map[string]string{}}
}

// buildVerifyMode normalises the configured build-verification tier to one of
// full / contents / none (archival correctness is the default for blank/legacy).
func (cfg Config) buildVerifyMode() string { return normBuildVerify(cfg.BuildVerify) }

const fastBuildWarning = "FAST build: stage-vs-source and decrypt round-trip verification were SKIPPED — this package's contents and decryptability are UNPROVEN."

type App struct {
	DataDir string
	Store   *Store
	// Preflight cache: the tool-version checks shell out to tar/gpg/par2, and a
	// cold tool (notably gpg spawning its agent on Windows) can take many seconds.
	// Both the Settings view and the periodic status lamp call Preflight, so a
	// slow probe used to hang the UI; a short single-flight cache + per-tool
	// timeout keep it snappy. See Preflight / toolVersionLine.
	pfMu  sync.Mutex
	pfAt  time.Time
	pfVal map[string]any
}

func (a *App) configPath() string { return filepath.Join(a.DataDir, "config.json") }

func (a *App) LoadConfig() Config {
	cfg := defaultConfig()
	if b, err := os.ReadFile(a.configPath()); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.Tools == nil {
		cfg.Tools = map[string]string{}
	}
	return cfg
}

func (a *App) SaveConfig(in map[string]any) (Config, error) {
	cfg := a.LoadConfig()
	b, _ := json.Marshal(in)
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	// Source-safety invariant: staging is written into during builds and keystores
	// are rewritten on key generation — neither may live inside source data.
	if err := a.Store.AssertOutsideSources(cfg.StagingDir); err != nil {
		return cfg, err
	}
	for _, ks := range cfg.KeystorePaths {
		if err := a.Store.AssertOutsideSources(ks); err != nil {
			return cfg, err
		}
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	return cfg, os.WriteFile(a.configPath(), out, 0o644)
}

// ---- tools ---------------------------------------------------------------

func (a *App) tool(name string) (string, error) {
	cfg := a.LoadConfig()
	if p := cfg.Tools[name]; p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("required tool %q not found — see Preflight for install hints", name)
	}
	return p, nil
}

// toolVersionLine runs "<path> --version" but STOPS WAITING after a short
// timeout, returning the first output line (or "" if it is slow/errored). A cold
// tool can take many seconds the first time — notably gpg spawning its agent and
// dirmngr on Windows, whose children inherit the output pipe so even killing the
// parent (CommandContext) does not return promptly. So we don't rely on killing
// it: we read the result in a goroutine and give up waiting on a timer, letting
// the harmless probe finish in the background. The "ok"/"found" status comes from
// LookPath, so a missing version string is cosmetic and fills in once warm.
func toolVersionLine(path string) string {
	ch := make(chan string, 1) // buffered: the goroutine never blocks even if we stop waiting
	go func() {
		v, err := exec.Command(path, "--version").CombinedOutput()
		if err != nil {
			ch <- ""
			return
		}
		ch <- strings.SplitN(strings.TrimSpace(string(v)), "\n", 2)[0]
	}()
	select {
	case line := <-ch:
		return line
	case <-time.After(3 * time.Second):
		return ""
	}
}

// detectLTFSMountsBounded caps LTFS detection so a slow volume probe can never
// hang Preflight; LTFS presence is informational and never affects readiness.
func detectLTFSMountsBounded(d time.Duration) []string {
	ch := make(chan []string, 1)
	go func() { ch <- detectLTFSMounts() }()
	select {
	case m := <-ch:
		return m
	case <-time.After(d):
		return nil
	}
}

// Preflight reports tool/keystore/media readiness, cached briefly and computed
// single-flight so the Settings view and the 20s status lamp never both stall on
// a slow tool probe.
func (a *App) Preflight() map[string]any {
	a.pfMu.Lock()
	defer a.pfMu.Unlock()
	if a.pfVal != nil && time.Since(a.pfAt) < 8*time.Second {
		return a.pfVal
	}
	a.pfVal, a.pfAt = a.computePreflight(), time.Now()
	return a.pfVal
}

func (a *App) computePreflight() map[string]any {
	out := map[string]any{}
	hints := []string{}
	allOK := true
	for name, hint := range map[string]string{
		"tar":  "Windows 10+ ships tar.exe (bsdtar) natively; Linux/macOS preinstalled.",
		"gpg":  "Windows: install Gpg4win (gpg4win.org). Linux: apt install gnupg. macOS: brew install gnupg.",
		"par2": "Windows: choco install par2cmdline. Linux: apt install par2. macOS: brew install par2.",
	} {
		p, err := a.tool(name)
		item := map[string]any{"ok": err == nil, "path": p}
		if err == nil {
			if line := toolVersionLine(p); line != "" {
				item["version"] = line
			}
		} else {
			hints = append(hints, name+": "+hint)
			allOK = false
		}
		out[name] = item
	}
	ks := a.KeystoreStatus()
	out["keystores"] = ks
	// LTFS tape detection is purely informational — it NEVER affects "ok". Absence
	// just means "no tape mounted"; HDD and optical need no LTFS. Bounded by a
	// timeout so a slow volume probe can never hang Preflight.
	ltfs := detectLTFSMountsBounded(3 * time.Second)
	out["ltfs"] = map[string]any{"mounted": len(ltfs) > 0, "mounts": ltfs}
	// smartctl (drive-mortality signals) is OPTIONAL — informational only, never
	// affects "ok". Present = the Media health card lights up on volumes; absent =
	// the feature hides behind an install hint. It complements hash verification.
	sp, serr := a.tool("smartctl")
	smart := map[string]any{"ok": serr == nil, "path": sp}
	if serr != nil {
		smart["hint"] = smartInstallHint
	}
	out["smartctl"] = smart
	// ffprobe (FFmpeg) — OPTIONAL and informational; never affects "ok". Present =
	// audio/video files get a created date + duration read during ingest (so a
	// musician's or filmmaker's library clusters into sessions by date the way a
	// photographer's does via EXIF); absent = those fields stay empty, ingest still
	// succeeds. It complements, never replaces, hash verification.
	fp, ferr := a.tool("ffprobe")
	ffprobe := map[string]any{"ok": ferr == nil, "path": fp}
	if ferr != nil {
		ffprobe["hint"] = ffprobeInstallHint
	}
	out["ffprobe"] = ffprobe
	// dvdisaster (disc-level ECC) — OPTIONAL and informational; never affects "ok".
	// Present = the Burn tab can auto-generate a per-disc .ecc after verify; absent =
	// the feature hides behind an install hint. It complements par2, never replaces it.
	dp, derr := a.tool("dvdisaster")
	dvd := map[string]any{"ok": derr == nil, "path": dp}
	if derr != nil {
		dvd["hint"] = dvdisasterInstallHint
	}
	out["dvdisaster"] = dvd
	// stenc (drive-level tape AES) — OPTIONAL, Linux only, informational; never
	// affects "ok". Present = the Tape Drive panel can read/manage the drive key;
	// absent (or non-Linux) = hidden behind an OS-aware hint. It is OUTSIDE the gpg
	// restore story — awareness, not dependence.
	out["stenc"] = a.StencStatus()
	// Tape diagnostics tool (ITDT / tapeinfo / sg_logs / HPE L&TT) — OPTIONAL and
	// informational; never affects "ok". Reads drive health only.
	out["tape_tool"] = a.TapeToolStatus()
	out["ok"] = allOK && ks["ok"].(bool)
	out["hints"] = hints
	return out
}

// ---- keystores -----------------------------------------------------------

type keystoreFile struct {
	Marker int `json:"mnemosyne_keystore"`
	// SchemaVersion is stamped on every write (absent/0 on legacy keystores, which
	// are structurally identical). Keys is append-only; entries tolerate being absent.
	SchemaVersion int              `json:"schema_version"`
	Keys          []map[string]any `json:"keys"`
}

func readStore(path string) (*keystoreFile, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &keystoreFile{Marker: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var ks keystoreFile
	if err := json.Unmarshal(b, &ks); err != nil || ks.Marker != 1 {
		return nil, fmt.Errorf("not a Mnemosyne keystore: %s", path)
	}
	if ks.SchemaVersion > currentSchemaVersion {
		return nil, fmt.Errorf("keystore %s was written by a newer Mnemosyne (schema v%d > v%d) — upgrade the app before writing keys", path, ks.SchemaVersion, currentSchemaVersion)
	}
	return &ks, nil
}

func writeStore(path string, ks *keystoreFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ks.Marker, ks.SchemaVersion = 1, currentSchemaVersion
	b, _ := json.MarshalIndent(ks, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *App) KeystoreStatus() map[string]any {
	cfg := a.LoadConfig()
	stores := []map[string]any{}
	sets := []map[string]bool{}
	reachable := true
	for _, p := range cfg.KeystorePaths {
		e := map[string]any{"path": p, "reachable": false, "key_count": 0}
		ks, err := readStore(p)
		if err == nil {
			e["reachable"] = true
			e["key_count"] = len(ks.Keys)
			set := map[string]bool{}
			for _, k := range ks.Keys {
				if r, ok := k["key_ref"].(string); ok {
					set[r] = true
				}
			}
			sets = append(sets, set)
		} else {
			e["error"] = err.Error()
			reachable = false
		}
		stores = append(stores, e)
	}
	consistent := true
	for i := 1; i < len(sets); i++ {
		if len(sets[i]) != len(sets[0]) {
			consistent = false
			break
		}
		for k := range sets[0] {
			if !sets[i][k] {
				consistent = false
			}
		}
	}
	ok := len(cfg.KeystorePaths) >= MinKeystores && reachable && consistent
	reason := ""
	switch {
	case len(cfg.KeystorePaths) < MinKeystores:
		reason = fmt.Sprintf("Only %d keystore path(s) registered; %d required, on different physical devices.", len(cfg.KeystorePaths), MinKeystores)
	case !reachable:
		reason = "One or more keystores are unreachable."
	case !consistent:
		reason = "Keystores hold different key sets — run key sync."
	}
	return map[string]any{"ok": ok, "reason": reason, "min_required": MinKeystores, "stores": stores}
}

func (a *App) GenerateKey(note string) (ref, passphrase, fpr string, err error) {
	st := a.KeystoreStatus()
	if !st["ok"].(bool) {
		return "", "", "", fmt.Errorf("keystore requirement not met: %s", st["reason"])
	}
	raw := make([]byte, 36)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	passphrase = base64.RawURLEncoding.EncodeToString(raw) // 288-bit
	rb := make([]byte, 4)
	_, _ = rand.Read(rb)
	ref = "K-" + strings.ToUpper(hex.EncodeToString(rb))
	sum := sha256.Sum256([]byte(passphrase))
	fpr = hex.EncodeToString(sum[:])
	rec := map[string]any{"key_ref": ref, "algorithm": "GPG-AES256", "passphrase": passphrase,
		"created_at": time.Now().UTC().Format(time.RFC3339), "note": note}
	cfg := a.LoadConfig()
	for _, p := range cfg.KeystorePaths {
		ks, e := readStore(p)
		if e != nil {
			return "", "", "", e
		}
		ks.Keys = append(ks.Keys, rec)
		if e := writeStore(p, ks); e != nil {
			return "", "", "", e
		}
	}
	a.Store.AddKeyMeta(KeyMeta{Ref: ref, Fingerprint: fpr, Note: note, CreatedAt: time.Now().UTC()})
	return
}

func (a *App) Passphrase(ref string) (string, error) {
	cfg := a.LoadConfig()
	for _, p := range cfg.KeystorePaths {
		ks, err := readStore(p)
		if err != nil {
			continue
		}
		for _, k := range ks.Keys {
			if k["key_ref"] == ref {
				if s, ok := k["passphrase"].(string); ok {
					return s, nil
				}
			}
		}
	}
	return "", fmt.Errorf("key %s not found in any keystore", ref)
}

func (a *App) SyncKeystores() (int, error) {
	cfg := a.LoadConfig()
	merged := map[string]map[string]any{}
	for _, p := range cfg.KeystorePaths {
		if ks, err := readStore(p); err == nil {
			for _, k := range ks.Keys {
				if r, ok := k["key_ref"].(string); ok {
					merged[r] = k
				}
			}
		}
	}
	out := &keystoreFile{Marker: 1}
	for _, k := range merged {
		out.Keys = append(out.Keys, k)
	}
	sort.Slice(out.Keys, func(i, j int) bool {
		a1, _ := out.Keys[i]["created_at"].(string)
		b1, _ := out.Keys[j]["created_at"].(string)
		return a1 < b1
	})
	for _, p := range cfg.KeystorePaths {
		if err := writeStore(p, out); err != nil {
			return 0, err
		}
	}
	return len(out.Keys), nil
}

// ---- scanning --------------------------------------------------------------

func (a *App) ScanFolder(collectionID int, root string, progress func(float64, string)) (int, error) {
	// SOURCE READ-ONLY: scanning only WalkDir-traverses and hashes (os.Open
	// O_RDONLY via hashFileHex). It registers `root` as a source root and writes
	// nothing back into it — the catalog is the only thing mutated.
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return 0, fmt.Errorf("not a readable folder: %s", root)
	}
	// Batch catalog writes for the duration of the scan (idempotent re-run).
	a.Store.BeginBatch()
	defer a.Store.EndBatch()
	a.Store.SetVersionsRetained(a.LoadConfig().VersionsRetained) // cap file-version history per config
	folder := a.Store.AddFolder(collectionID, root)

	var paths []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable, keep scanning
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	total := len(paths)
	reg := a.formatRegistry() // extension → role, for media metadata on still images
	parallelHash(paths, func(d int) {
		progress(float64(d)/float64(total), progStats(0, 0, int64(d), int64(total), "hashing source files"))
	}, func(p, sha, b3 string, size int64, mtime time.Time) {
		if rel, e := filepath.Rel(root, p); e == nil {
			// Media metadata: classify the role, and pull a capture/created date +
			// camera serial — EXIF for images, ffprobe for audio/video when available
			// (bounded, best-effort; empty on failure, never an error).
			role, _ := classifyRole(reg, rel)
			f := File{CollectionID: collectionID, FolderID: folder.ID,
				RelPath: filepath.ToSlash(rel), SizeBytes: size, HashAlg: "SHA256", Hash: sha, Blake3: b3, ModTime: mtime, Role: role}
			f.ShotAt, f.CameraSerial = a.extractMediaMeta(p, role)
			a.Store.UpsertFile(f)
		}
	})
	a.Store.Flush()
	a.Store.Log("scan", fmt.Sprintf("archive %d: %s (%d files)", collectionID, root, len(paths)))
	return len(paths), nil
}

// parallelHash hashes paths across a worker pool sized min(8, NumCPU). fn is
// called concurrently for each readable file with BOTH hashes computed in one
// read pass: sha256 (durable, on-media) and blake3 (fast, catalog-only) — callers
// must make fn safe (Store methods already lock). progress(done) fires every 25
// files and at the end. This is the shared pool used by scan, reconcile, and dock.
func parallelHash(paths []string, progress func(done int), fn func(path, sha, b3 string, size int64, mtime time.Time)) {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	total := len(paths)
	jobs := make(chan string)
	var done int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if st, e := os.Stat(p); e == nil && !st.IsDir() {
					if sha, b3, e := hashFileBoth(p); e == nil {
						fn(p, sha, b3, st.Size(), st.ModTime().UTC())
					}
				}
				if n := atomic.AddInt64(&done, 1); progress != nil && (n%25 == 0 || int(n) == total) {
					progress(int(n))
				}
			}
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
}

func hashFileHex(path string) (string, error) {
	// SOURCE READ-ONLY: os.Open is O_RDONLY. This is the ONLY way source files are
	// touched by scanning, drift rescan, and verification — read, hash, close.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 8<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---- planning ---------------------------------------------------------------

type PlanResult struct {
	Chunks    []*Chunk         `json:"chunks_created"`
	Oversized []map[string]any `json:"oversized_files_skipped"`
	Spanned   []map[string]any `json:"spanned_chunks"` // each: chunk, rel_path, size_bytes, media_required
	Payload   int64            `json:"payload_budget_bytes"`
	Staging   map[string]any   `json:"staging"`
}

func (a *App) Plan(collectionID int, mediaKind string, targetGB float64, par2 int, encrypted bool) (*PlanResult, error) {
	cfg := a.LoadConfig()
	if par2 <= 0 {
		par2 = a.effectiveIntegrity(collectionID).Par2Redundancy // archive override, else global preset
	}
	target := MediaPresets[mediaKind]
	if targetGB > 0 {
		target = int64(targetGB * 1e9)
	}
	if target <= 0 {
		return nil, fmt.Errorf("media_kind %s needs an explicit target_gb", mediaKind)
	}
	payload := int64(float64(target) / (1 + float64(par2)/100) * 0.985)

	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	files := a.Store.FilesOf(collectionID)
	if len(files) == 0 {
		return nil, fmt.Errorf("archive has no cataloged files — scan a folder first")
	}
	// A file is "already backed up" only if it's chunked AND its current hash
	// still matches the chunked hash. A changed-hash file counts as unchunked so
	// this Plan re-backs-up the new version naturally (older chunk stays historical).
	backedHash := a.Store.ChunkedFileHashes(collectionID)
	folders := map[int]string{}
	for _, f := range a.Store.FoldersOf(collectionID) {
		folders[f.ID] = f.Path
	}

	byRoot := map[int][]*File{}
	var bigFiles []*File // exceed one medium -> each becomes a spanned chunk
	for _, f := range files {
		if bh, ok := backedHash[f.ID]; ok && (bh == "" || bh == f.Hash) {
			continue // genuinely backed up (or a legacy chunk with no recorded hash)
		}
		if f.SizeBytes > payload {
			bigFiles = append(bigFiles, f)
			continue
		}
		byRoot[f.FolderID] = append(byRoot[f.FolderID], f)
	}

	seq := len(a.Store.Chunks(collectionID))
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, coll.Name)
	if len(safe) > 24 {
		safe = safe[:24]
	}
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "COLL"
	}

	res := &PlanResult{Payload: payload}
	fids := make([]int, 0, len(byRoot))
	for fid := range byRoot {
		fids = append(fids, fid)
	}
	sort.Ints(fids)
	for _, fid := range fids {
		group := byRoot[fid]
		sort.Slice(group, func(i, j int) bool { return group[i].RelPath < group[j].RelPath })
		var batch []ChunkFileRef
		var size int64
		flush := func() {
			if len(batch) == 0 {
				return
			}
			seq++
			c := a.Store.AddChunk(Chunk{CollectionID: collectionID, Name: fmt.Sprintf("%s-C%04d", safe, seq),
				Status: "PLANNED", MediaKind: mediaKind, TargetBytes: target, DataBytes: size,
				FileCount: len(batch), SrcRoot: folders[fid], HashAlg: "SHA256", Par2: par2,
				Encrypted: encrypted, Files: append([]ChunkFileRef{}, batch...)})
			res.Chunks = append(res.Chunks, c)
			batch, size = nil, 0
		}
		for _, f := range group {
			if len(batch) > 0 && size+f.SizeBytes > payload {
				flush()
			}
			batch = append(batch, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash})
			size += f.SizeBytes
		}
		flush()
	}

	// Spanned chunks: one oversized file each, byte-split across several media
	// at write time. Segment plan here is an estimate (based on target_bytes);
	// BuildChunk finalizes it against the real payload size.
	sort.Slice(bigFiles, func(i, j int) bool { return bigFiles[i].RelPath < bigFiles[j].RelPath })
	for _, f := range bigFiles {
		seq++
		est := f.SizeBytes + 4096 // ~tar overhead for a single file
		segs := planSegments(est, target)
		c := a.Store.AddChunk(Chunk{CollectionID: collectionID, Name: fmt.Sprintf("%s-C%04d", safe, seq),
			Status: "PLANNED", MediaKind: mediaKind, TargetBytes: target, DataBytes: f.SizeBytes,
			FileCount: 1, SrcRoot: folders[f.FolderID], HashAlg: "SHA256", Par2: par2,
			Encrypted: encrypted, Spanned: true, Segments: segs,
			Files: []ChunkFileRef{{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash}}})
		res.Chunks = append(res.Chunks, c)
		res.Spanned = append(res.Spanned, map[string]any{"chunk": c.Name, "rel_path": f.RelPath,
			"size_bytes": f.SizeBytes, "media_required": len(segs)})
	}

	// Staging peak is per-package (built one at a time, scratch reused), so the
	// binding constraint is the single largest package — computed via the shared
	// packageStagingPeak used by BuildChunk and /api/space-advice.
	staging := map[string]any{"staging_dir": cfg.StagingDir}
	if cfg.StagingDir != "" {
		if free, err := diskFree(cfg.StagingDir); err == nil {
			var peak int64
			for _, c := range res.Chunks {
				if p := packageStagingPeak(c.DataBytes, c.Par2, c.Encrypted, cfg.DeleteTarAfterEncrypt); p > peak {
					peak = p
				}
			}
			staging["free_bytes"] = free
			staging["peak_per_chunk_bytes"] = peak
			if peak > 0 {
				staging["chunks_stageable_concurrently"] = free / peak
			}
			if free < peak {
				staging["warning"] = "Staging free space cannot hold even one package build."
			}
		}
	}
	res.Staging = staging
	a.Store.Log("plan", fmt.Sprintf("archive %d -> %d package(s) on %s", collectionID, len(res.Chunks), mediaKind))
	return res, nil
}

// ---- building ------------------------------------------------------------

// Build-time fault-injection hooks. nil in production; only integration tests
// set them, to prove the verification stages actually catch a corrupted tar or
// a broken encryption step. buildAfterTarHook fires on the freshly written tar
// BEFORE stage verification; buildDecryptPassphraseHook rewrites the passphrase
// the crypt-verify decrypt uses (return a wrong one to simulate a corrupted
// encryption step). Guarded so a stray non-nil value can never affect a real
// build path — they are wired only from *_test.go in this package.
var (
	buildAfterTarHook          func(tarPath string)
	buildDecryptPassphraseHook func(pass string) string
)

// verifyTarContents streams the staged tar with Go's stdlib archive/tar reader
// (no extraction to disk, no external tool), hashes every regular-file member,
// and compares each against the catalog's source-file hash for that rel_path.
// It proves the package CONTAINS the source, byte-exact: any hash mismatch,
// missing source file, or unexpected extra member fails with the file named.
// A ChunkFileRef with an empty Hash (e.g. a hand-built chunk in a unit test)
// only has its presence checked — there is no source hash to compare against.
func verifyTarContents(tarPath string, files []ChunkFileRef) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	want := make(map[string]string, len(files))
	for _, cf := range files {
		want[path.Clean(cf.RelPath)] = cf.Hash
	}
	seen := make(map[string]bool, len(files))
	tr := tar.NewReader(f)
	buf := make([]byte, 1<<20)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stage verification: cannot read the staged tar (%v) — package is not a faithful archive", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue // directories, symlinks, etc. carry no cataloged source hash
		}
		name := path.Clean(strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./"))
		if name == bagPayloadManifestName {
			continue // the BagIt payload manifest is the first tar member, not a source file
		}
		exp, ok := want[name]
		if !ok {
			return fmt.Errorf("stage verification: package contains %q, which is not a cataloged member of this package (unexpected extra member)", name)
		}
		if seen[name] {
			return fmt.Errorf("stage verification: package contains %q more than once", name)
		}
		h := sha256.New()
		if _, err := io.CopyBuffer(h, tr, buf); err != nil {
			return fmt.Errorf("stage verification: reading %q from the tar: %w", name, err)
		}
		if exp != "" && hex.EncodeToString(h.Sum(nil)) != exp {
			return fmt.Errorf("stage verification: %s in the package does not match its source hash — the tar does not faithfully contain the source", name)
		}
		seen[name] = true
	}
	for rel := range want {
		if !seen[rel] {
			return fmt.Errorf("stage verification: source file %s is missing from the package", rel)
		}
	}
	return nil
}

// decryptRoundtripHash runs `gpg -d` on the ciphertext with stdout piped
// directly into a SHA-256 hasher — no plaintext ever lands on disk — and
// returns the hash of the decrypted stream. Used to prove the ciphertext
// actually decrypts back to the verified tar (compare against tar_hash).
func decryptRoundtripHash(gpgBin, ciphertext, pass string) (string, error) {
	cmd := exec.Command(gpgBin, "--batch", "--yes", "--pinentry-mode", "loopback",
		"--passphrase-fd", "0", "-d", ciphertext)
	cmd.Stdin = strings.NewReader(pass)
	var errb strings.Builder
	cmd.Stderr = &errb
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.CopyBuffer(h, pipe, make([]byte, 8<<20))
	waitErr := cmd.Wait()
	if waitErr != nil {
		return "", fmt.Errorf("gpg -d failed: %s", tail(errb.String(), 300))
	}
	if copyErr != nil {
		return "", copyErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *App) BuildChunk(id int, progress func(float64, string)) error {
	cfg := a.LoadConfig()
	c := a.Store.Chunk(id)
	if c == nil {
		return fmt.Errorf("package %d not found", id)
	}
	if c.Status != "PLANNED" && c.Status != "FAILED" {
		return fmt.Errorf("package %s is %s; only PLANNED/FAILED can build", c.Name, c.Status)
	}
	if c.Encrypted {
		if st := a.KeystoreStatus(); !st["ok"].(bool) {
			return fmt.Errorf("refusing to encrypt: %s", st["reason"])
		}
	}
	tarBin, err := a.tool("tar")
	if err != nil {
		return err
	}
	var gpgBin string
	if c.Encrypted {
		if gpgBin, err = a.tool("gpg"); err != nil {
			return err
		}
	}
	par2Bin, err := a.tool("par2")
	if err != nil {
		return err
	}
	if cfg.StagingDir == "" {
		return fmt.Errorf("staging_dir is not configured (Settings)")
	}
	// Re-check at build time (config could have been hand-edited): the staging dir
	// is written into and must never sit inside source data.
	if err := a.Store.AssertOutsideSources(cfg.StagingDir); err != nil {
		return err
	}
	work := filepath.Join(cfg.StagingDir, c.Name)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	// Pre-flight against this ONE package's build peak (shared with /api/space-advice
	// so the pre-build advice and this refusal never disagree).
	if free, err := diskFree(cfg.StagingDir); err == nil {
		if need := packageStagingPeak(c.DataBytes, c.Par2, c.Encrypted, cfg.DeleteTarAfterEncrypt); free < need {
			return fmt.Errorf("not enough staging space: this package's build peaks at ~%.1f GB but %s has only %.1f GB free — staging is just a folder, point it at any drive with room (Settings)",
				float64(need)/1e9, cfg.StagingDir, float64(free)/1e9)
		}
	}

	setStatus := func(st, msg string) { c.Status, c.Error = st, msg; a.Store.UpdateChunk(c) }
	setStatus("BUILDING", "")
	fail := func(err error) error { setStatus("FAILED", err.Error()); return err }

	list := filepath.Join(work, "filelist.txt")
	var sb strings.Builder
	for _, cf := range c.Files {
		sb.WriteString(cf.RelPath + "\n")
	}
	if err := os.WriteFile(list, []byte(sb.String()), 0o644); err != nil {
		return fail(err)
	}

	tarPath := filepath.Join(work, c.Name+".tar")
	payloadPath := filepath.Join(work, payloadName(c)) // <name>.tar.gpg (ciphertext) when encrypted, <name>.tar (the tar itself) when not

	// Per-stage timing so the operator can see WHERE the build time goes.
	// Pipeline order and the parity-over-payload rule are unchanged.
	timings := map[string]float64{}
	var summary []string
	finish := func(name string, since time.Time, frac float64) {
		secs := time.Since(since).Seconds()
		timings[name] = round2(secs)
		summary = append(summary, fmt.Sprintf("%s %.1fs", name, secs))
		progress(frac, strings.Join(summary, " · "))
	}

	// build_verify doctrine: "full" (default) PROVES the two custody links that
	// were previously only fingerprinted — the tar contains the source byte-exact
	// (stage_verify) and the ciphertext decrypts back to the tar (crypt_verify).
	// "fast" skips both and records the honest amber warning. See buildVerifyMode.
	// The build-verify tier comes from this archive's EFFECTIVE integrity (its own
	// override, else the global preset), and the resulting attestation records the
	// full effective settings so the medium self-documents its assurance level.
	iv := a.effectiveIntegrity(c.CollectionID)
	mode := iv.BuildVerify
	bv := &BuildVerified{Mode: mode, Preset: iv.Preset, Par2Percent: c.Par2,
		RoutineVerifyLevel: iv.RoutineVerifyLevel, VerifyDueMonths: iv.VerifyDueMonths, ReadbackAfterWrite: true}
	if mode == BuildVerifyNone {
		bv.Warning = fastBuildWarning
	}
	doContents := mode == BuildVerifyFull || mode == BuildVerifyContents
	doRoundtrip := mode == BuildVerifyFull

	// BagIt payload manifest, written to staging and made the FIRST member of the tar
	// (and, by writeBagItTags below, an identical sidecar on media). It lists the
	// source files as "sha256  relpath" over the ORIGINAL tree — so a reader can
	// stream-verify each member as it extracts, and the tree that comes out is
	// unchanged: this is one added description file at the tar root, never a restructure.
	manifestPath := filepath.Join(work, bagPayloadManifestName)
	if err := os.WriteFile(manifestPath, []byte(bagPayloadManifest(c.Files)), 0o644); err != nil {
		return fail(err)
	}

	progress(0.05, "tar…")
	t := time.Now()
	// SOURCE READ-ONLY: `tar -c … -C <SrcRoot> -T list` only READS the source
	// files; it writes exclusively to tarPath in the (validated) staging dir. tar
	// -c never modifies the files it archives. First member: the manifest (from
	// staging); then APPEND the source tree so the manifest leads the archive.
	if err := run(tarBin, "", "--format=posix", "-cf", tarPath, "-C", work, bagPayloadManifestName); err != nil {
		return fail(err)
	}
	if err := run(tarBin, "", "--format=posix", "-rf", tarPath, "-C", c.SrcRoot, "-T", list); err != nil {
		return fail(err)
	}
	finish("tar", t, 0.28)
	if buildAfterTarHook != nil { // test-only fault injection between tar and hash
		buildAfterTarHook(tarPath)
	}

	t = time.Now()
	if c.TarHash, err = hashFileHex(tarPath); err != nil {
		return fail(err)
	}
	finish("hash", t, 0.34)

	// (1) STAGE-VS-SOURCE — prove the package CONTAINS the source, byte-exact,
	// by stream-reading the tar and hashing every member against the catalog.
	// This runs before encryption (and before the tar may be deleted). "none"
	// skips it; contents stay unproven.
	if doContents {
		t = time.Now()
		if err := verifyTarContents(tarPath, c.Files); err != nil {
			return fail(err)
		}
		finish("stage_verify", t, 0.40)
		bv.Contents = true
		now := time.Now().UTC()
		a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: true, Path: tarPath,
			Note: fmt.Sprintf("stage_verify: %d member(s) byte-exact vs catalog source hashes", len(c.Files))})
	}

	var fpr string
	var chunkPass string // held to also encrypt the medium manifest when private
	if c.Encrypted {
		progress(0.40, strings.Join(summary, " · ")+" · encrypt…")
		t = time.Now()
		ref, pass, keyFpr, gerr := a.GenerateKey("package " + c.Name)
		if gerr != nil {
			return fail(gerr)
		}
		c.KeyRef, fpr, chunkPass = ref, keyFpr, pass
		if err := run(gpgBin, pass, "--batch", "--yes", "--pinentry-mode", "loopback",
			"--passphrase-fd", "0", "--symmetric", "--cipher-algo", "AES256",
			"--compress-algo", "none", "-o", payloadPath, tarPath); err != nil {
			return fail(err)
		}
		if c.EncHash, err = hashFileHex(payloadPath); err != nil {
			return fail(err)
		}
		if st, err := os.Stat(payloadPath); err == nil {
			c.EncBytes = st.Size()
		}
		finish("encrypt", t, 0.66)

		// (2) DECRYPT ROUND-TRIP — prove the ciphertext actually decrypts back to
		// the verified tar. gpg -d is piped straight into a SHA-256 hasher, so no
		// plaintext ever lands on disk; the result must equal tar_hash. Only the
		// full tier runs it; otherwise decryptability stays unproven.
		if doRoundtrip {
			t = time.Now()
			pass := chunkPass
			if buildDecryptPassphraseHook != nil { // test-only: simulate a corrupt encryption step
				pass = buildDecryptPassphraseHook(pass)
			}
			dh, derr := decryptRoundtripHash(gpgBin, payloadPath, pass)
			if derr != nil || dh != c.TarHash {
				return fail(fmt.Errorf("ciphertext does not decrypt to the verified tar — encryption step corrupted"))
			}
			finish("crypt_verify", t, 0.72)
			bv.DecryptRoundtrip = true
			now := time.Now().UTC()
			a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: true, Path: payloadPath,
				Note: "crypt_verify: ciphertext decrypts to the verified tar (tar_hash matched)"})
		}
		// Delete the intermediate tar only after both proofs are done with it.
		if cfg.DeleteTarAfterEncrypt {
			_ = os.Remove(tarPath)
		}
	} else {
		// No encryption requested: the tar IS the payload, and payloadName(c) is
		// <name>.tar, so it already sits at payloadPath (== tarPath) — no rename,
		// no misleading .gpg suffix on the medium. enc_hash/enc_bytes describe
		// that tar as written to media.
		t = time.Now()
		c.EncHash = c.TarHash
		if st, err := os.Stat(payloadPath); err == nil {
			c.EncBytes = st.Size()
		}
		finish("stage", t, 0.72)
		// A plaintext package has no encryption step to corrupt: its payload IS the
		// stage-verified tar, so the decrypt round-trip holds true by identity when
		// contents were proven. Left false when contents were not proven.
		if doRoundtrip {
			bv.DecryptRoundtrip = true
		}
	}

	progress(0.75, strings.Join(summary, " · ")+" · par2…")
	t = time.Now()
	if err := runPar2Create(par2Bin, cfg.Par2ExtraArgs, c.Par2,
		payloadPath+".par2", payloadPath); err != nil {
		return fail(err)
	}
	finish("par2", t, 0.90)
	c.BuildTimings = timings
	c.BuildVerified = bv // build-time attestation, carried into the manifest below
	if c.Spanned {
		// Finalize the segment plan against the real payload + par2 size now
		// that both exist, so the manifest/RESTORE.txt written below match.
		a.finalizeSegments(c, work)
	}

	progress(0.92, "manifest")
	// Private media: the medium carries an ENCRYPTED manifest so filenames don't
	// leak off a lost tape. The staged plaintext manifest stays for local use;
	// medium-write paths skip it and ship the .gpg instead.
	c.PrivateManifest = cfg.PrivateMedia && c.Encrypted
	if err := writeManifest(work, c, fpr); err != nil {
		return fail(err)
	}
	if c.PrivateManifest {
		manPlain := filepath.Join(work, c.Name+".manifest.json")
		if err := run(gpgBin, chunkPass, "--batch", "--yes", "--pinentry-mode", "loopback",
			"--passphrase-fd", "0", "--symmetric", "--cipher-algo", "AES256",
			"--compress-algo", "none", "-o", manPlain+".gpg", manPlain); err != nil {
			return fail(fmt.Errorf("encrypting private manifest: %w", err))
		}
	}
	writeRestoreTxt(work, c, cfg.eccIntended())
	writeBagItTags(work, c) // BagIt tag files beside the package (institutional legibility)

	c.StagedDir = work
	setStatus("STAGED", "")
	a.Store.Log("build", c.Name+" staged")
	progress(1.0, "staged")
	return nil
}

// runPar2Create runs `par2 create` with optional extra args (e.g. "-t0" =
// use all threads, on par2 builds that support it). If par2 rejects an extra
// option, it retries once WITHOUT the extras so stock par2cmdline still works.
func runPar2Create(par2Bin, extraArgs string, redundancy int, par2File, payload string) error {
	base := []string{"create", fmt.Sprintf("-r%d", redundancy), "-n1"}
	extra := strings.Fields(extraArgs)
	args := append(append(append([]string{}, base...), extra...), par2File, payload)
	err := run(par2Bin, "", args...)
	if err != nil && len(extra) > 0 && isUnknownOptionErr(err) {
		fallback := append(append([]string{}, base...), par2File, payload)
		return run(par2Bin, "", fallback...)
	}
	return err
}

func isUnknownOptionErr(err error) bool {
	s := strings.ToLower(err.Error())
	// e.g. stock par2cmdline: "Invalid option specified: -t0" or
	// "Invalid thread option: -t0". Match "option" alongside a reject word so
	// the intervening word ("thread", "specified") doesn't defeat detection.
	if strings.Contains(s, "option") &&
		(strings.Contains(s, "invalid") || strings.Contains(s, "unknown") ||
			strings.Contains(s, "unrecogni") || strings.Contains(s, "unsupported")) {
		return true
	}
	for _, k := range []string{"invalid command line", "usage:"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func run(bin, stdin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 700 {
			tail = tail[len(tail)-700:]
		}
		return fmt.Errorf("%s failed: %v: %s", filepath.Base(bin), err, tail)
	}
	return nil
}

// payloadName is the on-medium filename of a package's payload: <name>.tar for
// a plaintext package (the payload IS the POSIX tar) and <name>.tar.gpg when the
// tar is encrypted. This is the SINGLE source of truth for the payload filename —
// every place that builds or searches for a payload name (build, write, verify,
// burn, span, restore, manifest, RESTORE.txt) goes through here so a plaintext
// package is never mislabelled ".tar.gpg" on a 30-year medium. The par2 set names
// follow the payload name (<payload>.par2, <payload>.volNNN+MM.par2).
func payloadName(c *Chunk) string {
	if c.Encrypted {
		return c.Name + ".tar.gpg"
	}
	return c.Name + ".tar"
}

// legacyPayloadName is the pre-rename uniform name: plaintext packages built
// before payloadName always wrote <name>.tar.gpg for code-path uniformity. READ
// paths fall back to this so media staged/written under the old scheme keep
// verifying and restoring. For encrypted packages it equals payloadName.
func legacyPayloadName(c *Chunk) string { return c.Name + ".tar.gpg" }

// payloadNameCandidates lists the payload filenames a READ path should try, most
// current first: the real name, then the legacy .tar.gpg name when it differs
// (i.e. for plaintext packages). Deduped so encrypted packages get one entry.
func payloadNameCandidates(c *Chunk) []string {
	n := payloadName(c)
	if leg := legacyPayloadName(c); leg != n {
		return []string{n, leg}
	}
	return []string{n}
}

// payloadPathIn returns the payload's path directly inside dir (current name
// first, then the legacy name), or "" if neither exists.
func payloadPathIn(dir string, c *Chunk) string {
	for _, n := range payloadNameCandidates(c) {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// findPayload locates a package's payload under dir, honoring both the current
// name and the legacy .tar.gpg name, and both flat (dir/<payload>) and boxed
// (dir/<name>/<payload>) layouts. Returns "" if none is found.
func findPayload(dir string, c *Chunk) string {
	if p := payloadPathIn(dir, c); p != "" {
		return p
	}
	return payloadPathIn(filepath.Join(dir, c.Name), c)
}

func writeManifest(dir string, c *Chunk, keyFpr string) error {
	m := map[string]any{
		"mnemosyne_chunk": 1, "schema_version": currentSchemaVersion, "mnemosyne_version": appVersion,
		"name": c.Name, "created_utc": time.Now().UTC().Format(time.RFC3339),
		"collection_id": c.CollectionID, "source_root": c.SrcRoot,
		"encrypted": c.Encrypted,
		// payload_file is the exact on-medium filename of the payload: <name>.tar.gpg
		// when encrypted, <name>.tar when not. The par2 set is <payload_file>*.par2.
		"payload_file": payloadName(c),
		"hash_alg":     c.HashAlg, "tar_hash": c.TarHash,
		// ciphertext_hash/_bytes name the payload file as written to media. When
		// encrypted:false the payload is the plain tar (payload_file = <name>.tar),
		// so `sha256sum <payload_file>` still matches this value.
		"ciphertext_hash": c.EncHash, "ciphertext_bytes": c.EncBytes,
		"par2_redundancy_percent": c.Par2, "files": c.Files,
	}
	// build_verified attests, on the medium itself, that this package was proven
	// to contain the source (contents) and to decrypt back to the verified tar
	// (decrypt_roundtrip) at build time. A "fast" build carries mode:"fast" with
	// both false and a warning, so a reader of the tape knows what was NOT proven.
	if c.BuildVerified != nil {
		m["build_verified"] = c.BuildVerified
	}
	if c.Encrypted {
		m["cipher"] = "GnuPG symmetric AES-256, compression none"
		m["key_ref"] = c.KeyRef
		m["key_fingerprint_sha256"] = keyFpr
	} else {
		m["cipher"] = "none — payload is a plain POSIX tar"
	}
	if c.Spanned {
		m["spanned"] = true
		segs := make([]map[string]any, 0, len(c.Segments))
		for _, sg := range c.Segments {
			e := map[string]any{"index": sg.Index, "bytes": sg.Bytes}
			if sg.Par2 {
				e["par2_tape"] = true
			}
			segs = append(segs, e)
		}
		m["segments"] = segs
		m["rejoin_windows"] = "copy /b " + segJoinList(c) + " " + payloadName(c)
		m["rejoin_unix"] = "cat " + c.Name + ".seg* > " + payloadName(c)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, c.Name+".manifest.json"), b, 0o644)
}

// segJoinList builds "NAME.seg001+NAME.seg002+..." for the Windows copy /b rejoin.
func segJoinList(c *Chunk) string {
	n := dataSegmentCount(c.Segments)
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, fmt.Sprintf("%s.seg%03d", c.Name, i))
	}
	return strings.Join(parts, "+")
}

func writeRestoreTxt(dir string, c *Chunk, opticalEcc bool) {
	var t string
	if c.Spanned {
		t = spannedRestoreTxt(c)
		t = appendPrivacyNote(t, c)
		t = appendOpticalNote(t, c, opticalEcc)
		_ = os.WriteFile(filepath.Join(dir, "RESTORE.txt"), []byte(t), 0o644)
		return
	}
	if c.Encrypted {
		pl := payloadName(c) // <name>.tar.gpg
		t = fmt.Sprintf(`HOW TO RESTORE %[1]s
==========================================================
You need exactly three ubiquitous open-source programs:
  par2  (github.com/Parchive/par2cmdline)  - repair
  gpg   (gnupg.org)                        - decrypt
  tar   (POSIX standard, preinstalled)     - extract
And the passphrase for key %[2]s (Mnemosyne keystore, or the
printed key card labelled %[2]s).

1) VERIFY / REPAIR (no passphrase needed):
     par2 verify %[6]s.par2
     par2 repair %[6]s.par2      (only if verify fails)
2) DECRYPT (prompts for passphrase):
     gpg -d -o %[1]s.tar %[6]s
3) EXTRACT all, or one file:
     tar -xf %[1]s.tar
     tar -xf %[1]s.tar path/to/one/file

Integrity (%[3]s):  ciphertext %[4]s   tar %[5]s
Full file list: %[1]s.manifest.json
No compression anywhere — extracted bytes are the originals.
==========================================================
`, c.Name, c.KeyRef, c.HashAlg, c.EncHash, c.TarHash, pl)
	} else {
		pl := payloadName(c) // <name>.tar
		t = fmt.Sprintf(`HOW TO RESTORE %[1]s
==========================================================
This package is stored UNENCRYPTED — no passphrase or key needed.
The payload file %[4]s is a plain POSIX tar.

You need exactly two ubiquitous open-source programs:
  par2  (github.com/Parchive/par2cmdline)  - repair
  tar   (POSIX standard, preinstalled)     - extract

1) VERIFY / REPAIR:
     par2 verify %[4]s.par2
     par2 repair %[4]s.par2      (only if verify fails)
2) EXTRACT all, or one file (tar reads it directly, no gpg step):
     tar -xf %[4]s
     tar -xf %[4]s path/to/one/file

Integrity (%[2]s):  payload %[3]s
Full file list: %[1]s.manifest.json
No compression anywhere — extracted bytes are the originals.
==========================================================
`, c.Name, c.HashAlg, c.EncHash, pl)
	}
	t = appendPrivacyNote(t, c)
	t = appendOpticalNote(t, c, opticalEcc)
	_ = os.WriteFile(filepath.Join(dir, "RESTORE.txt"), []byte(t), 0o644)
}

// appendOpticalNote adds the optional dvdisaster-ECC paragraph for optical
// packages — stating plainly that par2 repair works regardless.
func appendOpticalNote(t string, c *Chunk, eccEnabled bool) string {
	if !isOpticalKind(c.MediaKind) {
		return t
	}
	return t + opticalEccParagraph(c.Name, eccEnabled)
}

// appendPrivacyNote adds the one paragraph RESTORE.txt needs when this medium's
// file listing is encrypted. RESTORE.txt itself contains no filenames.
func appendPrivacyNote(t string, c *Chunk) string {
	if !c.PrivateManifest {
		return t
	}
	return t + fmt.Sprintf(`
PRIVACY — this medium's file listing is ENCRYPTED (%[1]s.manifest.json.gpg),
so a lost tape reveals no filenames. To read it:
     gpg -d %[1]s.manifest.json.gpg      (passphrase for key %[2]s)
`, c.Name, c.KeyRef)
}

// spannedRestoreTxt documents the rejoin step (universal one-liner) BEFORE the
// usual par2 -> gpg -> tar, and is copied verbatim onto every tape in the set.
func spannedRestoreTxt(c *Chunk) string {
	n := dataSegmentCount(c.Segments)
	par2Home := "on the LAST tape, alongside the last segment"
	if len(c.Segments) > 0 && c.Segments[len(c.Segments)-1].Par2 {
		par2Home = "on its OWN final tape (labelled ...-tape-" + fmt.Sprint(len(c.Segments)) + "-of-" + fmt.Sprint(len(c.Segments)) + ")"
	}
	pl := payloadName(c) // <name>.tar (plaintext) or <name>.tar.gpg (encrypted)
	decrypt := "2) EXTRACT directly (plaintext — no key needed):\n     tar -xf " + pl
	if c.Encrypted {
		decrypt = "2) DECRYPT (prompts for the passphrase of key " + c.KeyRef + "):\n     gpg -d -o " + c.Name + ".tar " + pl + "\n" +
			"3) EXTRACT:\n     tar -xf " + c.Name + ".tar"
	}
	return fmt.Sprintf(`HOW TO RESTORE %[1]s   (SPANNED across %[2]d media)
==========================================================
This package's payload was byte-split across %[2]d tapes/drives as
%[1]s.seg001 … %[1]s.seg%[3]03d. Every tape also carries this file,
the manifest, and (par2 %[4]s).

STEP 0 — REJOIN the segments into the payload (one universal command).
Copy every tape's %[1]s.segNNN into ONE folder, then run ONE of:
     Windows:  copy /b %[5]s %[10]s
     Unix/mac: cat %[1]s.seg* > %[10]s
(The zero-padded names sort into the correct order automatically.)

Now the normal restore, exactly as for a single-medium package:
1) VERIFY / REPAIR (no key needed):
     par2 verify %[10]s.par2
     par2 repair %[10]s.par2      (only if verify fails)
%[6]s

Integrity (%[7]s): payload %[8]s   tar %[9]s
Custody chain: source file SHA-256s -> tar hash -> payload hash ->
per-segment hashes (manifest / catalog) link every tape to the originals.
Full file list: %[1]s.manifest.json
No compression anywhere — extracted bytes are the originals.
==========================================================
`, c.Name, n, n, par2Home, segJoinList(c), decrypt, c.HashAlg, c.EncHash, c.TarHash, pl)
}
