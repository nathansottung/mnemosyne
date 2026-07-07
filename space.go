package main

// space.go — the single source of truth for "how much scratch space does this
// need?" The whole point is to kill the common misconception that you need free
// space equal to your entire archive. You don't: Mnemosyne builds ONE package at
// a time and frees its staging before the next, so scratch only ever has to hold
// one package's build peak. All the math lives here so the UI never duplicates it
// (it just renders the verdict + numbers this returns via GET /api/space-advice).

import "fmt"

// spaceSlack is the small filesystem cushion applied on top of the raw peak.
const spaceSlack = 1.01

// packageStagingPeak is the most scratch space ONE package's build ever occupies:
//
//   - plaintext: the tar IS the payload (no second copy), so peak = tar + par2
//     ≈ data × (1 + par2%).  ~1.05–1.1× for typical redundancy.
//   - encrypted: gpg reads the tar and writes the ciphertext, so tar + ciphertext
//     briefly COEXIST ≈ 2× data. If delete_tar_after_encrypt is off, the par2 set
//     is then built while the tar still sits there, adding par2% on top.
//
// This is the exact figure BuildChunk pre-flights against and the advice reports,
// so the two never disagree.
func packageStagingPeak(dataBytes int64, par2 int, encrypted, deleteTar bool) int64 {
	p := float64(par2) / 100
	var mult float64
	if encrypted {
		mult = 2.0 // tar + ciphertext coexist during the encrypt step
		if !deleteTar {
			mult += p // par2 built alongside the retained tar
		}
	} else {
		mult = 1.0 + p // tar is the payload; only par2 adds to it
	}
	return int64(float64(dataBytes) * mult * spaceSlack)
}

// mediaFootprintBytes estimates what one package occupies on the DESTINATION
// medium once written: payload + par2 set + tiny sidecars (~1× + par2%). Uses the
// real ciphertext/payload size when the package is built, else the planned data.
func mediaFootprintBytes(c *Chunk) int64 {
	base := c.EncBytes
	if base == 0 {
		base = c.DataBytes
	}
	return int64(float64(base) * (1.0 + float64(c.Par2)/100) * spaceSlack)
}

// spaceVerdict grades free vs. needed space with the thresholds used everywhere:
// red under 100% (a build/write would fail), amber under 110% (fits but tight),
// green otherwise. "unknown" when free space couldn't be read (free < 0).
func spaceVerdict(free, need int64) string {
	if free < 0 {
		return "unknown"
	}
	if free < need {
		return "red"
	}
	if float64(free) < float64(need)*1.10 {
		return "amber"
	}
	return "green"
}

// SpaceAdvice answers "do I have room?" for the caller's situation, computed
// entirely server-side:
//   - chunkID > 0  → advice for building that ONE package (staging peak vs. free),
//     plus, if dest != "", writing it (dest free vs. its media footprint).
//   - otherwise    → plan-level advice over a collection (collectionID) or the
//     whole catalog (collectionID == 0): package count, media needed, and the
//     single largest package's staging peak — the real binding constraint.
//
// The staging block (path + free space) is always present.
func (a *App) SpaceAdvice(collectionID, chunkID int, dest string) map[string]any {
	cfg := a.LoadConfig()
	out := map[string]any{}

	// Staging: always reported. pathFree walks to the nearest existing ancestor,
	// so a staging dir that doesn't exist yet still yields the drive's free space.
	stagingFree := int64(-1)
	stagingExists := false
	if cfg.StagingDir != "" {
		if free, err := pathFree(cfg.StagingDir); err == nil {
			stagingFree, stagingExists = free, true
		}
	}
	out["staging_dir"] = cfg.StagingDir
	out["staging_free_bytes"] = stagingFree
	out["staging_exists"] = stagingExists
	out["delete_tar_after_encrypt"] = cfg.DeleteTarAfterEncrypt

	if chunkID > 0 {
		c := a.Store.Chunk(chunkID)
		if c == nil {
			out["error"] = fmt.Sprintf("package %d not found", chunkID)
			return out
		}
		peak := packageStagingPeak(c.DataBytes, c.Par2, c.Encrypted, cfg.DeleteTarAfterEncrypt)
		out["package"] = map[string]any{
			"chunk_id": c.ID, "name": c.Name, "encrypted": c.Encrypted,
			"data_bytes": c.DataBytes, "staging_peak_bytes": peak,
			"staging_free_bytes": stagingFree, "staging_verdict": spaceVerdict(stagingFree, peak),
		}
		if dest != "" {
			footprint := mediaFootprintBytes(c)
			d := map[string]any{"path": dest, "package_bytes": footprint}
			if free, err := pathFree(dest); err == nil {
				d["free_bytes"], d["verdict"] = free, spaceVerdict(free, footprint)
			} else {
				d["free_bytes"], d["verdict"], d["error"] = int64(-1), "unknown", err.Error()
			}
			out["dest"] = d
		}
		return out
	}

	// Plan-level: aggregate the packages that will consume staging. Adopted
	// packages are already on media (no build), so they're excluded.
	var packages, media int
	var largestPeak, largestData int64
	kind, mixed := "", false
	for _, c := range a.Store.Chunks(collectionID) {
		if c.Adopted || c.Status == "FAILED" {
			continue
		}
		packages++
		if kind == "" {
			kind = c.MediaKind
		} else if kind != c.MediaKind {
			mixed = true
		}
		if c.Spanned && len(c.Segments) > 0 {
			media += len(c.Segments)
		} else {
			media++
		}
		if peak := packageStagingPeak(c.DataBytes, c.Par2, c.Encrypted, cfg.DeleteTarAfterEncrypt); peak > largestPeak {
			largestPeak, largestData = peak, c.DataBytes
		}
	}
	if mixed {
		kind = "mixed"
	}
	out["plan"] = map[string]any{
		"collection_id": collectionID, "packages": packages, "media_kind": kind, "media_count": media,
		"largest_package_bytes": largestData, "largest_staging_peak_bytes": largestPeak,
		"verdict": spaceVerdict(stagingFree, largestPeak),
		"fits":    stagingExists && stagingFree >= largestPeak,
	}
	return out
}
