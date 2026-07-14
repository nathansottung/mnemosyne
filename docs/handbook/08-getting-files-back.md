# Getting your files back

This is the guide that matters most. A backup is only worth something if you can get your files out again. The good news: Mnemosyne is built so you can **always** get your files back — with the app, or without it, even many years from now.

A few words you will see below:

- A **"package"** is one sealed unit of your backup on a drive, disc, or tape. It is a folder that holds: `NAME.tar` (the actual bundle of your files — "tar" is a plain, decades-old way to pack many files into one) — or `NAME.tar.gpg` if you encrypted it (**"encrypted"** = scrambled so only someone with the secret passphrase can read it); a `NAME.par2` **recovery set** (extra repair data that can fix a limited amount of damage, like scratches on a disc); a `NAME.manifest.json` (the list of files inside); and a `RESTORE.txt` (plain-English restore steps that live right on the medium).
- A **"medium"** or **"volume"** is one physical thing that holds packages: a drive, a disc, or a tape.
- To **"mount"** a drive means to plug it in so it appears on your computer — as a drive letter like `E:\` on Windows, or a folder you can open on a Mac.
- A **"passphrase"** is the long secret password that unlocks an encrypted package. It lives in a **"keystore"** (a small file that holds your passphrases).
- A **"terminal"** or **"command line"** is a text window where you type commands instead of clicking. On Windows it is called **Command Prompt** or **PowerShell**; on a Mac it is called **Terminal**. You only need it for the by-hand method further down, and every step is spelled out.

Reassurance before you start: restoring **never** touches your backup media in a harmful way, and it **never** writes into your original folders. You always restore **into a fresh, empty folder** that you choose. If anything looks wrong, you can simply try again.

---

## Scenario A: Get one file back

Use this when you deleted or lost a single file and you know which package it lives in.

1. Plug in the drive (or load the disc/tape) that holds the package.
   You should now see it appear on your computer — for example as `E:\` on Windows, or as a drive on your Mac desktop.

2. In Mnemosyne, click the **Packages** tab on the left.
   You should now see a list of your written packages.

3. Find the package that contains your file and click its **Restore package…** button.
   A restore window opens.
   ![The restore window](../img/08-restore-window.png)

4. In the box labeled **Source (package folder on the medium; blank = recorded location)**, click **Browse…** and point to the package folder on your drive. If you leave it blank, Mnemosyne uses the location it remembers.
   You should now see the source path filled in (or left blank on purpose).

5. In the box labeled **Restore into**, click **Browse…** and pick a **new, empty folder** to receive the file — for example a folder called `Restored` on your desktop. **Never** pick one of your original folders.
   You should now see your chosen output folder in the box.

6. In the box labeled **Only these paths (optional, one per line)**, type the path of the single file you want, exactly as it appears in the package. For example: `photos/2019/beach.jpg`. One file per line.
   You should now see that one path listed.

7. Click the **par2 → decrypt → extract** button.
   Restoring starts. You should now see a message telling you to check the **Jobs** tab.

8. Click the **Jobs** tab to watch progress.
   You should now see the restore job running, then finishing. The three stages you may see are: **par2 verify** (checking the package is undamaged, and repairing minor damage if needed), **decrypt** (only if the package was encrypted — this needs the passphrase from a reachable keystore), and **extract** (pulling your file out).

9. Open your output folder.
   You should now see your one file sitting there, safe.

---

## Scenario B: Get one whole package back

Use this when you want everything in a package — for example, restoring an entire year of photos.

### The easy way (with the app)

Follow Scenario A above, but in **step 6 leave the "Only these paths" box empty**. That tells Mnemosyne to restore the **whole** package. Everything else is the same: pick a fresh empty output folder, click **par2 → decrypt → extract**, and watch the **Jobs** tab.

You should now see all of the package's files appear in your output folder.

### The by-hand way (works even if Mnemosyne is gone)

This is the promise that makes Mnemosyne trustworthy for the long haul: you do **not** need this app to get your files back. Every package carries a plain text file called **`RESTORE.txt`** that tells you exactly what to do, using three small tools that are **free** and available on Windows, Mac, and Linux:

- **`par2`** — checks the package for damage and repairs a limited amount.
- **`gpg`** — decrypts (unscrambles) an encrypted package, after asking for the passphrase. You only need this if the package was encrypted.
- **`tar`** — unpacks the bundle back into your files.

Here is how to do it:

1. Plug in the medium and open the package folder on it.
   You should now see files like `NAME.tar` (or `NAME.tar.gpg`), a `.par2` file, a `.manifest.json`, and `RESTORE.txt`.

2. Open `RESTORE.txt` by double-clicking it (it opens in a plain text viewer like Notepad or TextEdit).
   You should now see step-by-step restore instructions written for this exact package, including the real file names to type.

3. Open a **terminal** (Command Prompt or PowerShell on Windows; Terminal on a Mac) and move into the package folder. (In Windows, an easy trick: open the package folder, then in the address bar type `cmd` and press Enter — a terminal opens already pointed at that folder.)
   You should now see a text window ready for commands.

4. **Check for damage** by typing the par2 command shown in `RESTORE.txt`. It looks like this (the exact name comes from `RESTORE.txt`):

   ```
   par2 verify NAME.tar.par2
   ```

   You should now see a report. If it says the data is good, go on. If it reports damage, repair it by running:

   ```
   par2 repair NAME.tar.par2
   ```

   You should now see it fix the damage, if there is a small amount to fix.

5. **Only if the package is encrypted** (its bundle is named `NAME.tar.gpg`), decrypt it by typing the gpg command from `RESTORE.txt`:

   ```
   gpg -d -o NAME.tar NAME.tar.gpg
   ```

   You should now be asked for the passphrase. Type the passphrase for that package (from your keystore or a Recovery Kit QR card — see guide 09). When it finishes, you should now see a `NAME.tar` file that is no longer scrambled.

6. **Unpack the files** by typing the tar command:

   ```
   tar -xf NAME.tar
   ```

   You should now see your files appear, unpacked, in the current folder. To pull out **just one file** instead of everything, add its path:

   ```
   tar -xf NAME.tar path/to/one/file
   ```

That is the whole secret. Three free tools, in that order: **par2**, then **gpg** (only if encrypted), then **tar**. `RESTORE.txt` on every medium spells it out for you, so you never have to remember it.

---

## Scenario C: The disaster — your computer or server is gone

This is the worst day: the machine that ran Mnemosyne is dead, lost, or stolen, and all you have left are your backup drives, discs, or tapes. **Your files are still safe.** Here is the honest truth about how you get them back — there are **two real paths**, and you can use either one.

**Please read this first, so there are no surprises:** Mnemosyne does **not** have a one-click "rebuild my catalog" or "import my backup" button. Do not go looking for one — there isn't one, and that is by design. The two paths below are the real way to recover, and both work.

### Path 1: Re-adopt your media into a fresh install

This rebuilds Mnemosyne's catalog (its list of what you have and where) **from the media themselves**, without changing a single byte on your drives.

1. Install Mnemosyne again on any computer and open it in your browser.
   You should now see the familiar tabs: Home, Vault, Protection, and the rest.

2. Click the **Vault** tab, type the **same archive name** you used before into the **Create archive** box, and click **Create archive**.
   You should now see your (empty) archive listed.

3. Plug in your first backup drive (or load the disc/tape) so it mounts.
   You should now see it appear on your computer as a drive letter or folder.

4. Click the **Volumes** tab, then click **Adopt media…** and point it (with **Browse…**) at that drive's mount, choosing the archive from step 2.
   You should now see an adoption job start — check the **Jobs** tab.

5. Wait for it to finish, then repeat step 3 and 4 for **each** backup drive, disc, or tape you own.
   As each one finishes, Mnemosyne fingerprints (**"fingerprint"** = takes a short unique code of the file's contents, so it can prove the file is intact) every package bundle it finds — the `NAME.tar` or `NAME.tar.gpg` files — and re-catalogs them as verified packages, marked **ADOPTED-VERIFIED**.

   You should now see your packages reappear on the **Packages** tab, rebuilt straight from your media.

6. From here, restore any file or package normally, using **Scenario A or B** above.

Helpful to know: each medium also carries a small sidecar file (an inventory named something like `MNEMOSYNE_...` / `catalog_snapshot.json`) that lists what is on it, for your own reference. You do not have to open it — adoption reads the media directly — but it is there if you want a plain record.

### Path 2: Restore by hand, with no app at all

You do not even need to reinstall Mnemosyne. Every medium carries `RESTORE.txt`, and you (or any technical friend) can follow the **by-hand par2 → gpg → tar** steps from **Scenario B** above to pull your files straight off the media. The **Recovery Kit** (guide 09) bundles the full long-form instructions and, for encrypted data, the passphrases — so this works decades from now.

### The honest bottom line

There is **no automatic catalog-import button**. Re-adopting your media (Path 1) is how you rebuild your catalog inside the app, and by-hand restore (Path 2) is how you get files back with no app at all. Both are real, both are tested, and both leave your media untouched.

---

## How to know it worked

- For a single file or a whole package: your chosen **output folder** now contains the restored files, and the **Jobs** tab shows the restore finished without errors.
- For the disaster case (Path 1): after adopting each medium, your packages appear again on the **Packages** tab marked **ADOPTED-VERIFIED**, and you can restore from them.
- For the by-hand method: after `tar -xf`, your files are sitting in the folder, readable.

## If something went wrong

- **The restore job failed at the "par2 verify" stage.** The medium may be damaged or the source folder may be wrong. Re-check the **Source** path points at the real package folder. par2 can only repair a limited amount of damage; if a drive is badly failing, try another copy of the package on a different medium.
- **It asks for a passphrase and none works.** The package is encrypted and needs the correct passphrase from a reachable keystore (or a Recovery Kit QR card). Make sure a keystore holding that key is plugged in and listed on the **Keys** tab.
- **"Not enough room on the destination."** Your output drive is too full. Free up space or pick a different, larger drive to restore into.
- **You accidentally pointed the output at your originals.** Mnemosyne refuses to restore into a folder inside a scanned source folder and will say so. Choose a different, empty folder.
- **The by-hand tools are "not recognized."** The free tools are not installed on this computer. Install them: `tar` ships with Windows 10 and later and with Macs; get `gpg` from Gpg4win (Windows) or `brew install gnupg` (Mac); get `par2` via `choco install par2cmdline` (Windows) or `brew install par2` (Mac). On Linux, use your package manager (for example `apt install par2 gnupg tar`).
- **You are looking for an "import catalog" button and cannot find it.** That is expected — there isn't one. Use **Path 1 (Adopt existing media)** to rebuild your catalog.

## Screenshots to capture

- `../img/08-restore-window.png` — The restore window with Source, Restore into, and the "Only these paths" box.
- `../img/08-jobs-restore.png` — The Jobs tab showing a restore running through par2, decrypt, extract.
- `../img/08-adopt-media.png` — The Volumes tab with the "Adopt existing media" button.
- `../img/08-restore-txt.png` — A `RESTORE.txt` file open in a plain text viewer.
