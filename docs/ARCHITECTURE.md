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

- **Repair is key-independent:** par2 sits *over the ciphertext*, so a rotted
  tape is repaired with no passphrase — custody of secrets and repair of media
  are separate problems.
- **Spanning preserves the chain:** concatenating segments in order reproduces
  the payload whose whole-file hash is `enc_hash`; each tape's read-back proves
  it holds exactly its verified slice.
- **The literal proof** is a restore drill: rejoin → par2 verify → decrypt →
  `tar -xf` → compare extracted files against the catalog's source hashes.

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
