package main

// dvdisaster.go — the optional disc-level ECC layer.
//
// Contract mirrors smart.go exactly: dvdisaster is an OPTIONAL external tool,
// detected like smartctl and hidden behind an install hint when absent, and it is
// NEVER in the restore path. Reed–Solomon ECC computed over the whole disc image
// heals scratches that wipe out runs of physical sectors — a layer par2 cannot
// provide, because par2 protects the payload FILE, not the disc geometry. The two
// are complementary and independent: par2 repair of the payload works whether or
// not dvdisaster was ever used, and restore never requires it.
//
// Doctrine: ECC is generated only AFTER a burned disc verifies (no point protecting
// a disc we haven't proven readable), and generation is silent-but-logged — a
// failure forfeits the extra layer, never the burn.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const dvdisasterInstallHint = "dvdisaster not found — disc-level ECC is hidden. " +
	"Install it (open source): Windows — dvdisaster.jcea.es (or an OS package); " +
	"Linux — `apt install dvdisaster`; macOS — `brew install dvdisaster`. " +
	"ECC is an EXTRA layer over the disc geometry; par2 repair of the payload works " +
	"regardless, and restore never requires dvdisaster."

// dvdisasterBin resolves the dvdisaster binary (config Tools override, then PATH).
func (a *App) dvdisasterBin() (string, error) { return a.tool("dvdisaster") }

// dvdisasterAvailable reports whether the disc-ECC feature can run at all.
func (a *App) dvdisasterAvailable() bool { _, err := a.dvdisasterBin(); return err == nil }

// normBurnEcc normalises the configured ECC method to off / rs02 / rs03. Anything
// blank or unrecognised means OFF — ECC is opt-in and must never surprise anyone.
func normBurnEcc(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "rs02":
		return "rs02"
	case "rs03":
		return "rs03"
	default:
		return "off"
	}
}

// burnEccOn reports whether ECC auto-generation is configured (method != off).
func (cfg Config) burnEccOn() bool { return normBurnEcc(cfg.BurnEcc) != "off" }

// eccIntended reports whether a disc's RESTORE.txt should carry the ECC paragraph:
// either auto-generation is on, or the operator ticked the "note it in docs" flag.
func (cfg Config) eccIntended() bool { return cfg.burnEccOn() || cfg.OpticalEcc }

// eccMethodArg maps a normalised method to dvdisaster's -m<METHOD> value ("" = off).
func eccMethodArg(method string) string {
	switch normBurnEcc(method) {
	case "rs02":
		return "RS02"
	case "rs03":
		return "RS03"
	default:
		return ""
	}
}

// eccFileName is the ECC file that rides beside a package: <name>.ecc.
func eccFileName(chunkName string) string { return chunkName + ".ecc" }

// eccGenArgs builds the dvdisaster command that reads the burned disc at `device`
// and writes an external error-correction file `eccPath` using `method`. Pure (no
// I/O) so it is unit-testable. Returns nil when the method is off/unknown.
//
//	dvdisaster -d <device> -e <name>.ecc -m<METHOD> -c
func eccGenArgs(device, method, eccPath string) []string {
	m := eccMethodArg(method)
	if m == "" || strings.TrimSpace(device) == "" || strings.TrimSpace(eccPath) == "" {
		return nil
	}
	return []string{"-d", device, "-e", eccPath, "-m" + m, "-c"}
}

// deriveOpticalDevice makes a best-effort guess at the optical device from the burn
// command template (the xorriso/growisofs defaults name /dev/sr0). Returns "" when
// nothing recognisable is present — e.g. Windows ImgBurn, where the operator sets
// burn_ecc_device explicitly.
func deriveOpticalDevice(burnCommand string) string {
	for _, tok := range strings.Fields(burnCommand) {
		t := strings.Trim(tok, `"'`)
		// growisofs names its target as "-Z /dev/dvd=image.iso" — the device is the
		// part before the '='.
		if i := strings.IndexByte(t, '='); i >= 0 {
			t = t[:i]
		}
		for _, pre := range []string{"/dev/sr", "/dev/scd", "/dev/cd", "/dev/dvd"} {
			if strings.HasPrefix(t, pre) {
				return t
			}
		}
	}
	return ""
}

// generateDiscEcc creates the optional dvdisaster ECC file for a freshly-burned,
// VERIFIED disc, stored alongside the staged package as <name>.ecc. Non-fatal by
// contract: the tool may be absent, the device unknown, or the encode may fail —
// any of which only forfeits the extra layer. Returns the ecc path on success.
func (a *App) generateDiscEcc(c *Chunk, cfg Config, progress func(float64, string)) (string, error) {
	if !cfg.burnEccOn() {
		return "", nil
	}
	bin, err := a.dvdisasterBin()
	if err != nil {
		a.Store.Log("ecc", c.Name+": dvdisaster not installed — ECC layer skipped (par2 repair works regardless)")
		return "", err
	}
	device := strings.TrimSpace(cfg.BurnEccDevice)
	if device == "" {
		device = deriveOpticalDevice(cfg.BurnCommand)
	}
	if device == "" {
		a.Store.Log("ecc", c.Name+": no optical device for dvdisaster (set 'burn_ecc_device' in Settings) — ECC layer skipped")
		return "", fmt.Errorf("no optical device configured for dvdisaster ECC")
	}
	eccPath := filepath.Join(c.StagedDir, eccFileName(c.Name))
	args := eccGenArgs(device, cfg.BurnEcc, eccPath)
	if args == nil {
		return "", fmt.Errorf("ecc method %q is not generatable", cfg.BurnEcc)
	}
	progress(0.85, "ECC "+c.Name)
	// Reading a full BD and computing Reed–Solomon is slow; give it room but bound it.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if runErr != nil {
		_ = os.Remove(eccPath) // never leave a half-written .ecc behind
		tail := strings.TrimSpace(string(out))
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		a.Store.Log("ecc", fmt.Sprintf("%s: dvdisaster ECC failed (%v) — ECC layer skipped, par2 repair works regardless: %s", c.Name, runErr, tail))
		return "", runErr
	}
	a.Store.Log("ecc", fmt.Sprintf("%s: dvdisaster %s ECC written → %s", c.Name, strings.ToUpper(normBurnEcc(cfg.BurnEcc)), eccPath))
	return eccPath, nil
}
