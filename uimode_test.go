package main

// uimode_test.go — the view-mode preference is purely presentational: it must default
// to Guided, persist as a normal config field, and a partial save of just ui_mode must
// not disturb any other setting (the merge contract the picker relies on). Mode changes
// nothing the server branches on — there is no server behavior to test beyond storage.

import "testing"

func TestUIMode_DefaultAndMergePersist(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{DataDir: dir, Store: store}

	// Default is Guided.
	if got := app.LoadConfig().UIMode; got != UIModeGuided {
		t.Fatalf("default UIMode = %q, want %q", got, UIModeGuided)
	}

	// Establish some non-default settings, then flip ONLY ui_mode via a partial save.
	if _, err := app.SaveConfig(map[string]any{"par2_redundancy": 7, "build_verify": "contents"}); err != nil {
		t.Fatal(err)
	}
	before := app.LoadConfig()
	if _, err := app.SaveConfig(map[string]any{"ui_mode": UIModeComplete}); err != nil {
		t.Fatal(err)
	}
	after := app.LoadConfig()

	if after.UIMode != UIModeComplete {
		t.Errorf("ui_mode not persisted: got %q", after.UIMode)
	}
	// The partial save must not clobber unrelated settings.
	if after.Par2Redundancy != before.Par2Redundancy || after.BuildVerify != before.BuildVerify {
		t.Errorf("ui_mode save clobbered other settings: par2 %d→%d, build_verify %q→%q",
			before.Par2Redundancy, after.Par2Redundancy, before.BuildVerify, after.BuildVerify)
	}
}
