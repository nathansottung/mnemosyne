# Architecture

Mnemosyne is deliberately small and boring: one Go binary, the standard
library plus a single dependency (QR generation), and a **flat-file catalog**
you can read with a text editor in 30 years. There is no database service, no
CGO, no background daemon. The whole thing is ~4k lines of Go plus one HTML
file.

## The flat-file map

Each source file owns one concern. The dependency arrow points from callers to
callees; everything is `package main`.

```
main.go        HTTP server + REST API + embedded UI (//go:embed ui).
               Wires routes to App methods via runJob() (background jobs),
               registers OAIS aliases (/api/archives, /api/packages).
                 │
                 ▼
pipeline.go    App + Config. Keystores, parallel Scan, Plan (group files into
               media-sized packages), BuildChunk (tar → gpg → par2 → manifest
               → RESTORE.txt). The "policy" layer.
writer.go      RAM ring buffer (goroutines + bounded channel), write-throttle,
               WriteChunk (stream + read-back verify), VerifyChunk,
               VerifyCampaign, RestoreChunk (par2 → gpg → tar, + rejoin).
span.go        Spanning: segment plan, SpanWriteNext (one tape at a time),
               rejoinSegments for restore.
burner.go      Optical burn queue: persistent per-disc queue, shell-out to a
               burn command template, read-back verify.
drift.go       Inventory reconciliation: rescan source vs. packaged state,
               classify NEW/MODIFIED/MISSING/MOVED, per-extension report.
privacy.go     ReadMediumManifest: decrypt a private manifest.json.gpg off a
               found medium using the keystore (catalog-loss fallback).
adopt.go       AdoptMedia: catalog pre-existing *.tar/*.tar.gpg media in place —
               hash payloads, import manifests, "deep adopt" via tar -tvf,
               idempotent by payload hash. Never rewrites the medium.
recoverykit.go Recovery Kit export (README + inventory + QR cards + runbook).
                 │
                 ▼
store.go       The catalog. ALL persisted structs (Archive/Collection, File,
               Chunk/Package, Segment, Volume, Copy, VerifyEvent, DriftReport,
               BurnQueue …) and the Store: atomic JSON writes, daily backups,
               migrations, and reboot recovery. The only file that touches
               catalog.json.

ltfs_windows.go / ltfs_unix.go       build-tagged: detect a mounted LTFS volume.
diskfree_windows.go / diskfree_unix.go  build-tagged: free-space per platform.
ui/index.html  single-file SPA (vanilla JS, no build step); embedded at compile.
```

**Layering rule of thumb:** `store.go` knows nothing about tar/gpg/par2 or
HTTP; `pipeline.go`/`writer.go`/… know nothing about routing; `main.go` knows
nothing about JSON-on-disk. Swapping any layer (e.g. the storage backend, or
the UI) touches one file's surface.

## Custody chain

Integrity is a chain of hashes, each link independently checkable. Nothing is
trusted transitively — every arrow is a hash you can recompute by hand.

```
  source files on disk
        │  SHA-256 each (parallel scan)              ← File.Hash / ChunkFileRef.Hash
        ▼
  tar (POSIX, no compression)
        │  SHA-256 of the tar                        ← Chunk.TarHash
        ▼
  payload  =  tar            (plaintext package)
          or  gpg(tar)       (encrypted package, AES-256)
        │  SHA-256 of the payload as written to media ← Chunk.EncHash  ("enc_hash")
        ▼
  par2 parity  (computed OVER the payload)
        │
        ▼
  [spanning only] payload byte-split into segments
        │  SHA-256 of each segment's bytes            ← Segment.Hash
        ▼
  bytes on a Volume
        │  read-back SHA-256 after write / on verify  ← Copy.VerifyOK + VerifyEvent
        ▼
  a verified Copy  (package × volume, with location)
```

- **Payload filename mirrors encryption:** the payload is written to the medium
  as `<name>.tar` (plaintext — the payload *is* the POSIX tar) or `<name>.tar.gpg`
  (encrypted); the `.par2` set follows that name. `payloadName(c)` in `pipeline.go`
  is the single source of truth, used everywhere a payload name is built or
  searched (build, write, verify, burn, span rejoin/sidecars, restore, manifest
  `payload_file`, RESTORE.txt). **Legacy fallback:** plaintext packages built by
  earlier versions wrote `<name>.tar.gpg` for code-path uniformity, so every read
  path (verify, verify-campaign, restore, burn-verify, span rejoin, and the
  `par2SetFiles` lookup) also accepts that legacy name — previously staged/written
  media keep verifying and restoring unchanged.
- **Repair is key-independent:** par2 sits *over the ciphertext*, so a rotted
  tape is repaired with no passphrase — custody of secrets and repair of media
  are separate problems.
- **Spanning preserves the chain:** concatenating segments in order reproduces
  the payload whose whole-file hash is `enc_hash`; each tape's read-back proves
  it holds exactly its verified slice.
- **The literal proof** is a restore drill: rejoin → par2 verify → decrypt →
  `tar -xf` → compare extracted files against the catalog's source hashes.
- **Verification is per-copy, status is derived:** every check writes its result
  to the specific `Copy` it read (`verify_ok`, `last_verified_at`) plus the
  chunk's append-only `VerifyEvent` log. `refreshChunkStatus` (writer.go) then
  derives the package's lifecycle status from the best evidence — a verified
  copy → `VERIFIED`, else any copy → `WRITTEN`, else a present staged payload →
  `STAGED`. A bad medium marks only *that* copy failed; `FAILED` is reserved for
  a corrupt **staged artifact** (write stream-hash mismatch) or a failed
  **build**. Re-writing a failed copy supersedes the old `Copy` (kept as
  history, `superseded=true`) and records a fresh one; superseded copies are
  excluded from `VerifiedCopyCount`/`CurrentCopyCount` and from restore sources.

## Adoption — entering the chain partway

`adopt.go` catalogs media that Mnemosyne did **not** build, so it joins the
custody chain at whatever link the medium can prove — no more, no less:

```
  adopted medium
        │  SHA-256 of the payload as it exists now   ← Chunk.EncHash (ALWAYS known)
        ▼
  ADOPTED-VERIFIED package + a verified Copy on the operator's volume
```

- **Payload hash is the anchor.** Adoption records the payload's current SHA-256
  as `EncHash` and treats it as truth (`ADOPTED-VERIFIED`). Later verifies and
  restores compare against exactly this, identical to native packages.
- **Upward links are imported only if present.** A `manifest.json` (or a
  `.gpg` one, decrypted by trying keystore passphrases) supplies the *source-file
  hashes*, `tar_hash`, `key_ref`, and par2 percentage. Without it those links are
  simply absent — `ListingUnknown` is set and the package is flagged "restore to
  enumerate contents." **Deep adopt** (`tar -tvf`, streamed, never extracted)
  recovers the *path list* but not source hashes, so it does not forge chain
  links it cannot prove.
- **Idempotent by payload hash.** `AdoptMedia` indexes every existing chunk's
  `EncHash`; a match is reported as skipped-duplicate. This makes adoption safe to
  re-run and makes re-discovering a Mnemosyne-written chunk a no-op.
- **Everything downstream is unchanged.** An adopted chunk is an ordinary
  `Chunk` with a `Copy`, so volumes, search, redundancy accounting, verify, and
  restore treat it identically. Drift skips adopted file listings that have no
  source-folder linkage (they describe bytes on media, not files on disk).

## The reboot-recovery pattern

Long operations run as in-memory **Jobs** (they vanish on restart — the catalog
is the truth). So the only trace of an operation interrupted by a crash or power
loss is a **transient status persisted on a catalog object**. On `OpenStore`,
Mnemosyne heals every transient state back to its last stable value, mirroring
the same idea across subsystems:

| Object | Transient (mid-op) | Reset on open → | Note |
|--------|--------------------|-----------------|------|
| Package | `BUILDING` | `PLANNED` | "interrupted by shutdown — re-run Build" |
| Package | `WRITING` | `STAGED` | "interrupted mid-write — re-run Write" |
| Span segment | `WRITING` / `WRITTEN` | `PENDING` | unknown partial file on the tape; re-write |
| Burn disc | `BURNING` / `VERIFYING` | `PENDING` | the physical disc may be a coaster; re-burn |

The same open-time pass also runs **migrations** (e.g. an old `written_dest`
becomes a `Copy` on an auto-created "(unregistered)" volume) and, once per
calendar day, writes `catalog.json.bak-YYYYMMDD` (keeping the newest 14). All of
this lives in `store.go`'s `OpenStore` / `save`, so recovery is one code path.

## Why a JSON catalog (and where SQLite would slot in)

`catalog.json` is a single human-readable file written atomically
(`write tmp → rename`). The trade is deliberate:

- **Pro:** zero CGO, zero services, trivially inspectable and diff-able, and
  restorable by hand decades from now with any text editor. For a
  single-operator archive of thousands (not billions) of files, an in-memory
  map with a mutex is plenty.
- **Con:** the whole catalog is marshaled on each save, and it's single-writer.
  That's fine at this scale; it would not be at 10⁷ files.

If/when scale demands it, **SQLite (`modernc.org/sqlite`, pure Go — still no
CGO) slots into `store.go` alone.** Every other file talks to the `Store`
methods (`AddChunk`, `Chunks`, `RecordCopy`, `AppendVerifyEvent`, …), not to the
JSON — so the storage engine can change without touching pipeline, writer,
burner, or the API. The `Store` method surface *is* the seam.

## Request/job lifecycle

```
browser ──HTTP──▶ main.go handler ──▶ runJob(app, kind, label, fn)
                                          │  spawns a goroutine, returns a Job id
                                          ▼
                                    App method (BuildChunk / WriteChunk / …)
                                          │  progress(f, msg) updates the Job
                                          ▼
                                    Store methods ──▶ atomic save to catalog.json
browser ◀──poll GET /api/jobs── Job status (RUNNING/COMPLETED/FAILED)
```

Read-only calls (config, search, volume/drift reads) return synchronously;
anything long (scan, build, write, span-write, verify campaign, reconcile,
recovery kit) is a Job so the UI stays responsive and survives navigation.
