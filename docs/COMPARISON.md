# Why not restic / borg / Bacula / dar / Canister?

Mnemosyne answers a narrow question the popular tools answer badly: **"will a
stranger read these bytes off cold media in 2050, with no Mnemosyne installed?"**
Every design choice below follows from that. The other tools are excellent at what
they do — this is about *fit for decade-scale, hand-recoverable, write-once
archival*, not a claim that they are bad.

The one-line summary: **the tools below are backup engines whose on-disk format is
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
| **Cold / removable media (tape, BD-R)** | awkward | yes (its home turf) | possible | — | **first-class** — spanning, finalize/seal, barcodes, LTFS, burn queue |
| **Physical inventory** ("which tape, which shelf?") | no | partial (DB) | no | yes | **yes** — volumes, locations, offsite, SMART, verify history |
| **Institutional handoff** | no | no | no | no | **BagIt manifests + conformant-bag export**, Recovery Kit, per-line paper key sheets |
| **Format longevity advice** | no | no | no | no | **yes** — per-format census + reader registry travels with the media |

## Why each, specifically

- **restic / borg** — superb *operational* backup: fast, deduplicated,
  incremental, encrypted. But the repository is a chunk store only their code
  understands; lose or age-out the software and the packs are opaque. Dedup and
  compression are liabilities for archival — they entangle unrelated files and add
  a codec dependency. They target hot disks and cloud, not a shelf of BD-Rs a
  curator opens in thirty years.

- **Bacula / Bareos** — enterprise backup with tape libraries and a catalog
  database — genuinely strong at scheduled tape rotation. But restore is
  inseparable from a running Bacula director + its SQL catalog; the tape format is
  Bacula's. That is the opposite of "hand a stranger the tape and a page of
  instructions."

- **dar** (Disk ARchive) — closer in spirit (single-file archives, optional par2
  by convention), but `.dar` slices are a dar-specific format, and the ecosystem
  is one implementation. Mnemosyne's payload is a *plain tar* — the most widely
  implemented archive format in existence — with par2 and gpg as separate,
  swappable layers.

- **Canister** — Mac-oriented cold-storage cataloging; good at "what's on which
  disk," but proprietary and not built around hand-recoverable open formats or
  cryptographic custody chains.

## What Mnemosyne gives up

Honesty: no dedup and no compression means Mnemosyne uses **more space** than
restic/borg for redundant or compressible data, and it is **not** an incremental
hot-backup scheduler. If you want nightly deduplicated snapshots of a live
server, use restic or borg. If you want the bytes to still be readable, by anyone,
off cold media, after the software is gone — that is what Mnemosyne is for. Many
people run both: restic for the working set, Mnemosyne for the forever copy.
