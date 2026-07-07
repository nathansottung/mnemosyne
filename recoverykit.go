package main

// recoverykit.go — the "hand this to a stranger in 2060" export.
//
// One self-contained folder: plain-language instructions that name several
// independent open-source tools per step (so no single project going dark
// can strand the archive), a full media inventory, the 30-year runbook, and
// one QR card per key. It deliberately contains secrets (the QR payloads),
// so the generator shouts about storing it like a keystore.

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed docs/RESTORE_RUNBOOK.md
var restoreRunbook []byte

const recoveryKitWarning = "This kit contains key QR codes whose payloads ARE the package passphrases in the clear. " +
	"Anyone holding this folder can decrypt every encrypted package. Store and transport it as securely as your keystores."

// BuildRecoveryKit writes the kit into outputDir/mnemosyne-recovery-kit and
// returns a summary. It never fails the whole job over one unreachable key —
// those are recorded as warnings so the rest of the kit still gets written.
func (a *App) BuildRecoveryKit(outputDir string, progress func(float64, string)) (map[string]any, error) {
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("output_dir required")
	}
	// The kit (with its secret QR cards) is WRITTEN here — never into source data.
	if err := a.Store.AssertOutsideSources(outputDir); err != nil {
		return nil, err
	}
	kit := filepath.Join(outputDir, "mnemosyne-recovery-kit")
	keysDir := filepath.Join(kit, "keys")
	if err := os.MkdirAll(keysDir, 0o755); err != nil {
		return nil, err
	}

	chunks := a.Store.Chunks(0)
	keys := a.Store.KeyMetas()
	volm := map[int]*Volume{}
	for _, v := range a.Store.Volumes() {
		volm[v.ID] = v
	}
	var warnings []string

	progress(0.15, "inventory")
	if err := os.WriteFile(filepath.Join(kit, "MEDIA_INVENTORY.md"), []byte(mediaInventoryMD(chunks, volm)), 0o644); err != nil {
		return nil, err
	}

	progress(0.35, "readme")
	if err := os.WriteFile(filepath.Join(kit, "README_RECOVERY.md"), []byte(recoveryReadmeMD(len(chunks), len(keys))), 0o644); err != nil {
		return nil, err
	}

	progress(0.55, "runbook")
	if err := os.WriteFile(filepath.Join(kit, "RESTORE_RUNBOOK.md"), restoreRunbook, 0o644); err != nil {
		return nil, err
	}

	progress(0.7, "key QR cards")
	qrWritten := 0
	for i, k := range keys {
		// The .txt card never carries the passphrase — only ref + fingerprint.
		card := fmt.Sprintf("Mnemosyne key card\nkey_ref:      %s\nfingerprint:  %s (SHA-256 of the passphrase)\nnote:         %s\n\n"+
			"The passphrase itself is NOT written here in plaintext. It lives only inside\n"+
			"%s.png (QR payload  MNEMO1|<key_ref>|<passphrase>) and in your keystore files.\n",
			k.Ref, k.Fingerprint, k.Note, k.Ref)
		if err := os.WriteFile(filepath.Join(keysDir, k.Ref+".txt"), []byte(card), 0o644); err != nil {
			return nil, err
		}
		pass, err := a.Passphrase(k.Ref)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("key %s: no reachable keystore holds it — QR card omitted (%v)", k.Ref, err))
			continue
		}
		png, err := qrcode.Encode("MNEMO1|"+k.Ref+"|"+pass, qrcode.Medium, 320)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("key %s: QR encode failed (%v)", k.Ref, err))
			continue
		}
		if err := os.WriteFile(filepath.Join(keysDir, k.Ref+".png"), png, 0o644); err != nil {
			return nil, err
		}
		qrWritten++
		progress(0.7+0.28*float64(i+1)/float64(len(keys)), "key QR cards")
	}

	a.Store.Log("recoverykit", fmt.Sprintf("%s (%d packages, %d keys, %d QR cards)", kit, len(chunks), len(keys), qrWritten))
	progress(1.0, "done")
	return map[string]any{
		"output_dir": kit, "chunks": len(chunks), "keys": len(keys),
		"qr_cards": qrWritten, "warning": recoveryKitWarning, "warnings": warnings,
	}, nil
}

func mediaInventoryMD(chunks []*Chunk, volm map[int]*Volume) string {
	var b strings.Builder
	b.WriteString("# Media Inventory\n\n")
	b.WriteString("Generated " + time.Now().UTC().Format(time.RFC3339) + " — every package Mnemosyne has cataloged.\n\n")
	b.WriteString("`Payload SHA-256` is the hash of the file as written to the medium: the ciphertext ")
	b.WriteString("`<name>.tar.gpg` for encrypted packages, the plain tar `<name>.tar` for unencrypted ones ")
	b.WriteString("(the `Payload` column gives each package's exact filename). It is what you check with `sha256sum` before restoring.\n\n")
	if len(chunks) == 0 {
		b.WriteString("_No packages cataloged yet._\n")
		return b.String()
	}
	anyPrivate := false
	b.WriteString("| Package | Payload file | Media | Written destination | Size (bytes) | Payload SHA-256 | Encrypted | Manifest | Key ref | Files |\n")
	b.WriteString("|-------|--------------|-------|---------------------|-------------:|-----------------|-----------|----------|---------|------:|\n")
	for _, c := range chunks {
		dest := c.WrittenDest
		if dest == "" {
			dest = "(not yet written)"
		}
		key := c.KeyRef
		enc := "no"
		if c.Encrypted {
			enc = "yes"
		}
		if key == "" {
			key = "—"
		}
		hash := c.EncHash
		if hash == "" {
			hash = "(not built)"
		}
		manifest := "plaintext"
		if c.PrivateManifest {
			manifest, anyPrivate = "encrypted (.gpg)", true
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | %s | %s | %d | `%s` | %s | %s | %s | %d |\n",
			mdCell(c.Name), payloadName(c), mdCell(c.MediaKind), mdCell(dest), c.EncBytes, hash, enc, manifest, mdCell(key), c.FileCount))
	}
	if anyPrivate {
		b.WriteString("\n**Private media:** packages marked `encrypted (.gpg)` above shipped their file " +
			"listing to the medium as `NAME.manifest.json.gpg` (no plaintext filenames on the tape/disc). " +
			"To read one, decrypt with the package's key passphrase (QR card in `keys/`):\n\n" +
			"    gpg -d NAME.manifest.json.gpg\n")
	}

	// Copies & placement: where each package physically lives, straight from the
	// catalog's copies and (for spanned packages) per-segment records.
	b.WriteString("\n## Copies & placement\n\n")
	b.WriteString("Which physical volume each package lives on, and — for spanned packages — the exact tape each segment was written to. Locate a medium here, then follow its `RESTORE.txt`.\n")
	for _, c := range chunks {
		b.WriteString("\n### " + c.Name + "\n\n")
		writeCopiesTable(&b, c, volm)
		if c.Spanned && len(c.Segments) > 0 {
			writeSegmentsTable(&b, c, volm)
		}
	}
	return b.String()
}

// volDesc renders a volume as "Label (location)" for inline placement text,
// falling back to the raw id or "(unregistered)" when the volume is unknown.
func volDesc(volm map[int]*Volume, id int) string {
	if v := volm[id]; v != nil {
		if v.Location != "" {
			return v.Label + " (" + v.Location + ")"
		}
		return v.Label
	}
	if id == 0 {
		return "(unregistered)"
	}
	return fmt.Sprintf("vol#%d", id)
}

func copyResult(cp Copy) string {
	res := "not yet verified"
	if cp.VerifyOK != nil {
		if *cp.VerifyOK {
			res = "verified ✓"
		} else {
			res = "FAILED ✗"
		}
	}
	if cp.LastVerifiedAt != nil {
		res += " " + cp.LastVerifiedAt.UTC().Format("2006-01-02")
	}
	if cp.Superseded {
		res += " (superseded — re-written)"
	}
	return res
}

func writeCopiesTable(b *strings.Builder, c *Chunk, volm map[int]*Volume) {
	if len(c.Copies) == 0 {
		if c.Spanned {
			b.WriteString("_Recorded as a single spanned copy once every segment is written; see segment placement below._\n")
		} else {
			b.WriteString("_Not yet written to any volume._\n")
		}
		return
	}
	b.WriteString("**Copies**\n\n")
	b.WriteString("| Volume | Barcode | Kind | Location | Last verify | Path |\n")
	b.WriteString("|--------|---------|------|----------|-------------|------|\n")
	for _, cp := range c.Copies {
		label, barcode, kind, loc := "(unregistered)", "—", "—", "—"
		if v := volm[cp.VolumeID]; v != nil {
			label = v.Label
			if v.Barcode != "" {
				barcode = v.Barcode
			}
			if v.Kind != "" {
				kind = v.Kind
			}
			if v.Location != "" {
				loc = v.Location
			}
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
			mdCell(label), mdCell(barcode), mdCell(kind), mdCell(loc), mdCell(copyResult(cp)), mdCell(cp.Path)))
	}
}

func writeSegmentsTable(b *strings.Builder, c *Chunk, volm map[int]*Volume) {
	b.WriteString("\n**Spanned segments** — the payload was byte-split across these media (rejoin per `RESTORE.txt`):\n\n")
	b.WriteString("| Segment | Bytes | Hash (prefix) | Status | Volume | Destination |\n")
	b.WriteString("|---------|------:|---------------|--------|--------|-------------|\n")
	for _, sg := range c.Segments {
		name := fmt.Sprintf("%d of %d", sg.Index, len(c.Segments))
		if sg.Par2 {
			name += " · par2 set"
		}
		hp := "—"
		if sg.Hash != "" {
			n := len(sg.Hash)
			if n > 16 {
				n = 16
			}
			hp = "`" + sg.Hash[:n] + "…`"
		}
		dest := sg.Dest
		if dest == "" {
			dest = "(not yet written)"
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %s | %s | %s | %s |\n",
			mdCell(name), sg.Bytes, hp, mdCell(sg.Status), mdCell(volDesc(volm, sg.VolumeID)), mdCell(dest)))
	}
}

// mdCell keeps a value from breaking the one-line table row.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
}

func recoveryReadmeMD(nChunks, nKeys int) string {
	return fmt.Sprintf(`# Mnemosyne Recovery Kit

**Audience: anyone, decades from now, with no Mnemosyne software installed.**

This folder is everything a stranger needs to read the archive back off its
media by hand. Mnemosyne is a convenience; it is never required. Restoration
needs only widely-implemented, standardized, open-source tools — and for every
step below there are **several independent programs** to choose from, so no
single project going dark can strand your data.

> ⚠️ **SECURITY WARNING**
> %s
>
> The `+"`keys/`"+` folder holds one QR code per key. **Each QR image encodes a
> passphrase in the clear** (payload `+"`MNEMO1|<key_ref>|<passphrase>`"+`). Treat
> this entire kit like a keystore: same locked, off-site, access-controlled
> storage. The `+"`.txt`"+` card beside each QR lists only the key_ref and
> fingerprint — never the passphrase in readable text.

This kit describes %d package(s) and %d key(s). See `+"`MEDIA_INVENTORY.md`"+` for
the full per-package table and `+"`RESTORE_RUNBOOK.md`"+` for the deep-dive.

---

## What a package looks like on a medium

Each package is a small folder on its medium. The payload's filename tells you
which kind it is: **`+"`NAME.tar.gpg`"+`** (encrypted) or **`+"`NAME.tar`"+`**
(plaintext). The `+"`.par2`"+` parity files always follow that payload name.

    NAME/
      NAME.tar.gpg   or  NAME.tar   the payload (see "Encrypted vs plaintext" below)
      <payload>.par2                parity index (Reed–Solomon error correction)
      <payload>.vol*.par2           parity blocks (typically ~10%% redundancy)
      NAME.manifest.json            full file list, hashes, encrypted flag, key_ref,
                                    and payload_file (the exact payload filename)
      RESTORE.txt                   package-specific copy of these steps

There is **no compression anywhere** — what you extract is byte-identical to
the originals.

## Encrypted vs plaintext packages — check first

Open `+"`NAME.manifest.json`"+` (any text editor) and read the `+"`\"encrypted\"`"+` field
(or `+"`\"payload_file\"`"+`), or check the **Encrypted** / **Payload file** columns in
`+"`MEDIA_INVENTORY.md`"+`:

- **encrypted: true**  — the payload is `+"`NAME.tar.gpg`"+`, OpenPGP/AES-256
  ciphertext. Do all three steps: **repair → decrypt → extract**. You need the
  passphrase for its `+"`key_ref`"+` (scan the matching QR in `+"`keys/`"+`, or read it from a keystore).
- **encrypted: false** — the payload is `+"`NAME.tar`"+`, a *plain tar* (no `+"`.gpg`"+`
  suffix, nothing to decrypt). **Skip decryption entirely.** Do two steps:
  **repair → extract**. No key, no passphrase. (Media written by older Mnemosyne
  versions may still name a plaintext payload `+"`NAME.tar.gpg`"+`; it is likewise a
  plain tar — extract it directly.)

---

## Step 1 — Repair (both package types, no passphrase needed)

Parity is computed over the payload file, so a scratched or bit-rotted medium
can be healed *without any key present*.

Pick any PAR2 2.0 implementation:
- **par2cmdline** — the reference CLI (github.com/Parchive/par2cmdline)
- **MultiPar** — Windows GUI/CLI (multipar), reads the same %%.par2 files

Use the `+"`.par2`"+` next to the payload — `+"`NAME.tar.gpg.par2`"+` for an encrypted
package, `+"`NAME.tar.par2`"+` for a plaintext one:

    par2 verify NAME.tar.gpg.par2     # encrypted  (or NAME.tar.par2 for plaintext)
    par2 repair NAME.tar.gpg.par2     # only if verify reports damage

## Step 2 — Decrypt (encrypted packages ONLY)

The payload is standard OpenPGP symmetric encryption (RFC 4880/9580, AES-256).
Pick any OpenPGP implementation:
- **GnuPG (gpg)** — gnupg.org; on Windows via Gpg4win
- **Sequoia-PGP (sq)** — sequoia-pgp.org, a modern independent Rust implementation

Get the passphrase for the package's key_ref by scanning `+"`keys/<key_ref>.png`"+`
(the text after the last `+"`|`"+` in the QR payload) or reading it from a keystore.

    # GnuPG:
    gpg --decrypt --output NAME.tar NAME.tar.gpg
    # Sequoia (equivalent):
    sq decrypt --output NAME.tar NAME.tar.gpg

Low on disk? Pipe straight into extraction instead of staging the tar:

    gpg --decrypt NAME.tar.gpg | tar -x

## Step 3 — Extract (both package types)

The payload is a POSIX pax/ustar tar (IEEE 1003.1), readable by:
- **GNU tar** (Linux/most Unix)
- **bsdtar / libarchive** (`+"`tar`"+` on macOS and Windows 10+)
- **7-Zip** (7-zip.org) — opens `+"`.tar`"+` on Windows via GUI or `+"`7z x`"+`

    # encrypted package, after Step 2 produced NAME.tar:
    tar -xf NAME.tar
    # plaintext package — extract the payload (NAME.tar) directly, no decryption:
    tar -xf NAME.tar
    # one file only (either case), give its path from the manifest:
    tar -xf NAME.tar "some/path/inside/the/archive.ext"

---

## Verifying integrity without restoring

`+"`NAME.manifest.json`"+` records the SHA-256 of the payload (key `+"`ciphertext_hash`"+`,
which for plaintext packages is simply the tar's hash) and the payload's exact
filename (`+"`payload_file`"+`). Hash that file with any tool of the era — substitute
`+"`NAME.tar`"+` below for a plaintext package:

    # Linux / macOS / Git Bash:
    sha256sum NAME.tar.gpg
    # Windows PowerShell:
    Get-FileHash NAME.tar.gpg -Algorithm SHA256
    # Windows classic cmd:
    certutil -hashfile NAME.tar.gpg SHA256

A match means the medium is intact; you can do this with no key present.

## Where the passphrases live

Never in the catalog database. They exist only in:
1. **Keystore files** (`+"`keystore.json`"+`) — plain JSON, kept on ≥2 different
   physical devices.
2. **The QR cards in this kit's `+"`keys/`"+` folder** — one PNG per key_ref.

Losing every copy of a key means that package's encrypted data is gone for good —
that is exactly what AES-256 guarantees. Guard this kit accordingly.
`, recoveryKitWarning, nChunks, nKeys)
}
