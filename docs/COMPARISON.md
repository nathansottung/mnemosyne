# Why not restic / borg / Bacula / dar / Canister? (and how Mnemosyne relates to the rest)

Mnemosyne answers a narrow question the popular tools answer badly: **"will a
stranger read these bytes off cold media in 2050, with no Mnemosyne installed?"**
Every design choice below follows from that. The other tools are excellent at what
they do — this page is about *fit for decade-scale, hand-recoverable, write-once
archival*, not a claim that anything here is bad. Where a tool is a **complement**
rather than an alternative, it's marked as such.

The one-line summary: **most tools below are backup engines whose on-disk format is
theirs; Mnemosyne is an archival *organizer* whose on-media format is a plain
`tar` you can already open.** Restoration here needs only `par2`, `gpg`, and
`tar` — three ubiquitous, independently-implemented, open-source tools — and the
Recovery Kit documents them per package. Nothing needs Mnemosyne.

## The comparison

| | restic / borg | Bacula / Bareos | dar | Canister | **Mnemosyne** |
|---|---|---|---|---|---|
| **On-media format** | custom chunked repo (content-defined chunking, packs) | custom volume format + catalog DB | custom `.dar` slices | custom catalog + copies | **plain POSIX `tar`** (+ `par2`, `gpg`) |
| **Restore without the tool** | no — needs restic/borg to reassemble chunks | no — needs Bacula + its DB | effectively no (dar-specific) | no | **yes — `par2` → `gpg` → `tar` by hand** |
| **Dedup** | yes (great for churning backups) | limited | no | no | **deliberately none** — one file's bytes are one contiguous run; a lost chunk can't orphan unrelated files |
| **Compression** | yes | yes | yes | optional | **none** — extracted bytes are bit-identical; no codec to still have in 2050 |
| **Encryption** | repo-wide | optional | per-archive | — | **per-package OpenPGP (AES-256)**, key never in the catalog |
| **Bit-rot repair on media** | no (relies on healthy storage / repo check) | no | par2 by hand | no | **par2 Reed–Solomon beside every payload**, plus optional disc-level dvdisaster ECC |
| **Cold / removable media (tape, BD-R)** | awkward | yes (its home turf) | possible | yes | **first-class** — spanning, finalize/seal, barcodes, LTFS, burn queue |
| **Physical inventory** ("which tape, which shelf?") | no | partial (DB) | no | yes | **yes** — volumes, locations, offsite, SMART, verify history |
| **Institutional handoff** | no | no | no | no | **BagIt manifests inside & beside every package + conformant-bag export**, Recovery Kit, per-line paper key sheets |
| **Format longevity advice** | no | no | no | no | **yes** — per-format census + reader registry travels with the media |

## Repository-based backup engines

- **restic / borg** — superb *operational* backup: fast, deduplicated,
  incremental, encrypted, with a real track record. But the repository is a chunk
  store only their code understands; lose or age-out the software and the packs are
  opaque. Dedup and compression, which make them great for churning nightly
  backups, are liabilities for archival — they entangle otherwise-unrelated files
  and add a codec dependency to every future read. They target hot disks and cloud,
  not a shelf of BD-Rs a curator opens in thirty years. Different problem, done
  well. Many people run one of these for the working set and Mnemosyne for the
  forever copy.

## Enterprise tape backup

- **Bacula / Bareos / Amanda** — serious enterprise backup with tape libraries,
  scheduling, and a catalog database — genuinely strong at automated tape rotation
  across a fleet. But restore is inseparable from a running director/server plus
  its SQL catalog, and the on-tape format is the product's; that is the opposite of
  "hand a stranger the tape and a page of instructions." They also carry real
  operational weight (a database to run, back up, and keep compatible) that a
  one-archivist shelf of tapes doesn't want. Mnemosyne borrows their discipline
  about inventory and verification, not their format lock-in.

## Distributed / versioned file management

- **git-annex** — brilliant and, frankly, without peer at what it does: tracking
  where the *content* of large files lives across many repositories and remotes
  while git tracks only pointers. It is also famously steep — a conceptual model
  and a command surface that reward deep investment. Its home is a working,
  synchronized, multi-remote collection you actively manage, not a sealed
  write-once tape you file and forget. Mnemosyne's catalog is deliberately smaller
  in ambition and its media are deliberately dumber (a plain tar, no annex needed
  to read them).

## The closest philosophical relative

- **dar** (Disk ARchive) — the nearest neighbour in spirit: single-file archives,
  optional par2 by convention, a focus on archival rather than churning backup. The
  honest difference is a bet on tooling and containers. `.dar` slices are a
  dar-specific format served by essentially one implementation; Mnemosyne's payload
  is a *plain tar* — the most widely implemented archive format in existence — with
  par2 and gpg as separate, individually swappable layers, and a physical-media
  workflow (spanning, sealing, inventory, verification) that dar leaves to you. If
  you like dar's philosophy, Mnemosyne is the same philosophy that bet everything
  on formats you already have.

## Commercial media-archive tools — Mnemosyne's origin story

- **Hedge Canister / YoYotta / Archiware P5** — this category is where Mnemosyne
  comes from. These are the polished, respected tools a media archivist actually
  reaches for: cataloging what's on which LTO or disk, verifying copies, managing a
  physical library. They're good at it. The itch that produced Mnemosyne was wanting
  that same workflow — physical inventory, checksums, verification history,
  restore-with-confidence — **without** a proprietary on-tape format, a per-seat or
  per-drive licence, or a catalog you can't read once the vendor moves on. Mnemosyne
  is the open, hand-recoverable answer to "I love what Canister/YoYotta/P5 do for me
  operationally, but I need the bytes to outlive the product." Respect to the
  originals; this is a different bet on longevity.

## Standards & institutional preservation

- **BagIt / Archivematica** — not competitors so much as the world Mnemosyne wants
  to hand off *to*. BagIt (RFC 8493) is a packaging standard; Archivematica is a
  full OAIS-style digital-preservation pipeline for institutions. Mnemosyne meets
  them where it counts: every package carries a **BagIt `manifest-sha256.txt`** —
  as the first member inside its tar *and* as a sidecar on media — plus a
  `bag-info.txt`-style metadata file, and there's a one-click **"Export as BagIt"**
  that materializes a fully conformant bag (`data/` layout + manifests) for ingest.
  The distinction Mnemosyne holds firmly: BagIt is a **description/handoff** layer
  here, never the storage layout — packages still extract to your original tree, no
  bag tooling required. If your endgame is an institutional repository, Mnemosyne
  feeds it; it doesn't try to be it.

## The substrate, not a manager

- **LTFS** (Linear Tape File System) — often mentioned in the same breath, but it's
  a *layer below* Mnemosyne, not an alternative to it. LTFS makes an LTO tape mount
  like a filesystem; it says nothing about what you write there, whether it's
  verified, which shelf it's on, or how you'd recover it in 2050. Mnemosyne writes
  its packages *through* LTFS (or straight to a tape device) and adds everything
  LTFS doesn't: inventory, verification history, par2, sealing, and the Recovery
  Kit. Use LTFS as the substrate; let Mnemosyne be the keeper.

## Complements — movers, not keepers

These aren't alternatives at all; they pair well with Mnemosyne.

- **dvdisaster** — the disc-geometry counterpart to par2. par2 protects the payload
  *file's* contents; dvdisaster adds Reed–Solomon ECC over a disc's physical
  *sectors*, healing scratches that wipe out whole runs of sectors. Mnemosyne
  detects it as an optional tool and can generate a `.ecc` per burned disc. It's an
  extra armour layer on optical media, never required for restore (par2 → gpg → tar
  still stands).
- **FreeFileSync / Syncthing / rclone** — excellent at *moving and mirroring* bytes:
  folder sync, continuous replication between machines, and shovelling data to and
  from cloud object stores respectively. They keep two live locations in step; they
  don't seal a verified, self-describing, hand-recoverable archive and track it on a
  shelf. They're the movers; Mnemosyne is the keeper. Use them to feed Mnemosyne, or
  to distribute its outputs — not to replace the sealed archive.

## What Mnemosyne gives up

Honesty: no dedup and no compression means Mnemosyne uses **more space** than
restic/borg for redundant or compressible data, and it is **not** an incremental
hot-backup scheduler. If you want nightly deduplicated snapshots of a live server,
use restic or borg. If you want the bytes to still be readable, by anyone, off cold
media, after the software is gone — that is what Mnemosyne is for. Many people run
both: restic for the working set, Mnemosyne for the forever copy.
