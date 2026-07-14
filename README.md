# Mnemosyne — Archival Vault

[![CI](https://github.com/nathansottung/mnemosyne/actions/workflows/ci.yml/badge.svg)](https://github.com/nathansottung/mnemosyne/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/nathansottung/mnemosyne?logo=github&color=2e5e4e)](https://github.com/nathansottung/mnemosyne/releases/latest)
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

## New here? Start with the Handbook

**Not a developer? [Read the User Handbook →](docs/handbook/00-what-is-this.md)**
— a plain-language, task-based guide written for a careful novice (a photographer,
musician, filmmaker, or family archivist). It walks you through
[installing and the first run](docs/handbook/01-install-and-first-run.md),
[setting up safely](docs/handbook/02-set-up-safely.md),
[your first backup](docs/handbook/03-your-first-backup.md),
[the 3-2-1 setup](docs/handbook/04-the-3-2-1-setup.md),
[tape](docs/handbook/05-tape.md),
[discs & drives](docs/handbook/06-discs-and-drives.md),
[checking on your archive](docs/handbook/07-checking-on-your-archive.md),
[getting files back](docs/handbook/08-getting-files-back.md), and
[the Recovery Kit](docs/handbook/09-the-recovery-kit.md) — plus a
[glossary](docs/handbook/glossary.md) and
[troubleshooting](docs/handbook/troubleshooting.md). The rest of this README is
the technical/maintainer reference.

> **Why not restic / borg / Bacula / dar / Canister?** — the honest,
> one-paragraph-each answer (plus how Mnemosyne relates to git-annex, LTFS, BagIt /
> Archivematica, the commercial media-archive tools, and complements like
> dvdisaster and rclone) lives in **[docs/COMPARISON.md](docs/COMPARISON.md)**.

### No NAS? Start here

Mnemosyne doesn't need a server or a single "source of truth" folder. If your
stuff is **scattered across a pile of external drives** with overlapping copies and
no master, make a **sourceless archive**: at create time, pick *"scattered across
drives."* Then **adopt each drive** — Mnemosyne hashes its loose files and folds
them into the archive's **deduped union** (identical content across drives counts
once). The union *is* the archive; there's no source to scan or drift against.

The other half is **Locations** — first-class physical places like *"Shoe Box #1"*
(onsite) or *"Grandma's house"* (offsite). You assign each drive to a location, and
the **3-2-1 "offsite" math reads straight from it**: a file that lives on two drives
in two locations shows **2 copies across 2 locations**, and if one of those places
is offsite, its offsite requirement is satisfied. Flip a location to offsite once
and every drive there re-counts at a stroke — no per-drive bookkeeping. So even with
"just a shelf of USB drives," you get real 3-2-1 coverage tracking.

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
**[Releases page](https://github.com/nathansottung/mnemosyne/releases/latest)** —
pick the zip for your OS/architecture.

**Supported platforms:** Windows (x64, ARM), Linux (x64, ARM — Raspberry Pi), macOS (Intel, Apple Silicon).

| Zip | Platform |
|-----|----------|
| `mnemosyne-windows-amd64.zip` | Windows (x64) |
| `mnemosyne-windows-arm64.zip` | Windows on ARM |
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
ghcr.io/nathansottung/mnemosyne:latest   # or pin a release tag from the Releases page
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
  ghcr.io/nathansottung/mnemosyne:latest
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
2. **Image:** `ghcr.io/nathansottung/mnemosyne:latest`.
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

### Catalog
- **Scan** SHA-256-hashes every file at the source — *because a backup you
  can't prove is intact is just hope; the source hash is the root of the
  custody chain.*
- **Search** any filename to see which package and which physical volume(s)
  hold it — *because in 15 years the only question that matters is "where are
  the Smiths' files?"*

### Package
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

### Mirror — the browsable complement to packages

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

### ️ Write
- **RAM ring buffer** decouples reading from writing so a slow tape never
  starves — *because tape and optical punish stop-start writes.*
- **Throttle (MB/s)** caps the write rate — *because sustained writes cook
  cheap SSDs; pegging at ~35 MB/s keeps them cool, and the buffer proves the
  read side still ran fast.*
- **Read-back verify** re-hashes what actually landed on the medium — *because
  "the write returned success" is not the same as "the bytes are on the disc."*

### ✓ Verify

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

### How much integrity do I need?

Mnemosyne has many independent integrity knobs — build-verify depth, par2
redundancy, routine verify level, the re-verify window, read-back. Rather than
make you reason about each one, three **presets** bundle them into a single
comprehensible choice (and every knob stays individually editable — edit one and
the preset reads *Custom*):

| Preset | build verify | par2 | routine verify level | verify due | read-back after write |
|---|---|---|---|---|---|
| ARCHIVAL (default) | contents + decrypt round-trip | 10% | B (full) | 12 months | always |
| BALANCED | contents only | 10% | C routine / B on due date | 12 months | always |
| FAST | none (amber-flagged) | 5% | C | 24 months | always |

**Use ARCHIVAL for anything irreplaceable; use FAST only for re-creatable data.**
BALANCED sits between: it still proves each package contains your files, but
routine re-checks sample the ends (level C) and only go full (level B) on the
due date. FAST *skips proving the tar contains your files* — a package could
preserve a mistake without warning — which is why it is flagged amber
everywhere and reserved for data you could simply regenerate.

The preset is selectable globally in **Settings → Integrity**, and overridable
**per archive** (an Integrity field beside the archive's protection Profile — the
Profile says *how many copies*, Integrity says *how hard each copy is proven*).
Lowering integrity anywhere requires an explicit amber confirmation.

**Read-back after write is never configurable off.** Every write is immediately
re-read and hash-checked before the copy is trusted — there is no toggle to
disable it, because writing unverified media would defeat the entire point of a
backup. (If you go looking for that switch, the UI tells you so where it would
be.)

Each package's **manifest** and each **volume inventory** record the effective
integrity settings used at build/write time (in the `build_verified` block), so
the media self-document how much assurance they were created with — a FAST-built
package says so, on the medium, decades from now.

### Tiered verification — levels A / B / C

Re-hashing a 100 TB mirror set in full, on a schedule, is often impractical — so
verification has three levels, letting you trade cost against assurance **without
ever weakening what "verified" means**:

| Level | Name | What it checks | Cost | May satisfy COMPLETE? |
|---|---|---|---|---|
| A | Census | file exists + size matches catalog | seconds/TB | NO — advisory only |
| B | Full | complete content hash equals catalog hash | full read | YES — the only level that does |
| C | Sample | exists + size + hash of first and last 4 MiB | fast | NO — advisory only |

**Only Level B satisfies protection requirements.** Levels A and C are advisory:
they record intact-so-far evidence and are shown in the amber/advisory style, but
they never flip a file to `COMPLETE` and never refresh the `verify_due` clock —
only a Level-B pass does. A level selector (defaulting to **B**) appears on the
operations where cost is the concern — **mirror re-verify, verify campaigns, and
dock-session re-verification** — each with a one-line cost/assurance note. Package
payload verification and write read-back are **always Level B**, no option.

Every verify event records its level, and every place a verify result appears
names it — *"verified (B, full) · 2026-07-08"* versus *"checked (C, sample)"* — so
a cheap check can never be mistaken for a full one. (Level C hashes only the first
and last 4 MiB, so a mid-file corruption is invisible to it by design; that is
exactly why C can't satisfy `COMPLETE`.)

### Protection profiles & the six-status model

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
shown as colour + shape + text label together — never colour alone**:

| Status | Meaning | Colour | Shape |
|---|---|---|---|
| UNASSIGNED | no profile resolves | `#8A938C` gray | dot |
| NOT_BACKED_UP | 0 qualifying copies | `#A03123` red | dot |
| PARTIAL | some protection, but at least one dimension short — the UI states which, e.g. "2/3 copies · kinds ok · 0/1 offsite" | `#9A6B1F` amber | dot |
| COMPLETE | all three dimensions met, all verifies current | `#2E5E4E` green | dot |
| OVER_COMPLETE | exceeds requirements | `#1E3D8F` blue | dot |
| OUT_OF_POLICY | copies on disallowed media kinds, verifies older than `verify_due_months`, or a profile/assignment change invalidated prior compliance | `#6B2D86` purple | dot |

Any profile edit, assignment change, or volume offsite-flag change triggers a
**status recomputation job** that surfaces newly `OUT_OF_POLICY` / `PARTIAL`
counts in a toast and on the dashboard — *never silently.* The old
"under-protected" idea is now `PARTIAL`, with its dimension breakdown.

#### Designing your profiles

Think in terms of *how much you'd grieve losing it*, then pick the dimensions to
match — the two built-ins that anchor the range are illustrative:

- **A finished project you delivered** (a wedding you shot, an album you mixed, a
  film you cut) is irreplaceable but not under threat: the client isn't deleting
  anything, you just must never lose it. *3-2-1 Standard* (3 copies, 2 media kinds,
  1 offsite) is exactly right — enough redundancy and geographic spread that no
  single fire, theft, or drive death takes it out.
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

### Finalize — close the box and label it

There's a moment in every archival workflow that software usually skips: the
*ceremony* of declaring a volume **done**. You've written the copies, verified
them, and now the tape or drive is going into the vault. Finalize is that
moment made explicit — the digital equivalent of taping the box shut, signing
the lid, and shelving it.

It is deliberately **gated**. Finalizing enforces three preconditions, and each
is there because skipping it is how archives rot:

- **Every copy on the volume verified within N days** (`finalize_verify_days`,
  default 30) — you don't seal a box you haven't recently confirmed is intact.
- **Free-space buffer respected** (`buffer_pct`, default 5%) — *full drives die
  young*, so a nearly-full volume isn't vault-ready.
- **SMART not failing** — when drive-health data exists and `smart_block_finalize`
  is on, a drive reporting failure/advisory won't seal.

You *can* override a failing precondition, but it takes a deliberate act: you
type the volume's label to confirm and give a reason, and the forced seal — with
exactly which checks it overrode — is written into the audit log and the seal
record. No silent force.

On success the ceremony:

1. writes a **finalization record** (who, when, package count, bytes) to the
   catalog **and** to a `MNEMOSYNE_SEAL/` sidecar on the volume itself;
2. regenerates the volume's **inventory + catalog snapshot** onto that sidecar,
   so the medium self-documents for whoever finds it decades later;
3. marks the volume **SEALED** — the catalog now refuses every write to it
   (write, re-write copy, span, mirror, adopt) until an explicit, audit-logged
   **unseal**;
4. opens the **printable label** as the last step — close the box, print the
   label, shelve it.

The Volumes view shows sealed volumes distinctly (a blue SEALED tag), and the
volume page carries the full seal/unseal history. Unsealing is one click, needs a
reason, and is logged — because re-opening a sealed box should leave a mark.

### Format sustainability — will you still open these in 2050?

A perfectly-verified copy is worthless if nothing can *read* the file. So
Mnemosyne keeps an **editable format registry** and shows a **per-archive
census**: every file extension tagged with a longevity **tier**, a one-line
rationale, the open-source **reader projects** that decode it, and — where one
exists — a **migration** suggestion.

**The tiers rate the *format*, by two criteria only:**

1. **Is the format publicly documented?**
2. **Are there multiple independent, healthy open-source readers?**

| Tier | Meaning | Examples |
|---|---|---|
| **OPEN** | public spec, multiple independent readers | `.dng` `.tif` `.jpg` `.png` `.pdf` `.txt` `.wav` `.flac` |
| **DOCUMENTED-PROPRIETARY** | closed spec, but healthy open-source readers | `.nef` `.cr3` `.arw` `.psd` `.heic` |
| **AT-RISK** | single-vendor, weak/no open readers | vendor catalogs (`.lrcat`), obscure project files |
| **UNKNOWN** | not in the registry | anything unrated |

**Crucially, vendor financial health is *not* a criterion.** A format from a
giant company can be AT-RISK (a proprietary catalog only one app reads), and a
format from a defunct camera line can be DOCUMENTED-PROPRIETARY (because
`libraw`/`dcraw` decode it robustly, forever, regardless of the vendor).

**Worked example — NEF vs DNG.** A Nikon `.NEF` is **DOCUMENTED-PROPRIETARY**:
Nikon never published the spec, but `libraw`, `dcraw`, RawTherapee and darktable
all decode it well, so it is not endangered. Adobe's `.DNG` is **OPEN**: it is
publicly documented, TIFF/EP-based, and read by many independent tools. So the
registry's suggestion for NEF is *"retain the original NEF **and** consider
archiving a DNG sibling for extra longevity."* Note what it does **not** say: it
never suggests deleting the NEF. **Tiers are advisory. Mnemosyne never proposes
deleting an original — ever.** The census is a "keep an eye on this" signal, not
an alarm.

The census surfaces on the dashboard as *"97% of bytes in OPEN/DOCUMENTED
formats,"* with a per-archive breakdown table (colours map to the palette: OPEN
green, DOCUMENTED amber-muted, AT-RISK amber, UNKNOWN gray). The census **and the
per-format reader references travel with your media** — written into the Recovery
Kit (`FORMATS.md`) and every volume inventory sidecar — so *"which tools open a
.NEF?"* is answered on the medium itself, decades from now. The registry is
`formats.json`, embedded in the binary and **overridable**: drop your own
`formats.json` in the data dir to correct or extend it (entries merge by
extension, yours winning).

### Roles & starter templates — any creative discipline

Photography is one profile among peers, not the assumption. Alongside the longevity
tier, the registry tags each extension with a **role** — a small, discipline-neutral
taxonomy that says what a file *is* in a workflow (how irreplaceable it is), orthogonal
to how readable its format is:

| Role | What it is | Examples |
|---|---|---|
| **ORIGINALS** | irreplaceable masters | camera RAWs, audio stems/multitracks (`.wav` `.aif`), camera video originals (`.braw` `.r3d` `.mxf`), layered image masters (`.psd` `.tif`) |
| **DELIVERABLES** | rendered / exported outputs | `.jpg` `.png` exports, mastered `.mp3` `.flac`, delivery `.mp4` `.mov` |
| **SIDECARS** | per-asset metadata beside the content | `.xmp`, `.cue`, subtitles (`.srt` `.vtt`) |
| **PROJECT-FILES** | application project/catalog state (**CRITICAL**) | `.lrcat`, `.als`, `.flp`, `.logicx`, `.prproj`, `.drp`, `.aep` |
| **OTHER** | everything unclassified | — |

**PROJECT-FILES are flagged CRITICAL**: an Ableton `.als`, a Premiere `.prproj`, or a
Lightroom `.lrcat` is your *edit/arrangement state* — not your stems, footage, or
negatives — and losing it loses every edit you built, so the tool shouts about it (and
its migration note always says *archive the originals and a rendered mixdown/delivery;
never rely on the project file as the archive*).

Ship on top of this a **set of starter templates** — **Photographer**, **Musician**
(`music/{year}/{project}/` with separate *stems* vs *masters* routes), **Filmmaker**
(footage / projects / deliverables), and **General** (`{year}/{category}/{collection}`)
— each with its own editable category vocabulary and its own word for a grouping of
files (an "Event" for photographers, a "Session"/"Project"/"Collection" for the rest).
Routes are written with a discipline-neutral, aliased token set, so `{event}`,
`{collection}`, `{session}`, and `{project}` all name the same grouping.

**Metadata is discipline-neutral too.** Images still read **EXIF** (capture time +
camera body) with zero dependencies; audio and video read a **created date + duration**
via **ffprobe** when it is installed — an *optional* auto-detected tool, treated exactly
like `smartctl`: present, a musician's or filmmaker's library clusters into sessions by
date the way a photographer's does; absent, those fields are simply empty and ingest
never fails.

### Find & track
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

### Treemap — where is my *risk*

A disk-usage treemap you already know from tools like WizTree — rectangles sized
by bytes — but **colored by protection risk instead of by folder.** Open it from
any Archive card (or the **Treemap** tab in the Archive workbench): every folder
and file is a rectangle whose area is its bytes and whose fill is its
protection status (the same six colors used everywhere else —
not backed up, partial, complete, over-complete, out-of-policy,
unassigned), a folder taking the worst status of anything inside it. Hover for
name + size + status (color **and** icon **and** text — never color alone); click
a folder to zoom in, breadcrumb to zoom back out; the legend tallies bytes per
status for the level you're looking at. It is computed **entirely from the
catalog's recorded sizes — it never re-walks the disk** — and a million-file
archive stays instant because the server aggregates one level at a time and folds
thousands of tiny folders into a single "other" block. A toggle re-tints the map
by **drift state** (unchanged / modified / missing / moved / unarchived) wherever
a Rescan & compare report exists, turning *"where is my space?"* into *"where is
my risk?"* at a glance.

<!-- SCREENSHOT: docs/img/treemap.png — Archive treemap, one large red (NOT_BACKED_UP) block dominating a field of green, drift toggle top-right -->

### Dock — ingest a stack of legacy drives
- **Guided, resumable, hands-off.** Pick the Archive(s) to reconcile against,
  then dock old backup drives one at a time. Mnemosyne **watches** for each
  newly-inserted drive (polling mounts, diffed against session start) and, on one
  click, does everything: identifies the drive by **serial**, hashes every file
  and **matches it by content** against your archives, records the matches as
  verified copies, writes an inventory sidecar onto the drive, and shows a big
  **"DONE — safe to eject. Insert the next drive."** — *because migrating a shelf
  of unlabeled drives should be a rhythm, not a research project.*
- **Content match, not filename match** — a file that was reorganized into a
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

### Restore
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

### Version retention

Mnemosyne **never had the power to delete an old version of a file.** Once bytes
are sealed into a package on a tape or disc, they are there for good — that is the
whole promise of write-once archival media. Yet until now a rescan would quietly
*overwrite* a file's hash in the catalog: the medium still held the old bytes, but
the catalog had forgotten they existed. Version retention makes the catalog stop
pretending otherwise.

- **A rescan retains, it does not overwrite.** When a file's content changes, the
  previous `{hash, size, mtime, first_seen}` is moved into an append-only
  per-file **version history** (stamped with when it was superseded) instead of
  being discarded. The current content stays on the file; the prior versions line
  up behind it as `v1, v2, …`.
- **Every retained version stays locatable.** Package and mirror membership is
  *content-addressed*, so the catalog can still point at exactly which sealed
  package — and which tape or disc — holds each old version: *"v1 · 2024-03-12 ·
  in NSP-C0003 on tape LTO-0007."* A file's detail view (click any filename in
  **Find**) lists them all.
- **Restore any version.** Restore defaults to the newest, but takes a selector:
  a specific version, or **"as of &lt;date&gt;"** to get whatever was current
  then. The restored bytes are hash-checked against that version's recorded hash —
  the round-trip proof that "restore v1" really reproduced v1.
- **Drift shows the prior version inline.** A `MODIFIED` file names its retained
  prior version as the restore source, so recovering the pre-change copy is one
  click, not a hunt.
- **Capped only if you ask.** `versions_retained` (Settings) defaults to
  *unlimited*. Setting a cap forgets only the catalog's *pointer* to the oldest
  versions — it never deletes anything from media, because Mnemosyne can't and
  never could.

---

### Quarantine — never delete, made usable

Mnemosyne has no delete button and never will — but "you can't remove anything" is
only livable if there's *some* way to get a stray or superseded file out of the way.
Quarantine is that pressure valve, built to be **regret-proof**: the strongest action
the tool offers isn't deletion, it's a reversible **move**. Marking a file or folder
"quarantine" relocates it (bytes intact, structure preserved) to
`<destination_root>/_deleted/<original relative path>`, records *who* asked (implicitly
you), *when*, and an optional reason in the catalog and audit log, and stops counting it
toward protection — and it shows you that protection **consequence before you confirm**
(*"this drops Smith Wedding originals to 1 copy — proceed?"*). Because the original path
is preserved verbatim under `_deleted`, **un-quarantine is a plain reverse move** that
also re-credits the copy. Crucially, quarantine exists **only inside managed territory**
— the destination roots Mnemosyne itself populated via Plans — enforced by the very same
read-only guard that keeps the tool out of your source data: on adopted media and source
roots the action simply does not appear, because there is nothing there the tool created.
The Quarantine view lists everything staged (contents, age, total bytes) under one
standing promise: *removing these permanently is a manual act you perform in your file
manager — this tool has no delete button and never will.* The tool **never empties
`_deleted`**; if you clear it by hand, the next scan reconciles gracefully, marking those
entries *human-removed* while keeping their history.

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

## BagIt compatibility

Mnemosyne speaks [BagIt](https://datatracker.ietf.org/doc/html/rfc8493) for
institutional legibility **without adopting BagIt's storage layout** — because the
doctrine is immovable: *extraction always yields your original tree*. The split is
deliberate and exact:

- **Every package carries a BagIt payload manifest.** `manifest-sha256.txt` (plain
  `sha256␣␣relative/path` lines over the original tree) is written **both as the
  first member inside the package's `tar`** — so a reader can stream-verify each
  file as it extracts — **and as a sidecar on the media**. A
  `bag-info.txt`-style metadata sidecar rides along too (source organization,
  bagging date, package name, byte/file counts, Mnemosyne version). These are a
  **description layer**: the payload is still a plain `tar` that extracts to your
  original folders, no bag tooling required.
- **The storage format is never a bag.** Mnemosyne does **not** restructure
  packages into a BagIt `data/` tree. The manifest describes the tree; it doesn't
  relocate it. Extraction yields exactly your files (plus that one manifest file at
  the root), forever readable with plain `tar`.
- **A conformant bag is an explicit export, never the storage.** *Export as BagIt*
  (per package, or per archive) materializes a fully conformant bag — `data/`
  payload, `bagit.txt`, `manifest-sha256.txt` + `tagmanifest-sha256.txt`, and
  `COMPARISON.md` — into a directory you choose, for handoff to institutional
  tooling (Archivematica and friends). It's a copy for ingest; your archive on
  media is untouched.

In short: **BagIt manifests travel with every package for free; the fully
conformant bag is a handoff export you ask for.** See
[docs/COMPARISON.md](docs/COMPARISON.md) for how this sits next to BagIt /
Archivematica as institutional endpoints.

---

## Portable structure & plan exports

The catalog's *knowledge* — what files exist, where each physically lives, and
where a move would put them — travels as small, hash-keyed documents:

- **Structure Export** (per archive, JSON with a CSV twin and a printable Markdown
  companion): every file's SHA-256, size, role, event, capture date, every known
  location (volume serial + path + location name + last verified), and its planned
  path if a plan maps it. Import it into a fresh install and search, locations, and
  events answer exactly as on the original machine — it reconstructs the
  *knowledge*, not the data.
- **Plan Export** (JSON): a compiled reorganization, per source-drive **serial** —
  the pending/satisfied work with hashes and destinations. Import it on another
  machine and that machine can carry the move out; the serial binding makes it safe
  (a drive only advances the plan when its real serial matches).

> **These exports contain paths and hashes but ZERO file content — no media, no
> bytes.** It is safe to email your organization scheme or print it; it cannot
> reconstruct your data, only the map of what should exist and where. The Markdown
> companion (`STRUCTURE-<archive>.md`) is included in every Recovery Kit.

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
burner via a command template (`{SRC}` = staged folder, `{LABEL}` = package
name). The **documented default is [xorriso](https://www.gnu.org/software/xorriso/)**
— free, maintained, cross-platform, scriptable, and it leaves an auditable
command line rather than clicks in a GUI:

```
xorriso -outdev /dev/sr0 -volid "{LABEL}" -blank as_needed -map "{SRC}" / -commit -eject
```

(growisofs on Linux and ImgBurn on Windows still work; the Settings page has a
one-click "Use xorriso default".) See [Optical burn queue](#optical-burn-queue).

**Why discs get two kinds of repair data.** A disc can lose data two different
ways, so it earns two different layers. **par2** (already beside every payload)
protects the *contents* — corrupt bytes anywhere in the payload file are
reconstructed from its Reed–Solomon parity. But a scratch or a bad patch of dye
kills a whole *run of neighbouring sectors* at once, damage tied to the disc's
physical geometry rather than to any one file. That is what the **optional**
[dvdisaster](https://dvdisaster.jcea.es/) layer covers: set `burn_ecc` to `rs02`
or `rs03` in Settings and, after each disc *verifies*, Mnemosyne reads it back and
writes a `<name>.ecc` error-correction file over the disc geometry — the layer par2
cannot provide, because par2 protects the payload *file*, not the sectors under it.
The `.ecc` is computed after the disc is finished, so it rides onto the next disc
in the set or stays in staging (`burn_ecc_carry`). dvdisaster is detected like
`smartctl` and hides behind an install hint when absent. It is strictly
complementary and never required: **par2 repair of the payload works regardless**,
every disc's `RESTORE.txt` says so in plain words, and the three-tool restore
(par2 → gpg → tar) never depends on it.

---

## Screenshots

> **Placeholders — capture these and drop them in [`docs/img/`](docs/img/).**
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
  in keystore JSON files; the catalog stores fingerprints, never secrets. The
  Recovery Kit prints each key as a **one-per-page key page** for the fireproof
  box: a QR to scan *and* a retypable character grid (groups of 4, a **CRC-16**
  per line, and the whole passphrase proven by its SHA-256 fingerprint) so a
  human with only a keyboard can still key it in and confirm it's right. QR and
  typed forms are the **same secret** — and *Keys > Enter key from sheet* verifies
  a retyped passphrase against the catalog without ever revealing it. (These are
  symmetric AES-256 passphrases, not GPG keypairs, so `paperkey` doesn't apply —
  the printable backup is the passphrase itself.)
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

### Two layers of encryption — and why gpg is the portable one

Some tape operators run their **drive's built-in AES** (LTO hardware encryption,
managed on Linux with [`stenc`](https://github.com/scsitape/stenc)) *alongside*
Mnemosyne's application-layer gpg. Mnemosyne **supports awareness of this layer,
not dependence on it** — and the distinction is the whole point:

| | **gpg (application layer)** | **Drive-level AES (hardware layer)** |
|---|---|---|
| Where the ciphertext is | the `.tar.gpg` **file** on the medium | the **raw bytes** the drive records |
| What reads it back | *any* drive + `gpg` + the passphrase | *only* a compatible drive with the **drive key** loaded |
| In the restore story? | **yes** — QR card, paper key, keystore all carry it | **no** — the drive key lives entirely outside Mnemosyne |
| If the key is lost | the package is one of *N* verified copies; other layers stand | the tape is **scrap** — `gpg`, `tar`, and `par2` all fail; par2 can't even see the data to repair it |

gpg is the **portable** layer: the ciphertext is an ordinary file, and multiple
independent implementations (GnuPG, Sequoia) can open it on any machine, forever.
Drive-level AES is a hardware property of one key in one drive family — powerful,
but a decade-scale single point of failure that no amount of par2 or gpg can undo.

So Mnemosyne treats the hardware layer as **awareness, never dependence**:

- **stenc is optional and detected** (Linux; on Windows/macOS the drive key is a
  vendor-tool concern). When present with a drive attached, the **Tape Drive**
  panel reads and shows the drive's encryption status (on/off, key loaded).
- **Setting or clearing the drive key** is a clearly-marked *advanced* action
  behind an explicit warning — *"a tape written with it cannot be read by any
  drive without this key configured; your packages remain gpg-protected
  regardless; most users should leave this off."* It is **never enabled silently**.
- **Volumes remember it.** A tape written while drive AES was active is recorded
  with `drive_encryption: true`, and the **volume inventory + Recovery Kit** say
  so prominently — *"this tape ALSO requires drive-level key `<label>` — see
  stenc"* — with the one instruction that matters: **preserve the drive key
  separately, or the tape is unrecoverable.**

Restore **never requires** the drive layer: the guarantee is always par2 → gpg →
tar, and gpg is the layer that travels.

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
[MIT](LICENSE) © 2026 The Mnemosyne Authors. The three restore tools and any LTFS
driver are separate software under their own licenses; this repo references but
never bundles them.
