package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The "Seeing what happened" feature: every job records the artifacts it produced
// (persisted across restarts), and the Explorer's Validation coloring reflects the
// catalog's verify state. These tests exercise a scan end-to-end through the HTTP
// layer (so the runJob→artifact wiring is real), the validation treemap, the
// jobs.json sidecar, and the interrupted-job reconcile — none of which need the
// native tar/gpg/par2 toolchain, so they always run. A separate build test is
// guarded by nativeTools.

// swhHarness is a minimal HTTP test server around a real App (no tool dependency).
type swhHarness struct {
	t       *testing.T
	url     string
	app     *App
	dataDir string
}

func newSWH(t *testing.T) *swhHarness {
	t.Helper()
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	app := &App{DataDir: dataDir, Store: store}
	mux := http.NewServeMux()
	api(mux, app)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &swhHarness{t: t, url: ts.URL, app: app, dataDir: dataDir}
}

func (h *swhHarness) obj(method, path string, body any) map[string]any {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.url+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	m["_status"] = float64(resp.StatusCode)
	return m
}

// waitJob polls GET /api/jobs/{id} until the job finishes, returning its record.
func (h *swhHarness) waitJob(jobID int) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j := h.obj("GET", fmt.Sprintf("/api/jobs/%d", jobID), nil)
		switch j["status"] {
		case "COMPLETED":
			return j
		case "FAILED", "INTERRUPTED":
			h.t.Fatalf("job %d ended %v: %v", jobID, j["status"], j["label"])
		}
		time.Sleep(30 * time.Millisecond)
	}
	h.t.Fatalf("job %d timed out", jobID)
	return nil
}

func swhSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, data := range map[string]string{
		"a.txt":     "hello world",
		"sub/b.txt": "second file here",
		"sub/c.bin": "\x00\x01\x02 binary bytes",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// artifactsOf pulls the typed artifact list out of a job JSON map.
func artifactsOf(t *testing.T, job map[string]any) []Artifact {
	t.Helper()
	raw, _ := json.Marshal(job["artifacts"])
	var arts []Artifact
	if err := json.Unmarshal(raw, &arts); err != nil {
		t.Fatalf("decode artifacts: %v", err)
	}
	return arts
}

// TestSeeingWhatHappened_ScanArtifactsAndValidation proves a scan records a catalog
// artifact that points into the Explorer, that the artifact survives a restart via
// jobs.json, and that the Validation treemap coloring follows the catalog's verify
// state (hashed → verified once a verified copy exists).
func TestSeeingWhatHappened_ScanArtifactsAndValidation(t *testing.T) {
	h := newSWH(t)
	src := swhSource(t)

	coll := h.obj("POST", "/api/collections", map[string]any{"name": "SWH"})
	cid := int(coll["id"].(float64))

	scan := h.obj("POST", fmt.Sprintf("/api/collections/%d/scan", cid), map[string]any{"path": src})
	jid := int(scan["job_id"].(float64))
	job := h.waitJob(jid)

	// (1) The scan job carries a catalog artifact scoped for "View results".
	arts := artifactsOf(t, job)
	if len(arts) != 1 {
		t.Fatalf("scan should record exactly one artifact, got %d: %+v", len(arts), arts)
	}
	a := arts[0]
	if a.Kind != "catalog" {
		t.Errorf("artifact kind = %q, want catalog", a.Kind)
	}
	if a.ShowView != "explore" || a.ShowID != cid {
		t.Errorf("Show target = %s/%d, want explore/%d", a.ShowView, a.ShowID, cid)
	}
	if a.ShowPath != src {
		t.Errorf("Show path = %q, want scanned root %q", a.ShowPath, src)
	}
	if a.Count != 3 {
		t.Errorf("artifact count = %d, want 3 files cataloged", a.Count)
	}

	// (2) Before any copy is verified, every file is HASHED (neutral), none VERIFIED.
	pre := h.app.Store.Treemap(cid, "", "validation")
	if pre.ColorBy != "validation" {
		t.Fatalf("treemap color_by = %q, want validation", pre.ColorBy)
	}
	if pre.StatusBytes["HASHED"] <= 0 {
		t.Errorf("pre-verify: expected HASHED bytes > 0, got %v", pre.StatusBytes)
	}
	if pre.StatusBytes["VERIFIED"] != 0 {
		t.Errorf("pre-verify: expected no VERIFIED bytes, got %d", pre.StatusBytes["VERIFIED"])
	}

	// Give the files a verified copy: a package covering them, on a volume, verified OK.
	files := h.app.Store.FilesOf(cid)
	if len(files) != 3 {
		t.Fatalf("expected 3 cataloged files, got %d", len(files))
	}
	vol := h.app.Store.AddVolume(Volume{Label: "SWH-VOL", Kind: "HDD", Location: "shelf"})
	refs := make([]ChunkFileRef, 0, len(files))
	for _, f := range files {
		refs = append(refs, ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash})
	}
	ch := h.app.Store.AddChunk(Chunk{
		Name: "SWH-PKG", Status: "VERIFIED", CollectionID: cid, MediaKind: "HDD",
		HashAlg: "SHA256", FileCount: len(refs), Files: refs,
	})
	h.app.Store.RecordCopy(ch, vol.ID, "H:/SWH-PKG", true)

	// (3) Now the same files color VERIFIED (green), matching the catalog verify state.
	post := h.app.Store.Treemap(cid, "", "validation")
	if post.StatusBytes["VERIFIED"] <= 0 {
		t.Errorf("post-verify: expected VERIFIED bytes > 0, got %v", post.StatusBytes)
	}
	if post.StatusBytes["HASHED"] != 0 {
		t.Errorf("post-verify: expected no HASHED bytes once verified, got %v", post.StatusBytes)
	}

	// (4) Persistence: reopening the store recovers the scan job + its artifacts.
	store2, err := OpenStore(h.dataDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var found *Job
	for _, j := range store2.Jobs() {
		if j.ID == jid {
			found = j
		}
	}
	if found == nil {
		t.Fatalf("scan job %d did not survive restart", jid)
	}
	if found.Status != "COMPLETED" {
		t.Errorf("reopened job status = %q, want COMPLETED", found.Status)
	}
	if len(found.Artifacts) != 1 || found.Artifacts[0].Kind != "catalog" {
		t.Errorf("reopened job lost its artifacts: %+v", found.Artifacts)
	}
}

// TestSeeingWhatHappened_InterruptedReconcile proves a job still marked RUNNING when
// the process exits is picked up as INTERRUPTED on the next open (not lost, not
// silently completed).
func TestSeeingWhatHappened_InterruptedReconcile(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	j := s1.NewJob("scan", "Scan /somewhere") // left RUNNING (goroutine "dies")
	s1.AppendJobArtifact(j.ID, Artifact{Kind: "catalog", Label: "partial", Count: 5})

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := s2.Job(j.ID)
	if got == nil {
		t.Fatalf("interrupted job %d was lost on restart", j.ID)
	}
	if got.Status != "INTERRUPTED" {
		t.Errorf("status = %q, want INTERRUPTED", got.Status)
	}
	if len(got.Artifacts) != 1 {
		t.Errorf("partial artifacts should be preserved, got %+v", got.Artifacts)
	}
}

// TestSeeingWhatHappened_BuildArtifact proves a package build records the staged
// package as an artifact whose Show action opens the package. Needs the native
// toolchain, so it skips where tar/gpg/par2 are unavailable.
func TestSeeingWhatHappened_BuildArtifact(t *testing.T) {
	s := newIT(t) // t.Skips if native tools are missing
	s.setConfig(nil)
	src := s.makeSource(map[string][]byte{"one.txt": []byte("alpha"), "two.txt": []byte("beta")})

	coll := s.obj("POST", "/api/collections", map[string]any{"name": "SWH-BUILD"})
	cid := int(coll["id"].(float64))
	s.job(s.obj("POST", fmt.Sprintf("/api/collections/%d/scan", cid), map[string]any{"path": src}))
	plan := s.obj("POST", "/api/plan", map[string]any{
		"collection_id": cid, "media_kind": "CUSTOM", "target_gb": 1.0, "encrypted": false, "par2": 5,
	})
	pkgs, ok := plan["chunks_created"].([]any)
	if !ok || len(pkgs) == 0 {
		t.Fatalf("plan created no packages: %v", plan)
	}
	pid := int(pkgs[0].(map[string]any)["id"].(float64))

	build := s.obj("POST", fmt.Sprintf("/api/chunks/%d/build", pid), nil)
	bjid := int(build["job_id"].(float64))
	s.job(build) // wait for completion

	job := s.obj("GET", fmt.Sprintf("/api/jobs/%d", bjid), nil)
	arts := artifactsOf(t, job)
	var pkg *Artifact
	for i := range arts {
		if arts[i].Kind == "package" {
			pkg = &arts[i]
		}
	}
	if pkg == nil {
		t.Fatalf("build job recorded no package artifact: %+v", arts)
	}
	if pkg.ShowView != "packages" || pkg.ShowID != pid {
		t.Errorf("package Show target = %s/%d, want packages/%d", pkg.ShowView, pkg.ShowID, pid)
	}
	if pkg.Path == "" {
		t.Error("package artifact should carry its staged path")
	}
}
