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

## Docker — the NAS-side brain

Mnemosyne also ships as a small multi-arch container image on GHCR, built by the
same release tag:

```
ghcr.io/nsottung/mnemosyne:latest      # or pin a version, e.g. :2.1.0
```

The image is multi-stage: a static, CGO-free binary on top of Alpine with the
three restore-story tools baked in (`apk add tar gnupg par2cmdline` — Alpine's
`tar` is GNU tar). It exposes port **7821** and declares two volumes:

- **`/data`** — `catalog.json`, `config.json`, daily backups. *Persist and back
  this up.*
- **`/staging`** — scratch for package builds (big + fast; safe to lose).

### What runs in the container, and what doesn't

The container is the **NAS-side brain**: it catalogs your datasets, plans and
**builds packages**, and **mirrors** to spinning drives — everything that is
pure file I/O over the network-attached storage. The **hardware-in-the-loop**
workflows — **tape** and **optical** burning, the **docking** flow for a stack of
loose drives, and **SMART**/tape-drive diagnostics — want a device physically
attached, so they belong on a **native binary running next to the hardware**
(download one above). Point that native instance at the same NAS shares; the
catalog is one JSON file, so both can operate on the same data. *Bytes-to-metal
lives at the metal; the container does the thinking.*

### Auth is mandatory off-box

Binding anything other than localhost (which a container must, to be reachable)
**requires a bearer token** — the binary refuses to start otherwise. Set a strong
secret via **`MNEMO_AUTH_TOKEN`** (env, recommended) or **`auth_token`** in
`config.json`; then every `/api` request needs `Authorization: Bearer <token>`.
The web UI prompts once and remembers it for the browser session. The static UI
itself is public (so it can load and prompt); only `/api` is gated.

```bash
# Quick run (creates a token, persists /data, mirrors a read-only source):
docker run -d --name mnemosyne -p 7821:7821 \
  -e MNEMO_AUTH_TOKEN="$(openssl rand -hex 32)" \
  -v mnemo-data:/data -v mnemo-staging:/staging \
  -v /mnt/tank/photos:/sources/photos:ro \
  ghcr.io/nsottung/mnemosyne:latest
```

Or use the committed **[`docker-compose.yml`](docker-compose.yml)** (put the token
in a `.env` as `MNEMO_AUTH_TOKEN=…`).

### Source datasets: always mount `:ro`

Mount every source dataset **read-only** (`:ro`). Mnemosyne is already read-only
toward sources by design (its `AssertOutsideSources` guard refuses any write into
a registered source root), but `:ro` enforces that in the **kernel** — even a bug
or a crafted request physically cannot write to your originals, because the write
syscall fails below the application entirely. Belt *and* suspenders:

```yaml
volumes:
  - /mnt/tank/photos:/sources/photos:ro     # scan/mirror point at /sources/...
  - /mnt/tank/documents:/sources/documents:ro
```

### TrueNAS SCALE

TrueNAS SCALE runs containers natively, which makes it an ideal host for the
brain:

1. **Apps → Discover Apps → Custom App** (or *Install via YAML* and paste the
   compose above).
2. **Image:** `ghcr.io/nsottung/mnemosyne:latest`.
3. **Environment:** add `MNEMO_AUTH_TOKEN` = a long random secret.
4. **Storage / host-path volumes:**
   - a dataset → **`/data`** (read-write; this is your catalog — snapshot it),
   - a scratch dataset → **`/staging`** (read-write),
   - each pool dataset you want to protect → **`/sources/<name>`** set
     **read-only** (SCALE exposes a per-mount read-only toggle — turn it **on**;
     it becomes a kernel `:ro` bind).
5. **Port:** publish **7821**.
6. Browse to `http://<truenas-ip>:7821`, paste the token, then **Scan** the
   `/sources/...` paths and **Mirror** or **Build packages** to another dataset or
   an attached drive.

For the tape drive bolted into that same TrueNAS box, run the **native Linux
binary** on the host (or in a privileged container with the tape `/dev` node
passed through) — the build/catalog it shares with the app is the same
`catalog.json`.

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
- **Build-time verification** proves the two custody links that used to be only
  fingerprinted, *before* the package can reach media — *because a bad artifact
  must never be faithfully preserved:*
  - **Contents (stage-vs-source)** — the staged tar is stream-read with Go's
    `archive/tar` (no extraction, no external tool), every member hashed and
    compared byte-exact against the catalog's source hash; any mismatch, missing,
    or extra member **fails the build with the file named**.
  - **Decrypt round-trip** — the ciphertext is decrypted with `gpg -d` piped
    straight into a hasher (*no plaintext ever touches disk*) and compared to the
    tar hash; a mismatch fails with *"ciphertext does not decrypt to the verified
    tar — encryption step corrupted."* Plaintext packages skip this — their
    payload **is** the verified tar.

  The attestation `build_verified: {contents, decrypt_roundtrip}` is written into
  every medium's `manifest.json`, so the tapes carry their own proof. Config
  `build_verify` defaults to **`full`**; **`fast`** skips both checks with an
  explicit amber warning in the UI and manifest — *archival correctness is the
  default, speed is the opt-out.* **Honest cost:** full verification adds roughly
  one read pass + one decrypt pass per package build — the price of never
  archiving a lie.
- **Spanning** splits a package larger than one medium across several, with
  eject-and-continue and full reboot-resume — *because a 200 GB package
  shouldn't be un-backup-able just because your discs are 100 GB.*

### 🪞 Mirror — the browsable complement to packages

Mnemosyne backs up two ways, and they are peers — every archive can have both:

|                | **Packages** | **Mirrors** |
|----------------|--------------|-------------|
| On the medium  | sealed `tar → gpg → par2` units | **plain files**, source tree preserved |
| Best for       | **tape & optical** (LTO, BD-R) | **spinning drives / SSDs** |
| Encryption     | AES-256 (optional) | none — *un-encrypted by design* |
| Error-correction | par2 parity | none (the filesystem is the store) |
| To read back   | par2 → gpg → tar | open the file in any file manager |
| Integrity      | read-back verified, sealed | **copy-then-verified** per file |

- **Mirror backup** copies an archive's folders to one (or several) target
  Volumes as **plain files that preserve the source tree** — so you, or anyone,
  can browse and restore them decades from now with nothing but a file manager,
  *no Mnemosyne, no key, no unpack step.* Each file is copied with v1's
  **copy-then-verify** discipline: staged to `<name>.mnemo_tmp`, hashed, read
  back off the destination, and only then **atomically renamed** into place — so
  a half-written or corrupted file never appears under its real name.
- **Multi-volume at once** — mirror to several drives concurrently, one job per
  volume, *because filling three parents'-house drives shouldn't be three
  sequential waits.* Writes honor the **throttle (MB/s)** to keep drives cool.
- **Mirrors are copies too** — each mirrored file is recorded as a **verified
  file-level copy** on its volume, the *same record* an adopted mirror produces,
  so **drift and coverage count mirrors and packages identically**. Two verified
  copies in two locations is the goal whether they're sealed tapes or live
  drives. The volume's inventory sidecar is refreshed so the medium
  self-documents.
- *Packages are the sealed, verified, encrypted form for cold media; mirrors are
  the live, browsable form for the spinning drive on the shelf.* Use whichever
  fits the medium — or both, for belt-and-suspenders.

### ✍️ Write
- **RAM ring buffer** decouples reading from writing so a slow tape never
  starves — *because tape and optical punish stop-start writes.*
- **Throttle (MB/s)** caps the write rate — *because sustained writes cook
  cheap SSDs; pegging at ~35 MB/s keeps them cool, and the buffer proves the
  read side still ran fast.*
- **Read-back verify** re-hashes what actually landed on the medium — *because
  "the write returned success" is not the same as "the bytes are on the disc."*

### ✅ Verify

Every hop of the custody chain is now independently proven — nothing is trusted
transitively:

```
source → [contents-verified] tar → [roundtrip-verified] ciphertext
       → [stream-verified] write → [read-back-verified] medium
```

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

### 🛡 Protection profiles & the six-status model

3-2-1 is not one number — it has **three dimensions**: **three copies**, on
**two distinct kinds of media**, with **one offsite**. A *Profile* expresses all
three, and every file is evaluated against its resolved profile across all three
at once. Only **verified** copies count toward requirements.

**Offsite is a property of the Volume, not the Profile**, because "is this copy
in another building?" is a fact about a physical medium, not about a policy: the
same 3-2-1 profile is satisfied or not depending on *where the tapes actually
live*. So each Volume carries an **Onsite/Offsite** flag (the free-text location
stays, for the human), and flipping a volume offsite→onsite re-derives the status
of every file whose copies land on it. Without that flag, the "1" in 3-2-1 is
unknowable.

Ship with three built-in profiles (immutable, but duplicatable as a starting
point for your own):

| Built-in | copies | distinct kinds | offsite | verify due | intent |
|---|---|---|---|---|---|
| Single Copy | 1 | 1 | 0 | 12 mo | low-value / re-creatable data |
| 3-2-1 Standard | 3 | 2 | 1 | 12 mo | the canonical rule; default for new Archives |
| Pre-Deletion Hold | 4 | 2 | 1 | 6 mo | over-protect data whose SOURCE is about to be deleted |

**Assign per Archive and per folder path.** A profile is assignable to a whole
Archive and, optionally, overridden on any folder path within it. Resolution is
**nearest-ancestor-wins**: a folder without its own assignment inherits the
closest ancestor's, ultimately the Archive's. Inherited assignments render muted
("3-2-1 Standard · inherited"); explicit ones render solid. The canonical
example: Archive **Photography** on *3-2-1 Standard*, with its subfolder
**To-Delete-2020** explicitly on *Pre-Deletion Hold*.

**Every file gets exactly one of six statuses** (folders take the worst status
among their children), evaluated across all three dimensions. Status is **always
shown as colour + icon + text label together — never colour alone**:

| Status | Meaning | Colour | Icon |
|---|---|---|---|
| UNASSIGNED | no profile resolves | `#8A938C` gray | ○ |
| NOT_BACKED_UP | 0 qualifying copies | `#A03123` red | ✕ |
| PARTIAL | some protection, but at least one dimension short — the UI states which, e.g. "2/3 copies · kinds ok · 0/1 offsite" | `#9A6B1F` amber | ◐ |
| COMPLETE | all three dimensions met, all verifies current | `#2E5E4E` green | ✓ |
| OVER_COMPLETE | exceeds requirements | `#1E3D8F` blue | ✓+ |
| OUT_OF_POLICY | copies on disallowed media kinds, verifies older than `verify_due_months`, or a profile/assignment change invalidated prior compliance | `#6B2D86` purple | ⚠ |

Any profile edit, assignment change, or volume offsite-flag change triggers a
**status recomputation job** that surfaces newly `OUT_OF_POLICY` / `PARTIAL`
counts in a toast and on the dashboard — *never silently.* The old
"under-protected" idea is now `PARTIAL`, with its dimension breakdown.

#### Designing your profiles

Think in terms of *how much you'd grieve losing it*, then pick the dimensions to
match — the two built-ins that anchor the range are illustrative:

- **A wedding you shot** is irreplaceable but not under threat: the couple isn't
  deleting anything, you just must never lose it. *3-2-1 Standard* (3 copies, 2
  media kinds, 1 offsite) is exactly right — enough redundancy and geographic
  spread that no single fire, theft, or drive death takes it out.
- **A client folder whose SOURCE is about to be wiped** is different: once they
  reclaim the NAS, *your copies are the only copies*, so the moment of maximum
  risk is right after deletion. *Pre-Deletion Hold* over-protects on purpose — 4
  copies and a tighter 6-month re-verify window — so you carry extra redundancy
  through exactly the window where a mistake is unrecoverable. Assign it to the
  specific to-delete folder, let the rest of the archive stay on 3-2-1, and drop
  the folder back to Standard once the source is safely gone.

To make your own, **duplicate** the closest built-in and adjust the three
dimensions (and, if you care, restrict the allowed media kinds so a copy landing
on the wrong medium reads as `OUT_OF_POLICY`).

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
- **Drive identity capture** records each volume's real **serial, model, and
  capacity**, read from the OS behind a mounted path (Windows `Get-Disk` via a
  PowerShell/CIM one-shot; Linux `lsblk`; macOS `diskutil`) — *so a dead drive is
  identifiable by serial, and the catalog knows a 4 TB HGST from a 2 TB Seagate.*
  Captured at register and adopt time, or on demand with **Detect drive
  identity…**. It surfaces on the volume cards, the Recovery Kit inventory, and
  the label. *Caveat:* external docks and USB-SATA bridges frequently report the
  **enclosure's** serial (or none) rather than the drive's — Mnemosyne records
  whatever the bridge reports and flags it, so treat a bridged serial as a hint,
  not a fingerprint. Resolution failure is never fatal; the field just stays
  blank.
- **Media health (SMART)** reads a drive's own mortality self-report via
  `smartctl` (smartmontools) — overall status, temperature, power-on hours, and
  reallocated/pending sectors (or the NVMe equivalents) — and raises a red
  **"migrate copies off this volume"** advisory when reallocated/pending sectors
  appear or SMART reports FAILING. Snapshots are recorded per volume so trends
  show across dock sessions. The hard part — mapping a mounted volume to its
  physical device — reuses the identity plumbing (drive letter → disk number →
  `/dev/pdN` on Windows; `lsblk` parent on Linux). It reads only on the volume
  view and dock ingest, never in the write path, with timeouts and silent-but-
  logged failures; if `smartctl` isn't installed the feature simply hides behind
  an install hint.
  > **What SMART does and doesn't tell you.** SMART is a *mortality* signal, not
  > an *integrity* one. It estimates how likely a drive is to **die**; it says
  > nothing about whether the bytes already written are **intact**. A drive that
  > reports "PASSED" can still hand back a bit-rotted file, and a drive flagged
  > "FAILING" may still read its verified copies perfectly. So Mnemosyne treats
  > SMART strictly as an early-warning nudge to move copies *before* a drive
  > dies — it never marks data good or bad. Only the custody-chain hashes
  > (read-back, verify, restore) prove your bytes are still your bytes. Health is
  > a complement to verification, never a substitute for it.
- **Tape-drive diagnostics** *(optional)* read a tape drive's own self-report
  (TapeAlert flags + LOG SENSE pages) and render it in plain language:
  **CLEAN NOW / CLEAN PERIODIC → "cleaning cartridge recommended"**, media/drive
  error flags as **amber/red advisories**, plus power-on hours and lifetime bytes
  read/written when the tool exposes them. It surfaces on the **Volumes** page and
  as a **"check before a big write"** nudge in the write dialog. Three tool
  families are supported, auto-probed on `PATH` (or set an explicit path in
  Settings): **IBM ITDT** (`itdt`), **sg3_utils** (`tapeinfo` / `sg_logs`), and
  **HPE Library & Tape Tools** (`hp_ltt`). When none is present the feature hides
  behind an install hint. Each tool has its own defensive parser checked against
  captured sample outputs in `testdata/`.
  > **Windows:** IBM's **ITDT** is a free download from IBM Fix Central and is the
  > easiest way to get TapeAlert on Windows. **This feature reads drive
  > diagnostics only — it NEVER issues movement or write commands** (no rewind,
  > space, load/unload, erase, or write). It runs strictly *outside* the write
  > path, on the Volumes view and the pre-write nudge, with per-command timeouts,
  > and any failure is silent-but-logged. Like SMART, it is a maintenance/
  > mortality *signal* that **complements** hash verification — a "clean me" or
  > "I'm failing" hint from the drive, never a claim that the data on a cartridge
  > is intact. Only the custody-chain hashes prove that.
- **Print label** generates a self-contained, print-ready HTML label per volume:
  a **Code128** barcode of the volume's barcode (scans straight back into the
  lookup box), a **QR** of the volume ID, and the human-readable label, kind,
  capacity, serial, and date — at common label sizes. A volume with no barcode is
  offered the **next one from a configurable scheme** (`barcode_scheme` prefix →
  `NSP-0001`, `NSP-0002`, …) right at label time — *because an unlabeled tape is a
  future mystery, and the barcode you print should be the barcode you can scan.*
- **Copies** record every (package × volume) with its verify state — *so a
  search answers "on tape NSP-0007 (office safe) and HDD ARCH-03 (parents'
  house), both verified 2026-03."*
- **Drift (Rescan & compare)** classifies the source vs. what's backed up:
  UNARCHIVED (present on disk, not yet in any package) / MODIFIED / MISSING /
  MOVED, per file-type — *because you need to know a `.NEF` went missing, and
  not be drowned in expected `.xmp` churn.*

### 🧲 Dock — ingest a stack of legacy drives
- **Guided, resumable, hands-off.** Pick the Archive(s) to reconcile against,
  then dock old backup drives one at a time. Mnemosyne **watches** for each
  newly-inserted drive (polling mounts, diffed against session start) and, on one
  click, does everything: identifies the drive by **serial**, hashes every file
  and **matches it by content** against your archives, records the matches as
  verified copies, writes an inventory sidecar onto the drive, and shows a big
  **"DONE — safe to eject. Insert the next drive."** — *because migrating a shelf
  of unlabeled drives should be a rhythm, not a research project.*
- **Content match, not filename match** — a photo that was reorganized into a
  different folder on the old drive still counts, because matching is by SHA-256.
  Each drive reports *matched / historical (older versions) / unrecognized /
  unreadable*.
- **Recognized by serial** — re-insert a drive you already processed and
  Mnemosyne knows it (even on a different letter) and offers **re-verify**, not a
  duplicate adopt. Idempotent across sessions.
- **Resumable across days** — the session persists; close the app and reopen it
  to exactly where you were, coverage and all.
- **Running coverage + a report you keep** — a live dashboard ("62% of *Photos*
  now has ≥1 verified copy; 9,412 files still on 0 copies") and an exportable
  markdown **session report** listing every drive's serial, label, and contents
  summary — the documentation trail for the migration.
- **Read-only toward your sources** — the NAS archive folders are *only ever
  hashed* for comparison; the one write onto media is the drive's own sidecar,
  guarded so it can never touch a source path. The view says so plainly.

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

**How much scratch (staging) space do I need?**
**Not the size of your archive — just enough for one package.** Mnemosyne builds
packages **one at a time** and frees each one's staging *before* starting the
next, so scratch space only ever has to hold a single package's build peak, and
that same space is reused for every package.

Per-package build peak:
- **Plaintext** — the tar *is* the payload, so peak ≈ **1× the package + par2**
  (~1.05–1.1× the media size).
- **Encrypted** — `gpg` reads the tar and writes the ciphertext, so the two
  briefly coexist: peak ≈ **2× the package** during the encrypt step (a bit more
  if you turn off `delete_tar_after_encrypt`).

**Worked example — 100 TB archive → LTO-8 (~12 TB tapes):** that's ~9 packages of
~12 TB each. You do **not** need 100 TB (let alone 250 TB) of scratch — you need
room for **one** package: about **~13 TB** for plaintext (or ~24 TB if encrypted),
**reused for all ~9 packages**. Staging is just a folder — point it at any drive
with that much free (Settings), including an external drive; Mnemosyne streams
from it to the destination. The app shows these exact numbers with a
green/amber/red verdict after planning, before each build, and on the Home view
(all from one endpoint, `GET /api/space-advice`).

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
