package main

// escrow_docs.go — the human-readable heart of an Escrow Bundle: the philosophy
// note (ESCROW_README.md) and the redistribution-terms statement (LICENSES.md).

import (
	"fmt"
	"strings"
)

// escrowReadmeMD explains, plainly, what the bundle is and — more importantly —
// what it is NOT: a dependency. The static binaries are the practical escrow, the
// source is the audit trail and recompile path, and the three-tool restore never
// needs Mnemosyne at all.
func escrowReadmeMD(plan EscrowPlan) string {
	var b strings.Builder
	b.WriteString("# Escrow Bundle — the archive preserves its own reader\n\n")
	b.WriteString("This folder is **belt-and-suspenders, not a dependency.** Your data can be\n")
	b.WriteString("restored *without anything in here* — see \"You do not need this bundle\" below.\n")
	b.WriteString("It exists so that, decades from now, the exact software that wrote these media\n")
	b.WriteString("travels *with* them: ready to run, and — if you ever doubt it — ready to audit\n")
	b.WriteString("and recompile from source.\n\n")

	b.WriteString(fmt.Sprintf("Generated for Mnemosyne **%s**, mode **%s**.\n\n", plan.Version, plan.Mode))

	b.WriteString("## The three layers, most practical first\n\n")
	b.WriteString("1. **Static binaries (`mnemosyne/`) — the practical escrow.** These are\n")
	b.WriteString("   self-contained executables for Windows, Linux, and macOS. They run on a\n")
	b.WriteString("   compatible OS of the era with **nothing to compile and nothing to install** —\n")
	b.WriteString("   no runtime, no libraries. Verify one against `mnemosyne/SHA-256SUMS.txt`,\n")
	b.WriteString("   then run it. This is what you reach for first.\n")
	b.WriteString("2. **Source tarball (`mnemosyne/mnemosyne-src-*.tar.gz`) — the audit trail and\n")
	b.WriteString("   the recompile path.** If no shipped binary matches your future hardware, or\n")
	b.WriteString("   you simply refuse to trust a binary you did not build, the complete source\n")
	b.WriteString("   is here. It is a normal Go program: `go build ./...` reproduces the tool.\n")
	b.WriteString("3. **Restore-toolchain source (`restore-toolchain/`) — belt-and-suspenders for\n")
	b.WriteString("   the tools that don't even need Mnemosyne.** The complete source of\n")
	b.WriteString("   par2cmdline and GnuPG, under licenses that permit redistributing them *with*\n")
	b.WriteString("   this source (see `LICENSES.md`). You will almost certainly find these tools\n")
	b.WriteString("   pre-installed or one package-manager command away — this is the fallback if\n")
	b.WriteString("   you cannot.\n\n")

	b.WriteString("## You do not need this bundle — the three-tool restore\n\n")
	b.WriteString("Every package on the media is restorable **by hand** with three standard,\n")
	b.WriteString("independently-implemented open-source tools and **no Mnemosyne at all**:\n\n")
	b.WriteString("    par2 repair NAME.tar.gpg.par2      # 1. heal any bit-rot (Reed–Solomon)\n")
	b.WriteString("    gpg  --decrypt --output NAME.tar NAME.tar.gpg   # 2. decrypt (encrypted packages only)\n")
	b.WriteString("    tar  -xf NAME.tar                  # 3. extract — byte-identical originals\n\n")
	b.WriteString("The full walkthrough — including several independent alternatives for each step\n")
	b.WriteString("(MultiPar, Sequoia-PGP, bsdtar, 7-Zip…) — is in the Recovery Kit's\n")
	b.WriteString("`RESTORE_RUNBOOK.md`. That path is the guarantee. This bundle is the insurance\n")
	b.WriteString("on the guarantee.\n\n")

	if plan.Mode == EscrowBinariesOnly {
		b.WriteString("> **This is a `binaries-only` bundle.** To conserve space on this medium the\n")
		b.WriteString("> source tarballs were omitted; the runnable binaries and their checksums are\n")
		b.WriteString("> present. The full source (audit + recompile) always lives in the Recovery\n")
		b.WriteString("> Kit's Escrow Bundle.\n\n")
	}

	if plan.MissingCount > 0 {
		b.WriteString("## Components not present in this bundle\n\n")
		b.WriteString("The following were not cached when this bundle was written and are therefore\n")
		b.WriteString("absent (each is recompilable/obtainable from the URL in `MANIFEST.json`):\n\n")
		for _, name := range plan.MissingNames {
			b.WriteString("- " + name + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Integrity\n\n")
	b.WriteString("`SHA-256SUMS` at the root of this bundle lists every file's hash. Check it with\n")
	b.WriteString("any tool of the era (`sha256sum -c SHA-256SUMS`, `Get-FileHash`, `certutil`).\n")
	return b.String()
}

// escrowLicensesMD states each redistributed component's terms and affirms the
// redistribution basis: GPL/permissive components are shipped WITH their complete
// source; non-redistributable software (IBM LTFS et al.) is never included.
func escrowLicensesMD(plan EscrowPlan) string {
	var b strings.Builder
	b.WriteString("# Licenses & redistribution basis\n\n")
	b.WriteString("Everything in this bundle is redistributed under terms that **permit\n")
	b.WriteString("redistribution together with the complete corresponding source.** For the\n")
	b.WriteString("copyleft components (par2cmdline, GnuPG) that source *is* the accompanying\n")
	b.WriteString("tarball in `restore-toolchain/` — that is precisely what the GPL requires and\n")
	b.WriteString("this bundle honours it. Each tarball's own `COPYING`/`LICENSE` files are the\n")
	b.WriteString("authoritative text; the summaries below are a convenience, not a substitute.\n\n")
	b.WriteString("> **Never included:** IBM Spectrum Archive / LTFS or any other software whose\n")
	b.WriteString("> license forbids redistribution. Where the media use such a format, the\n")
	b.WriteString("> Recovery Kit names where to *obtain* the reader — it is never bundled here.\n\n")

	// Group by kind for a readable table.
	sections := []struct {
		kind, title string
	}{
		{"mnemosyne-source", "Mnemosyne"},
		{"mnemosyne-binary", "Mnemosyne (binaries)"},
		{"toolchain-source", "Restore toolchain"},
		{"reader-source", "Format readers"},
	}
	for _, sec := range sections {
		var rows []escrowComponent
		for _, c := range plan.Components {
			if c.Kind == sec.kind && c.License != "" {
				rows = append(rows, c)
			}
		}
		if len(rows) == 0 {
			continue
		}
		b.WriteString("## " + sec.title + "\n\n")
		seen := map[string]bool{}
		for _, c := range rows {
			key := c.File + "|" + c.License
			if seen[key] {
				continue
			}
			seen[key] = true
			status := "included"
			if !c.Present {
				status = "referenced (not cached — see MANIFEST.json)"
			}
			b.WriteString(fmt.Sprintf("### %s\n\n", c.Name))
			b.WriteString(fmt.Sprintf("- **File:** `%s` (%s)\n", c.File, status))
			b.WriteString(fmt.Sprintf("- **License:** %s\n", c.License))
			if c.LicenseNote != "" {
				b.WriteString("- **Terms:** " + c.LicenseNote + "\n")
			}
			if c.URL != "" {
				b.WriteString("- **Upstream source:** " + c.URL + "\n")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
