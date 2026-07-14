package main

// appbackup.go — one-file export/restore of the app's COMPLETE state, so the
// verification history and everything the tool knows can travel with (or ahead of)
// the data to a new machine or OS. The bundle is a plain, uncompressed tar
// (inspectable with stock `tar`, per doctrine) plus a `.sha256` sidecar over the
// whole file.
//
// What travels: the catalog (files, hashes, volumes, locations, copies, packages,
// segments, events, plans, conflicts, version histories, drift/scrub reports, dock
// sessions, profiles, templates), config, the persisted job history with artifacts,
// and any user format-registry overrides. Keystores are SECRETS the user may keep on
// separate devices; they are EXCLUDED by default and only ride along on explicit
// opt-in.
//
// Restore is defensive: it verifies the sidecar and every member hash BEFORE it
// touches the data dir, refuses a newer-schema backup (update the app first) up
// front, backs up the current records, then extracts and reopens the store — which
// runs the ordinary schema-migration registry to bring an older backup forward.
// Volumes reconnect by serial automatically the next time they're seen (see
// Store.VolumeBySerial); restore does nothing special for that.

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	appBackupFormat  = "mnemosyne-appbackup"
	appBackupVersion = 1
	appBackupName    = "mnemosyne" // filename stem: <appname>-appbackup-<date>.tar
)

// appBackupMember is one file inside the bundle, with its SHA-256 for the manifest.
type appBackupMember struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// appBackupManifest is MANIFEST.json — the self-describing header. It lists a
// SHA-256 of every OTHER member (never itself); the whole-tar .sha256 sidecar covers
// the manifest too.
type appBackupManifest struct {
	Format        string            `json:"format"`
	Version       int               `json:"version"`
	SchemaVersion int               `json:"schema_version"`
	AppVersion    string            `json:"app_version"`
	Created       string            `json:"created"`
	IncludesKeys  bool              `json:"includes_keys"`
	Members       []appBackupMember `json:"members"`
}

// ExportResult is the summary returned after writing a bundle.
type ExportResult struct {
	TarPath       string `json:"tar_path"`
	SidecarPath   string `json:"sidecar_path"`
	Bytes         int64  `json:"bytes"`
	MemberCount   int    `json:"member_count"`
	IncludesKeys  bool   `json:"includes_keys"`
	SchemaVersion int    `json:"schema_version"`
}

// RestoreResult is the post-restore summary.
type RestoreResult struct {
	SchemaVersion int    `json:"schema_version"`
	AppVersion    string `json:"app_version"`
	Created       string `json:"created"`
	IncludedKeys  bool   `json:"included_keys"`
	Archives      int    `json:"archives"`
	Files         int    `json:"files"`
	Volumes       int    `json:"volumes"`
	Packages      int    `json:"packages"`
	Jobs          int    `json:"jobs"`
	VerifyEvents  int    `json:"verify_events"`
	PreRestoreDir string `json:"pre_restore_dir"`
	ReadOnly      bool   `json:"read_only,omitempty"`
	ReadOnlyWhy   string `json:"read_only_why,omitempty"`
}

// rawMember is a gathered file: its in-tar name and exact bytes.
type rawMember struct {
	name string
	data []byte
}

// gatherMembers reads the data-dir files that make up the app state. config.json is
// re-marshaled with the auth token scrubbed (a deployment secret, not knowledge);
// keystores are included only when opted in. Missing optional files are skipped.
func (a *App) gatherMembers(includeKeys bool) ([]rawMember, error) {
	var members []rawMember

	// catalog.json — the exact on-disk bytes (the source of truth).
	catalog, err := os.ReadFile(filepath.Join(a.DataDir, "catalog.json"))
	if err != nil {
		return nil, fmt.Errorf("read catalog.json: %w", err)
	}
	members = append(members, rawMember{"catalog.json", catalog})

	// config.json — scrub the auth token so a deployment secret never travels.
	cfg := a.LoadConfig()
	cfg.AuthToken = ""
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	members = append(members, rawMember{"config.json", cfgBytes})

	// jobs.json — persisted job board (history + artifacts). Optional (a brand-new
	// install may have none).
	if b, err := os.ReadFile(filepath.Join(a.DataDir, "jobs.json")); err == nil {
		members = append(members, rawMember{"jobs.json", b})
	}
	// formats.json — user format-registry overrides. Optional.
	if b, err := os.ReadFile(filepath.Join(a.DataDir, "formats.json")); err == nil {
		members = append(members, rawMember{"formats.json", b})
	}

	if includeKeys {
		seen := map[string]bool{}
		for _, ksPath := range cfg.KeystorePaths {
			b, err := os.ReadFile(ksPath)
			if err != nil {
				return nil, fmt.Errorf("read keystore %q: %w", ksPath, err)
			}
			name := "keystores/" + filepath.Base(ksPath)
			if seen[name] { // two keystores with the same basename — disambiguate
				name = fmt.Sprintf("keystores/%d-%s", len(members), filepath.Base(ksPath))
			}
			seen[name] = true
			members = append(members, rawMember{name, b})
		}
	}
	return members, nil
}

// exportAppBackupTo writes the bundle to exactly tarPath (+ .sha256 sidecar). Core
// used by both the manual export (dated filename) and the auto-export ticker
// (per-period filename). The write is atomic (temp + rename).
func (a *App) exportAppBackupTo(tarPath string, includeKeys bool) (ExportResult, error) {
	members, err := a.gatherMembers(includeKeys)
	if err != nil {
		return ExportResult{}, err
	}

	// Schema version comes from the catalog member itself, so the manifest states what
	// the bundle actually holds.
	schemaVer := currentSchemaVersion
	for _, m := range members {
		if m.name == "catalog.json" {
			var head struct {
				SchemaVersion int `json:"schema_version"`
			}
			if json.Unmarshal(m.data, &head) == nil && head.SchemaVersion > 0 {
				schemaVer = head.SchemaVersion
			}
		}
	}

	man := appBackupManifest{
		Format: appBackupFormat, Version: appBackupVersion, SchemaVersion: schemaVer,
		AppVersion: appVersion, Created: time.Now().UTC().Format(time.RFC3339),
		IncludesKeys: includeKeys,
	}
	for _, m := range members {
		man.Members = append(man.Members, appBackupMember{Name: m.name, SHA256: sha256Hex(m.data), Size: int64(len(m.data))})
	}
	manBytes, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return ExportResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(tarPath), 0o755); err != nil {
		return ExportResult{}, err
	}
	tmp := tarPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return ExportResult{}, err
	}
	tw := tar.NewWriter(f)
	now := time.Now().UTC()
	// MANIFEST.json first, then every gathered member.
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: now, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	writeErr := write("MANIFEST.json", manBytes)
	for _, m := range members {
		if writeErr != nil {
			break
		}
		writeErr = write(m.name, m.data)
	}
	if writeErr == nil {
		writeErr = tw.Close()
	} else {
		_ = tw.Close()
	}
	if cerr := f.Close(); writeErr == nil {
		writeErr = cerr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return ExportResult{}, writeErr
	}
	if err := atomicRename(tmp, tarPath); err != nil {
		_ = os.Remove(tmp)
		return ExportResult{}, err
	}

	// Whole-tar sidecar: <tar>.sha256, "  <sha>  <basename>\n" (sha256sum format).
	sum, err := hashFileHex(tarPath)
	if err != nil {
		return ExportResult{}, err
	}
	sidecar := tarPath + ".sha256"
	if err := os.WriteFile(sidecar, []byte(fmt.Sprintf("%s  %s\n", sum, filepath.Base(tarPath))), 0o644); err != nil {
		return ExportResult{}, err
	}

	fi, _ := os.Stat(tarPath)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	return ExportResult{
		TarPath: tarPath, SidecarPath: sidecar, Bytes: size,
		MemberCount: len(members) + 1, IncludesKeys: includeKeys, SchemaVersion: schemaVer,
	}, nil
}

// appBackupBaseName builds the dated bundle filename for a manual export.
func appBackupBaseName(t time.Time) string {
	return fmt.Sprintf("%s-appbackup-%s.tar", appBackupName, t.UTC().Format("20060102-150405"))
}

// ExportAppBackup writes a dated app-backup bundle into destDir and returns a summary.
func (a *App) ExportAppBackup(destDir string, includeKeys bool) (ExportResult, error) {
	if strings.TrimSpace(destDir) == "" {
		return ExportResult{}, fmt.Errorf("a destination folder is required")
	}
	// Same source-safety invariant as staging/keystores: never write into source data.
	if err := a.Store.AssertOutsideSources(destDir); err != nil {
		return ExportResult{}, err
	}
	return a.exportAppBackupTo(filepath.Join(destDir, appBackupBaseName(time.Now())), includeKeys)
}

// ---- restore -------------------------------------------------------------

// readTarMembers reads every member of a tar into memory, keyed by name.
func readTarMembers(tarPath string) (map[string][]byte, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a readable tar (%s): %w", filepath.Base(tarPath), err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out[hdr.Name] = b
	}
	return out, nil
}

// verifyAppBackup validates a bundle WITHOUT touching the data dir: the whole-tar
// sidecar (if present), that it's a well-formed app-backup, the schema gate, and
// every member hash against the manifest. Returns the parsed manifest and members.
func verifyAppBackup(tarPath string) (appBackupManifest, map[string][]byte, error) {
	var man appBackupManifest

	// Whole-tar sidecar check (if the sidecar is present).
	if sc, err := os.ReadFile(tarPath + ".sha256"); err == nil {
		want := strings.TrimSpace(string(sc))
		if i := strings.IndexAny(want, " \t"); i >= 0 {
			want = want[:i]
		}
		got, err := hashFileHex(tarPath)
		if err != nil {
			return man, nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(want), got) {
			return man, nil, fmt.Errorf("the .sha256 sidecar does not match this file — the backup is corrupted or was altered")
		}
	}

	members, err := readTarMembers(tarPath)
	if err != nil {
		return man, nil, err
	}
	manBytes, ok := members["MANIFEST.json"]
	if !ok {
		return man, nil, fmt.Errorf("this is not a Mnemosyne app backup (no MANIFEST.json)")
	}
	if err := json.Unmarshal(manBytes, &man); err != nil {
		return man, nil, fmt.Errorf("MANIFEST.json is unreadable: %w", err)
	}
	if man.Format != appBackupFormat {
		return man, nil, fmt.Errorf("this is not a Mnemosyne app backup (format %q)", man.Format)
	}
	// Schema gate — same rule as OpenStore: refuse a bundle a newer app created rather
	// than silently dropping fields we don't understand.
	if man.SchemaVersion > currentSchemaVersion {
		return man, nil, fmt.Errorf("this backup was created by a newer version of Mnemosyne "+
			"(catalog schema v%d; this build understands v%d). Update the app first, then restore.",
			man.SchemaVersion, currentSchemaVersion)
	}
	// Every member hash must match the manifest (tamper / corruption detection).
	for _, m := range man.Members {
		b, ok := members[m.Name]
		if !ok {
			return man, nil, fmt.Errorf("backup is incomplete — member %q is missing", m.Name)
		}
		if got := sha256Hex(b); !strings.EqualFold(got, m.SHA256) {
			return man, nil, fmt.Errorf("integrity check failed on %q — the backup is corrupted or was altered", m.Name)
		}
		if int64(len(b)) != m.Size {
			return man, nil, fmt.Errorf("integrity check failed on %q — size mismatch", m.Name)
		}
	}
	return man, members, nil
}

// InspectAppBackup validates a bundle read-only and returns its manifest, powering a
// pre-restore preview.
func (a *App) InspectAppBackup(tarPath string) (appBackupManifest, error) {
	man, _, err := verifyAppBackup(tarPath)
	return man, err
}

// RestoreAppBackup verifies a bundle, backs up the current records, then replaces the
// data-dir files and reopens the store (migrating an older backup forward). All
// verification happens before ANYTHING is written, so a bad backup leaves the current
// data dir untouched.
func (a *App) RestoreAppBackup(tarPath string) (RestoreResult, error) {
	man, members, err := verifyAppBackup(tarPath)
	if err != nil {
		return RestoreResult{}, err
	}

	// Preserve the current machine's auth token when the backup's is blank (we scrub on
	// export) — never lock a running deployment out of its own API.
	curToken := a.LoadConfig().AuthToken

	// (4) Back up the current records first, so the restore is itself reversible.
	stamp := time.Now().UTC().Format("20060102-150405")
	preDir := filepath.Join(a.DataDir, "pre-restore-"+stamp)
	if err := os.MkdirAll(preDir, 0o755); err != nil {
		return RestoreResult{}, fmt.Errorf("prepare pre-restore backup: %w", err)
	}
	for _, name := range []string{"catalog.json", "config.json", "jobs.json", "formats.json"} {
		src := filepath.Join(a.DataDir, name)
		if b, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(filepath.Join(preDir, name), b, 0o644)
		}
	}

	// (5) Extract members (except the manifest + config, handled below). Keystores land
	// in <data>/keystores/ and the restored config is repointed at them.
	writeFile := func(dest string, b []byte) error {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		tmp := dest + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		return atomicRename(tmp, dest)
	}
	var restoredKeystores []string
	for _, m := range man.Members {
		b := members[m.Name]
		switch {
		case m.Name == "config.json":
			continue // handled last, after keystore paths are known
		case strings.HasPrefix(m.Name, "keystores/"):
			dest := filepath.Join(a.DataDir, "keystores", filepath.Base(m.Name))
			if err := writeFile(dest, b); err != nil {
				return RestoreResult{}, fmt.Errorf("restore %s: %w", m.Name, err)
			}
			restoredKeystores = append(restoredKeystores, dest)
		default: // catalog.json, jobs.json, formats.json
			if err := writeFile(filepath.Join(a.DataDir, m.Name), b); err != nil {
				return RestoreResult{}, fmt.Errorf("restore %s: %w", m.Name, err)
			}
		}
	}

	// Config, patched: keep the current auth token if the backup's is blank, and point
	// KeystorePaths at any keystores we just restored.
	if cfgBytes, ok := members["config.json"]; ok {
		var cfg Config
		if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
			return RestoreResult{}, fmt.Errorf("restored config is unreadable: %w", err)
		}
		if cfg.AuthToken == "" {
			cfg.AuthToken = curToken
		}
		if len(restoredKeystores) > 0 {
			cfg.KeystorePaths = restoredKeystores
		}
		out, _ := json.MarshalIndent(cfg, "", "  ")
		if err := writeFile(a.configPath(), out); err != nil {
			return RestoreResult{}, fmt.Errorf("restore config.json: %w", err)
		}
	}

	// (6) Reopen the store from the restored files — this runs the migration registry,
	// bringing an older backup forward — and swap it in.
	ns, err := OpenStore(a.DataDir)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("reopen catalog after restore: %w", err)
	}
	a.Store = ns

	// (7) Summary.
	res := RestoreResult{
		SchemaVersion: man.SchemaVersion, AppVersion: man.AppVersion, Created: man.Created,
		IncludedKeys: man.IncludesKeys, PreRestoreDir: preDir,
		Archives: len(ns.Collections()), Files: len(ns.AllFiles()),
		Volumes: len(ns.Volumes()), Jobs: len(ns.Jobs()),
	}
	for _, c := range ns.Collections() {
		chunks := ns.Chunks(c.ID)
		res.Packages += len(chunks)
		for _, ch := range chunks {
			res.VerifyEvents += len(ch.VerifyEvents)
		}
	}
	if ro, why := ns.ReadOnly(); ro {
		res.ReadOnly, res.ReadOnlyWhy = true, why
	}
	return res, nil
}

// ---- auto-export ---------------------------------------------------------

// autoExportPeriodName returns the per-period bundle filename for a cadence, so the
// ticker can tell "have I already written this period's backup?" without extra state
// (mirroring the daily-backup once-per-day check).
func autoExportPeriodName(cadence string, t time.Time) (string, bool) {
	t = t.UTC()
	switch strings.ToLower(strings.TrimSpace(cadence)) {
	case "daily":
		return fmt.Sprintf("%s-appbackup-%s.tar", appBackupName, t.Format("20060102")), true
	case "weekly":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%s-appbackup-%04d-W%02d.tar", appBackupName, y, w), true
	default:
		return "", false // "off" or unknown → no auto-export
	}
}

// autoExportCadenceLabel renders a cadence for display ("daily"/"weekly"/"off").
func autoExportCadenceLabel(cadence string) string {
	switch strings.ToLower(strings.TrimSpace(cadence)) {
	case "daily":
		return "every day"
	case "weekly":
		return "every week"
	default:
		return "off"
	}
}

// maybeAutoExport writes one app backup for the current period if a cadence is set
// and this period's file is not already present. Keys are never included in an
// automated export. Best-effort: returns nil when there is nothing to do.
func (a *App) maybeAutoExport(now time.Time) error {
	cfg := a.LoadConfig()
	dir := strings.TrimSpace(cfg.AutoExportDir)
	if dir == "" {
		return nil
	}
	name, on := autoExportPeriodName(cfg.AutoExportCadence, now)
	if !on {
		return nil
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err == nil {
		return nil // already written this period
	}
	if err := a.Store.AssertOutsideSources(dir); err != nil {
		return err
	}
	_, err := a.exportAppBackupTo(target, false)
	return err
}
