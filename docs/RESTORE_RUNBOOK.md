# Mnemosyne Restore Runbook

**Audience: anyone, decades from now, with no Mnemosyne installed.**

Every chunk on every medium carries this same information in its own
`RESTORE.txt` and `*.manifest.json`. Mnemosyne is a convenience, never a
requirement — restoration depends on exactly three ubiquitous,
open-source, standardized programs:

| Step | Tool | Standard / longevity argument |
|------|------|-------------------------------|
| 1. Repair  | `par2` | Parchive 2.0 spec (2003), Reed–Solomon; par2cmdline is open source with many independent implementations |
| 2. Decrypt | `gpg`  | OpenPGP, RFC 4880/9580; AES-256 is a NIST FIPS-197 standard |
| 3. Extract | `tar`  | POSIX pax/ustar, IEEE 1003.1; readable by every Unix since the 1980s and Windows 10+ |

There is **no compression layer anywhere** — media files don't compress,
and removing the layer removes a future dependency. What tar extracts is
byte-identical to the originals.

## What a chunk looks like on the medium

```
NASA-C0003/
  NASA-C0003.tar.gpg            the data: a POSIX tar, AES-256 encrypted
  NASA-C0003.tar.gpg.par2       parity index (over the CIPHERTEXT)
  NASA-C0003.tar.gpg.vol*.par2  parity blocks (~10% redundancy)
  NASA-C0003.manifest.json      full file list, sizes, hashes, key_ref
  RESTORE.txt                   these instructions, chunk-specific
```

Parity is computed over the *ciphertext* deliberately: a damaged medium
can be repaired **without any key present** — repair and custody of
secrets are independent problems.

## The three commands

```bash
# 1. verify, and repair only if needed (no passphrase required)
par2 verify NASA-C0003.tar.gpg.par2
par2 repair NASA-C0003.tar.gpg.par2

# 2. decrypt (prompts for the passphrase of the key_ref in RESTORE.txt)
gpg -d -o NASA-C0003.tar NASA-C0003.tar.gpg

# 3. extract all, or one file
tar -xf NASA-C0003.tar
tar -xf NASA-C0003.tar "2025/Smith Wedding/highlights/0042.NEF"
```

Low disk space? Pipe instead of staging the tar:
`gpg -d NASA-C0003.tar.gpg | tar -x`

## Where the keys are

Passphrases are **never** in the catalog database. They exist in:

1. **Keystore files** (`keystore.json`) — plain JSON, registered in at
   least two locations on different physical devices. Human-readable on
   purpose; protect the files, not the format.
2. **Printed QR cards** — one per key_ref (`GET /api/pipeline/keys/{ref}/qr`),
   payload `MNEMO1|<key_ref>|<passphrase>`. Print, label, fireproof box.

Losing every copy of a key means that chunk is cryptographically gone.
That is what AES-256 promises. This is why key generation *refuses to
run* unless two keystores are registered and in sync.

## Verifying without restoring

`manifest.json` records SHA-256 (or BLAKE3) hashes of both the tar and
the ciphertext. Any era's hashing tool can confirm medium integrity
without touching a key:

```bash
sha256sum NASA-C0003.tar.gpg     # compare to "ciphertext_hash" in the manifest
```

## Media-specific notes

- **LTO/LTFS tape**: mount with any LTFS implementation (IBM Storage
  Archive, HPE StoreOpen, open-source `ltfs`); the chunk appears as a
  normal folder. LTFS is ISO/IEC 20919.
- **Hard drives**: plain filesystem; prefer widely-readable filesystems
  (exFAT/NTFS/ext4) and note the choice on the label.
- **Optical (BD-R)**: chunks planned at 23/46/92 GB burn 1:1 onto
  25/50/100 GB discs; the disc filesystem (UDF) is ISO 13346.
