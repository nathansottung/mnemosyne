# Backing up to LTO tape

This guide shows you how to back up your work onto LTO tape. Take it slow. You cannot hurt your originals — Mnemosyne only ever reads your source folders. It never changes, moves, or deletes them, and it never sends anything over the internet.

Some words you'll see:

- **LTO** = a kind of magnetic tape cartridge made for long-term backup. One cartridge holds a lot and lasts for decades.
- **Package** = one media-sized sealed unit that Mnemosyne makes. Inside it are your files (bundled into one big file), extra "recovery" data to repair damage, a list of what's inside, and a plain-text restore guide.
- **Mount / mounted** = when a drive or tape shows up on your computer as a drive letter (like `E:\`) or a folder you can open.
- **Hash** = a short fingerprint of a file's exact contents. Change one single byte and the fingerprint changes. This is how the app checks that a file is still perfect.
- **Verify** = the app re-reads what it wrote and re-checks the fingerprint, to prove the copy is good.

---

## Is tape worth it for you?

Tape is wonderful, but it is not for everyone.

**Tape is a good fit when:**

- You have a large collection (many terabytes).
- You want storage that lasts for decades.
- You want "cold" copies you can store offsite (somewhere other than your home or studio), safe from fire, flood, or theft.

**Tape is probably NOT worth it when:**

- You only have a few hundred gigabytes. A plain external hard drive is simpler and cheaper. (See guide 06 for mirror drives.)

The catch: tape needs a special tape drive, and those are expensive. One LTO-8 cartridge holds about 11 TB. One LTO-9 cartridge holds about 16.5 TB after the app's own overhead (the extra room used by recovery and packing data). If that scale matches your collection, read on.

---

## Step 1: Install an LTFS driver

Before Mnemosyne can write to tape, your computer needs to see the tape as a normal drive letter you can drop files onto. That is the job of an **LTFS driver** (LTFS = a free piece of software that makes an LTO tape act like an ordinary drive, so `E:\` might become your tape).

**Mnemosyne does NOT include an LTFS driver.** You install one yourself, separately, one time. This is normal and expected.

1. Pick one LTFS driver and install it. Common choices are IBM Storage Archive (also called Spectrum Archive Single Drive Edition), HPE StoreOpen, and the open-source LTFS project. See the README's LTFS links for where to download these.
2. Follow that driver's own installer. It is separate software, so it has its own steps.
3. Insert a tape into your tape drive and wait a moment for it to load.

You should now see the tape appear on your computer as a drive letter, the same way a USB stick does.

## Step 2: Confirm Mnemosyne sees the tape

1. In Mnemosyne, open the **Settings** tab on the left.
2. Look for the line that reports whether an LTFS tape is detected.

![Settings showing a detected LTFS tape](../img/05-tape-settings.png)

You should now see Settings confirm that an LTFS tape is detected. If it does, the driver is working and you are ready to write. If it does not, see **If something went wrong** at the bottom.

---

## Step 3: Plan your packages for tape

A "plan" decides how your work is cut into media-sized Packages that fit your tape.

1. Open the **Packages** tab.
2. Choose **Plan packages…**.
3. When asked for the target media, pick **LTO-8** or **LTO-9** to match the cartridges you bought.
4. Let the plan finish.

![Plan packages dialog with LTO target selected](../img/05-tape-plan.png)

You should now see one or more planned Packages sized to fit your chosen tape.

---

## Step 4: Write a single package to tape

If a Package fits on one tape, this is all you do.

1. Make sure your tape is inserted and shows as a drive letter (Step 1).
2. On the **Packages** tab, find the package you want to write.
3. Start the write to tape. Every path box has a **📁 Browse…** button if you need to point at the tape's drive letter.
4. Watch the **Jobs** tab. Long jobs run in the background there, with a progress bar, a live speed in MB/s (megabytes per second), and an ETA (estimated time remaining).

![Jobs tab showing a tape write in progress with MB/s and ETA](../img/05-tape-write.png)

You should now see the job finish, then automatically verify (re-read and re-check the fingerprint). When it says the copy is verified, that tape holds a proven-good copy.

5. Take the tape out and write the package name on the cartridge label with a marker, so you can find it later.

---

## Step 5: Spanning a big package across several tapes

Sometimes one Package is bigger than one tape. Mnemosyne handles this by **spanning** — splitting the package across several tapes. You do them one at a time in a simple rhythm. Each tape is checked on its own, so you are never trusting the whole set to a single unchecked cartridge.

A spanned package shows a **Write next segment →** button. Here is the rhythm:

1. Insert the first blank tape. Wait until it shows as a drive letter.
2. On the **Packages** tab, click **Write next segment →**.
3. Watch the **Jobs** tab for the progress bar, MB/s, and ETA.
4. When that segment finishes, the app verifies it. Wait for the verify to pass.
5. When the app tells you it is safe to eject, take the tape out and label it with a marker. Number your tapes in order (for example, "Family Photos — tape 1 of 3").
6. Insert the next blank tape and wait for it to mount.
7. Click **Write next segment →** again.
8. Repeat this eject-and-label rhythm until every segment is done.

![Packages tab showing the Write next segment button for a spanned package](../img/05-tape-span.png)

You should now see each tape verified one by one, and the package marked complete when the last segment passes.

**About the recovery tape:** a spanned set may include an extra **par2 recovery tape**. Par2 is repair data — if one tape later gets a bad spot, this recovery tape can help rebuild the missing pieces. Keep it with the set and label it clearly (for example, "Family Photos — recovery tape").

---

## How to know it worked

- Settings shows that an LTFS tape is detected.
- Each tape's write job on the **Jobs** tab finished and then verified.
- For a spanned package, every segment verified, and the package shows complete.
- Each physical tape is labeled with a marker, in order, including any recovery tape.

If all of these are true, you have proven-good tape copies of your work.

## If something went wrong

- **Settings does not detect the tape.** The LTFS driver is not installed or the tape has not finished loading. Confirm the tape shows as a drive letter in your file manager first. If it does not, re-check the LTFS driver install (Step 1). Give the tape a full minute to load after inserting it.
- **The write job stalls or fails on the Jobs tab.** Your originals are untouched — nothing is lost. Try a fresh blank tape; the old one may have a bad spot. Make sure the tape is not write-protected (many cartridges have a small tab).
- **A segment failed to verify.** Do not eject and move on. A failed verify means that tape is not trustworthy. Insert a fresh blank and write that segment again.
- **You lost track of tape order.** That is what the marker labels are for. If any tape is unlabeled, re-verify the set (see guide 07) and label as you go.

## Screenshots to capture

- `../img/05-tape-settings.png` — Settings tab showing an LTFS tape detected.
- `../img/05-tape-plan.png` — Plan packages dialog with LTO-8/LTO-9 chosen as the target.
- `../img/05-tape-write.png` — Jobs tab with a tape write running, showing MB/s and ETA.
- `../img/05-tape-span.png` — Packages tab showing the "Write next segment →" button on a spanned package.
