# Checking on your archive (the yearly ritual)

Backing up once is not enough. Once a year, you check that your backups are still good and that your originals haven't drifted away from them. This guide walks you through that ritual. Nothing here changes or deletes your files — checking only reads.

Some words you'll see:

- **Hash / fingerprint** = a short code made from a file's exact contents. Change one byte and it changes. Checking compares fingerprints to prove a copy still matches.
- **Bit-rot** = when a stored file slowly goes bad on its own over the years, even though nobody touched it. A checkup catches this early.
- **Catalog** = Mnemosyne's own record of what your backups should contain.
- **Package** = one sealed unit on your media. **Copy** = one package living on one volume (a physical disc, drive, or tape). **Volume** = the physical medium you hold.
- **Drift** = the difference between your original source folders today and what you backed up earlier.
- **Mount / mounted** = when a tape, disc, or drive shows up as a drive letter (like `E:\`) you can open.

---

## Why check at all?

Storage fails quietly. A disc develops a bad spot, a drive gets a flaky sector, a tape sheds a little — and nothing warns you. **Bit-rot** happens with no error message. If you only find out during a real emergency (you've lost the originals and reach for the backup), that's the worst possible time.

A yearly checkup flips this around. You find problems **on your own schedule**, while you still have the originals and can simply make a fresh copy. That is the whole point of this ritual: no surprises later.

---

## Part 1: Verify your media (a verify campaign)

This step re-reads a physical medium and re-checks every package's fingerprint against the catalog. You do it for each tape, disc, or mirror drive you want to confirm.

1. Insert the medium (tape, disc, or drive) and wait until it mounts as a drive letter.
2. Open the **Packages** tab.
3. Choose **Verify a medium…**.
4. When asked for a level, choose the depth of the check:
   - **A — Census:** just confirms each package exists and is the right size. Takes seconds. This does **not** prove the contents are good.
   - **B — Full:** reads the complete contents and re-checks the whole fingerprint. This is the **only** level that counts as truly verified.
   - **C — Sample:** checks only the ends of each package. Fast, but partial.
5. Pick **B (Full)**. It is the default and the recommended choice. Use A or C only for a quick spot-check between real verifies.
6. Let it run. The **Jobs** tab shows progress; long checks run in the background there.

![Verify a medium dialog with level B selected](../img/07-checking-on-your-archive-verify.png)

You should now see each package on that medium pass its full check. A package that passes level B is truly re-verified as of today.

---

## Part 2: Rescan your source and read the drift report

The second step looks the other way — at your **original source folders** — to see what has changed since you backed up. This is called drift.

1. Open the **Vault** tab.
2. Find the archive you want to check.
3. Choose **Rescan & compare** for that archive.
4. Let it re-fingerprint your source folders. Watch the **Jobs** tab for progress.

![Rescan and compare showing a drift report](../img/07-checking-on-your-archive-drift.png)

You should now see a **drift report**. It sorts every file into one of these states:

- **Unarchived (added):** a new file in your source that was never backed up. Back it up.
- **Modified:** a file that changed since you backed it up. Back up the new version.
- **Missing:** a file that's gone from your source. Your backup still has it — decide if that's on purpose.
- **Moved:** a file that's the same, just in a new place. Usually nothing to fix; the app recognized it by content.

The rescan also flags any package as **verify due** if it hasn't had a full (level B) check within the window — 12 months by default. That's your reminder to run a verify campaign (Part 1) on the media holding it.

---

## Part 3: Read the status colours and act on each

Across the app, each part of your archive shows a coloured status. Here's what each one means and what to do.

There are **six** statuses. Each is always shown as **colour + icon + words**
together (so it still reads clearly if you're colour-blind or printing in black and
white):

| Colour | Status | What it means | What to do |
|--------|--------|---------------|------------|
| Green | **COMPLETE** | Fully backed up and verified, meeting your goal. | Nothing. You're good. |
| Blue | **OVER_COMPLETE** | More protected than your goal asks (e.g. an extra copy). | Nothing — this is a good "problem." |
| Amber | **PARTIAL** | Backed up, but a gap remains (e.g. "2/3 copies · 0/1 offsite"). | Add whatever the message says is short. |
| Red | **NOT_BACKED_UP** | No verified copy exists yet. | Back it up now. |
| Purple | **OUT_OF_POLICY** | A verify is overdue, or a copy sits on a medium you've disallowed. | Re-verify it, or move/replace the copy. |
| Gray | **UNASSIGNED** | No protection profile is set, so there's no goal to measure against. | Assign a profile on the **Protection** tab. |

Work down the list by colour: clear the reds first (no copy at all), then the
ambers (a gap), then the purples (overdue or out-of-policy), then any grays (give
them a goal). Greens and blues need nothing.

---

## A suggested yearly routine

Do this once a year — pick a memorable date, like a birthday or New Year.

1. **Rescan & compare** every archive (Part 2). Handle drift: back up anything unarchived or modified.
2. **Verify a medium…** at level B (Part 1) for each tape, disc, and mirror drive — especially any flagged **verify due**.
3. **Clear the colours** (Part 3): fix reds, then ambers, then purples.
4. **Check your offsite copy.** Make sure at least one good copy lives somewhere other than home (an Offsite volume).
5. **Write down the date** you finished, so next year you know it's been a full cycle.

You should now have every archive rescanned, every medium freshly verified, and every status either green or on a to-do list.

---

## How to know it worked

- Each medium you verified passed at level B (Full).
- You ran **Rescan & compare** on every archive and acted on the drift report.
- No **NOT_BACKED_UP** (red) items remain unaddressed.
- Nothing is flagged **verify due** anymore.
- You have a fresh date written down for the checkup.

## If something went wrong

- **A package fails a level-B verify.** That copy is no longer trustworthy — but your originals are safe. Make a new copy on fresh media, then re-verify it.
- **The medium won't verify because it won't mount.** Confirm it shows as a drive letter in your normal file manager first. A tape needs its LTFS driver loaded (see guide 05).
- **The drift report shows many "missing" files.** Check that you pointed the rescan at the right source folder, and that the drive holding your source is fully connected. Missing usually means the source moved, not that anything was deleted from your backup.
- **A status stays purple after you verified.** The verify may not have been level B, or the copy is on a medium your policy disallows. Re-run the verify at level B, or move that copy to an allowed medium.
- **You're overwhelmed by the report.** Start with red only. One red item fixed is real progress. The app never deletes anything, so you can take your time.

## Seeing what happened

Every job — a scan, a build, a write, a verify — should answer three plain
questions: **what did it do, to what, and where can I see the result?** Two places
answer them.

**After any job: open its detail.** On the **Jobs** page, click any row — running
or finished. You get a plain summary (what ran, on what, how long it took), live
counters while it's still going (files done, MB/s), and an **Artifacts** list: every
concrete thing the job produced — a package staged (with its folder and size), files
copied, a catalog record touched, a drift report. Each artifact has a **Show**
button that opens the thing itself, so you're never left wondering where the output
went. Jobs are remembered across restarts; if one was cut off by a shutdown it shows
as **INTERRUPTED**, so you know to run it again rather than assuming it finished.

**After any scan or verify: open the Explorer.** A scan or verify job's detail has a
**View results** button that opens the **Explorer** (also reachable on its own from
**Check → Explore data**). The Explorer draws your archive as a size-weighted map —
each rectangle is a folder or file, and its area is how many bytes it holds — beside
a breakdown panel (totals, largest folders, file roles, counts by type). Switch the
colouring to **Validation** to see, at a glance, what is actually proven:

- **Green ✓** — a verified copy of this file exists on your media.
- **Neutral** — the file is hashed and catalogued, but no copy has been verified yet.
- **Red ✗** — a copy that should hold this file **failed** its last check.

Colour is always paired with text, so the state is readable without relying on
colour alone. Click any block to zoom in; use the breadcrumb to zoom back out. This
is the fastest way to confirm a checkup did what you expected: run a verify, open the
Explorer in Validation colouring, and watch the reds turn green.

The same Explorer browses an **unplugged drive** you've inventoried — pick it under
**Drive snapshots** — so you can look through a drive's captured contents (structure,
sizes, roles) without plugging it back in.

---

## Screenshots to capture

- `../img/07-checking-on-your-archive-verify.png` — "Verify a medium…" dialog on the Packages tab with level B selected.
- `../img/07-checking-on-your-archive-drift.png` — "Rescan & compare" drift report showing unarchived/modified/missing/moved files.
- `../img/07-checking-on-your-archive-jobdetail.png` — a finished job's detail view with its Artifacts list and Show actions.
- `../img/07-checking-on-your-archive-explorer.png` — the Explorer with Validation colouring, greens and reds beside the breakdown panel.
