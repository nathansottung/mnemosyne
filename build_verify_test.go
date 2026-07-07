package main

// build_verify_test.go — the two custody links that were fingerprinted but never
// proven, now proven at BUILD time. These drive the real BuildChunk (native
// tar/gpg/par2 via nativeTools) and inject a fault at a precise point through the
// package-private build hooks, asserting the build fails at the RIGHT stage with
// the RIGHT message — a corrupt or undecryptable payload can never reach media.
//
// Tests here share the package-level build hooks, so none call t.Parallel().

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// hashRefs fills in each ChunkFileRef.Hash from the real source file, so stage
// verification has a source hash to compare the tar's members against.
func hashRefs(t *testing.T, src string, refs []ChunkFileRef) []ChunkFileRef {
	t.Helper()
	out := append([]ChunkFileRef{}, refs...)
	for i := range out {
		h, err := hashFileHex(filepath.Join(src, filepath.FromSlash(out[i].RelPath)))
		if err != nil {
			t.Fatalf("hashing source %s: %v", out[i].RelPath, err)
		}
		out[i].Hash = h
	}
	return out
}

// corruptFirstTarMember rewrites the tar in place with the first regular-file
// member's content byte flipped — a structurally valid tar whose member no
// longer matches its source hash. Returns the corrupted member's rel path.
func corruptFirstTarMember(t *testing.T, tarPath string) string {
	t.Helper()
	raw, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	corrupted := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if corrupted == "" && (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) && len(data) > 0 {
			data[0] ^= 0xFF // flip a content byte — size unchanged, header stays valid
			corrupted = path.Clean(strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./"))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if corrupted == "" {
		t.Fatal("no regular-file member found to corrupt")
	}
	if err := os.WriteFile(tarPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return corrupted
}

func newVerifyChunk(app *App, src string, refs []ChunkFileRef, encrypted bool) *Chunk {
	return app.Store.AddChunk(Chunk{
		Name: "BV-PKG", Status: "PLANNED", MediaKind: "CUSTOM",
		TargetBytes: 1 << 30, DataBytes: 4096, FileCount: len(refs),
		SrcRoot: src, HashAlg: "SHA256", Par2: 5, Encrypted: encrypted,
		Files: append([]ChunkFileRef{}, refs...),
	})
}

// (1) STAGE-VS-SOURCE catches a tar corrupted between the tar and hash steps,
// failing the build at stage verification with the exact file named.
func TestBuildVerify_CatchesCorruptTar(t *testing.T) {
	tools := nativeTools(t)
	app, _ := newTestApp(t, tools)
	src, refs := makeSource(t)
	refs = hashRefs(t, src, refs)
	c := newVerifyChunk(app, src, refs, false) // plaintext: stage_verify still runs

	var corrupted string
	buildAfterTarHook = func(tarPath string) { corrupted = corruptFirstTarMember(t, tarPath) }
	defer func() { buildAfterTarHook = nil }()

	err := app.BuildChunk(c.ID, noProg)
	if err == nil {
		t.Fatal("build must FAIL when the tar does not contain the source byte-exact")
	}
	if !strings.Contains(err.Error(), "stage verification") {
		t.Errorf("expected a stage-verification failure, got: %v", err)
	}
	if corrupted == "" || !strings.Contains(err.Error(), corrupted) {
		t.Errorf("failure must name the exact file %q, got: %v", corrupted, err)
	}
	c = app.Store.Chunk(c.ID)
	if c.Status != "FAILED" {
		t.Errorf("build failure must mark the package FAILED, got %s", c.Status)
	}
}

// (2) DECRYPT ROUND-TRIP catches a ciphertext that does not decrypt back to the
// verified tar (here via an injected wrong passphrase — a corrupt encryption
// step in the field), failing the build with the doctrine message.
func TestBuildVerify_CatchesBadEncryption(t *testing.T) {
	tools := nativeTools(t)
	app, _ := newTestApp(t, tools)
	src, refs := makeSource(t)
	refs = hashRefs(t, src, refs)
	c := newVerifyChunk(app, src, refs, true) // encrypted: crypt_verify runs

	buildDecryptPassphraseHook = func(pass string) string { return pass + "-WRONG" }
	defer func() { buildDecryptPassphraseHook = nil }()

	err := app.BuildChunk(c.ID, noProg)
	if err == nil {
		t.Fatal("build must FAIL when the ciphertext does not decrypt to the verified tar")
	}
	if !strings.Contains(err.Error(), "does not decrypt to the verified tar") {
		t.Errorf("expected the decrypt-round-trip failure message, got: %v", err)
	}
	if c = app.Store.Chunk(c.ID); c.Status != "FAILED" {
		t.Errorf("build failure must mark the package FAILED, got %s", c.Status)
	}
}

// A full build records both proofs: build_timings gains stage_verify +
// crypt_verify, BuildVerified attests both, and it rides into the manifest.
func TestBuildVerify_FullAttestation(t *testing.T) {
	tools := nativeTools(t)
	app, _ := newTestApp(t, tools)
	src, refs := makeSource(t)
	refs = hashRefs(t, src, refs)
	c := newVerifyChunk(app, src, refs, true)
	if err := app.BuildChunk(c.ID, noProg); err != nil {
		t.Fatalf("clean full build must succeed: %v", err)
	}
	c = app.Store.Chunk(c.ID)
	if c.BuildVerified == nil || c.BuildVerified.Mode != "full" ||
		!c.BuildVerified.Contents || !c.BuildVerified.DecryptRoundtrip {
		t.Fatalf("full build must attest contents + decrypt_roundtrip: %+v", c.BuildVerified)
	}
	for _, k := range []string{"stage_verify", "crypt_verify"} {
		if _, ok := c.BuildTimings[k]; !ok {
			t.Errorf("build_timings must record %q: %v", k, c.BuildTimings)
		}
	}
	// The attestation must be on the medium's manifest.
	mb, err := os.ReadFile(filepath.Join(c.StagedDir, c.Name+".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		BuildVerified BuildVerified `json:"build_verified"`
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	if !m.BuildVerified.Contents || !m.BuildVerified.DecryptRoundtrip || m.BuildVerified.Mode != "full" {
		t.Errorf("manifest.json must carry build_verified: %+v", m.BuildVerified)
	}
}

// build_verify=fast is the explicit opt-out: both checks are skipped (so an
// injected tar corruption is NOT caught), and the package is stamped with the
// amber warning in the catalog and on the medium's manifest.
func TestBuildVerify_FastModeSkipsAndWarns(t *testing.T) {
	tools := nativeTools(t)
	app, _ := newTestApp(t, tools)
	if _, err := app.SaveConfig(map[string]any{"build_verify": "fast"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	src, refs := makeSource(t)
	refs = hashRefs(t, src, refs)
	c := newVerifyChunk(app, src, refs, false)

	// Even with the tar deliberately corrupted, fast mode does not check it.
	buildAfterTarHook = func(tarPath string) { corruptFirstTarMember(t, tarPath) }
	defer func() { buildAfterTarHook = nil }()

	if err := app.BuildChunk(c.ID, noProg); err != nil {
		t.Fatalf("fast build must SUCCEED (checks skipped): %v", err)
	}
	c = app.Store.Chunk(c.ID)
	// Legacy build_verify="fast" now normalises to the "none" tier (both proofs off).
	if c.BuildVerified == nil || c.BuildVerified.Mode != "none" ||
		c.BuildVerified.Contents || c.BuildVerified.DecryptRoundtrip {
		t.Fatalf("fast build must record mode:none with both proofs false: %+v", c.BuildVerified)
	}
	if c.BuildVerified.Warning == "" {
		t.Error("fast build must carry an explicit amber warning")
	}
	if _, ok := c.BuildTimings["stage_verify"]; ok {
		t.Error("fast build must not record a stage_verify timing")
	}
}
