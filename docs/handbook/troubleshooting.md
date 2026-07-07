# Troubleshooting

This page lists the small snags people hit, in plain language. Each entry has three parts: **Symptom** (what you see), **Why** (what is really going on), and **Fix** (what to do). None of these problems put your original files at risk — Mnemosyne only ever reads your originals, never changes or deletes them, and never sends anything over the internet.

A couple of words used below:

- A **"tool"** here means one of the three free helper programs Mnemosyne relies on: **tar** (packs files together), **gpg** (encrypts and decrypts), and **par2** (makes and uses repair data).
- The **"status lamp"** is the small indicator at the bottom-left that tells you if the app is ready.
- A **"staging folder"** is the temporary scratch workspace where the app builds one package at a time.
- A **"source root" / "source folder"** is a folder you told Mnemosyne to back up. The app refuses to **write** anything into these, so your originals are never at risk.

---

## Setup

**Symptom:** The status lamp says **"setup needed — see Settings."**
**Why:** One of the three tools (tar, gpg, or par2) is not installed on your computer, so the app is not fully ready.
**Fix:** Open the **Settings** tab — it shows per-computer hints for installing each tool. On **Windows**, `tar` already ships with Windows 10 and later; get `gpg` from **Gpg4win**; get `par2` with `choco install par2cmdline`. On a **Mac**, use Homebrew: `brew install gnupg` and `brew install par2`. On **Linux**, use your package manager (for example `apt install par2 gnupg tar`). After installing, click around Settings and confirm each tool shows **"found."**

**Symptom:** The **Settings** page or another screen seems slow the very first time you open it.
**Why:** The app is checking your backup tools for the first time.
**Fix:** Nothing — just wait a moment. It is fast every time after that.

---

## Building and writing

**Symptom:** Building an encrypted package stops with **"refusing to encrypt: need 2 keystores."**
**Why:** Encryption is only allowed once you have at least **two** keystores (two copies of your secret), so a single lost keystore can never wipe out your data.
**Fix:** Add a second keystore path in **Settings**, ideally on a different device (like a USB stick). Or, if you do not need encryption, turn encryption off for this package.

**Symptom:** A whole package shows **FAILED** (a red rail).
**Why:** The build or the staging step failed partway through.
**Fix:** Open the **Jobs** tab and read the error. The two most common causes are (1) not enough staging space — in **Settings**, point the staging folder at a bigger, faster drive — or (2) a missing tool — fix it as in the Setup section above.

**Symptom:** Building refuses with **"not enough space"** (or will not start).
**Why:** Your staging folder's drive is too small or too full. Staging only needs room for **one** package at a time, but it does need that much.
**Fix:** In **Settings**, point the staging folder at a bigger, faster drive with plenty of free space.

**Symptom:** An error like **"refusing: <path> is inside a source root."**
**Why:** You pointed a **write** destination — staging, a backup target, or the recovery kit — at a folder that sits inside one of the folders you are backing up. The app never writes into your originals.
**Fix:** Choose a different folder that is **not** inside any of your scanned source folders.

---

## Media (drives, discs, tapes)

**Symptom:** A package **copy** shows red/failed after a write or a verify.
**Why:** The medium did not match its fingerprint — usually a bad drive, disc, or cable — so that copy cannot be trusted.
**Fix:** Use **"Re-write this copy"** to write a fresh copy to the same volume (the failed one is kept in your history for the record). Or write the copy to a different drive.

**Symptom:** A volume is **SEALED** and will not accept new writes.
**Why:** You finalized (sealed) that volume on purpose, which locks it against further writing.
**Fix:** Click **"Unseal…"** to allow writing again. Unsealing is logged, so there is always a record.

**Symptom:** A disc burn failed, or a disc came out as a **coaster** (unusable).
**Why:** The burn was interrupted. The burn queue automatically reset that disc to **pending**.
**Fix:** Re-burn onto a **fresh blank** disc. Also confirm the burn command is set correctly in **Settings**.

---

## Restoring

**Symptom:** A restore stops asking for a **passphrase**, and nothing you type works.
**Why:** The package is encrypted and needs the correct passphrase from a reachable keystore (or from a Recovery Kit QR card).
**Fix:** Plug in a keystore that holds that key and confirm it appears on the **Keys** tab, then try the restore again. See guide 09 for the Recovery Kit.

**Symptom:** A restore fails at the **"par2 verify"** stage.
**Why:** The medium is damaged, or the source path points at the wrong folder. par2 can only repair a limited amount of damage.
**Fix:** Re-check that the **Source** path points at the real package folder on the medium. If a drive is badly failing, restore from another copy of the same package on a different medium.

**Symptom:** You are hunting for an **"import my catalog"** or **"rebuild"** button after losing your computer, and cannot find one.
**Why:** There isn't one — by design. Mnemosyne has no automatic catalog-import feature.
**Fix:** Rebuild your catalog by re-adopting your media instead: install Mnemosyne fresh, create an Archive with the same name, then on the **Volumes** tab use **"Adopt existing media"** on each backup drive. Or restore by hand with par2 → gpg → tar per the `RESTORE.txt` on each medium. See guide 08.
