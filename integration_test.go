package main

// integration_test.go — the manual audit, frozen as tests. Each case spins up
// the REAL HTTP API in-process on a random port (httptest) against fresh temp
// dirs, then drives it exactly as the browser would: config → scan → plan →
// build → write → verify → restore. The custody chain (source hash → tar → par2
// → payload → read-back) is asserted end-to-end so it can never silently
// regress. Every case t.Skips cleanly when tar/gpg/par2 are absent (via
// nativeTools), so `go test ./...` is green on a bare machine and exercised for
// real on CI (which apt-installs gnupg + par2).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- in-process server harness ----------------------------------------

type itServer struct {
	t                *testing.T
	url              string
	app              *App
	dataDir, staging string
	tools            map[string]string
}

func newIT(t *testing.T) *itServer {
	t.Helper()
	tools := nativeTools(t) // t.Skips if tar/gpg/par2 are unavailable
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
	return &itServer{t: t, url: ts.URL, app: app, dataDir: dataDir, staging: t.TempDir(), tools: tools}
}

func (s *itServer) do(method, path string, body any) (int, []byte) {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.url+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// obj issues a request expecting a JSON object; the HTTP status is stashed at "_status".
func (s *itServer) obj(method, path string, body any) map[string]any {
	code, b := s.do(method, path, body)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	m["_status"] = float64(code)
	return m
}

func (s *itServer) arr(method, path string, body any) []any {
	_, b := s.do(method, path, body)
	var a []any
	_ = json.Unmarshal(b, &a)
	return a
}

func (s *itServer) status(m map[string]any) int { return int(m["_status"].(float64)) }

// job waits for the background job named in a runJob response to finish.
func (s *itServer) job(m map[string]any) {
	s.t.Helper()
	jid, ok := m["job_id"].(float64)
	if !ok {
		s.t.Fatalf("expected a job response, got: %v", m)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range s.arr("GET", "/api/jobs", nil) {
			jm := j.(map[string]any)
			if jm["id"].(float64) == jid {
				switch jm["status"] {
				case "COMPLETED":
					return
				case "FAILED":
					s.t.Fatalf("job failed: %v", jm["label"])
				}
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	s.t.Fatalf("job %v timed out", jid)
}

func (s *itServer) setConfig(extra map[string]any) {
	s.t.Helper()
	cfg := map[string]any{"staging_dir": s.staging, "par2_redundancy": 10, "tools": s.tools}
	for k, v := range extra {
		cfg[k] = v
	}
	if m := s.obj("PUT", "/api/config", cfg); s.status(m) != 200 {
		s.t.Fatalf("config PUT failed: %v", m)
	}
}

func (s *itServer) makeKeystores(n int) []string {
	s.t.Helper()
	dir := s.t.TempDir()
	var paths []string
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("keystore%d.json", i))
		if err := writeStore(p, &keystoreFile{Marker: 1}); err != nil {
			s.t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func (s *itServer) makeSource(files map[string][]byte) string {
	s.t.Helper()
	dir := s.t.TempDir()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	return dir
}

// scanPlanBuild runs the common front of the pipeline and returns the first
// package created by the plan.
func (s *itServer) scanPlanBuild(src string, targetGB float64, encrypted bool, par2 int) map[string]any {
	s.t.Helper()
	coll := s.obj("POST", "/api/collections", map[string]any{"name": "IT"})
	cid := int(coll["id"].(float64))
	s.job(s.obj("POST", fmt.Sprintf("/api/collections/%d/scan", cid), map[string]any{"path": src}))
	plan := s.obj("POST", "/api/plan", map[string]any{
		"collection_id": cid, "media_kind": "CUSTOM", "target_gb": targetGB,
		"encrypted": encrypted, "par2": par2,
	})
	pkgs, ok := plan["chunks_created"].([]any)
	if !ok || len(pkgs) == 0 {
		s.t.Fatalf("plan created no packages: %v", plan)
	}
	pkg := pkgs[0].(map[string]any)
	pid := int(pkg["id"].(float64))
	s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/build", pid), nil))
	return s.obj("GET", fmt.Sprintf("/api/chunks/%d", pid), nil)
}

func (s *itServer) addVolume(label, loc string) int {
	m := s.obj("POST", "/api/volumes", map[string]any{"label": label, "kind": "HDD", "location": loc})
	return int(m["id"].(float64))
}

// ---- shared assertions -------------------------------------------------

func assertTreeMatches(t *testing.T, src, out string) {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		want, _ := hashFileHex(p)
		got, herr := hashFileHex(filepath.Join(out, rel))
		if herr != nil {
			t.Errorf("restored file missing: %s (%v)", rel, herr)
			return nil
		}
		if want != got {
			t.Errorf("restored %s hash mismatch: want %s got %s", rel, want, got)
		}
		n++
		return nil
	})
	if n == 0 {
		t.Fatal("no source files were compared")
	}
}

// corruptRun flips a run of bytes near the middle of a file — enough to trip a
// hash check, few enough that par2 (with sufficient redundancy) can repair it.
func corruptRun(t *testing.T, path string, n int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < n+8 {
		t.Fatalf("file %s too small (%d) to corrupt", path, len(b))
	}
	start := len(b)/2 - n/2
	for i := 0; i < n; i++ {
		b[start+i] ^= 0xFF
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- (1) keystore enforcement -----------------------------------------

func TestIntegration_KeystoreEnforcement(t *testing.T) {
	s := newIT(t)
	ks := s.makeKeystores(2)

	s.setConfig(map[string]any{"keystore_paths": ks[:1]})
	m := s.obj("POST", "/api/keys", map[string]any{"note": "solo"})
	if s.status(m) == 200 {
		t.Fatalf("key generation must fail with 1 keystore, got: %v", m)
	}

	s.setConfig(map[string]any{"keystore_paths": ks})
	m = s.obj("POST", "/api/keys", map[string]any{"note": "pair"})
	if s.status(m) != 200 {
		t.Fatalf("key generation must succeed with 2 keystores, got: %v", m)
	}
	if _, ok := m["key_ref"].(string); !ok {
		t.Fatalf("expected a key_ref in response: %v", m)
	}
}

// ---- (2) full chain: build → write → corrupt → verify-fails → par2 repair on restore → hashes match

func TestIntegration_FullChainCorruptRepairRestore(t *testing.T) {
	s := newIT(t)
	s.setConfig(map[string]any{"keystore_paths": s.makeKeystores(2)})
	src := s.makeSource(map[string][]byte{
		"a.txt":      bytes.Repeat([]byte("the quick brown fox\n"), 2000),
		"sub/b.bin":  bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 5000),
		"deep/c.dat": []byte("a third, small file\n"),
	})
	// High redundancy so the corruption is comfortably repairable on tiny data.
	c := s.scanPlanBuild(src, 1, true, 40)
	pid := int(c["id"].(float64))
	name := c["name"].(string)

	dest := t.TempDir()
	vid := s.addVolume("ARCH-A", "lab")
	s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/write", pid), map[string]any{"dest_dir": dest, "volume_id": vid}))

	// Corrupt the written ciphertext payload on the medium.
	payload := filepath.Join(dest, name, name+".tar.gpg")
	corruptRun(t, payload, 24)

	// Verify must now report failure (copy-level), but not destroy the package.
	vr := s.obj("POST", fmt.Sprintf("/api/chunks/%d/verify", pid), map[string]any{"dest_dir": dest})
	if ok, _ := vr["verify_ok"].(bool); ok {
		t.Fatalf("verify should fail on corrupted payload: %v", vr)
	}

	// Restore must auto-repair via par2, then decrypt+extract cleanly.
	out := t.TempDir()
	s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/restore", pid), map[string]any{
		"source_dir": filepath.Join(dest, name), "output_dir": out,
	}))
	assertTreeMatches(t, src, out)
}

// ---- (3) plaintext package: no keystores consulted, no gpg in RESTORE.txt, payload named <name>.tar

func TestIntegration_PlaintextPackage(t *testing.T) {
	s := newIT(t)
	// Deliberately NO keystore_paths configured — a plaintext build must not need them.
	s.setConfig(nil)
	src := s.makeSource(map[string][]byte{"note.txt": []byte("plaintext, no key needed\n")})
	c := s.scanPlanBuild(src, 1, false, 10)
	name := c["name"].(string)
	staged := c["staged_dir"].(string)
	if staged == "" {
		t.Fatal("expected a staged_dir after build")
	}

	// Payload named per the plaintext scheme: <name>.tar, never <name>.tar.gpg.
	if _, err := os.Stat(filepath.Join(staged, name+".tar")); err != nil {
		t.Errorf("expected plaintext payload %s.tar: %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(staged, name+".tar.gpg")); err == nil {
		t.Errorf("plaintext package must NOT produce %s.tar.gpg", name)
	}

	rt, err := os.ReadFile(filepath.Join(staged, "RESTORE.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// No actual decrypt step: no gpg command, no ciphertext filename. (The prose
	// "no gpg step" is fine; we check for a real `gpg -d`/`gpg --` invocation.)
	for _, forbidden := range []string{"gpg -d", "gpg --", ".tar.gpg"} {
		if strings.Contains(string(rt), forbidden) {
			t.Errorf("plaintext RESTORE.txt must not contain %q:\n%s", forbidden, rt)
		}
	}
	if !strings.Contains(string(rt), "tar -xf "+name+".tar") {
		t.Errorf("plaintext RESTORE.txt should extract %s.tar directly:\n%s", name, rt)
	}
}

// ---- (4) privacy mode: no plaintext manifest on the medium; read-medium-manifest decrypts it back

func TestIntegration_PrivacyMode(t *testing.T) {
	s := newIT(t)
	s.setConfig(map[string]any{"keystore_paths": s.makeKeystores(2), "private_media": true})
	src := s.makeSource(map[string][]byte{"secret/location.txt": []byte("filenames must not leak\n")})
	c := s.scanPlanBuild(src, 1, true, 10)
	pid := int(c["id"].(float64))
	name := c["name"].(string)

	dest := t.TempDir()
	vid := s.addVolume("PRIV-1", "vault")
	s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/write", pid), map[string]any{"dest_dir": dest, "volume_id": vid}))

	pkgDir := filepath.Join(dest, name)
	if _, err := os.Stat(filepath.Join(pkgDir, name+".manifest.json")); err == nil {
		t.Errorf("private media must NOT write a plaintext %s.manifest.json to the medium", name)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, name+".manifest.json.gpg")); err != nil {
		t.Errorf("private media must write the encrypted %s.manifest.json.gpg: %v", name, err)
	}

	// The catalog can still recover the listing from the medium by decrypting.
	m := s.obj("POST", fmt.Sprintf("/api/chunks/%d/read-medium-manifest", pid), map[string]any{"mount": dest})
	if s.status(m) != 200 {
		t.Fatalf("read-medium-manifest failed: %v", m)
	}
	if enc, _ := m["_encrypted"].(bool); !enc {
		t.Errorf("expected the recovered manifest to be marked encrypted: %v", m)
	}
	if files, ok := m["files"].([]any); !ok || len(files) == 0 {
		t.Errorf("decrypted manifest should list files: %v", m["files"])
	}
}

// ---- (5) spanning: ≥3 segments across separate destinations, state persists over reopen, rejoin restores

func TestIntegration_Spanning(t *testing.T) {
	s := newIT(t)
	s.setConfig(nil) // plaintext spanning — no keystores needed
	// One file large enough to force several segments at a tiny media target.
	big := bytes.Repeat([]byte("mnemosyne-spanning-payload-"), 8000) // ~216 KB
	src := s.makeSource(map[string][]byte{"big.dat": big})
	// Tiny target (~50 KB) so the payload byte-splits into many segments.
	c := s.scanPlanBuild(src, 0.00005, false, 5)
	pid := int(c["id"].(float64))
	name := c["name"].(string)
	if spanned, _ := c["spanned"].(bool); !spanned {
		t.Fatalf("expected a spanned package, got: %v", c["spanned"])
	}

	// Write each pending segment to its OWN destination dir + volume.
	var destDirs []string
	for i := 0; i < 30; i++ {
		cur := s.obj("GET", fmt.Sprintf("/api/chunks/%d", pid), nil)
		segs, _ := cur["segments"].([]any)
		pending := -1
		for idx, sg := range segs {
			st := sg.(map[string]any)["status"]
			if st == "PENDING" || st == "FAILED" {
				pending = idx
				break
			}
		}
		if pending < 0 {
			break // all written
		}
		d := t.TempDir()
		destDirs = append(destDirs, d)
		vid := s.addVolume(fmt.Sprintf("TAPE-%d", len(destDirs)), fmt.Sprintf("shelf %d", len(destDirs)))
		s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/span-write", pid), map[string]any{"dest_dir": d, "volume_id": vid}))
	}

	final := s.obj("GET", fmt.Sprintf("/api/chunks/%d", pid), nil)
	segs := final["segments"].([]any)
	dataSegs := 0
	for _, sg := range segs {
		m := sg.(map[string]any)
		if p, _ := m["par2"].(bool); !p {
			dataSegs++
		}
		if m["status"] != "VERIFIED" {
			t.Errorf("segment %v not VERIFIED: %v", m["index"], m["status"])
		}
	}
	if dataSegs < 3 {
		t.Fatalf("expected ≥3 data segments, got %d", dataSegs)
	}
	if final["status"] != "VERIFIED" {
		t.Fatalf("spanned package should be VERIFIED once all segments are, got %v", final["status"])
	}

	// Persistence across a store close/reopen: a fresh Store reads catalog.json
	// and the segment state survives (OpenStore's recovery keeps VERIFIED segments).
	reopened, err := OpenStore(s.dataDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	var rc *Chunk
	for _, cc := range reopened.Chunks(0) {
		if cc.Name == name {
			rc = cc
		}
	}
	if rc == nil {
		t.Fatalf("package %s missing after reopen", name)
	}
	if !rc.Spanned || len(rc.Segments) != len(segs) {
		t.Fatalf("segment plan not persisted: spanned=%v segs=%d", rc.Spanned, len(rc.Segments))
	}
	for _, sg := range rc.Segments {
		if sg.Status != "VERIFIED" {
			t.Errorf("segment %d not VERIFIED after reopen: %s", sg.Index, sg.Status)
		}
	}

	// Rejoin restore: gather every tape's segment + the par2 set into one folder
	// (the manual "copy every segNNN into one dir" step), then restore.
	rejoin := t.TempDir()
	for _, d := range destDirs {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			b := filepath.Base(p)
			if strings.Contains(b, ".seg") || strings.HasSuffix(b, ".par2") {
				data, _ := os.ReadFile(p)
				_ = os.WriteFile(filepath.Join(rejoin, b), data, 0o644)
			}
			return nil
		})
	}
	out := t.TempDir()
	s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/restore", pid), map[string]any{
		"source_dir": rejoin, "output_dir": out,
	}))
	assertTreeMatches(t, src, out)
}

// ---- source-safety invariant: every writable destination inside a source root is refused

func TestIntegration_SourceSafetyRefusals(t *testing.T) {
	s := newIT(t)
	s.setConfig(nil) // staging = temp, outside any source
	// Scanning registers src as a source root; build a package to write/restore.
	src := s.makeSource(map[string][]byte{"keep.txt": []byte("precious original\n")})
	c := s.scanPlanBuild(src, 1, false, 5)
	pid := int(c["id"].(float64))
	insideSrc := filepath.Join(src, "danger", "dest")
	const msg = "Mnemosyne never writes into source data"

	refused := func(label string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected refusal, got nil error", label)
		}
		if !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), "inside source root") {
			t.Errorf("%s: refusal message wrong: %v", label, err)
		}
	}

	// (1) staging_dir inside a source root — config save refused (HTTP 400).
	cm := s.obj("PUT", "/api/config", map[string]any{"staging_dir": insideSrc, "tools": s.tools})
	if s.status(cm) == 200 {
		t.Fatalf("config with staging inside source should be refused, got: %v", cm)
	}
	if em, _ := cm["error"].(string); !strings.Contains(em, msg) {
		t.Errorf("config refusal message wrong: %v", cm["error"])
	}

	// (2) keystore path inside a source root — also refused (keystores get rewritten).
	km := s.obj("PUT", "/api/config", map[string]any{
		"staging_dir": s.staging, "keystore_paths": []string{filepath.Join(src, "keystore.json")}, "tools": s.tools,
	})
	if s.status(km) == 200 {
		t.Fatalf("config with keystore inside source should be refused, got: %v", km)
	}

	// (3) write destination inside a source root — refused before any write.
	_, werr := s.app.WriteChunk(pid, insideSrc, 0, 0, 0, 0, noProg)
	refused("write dest", werr)

	// (4) restore output inside a source root — refused before extraction.
	_, rerr := s.app.RestoreChunk(pid, "", insideSrc, nil, noProg)
	refused("restore output", rerr)

	// Sanity: the source files are untouched (the refusals happened up front).
	if b, err := os.ReadFile(filepath.Join(src, "keep.txt")); err != nil || string(b) != "precious original\n" {
		t.Errorf("source file must be pristine, got %q err=%v", b, err)
	}
}

// ---- (6) throttle: write_mbps stays at/under the cap while read_mbps exceeds it

func TestIntegration_Throttle(t *testing.T) {
	s := newIT(t)
	s.setConfig(nil)
	const mb = 16
	src := s.makeSource(map[string][]byte{"blob.dat": bytes.Repeat([]byte("x"), mb<<20)})
	c := s.scanPlanBuild(src, 1, false, 5)
	pid := int(c["id"].(float64))

	const cap = 5.0
	dest := t.TempDir()
	vid := s.addVolume("THR-1", "bench")
	// Small blocks + buffer so the throttle paces smoothly and the reader outruns it.
	s.job(s.obj("POST", fmt.Sprintf("/api/chunks/%d/write", pid), map[string]any{
		"dest_dir": dest, "volume_id": vid, "throttle_mbps": cap, "block_mb": 1, "buffer_gb": 0.05,
	}))

	final := s.obj("GET", fmt.Sprintf("/api/chunks/%d", pid), nil)
	rs, ok := final["ring_stats"].(map[string]any)
	if !ok {
		t.Fatalf("expected ring_stats after write: %v", final["ring_stats"])
	}
	readMBps := rs["read_mbps"].(float64)
	writeMBps := rs["write_mbps"].(float64)
	t.Logf("throttle cap=%.1f  read=%.1f  write=%.1f MB/s", cap, readMBps, writeMBps)
	if writeMBps > cap*1.15 {
		t.Errorf("write_mbps %.1f should be at/under the %.1f cap", writeMBps, cap)
	}
	if readMBps <= cap {
		t.Errorf("read_mbps %.1f should exceed the throttle cap %.1f (reader runs unpaced)", readMBps, cap)
	}
	if readMBps <= writeMBps {
		t.Errorf("read_mbps %.1f should exceed write_mbps %.1f", readMBps, writeMBps)
	}
}
