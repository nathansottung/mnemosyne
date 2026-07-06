package main

// pipeline.go — configuration, keystores, scanning, planning, building.
// Restore doctrine: par2 -> gpg -> tar, always runnable by hand.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MinKeystores = 2

var MediaPresets = map[string]int64{
	"LTO8": 11_000_000_000_000, "LTO9": 16_500_000_000_000,
	"BD-R25": 23_000_000_000, "BD-R50": 46_000_000_000, "BD-R100": 92_000_000_000,
	"HDD": 0, "CUSTOM": 0,
}

// ---- config ------------------------------------------------------------

type Config struct {
	StagingDir            string            `json:"staging_dir"`
	KeystorePaths         []string          `json:"keystore_paths"`
	DeleteTarAfterEncrypt bool              `json:"delete_tar_after_encrypt"`
	Par2Redundancy        int               `json:"par2_redundancy"`
	BufferGB              float64           `json:"buffer_gb"`
	BlockMB               int               `json:"block_mb"`
	Tools                 map[string]string `json:"tools"`
}

func defaultConfig() Config {
	return Config{DeleteTarAfterEncrypt: true, Par2Redundancy: 10, BufferGB: 8, BlockMB: 64, Tools: map[string]string{}}
}

type App struct {
	DataDir string
	Store   *Store
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

func (a *App) Preflight() map[string]any {
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
			if v, e := exec.Command(p, "--version").CombinedOutput(); e == nil {
				lines := strings.SplitN(strings.TrimSpace(string(v)), "\n", 2)
				item["version"] = lines[0]
			}
		} else {
			hints = append(hints, name+": "+hint)
			allOK = false
		}
		out[name] = item
	}
	ks := a.KeystoreStatus()
	out["keystores"] = ks
	out["ok"] = allOK && ks["ok"].(bool)
	out["hints"] = hints
	return out
}

// ---- keystores -----------------------------------------------------------

type keystoreFile struct {
	Marker int              `json:"mnemosyne_keystore"`
	Keys   []map[string]any `json:"keys"`
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
	return &ks, nil
}

func writeStore(path string, ks *keystoreFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
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
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return 0, fmt.Errorf("not a readable folder: %s", root)
	}
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
	for i, p := range paths {
		rel, _ := filepath.Rel(root, p)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		h, err := hashFileHex(p)
		if err != nil {
			continue
		}
		a.Store.UpsertFile(File{CollectionID: collectionID, FolderID: folder.ID,
			RelPath: filepath.ToSlash(rel), SizeBytes: st.Size(), HashAlg: "SHA256", Hash: h})
		if i%25 == 0 {
			progress(float64(i)/float64(len(paths)), fmt.Sprintf("hashed %d/%d", i, len(paths)))
		}
	}
	a.Store.Flush()
	a.Store.Log("scan", fmt.Sprintf("collection %d: %s (%d files)", collectionID, root, len(paths)))
	return len(paths), nil
}

func hashFileHex(path string) (string, error) {
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
	Payload   int64            `json:"payload_budget_bytes"`
	Staging   map[string]any   `json:"staging"`
}

func (a *App) Plan(collectionID int, mediaKind string, targetGB float64, par2 int) (*PlanResult, error) {
	cfg := a.LoadConfig()
	if par2 <= 0 {
		par2 = cfg.Par2Redundancy
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
		return nil, fmt.Errorf("collection %d not found", collectionID)
	}
	files := a.Store.FilesOf(collectionID)
	if len(files) == 0 {
		return nil, fmt.Errorf("collection has no cataloged files — scan a folder first")
	}
	already := a.Store.ChunkedFileIDs(collectionID)
	folders := map[int]string{}
	for _, f := range a.Store.FoldersOf(collectionID) {
		folders[f.ID] = f.Path
	}

	byRoot := map[int][]*File{}
	var oversized []map[string]any
	for _, f := range files {
		if already[f.ID] {
			continue
		}
		if f.SizeBytes > payload {
			oversized = append(oversized, map[string]any{"rel_path": f.RelPath, "size_bytes": f.SizeBytes,
				"note": "exceeds one medium's payload budget; spanning is a v2 feature"})
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

	res := &PlanResult{Payload: payload, Oversized: oversized}
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
				Files: append([]ChunkFileRef{}, batch...)})
			res.Chunks = append(res.Chunks, c)
			batch, size = nil, 0
		}
		for _, f := range group {
			if len(batch) > 0 && size+f.SizeBytes > payload {
				flush()
			}
			batch = append(batch, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes})
			size += f.SizeBytes
		}
		flush()
	}

	staging := map[string]any{"staging_dir": cfg.StagingDir}
	if cfg.StagingDir != "" {
		if free, err := diskFree(cfg.StagingDir); err == nil {
			var worst int64 = payload
			for _, c := range res.Chunks {
				if c.DataBytes > worst {
					worst = c.DataBytes
				}
			}
			mult := 2.02
			if !cfg.DeleteTarAfterEncrypt {
				mult += float64(par2) / 100
			}
			peak := int64(float64(worst) * mult)
			staging["free_bytes"] = free
			staging["peak_per_chunk_bytes"] = peak
			if peak > 0 {
				staging["chunks_stageable_concurrently"] = free / peak
			}
			if free < peak {
				staging["warning"] = "Staging free space cannot hold even one chunk build."
			}
		}
	}
	res.Staging = staging
	a.Store.Log("plan", fmt.Sprintf("collection %d -> %d chunk(s) on %s", collectionID, len(res.Chunks), mediaKind))
	return res, nil
}

// ---- building ------------------------------------------------------------

func (a *App) BuildChunk(id int, progress func(float64, string)) error {
	cfg := a.LoadConfig()
	c := a.Store.Chunk(id)
	if c == nil {
		return fmt.Errorf("chunk %d not found", id)
	}
	if c.Status != "PLANNED" && c.Status != "FAILED" {
		return fmt.Errorf("chunk %s is %s; only PLANNED/FAILED can build", c.Name, c.Status)
	}
	if st := a.KeystoreStatus(); !st["ok"].(bool) {
		return fmt.Errorf("refusing to encrypt: %s", st["reason"])
	}
	tarBin, err := a.tool("tar")
	if err != nil {
		return err
	}
	gpgBin, err := a.tool("gpg")
	if err != nil {
		return err
	}
	par2Bin, err := a.tool("par2")
	if err != nil {
		return err
	}
	if cfg.StagingDir == "" {
		return fmt.Errorf("staging_dir is not configured (Settings)")
	}
	work := filepath.Join(cfg.StagingDir, c.Name)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	mult := 2.02
	if !cfg.DeleteTarAfterEncrypt {
		mult += float64(c.Par2) / 100
	}
	if free, err := diskFree(cfg.StagingDir); err == nil {
		if need := int64(float64(c.DataBytes) * mult); free < need {
			return fmt.Errorf("not enough staging space: need ~%.1f GB peak, have %.1f GB", float64(need)/1e9, float64(free)/1e9)
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
	encPath := filepath.Join(work, c.Name+".tar.gpg")

	progress(0.05, "tar")
	if err := run(tarBin, "", "--format=posix", "-cf", tarPath, "-C", c.SrcRoot, "-T", list); err != nil {
		return fail(err)
	}
	progress(0.30, "hash tar")
	if c.TarHash, err = hashFileHex(tarPath); err != nil {
		return fail(err)
	}

	progress(0.40, "encrypt")
	ref, pass, fpr, err := a.GenerateKey("chunk " + c.Name)
	if err != nil {
		return fail(err)
	}
	c.KeyRef = ref
	if err := run(gpgBin, pass, "--batch", "--yes", "--pinentry-mode", "loopback",
		"--passphrase-fd", "0", "--symmetric", "--cipher-algo", "AES256",
		"--compress-algo", "none", "-o", encPath, tarPath); err != nil {
		return fail(err)
	}

	progress(0.65, "hash ciphertext")
	if c.EncHash, err = hashFileHex(encPath); err != nil {
		return fail(err)
	}
	if st, err := os.Stat(encPath); err == nil {
		c.EncBytes = st.Size()
	}
	if cfg.DeleteTarAfterEncrypt {
		_ = os.Remove(tarPath)
	}

	progress(0.75, "par2")
	if err := run(par2Bin, "", "create", fmt.Sprintf("-r%d", c.Par2), "-n1",
		filepath.Join(work, c.Name+".tar.gpg.par2"), encPath); err != nil {
		return fail(err)
	}

	progress(0.92, "manifest")
	if err := writeManifest(work, c, fpr); err != nil {
		return fail(err)
	}
	writeRestoreTxt(work, c)

	c.StagedDir = work
	setStatus("STAGED", "")
	a.Store.Log("build", c.Name+" staged")
	progress(1.0, "staged")
	return nil
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

func writeManifest(dir string, c *Chunk, keyFpr string) error {
	m := map[string]any{
		"mnemosyne_chunk": 1, "name": c.Name, "created_utc": time.Now().UTC().Format(time.RFC3339),
		"collection_id": c.CollectionID, "source_root": c.SrcRoot,
		"cipher": "GnuPG symmetric AES-256, compression none",
		"key_ref": c.KeyRef, "key_fingerprint_sha256": keyFpr,
		"hash_alg": c.HashAlg, "tar_hash": c.TarHash,
		"ciphertext_hash": c.EncHash, "ciphertext_bytes": c.EncBytes,
		"par2_redundancy_percent": c.Par2, "files": c.Files,
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, c.Name+".manifest.json"), b, 0o644)
}

func writeRestoreTxt(dir string, c *Chunk) {
	t := fmt.Sprintf(`HOW TO RESTORE %[1]s
==========================================================
You need exactly three ubiquitous open-source programs:
  par2  (github.com/Parchive/par2cmdline)  - repair
  gpg   (gnupg.org)                        - decrypt
  tar   (POSIX standard, preinstalled)     - extract
And the passphrase for key %[2]s (Mnemosyne keystore, or the
printed key card labelled %[2]s).

1) VERIFY / REPAIR (no passphrase needed):
     par2 verify %[1]s.tar.gpg.par2
     par2 repair %[1]s.tar.gpg.par2      (only if verify fails)
2) DECRYPT (prompts for passphrase):
     gpg -d -o %[1]s.tar %[1]s.tar.gpg
3) EXTRACT all, or one file:
     tar -xf %[1]s.tar
     tar -xf %[1]s.tar path/to/one/file

Integrity (%[3]s):  ciphertext %[4]s   tar %[5]s
Full file list: %[1]s.manifest.json
No compression anywhere — extracted bytes are the originals.
==========================================================
`, c.Name, c.KeyRef, c.HashAlg, c.EncHash, c.TarHash)
	_ = os.WriteFile(filepath.Join(dir, "RESTORE.txt"), []byte(t), 0o644)
}
