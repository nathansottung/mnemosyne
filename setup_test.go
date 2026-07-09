package main

// setup_test.go — the first-run interview must configure a COHERENT state on every
// path a user can take through it: each single answer maps to the right template /
// archive kind / nav groups / target requirements / integrity preset, a skip lands
// safe defaults, and the full combinatorial matrix never produces a contradictory
// config (e.g. a tape user missing the LTFS step, or a non-tape user told to install it).

import (
	"strings"
	"testing"
)

func newSetupApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &App{DataDir: dir, Store: store}
}

func hasReq(reqs []SetupRequirement, target string) *SetupRequirement {
	for i := range reqs {
		if reqs[i].Target == target {
			return &reqs[i]
		}
	}
	return nil
}

// A skipped interview must still mark setup complete and land the documented defaults.
func TestApplySetup_SkipDefaults(t *testing.T) {
	app := newSetupApp(t)
	res, err := app.ApplySetup(SetupAnswers{Skipped: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := res.Config
	if !cfg.SetupComplete {
		t.Fatal("skip must set SetupComplete")
	}
	if cfg.DataKind != DataMixed || cfg.PrimaryLocation != LocBoth {
		t.Errorf("skip defaults: data=%q loc=%q, want mixed/both", cfg.DataKind, cfg.PrimaryLocation)
	}
	if len(cfg.BackupTargets) != 1 || cfg.BackupTargets[0] != TargetDrives {
		t.Errorf("skip default targets = %v, want [drives]", cfg.BackupTargets)
	}
	if cfg.UIMode != UIModeGuided {
		t.Errorf("skip default ui_mode = %q, want guided", cfg.UIMode)
	}
	if res.Integrity.Preset != "ARCHIVAL" {
		t.Errorf("skip default integrity = %q, want ARCHIVAL", res.Integrity.Preset)
	}
	if res.Template != "General" || res.ArchiveKind != ArchiveSourced {
		t.Errorf("skip derived: template=%q kind=%q, want General/SOURCED", res.Template, res.ArchiveKind)
	}
}

// Each data kind selects the matching starter template and its vocabulary.
func TestApplySetup_DataKindTemplate(t *testing.T) {
	cases := map[string]struct {
		tmpl  string
		vocab []string
	}{
		DataPhotos:    {"Photographer", photographerVocabulary},
		DataMusic:     {"Musician", musicianVocabulary},
		DataVideo:     {"Filmmaker", filmmakerVocabulary},
		DataDocuments: {"General", generalVocabulary},
		DataMixed:     {"General", generalVocabulary},
	}
	for kind, want := range cases {
		app := newSetupApp(t)
		res, err := app.ApplySetup(SetupAnswers{DataKind: kind})
		if err != nil {
			t.Fatal(err)
		}
		if res.Template != want.tmpl {
			t.Errorf("%s → template %q, want %q", kind, res.Template, want.tmpl)
		}
		if strings.Join(res.Vocabulary, ",") != strings.Join(want.vocab, ",") {
			t.Errorf("%s → vocab %v, want %v", kind, res.Vocabulary, want.vocab)
		}
		// The chosen template must actually be a seeded built-in (so the UI can point at it).
		found := false
		for _, tt := range app.Store.Templates() {
			if tt.Name == res.Template && tt.BuiltIn {
				found = true
			}
		}
		if !found {
			t.Errorf("%s → template %q is not a seeded built-in", kind, res.Template)
		}
	}
}

// Location maps to archive kind + which nav groups open expanded.
func TestApplySetup_LocationArchiveAndNav(t *testing.T) {
	cases := map[string]struct {
		kind string
		nav  []string
	}{
		LocNAS:       {ArchiveSourced, []string{"ORGANIZE", "PROTECT"}},
		LocScattered: {ArchiveSourceless, []string{"INGEST", "ORGANIZE"}},
		LocBoth:      {ArchiveSourced, []string{"ORGANIZE", "INGEST", "PROTECT"}},
	}
	for loc, want := range cases {
		app := newSetupApp(t)
		res, err := app.ApplySetup(SetupAnswers{PrimaryLocation: loc})
		if err != nil {
			t.Fatal(err)
		}
		if res.ArchiveKind != want.kind {
			t.Errorf("%s → archive kind %q, want %q", loc, res.ArchiveKind, want.kind)
		}
		if strings.Join(res.NavExpanded, ",") != strings.Join(want.nav, ",") {
			t.Errorf("%s → nav %v, want %v", loc, res.NavExpanded, want.nav)
		}
	}
}

// A tape user's requirements include the LTFS install step with a link; a
// drive-only user's requirements mention neither tape nor any install step.
func TestApplySetup_TargetRequirements(t *testing.T) {
	app := newSetupApp(t)
	tape, err := app.ApplySetup(SetupAnswers{BackupTargets: []string{TargetTape}})
	if err != nil {
		t.Fatal(err)
	}
	tr := hasReq(tape.Requirements, TargetTape)
	if tr == nil || tr.ChecklistStep == "" || tr.LinkURL == "" {
		t.Fatalf("tape target must carry an install step + link, got %+v", tr)
	}
	if !strings.Contains(strings.ToLower(tr.ChecklistStep), "ltfs") {
		t.Errorf("tape checklist step = %q, want it to mention LTFS", tr.ChecklistStep)
	}

	drives, err := app.ApplySetup(SetupAnswers{BackupTargets: []string{TargetDrives}})
	if err != nil {
		t.Fatal(err)
	}
	if hasReq(drives.Requirements, TargetTape) != nil {
		t.Error("drive-only setup must not include a tape requirement")
	}
	for _, r := range drives.Requirements {
		if r.ChecklistStep != "" {
			t.Errorf("drive-only setup must add no install steps, got %q", r.ChecklistStep)
		}
	}
	if !drives.Config.wantsTarget(TargetDrives) || drives.Config.wantsTarget(TargetTape) {
		t.Error("wantsTarget disagrees with saved backup_targets")
	}
}

// The integrity answer flows through to the real config knobs the pipeline reads.
func TestApplySetup_IntegrityPresetKnobs(t *testing.T) {
	for _, name := range integrityPresetOrder {
		app := newSetupApp(t)
		res, err := app.ApplySetup(SetupAnswers{IntegrityPreset: name})
		if err != nil {
			t.Fatal(err)
		}
		want := integrityPresets()[name]
		got := res.Config.globalIntegrity()
		if got.Preset != name || got.BuildVerify != want.BuildVerify ||
			got.Par2Redundancy != want.Par2Redundancy || got.VerifyDueMonths != want.VerifyDueMonths {
			t.Errorf("%s preset didn't reach config: got %+v want %+v", name, got, want)
		}
	}
}

// SetupState re-derives the exact same facts from a reloaded config.
func TestSetupState_RoundTrip(t *testing.T) {
	app := newSetupApp(t)
	applied, err := app.ApplySetup(SetupAnswers{
		DataKind: DataMusic, PrimaryLocation: LocScattered,
		BackupTargets: []string{TargetTape, TargetCloud}, UIMode: UIModeStandard, IntegrityPreset: "BALANCED",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fresh app over the same data dir = a reload.
	store, err := OpenStore(app.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := (&App{DataDir: app.DataDir, Store: store}).SetupState()
	if reloaded.Template != applied.Template || reloaded.ArchiveKind != applied.ArchiveKind ||
		strings.Join(reloaded.NavExpanded, ",") != strings.Join(applied.NavExpanded, ",") ||
		len(reloaded.Requirements) != len(applied.Requirements) ||
		reloaded.Config.UIMode != UIModeStandard || reloaded.Integrity.Preset != "BALANCED" {
		t.Errorf("SetupState after reload diverged:\n applied=%+v\n reloaded=%+v", applied, reloaded)
	}
}

// Applying setup must not clobber unrelated settings (the config merge contract).
func TestApplySetup_PreservesUnrelated(t *testing.T) {
	app := newSetupApp(t)
	if _, err := app.SaveConfig(map[string]any{"staging_dir": "", "barcode_scheme": "ZZZ", "buffer_gb": 12.5}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplySetup(SetupAnswers{DataKind: DataPhotos, IntegrityPreset: "FAST"}); err != nil {
		t.Fatal(err)
	}
	cfg := app.LoadConfig()
	if cfg.BarcodeScheme != "ZZZ" || cfg.BufferGB != 12.5 {
		t.Errorf("setup clobbered unrelated settings: scheme=%q buffer=%v", cfg.BarcodeScheme, cfg.BufferGB)
	}
}

// The whole matrix: every combination of answers yields a self-consistent state.
func TestApplySetup_MatrixCoherent(t *testing.T) {
	targetSets := [][]string{
		{TargetDrives}, {TargetNAS}, {TargetTape}, {TargetOptical}, {TargetCloud},
		{TargetDrives, TargetTape}, {TargetTape, TargetOptical, TargetCloud},
		setupTargets, nil,
	}
	modes := []string{UIModeGuided, UIModeStandard, UIModeComplete, "garbage"}
	presets := []string{"ARCHIVAL", "BALANCED", "FAST", ""}
	for _, kind := range append(append([]string{}, setupDataKinds...), "garbage") {
		for _, loc := range append(append([]string{}, setupLocations...), "garbage") {
			for _, tgs := range targetSets {
				for _, mode := range modes {
					for _, preset := range presets {
						app := newSetupApp(t)
						res, err := app.ApplySetup(SetupAnswers{
							DataKind: kind, PrimaryLocation: loc, BackupTargets: tgs,
							UIMode: mode, IntegrityPreset: preset,
						})
						if err != nil {
							t.Fatalf("apply failed for %v/%v/%v/%v/%v: %v", kind, loc, tgs, mode, preset, err)
						}
						cfg := res.Config
						// Always complete; never a blank/invalid persisted answer.
						if !cfg.SetupComplete {
							t.Fatal("matrix: SetupComplete must be set")
						}
						if !containsFold(setupDataKinds, cfg.DataKind) {
							t.Errorf("matrix: invalid persisted data kind %q", cfg.DataKind)
						}
						if !containsFold(setupLocations, cfg.PrimaryLocation) {
							t.Errorf("matrix: invalid persisted location %q", cfg.PrimaryLocation)
						}
						if len(cfg.BackupTargets) == 0 {
							t.Error("matrix: backup targets must never persist empty")
						}
						if cfg.UIMode != UIModeGuided && cfg.UIMode != UIModeStandard && cfg.UIMode != UIModeComplete {
							t.Errorf("matrix: invalid persisted ui_mode %q", cfg.UIMode)
						}
						// Archive kind is one of the two valid values and matches the location rule.
						if res.ArchiveKind != archiveKindForLocation(cfg.PrimaryLocation) {
							t.Errorf("matrix: archive kind %q inconsistent with location %q", res.ArchiveKind, cfg.PrimaryLocation)
						}
						// Requirements are exactly the selected targets — no extras, no omissions.
						if len(res.Requirements) != len(cfg.BackupTargets) {
							t.Errorf("matrix: %d requirements for %d targets", len(res.Requirements), len(cfg.BackupTargets))
						}
						for _, r := range res.Requirements {
							if !cfg.wantsTarget(r.Target) {
								t.Errorf("matrix: requirement %q for an unselected target", r.Target)
							}
						}
						// The LTFS install step appears IFF tape is a target — the acceptance rule.
						tapeStep := false
						for _, r := range res.Requirements {
							if r.ChecklistStep != "" && strings.Contains(strings.ToLower(r.ChecklistStep), "ltfs") {
								tapeStep = true
							}
						}
						if tapeStep != cfg.wantsTarget(TargetTape) {
							t.Errorf("matrix: LTFS step presence %v != tape selected %v", tapeStep, cfg.wantsTarget(TargetTape))
						}
						// Integrity is a valid preset (never Custom out of a preset answer).
						if _, ok := integrityPresets()[res.Integrity.Preset]; !ok {
							t.Errorf("matrix: integrity preset %q is not a named preset", res.Integrity.Preset)
						}
					}
				}
			}
		}
	}
}
