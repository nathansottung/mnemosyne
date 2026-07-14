# Where your data lives (the honesty map)

A backup tool touches your files. So you deserve a plain, complete answer to one question: **where does this tool write, and what does it promise never to touch?** Mnemosyne answers it on one screen — open **Home → "Where your data lives"** (also linked from **Settings** and the end of first-run setup) — and this chapter is the paper version.

Nothing here is a policy you have to trust on faith. Every path on that screen is read straight from your live settings, and the one promise that matters ("it never writes into your originals") is enforced in code and checked by tests you can run yourself.

Some words you'll see:

- **Catalog** = Mnemosyne's own record of everything it knows — hashes, drives, plans. Not your file content.
- **Keystore** = a small file holding the passphrases that lock and unlock encrypted backups.
- **Staging** = a scratch workspace where a package is assembled before it's written to media.
- **Source folder** = a folder you pointed the tool at to scan (your originals).
- **Adopted drive** = an old drive you asked the tool to inventory ("what's on here?").
- **Sidecar** = a small self-documenting folder the tool writes onto media it made, so the media explains itself later.

---

## What this tool writes

| Place | What it is |
| --- | --- |
| **The catalog** | The brain: every hash, drive, and plan. It holds no file content, but losing it loses what the tool knows — so back it up. It lives in your data folder as `catalog.json`, and the tool keeps **daily backups automatically** next to it (`catalog.json.bak-YYYYMMDD`, newest 14 kept). |
| **Settings** | Your configuration, as plain readable JSON (`config.json` in the data folder). |
| **Keystores** | Your encryption keys. The app **refuses to run encryption without two**, on different devices. Secrets live only here; the catalog stores fingerprints, never the key itself. |
| **Staging** | A temporary workspace while building packages, **emptied as packages complete**. It can't live inside a folder you back up. |
| **Destinations you choose** | The tape, disc, or drive you pick for each copy. The tool writes the package there and then re-reads it to verify. On sealed media it also writes the recovery tools (escrow) so the media can rebuild itself years from now. |
| **Inventory & seal sidecars** | Small folders (`MNEMOSYNE_SEAL` when sealing, `MNEMOSYNE_DOCK` on a mirror target) written **only to media this tool itself writes** — never to drives you adopt, never to your source folders. |
| **Quarantine folders** | Reversible `_deleted` holding areas, created **only inside libraries this tool built**. Setting a file aside moves it here; nothing is ever destroyed, and it can always be put back. |

---

## What it never writes to

This list is just as important as the one above.

- **Your source folders.** The folders you scan are read-only, always. Every file is opened only for reading and hashed. Not one byte is written back.
- **Drives you adopt.** When you inventory an old drive, the tool records what's on it *in its own catalog* and writes **nothing** to the drive itself — no sidecar, no marker, no "I was here."

**How this is enforced:** every writable destination — staging, a copy target, a keystore, a restore or recovery-kit folder — is resolved to an absolute path and checked against your registered source folders *before anything is written*. A destination at or beneath a source is **refused**, not silently redirected. (If you've seen the message *"Refusing: &lt;path&gt; is inside a source root,"* that's this guard doing its job.)

---

## Verify this claim yourself

You don't have to take our word for it. The promise is guarded by one function and proven by tests that ship in the source tree. Run them with `go test ./...`:

- **`TestIntegration_SourceSafetyRefusals`** (`integration_test.go`) — staging, write, restore, and kit targets inside a source are all refused.
- **`TestMirror_RefusesSourceDest`** (`mirror_test.go`) — a mirror copy cannot target a source folder.
- **`TestQuarantine_AbsentOnAdoptedAndRefusedForSources`** (`quarantine_test.go`) — quarantine never appears on adopted or source data.
- **`Store.AssertOutsideSources`** (`store.go`) — the single guard every write path calls.

---

## Bonus: every job says where it writes

While a long job runs (and after it finishes), the **Jobs** tab spells out what that job writes and where — "reads your source folder (read-only); writes only to the catalog," "assembles the package in staging …," "writes the package copy to the destination volume you chose," and so on. Same honesty, at the moment it matters.

## Screenshots to capture

- `../img/10-where-screen.png` — The "Where your data lives" screen, showing the writes table and the "never writes to" list.
- `../img/10-jobs-writes.png` — A running job on the Jobs tab with its "Writes:" line.
