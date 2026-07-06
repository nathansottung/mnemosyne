# Mnemosyne v2 — Archival Vault

Catalog, chunk, encrypt, write, verify, and restore your archive onto
LTO tape, hard drives, or optical media — with a restore story that
depends on exactly three immortal open-source programs: **par2 → gpg → tar**.

## Run it (no installs, no runtime)
Download the binary for your OS and run it:
```
mnemosyne.exe                # Windows — then open http://127.0.0.1:7821
./mnemosyne-linux-amd64      # Linux (R720 / TrueNAS jail / Pi)
./mnemosyne-macos-arm64      # Apple Silicon
```
Flags: `-port 7821 -data ~/.mnemo`

The three external tools are the only prerequisites (Preflight in the UI
checks them and tells you how to install anything missing):
tar (built into Win10+/Linux/macOS), gpg (Gpg4win / apt / brew),
par2 (choco / apt / brew).

## Build from source
```
go build -o mnemosyne .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mnemosyne.exe .
```
One dependency (QR code generation). Everything else is the Go stdlib.

## The pipeline
Collection → scan (SHA-256 every file at the source) → plan media-sized
chunks (folder-grouped, par2 budgeted) → build (tar → AES-256 → par2
over the ciphertext) → write through a RAM ring buffer → read-back
verify → re-verify any anniversary → restore (par2 repair → decrypt →
extract), and every chunk carries its own manifest.json + RESTORE.txt.

## Keys
Per-chunk random ~288-bit passphrases. Secrets live ONLY in keystore
JSON files — the catalog stores fingerprints. Builds refuse to run until
≥2 keystore paths on different devices are registered and in sync.
Print a QR card per key for the fireproof box.

## Design notes / v2 scope
- Catalog is a single human-readable `catalog.json` with atomic writes
  (swap-in point for SQLite is `store.go` alone).
- No spanning of single files larger than one medium yet (planner skips
  and reports them). Optical burns stay in your burner app (we produce
  the sized, verified folder).
- Adoption/reconciliation of legacy volumes (Mnemosyne v1 feature) is
  the next port target.
- docs/RESTORE_RUNBOOK.md is the 30-year document; hand it to anyone.
