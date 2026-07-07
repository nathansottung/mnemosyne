# Contributing

Thanks for helping keep a decades-scale archival tool boring and trustworthy.
The bar here is *longevity and clarity*, not cleverness.

## Build & check

Requires **Go 1.22+**. No other toolchain, no code generation, no `make`.

```bash
go build ./...        # compiles the binary (UI is embedded via //go:embed)
go vet ./...          # catches the usual mistakes
gofmt -l .            # must print nothing — run `gofmt -w .` to fix
```

Cross-compile the release targets (all pure Go, `CGO_ENABLED=0`):

```bash
GOOS=windows GOARCH=amd64 go build -o mnemosyne.exe .
GOOS=linux   GOARCH=amd64 go build -o mnemosyne-linux-amd64 .
GOOS=darwin  GOARCH=arm64 go build -o mnemosyne-macos-arm64 .
```

**Every PR must build, vet, and gofmt cleanly on all three OSes.** Platform
code is isolated behind build tags (`*_windows.go` / `*_unix.go`), so keep
platform-specific calls there.

## Code style

Match the surrounding code — it is intentional, not accidental:

- **Flat files, one concern each.** New feature areas get their own top-level
  file (see [`ARCHITECTURE.md`](ARCHITECTURE.md)), all `package main`. Don't
  introduce package trees or internal frameworks.
- **Standard library first.** The only third-party dependency is QR-code
  generation. Adding a dependency needs a strong, stated reason in the PR;
  anything that pulls in CGO is a hard no (the whole point is a static binary).
- **The `Store` is the only thing that touches `catalog.json`.** Persistence,
  migrations, and reboot recovery live in `store.go`. Other files call `Store`
  methods; they never marshal the catalog themselves.
- **Never break the restore doctrine.** The on-media artifacts must always be
  restorable by hand with `par2` / `gpg` / `tar`. par2 is computed over the
  payload; no compression layer, ever. If a change touches build/write/restore,
  it must keep `RESTORE.txt` accurate and end-to-end true.
- **Persisted JSON is a contract.** Don't rename existing `json:"..."` tags or
  API routes — add fields/aliases and keep old catalogs loading. Match the
  existing comment density and naming.
- **UI is one vanilla-JS file** (`ui/index.html`), no build step. Keep it that
  way; changes ship by editing the file.
- **Line endings are normalized by `.gitattributes`.** The repo root carries a
  `.gitattributes` with `* text=auto` plus explicit `eol=lf` for `*.go`, `*.md`,
  and `*.html`, and `eol=crlf` for `*.bat` (so `cmd.exe` parses batch files
  correctly). Don't commit CRLF into source or docs; if a clone shows spurious
  whole-file diffs, run `git add --renormalize .` once.

## Smoke test (manual, end-to-end)

There is no CI harness yet; the trustworthy check is to **drive a real archive
through the pipeline against a scratch data dir** and confirm the bytes come
back. This is the flow to run before opening a PR that touches the pipeline:

```bash
# 0. build and start against a throwaway data dir + port
go build -o /tmp/mn . && /tmp/mn -port 7799 -data /tmp/mn-data &

# 1. point staging + tools at real binaries (Preflight in the UI shows paths)
curl -s -X PUT http://127.0.0.1:7799/api/config \
  -d '{"staging_dir":"/tmp/mn-stage","tools":{"tar":"tar","par2":"par2"}}'

# 2. create an archive, scan a small folder into it
curl -s -X POST http://127.0.0.1:7799/api/archives -d '{"name":"Smoke"}'
curl -s -X POST http://127.0.0.1:7799/api/collections/1/scan -d '{"path":"/some/small/folder"}'

# 3. plan (plaintext to skip keystores), build package 1, write to a folder
curl -s -X POST http://127.0.0.1:7799/api/plan \
  -d '{"collection_id":1,"media_kind":"HDD","target_gb":1,"encrypted":false}'
curl -s -X POST http://127.0.0.1:7799/api/packages/1/build
curl -s -X POST http://127.0.0.1:7799/api/chunks/1/write -d '{"dest_dir":"/tmp/mn-medium"}'

# 4. restore it to a scratch dir and diff against the source
curl -s -X POST http://127.0.0.1:7799/api/chunks/1/restore \
  -d '{"source_dir":"/tmp/mn-medium/Smoke-C0001","output_dir":"/tmp/mn-restore"}'
diff -r /some/small/folder /tmp/mn-restore/...   # must be identical
```

Watch `GET /api/jobs` for each async step to reach `COMPLETED`. The pass
criterion is simple and non-negotiable: **the restored files are byte-identical
to the source.** For encryption/spanning/privacy changes, also exercise those
paths (register two keystores; use a small `target_gb` to force ≥3 segments;
toggle `private_media` and confirm no plaintext `manifest.json` on the medium).

You can also verify the doctrine holds *without Mnemosyne* — that's the real
test: `par2 verify` → `gpg -d` → `tar -xf` by hand on the written folder, per
[`RESTORE_RUNBOOK.md`](RESTORE_RUNBOOK.md).

## Cutting a release

Releases are fully automated by [`.github/workflows/release.yml`](../.github/workflows/release.yml):
pushing a `v*` tag cross-compiles all targets (pure Go, `CGO_ENABLED=0`), zips
each with the docs, writes `SHA-256SUMS.txt`, and publishes a GitHub Release.
The version is baked into the binary via `-ldflags "-X main.appVersion=<tag>"`.

```bash
# from a clean main that builds + vets + tests green:
git tag v2.1.0
git push --tags
```

That's the whole process — watch the **Release** workflow in the Actions tab;
the binaries and checksums appear on the Releases page when it finishes. (Every
push/PR also runs the **CI** workflow: vet + build + `go test ./...`.)

## PR expectations

- **One focused change per PR**, with a description of *why*, not just *what*.
- **Builds + vet + gofmt clean** on windows/linux/darwin.
- **Ran the smoke test** (or the relevant subset) — say so, and paste the
  restore-matches-source result.
- **No behavior change hidden in a "docs" or "refactor" PR.** If you rename a
  user-visible string, keep JSON tags and routes stable.
- **Update the docs you touched:** `README.md`, this file, `ARCHITECTURE.md`,
  and — if the on-media layout changes — `RESTORE_RUNBOOK.md`, which is the
  authoritative 30-year document and must stay hand-followable.
