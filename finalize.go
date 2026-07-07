package main

// finalize.go — the "close the box and label it" ceremony.
//
// Finalizing a volume is an explicit, gated act that declares it DONE and
// vault-ready. It is deliberately more than a flag: preconditions are enforced
// (a forced override demands a typed confirmation and is audit-logged), the
// medium is made self-documenting (a seal record + regenerated inventory +
// catalog snapshot written onto the volume itself), and the volume is SEALED so
// stray writes can't quietly change what the label promises. Unsealing is the
// explicit, audit-logged way back.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sealSidecarDir is the single folder the finalize ceremony writes onto a volume.
const sealSidecarDir = "MNEMOSYNE_SEAL"

// finalizeCheck is one enforced precondition and its verdict.
type finalizeCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// finalizeAssessment is the full precondition read for a volume: the checks, and
// the counts/bytes/free-space the seal record will carry.
type finalizeAssessment struct {
	Checks     []finalizeCheck `json:"checks"`
	Packages   int             `json:"packages"`
	Bytes      int64           `json:"bytes"`
	FreeBytes  int64           `json:"free_bytes"`
	TotalBytes int64           `json:"total_bytes"`
	OK         bool            `json:"ok"`
}

// volumeChunks returns the packages with a live (non-superseded copy or segment)
// presence on volume v, plus the package count and on-medium byte total.
func (a *App) volumeChunks(v *Volume) (chunks []*Chunk, packages int, bytes int64) {
	for _, c := range a.Store.Chunks(0) {
		onCopy, onSeg := false, false
		for _, cp := range c.Copies {
			if !cp.Superseded && cp.VolumeID == v.ID {
				onCopy = true
			}
		}
		var segBytes int64
		for _, sg := range c.Segments {
			if sg.VolumeID == v.ID {
				onSeg = true
				segBytes += sg.Bytes
			}
		}
		if !onCopy && !onSeg {
			continue
		}
		chunks = append(chunks, c)
		packages++
		if onCopy {
			bytes += c.EncBytes
		} else {
			bytes += segBytes
		}
	}
	return
}

// AssessFinalize evaluates the finalize preconditions for v mounted at mountPath.
// All three are enforced; a forced seal overrides the failing ones (with a typed
// reason). mountPath may be empty for a preview — the space check then reports it
// could not be read.
func (a *App) AssessFinalize(v *Volume, mountPath string, cfg Config) finalizeAssessment {
	var as finalizeAssessment
	chunks, packages, bytes := a.volumeChunks(v)
	as.Packages, as.Bytes = packages, bytes

	// (1) content present.
	as.Checks = append(as.Checks, finalizeCheck{Key: "content", Label: "Has content", OK: packages > 0,
		Detail: fmt.Sprintf("%d package(s) on this volume", packages)})

	// (2) every copy on the volume verified within N days.
	cutoff := time.Now().AddDate(0, 0, -cfg.FinalizeVerifyDays)
	var stale []string
	for _, c := range chunks {
		for _, cp := range c.Copies {
			if cp.Superseded || cp.VolumeID != v.ID {
				continue
			}
			if cp.VerifyOK == nil || !*cp.VerifyOK {
				stale = append(stale, c.Name+" (unverified)")
			} else if cp.LastVerifiedAt == nil || cp.LastVerifiedAt.Before(cutoff) {
				stale = append(stale, c.Name+" (verify stale)")
			}
		}
		for _, sg := range c.Segments {
			if sg.VolumeID == v.ID && sg.Status != "VERIFIED" {
				stale = append(stale, fmt.Sprintf("%s segment %d (%s)", c.Name, sg.Index, sg.Status))
			}
		}
	}
	verifyDetail := fmt.Sprintf("all copies verified within %d days", cfg.FinalizeVerifyDays)
	if packages == 0 {
		verifyDetail = "no copies on this volume"
	} else if len(stale) > 0 {
		verifyDetail = fmt.Sprintf("%d not current: %s", len(stale), joinCap(stale, 5))
	}
	as.Checks = append(as.Checks, finalizeCheck{Key: "verify", Label: "Copies verified", OK: packages > 0 && len(stale) == 0, Detail: verifyDetail})

	// (3) free-space buffer respected — full drives die young.
	if strings.TrimSpace(mountPath) == "" {
		as.Checks = append(as.Checks, finalizeCheck{Key: "space", Label: "Free-space buffer", OK: false, Detail: "mount path not provided — cannot read free space"})
	} else if free, total, err := diskUsage(mountPath); err != nil {
		as.Checks = append(as.Checks, finalizeCheck{Key: "space", Label: "Free-space buffer", OK: false, Detail: "could not read free space at " + mountPath + ": " + err.Error()})
	} else {
		as.FreeBytes, as.TotalBytes = free, total
		pct := 0.0
		if total > 0 {
			pct = float64(free) / float64(total) * 100
		}
		as.Checks = append(as.Checks, finalizeCheck{Key: "space", Label: "Free-space buffer", OK: total > 0 && pct >= cfg.BufferPct,
			Detail: fmt.Sprintf("%.1f%% free (%s of %s) — need ≥ %.1f%%", pct, humanBytes(free), humanBytes(total), cfg.BufferPct)})
	}

	// (4) SMART not failing (only when data exists and gating is on).
	smartOK, smartDetail := true, "SMART gating off"
	if cfg.SmartBlockFinalize {
		if len(v.SmartHistory) == 0 {
			smartDetail = "no SMART reading on record — not blocking"
		} else {
			last := v.SmartHistory[len(v.SmartHistory)-1]
			switch {
			case last.Passed != nil && !*last.Passed:
				smartOK, smartDetail = false, "SMART self-assessment reports FAILING"
			case last.Advisory:
				smartOK, smartDetail = false, nonEmpty(last.AdvisoryWhy, "SMART advisory — migrate copies off this volume")
			default:
				smartDetail = "SMART not failing"
			}
		}
	}
	as.Checks = append(as.Checks, finalizeCheck{Key: "smart", Label: "SMART health", OK: smartOK, Detail: smartDetail})

	as.OK = true
	for _, c := range as.Checks {
		if !c.OK {
			as.OK = false
		}
	}
	return as
}

// FinalizeVolume runs the ceremony. When preconditions fail and force is false it
// returns a non-error "blocked" result carrying the assessment; when force is set
// it demands a typed confirmation reason and records every overridden check.
func (a *App) FinalizeVolume(v *Volume, mountPath, by string, force bool, forceReason string) (map[string]any, error) {
	if strings.TrimSpace(mountPath) == "" {
		return nil, fmt.Errorf("mount_path required (where the volume is mounted — the seal sidecar is written there)")
	}
	if err := a.Store.AssertOutsideSources(mountPath); err != nil {
		return nil, err
	}
	if v.Sealed {
		return nil, fmt.Errorf("%s is already SEALED — unseal it first if you need to re-finalize", v.Label)
	}
	cfg := a.LoadConfig()
	as := a.AssessFinalize(v, mountPath, cfg)

	var overrides []string
	forced := false
	if !as.OK {
		for _, c := range as.Checks {
			if !c.OK {
				overrides = append(overrides, c.Label+": "+c.Detail)
			}
		}
		if !force {
			return map[string]any{"ok": false, "blocked": true, "assessment": as}, nil
		}
		if strings.TrimSpace(forceReason) == "" {
			return nil, fmt.Errorf("forced finalize requires a typed confirmation reason — the override is audit-logged")
		}
		forced = true
	}

	if strings.TrimSpace(by) == "" {
		by = "operator"
	}
	now := time.Now().UTC()
	fin := Finalization{At: now, By: by, Action: "SEALED", Packages: as.Packages, Bytes: as.Bytes,
		FreeBytes: as.FreeBytes, TotalBytes: as.TotalBytes, Forced: forced}
	if forced {
		fin.ForceReason, fin.Overrides = forceReason, overrides
	}

	sidecar, err := a.writeFinalizeSidecar(mountPath, v, as, fin)
	if err != nil {
		return nil, fmt.Errorf("writing seal sidecar: %w", err)
	}
	fin.Sidecar = sidecar

	v.Sealed, v.SealedAt = true, &now
	v.Finalizations = append(v.Finalizations, fin)
	a.Store.UpdateVolume(v)

	detail := fmt.Sprintf("%s SEALED by %s — %d package(s), %s", v.Label, by, as.Packages, humanBytes(as.Bytes))
	if forced {
		detail += " · FORCED (" + forceReason + ") overriding: " + strings.Join(overrides, "; ")
	}
	a.Store.Log("finalize", detail)

	return map[string]any{"ok": true, "volume": v, "finalization": fin, "assessment": as,
		"label_url": fmt.Sprintf("/api/volumes/%d/label", v.ID)}, nil
}

// UnsealVolume re-enables writes to a sealed volume. The reason is required and
// audit-logged, and recorded as an UNSEALED entry in the ceremony history.
func (a *App) UnsealVolume(v *Volume, by, reason string) error {
	if !v.Sealed {
		return fmt.Errorf("%s is not sealed", v.Label)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("unseal requires a reason — it is audit-logged")
	}
	if strings.TrimSpace(by) == "" {
		by = "operator"
	}
	now := time.Now().UTC()
	v.Sealed, v.SealedAt = false, nil
	v.Finalizations = append(v.Finalizations, Finalization{At: now, By: by, Action: "UNSEALED", ForceReason: reason})
	a.Store.UpdateVolume(v)
	a.Store.Log("finalize", fmt.Sprintf("%s UNSEALED by %s — %s (writes re-enabled)", v.Label, by, reason))
	return nil
}

// writeFinalizeSidecar makes the medium self-documenting: FINALIZATION.json (the
// seal record + checks), catalog_snapshot.json (machine-readable contents), and
// INVENTORY.md (the human trail). Guarded against writing into a source.
func (a *App) writeFinalizeSidecar(mountPath string, v *Volume, as finalizeAssessment, fin Finalization) (string, error) {
	if err := a.Store.AssertOutsideSources(mountPath); err != nil {
		return "", err
	}
	dir := filepath.Join(mountPath, sealSidecarDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	chunks, _, _ := a.volumeChunks(v)

	type invPkg struct {
		Name           string         `json:"name"`
		Bytes          int64          `json:"bytes"`
		Encrypted      bool           `json:"encrypted"`
		Spanned        bool           `json:"spanned,omitempty"`
		LastVerifiedAt *time.Time     `json:"last_verified_at,omitempty"`
		Integrity      *BuildVerified `json:"integrity,omitempty"` // effective integrity this package was built with
		Files          []string       `json:"files"`
	}
	pkgs := make([]invPkg, 0, len(chunks))
	for _, c := range chunks {
		var lv *time.Time
		for _, cp := range c.Copies {
			if !cp.Superseded && cp.VolumeID == v.ID {
				lv = cp.LastVerifiedAt
			}
		}
		files := make([]string, 0, len(c.Files))
		for _, cf := range c.Files {
			files = append(files, cf.RelPath)
		}
		pkgs = append(pkgs, invPkg{Name: c.Name, Bytes: c.EncBytes, Encrypted: c.Encrypted,
			Spanned: c.Spanned, LastVerifiedAt: lv, Integrity: c.BuildVerified, Files: files})
	}

	frec := map[string]any{"mnemosyne_finalization": 1, "generated_utc": now.Format(time.RFC3339),
		"volume": v, "finalization": fin, "checks": as.Checks}
	if b, err := json.MarshalIndent(frec, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "FINALIZATION.json"), b, 0o644); err != nil {
			return dir, err
		}
	}
	snap := map[string]any{"mnemosyne_seal_snapshot": 1, "generated_utc": now.Format(time.RFC3339),
		"volume": v, "package_count": len(pkgs), "packages": pkgs}
	sb, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "catalog_snapshot.json"), sb, 0o644); err != nil {
		return dir, err
	}

	var b strings.Builder
	b.WriteString("# Mnemosyne — sealed volume inventory\n\n")
	b.WriteString(fmt.Sprintf("**%s** sealed %s by %s.\n\n", v.Label, now.Format("2006-01-02 15:04 MST"), fin.By))
	b.WriteString(fmt.Sprintf("- **Barcode:** `%s`\n", nonEmpty(v.Barcode, "—")))
	if v.Serial != "" {
		b.WriteString(fmt.Sprintf("- **Serial:** `%s`\n", v.Serial))
	}
	if v.Location != "" {
		b.WriteString(fmt.Sprintf("- **Location:** %s (%s)\n", v.Location, offsiteWord(v.Offsite)))
	}
	b.WriteString(fmt.Sprintf("- **Packages:** %d · **On-medium bytes:** %s\n", fin.Packages, humanBytes(fin.Bytes)))
	if fin.Forced {
		b.WriteString(fmt.Sprintf("- **⚠ FORCED seal:** %s — overriding: %s\n", fin.ForceReason, strings.Join(fin.Overrides, "; ")))
	}
	b.WriteString("\nThis medium is SEALED: the catalog refuses further writes to it until an explicit, audit-logged unseal.\n\n")
	for _, p := range pkgs {
		b.WriteString(fmt.Sprintf("## %s — %s%s\n\n", p.Name, humanBytes(p.Bytes), map[bool]string{true: " · encrypted", false: ""}[p.Encrypted]))
		for i, f := range p.Files {
			if i >= 2000 {
				b.WriteString(fmt.Sprintf("_…and %d more (see catalog_snapshot.json)_\n", len(p.Files)-i))
				break
			}
			b.WriteString(fmt.Sprintf("- `%s`\n", f))
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "INVENTORY.md"), []byte(b.String()), 0o644); err != nil {
		return dir, err
	}
	return dir, nil
}

// joinCap joins up to n items, appending "(+k more)" when truncated.
func joinCap(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" (+%d more)", len(items)-n)
}
