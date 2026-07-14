# Discs, mirror drives, and adopting old drives

This guide covers three ways to keep copies that don't involve tape: burning discs, copying plain files to an external drive, and bringing a box of old drives into Mnemosyne. As always, the app only reads your source folders. It never changes, moves, or deletes your originals, and it never sends anything over the internet.

Some words you'll see:

- **Package** = one media-sized sealed unit the app makes (bundled files + repair data + a contents list + a plain-text restore guide). You unpack it to get your files back.
- **Mirror** = plain files copied straight to a drive. No sealing, no unpacking — you browse them with any file manager, like an ordinary folder.
- **Archive** = a body of work you keep together (for example, "Wedding Photos 2024").
- **Hash / fingerprint** = a short code made from a file's exact contents. Change one byte and it changes. This is how the app proves a copy matches the original.
- **Mount / mounted** = when a drive or disc shows up as a drive letter (like `E:\`) you can open.
- **Read-only** = the app only looks; it never writes to or changes the thing it is reading.

---

## Part A: Burning Blu-ray or DVD discs

Discs are handy for small, cheap, shelf-stable copies. Mnemosyne burns them from the **Burn** tab using a **burn queue** — a list of packages to burn, one disc each, all the same disc type.

### Step 1: Set up a burn command (one time)

Mnemosyne uses a separate burning program on your computer to actually write discs. You tell it which one to use, once.

1. Open the **Settings** tab.
2. Find the burn command setting and point it at your burning program. The recommended default is **xorriso** — one free, actively-maintained, cross-platform program. On the Settings page, click **Use xorriso default** to fill it in:

   ```
   xorriso -outdev /dev/sr0 -volid "{LABEL}" -blank as_needed -map "{SRC}" / -commit -eject
   ```

   `{SRC}` is the package's staged folder and `{LABEL}` is the package name — Mnemosyne fills those in for each disc. Install xorriso with `apt install xorriso` (Linux) or `brew install xorriso` (macOS).

   Other burners work too: **growisofs** on Linux, and **ImgBurn** (a common free choice) on Windows. Whatever you pick, the burn command just needs to write the `{SRC}` folder to the disc and return success (exit code 0).

![Settings showing the burn command configured](../img/06-discs-and-drives-burncmd.png)

You should now see a burn command saved in Settings. Without this, the Burn tab has nothing to run.

### Step 2: Create a burn queue

1. Open the **Burn** tab.
2. Choose **Create burn queue…**.
3. Pick the **Archive** you want to burn.
4. Pick the **media kind**: Blu-ray or DVD.
5. Optionally give the queue a name so you can recognize it later.

![Create burn queue dialog](../img/06-discs-and-drives-queue.png)

You should now see a new queue with one square for each disc to burn. Each square is a package.

### Step 3: Burn the discs one at a time

1. Put a blank disc of the right type into your burner.
2. Click **Insert blank → Burn next disc**.
3. Wait. The **Jobs** tab shows the progress bar and speed.
4. When the disc finishes, take it out and write the package name on it with a marker.
5. Insert the next blank and repeat until every square is done.

![Burn tab showing coloured disc squares](../img/06-discs-and-drives-squares.png)

The squares change colour so you always know where you are:

- **Green** = done and good.
- **Amber** = burning right now.
- **Red** = failed.
- **Grey** = still waiting (pending).

You should now see squares turn green one by one as you burn and label each disc.

### About reboots and coasters

A burn queue is tough. If the app or your PC restarts in the middle of a burn, the queue is still there when you come back. The disc that was burning resets to **pending** (grey). That half-burned disc may be a **coaster** (a ruined disc), so throw it away and burn that one onto a fresh blank.

### Optional: an extra armour layer for discs (dvdisaster ECC)

Discs die differently from drives. A scratch or a bad patch of dye kills a **run of neighbouring sectors** all at once — a kind of damage that a per-file repair can't always see coming. Every package already carries **par2** repair data, which protects the *contents* of the payload file. **dvdisaster** adds a second, different kind of armour: Reed–Solomon error correction computed over the disc's whole *sector geometry*, so even a scratch that wipes out a band of physical sectors can be healed.

This layer is **completely optional and never required to restore**. Your files come back from par2 + tar (+ gpg if encrypted) whether or not dvdisaster was ever used — the RESTORE.txt on every disc says this in plain words. dvdisaster is just belt-and-suspenders for the physical disc.

To turn it on:

1. Install **dvdisaster** (open source — `apt install dvdisaster` on Linux, `brew install dvdisaster` on macOS, or dvdisaster.jcea.es on Windows). Like the drive-health tool, if it isn't installed the feature simply hides, with an install hint in **Settings → External tools**.
2. In **Settings → Optical burning**, set **Disc-level ECC** to **RS02** or **RS03**.
3. Optionally tell it which optical drive to read (blank works when your burn command already names one, like `/dev/sr0`).

After each disc **verifies**, Mnemosyne reads it back and writes an error-correction file named `<package>.ecc`. Because that file is made *after* the disc is finished, it can't live on the disc it protects — so it either **rides onto the next disc in the set** (tick "Carry onto the next disc") or **stays in your staging folder**, your choice. Keep those `.ecc` files with your discs; if a disc later develops read errors, dvdisaster can use its `.ecc` to repair it.

---

## Part B: Mirror copies to a plain external drive

A **mirror** copies an archive's files onto an external drive as ordinary, browsable files. There is no sealing and no unpacking — you can open the drive on any computer with any file manager, even without Mnemosyne. This is the best choice for a drive you actually browse.

1. Open the **Vault** tab.
2. Choose **Mirror backup…**.
3. Pick the folders you want to copy. Every path box has a **Browse…** button.
4. Pick the target external drive as the destination.
5. Start the mirror and watch the **Jobs** tab for progress, MB/s, and ETA.

![Mirror backup dialog in the Vault tab](../img/06-discs-and-drives-mirror.png)

As it works, each file is copied and then **re-read and checked** against its fingerprint, so you know every copied file is perfect — not just that it copied.

You should now see the job finish with every file copied and verified. Open the drive in your normal file manager and your files are right there as plain folders.

---

## Part C: Adopting a box of old drives (Dock sessions)

Have a shelf of old external drives full of backups you made by hand over the years? The **Dock** tab brings them in, one drive at a time, and records which of your files they already hold. This is how you **adopt** existing plain-file backups without re-copying anything.

Dock is **read-only toward your source folders and toward the old drives' folders** — it looks and fingerprints, but it does not change your files. It does write one small inventory file onto each old drive so you have a record of what was found.

### Prerequisite: scan the original source first

Dock recognizes files by their **contents** (their fingerprints), not by name. To match the loose files on an old drive, Mnemosyne needs something to compare them against. So you must have already **scanned the original source folder into an Archive**. If you have not done that yet, do it first (see the earlier vault/scan guide). Without it, there is nothing to match against.

### Step 1: Start a Dock session

1. Open the **Dock** tab.
2. Start a new session.
3. Pick which **Archive** (or archives) to check the drives against.

![Dock tab starting a session and choosing archives](../img/06-discs-and-drives-dock-start.png)

You should now see the session waiting for a drive.

### Step 2: Plug in a drive and ingest it

1. Plug one old drive into your computer.
2. Within about 5 seconds it appears in the Dock session.
3. Click **Ingest this drive**.

Mnemosyne fingerprints every file on the drive and matches it **by content** against your archive. Because it matches by content, it still recognizes your files even if they were renamed or moved into different folders on that old drive. Every match is recorded as a verified copy, and a small inventory file is written onto the drive.

![Dock session showing a drive ready to ingest with a coverage bar](../img/06-discs-and-drives-dock-ingest.png)

You should now see a **coverage bar** showing how much of the archive that drive covers, and each matched file recorded as a verified copy.

### Step 3: Repeat, re-verify, and export

1. Unplug that drive and plug in the next one. Repeat Step 2 for the whole box.
2. If you plug in a drive Mnemosyne has seen before, it offers **Re-verify…** instead of Ingest. Re-verify re-checks that the drive still holds good copies.
3. When you're done, use **Export report (.md)** to save a plain-text summary of what each drive holds.

You should now see rising coverage as you work through the box, and a saved report you can keep.

---

## How to know it worked

- **Discs:** every square in the burn queue is green, and each disc is labeled with a marker.
- **Mirror:** the Jobs tab job finished with every file copied and verified, and you can open the plain files on the drive in any file manager.
- **Dock:** the coverage bar rose as you ingested drives, matches are recorded as verified copies, and you exported a report.

## If something went wrong

- **The Burn tab won't burn.** Check that a burn command is set in Settings (Part A, Step 1). Without it, there's nothing to run.
- **A disc turned red (failed).** Throw that disc away — it may be a coaster — and burn that package again on a fresh blank.
- **The app restarted mid-burn.** That's fine. The queue survived. The disc that was burning is now grey (pending); discard it and re-burn on a new blank.
- **Dock finds no matches on an old drive.** Almost always this means the original source was never scanned into an Archive, so there's nothing to compare against. Scan the source first (see the prerequisite), then run the Dock session again. Your files are safe either way — Dock only reads.
- **A drive doesn't appear in Dock.** Give it a few more seconds. Confirm it mounts (shows a drive letter) in your normal file manager first.

## Screenshots to capture

- `../img/06-discs-and-drives-burncmd.png` — Settings tab with the burn command configured.
- `../img/06-discs-and-drives-queue.png` — Create burn queue dialog with archive and media kind selected.
- `../img/06-discs-and-drives-squares.png` — Burn tab showing green/amber/red/grey disc squares.
- `../img/06-discs-and-drives-mirror.png` — Vault tab "Mirror backup…" dialog picking folders and a target drive.
- `../img/06-discs-and-drives-dock-start.png` — Dock tab starting a session and choosing archives.
- `../img/06-discs-and-drives-dock-ingest.png` — Dock session with a drive ready to ingest and a coverage bar.
