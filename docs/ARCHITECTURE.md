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
               registers OAIS aliases (/api/archives, /api/packages). -listen
               binds localhost by default; a non-localhost bind (containers)
               REFUSES to start without a bearer token, and authMiddleware then
               gates every /api call (static UI stays public so it can prompt).
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
dock.go        DockSession ingest: watch for a docked drive, mirror-adopt it by
               CONTENT hash against selected archives, write a drive sidecar,
               track coverage, export a session report. Mounts enumerated in
               dock_mounts_windows.go / dock_mounts_unix.go.
mirror.go      MirrorToVolume: native mirror backup — copy an archive's files to a
               volume as PLAIN FILES (copy-then-verify via .mnemo_tmp → atomic
               rename), recorded as verified file-level copies (same Chunk.Mirror
               record dock adoption makes, so drift/coverage count them), plus the
               reusable writeVolumeInventory sidecar. One job per volume; peers
               with packages.
deviceid.go    resolveVolumeIdentity: best-effort physical device identity
               (serial/model/capacity) behind a mounted path. Platform resolvers
               in deviceid_windows.go / deviceid_unix.go. Non-fatal everywhere.
smart.go       VolumeHealth: drive-mortality signals via smartctl (-j), parsed
               for ATA + NVMe into a SmartSnapshot with a "migrate copies off"
               advisory. Device mapping in smart_windows.go / smart_unix.go. A
               COMPLEMENT to hash verification — never in the write path.
tape.go        TapeCheck: optional tape-drive diagnostics (TapeAlert + LOG SENSE)
               via ITDT / tapeinfo / sg_logs / HPE L&TT. Flag catalogue + tool
               registry here; one parser per tool in tape_parsers.go (tested
               against testdata/). Read-only toward the drive; never a write/move
               command; strictly outside the write path.
label.go       volumeLabelHTML: a self-contained printable volume label —
               Code128 of the barcode + QR of the volume ID + human identity.
recoverykit.go Recovery Kit export (README + inventory + QR cards + runbook).
integrity.go   Named integrity presets (ARCHIVAL/BALANCED/FAST) unifying the
               build-verify tier, par2 %, routine verify level, verify-due window
               and (always-on) read-back into one choice — global or per-archive
               (Collection.Integrity), with the effective settings attested into
               each package's build_verified block.
formats.go     Format-sustainability registry (embedded formats.json, override in
               the data dir) + per-archive census: tiers OPEN / DOCUMENTED-
               PROPRIETARY / AT-RISK / UNKNOWN by documentation + independent
               readers (not vendor health), reader projects, migrations. Feeds
               the dashboard, Recovery Kit (FORMATS.md), and volume inventories.
browse.go      Read-only server-side folder browser (GET /api/browse) powering
               the path picker: lists a directory's immediate subfolders only,
               never file contents, and only where the operator navigates.
verify_levels.go Tiered verification (A census · B full · C sample): level
               helpers, the first/last-4-MiB sample fingerprint, per-file checks,
               and mirror re-verify. Only level B satisfies protection or refreshes
               verify-due; A/C are advisory. Package payloads + write read-back are
               always B (in writer.go); campaigns/dock thread the level through.
finalize.go    The "close the box and label it" ceremony: enforced finalize
               preconditions (recent verify, free-space buffer, SMART), forced
               override with typed confirmation + audit, seal sidecar (record +
               inventory + catalog snapshot) onto the medium, SEALED/unseal state
               and the write-refusal guard.
profiles.go    Protection Profiles + the six-status 3-2-1 model. Built-in
               profiles, nearest-ancestor assignment resolution, per-file status
               derivation across copies × distinct media kinds × offsite, folder
               worst-of aggregation, and the recompute job. Reads the catalog via
               Store methods; the persisted structs live in store.go.
                 │
                 ▼
store.go       The catalog. ALL persisted structs (Archive/Collection, File,
               Chunk/Package, Segment, Volume, Copy, VerifyEvent, DriftReport,
               BurnQueue, Profile, Assignment, ProtectionSummary …) and the
               Store: atomic JSON writes, daily backups,
               migrations, and reboot recovery. The only file that touches
               catalog.json.

ltfs_windows.go / ltfs_unix.go       build-tagged: detect a mounted LTFS volume.
diskfree_windows.go / diskfree_unix.go  build-tagged: free-space per platform.
deviceid_windows.go / deviceid_unix.go  build-tagged: physical device identity.
smart_windows.go / smart_unix.go     build-tagged: mount → smartctl device node.
dock_mounts_windows.go / dock_mounts_unix.go  build-tagged: enumerate mounts.
ui/index.html  single-file SPA (vanilla JS, no build step); embedded at compile.
```

**Layering rule of thumb:** `store.go` knows nothing about tar/gpg/par2 or
HTTP; `pipeline.go`/`writer.go`/… know nothing about routing; `main.go` knows
nothing about JSON-on-disk. Swapping any layer (e.g. the storage backend, or
the UI) touches one file's surface.

## Custody chain

Integrity is a chain of hashes, each link independently checkable. Nothing is
trusted transitively — every arrow is a hash you can recompute by hand. Two
links used to be *fingerprinted but never proven* — the tar was hashed but its
contents unproven, the ciphertext was hashed but its decryptability unproven.
Both are now **proven at build time**, before a package can ever reach media:

```
  source → [contents-verified] tar → [roundtrip-verified] ciphertext
         → [stream-verified] write → [read-back-verified] medium
```

```
  source files on disk
        │  SHA-256 each (parallel scan)              ← File.Hash / ChunkFileRef.Hash
        ▼
  tar (POSIX, no compression)
        │  SHA-256 of the tar                        ← Chunk.TarHash
        │  ── stage_verify: stream-read the tar with Go's archive/tar (no
        │     extraction, no external tool), hash every member, compare each to
        │     the catalog's source hash. Proves the package CONTAINS the source,
        │     byte-exact.                             ← BuildVerified.Contents
        ▼
  payload  =  tar            (plaintext package)
          or  gpg(tar)       (encrypted package, AES-256)
        │  SHA-256 of the payload as written to media ← Chunk.EncHash  ("enc_hash")
        │  ── crypt_verify (encrypted only): gpg -d piped straight into a SHA-256
        │     hasher (no plaintext to disk), result compared to tar_hash. Proves
        │     the ciphertext DECRYPTS back to the verified tar.
        │                                            ← BuildVerified.DecryptRoundtrip
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
- **Build-time verification fixes both proofs at the source:** `stage_verify`
  and `crypt_verify` run inside `BuildChunk` *before* par2/manifest, so a package
  that does not faithfully contain the source, or whose ciphertext will not
  decrypt, **fails the build** and never reaches media — a bad artifact can never
  be faithfully preserved. Both record their result + duration in
  `Chunk.BuildTimings` (`stage_verify`, `crypt_verify`) and the append-only
  `VerifyEvent` log, and the attestation `{"contents":…,"decrypt_roundtrip":…}`
  rides into the on-medium `manifest.json` (`BuildVerified`). Spanned packages run
  both checks on the staged payload *before* segmentation, unchanged. Config
  `build_verify` is `"full"` by default; `"fast"` skips both with an explicit
  amber warning in the UI and manifest (`mode:"fast"`, both false) — archival
  correctness is the default, speed is the opt-out. Cost of full: roughly one
  extra read pass + one decrypt pass per package build.
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

### Measured: the catalog at 1M files (the scalability pass)

`catalog_scale_test.go` is a synthetic benchmark — skipped by default; run with
`MNEMO_SCALE=1 go test -run TestCatalogScale -v -timeout 20m` (override sizes with
`MNEMO_FILES` / `MNEMO_MIRROR`). It builds **1,000,000 file records + 500,000
mirror-copy records** (~315 MB catalog) and measures load, save, insert
throughput, memory, and search. Numbers on a Windows dev box (SSD):

| Metric | Baseline (per-mutation, pretty JSON) | After this pass | Gate (≤ ~3s) |
|---|---|---|---|
| SAVE (marshal + atomic write) | 2.65 s | **0.83 s** | ✅ pass |
| LOAD (`OpenStore`) | 5.11 s | **4.36 s** | ❌ over |
| Insert 100k via `UpsertFile` | O(n²) — minutes | **0.27 s (~375k/s)** | — |
| Adoption write cost (1000 mutations) | ~14 min if unbatched | **~2 writes total** | — |
| Search (path / hash-prefix / ext) | — | **0.2–0.4 s** | — |
| Heap in use after load | — | ~304 MB | — |

What this pass hardened (all in `store.go` behind the same `Store` surface):

- **`UpsertFile` dedup is O(1)**, via a `(collection\|folder\|relpath) → *File`
  index (`fileIdx`) rebuilt on open. It was a linear scan → **O(n²) per scan**,
  the real 1M-file killer (independent of persistence).
- **Compact JSON above `compactThreshold` (50k files).** At 1M the indentation
  alone was ~90 MB of whitespace; compact form cut the file 378→315 MB and SAVE
  2.65→0.83 s. Small catalogs stay pretty-printed and text-editor-inspectable.
- **Batched, coalesced persistence during jobs.** Scan / adoption / mirror / dock
  bracket their work in `Store.BeginBatch()`/`EndBatch()`; while batched, `save()`
  writes at most once per `batchInterval` (default 3 s) and forces a final write
  at `EndBatch`. This collapses a per-mutation write storm (each `save()` marshals
  the *whole* catalog — O(n·m)) to a handful of writes. **Crash-safety is
  preserved because those jobs are idempotent re-runs** — a scan re-hashes the
  same tree and `UpsertFile` replaces by key; adoption skips payloads already
  cataloged by hash; a mirror re-copies-then-verifies. A crash mid-job before the
  final write is recovered by simply re-running it. (Builds and writes are **not**
  batched — they persist immediately, since a half-finished write is not
  replayable.)

**Decision gate:** SAVE now passes (0.83 s). **LOAD (4.36 s) still exceeds ~3 s,
and JSON cannot get under it at 1M** — unmarshaling ~1.5M objects whose 64-char
SHA-256 hashes are irreducible high-entropy text is inherently multi-second.
Per the gate, the remedy is the **pure-Go SQLite swap** above. It is *viable*
(`modernc.org/sqlite` fetches and is CGO-free) but it is intentionally **NOT
bundled into this pass**: it bumps the Go toolchain to 1.25 and adds ~9
transitive modules (incl. the large `modernc.org/libc`), and a correct
implementation reworks the "return an in-memory pointer, mutate it, call
`UpdateX`" pattern every `Store` method relies on — a large, higher-risk change
that belongs in its own reviewed PR with:

1. a one-time automatic migration on open (if `catalog.json` exists and the DB
   does not): load JSON → bulk-insert into SQLite → **back up the JSON, never
   delete it**;
2. the JSON retained as a **"portable catalog" export** (hand-inspectable,
   diffable, engine-independent) — the 30-year-restorable promise stays;
3. a normalized schema that also removes the mirror-copy duplication (per-volume
   file lists keyed by file ID) for free.

This pass ships the operationally critical wins — SAVE under the gate, scan
de-quadratic-ized, the adoption write-storm eliminated, search and memory
healthy — which make 1M-file ingest *feasible today*; the ~4.4 s one-time
startup load is the remaining item the SQLite PR closes.

## Volume identity & labels

A `Volume` is a physical medium the operator can hold. Beyond the human label,
barcode, kind, and location, it carries the **drive's own identity** — serial,
model, and capacity — resolved best-effort from a mounted path:

- **Windows** (`deviceid_windows.go`): one PowerShell/CIM shot,
  `Get-Partition -DriveLetter → Get-Disk`, emitted as JSON via `ConvertTo-Json`
  and parsed. No WMI COM, no CGO.
- **Linux** (`deviceid_unix.go`): `lsblk -J -b …`, walking the tree to the disk
  whose subtree owns the path's mountpoint.
- **macOS** (`deviceid_unix.go`): `diskutil info`, following *Part of Whole* to
  the physical disk for media name and capacity.

Resolution is **non-fatal by construction** (`resolveVolumeIdentity` never
returns an error the caller must handle) and it **never overwrites a good serial
with a blank one** — a later resolve through a dock that masks the serial cannot
erase a real serial captured earlier. External USB/1394 bridges frequently
report the *enclosure's* identity rather than the drive's; when the bus type
says so, `DeviceNote` records the caveat and the UI/inventory/label flag it with
an asterisk. Identity is captured on **register** (`POST /api/volumes` with a
`mount_path`), on **adopt** (from the medium being adopted), and on demand
(`POST /api/volumes/{id}/identify`). It surfaces on the volume cards, the
Recovery Kit's `MEDIA_INVENTORY.md` "Physical volumes" table, and the label.

**Media health** (`smart.go`) reads a drive's SMART self-report via `smartctl`
on the volume view and dock ingest — mapping the mount to a device node with the
same identity plumbing (drive letter → disk number → `/dev/pdN`), parsing ATA and
NVMe into a `SmartSnapshot`, and appending it to `Volume.SmartHistory` so trends
are visible across sessions. It raises a "migrate copies off" advisory on
reallocated/pending sectors or a FAILING self-assessment. It is deliberately a
**complement** to the custody chain, never a substitute: a mortality *signal*
(will this drive die?) carries no claim about data *integrity* (are the bytes
intact?) — only the hashes prove that. Never in the write path; timeouts, and
failures are silent-but-logged; absent `smartctl` hides the feature.

The **tape** analogue is `tape.go` (TapeAlert + LOG SENSE via ITDT / `tapeinfo` /
`sg_logs` / HPE L&TT). Same doctrine, same guardrails: read-only toward the drive
(it never issues a movement or write command — no rewind/space/load/erase),
strictly outside the write path, one defensive parser per tool tested against
`testdata/` sample outputs, hidden behind an install hint when no tool is present.
It renders cleaning/error advisories in plain language and feeds the write
dialog's "check before a big write" nudge. Live validation requires the drive
attached; the parsers are the tested unit.

**Labels** (`label.go`, `GET /api/volumes/{id}/label`) are a self-contained,
print-ready HTML page: a **Code128** of the volume barcode (so a printed label
scans straight back into the barcode lookup), a **QR** of the volume ID, and the
human-readable identity. A volume with no barcode is offered the **next barcode
from the configured scheme** (`barcode_scheme` prefix + gap-free counter, e.g.
`NSP-0001`) at label time — `Store.NextBarcode` derives the number from the max
existing barcode, so there is no stored counter to drift out of sync.

## Dock — guided legacy-drive ingest

`dock.go` is a resumable mode for ingesting a stack of old backup drives through
a dock, one at a time, hands-off after insertion. A `DockSession` (persisted in
`store.go`, so it survives closing the app and resumes across days) reconciles
against one or more Archives and remembers every drive it processed.

- **Watch:** at start the session snapshots the mounts present (`Baseline`);
  `DockCandidates` diffs the live mounts (`enumerateMounts`, platform-tagged)
  against it, so only *newly-inserted* drives are offered, each annotated with
  its resolved serial/model/size and whether it's been seen before.
- **Mirror adoption** is the heart: `IngestDrive` → `mirrorAdopt` hashes every
  loose file on the drive and matches it **by content** against the selected
  archives' cataloged source hashes. Matches are recorded as an
  `ADOPTED-VERIFIED` **mirror** package (`Chunk.Mirror`) per archive, with a
  verified `Copy` on the drive's `Volume` — so the *existing* coverage/redundancy
  machinery counts them with zero special-casing. Files are bucketed as *matched*
  (a current source file), *historical* (an older packaged version), *other*
  (readable but foreign), or *unreadable*.
- **Identity & idempotency:** the drive's `Volume` is matched by physical
  **serial** (`VolumeBySerial`), so re-inserting a drive already ingested — even
  on a different mount letter — is recognized and run as a **re-verify** (updates
  the copy's verify timestamp) instead of a duplicate adopt. Idempotent across
  sessions.
- **Read-only toward sources (Prompt 31):** the NAS archive folders are only ever
  *hashed*. The one write onto media — the drive's `MNEMOSYNE_DOCK/` inventory
  sidecar + `catalog_snapshot.json` — goes through `AssertOutsideSources`, so it
  can never land on a source path. The drive walk skips its own sidecar dir.
- **Coverage & report:** `archiveCoverage` computes, across all chunks with a
  verified copy, how many of the selected archives' files now have ≥1 copy
  (matched by file-id or content hash). `SessionReportMarkdown` is the exportable
  documentation trail — every drive's serial/label/contents summary plus running
  coverage.

## Dependencies

Two tiny, pure-Go, CGO-free libraries — each earns its place against the
"one static binary, hand-restorable, no service" bargain:

- **`github.com/skip2/go-qrcode`** — QR images for key recovery cards and
  volume-ID labels.
- **`github.com/boombuler/barcode`** — Code128 for volume labels. Pure Go, no
  transitive dependencies, renders to the standard-library `image.Image` we PNG
  ourselves. A printed label that scans back into the catalog is worth one small
  dependency; rolling our own Code128 encoder would be more code and more risk
  than importing a focused, widely-used one.

Everything else — tar, gpg, par2 — is an **external tool** shelled out to, on
purpose (see the custody chain and Recovery Kit): the restore story must not
depend on Mnemosyne's Go code existing.

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
