package main

// integrity.go — named integrity presets that unify the app's many independent
// assurance knobs (build-verify depth, par2 %, routine verify level, verify-due
// window, read-back) into three comprehensible choices, WITHOUT removing
// individual control.
//
//	Preset     build verify              par2   routine verify   verify due   read-back
//	ARCHIVAL   contents + round-trip     10%    B (full)         12 months    always
//	BALANCED   contents only             10%    C / B on due     12 months    always
//	FAST       none (amber-flagged)      5%     C                24 months    always
//
// Read-back after write is NEVER off: writing unverified media contradicts the
// product, so ReadbackAfterWrite is always true and is not a settable knob.

import "strings"

// Integrity is the effective set of assurance knobs — global, or overridden per
// archive. Preset is the friendly label; it reads "Custom" once any derived knob
// is edited away from a named preset.
type Integrity struct {
	Preset             string `json:"preset"`
	BuildVerify        string `json:"build_verify"`         // full | contents | none
	Par2Redundancy     int    `json:"par2_redundancy"`      // percent
	RoutineVerifyLevel string `json:"routine_verify_level"` // B | C
	VerifyDueMonths    int    `json:"verify_due_months"`
	ReadbackAfterWrite bool   `json:"readback_after_write"` // always true
}

const (
	BuildVerifyFull     = "full"     // contents + decrypt round-trip
	BuildVerifyContents = "contents" // contents only
	BuildVerifyNone     = "none"     // neither (amber)
)

// integrityPresetOrder is high-to-low assurance; index doubles as a rank for the
// "are we lowering integrity?" amber-confirmation check.
var integrityPresetOrder = []string{"ARCHIVAL", "BALANCED", "FAST"}

func integrityPresets() map[string]Integrity {
	return map[string]Integrity{
		"ARCHIVAL": {Preset: "ARCHIVAL", BuildVerify: BuildVerifyFull, Par2Redundancy: 10, RoutineVerifyLevel: "B", VerifyDueMonths: 12, ReadbackAfterWrite: true},
		"BALANCED": {Preset: "BALANCED", BuildVerify: BuildVerifyContents, Par2Redundancy: 10, RoutineVerifyLevel: "C", VerifyDueMonths: 12, ReadbackAfterWrite: true},
		"FAST":     {Preset: "FAST", BuildVerify: BuildVerifyNone, Par2Redundancy: 5, RoutineVerifyLevel: "C", VerifyDueMonths: 24, ReadbackAfterWrite: true},
	}
}

// normBuildVerify canonicalises a build-verify value to one of the three tiers.
// Legacy "fast" maps to none; blank/anything-else means the archival default.
func normBuildVerify(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case BuildVerifyNone, "fast":
		return BuildVerifyNone
	case BuildVerifyContents:
		return BuildVerifyContents
	default:
		return BuildVerifyFull
	}
}

// normLevelBC clamps a routine verify level to B or C (A is not a routine level).
func normLevelBC(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "C") {
		return "C"
	}
	return "B"
}

// normalize fills a preset label + defaults + the always-on read-back flag.
func (iv Integrity) normalize() Integrity {
	iv.BuildVerify = normBuildVerify(iv.BuildVerify)
	iv.RoutineVerifyLevel = normLevelBC(iv.RoutineVerifyLevel)
	if iv.VerifyDueMonths <= 0 {
		iv.VerifyDueMonths = 12
	}
	if iv.Par2Redundancy < 0 {
		iv.Par2Redundancy = 0
	}
	iv.ReadbackAfterWrite = true
	iv.Preset = presetLabelFor(iv)
	return iv
}

// presetLabelFor returns the matching preset name for a set of knob values, or
// "Custom" when they match no named preset.
func presetLabelFor(iv Integrity) string {
	for _, name := range integrityPresetOrder {
		p := integrityPresets()[name]
		if normBuildVerify(iv.BuildVerify) == p.BuildVerify && iv.Par2Redundancy == p.Par2Redundancy &&
			normLevelBC(iv.RoutineVerifyLevel) == p.RoutineVerifyLevel && iv.VerifyDueMonths == p.VerifyDueMonths {
			return name
		}
	}
	return "Custom"
}

// integrityRank ranks a preset for the lowering check (higher = stronger). Custom
// ranks between the presets it most resembles by build-verify depth.
func integrityRank(iv Integrity) int {
	switch normBuildVerify(iv.BuildVerify) {
	case BuildVerifyFull:
		return 3
	case BuildVerifyContents:
		return 2
	default:
		return 1
	}
}

// globalIntegrity reads the effective global integrity from the flat config knobs.
func (cfg Config) globalIntegrity() Integrity {
	return Integrity{
		BuildVerify: cfg.BuildVerify, Par2Redundancy: cfg.Par2Redundancy,
		RoutineVerifyLevel: cfg.RoutineVerifyLevel, VerifyDueMonths: cfg.VerifyDueMonths,
	}.normalize()
}

// effectiveIntegrity returns the integrity a collection's packages are built with:
// the archive's own override if set, else the global setting.
func (a *App) effectiveIntegrity(collectionID int) Integrity {
	if collectionID > 0 {
		if c := a.Store.Collection(collectionID); c != nil && c.Integrity != nil {
			return c.Integrity.normalize()
		}
	}
	return a.LoadConfig().globalIntegrity()
}

// integrityView is the API payload for the Integrity panel: the preset table,
// the global setting, and the effective setting for a collection (0 = global).
func (a *App) integrityView(collectionID int) map[string]any {
	presets := []map[string]any{}
	for _, name := range integrityPresetOrder {
		p := integrityPresets()[name]
		presets = append(presets, map[string]any{"name": name, "build_verify": p.BuildVerify,
			"par2_redundancy": p.Par2Redundancy, "routine_verify_level": p.RoutineVerifyLevel,
			"verify_due_months": p.VerifyDueMonths})
	}
	g := a.LoadConfig().globalIntegrity()
	out := map[string]any{"presets": presets, "order": integrityPresetOrder, "global": g, "readback_always": true, "effective": g}
	if collectionID > 0 {
		c := a.Store.Collection(collectionID)
		out["effective"] = a.effectiveIntegrity(collectionID)
		out["override"] = c != nil && c.Integrity != nil
	}
	return out
}

// mergeIntegrityBody applies a request body over a base integrity: a named preset
// resets all four knobs; individual fields then override (yielding "Custom").
func mergeIntegrityBody(base Integrity, b map[string]any) Integrity {
	if name := strings.ToUpper(strings.TrimSpace(s(b, "preset"))); name != "" && name != "CUSTOM" {
		if p, ok := integrityPresets()[name]; ok {
			base = p
		}
	}
	if v := s(b, "build_verify"); v != "" {
		base.BuildVerify = v
	}
	if v := s(b, "routine_verify_level"); v != "" {
		base.RoutineVerifyLevel = v
	}
	if _, ok := b["par2_redundancy"]; ok {
		base.Par2Redundancy = int(f(b, "par2_redundancy"))
	}
	if _, ok := b["verify_due_months"]; ok {
		base.VerifyDueMonths = int(f(b, "verify_due_months"))
	}
	return base
}

// applyGlobalIntegrity writes the global integrity knobs into config.json.
func (a *App) applyGlobalIntegrity(b map[string]any) (Integrity, error) {
	iv := mergeIntegrityBody(a.LoadConfig().globalIntegrity(), b).normalize()
	_, err := a.SaveConfig(map[string]any{
		"build_verify": iv.BuildVerify, "par2_redundancy": iv.Par2Redundancy,
		"routine_verify_level": iv.RoutineVerifyLevel, "verify_due_months": iv.VerifyDueMonths,
	})
	return iv, err
}

// applyArchiveIntegrity sets or clears an archive's integrity override.
func (a *App) applyArchiveIntegrity(id int, b map[string]any) (Integrity, error) {
	if bl(b, "clear") {
		if err := a.Store.SetCollectionIntegrity(id, nil); err != nil {
			return Integrity{}, err
		}
		return a.LoadConfig().globalIntegrity(), nil
	}
	iv := mergeIntegrityBody(a.effectiveIntegrity(id), b).normalize()
	cp := iv
	return iv, a.Store.SetCollectionIntegrity(id, &cp)
}
