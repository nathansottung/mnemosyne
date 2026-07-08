# Release checklist — v0.9.0

Prepared 2026-07-07. This confirms the three release-prep workstreams. Tick the two
push-time items (CI, tag) after pushing; everything else is verified in-repo.

## 1. Version audit — one source of truth

- [x] **Single source: `appVersion`** ([main.go](main.go)) is the only version
  string; the in-repo default is **`0.9.0-dev`** (marks any non-release build).
- [x] **Injected at build time from the git tag** via
  `-ldflags "-X main.appVersion=<tag>"` — in
  [`.github/workflows/release.yml`](.github/workflows/release.yml) (`${TAG}`) and the
  [`Dockerfile`](Dockerfile) (`VERSION` arg). The container image also derives tags
  and OCI labels from the tag via `docker/metadata-action`.
- [x] **Everything else derives from `appVersion`:** startup banner, `GET /api/health`,
  About/escrow status (`GET /api/escrow`), BagIt `Bag-Software-Agent`, package
  manifests (`mnemosyne_version`), dock inventory sidecars, and the Recovery Kit all
  read `appVersion` — no independent version literal anywhere.
- [x] **No hardcoded `2.0`/`2.1`/`v1`/`v2` app-version strings remain.** Fixed:
  `main.go` default + comment, `escrow.go` comment, `README.md` (Docker pin →
  Releases page), `release.yml` + `CONTRIBUTING.md` tag examples (`v0.9.0`), handbook
  install banner (`Mnemosyne <version>`).
- [x] **README stops claiming version numbers in prose** — it points to
  **the latest release** (badges + Releases page + "pin a release tag from the
  Releases page").
- [x] **`grep -E '2\.0\.0|2\.1\.0|v1\.|v2\.'` audited.** Remaining matches are **not**
  app versions and are intentionally left:
  - `escrow_manifest.json` — third-party **license** ids (`GPL-2.0`, `LGPL-2.1`,
    `CDDL-1.0`) and dependency URLs (`openjpeg v2.5.2`).
  - `RESTORE_RUNBOOK.md`, `recoverykit.go`, `README.md` — **Parchive/PAR2 2.0** (the
    par2 file-format spec, not our version).
  - `integration_test.go`, `versions.go`, `README.md` — **"restore v1 / version 2"**
    refer to per-file *content versions*, not the app.
  - `.vscode/launch.json` — VSCode launch-file **schema** `0.2.0`.
  - `auth_test.go` (`0.0.0.0:7821`), `space.go` (`mult = 2.0`) — not versions.

## 2. Schema versioning — the forward-compatibility guarantee

- [x] **`schema_version` on every persisted file.** `catalog.json` root (first field),
  keystores (`schema_version` beside `mnemosyne_keystore`), package manifests
  (`schema_version` + `mnemosyne_version`), and dock inventory sidecars. Current
  version: **`schema_version: 1`** (`currentSchemaVersion` in [store.go](store.go)).
- [x] **Load contract enforced** ([store.go](store.go) `OpenStore` / `writeCatalog`):
  - `== current` → proceed;
  - `< current` → **back up exact bytes** (`catalog.json.pre-schema-vN-<ts>`) then run
    the ordered, idempotent **`schemaMigrations`** registry (one func per step);
  - `> current` → **refuse to write** with a clear message ("created by a newer
    version… Upgrade the app… refusing to save so newer fields aren't silently
    dropped"); read-only viewing allowed, surfaced in the startup log and
    `GET /api/health` (`read_only` / `read_only_reason`).
- [x] **CONTRIBUTING hard rules** ([docs/CONTRIBUTING.md](docs/CONTRIBUTING.md),
  "Schema versioning"): persisted fields are **append-only** (never rename, repurpose,
  or re-mean); **removal requires a migration + major bump**; **every new field must
  tolerate being absent** (zero-value semantics documented at the struct); only
  `store.go` migrates; every schema change adds a fixture test.
- [x] **Round-trip fixture test** ([schema_test.go](schema_test.go) +
  [testdata/catalog_schema1.json](testdata/catalog_schema1.json)): loads a checked-in
  schema-1 catalog, saves, reloads, and asserts nothing is lost (byte-identical
  persistence + deep spot-checks). Plus a **legacy → v1 migration + backup** test and a
  **newer-schema read-only refusal** test.

## 3. CI, links, and tests

- [x] **`go build ./...`** — clean.
- [x] **`go vet ./...`** — clean.
- [x] **`go test ./...`** — all packages pass locally (with `gpg`/`par2` present, same
  as CI installs).
- [x] **`gofmt -l`** — clean.
- [x] **README links point to `nathansottung/mnemosyne`** — all 8 repo references (CI
  badge, Release badge, Releases page, GHCR image ×2, etc.) updated; the only
  `github.com/*/mnemosyne` link is `nathansottung/mnemosyne`. Third-party links
  (LTFS, par2cmdline-turbo, stenc) are unrelated and correct.
- [x] **Release-download repo fixed** — `escrowRepo` ([escrow.go](escrow.go)) now
  `nathansottung/mnemosyne`, matching where `release.yml` publishes, so Escrow-Bundle
  binary fetches resolve instead of 404ing.
- [x] **No `microsoft`/wrong-org references** remain in code, docs, or workflows.
- [x] **CI workflow is valid** ([.github/workflows/ci.yml](.github/workflows/ci.yml)):
  standard `actions/checkout@v4` + `actions/setup-go@v5`, installs `gnupg`/`par2`, runs
  `go vet` → `go build` → `go test ./... -v`. The local equivalent is green above.
- [ ] **CI is green on the pushed branch** — confirm the **CI** workflow run passes on
  GitHub after pushing (cannot be triggered from here).
- [ ] **Tag the release** — from a clean `main`: `git tag v0.9.0 && git push --tags`
  (drives `release.yml`: cross-compiled binaries, checksums, GitHub Release, GHCR image).

- [x] **Go module path matches the repo** — `go.mod` is now
  `github.com/nathansottung/mnemosyne` (no internal imports referenced it, so the
  rename is inert to the build; verified with `go build`/`go vet`/`go test`).
