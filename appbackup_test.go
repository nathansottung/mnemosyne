package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// App-state backup: a one-file bundle that carries the whole brain (catalog, config,
// job history with artifacts, format overrides) to a new machine. These tests need
// no native toolchain — export/restore never shell out — so they always run.

// abTestApp builds an App on a fresh temp data dir with a temp staging + keystores,
// without requiring native tools (unlike newTestApp).
func abTestApp(t *testing.T) *App {
	t.Helper()
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	app := &App{DataDir: dataDir, Store: store}
	ks := filepath.Join(t.TempDir(), "keystore1.json")
	if err := writeStore(ks, &keystoreFile{Marker: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveConfig(map[string]any{
		"staging_dir":    t.TempDir(),
		"keystore_paths": []string{ks},
		"auth_token":     "secret-token-should-not-travel",
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	return app
}

// abPopulate gives a data dir some real state: an archive with scanned files, a
// volume with a serial, a package with a verified copy (verify history), and a job
// with an artifact. Returns the recovered-count expectations.
func abPopulate(t *testing.T, app *App) (archives, files, volumes, packages, verifyEvents, jobs int) {
	t.Helper()
	src := t.TempDir()
	for rel, data := range map[string]string{"a.txt": "alpha", "sub/b.txt": "bravo bytes"} {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	coll := app.Store.AddCollection("Backup Me")
	n, err := app.ScanFolder(coll.ID, src, func(float64, string) {})
	if err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}

	vol := app.Store.AddVolume(Volume{Label: "AB-VOL", Kind: "HDD", Serial: "SERIAL-XYZ-123"})
	catFiles := app.Store.FilesOf(coll.ID)
	refs := make([]ChunkFileRef, 0, len(catFiles))
	for _, f := range catFiles {
		refs = append(refs, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash})
	}
	ch := app.Store.AddChunk(Chunk{
		Name: "AB-PKG", Status: "VERIFIED", CollectionID: coll.ID, MediaKind: "HDD",
		HashAlg: "SHA256", FileCount: len(refs), Files: refs,
	})
	app.Store.RecordCopy(ch, vol.ID, "H:/AB-PKG", true)
	app.Store.AppendVerifyEvent(ch, VerifyEvent{At: time.Now().UTC(), OK: true, Level: "B", Note: "campaign"})

	j := app.Store.NewJob("scan", "Scan "+src)
	app.Store.AppendJobArtifact(j.ID, Artifact{Kind: "catalog", Label: "2 files cataloged", Count: n})
	app.Store.SetJob(j.ID, 1, "", "COMPLETED")

	// Recount verify events the way RestoreResult does (per chunk).
	ve := 0
	for _, c := range app.Store.Chunks(coll.ID) {
		ve += len(c.VerifyEvents)
	}
	return len(app.Store.Collections()), len(app.Store.AllFiles()),
		len(app.Store.Volumes()), len(app.Store.Chunks(coll.ID)), ve, len(app.Store.Jobs())
}

func readManifest(t *testing.T, tarPath string) appBackupManifest {
	t.Helper()
	members, err := readTarMembers(tarPath)
	if err != nil {
		t.Fatalf("readTarMembers: %v", err)
	}
	var man appBackupManifest
	if err := json.Unmarshal(members["MANIFEST.json"], &man); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return man
}

func memberNames(man appBackupManifest) map[string]bool {
	m := map[string]bool{}
	for _, mm := range man.Members {
		m[mm.Name] = true
	}
	return m
}

// TestAppBackup_ExportRestoreRoundTrip is the core: export from one data dir, restore
// into a fresh one, and confirm the recovered counts + verify history + job artifacts
// match. Also confirms keystores are excluded by default and the auth token is scrubbed.
func TestAppBackup_ExportRestoreRoundTrip(t *testing.T) {
	src := abTestApp(t)
	wantArch, wantFiles, wantVols, wantPkgs, wantVE, wantJobs := abPopulate(t, src)

	dest := t.TempDir()
	res, err := src.ExportAppBackup(dest, false)
	if err != nil {
		t.Fatalf("ExportAppBackup: %v", err)
	}
	if _, err := os.Stat(res.TarPath); err != nil {
		t.Fatalf("tar not written: %v", err)
	}
	if _, err := os.Stat(res.SidecarPath); err != nil {
		t.Fatalf(".sha256 sidecar not written: %v", err)
	}

	man := readManifest(t, res.TarPath)
	names := memberNames(man)
	for _, need := range []string{"catalog.json", "config.json", "jobs.json"} {
		if !names[need] {
			t.Errorf("manifest missing expected member %q", need)
		}
	}
	// Keystore exclusion honored by default.
	for n := range names {
		if len(n) >= 10 && n[:10] == "keystores/" {
			t.Errorf("default export must NOT include keystores, found %q", n)
		}
	}
	// Auth token scrubbed in the exported config member.
	members, _ := readTarMembers(res.TarPath)
	var expCfg Config
	_ = json.Unmarshal(members["config.json"], &expCfg)
	if expCfg.AuthToken != "" {
		t.Errorf("exported config must scrub auth_token, got %q", expCfg.AuthToken)
	}

	// Restore into a completely fresh data dir.
	dst := abTestApp(t)
	// give the target a DIFFERENT auth token to prove it's preserved (incoming is blank)
	dstToken := dst.LoadConfig().AuthToken
	rr, err := dst.RestoreAppBackup(res.TarPath)
	if err != nil {
		t.Fatalf("RestoreAppBackup: %v", err)
	}
	if rr.Archives != wantArch || rr.Files != wantFiles || rr.Volumes != wantVols ||
		rr.Packages != wantPkgs || rr.Jobs != wantJobs {
		t.Errorf("recovered counts mismatch: got %+v; want arch=%d files=%d vols=%d pkgs=%d jobs=%d",
			rr, wantArch, wantFiles, wantVols, wantPkgs, wantJobs)
	}
	if rr.VerifyEvents != wantVE {
		t.Errorf("verify history mismatch: got %d, want %d", rr.VerifyEvents, wantVE)
	}
	// Job artifacts survived.
	var jobArts int
	for _, j := range dst.Store.Jobs() {
		jobArts += len(j.Artifacts)
	}
	if jobArts == 0 {
		t.Error("restored jobs lost their artifacts")
	}
	// Volume serial intact (this is what makes auto-reconnect work).
	if dst.Store.VolumeBySerial("SERIAL-XYZ-123") == nil {
		t.Error("restored volume lost its serial — reconnect-by-serial would break")
	}
	// Auth token preserved (backup's was blank → keep the machine's own).
	if dst.LoadConfig().AuthToken != dstToken {
		t.Errorf("restore should preserve the current machine's auth token; got %q want %q",
			dst.LoadConfig().AuthToken, dstToken)
	}
	// A pre-restore backup was made.
	if _, err := os.Stat(rr.PreRestoreDir); err != nil {
		t.Errorf("pre-restore backup dir missing: %v", err)
	}
}

// TestAppBackup_IncludeKeys proves the opt-in includes keystore members.
func TestAppBackup_IncludeKeys(t *testing.T) {
	app := abTestApp(t)
	abPopulate(t, app)
	dest := t.TempDir()
	res, err := app.ExportAppBackup(dest, true)
	if err != nil {
		t.Fatalf("ExportAppBackup(includeKeys): %v", err)
	}
	man := readManifest(t, res.TarPath)
	found := false
	for n := range memberNames(man) {
		if len(n) >= 10 && n[:10] == "keystores/" {
			found = true
		}
	}
	if !found {
		t.Error("include_keys export must contain a keystores/ member")
	}
	if !man.IncludesKeys {
		t.Error("manifest should record includes_keys=true")
	}
}

// TestAppBackup_TamperedMemberRefused proves a member altered after export fails the
// import hash check, and the target data dir is left untouched.
func TestAppBackup_TamperedMemberRefused(t *testing.T) {
	src := abTestApp(t)
	abPopulate(t, src)
	dest := t.TempDir()
	res, err := src.ExportAppBackup(dest, false)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Rebuild the tar with catalog.json bytes flipped (manifest hash no longer matches).
	members, err := readTarMembers(res.TarPath)
	if err != nil {
		t.Fatal(err)
	}
	members["catalog.json"] = append(members["catalog.json"], []byte(" tampered")...)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// MANIFEST first (unchanged), then the rest.
	writeM := func(name string) {
		b := members[name]
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Format: tar.FormatPAX})
		_, _ = tw.Write(b)
	}
	writeM("MANIFEST.json")
	for name := range members {
		if name != "MANIFEST.json" {
			writeM(name)
		}
	}
	_ = tw.Close()
	badTar := filepath.Join(dest, "bad.tar")
	if err := os.WriteFile(badTar, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := abTestApp(t)
	before := len(dst.Store.AllFiles())
	if _, err := dst.RestoreAppBackup(badTar); err == nil {
		t.Fatal("restore must REFUSE a tampered member")
	}
	if after := len(dst.Store.AllFiles()); after != before {
		t.Errorf("a refused restore must leave the target untouched: files %d → %d", before, after)
	}
}

// TestAppBackup_NewerSchemaRefused proves a bundle from a newer app is refused with
// the "update the app" message, before touching the data dir.
func TestAppBackup_NewerSchemaRefused(t *testing.T) {
	src := abTestApp(t)
	abPopulate(t, src)
	dest := t.TempDir()
	res, err := src.ExportAppBackup(dest, false)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the manifest with a schema version one above what this build understands.
	members, err := readTarMembers(res.TarPath)
	if err != nil {
		t.Fatal(err)
	}
	var man appBackupManifest
	_ = json.Unmarshal(members["MANIFEST.json"], &man)
	man.SchemaVersion = currentSchemaVersion + 1
	nm, _ := json.MarshalIndent(man, "", "  ")
	members["MANIFEST.json"] = nm
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeM := func(name string) {
		b := members[name]
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Format: tar.FormatPAX})
		_, _ = tw.Write(b)
	}
	writeM("MANIFEST.json")
	for name := range members {
		if name != "MANIFEST.json" {
			writeM(name)
		}
	}
	_ = tw.Close()
	newerTar := filepath.Join(dest, "newer.tar")
	if err := os.WriteFile(newerTar, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// No sidecar for this hand-built tar, so the whole-tar check is skipped and the
	// schema gate is what must fire.
	_, err = src.RestoreAppBackup(newerTar)
	if err == nil {
		t.Fatal("restore must refuse a newer-schema backup")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("Update the app")) {
		t.Errorf("refusal should tell the user to update the app; got: %v", err)
	}
}
