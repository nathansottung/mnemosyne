package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// finalizeApp builds an app with one volume holding one verified package, mounted
// at a scratch dir, with the free-space buffer disabled so the space precondition
// never flakes on a full CI disk.
func finalizeApp(t *testing.T) (*App, *Volume, *Chunk, string) {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	app := &App{DataDir: filepath.Dir(st.path), Store: st}
	// Disable the free-space buffer so the space precondition never flakes.
	if _, err := app.SaveConfig(map[string]any{"buffer_pct": 0}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	vol := st.AddVolume(Volume{Label: "VAULT-01", Kind: "HDD"})
	ch := st.AddChunk(Chunk{Name: "PKG-C0001", Status: "WRITTEN", EncBytes: 1000,
		Files: []ChunkFileRef{{FileID: 1, RelPath: "a.nef"}}})
	st.RecordCopy(ch, vol.ID, "M:/PKG-C0001", true)
	mount := t.TempDir()
	return app, vol, ch, mount
}

func TestFinalizeSealsAndWritesSidecar(t *testing.T) {
	app, vol, _, mount := finalizeApp(t)

	res, err := app.FinalizeVolume(vol, mount, "NS", false, "")
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("expected clean seal, got %v", res)
	}
	if !vol.Sealed || vol.SealedAt == nil {
		t.Fatal("volume should be sealed")
	}
	for _, f := range []string{"FINALIZATION.json", "catalog_snapshot.json", "INVENTORY.md"} {
		if _, err := os.Stat(filepath.Join(mount, sealSidecarDir, f)); err != nil {
			t.Errorf("sidecar %s should exist: %v", f, err)
		}
	}
	// A sealed volume refuses further writes until unsealed.
	if err := sealGuard(app, vol.ID); err == nil {
		t.Fatal("sealGuard should refuse a write to a sealed volume")
	}
	// Unseal requires a reason and re-enables writes.
	if err := app.UnsealVolume(vol, "NS", ""); err == nil {
		t.Fatal("unseal should require a reason")
	}
	if err := app.UnsealVolume(vol, "NS", "adding a third copy"); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if vol.Sealed {
		t.Fatal("volume should be unsealed")
	}
	if err := sealGuard(app, vol.ID); err != nil {
		t.Fatalf("sealGuard should allow writes after unseal: %v", err)
	}
}

func TestFinalizeBlockedUnlessForced(t *testing.T) {
	app, vol, ch, mount := finalizeApp(t)
	// Make the copy's verification stale so the verify precondition fails.
	old := time.Now().AddDate(0, 0, -365)
	ch.Copies[0].LastVerifiedAt = &old

	res, err := app.FinalizeVolume(vol, mount, "NS", false, "")
	if err != nil {
		t.Fatalf("finalize (expected blocked, not error): %v", err)
	}
	if blocked, _ := res["blocked"].(bool); !blocked {
		t.Fatalf("stale verify should block finalize, got %v", res)
	}
	if vol.Sealed {
		t.Fatal("blocked finalize must not seal")
	}
	// Forcing without a typed reason is refused.
	if _, err := app.FinalizeVolume(vol, mount, "NS", true, ""); err == nil {
		t.Fatal("forced finalize should require a typed reason")
	}
	// Forcing with a reason seals and records the override.
	res, err = app.FinalizeVolume(vol, mount, "NS", true, "vaulting today; re-verify scheduled")
	if err != nil {
		t.Fatalf("forced finalize: %v", err)
	}
	if ok, _ := res["ok"].(bool); !ok || !vol.Sealed {
		t.Fatalf("forced finalize should seal, got %v", res)
	}
	fin := vol.Finalizations[len(vol.Finalizations)-1]
	if !fin.Forced || fin.ForceReason == "" || len(fin.Overrides) == 0 {
		t.Fatalf("forced seal should record the override, got %+v", fin)
	}
}
