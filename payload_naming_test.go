package main

// payload_naming_test.go — end-to-end proof that a plaintext package is named
// <name>.tar on the medium while an encrypted one is <name>.tar.gpg, that both
// build/write/verify/restore cleanly, that RESTORE.txt is self-consistent per
// mode, and that a legacy plaintext package (payload named <name>.tar.gpg by an
// older build) still verifies and restores via the read-path fallback.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func noProg(float64, string) {}

// nativeTools pins the real Windows executables. The MSYS/Git tar reinterprets a
// "C:\..." argument as an rsh host, so we must use native bsdtar; gpg/par2 are
// pinned for reproducibility. Falls back to PATH lookup when a pin is absent.
func nativeTools(t *testing.T) map[string]string {
	t.Helper()
	pins := map[string]string{
		"tar":  `C:/Windows/System32/tar.exe`,
		"gpg":  `C:/Program Files/GnuPG/bin/gpg.exe`,
		"par2": `C:/Tools/par2/par2.exe`,
	}
	out := map[string]string{}
	for name, p := range pins {
		if _, err := os.Stat(p); err == nil {
			out[name] = p
			continue
		}
		lp, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("required tool %q not found (pin %s missing, not on PATH)", name, p)
		}
		out[name] = lp
	}
	return out
}

// newTestApp wires an App on a temp catalog with a temp staging dir and two
// (empty, consistent) keystores so GenerateKey is allowed to run.
func newTestApp(t *testing.T, tools map[string]string) (*App, string) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	app := &App{DataDir: dataDir, Store: store}
	staging := t.TempDir()
	ks1 := filepath.Join(t.TempDir(), "keystore1.json")
	ks2 := filepath.Join(t.TempDir(), "keystore2.json")
	if err := writeStore(ks1, &keystoreFile{Marker: 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeStore(ks2, &keystoreFile{Marker: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveConfig(map[string]any{
		"staging_dir":     staging,
		"keystore_paths":  []string{ks1, ks2},
		"par2_redundancy": 5,
		"tools":           tools,
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	return app, staging
}

// makeSource writes a couple of real files and returns the source root plus the
// ChunkFileRefs BuildChunk needs (it tars by RelPath under SrcRoot).
func makeSource(t *testing.T) (string, []ChunkFileRef) {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"hello.txt":     "the quick brown fox\n",
		"sub/notes.txt": "a second file in a subfolder\n",
	}
	var refs []ChunkFileRef
	for rel, body := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ChunkFileRef{RelPath: rel, SizeBytes: int64(len(body))})
	}
	return src, refs
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist: %s (%v)", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected file to NOT exist: %s", path)
	}
}

func TestPayloadNamingEndToEnd(t *testing.T) {
	tools := nativeTools(t)
	app, staging := newTestApp(t, tools)
	src, refs := makeSource(t)

	cases := []struct {
		name      string
		encrypted bool
		payload   string // expected on-medium payload filename
		legacy    string // the name a pre-rename build would have used
	}{
		{name: "ENC-PKG", encrypted: true, payload: "ENC-PKG.tar.gpg", legacy: "ENC-PKG.tar.gpg"},
		{name: "PLAIN-PKG", encrypted: false, payload: "PLAIN-PKG.tar", legacy: "PLAIN-PKG.tar.gpg"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := app.Store.AddChunk(Chunk{
				Name: tc.name, Status: "PLANNED", MediaKind: "CUSTOM",
				TargetBytes: 1 << 30, DataBytes: 4096, FileCount: len(refs),
				SrcRoot: src, HashAlg: "SHA256", Par2: 5,
				Encrypted: tc.encrypted, Files: append([]ChunkFileRef{}, refs...),
			})

			// ---- build ----
			if err := app.BuildChunk(c.ID, noProg); err != nil {
				t.Fatalf("BuildChunk: %v", err)
			}
			c = app.Store.Chunk(c.ID)
			stageDir := filepath.Join(staging, tc.name)
			mustExist(t, filepath.Join(stageDir, tc.payload))
			mustExist(t, filepath.Join(stageDir, tc.payload+".par2"))
			mustExist(t, filepath.Join(stageDir, tc.name+".manifest.json"))
			mustExist(t, filepath.Join(stageDir, "RESTORE.txt"))
			if !tc.encrypted {
				// the misleading .tar.gpg must be gone for plaintext
				mustNotExist(t, filepath.Join(stageDir, tc.name+".tar.gpg"))
				mustNotExist(t, filepath.Join(stageDir, tc.name+".tar.gpg.par2"))
			}

			// manifest records the exact payload filename
			manifest, err := os.ReadFile(filepath.Join(stageDir, tc.name+".manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(manifest), `"payload_file": "`+tc.payload+`"`) {
				t.Errorf("manifest missing payload_file %q:\n%s", tc.payload, manifest)
			}

			// RESTORE.txt self-consistency (no cross-contamination)
			restore, err := os.ReadFile(filepath.Join(stageDir, "RESTORE.txt"))
			if err != nil {
				t.Fatal(err)
			}
			rt := string(restore)
			if !strings.Contains(rt, "par2 verify "+tc.payload+".par2") {
				t.Errorf("RESTORE.txt missing par2 line for %q:\n%s", tc.payload, rt)
			}
			if tc.encrypted {
				if !strings.Contains(rt, "gpg -d -o "+tc.name+".tar "+tc.payload) {
					t.Errorf("encrypted RESTORE.txt missing decrypt step:\n%s", rt)
				}
			} else {
				if strings.Contains(rt, ".tar.gpg") {
					t.Errorf("plaintext RESTORE.txt must not mention .tar.gpg:\n%s", rt)
				}
				if !strings.Contains(rt, "tar -xf "+tc.payload+"\n") {
					t.Errorf("plaintext RESTORE.txt missing direct extract:\n%s", rt)
				}
			}

			// ---- write to a medium ----
			medium := t.TempDir()
			if _, err := app.WriteChunk(c.ID, medium, 0, 0, 0, 0, noProg); err != nil {
				t.Fatalf("WriteChunk: %v", err)
			}
			pkgDir := filepath.Join(medium, tc.name)
			mustExist(t, filepath.Join(pkgDir, tc.payload))
			mustExist(t, filepath.Join(pkgDir, tc.payload+".par2"))
			if !tc.encrypted {
				mustNotExist(t, filepath.Join(pkgDir, tc.name+".tar.gpg"))
			}

			// ---- verify ----
			vr, err := app.VerifyChunk(c.ID, medium)
			if err != nil {
				t.Fatalf("VerifyChunk: %v", err)
			}
			if ok, _ := vr["verify_ok"].(bool); !ok {
				t.Fatalf("VerifyChunk not ok: %+v", vr)
			}

			// ---- restore ----
			out := t.TempDir()
			if _, err := app.RestoreChunk(c.ID, pkgDir, out, nil, noProg); err != nil {
				t.Fatalf("RestoreChunk: %v", err)
			}
			assertRestored(t, src, out, refs)

			// ---- legacy fallback (plaintext only): rename the on-medium payload
			// + par2 set to the old uniform .tar.gpg scheme and prove read paths
			// still resolve it.
			if !tc.encrypted {
				legacyMedium := t.TempDir()
				legacyPkg := filepath.Join(legacyMedium, tc.name)
				if err := os.MkdirAll(legacyPkg, 0o755); err != nil {
					t.Fatal(err)
				}
				renameToLegacy(t, pkgDir, legacyPkg, tc.name, tools["par2"])

				vr, err := app.VerifyChunk(c.ID, legacyMedium)
				if err != nil {
					t.Fatalf("legacy VerifyChunk: %v", err)
				}
				if ok, _ := vr["verify_ok"].(bool); !ok {
					t.Fatalf("legacy VerifyChunk not ok: %+v", vr)
				}
				if path, _ := vr["path"].(string); !strings.HasSuffix(path, tc.legacy) {
					t.Errorf("legacy verify resolved %q, want suffix %q", path, tc.legacy)
				}
				out2 := t.TempDir()
				if _, err := app.RestoreChunk(c.ID, legacyPkg, out2, nil, noProg); err != nil {
					t.Fatalf("legacy RestoreChunk: %v", err)
				}
				assertRestored(t, src, out2, refs)
			}
		})
	}
}

// TestSpannedPayloadNaming proves a spanned package byte-splits its payload into
// <name>.segNNN, and that the rejoin-and-restore drill reconstructs the payload
// under the correct name (<name>.tar for plaintext, <name>.tar.gpg encrypted),
// verifies via par2, and extracts.
func TestSpannedPayloadNaming(t *testing.T) {
	tools := nativeTools(t)
	app, staging := newTestApp(t, tools)

	// One ~40KB file, target ~12KB -> several data segments.
	src := t.TempDir()
	big := strings.Repeat("mnemosyne-spanning-payload-", 1500) // ~40KB
	if err := os.WriteFile(filepath.Join(src, "big.dat"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	refs := []ChunkFileRef{{RelPath: "big.dat", SizeBytes: int64(len(big))}}

	for _, tc := range []struct {
		name      string
		encrypted bool
		payload   string
	}{
		{name: "SPAN-PLAIN", encrypted: false, payload: "SPAN-PLAIN.tar"},
		{name: "SPAN-ENC", encrypted: true, payload: "SPAN-ENC.tar.gpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := app.Store.AddChunk(Chunk{
				Name: tc.name, Status: "PLANNED", MediaKind: "CUSTOM",
				TargetBytes: 12000, DataBytes: int64(len(big)), FileCount: 1,
				SrcRoot: src, HashAlg: "SHA256", Par2: 5,
				Encrypted: tc.encrypted, Spanned: true,
				Segments: planSegments(int64(len(big))+4096, 12000),
				Files:    append([]ChunkFileRef{}, refs...),
			})
			if err := app.BuildChunk(c.ID, noProg); err != nil {
				t.Fatalf("BuildChunk: %v", err)
			}
			c = app.Store.Chunk(c.ID)
			stageDir := filepath.Join(staging, tc.name)
			mustExist(t, filepath.Join(stageDir, tc.payload))
			mustExist(t, filepath.Join(stageDir, tc.payload+".par2"))

			// Write every segment into one shared medium folder.
			medium := t.TempDir()
			for i := 0; i < 100; i++ {
				res, err := app.SpanWriteNext(c.ID, medium, 0, 0, 0, 0, noProg)
				if err != nil {
					t.Fatalf("SpanWriteNext: %v", err)
				}
				if done, _ := res["complete"].(bool); done {
					break
				}
			}
			c = app.Store.Chunk(c.ID)
			if c.Status != "VERIFIED" {
				t.Fatalf("spanned package status = %s, want VERIFIED", c.Status)
			}
			pkgDir := filepath.Join(medium, tc.name)
			mustExist(t, filepath.Join(pkgDir, tc.name+".seg001"))
			// The joined payload does not exist on the medium — only segments do.
			mustNotExist(t, filepath.Join(pkgDir, tc.payload))

			// Restore drills the rejoin path; the joined payload lands in out/.
			out := t.TempDir()
			if _, err := app.RestoreChunk(c.ID, pkgDir, out, nil, noProg); err != nil {
				t.Fatalf("RestoreChunk (spanned): %v", err)
			}
			mustExist(t, filepath.Join(out, tc.payload)) // rejoined under the right name
			assertRestored(t, src, out, refs)
		})
	}
}

// renameToLegacy builds a faithful pre-rename plaintext package in dstPkg: the
// payload is copied under the old uniform <name>.tar.gpg name and a fresh par2
// set is generated OVER that file (so the target filename embedded in the .par2
// is <name>.tar.gpg, exactly as an older Mnemosyne would have produced). Non-par2
// sidecars (manifest, RESTORE.txt) are copied verbatim.
func renameToLegacy(t *testing.T, srcPkg, dstPkg, name, par2Bin string) {
	t.Helper()
	entries, err := os.ReadDir(srcPkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, name+".tar") {
			continue // skip the new-scheme payload + par2; recreated below
		}
		b, err := os.ReadFile(filepath.Join(srcPkg, n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstPkg, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// payload <name>.tar -> legacy <name>.tar.gpg
	payload, err := os.ReadFile(filepath.Join(srcPkg, name+".tar"))
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload := filepath.Join(dstPkg, name+".tar.gpg")
	if err := os.WriteFile(legacyPayload, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPar2Create(par2Bin, "", 5, legacyPayload+".par2", legacyPayload); err != nil {
		t.Fatalf("regenerate legacy par2: %v", err)
	}
}

func assertRestored(t *testing.T, src, out string, refs []ChunkFileRef) {
	t.Helper()
	for _, r := range refs {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(r.RelPath)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(r.RelPath)))
		if err != nil {
			t.Errorf("restored file missing %s: %v", r.RelPath, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("restored %s differs from source", r.RelPath)
		}
	}
}
