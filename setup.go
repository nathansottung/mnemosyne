package main

// setup.go — the first-run interview that REPLACES the cold-start checklist. Five
// short, skippable questions turn "here's an empty app, good luck" into a configured,
// coherent starting state:
//
//	1. What kind of data?      → starter template + category vocabulary
//	2. Where does it live?     → default archive kind + which nav groups open expanded
//	3. What will you back up to?→ which target UI shows (the rest collapse), + what each needs
//	4. How much guidance?      → the UI mode (guided/standard/complete)
//	5. How careful checking?   → the integrity preset (ARCHIVAL/BALANCED/FAST)
//
// Every answer is optional: a skipped interview still lands a coherent DEFAULT state
// (mixed data, both locations, external drives, guided, ARCHIVAL) and marks setup
// complete so it never nags again. ApplySetup is the single testable seam that maps
// answers → config; SetupState re-derives the same presentational facts from a saved
// config so the UI (summary screen + scoped getting-started checklist) reads one truth.

import "strings"

// ---- answer vocabularies (small, closed sets; blanks fall back to a default) ----

const (
	DataPhotos    = "photos"
	DataMusic     = "music"
	DataVideo     = "video"
	DataDocuments = "documents"
	DataMixed     = "mixed"

	LocNAS       = "nas"       // one main place (a NAS or a big drive) with source folders
	LocScattered = "scattered" // spread across many drives, no single source
	LocBoth      = "both"

	TargetDrives  = "drives"
	TargetNAS     = "nas"
	TargetTape    = "tape"
	TargetOptical = "optical"
	TargetCloud   = "cloud"
)

// setupDataKinds / setupLocations / setupTargets are the canonical option orders the
// interview presents and the normalizers validate against.
var (
	setupDataKinds = []string{DataPhotos, DataMusic, DataVideo, DataDocuments, DataMixed}
	setupLocations = []string{LocNAS, LocScattered, LocBoth}
	setupTargets   = []string{TargetDrives, TargetNAS, TargetTape, TargetOptical, TargetCloud}
)

// SetupAnswers is one interview submission. Any field may be blank/empty (a question
// was skipped); Skipped=true means the whole interview was dismissed.
type SetupAnswers struct {
	Skipped         bool
	DataKind        string
	PrimaryLocation string
	BackupTargets   []string
	UIMode          string
	IntegrityPreset string
}

// SetupRequirement is the one-line "what this target needs" the checklist and summary
// show, with an install link where relevant (tape → LTFS, optical → burner, cloud → rclone).
type SetupRequirement struct {
	Target   string `json:"target"`
	Label    string `json:"label"`
	Need     string `json:"need"`
	LinkText string `json:"link_text,omitempty"`
	LinkURL  string `json:"link_url,omitempty"`
	// ChecklistStep is non-empty when this target adds a getting-started step
	// (installing something). Targets that need nothing extra leave it blank.
	ChecklistStep string `json:"checklist_step,omitempty"`
}

// SetupResult is what ApplySetup / SetupState hand back: the saved config plus the
// presentational facts derived from it. The UI renders the summary screen and the
// scoped checklist entirely from these — no answer-to-UI mapping is duplicated client-side.
type SetupResult struct {
	Config       Config             `json:"config"`
	Template     string             `json:"template"`     // starter template name for the chosen data kind
	Vocabulary   []string           `json:"vocabulary"`   // that template's category words (preview)
	ArchiveKind  string             `json:"archive_kind"` // SOURCED | SOURCELESS default for new archives
	NavExpanded  []string           `json:"nav_expanded"` // nav group ids that should open expanded
	Requirements []SetupRequirement `json:"requirements"` // one per selected backup target
	Integrity    Integrity          `json:"integrity"`    // effective global integrity after setup
}

// ---- answer → fact mappings (pure; each has a safe default) -----------------

// normDataKind clamps an answer to a known data kind, defaulting to mixed.
func normDataKind(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, k := range setupDataKinds {
		if v == k {
			return v
		}
	}
	return DataMixed
}

// normLocation clamps an answer to a known primary-location, defaulting to both.
func normLocation(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, l := range setupLocations {
		if v == l {
			return v
		}
	}
	return LocBoth
}

// normTargets keeps only known targets (order-stable, deduped). Empty → external
// drives, the zero-dependency default everyone has.
func normTargets(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		for _, known := range setupTargets {
			if t == known && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		out = []string{TargetDrives}
	}
	return out
}

// starterTemplateFor maps a data kind to the built-in starter template name (from
// starterTemplates()). Documents and mixed both start on the discipline-neutral General.
func starterTemplateFor(kind string) string {
	switch normDataKind(kind) {
	case DataPhotos:
		return "Photographer"
	case DataMusic:
		return "Musician"
	case DataVideo:
		return "Filmmaker"
	default:
		return "General"
	}
}

// vocabularyFor returns the category words the chosen starter template ships with —
// used only as a preview in the interview and summary.
func vocabularyFor(kind string) []string {
	switch normDataKind(kind) {
	case DataPhotos:
		return append([]string(nil), photographerVocabulary...)
	case DataMusic:
		return append([]string(nil), musicianVocabulary...)
	case DataVideo:
		return append([]string(nil), filmmakerVocabulary...)
	default:
		return append([]string(nil), generalVocabulary...)
	}
}

// archiveKindForLocation picks the default archive kind for new archives: a single
// main place has source folders (SOURCED); scattered drives have no single source
// (SOURCELESS); "both" still has a main place, so it defaults SOURCED.
func archiveKindForLocation(loc string) string {
	if normLocation(loc) == LocScattered {
		return ArchiveSourceless
	}
	return ArchiveSourced
}

// navExpandedForLocation returns the nav group ids (see NAV_GROUPS in the UI) that
// should open expanded for this location. A NAS user lives in ORGANIZE+PROTECT; a
// scattered-drives user lives in INGEST (drive inventory) first; "both" opens all three.
func navExpandedForLocation(loc string) []string {
	switch normLocation(loc) {
	case LocNAS:
		return []string{"ORGANIZE", "PROTECT"}
	case LocScattered:
		return []string{"INGEST", "ORGANIZE"}
	default: // both
		return []string{"ORGANIZE", "INGEST", "PROTECT"}
	}
}

// requirementFor returns the one-line need (and any install link / checklist step)
// for a single backup target.
func requirementFor(target string) SetupRequirement {
	switch target {
	case TargetDrives:
		return SetupRequirement{Target: target, Label: "External drives",
			Need: "No extra software — verified copies write straight to any drive."}
	case TargetNAS:
		return SetupRequirement{Target: target, Label: "A NAS",
			Need: "No extra software — point a copy at a mounted network share like any drive."}
	case TargetTape:
		return SetupRequirement{Target: target, Label: "LTO tape",
			Need:     "Needs an LTFS driver so the tape mounts as a plain folder.",
			LinkText: "Get a free LTFS driver", LinkURL: "https://github.com/LinearTapeFileSystem/ltfs",
			ChecklistStep: "Install the LTFS driver"}
	case TargetOptical:
		return SetupRequirement{Target: target, Label: "Blu-ray / DVD",
			Need:     "Needs a disc-burning tool (xorriso is the free default; ImgBurn on Windows).",
			LinkText: "Install xorriso", LinkURL: "https://www.gnu.org/software/xorriso/",
			ChecklistStep: "Install a disc burner"}
	case TargetCloud:
		return SetupRequirement{Target: target, Label: "Cloud",
			Need:     "Needs rclone to sync a copy to your cloud provider.",
			LinkText: "Install rclone", LinkURL: "https://rclone.org/downloads/",
			ChecklistStep: "Install rclone"}
	}
	return SetupRequirement{Target: target, Label: target}
}

func requirementsFor(targets []string) []SetupRequirement {
	out := make([]SetupRequirement, 0, len(targets))
	for _, t := range targets {
		out = append(out, requirementFor(t))
	}
	return out
}

// wantsTarget reports whether a saved config selected a given backup target. Used by
// the UI (and tests) to hide/collapse unselected paths' UI.
func (cfg Config) wantsTarget(target string) bool {
	for _, t := range cfg.BackupTargets {
		if strings.EqualFold(t, target) {
			return true
		}
	}
	return false
}

// ---- apply + re-derive ------------------------------------------------------

// ApplySetup writes the interview answers into config coherently and returns the
// resulting state. Integrity is applied through the same seam the Verify panel uses
// (so a named preset resets all four knobs); the three answer fields and ui_mode are
// merged into config; setup_complete is always set so the interview never re-nags.
// A skipped interview lands the coherent defaults.
func (a *App) ApplySetup(ans SetupAnswers) (SetupResult, error) {
	kind := normDataKind(ans.DataKind)
	loc := normLocation(ans.PrimaryLocation)
	targets := normTargets(ans.BackupTargets)

	uiMode := strings.ToLower(strings.TrimSpace(ans.UIMode))
	if _, ok := map[string]bool{UIModeGuided: true, UIModeStandard: true, UIModeComplete: true}[uiMode]; !ok {
		uiMode = UIModeGuided
	}

	// Integrity: a named preset (default ARCHIVAL) resets the four assurance knobs.
	preset := strings.ToUpper(strings.TrimSpace(ans.IntegrityPreset))
	if _, ok := integrityPresets()[preset]; !ok {
		preset = "ARCHIVAL"
	}
	if _, err := a.applyGlobalIntegrity(map[string]any{"preset": preset}); err != nil {
		return SetupResult{}, err
	}

	cfg, err := a.SaveConfig(map[string]any{
		"setup_complete":   true,
		"data_kind":        kind,
		"primary_location": loc,
		"backup_targets":   targets,
		"ui_mode":          uiMode,
	})
	if err != nil {
		return SetupResult{}, err
	}
	a.Store.Log("setup", "interview applied: data="+kind+" location="+loc+" targets="+strings.Join(targets, ",")+" mode="+uiMode+" integrity="+preset)
	return a.setupResult(cfg), nil
}

// SetupState re-derives the presentational facts from the CURRENT saved config,
// without changing anything — the UI reads it to re-render the summary or the scoped
// checklist after a reload.
func (a *App) SetupState() SetupResult {
	return a.setupResult(a.LoadConfig())
}

// setupResult builds the derived facts for a config value.
func (a *App) setupResult(cfg Config) SetupResult {
	targets := cfg.BackupTargets
	if len(targets) == 0 {
		targets = []string{TargetDrives}
	}
	return SetupResult{
		Config:       cfg,
		Template:     starterTemplateFor(cfg.DataKind),
		Vocabulary:   vocabularyFor(cfg.DataKind),
		ArchiveKind:  archiveKindForLocation(cfg.PrimaryLocation),
		NavExpanded:  navExpandedForLocation(cfg.PrimaryLocation),
		Requirements: requirementsFor(targets),
		Integrity:    cfg.globalIntegrity(),
	}
}
