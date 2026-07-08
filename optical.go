package main

// optical.go — optical burning defaults and the optional dvdisaster ECC layer.
//
// xorriso is the documented DEFAULT burner: it is free, actively maintained,
// cross-platform, scriptable, and (unlike GUI tools) leaves an auditable command
// line. dvdisaster adds an OPTIONAL second layer of protection that par2 cannot:
// sector-geometry (Reed–Solomon) ECC computed over the whole disc image, so a
// scratch that wipes out a run of physical sectors is recoverable even before a
// single file is read. The two layers are independent and complementary — and,
// crucially, neither is required: the RESTORE.txt paragraph says so in plain words,
// because par2 repair of the payload works regardless of whether dvdisaster was
// ever used.

import "strings"

// opticalKinds are the media kinds that are burned discs (as opposed to tape/HDD).
var opticalKinds = map[string]bool{
	"BD-R25": true, "BD-R50": true, "BD-R100": true, "DVD-R": true, "DVD-DL": true,
}

// isOpticalKind reports whether a media kind is a burnable optical disc.
func isOpticalKind(kind string) bool { return opticalKinds[strings.ToUpper(strings.TrimSpace(kind))] }

// defaultBurnCommand is the documented xorriso default: build a filesystem from
// the staged package folder and burn it to the disc, ejecting on success. The
// operator sets their real device in Settings; {SRC}/{LABEL} are substituted at
// burn time (see BurnNext).
const defaultBurnCommand = `xorriso -outdev /dev/sr0 -volid "{LABEL}" -blank as_needed -map "{SRC}" / -commit -eject`

// dvdisasterAugmentHint documents the ALTERNATIVE manual flow: augment an ISO with
// an embedded ECC layer BEFORE burning, so the ECC rides inside the disc itself.
// This differs from Mnemosyne's automatic path (burn_ecc), which computes an
// external <name>.ecc from the disc AFTER it verifies. Kept as reference for
// operators who prefer the embedded style; Mnemosyne never runs it automatically.
const dvdisasterAugmentHint = `xorriso -as mkisofs -V "{LABEL}" -o {LABEL}.iso "{SRC}"  &&  dvdisaster -i {LABEL}.iso -mRS02 -c   # then burn {LABEL}.iso`

// opticalEccParagraph is appended to RESTORE.txt for optical packages. It states
// the belt-and-suspenders truth: dvdisaster is an OPTIONAL extra layer, and par2
// repair of the payload works whether or not it was ever applied. The ECC file for
// a disc is <name>.ecc; because it is computed AFTER the disc verifies, it rides on
// the NEXT disc in the set or is kept in staging (per burn_ecc_carry) — never on
// the disc it protects.
func opticalEccParagraph(name string, eccEnabled bool) string {
	ecc := name + ".ecc"
	applied := "This disc MAY be covered by a dvdisaster ECC file"
	if eccEnabled {
		applied = "This disc is intended to be covered by a dvdisaster ECC file"
	}
	return `
OPTICAL ECC (optional, second layer) — ` + applied + ` named ` + ecc + `.
dvdisaster (open source, dvdisaster.jcea.es) adds sector-geometry Reed–Solomon ECC
over the whole disc image, healing scratches that wipe out runs of physical sectors.
Because ` + ecc + ` is computed after this disc is verified, it lives on the NEXT
disc in the set or in the operator's staging folder — not on this disc. If this disc
develops read errors and the ECC file is at hand:
     dvdisaster -d <this-drive> -e ` + ecc + ` -f      (repair the disc from its ECC)
It is a COMPLEMENT to par2, not a replacement. **par2 repair of the payload works
regardless** — you never need dvdisaster to restore this package:
     par2 repair PAYLOAD.par2
The three-tool restore (par2 -> gpg -> tar) below is the guarantee; disc-level ECC
is insurance on the physical medium.
`
}
