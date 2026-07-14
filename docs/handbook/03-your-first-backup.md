# Your first backup

This guide walks you all the way through making your first real backup, one step at a time. It is written for a complete beginner. Nothing here changes or deletes your original files — Mnemosyne only reads them. And it never sends anything over the internet.

We will do five things:

1. Create an **Archive** (a body of work you keep together, like "Family Photos").
2. **Scan** a folder into it, so Mnemosyne learns what you have.
3. **Plan packages** — group your files into media-sized units.
4. **Build** one package.
5. **Write** that package to an external hard drive and let the app verify it.

Before you start, open Mnemosyne in your browser at http://127.0.0.1:7821 and check the status **lamp** at the bottom-left reads **"tar · gpg · par2 · keys ready"**. If it says **"setup needed — see Settings"**, do the "Set up safely" guide first.

Have your external hard drive plugged in. A "mounted" drive means it shows up with a drive letter like `E:\` that you can open. Make sure yours is mounted before the writing step.

---

## Step 1: Create an Archive

1. Click the **Vault** tab on the left.
   You should see the Vault page with a **Create archive** box.

2. In the **Create archive** box, type a clear name, for example `Family Photos`, and confirm it.
   You should now see a new row for `Family Photos` in the list of archives.

![The Vault tab with a new archive named Family Photos](../img/03-create-archive.png)

Each archive row has buttons: **Scan folder…**, **Plan packages…**, **Mirror backup…**, **Protection…**, and **Rescan & compare**. We will use the first three.

---

## Step 2: Scan a folder into the Archive

Scanning reads your files and records a "hash" for each one. A "hash" is a short fingerprint of a file's exact contents — if even one byte changes, the fingerprint changes. This lets Mnemosyne later prove your backup is a perfect copy. Scanning **only reads** your files. It never changes them.

1. On the `Family Photos` row, click **Scan folder…**.
   A dialog opens asking for a **"Folder to catalog (walked recursively; every file SHA-256 hashed)"**. "Walked recursively" just means it looks inside every subfolder too. "SHA-256" is the type of fingerprint it uses.

2. Click **Browse…** and choose the folder that holds the photos you want to back up.
   The folder's path appears in the box.

3. Start the scan.
   The scan runs **in the background**, so you can keep using the app.

4. Click the **Jobs** tab to watch progress.
   You should see a scan job running, then finishing.

![The Jobs tab showing a scan job in progress](../img/03-scan-job.png)

5. Go back to the **Vault** tab.
   The `Family Photos` row should now show a **file count** (how many files it found). That number means the scan worked.

---

## Step 3: Plan packages

Now Mnemosyne groups your files into "packages" — media-sized sealed units, each one sized to fit the kind of media you plan to store it on.

1. On the `Family Photos` row, click **Plan packages…**.
   A dialog opens.

2. Choose a **target medium** (the size to aim for). Your choices include LTO-8 or LTO-9 tape, Blu-ray 25/50/100 GB, DVD-R or DVD-DL, or **Custom size**.
   - If you are backing up to an **external hard drive**, pick **Custom size** and set it to match your drive (or a comfortable chunk of it).
   - If you are burning **discs**, pick the matching Blu-ray or DVD size.

3. Leave the **par2** setting at its default of **10%**. "par2" is extra recovery data (about 10% more) that can repair the package if part of it gets damaged later. More is safer but takes more room; 10% is a good balance.

4. Choose whether to **encrypt** (scramble) the packages.
   - For your very first backup, it is fine to choose **no encryption** to keep things simple.
   - If you choose **yes**, remember the app **requires two keystores** (two copies of the secret) before it will build. If you have not set those up, see the "Set up safely" guide.

5. Confirm the plan.
   You should now see one or more planned packages for the archive.

![The Plan packages dialog with a target size chosen](../img/03-plan-packages.png)

---

## Step 4: Build one package

Building actually creates the sealed package files in your staging folder (the scratch workspace you set in Settings).

1. Click the **Packages** tab.
   You should see your planned package(s) listed, each with a **Build package** button.

2. Look at the **Build** dialog or the package details — it shows **how much staging room** the build needs. Make sure your staging drive has that much free space.

3. Click **Build package** on one package.
   You should see a lifecycle "rail" (a row of stages) move along:
   **PLANNED → BUILDING → STAGED → WRITING → WRITTEN → VERIFIED**.
   For now, watch it reach **STAGED**. (Red means **FAILED** — see the troubleshooting section if that happens.)

![The Packages tab showing the lifecycle rail reaching STAGED](../img/03-build-staged.png)

You can watch detailed progress on the **Jobs** tab at any time.

---

## Step 5: Write the package to your external drive

Writing copies the built package onto your physical drive. A "Copy" is one package sitting on one "Volume" (a physical thing you can hold, like this external drive). After writing, Mnemosyne **automatically re-reads the drive and checks the fingerprint** to prove the copy is perfect. This "read-back verify" is always on and cannot be turned off — it is what makes a copy trustworthy.

1. Make sure your external drive is plugged in and mounted (showing a drive letter like `E:\`).

2. On the **Packages** tab, click **Write to volume…** on your STAGED package.
   A dialog opens.

3. Click **Browse…** and choose the destination folder or drive — your external drive.
   The path appears in the box.

4. Pick a **Volume**. Since this is your first drive, **register a new one right there**. Give it:
   - a **label** (a name you choose, like `Backup Drive A`),
   - a **kind** (choose **HDD** for a hard drive, or **SSD** for a solid-state drive),
   - a **location** (free text, like `Home office shelf`),
   - an **Onsite/Offsite** choice (choose **Onsite** for now — it lives with you).

5. Start the write.
   The package streams to the drive, then the app re-reads and verifies it.

6. Click the **Jobs** tab to watch.
   You should see live **MB/s** (megabytes per second — how fast it is writing) and an **ETA** (estimated time left).

7. Wait for the lifecycle rail to reach **VERIFIED**.
   When it says **VERIFIED**, the copy is trusted — Mnemosyne confirmed it is a perfect match.

![The Jobs tab showing MB/s and ETA during a write](../img/03-write-verify.png)

---

## Step 6: See your backup and read the RESTORE.txt

This last step is the reassuring part. Let's open the drive and look at what Mnemosyne made.

1. Open your external drive in your computer's file manager (File Explorer on Windows, Finder on Mac).
   You should see a folder for your package.

2. Open that package folder.
   Inside you should see:
   - `NAME.tar` (or `NAME.tar.gpg` if you encrypted it) — the actual bundle of your files,
   - `NAME.par2` — the recovery data,
   - `NAME.manifest.json` — a list of every file in the package,
   - `RESTORE.txt` — plain-English instructions.

3. Open **RESTORE.txt** in any text editor and read it together.
   It explains, in plain English, how to get your files back using **three free tools** (par2, gpg, and tar) — **even if Mnemosyne no longer exists**.

![The package folder open, showing RESTORE.txt and the other files](../img/03-restore-txt.png)

That RESTORE.txt is your safety net. Your backup does not depend on this app being around forever. As long as you have the drive and those three free tools, you can recover your files by hand.

---

## How to know it worked

- The archive in **Vault** shows a **file count** after scanning.
- On **Packages**, your package's lifecycle rail reached **VERIFIED** (not red/FAILED).
- On the **Volumes** tab, your drive appears with the **label** and **location** you gave it, holding one **Copy**.
- Opening the drive shows the package folder with `NAME.tar`, `NAME.par2`, `NAME.manifest.json`, and `RESTORE.txt`.

## If something went wrong

- **The rail turned red (FAILED) during Build.** The most common cause is not enough space in your staging folder. Free up space on the staging drive, or set a roomier staging folder in Settings, then click **Build package** again.
- **Write failed or verify failed.** The drive may have been unplugged, gone to sleep, or run out of space. Reconnect the drive, make sure it is mounted and has room, and try **Write to volume…** again. A failed verify means the app did its job and caught a bad copy — your originals are untouched.
- **The app will not build because you chose encryption.** You need **two keystores** registered first. See the "Set up safely" guide.
- **No file count after scanning.** Check the **Jobs** tab — the scan may still be running, or it may show an error (for example, if the folder was moved or unplugged mid-scan). Rescan once the folder is available.
- Stuck on any screen? Click the small **ⓘ** help button for a reminder.

## Screenshots to capture

- `../img/03-create-archive.png` — The Vault tab with a new archive named Family Photos.
- `../img/03-scan-job.png` — The Jobs tab showing a scan job in progress.
- `../img/03-plan-packages.png` — The Plan packages dialog with a target size chosen.
- `../img/03-build-staged.png` — The Packages tab showing the lifecycle rail reaching STAGED.
- `../img/03-write-verify.png` — The Jobs tab showing MB/s and ETA during a write.
- `../img/03-restore-txt.png` — The package folder open on the drive, showing RESTORE.txt and the other files.
