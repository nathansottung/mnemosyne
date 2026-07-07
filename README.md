# Mnemosyne — Archival Vault

[![CI](https://github.com/nsottung/mnemosyne/actions/workflows/ci.yml/badge.svg)](https://github.com/nsottung/mnemosyne/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/nsottung/mnemosyne?logo=github&color=2e5e4e)](https://github.com/nsottung/mnemosyne/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Single binary](https://img.shields.io/badge/deploy-single%20binary-2e5e4e)](#quickstart)
[![Restore: par2 · gpg · tar](https://img.shields.io/badge/restore-par2%20%C2%B7%20gpg%20%C2%B7%20tar-informational)](docs/RESTORE_RUNBOOK.md)

**Mnemosyne turns folders of files into self-contained archival *packages* and
writes them onto LTO tape, hard drives, or optical discs — each package
restorable decades from now with three stock, open-source tools (`par2`,
`gpg`, `tar`) even if this program no longer exists.** It is one small Go
binary (no installer, no runtime, no database service): a local web app that
catalogs your files, packages them at a size that fits your medium, encrypts
and adds error-correction, writes them through a RAM buffer with read-back
verification, and remembers exactly which physical volume — in which drawer,
in which house — each verified copy lives on.

> Not another sync tool. Mnemosyne is for **cold, offline, decades-long
> preservation**: the stuff you write once, put on a shelf, and need to trust
> you can still read at your kid's wedding.

---

## Source safety guarantee

**Mnemosyne never modifies your source data. This is an enforced invariant, not a
promise you have to trust.**

- **Sources are only ever opened for reading.** Scanning, hashing, the `tar`
  archive step (`tar -c` reads, never writes what it archives), drift rescans,
  and verification all open source files strictly read-only (`O_RDONLY`). The
  only things that change are the catalog and the copies you write to *media* —
  never the originals. Each such read path carries a `SOURCE READ-ONLY:` audit
  comment in the code.
- **Every writable destination is validated against your source roots.** Before
  Mnemosyne writes anything, the target is checked by a single central guard
  (`AssertOutsideSources`). If a destination resolves to a path *inside* any
  registered Archive source folder, the operation is refused with:

  > `refusing: <path> is inside source root <root>; Mnemosyne never writes into source data`

  This covers the **staging folder** (set-time and build-time), **write / span /
  burn destinations**, **restore output**, the **recovery-kit output**, and even
  **keystore paths** (keystores get rewritten, so they must stay out of source
  data too).
- **The only thing that ever changes your originals is you.** Delete or move a
  source file yourself and drift will report it — but Mnemosyne's own code has no
  path that writes into a source root.

---

## Download

Prebuilt, self-contained binaries for every release are on the
**[Releases page](https://github.com/nsottung/mnemosyne/releases/latest)** —
pick the zip for your OS/architecture:

| Zip | Platform |
|-----|----------|
| `mnemosyne-windows-amd64.zip` | Windows (x64) |
| `mnemosyne-linux-amd64.zip`   | Linux server / NAS (x64) |
| `mnemosyne-linux-arm64.zip`   | Raspberry Pi / ARM Linux |
| `mnemosyne-darwin-arm64.zip`  | Apple Silicon macOS |
| `mnemosyne-darwin-amd64.zip`  | Intel macOS |

Each zip contains the binary plus `README.md`, `LICENSE`, and the
`RESTORE_RUNBOOK.md` so the archive stays hand-restorable even offline. Verify
your download against **`SHA-256SUMS.txt`** (attached to every release):

```
sha256sum -c SHA-256SUMS.txt          # Linux / macOS / Git Bash
# or on Windows PowerShell:
(Get-FileHash mnemosyne-windows-amd64.zip -Algorithm SHA256).Hash
```

Builds are produced by the tag-triggered [release workflow](.github/workflows/release.yml)
(pure Go, `CGO_ENABLED=0`, `-ldflags "-s -w -X main.appVersion=<tag>"`).

## Quickstart

**1. Download & run.** Grab the binary for your OS (see [Download](#download)) —
it's self-contained.

```
mnemosyne.exe                # Windows — then open http://127.0.0.1:7821
./mnemosyne-linux-amd64      # Linux (server / NAS / Pi)
./mnemosyne-darwin-arm64     # Apple Silicon
```
Flags: `-port 7821 -data ~/.mnemo`. Open the printed URL; the **Preflight**
panel (Settings) checks that `tar`/`gpg`/`par2` are installed and tells you
how to get any that are missing.

**2. Your first archive, in five steps** (all in the browser):

| # | Do this | What happens |
|---|---------|--------------|
| 1 | **Vault → Create archive** (name it "Personal") | an *Archive* is a body of work you keep together |
| 2 | **Scan folder…** and point at a directory | every file is walked and SHA-256 hashed into the catalog |
| 3 | **Plan packages…**, pick a media size (e.g. BD-R25), set redundancy | files are grouped into media-sized *packages* |
| 4 | **Build** a package, then **Write…** to a drive/mount/folder | tar → (encrypt) → par2, streamed to media and **read-back verified** |
| 5 | **Register the Volume** (barcode + location like "office safe") | now you know *where the bytes physically live* |

That's a verified copy on real media. Do it again to a second volume in a
different location and the package is fully protected.

> **Fastest first run:** encryption is ON by default and requires two
> registered keystores (see [Security](#security--privacy)). To try the flow
> without that, untick *Encrypt packages* in the Plan dialog — you still get
> tar + par2 + verification, just no AES layer.

---

## The journey — features, and *why*

Mnemosyne follows the life of your data. Each step earns its place:

### 📇 Catalog
- **Scan** SHA-256-hashes every file at the source — *because a backup you
  can't prove is intact is just hope; the source hash is the root of the
  custody chain.*
- **Search** any filename to see which package and which physical volume(s)
  hold it — *because in 15 years the only question that matters is "where are
  the Smiths' photos?"*

### 📦 Package
- **Plan** groups files into media-sized **packages** (folder-aware, parity-
  budgeted) — *because a package is the OAIS "archival unit": self-contained,
  one per medium, independently restorable.*
- **Build** produces `tar → gpg → par2` with **no compression anywhere** —
  *because compression is a future dependency and a single-bit-error amplifier;
  raw bytes + Reed–Solomon parity age better.*
- **Spanning** splits a package larger than one medium across several, with
  eject-and-continue and full reboot-resume — *because a 200 GB package
  shouldn't be un-backup-able just because your discs are 100 GB.*

### ✍️ Write
- **RAM ring buffer** decouples reading from writing so a slow tape never
  starves — *because tape and optical punish stop-start writes.*
- **Throttle (MB/s)** caps the write rate — *because sustained writes cook
  cheap SSDs; pegging at ~35 MB/s keeps them cool, and the buffer proves the
  read side still ran fast.*
- **Read-back verify** re-hashes what actually landed on the medium — *because
  "the write returned success" is not the same as "the bytes are on the disc."*

### ✅ Verify
- **Verify a medium / campaign** re-checks one tape (or everything on it) in
  one click — *because bit-rot is silent; you find it on a schedule, not during
  a restore.*
- **Copy-level verification** — every check (write read-back, manual verify,
  verify campaign, burn verify) records its result on the *specific copy* it
  read, and appends to the package's verify-event history. A failed medium
  marks **that copy** failed; it does **not** mark the whole package failed
  while the staged payload or another verified copy is intact. The package's
  status is derived from the best evidence it has — *because one rotted tape
  out of three copies is a copy problem, not a data-loss event, and the UI
  should say so: "copy on ARCH-01: FAILED · copy on TAPE-01: verified."* Only a
  corrupt **staged artifact** or a failed **build** marks a package `FAILED`.
- **Re-write this copy** — one click re-runs the write of a failed copy to the
  same volume from staging, read-back verifies it, and restores redundancy. The
  failed copy is kept in history (marked *superseded*) so the verify trail is
  never lost.
- **Verify history + due tracking** flags copies not re-checked within
  `verify_due_months` — *because "I verified it once in 2026" is not a plan.*
- **Redundancy policy** flags any package with fewer than `required_copies`
  verified copies as *under-protected* — *because two copies in two locations
  is the actual goal, and gaps should be visible at a glance.*

### 🔎 Find & track
- **Adopt existing media** — catalog archives written *before* Mnemosyne (or by
  hand with `tar`+`par2`) without rewriting a byte. Point **Volumes → Adopt
  media…** at a mount; every `*.tar` / `*.tar.gpg` payload is hashed and recorded
  as an `ADOPTED-VERIFIED` package with a verified copy on the volume. Adoption
  is idempotent (payloads already cataloged are skipped by hash), so pointing it
  at your own written chunks is a safe no-op — *because your legacy 100 TB should
  become first-class without a migration project.* See
  [Adopting existing media](#adopting-existing-media).
- **Volumes** are physical media with barcode, kind, and free-text location;
  scan a barcode to jump straight to a tape's contents — *because a shelf of
  unlabeled LTO cartridges is a museum of unknowns.*
- **Copies** record every (package × volume) with its verify state — *so a
  search answers "on tape NSP-0007 (office safe) and HDD ARCH-03 (parents'
  house), both verified 2026-03."*
- **Drift (Rescan & compare)** classifies the source vs. what's backed up:
  UNARCHIVED (present on disk, not yet in any package) / MODIFIED / MISSING /
  MOVED, per file-type — *because you need to know a `.NEF` went missing, and
  not be drowned in expected `.xmp` churn.*

### 🔓 Restore
- **Three-command restore**, documented on every medium in `RESTORE.txt` —
  *because the whole point is that you (or a stranger) can get the data back
  with `par2`/`gpg`/`tar` alone, no Mnemosyne required.*
- **Restore drill** rejoins spanned tapes and runs the full repair → decrypt →
  extract → compare against source hashes — *because the only real proof is
  bytes back out, matching the bytes that went in.*
- **Recovery Kit** exports a single folder — plain-language instructions,
  media inventory, per-key QR cards, the runbook — *because your future self
  may have the tapes and the keystores but no memory of how any of this worked.*

---

## Adopting existing media

You almost certainly have data on disks and tapes from before Mnemosyne. The
**Adopt media…** action (Volumes view) brings it into the catalog *in place* —
nothing is copied, re-tarred, or re-encrypted. Point it at a mount and pick the
archive + volume; it scans for payloads (`*.tar` / `*.tar.gpg`, flat or one
folder deep), and for each one hashes the payload and records an
`ADOPTED-VERIFIED` package with a verified **copy** on that volume.

**What is always known** (recorded for every adopted package):

- the **payload SHA-256** as it exists on the medium (the integrity anchor —
  future verifies compare against exactly this);
- whether it is **encrypted** (`.tar.gpg`) or **plaintext** (`.tar`);
- whether a **par2** set rides alongside it;
- **where it physically lives** (the volume + path).

**What is known only if the archive carries it:**

- **Contents (file list + per-file source hashes)** come from a `manifest.json`
  if one is present (a `manifest.json.gpg` is decrypted by trying your keystore
  passphrases). Without a manifest the package is marked **"listing unknown —
  restore to enumerate contents."**
- **Deep adopt** (a checkbox) fills the listing *without* a manifest by streaming
  the payload through `tar -tvf` (works for plaintext tars, or encrypted ones
  whose key is in a keystore). This gives you **paths and sizes but not source
  hashes** — the original per-file hashes are unknowable without the manifest.
- **Key reference** for encrypted payloads comes from the manifest (or the
  keystore key that decrypted it); an encrypted payload with no known key can be
  cataloged and verified by hash, but not listed or restored until the key turns
  up.

**Idempotent by design:** a payload whose hash is already in the catalog is
reported as *skipped-duplicate*, never double-cataloged — so re-running adoption,
or accidentally adopting a Mnemosyne-written chunk, changes nothing. Adopted
packages behave like native ones everywhere else: they count toward redundancy,
show in search and on volumes, and can be verified and restored with the same
`par2`/`gpg`/`tar` doctrine.

---

## Terminology

Mnemosyne uses professional archival vocabulary aligned with the **OAIS
reference model (ISO 14721)**:

- **Archive** — a body of work you keep together (e.g. *Photography Business*,
  *Personal*).
- **Package** — one self-contained, media-sized archival unit: a `tar` of files
  + par2 parity + `manifest.json` + `RESTORE.txt`. The plain-English equivalent
  of an OAIS **AIP** (Archival Information Package).
- **Volume** — a physical medium you can hold and locate: a tape, drive, or
  disc, with a barcode, kind, and location.
- **Copy** — one package written on one volume. Two verified copies on volumes
  in *different locations* is the redundancy goal.

---

## Prerequisites

**Three ubiquitous tools do the actual work** — they are the entire restore
story, so Mnemosyne shells out to them rather than reimplementing them:

| Tool | Purpose | Windows | Linux | macOS |
|------|---------|---------|-------|-------|
| `tar` | package/extract | built into Win10+ (`tar.exe`) | preinstalled | preinstalled |
| `gpg` | encrypt/decrypt | [Gpg4win](https://gpg4win.org) | `apt install gnupg` | `brew install gnupg` |
| `par2` | parity repair | `choco install par2cmdline` | `apt install par2` | `brew install par2` |

The **Preflight** panel checks all three and shows install hints for anything
missing. Restoration needs only these three — multiple independent
implementations exist for each (par2cmdline / MultiPar; GnuPG / Sequoia;
GNU tar / bsdtar / 7-Zip), so no single project going dark can strand you.

### LTFS for LTO tape *(optional, separate install)*
Writing to **LTO tape** needs an LTFS driver so the tape mounts as a normal
folder. LTFS drivers are **proprietary or separately-licensed** — this
MIT-licensed repo only *references* them, never bundles them:

- **IBM Storage Archive Single Drive Edition** — <https://www.ibm.com/products/storage-archive>
- **HPE StoreOpen (Standalone)** — <https://www.hpe.com/> (search "StoreOpen")
- **LinearTapeFileSystem/ltfs** (open source) — <https://github.com/LinearTapeFileSystem/ltfs>

Preflight reports a detected LTFS mount but never *requires* one — HDD and
optical need no LTFS.

### Optical burning *(optional)*
Discs can't stream through the RAM buffer, so the **Burn** tab drives your
existing burner via a command template (`{SRC}` = staged folder, `{LABEL}` =
package name). Examples: **ImgBurn** (Windows), **growisofs** / **xorriso**
(Linux). See [Optical burn queue](#optical-burn-queue).

---

## Screenshots

> 📸 **Placeholders — capture these and drop them in [`docs/img/`](docs/img/).**
> See [`docs/img/README.md`](docs/img/README.md) for the shot list.

| | |
|---|---|
| ![Home dashboard](docs/img/home.png) | ![Getting Started checklist](docs/img/getting-started.png) |
| *Home: pipeline map + per-archive health board* | *Guided first run when the catalog is empty* |
| ![Vault & archives](docs/img/vault.png) | ![Packages lifecycle](docs/img/packages.png) |
| *Vault: archives, search, drift* | *Packages: build → write → verify* |
| ![Volumes & copies](docs/img/volumes.png) | ![Verify & redundancy](docs/img/verify.png) |
| *Volumes: where the bytes live* | *Redundancy & verify history* |

The **Home** view is the default landing page: an at-a-glance archive health
board (files cataloged, packages by status, under-protected and verify-due
counts, drift) with a clickable Catalog → Package → Write → Verify pipeline
strip. On an empty catalog it becomes a Getting Started checklist that detects
real state (tools, staging, keystores, first archive/scan/package/verified copy)
and links each unchecked step to the exact spot.

---

## Performance

- **Parallel scan** — hashing runs on a worker pool (`min(8, CPU cores)`), so
  cataloging is disk-bound, not single-core.
- **par2 is usually the slowest stage.** Mnemosyne passes `par2_extra_args`
  (default `-t0` = all threads) and silently retries without it if your `par2`
  rejects the flag. For a big speedup, point the `tools.par2` override at
  [**par2cmdline-turbo**](https://github.com/animetosho/par2cmdline-turbo) — a
  drop-in, dramatically faster build.
- **Staging locality** — set the staging folder on a big, fast volume (the NAS
  itself is ideal); the whole payload is staged there before it streams to
  media, and per-package peak usage is estimated up front.
- **Write throttle** — cap writer-side MB/s for thermal control; ring-buffer
  telemetry (read MB/s, write MB/s, min buffer fill, starvation events) is
  shown per package as proof the buffer decoupled a fast read from a capped write.
- **Build timings** per stage (tar, hash, encrypt, par2) are recorded on each
  package so you can see exactly where the time goes.

---

## Security & privacy

- **What encryption covers.** Encrypted packages are GnuPG **symmetric
  AES-256** over the tar (no compression). The `.tar.gpg` payload on the medium
  is ciphertext; par2 is computed over it so a scratched tape can be *repaired
  without any key present* — repair and custody are independent problems.
- **Where secrets live.** Per-package random ~288-bit passphrases live **only**
  in keystore JSON files; the catalog stores fingerprints, never secrets. Print
  a QR card per key for the fireproof box.
- **The enforced 2-keystore rule.** Encrypted builds **refuse to run** until
  ≥2 keystore paths (on different physical devices) are registered and in sync
  — because a single copy of a key is a single point of total loss.
- **`private_media` mode.** By default the on-media `manifest.json` (filenames,
  sizes, hashes) is **plaintext**, so a lost tape leaks filenames even though
  the data is encrypted. Turn on `private_media` and the medium instead carries
  `manifest.json.gpg` (same package key) with **no plaintext listing**;
  `RESTORE.txt` stays filename-free and documents how to decrypt it. Your own
  NAS keeps the catalog and staging plaintext — the threat model is *the medium
  leaving your custody*, not your server.

| On the medium (encrypted package) | default | `private_media` |
|---|---|---|
| `NAME.tar.gpg` (payload) | encrypted | encrypted |
| `NAME.manifest.json` (filenames) | **plaintext** | **absent** |
| `NAME.manifest.json.gpg` | — | encrypted |
| `RESTORE.txt` | plaintext, no filenames | plaintext, no filenames |

- **Catalog-loss fallback.** *Read manifest from mounted medium* finds
  `NAME.manifest.json.gpg` on a found tape and decrypts it with the keystore —
  and if the keystores are gone too, RESTORE.txt documents the manual `gpg -d`.

---

## FAQ

**Is my data locked into this tool?**
No — and you can prove it in three commands without Mnemosyne installed. Every
medium carries a `RESTORE.txt`:
```
par2 verify  NAME.tar.gpg.par2     # repair if the medium rotted (no key needed)
gpg  -d -o   NAME.tar NAME.tar.gpg # decrypt (skip for plaintext packages)
tar  -xf     NAME.tar              # extract the original files
```
That's it. See [docs/RESTORE_RUNBOOK.md](docs/RESTORE_RUNBOOK.md).

**What if this project dies / GitHub vanishes?**
Nothing changes for your archives. Mnemosyne is a convenience layer over
standardized formats (POSIX tar, OpenPGP/AES-256, Parchive 2.0). The three
restore tools each have multiple independent open-source implementations, and
the runbook + Recovery Kit are written for a stranger with none of this
software.

**What if I lose the catalog (`catalog.json`)?**
Each medium is self-describing: `manifest.json` (the file list) + `RESTORE.txt`
(the procedure) travel with the data. *Read manifest from mounted medium*
rebuilds the listing from a found tape; a full restore drill gets the files
back. The catalog is an index for convenience, not a dependency for recovery.

**What if I lose the encryption keys?**
Then encrypted packages are cryptographically gone — that is what AES-256
promises. This is exactly why builds **enforce ≥2 keystores on different
devices**, and why you should print the per-key QR cards and store them
off-site. Plaintext packages have no such risk.

**Why not compression / dedup / a fancy container format?**
Every layer is a future dependency and a bit-error amplifier. Media files
barely compress anyway. Raw tar + external parity is the most boring, longest-
lived option — which is the point.

---

## Build from source
```
go build -o mnemosyne .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mnemosyne.exe .
```
One dependency (QR-code generation); everything else is the Go standard
library. Requires **Go 1.22+**. See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md)
and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Optical burn queue
Optical media can't stream through the RAM buffer — you feed one blank disc,
wait, label it, repeat. The **Burn** tab is a persistent queue (one disc per
package) that **survives reboots**: a disc caught mid-burn resets to PENDING on
restart (it may be a coaster), so you re-burn on a fresh blank. Each "Burn next
disc" shells out to your command template:

```
# Windows — ImgBurn
"C:\Program Files (x86)\ImgBurn\ImgBurn.exe" /MODE BUILD /BUILDMODE DISC ^
  /SRC "{SRC}" /VOLUMELABEL "{LABEL}" /START /CLOSESUCCESS /NOIMAGEDETAILS

# Linux — growisofs (UDF, label from {LABEL})
growisofs -Z /dev/sr0 -R -J -udf -V "{LABEL}" "{SRC}"
```
`{SRC}` = the package's staged folder, `{LABEL}` = the package name. If a
**burn verify mount** is set, Mnemosyne read-back hashes the burned disc
against the package's `enc_hash` before marking it DONE.

## API notes (backward compatibility)
The REST API keeps its original paths, with OAIS-named aliases added:

- `/api/archives…` — working **alias** for `/api/collections…`
- `/api/packages…` — working **alias** for `/api/chunks…`

The original `/api/collections` and `/api/chunks` routes are **deprecated but
fully functional**, and persisted `catalog.json` field names (`chunks`,
`collection_id`, `spanned_chunks`, …) are **unchanged** — existing catalogs and
scripts keep working. New integrations should prefer the aliases.

## GitHub repo setup
Suggested **topics** (Settings → Topics): `archival`, `lto`, `tape`, `ltfs`,
`par2`, `gpg`, `digital-preservation`, `go`. Suggested description:
*"Self-contained archival packages onto tape, drives, and optical — restorable
decades from now with three stock open-source tools."*

## License
[MIT](LICENSE) © 2026 Nathan Sottung. The three restore tools and any LTFS
driver are separate software under their own licenses; this repo references but
never bundles them.
