# Keeping a backup current (the weekly ritual)

Your first backup is a snapshot in time. But you keep working — new shoots land, edits happen, projects grow. **Keeping a backup current** means topping it up with just what's new, without re-copying everything. Mnemosyne makes this a five-minute ritual you can do weekly.

The good news up front: **you never have to think about "full vs. incremental" backups.** Old backup tools make you schedule a slow "full" backup now and then, with faster "incrementals" in between, and pray the chain stays intact. Mnemosyne doesn't work that way. It tracks **every file individually** — so it always knows, file by file, what's already safe and what isn't. "Back up changes" just copies the difference. There's no chain to break, and nothing to remember.

Some words you'll see:

- **Delta** = the difference — the files that are new or changed since what's already backed up.
- **Volume** = one physical drive, tape, or disc you back up onto.
- **Mirror** = plain-file copies you can browse with any file manager (the default for drives).
- **Session** = one "back up changes" run, recorded in your history.

---

## The five-step ritual

1. Plug in the drive you rotate your backups onto, and wait for it to mount.
2. Open the **Archives** tab and click **Back up changes…** on the archive you're keeping current.
3. Choose **what to back up** (see the two options below). Pick the destination volume — or register a new one right there.
4. Read the preview: it shows **how many files, how many gigabytes, a per-type breakdown, and whether it fits** on the drive. Nothing has moved yet.
5. Click **Back up changes**. Each file is copied and immediately re-read to verify it landed intact, then recorded as a verified copy. Done.

That's it. Do it weekly (or whenever you've done a batch of work), and your backup drive stays current with almost no effort.

---

## The two choices, in plain terms

When you click **Back up changes**, you pick one of two things to copy:

- **"Everything not yet on this volume."** Copies the files this particular drive doesn't already hold — new files and changed ones. This is the one to use when you **rotate drives**: each drive gets topped up to match your originals. It's the everyday choice.

- **"Everything not fully protected."** Copies the files that haven't yet met your safety goal (their **profile** — how many copies, on how many kinds of media, how many offsite). Use this when your aim is to **raise overall protection** — for example, making a second or third copy of everything that's still short.

Either way, the preview tells you exactly what will happen before you commit.

---

## Why interruptions don't matter

Because every run **recomputes the delta from scratch**, there is nothing to corrupt and nothing to resume. If a run is interrupted — the drive is unplugged, the power blips, you cancel it — just run **Back up changes** again. It looks at what actually made it onto the drive and copies whatever's still missing. Skip a week? Same thing: the next run simply catches up. No "backup chain," no bookkeeping, no way to end up with a half-broken incremental set.

---

## Where drives land — and mirrors vs. packages

The output follows the destination:

- **Drives (the default): plain-file mirrors.** Your files land in their normal folder structure. You can browse or restore them with any file manager — no Mnemosyne, no key, no unpack step. Each drive also carries a refreshed inventory sidecar describing what's on it.
- **Tape or optical: packages.** Mnemosyne plans the delta into media-sized packages, which you then build and write from the **Packages** tab (each write records a verified copy). Tape and disc can't be browsed like a folder, so sealed packages are the right shape for them.

---

## Your backup history

Every run is recorded as a named **session** — for example, *"Incremental to ARCH-03 — 214 files, 8.1 GB, 2026-07-09."* Open **Backup history…** on the archive to see them, newest first. These sessions also teach the **Home** screen to recognize your rotation: a drive you keep backing up shows up as *"looks like periodic backups of &lt;Archive&gt;"* — no configuration needed.

## If something looks off

- **"Already current — nothing new to back up."** Good news: this drive already holds everything in scope. Nothing to do.
- **"Too big."** The delta won't fit on the destination's free space. Free some room, use a larger drive, or narrow the scope with **Limit to specific folders**.
- **The preview shows more than you expected.** If you modified a lot of files (or a re-edit touched many sidecars), they all count as "changed." Check the **Rescan & compare** view to see exactly what differs.

## Screenshots to capture

- `../img/11-back-up-changes.png` — The "Back up changes" dialog with the two base choices and the live delta preview.
- `../img/11-backup-history.png` — The Backup history list on an archive.
