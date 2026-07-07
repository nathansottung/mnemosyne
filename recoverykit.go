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
	kit := filepath.Join(outputDir, "mnemosyne-recovery-kit")
	keysDir := filepath.Join(kit, "keys")
	if err := os.MkdirAll(keysDir, 0o755); err != nil {
		return nil, err
	}

	chunks := a.Store.Chunks(0)
	keys := a.Store.KeyMetas()
	var warnings []string

	progress(0.15, "inventory")
	if err := os.WriteFile(filepath.Join(kit, "MEDIA_INVENTORY.md"), []byte(mediaInventoryMD(chunks)), 0o644); err != nil {
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

func mediaInventoryMD(chunks []*Chunk) string {
	var b strings.Builder
	b.WriteString("# Media Inventory\n\n")
	b.WriteString("Generated " + time.Now().UTC().Format(time.RFC3339) + " — every package Mnemosyne has cataloged.\n\n")
	b.WriteString("`Payload SHA-256` is the hash of the file as written to the medium (the `.tar.gpg`): ")
	b.WriteString("the ciphertext for encrypted packages, the plain tar for unencrypted ones. It is what you check with `sha256sum` before restoring.\n\n")
	if len(chunks) == 0 {
		b.WriteString("_No packages cataloged yet._\n")
		return b.String()
	}
	anyPrivate := false
	b.WriteString("| Package | Media | Written destination | Size (bytes) | Payload SHA-256 | Encrypted | Manifest | Key ref | Files |\n")
	b.WriteString("|-------|-------|---------------------|-------------:|-----------------|-----------|----------|---------|------:|\n")
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
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %d | `%s` | %s | %s | %s | %d |\n",
			mdCell(c.Name), mdCell(c.MediaKind), mdCell(dest), c.EncBytes, hash, enc, manifest, mdCell(key), c.FileCount))
	}
	if anyPrivate {
		b.WriteString("\n**Private media:** packages marked `encrypted (.gpg)` above shipped their file " +
			"listing to the medium as `NAME.manifest.json.gpg` (no plaintext filenames on the tape/disc). " +
			"To read one, decrypt with the package's key passphrase (QR card in `keys/`):\n\n" +
			"    gpg -d NAME.manifest.json.gpg\n")
	}
	return b.String()
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

Each package is a small folder on its medium:

    NAME/
      NAME.tar.gpg            the payload (see "Encrypted vs plaintext" below)
      NAME.tar.gpg.par2       parity index (Reed–Solomon error correction)
      NAME.tar.gpg.vol*.par2  parity blocks (typically ~10%% redundancy)
      NAME.manifest.json      full file list, sizes, hashes, encrypted flag, key_ref
      RESTORE.txt             package-specific copy of these steps

There is **no compression anywhere** — what you extract is byte-identical to
the originals.

## Encrypted vs plaintext packages — check first

Open `+"`NAME.manifest.json`"+` (any text editor) and read the `+"`\"encrypted\"`"+` field,
or check the **Encrypted** column in `+"`MEDIA_INVENTORY.md`"+`:

- **encrypted: true**  — the `+"`.tar.gpg`"+` is OpenPGP/AES-256 ciphertext.
  Do all three steps: **repair → decrypt → extract**. You need the passphrase
  for its `+"`key_ref`"+` (scan the matching QR in `+"`keys/`"+`, or read it from a keystore).
- **encrypted: false** — the `+"`.tar.gpg`"+` is a *plain tar* that merely keeps the
  `+"`.tar.gpg`"+` name so the media layout is uniform. **Skip decryption entirely.**
  Do two steps: **repair → extract**. No key, no passphrase.

---

## Step 1 — Repair (both package types, no passphrase needed)

Parity is computed over the payload file, so a scratched or bit-rotted medium
can be healed *without any key present*.

Pick any PAR2 2.0 implementation:
- **par2cmdline** — the reference CLI (github.com/Parchive/par2cmdline)
- **MultiPar** — Windows GUI/CLI (multipar), reads the same %%.par2 files

    par2 verify NAME.tar.gpg.par2
    par2 repair NAME.tar.gpg.par2      # only if verify reports damage

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
    # plaintext package — extract the payload directly, no decryption:
    tar -xf NAME.tar.gpg
    # one file only (either case), give its path from the manifest:
    tar -xf NAME.tar "some/path/inside/the/archive.ext"

---

## Verifying integrity without restoring

`+"`NAME.manifest.json`"+` records the SHA-256 of the payload (key `+"`ciphertext_hash`"+`,
which for plaintext packages is simply the tar's hash). Compare it with any hashing
tool of the era:

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
