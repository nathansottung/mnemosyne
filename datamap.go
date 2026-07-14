package main

// datamap.go — pure honesty surfacing. It answers, in one place and in plain terms,
// "where does this tool put things, and what does it promise never to touch?" Nothing
// here changes behavior; it only reports paths and invariants that already hold:
//
//   - everything Mnemosyne WRITES (catalog + daily backups, config, keystores, staging,
//     the destinations you pick, the small inventory/seal sidecars it puts ONLY on media
//     it writes itself, and the reversible "_deleted" quarantine folders); and
//   - everything it NEVER writes to (your source folders, drives you adopt), with the one
//     enforcement sentence and a pointer to the tests that prove it.
//
// The paths are computed from the same config the rest of the app reads, so the screen
// can never drift from reality.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DataLocation is one row in the "where your data lives" table.
type DataLocation struct {
	Name    string   `json:"name"`
	What    string   `json:"what"`            // plain one-line description
	Path    string   `json:"path,omitempty"`  // the concrete path, when there is a single one
	Paths   []string `json:"paths,omitempty"` // multiple paths (keystores)
	Note    string   `json:"note,omitempty"`  // an extra plain sentence
	Missing bool     `json:"missing,omitempty"`
	Warn    string   `json:"warn,omitempty"` // shown amber when Missing
}

// DataMap is the whole "where your data lives" payload.
type DataMap struct {
	Writes      []DataLocation `json:"writes"`
	Never       []DataLocation `json:"never"`
	Enforcement string         `json:"enforcement"`
	VerifyNote  string         `json:"verify_note"`
	VerifyTests []string       `json:"verify_tests"`
}

// DataMap builds the honesty report from live config + catalog. Read-only.
func (a *App) DataMap() DataMap {
	cfg := a.LoadConfig()
	catalog := filepath.Join(a.DataDir, "catalog.json")

	var dm DataMap

	dm.Writes = append(dm.Writes, DataLocation{
		Name: "The catalog",
		What: "The brain: every hash, drive, and plan. Nothing here is your file content — but losing it loses what the tool knows. Back this up.",
		Path: catalog,
		Note: "Daily backups are kept here automatically: " + filepath.Join(a.DataDir, "catalog.json.bak-YYYYMMDD") + " (the newest 14 are retained).",
	})
	dm.Writes = append(dm.Writes, DataLocation{
		Name: "Settings",
		What: "Your configuration — tool paths, staging, redundancy goals. Plain JSON you can read.",
		Path: a.configPath(),
	})

	ks := DataLocation{
		Name:  "Keystores",
		What:  "Your encryption keys — the app refuses to run encryption without two, on different devices. Secrets live ONLY here; the catalog stores fingerprints, never the key.",
		Paths: append([]string(nil), cfg.KeystorePaths...),
	}
	if len(cfg.KeystorePaths) < MinKeystores {
		ks.Missing = true
		ks.Warn = fmt.Sprintf("Only %d registered — encrypted builds need at least %d.", len(cfg.KeystorePaths), MinKeystores)
	}
	dm.Writes = append(dm.Writes, ks)

	staging := DataLocation{
		Name: "Staging",
		What: "Temporary workspace while building packages; emptied as packages complete. It can't live inside a folder you back up.",
		Path: cfg.StagingDir,
	}
	if strings.TrimSpace(cfg.StagingDir) == "" {
		staging.Missing = true
		staging.Warn = "Not set yet — pick a big, fast scratch folder in Settings."
	}
	dm.Writes = append(dm.Writes, staging)

	appbk := DataLocation{
		Name: "App backups (export)",
		What: "One-file snapshots of everything the tool knows — the catalog, settings, and job history — so it can move to a new computer. Written only when you run \"Back up this app's records\" or turn on automatic backups.",
		Path: cfg.AutoExportDir,
	}
	if strings.TrimSpace(cfg.AutoExportDir) == "" {
		appbk.Missing = true
		appbk.Warn = "Automatic app backups are off. Run one anytime from Settings, or set a folder + cadence there."
	} else {
		appbk.Note = "Automatic cadence: " + autoExportCadenceLabel(cfg.AutoExportCadence) + "."
	}
	dm.Writes = append(dm.Writes, appbk)

	dm.Writes = append(dm.Writes,
		DataLocation{
			Name: "Destinations you choose",
			What: "The tape, disc, or drive you pick for each copy. The tool writes the package there, then re-reads it to verify. On sealed media it also writes the recovery tools (escrow) so the media can rebuild itself later.",
		},
		DataLocation{
			Name: "Inventory & seal sidecars",
			What: "Small self-documenting folders (" + sealSidecarDir + " when sealing, " + dockSidecarDir + " on a mirror target) written ONLY to media this tool itself writes — never to drives you adopt, never to your source folders.",
		},
		DataLocation{
			Name: "Quarantine folders",
			What: "Reversible \"" + QuarantineDir + "\" holding areas, created ONLY inside libraries this tool built. Setting a file aside moves it here; nothing is ever destroyed, and it can always be put back.",
		},
	)

	// ---- the inverse list: what it NEVER writes to ----
	sources := a.Store.SourceRoots()
	dm.Never = append(dm.Never, DataLocation{
		Name:  "Your source folders",
		What:  "The folders you scan. Read-only, always — every file is only opened for reading and hashed. Not one byte is written back.",
		Paths: sources,
	})
	dm.Never = append(dm.Never, DataLocation{
		Name:  "Drives you adopt",
		What:  "Drives you inventory (\"adopt\"). The tool records what's on them in its own catalog and writes NOTHING to the drive itself — no sidecar, no marker.",
		Paths: adoptedDriveLabels(a),
	})

	dm.Enforcement = "Every writable destination — staging, a copy target, a keystore, a restore or recovery-kit folder — is resolved to an absolute path and checked against your registered source folders before anything is written. A destination at or beneath a source is refused, not silently redirected."
	dm.VerifyNote = "Don't take our word for it — this invariant is enforced by tests you can run yourself with `go test ./...`:"
	dm.VerifyTests = []string{
		"TestIntegration_SourceSafetyRefusals (integration_test.go) — staging, write, restore, and kit targets inside a source are all refused.",
		"TestMirror_RefusesSourceDest (mirror_test.go) — a mirror copy cannot target a source folder.",
		"TestQuarantine_AbsentOnAdoptedAndRefusedForSources (quarantine_test.go) — quarantine never appears on adopted or source data.",
		"Store.AssertOutsideSources (store.go) — the one guard every write path calls.",
	}
	return dm
}

// adoptedDriveLabels lists the drives the tool has inventoried (from their offline
// snapshots) — the media it reads but never writes to. Deduped, capped for display.
func adoptedDriveLabels(a *App) []string {
	seen := map[string]bool{}
	var out []string
	for _, snap := range a.Store.VolumeSnapshots() {
		label := strings.TrimSpace(snap.Label)
		if label == "" {
			label = fmt.Sprintf("vol#%d", snap.VolumeID)
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}
