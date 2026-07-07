# What is this? (start here)

Mnemosyne (say it "neh-MOSS-uh-nee") is a program that helps you **back up files
you never want to lose** — photos, videos, scans, a lifetime of work — and prove,
years from now, that every copy is still perfect.

It is built for one job: to keep your files **safe and provable for decades**,
not just until next week.

## What it does, in one page

1. **It remembers your files.** You point it at a folder. It reads every file and
   records a *hash* for each one. (A "hash" is a short fingerprint of a file's
   exact contents — if even one byte changes, the fingerprint changes. That's how
   the app can later tell whether a file is still perfect or has quietly gone
   bad.)

2. **It packages them for storage.** It bundles your files into sealed units
   called **packages** and adds extra *recovery data* (called par2) that can
   repair a limited amount of damage — like a scratch on a disc or a few bad spots
   on a drive.

3. **It writes copies to real media** — external hard drives, LTO tapes, or
   Blu-ray/DVD discs — and **immediately reads each copy back** to confirm it was
   written perfectly. A copy it can't verify is not trusted.

4. **It tracks where every copy lives** and reminds you to re-check them on a
   schedule, so you catch problems early — on a calm Tuesday, not during a real
   emergency.

You drive all of this from a simple page in your web browser. There is nothing to
learn about the command line (the text window where you type commands) — though if
you ever want to, you can.

## The promise

**Your files, verifiable forever, and restorable with three free tools even if
this app disappears.**

Every package the app writes includes a plain-text file called `RESTORE.txt` that
explains, in ordinary language, how to get your files back using three free,
standard tools — `par2`, `gpg`, and `tar` — that run on Windows, Mac, and Linux.
So you are **never locked in**. Even if Mnemosyne vanished tomorrow, and even
decades from now, anyone technical could follow that page and recover your files
straight from the media.

## What it will NEVER do

- **It never touches your originals.** It only ever *reads* your source folders.
  It will not move, rename, change, or delete a single original file. If you ever
  try to point a *write* action at your originals, it refuses.
- **It never deletes your data.** Nothing you back up is removed by the app.
- **It never phones home.** It runs entirely on your own computer. It does not
  send your files, your file names, or any information about you anywhere. There
  is no account, no cloud, no tracking.

## Who this handbook is for

You. It assumes you are careful and thoughtful, but **not** a programmer. It
explains every technical word the first time it appears, and every step tells you
what you should see so you always know it worked.

## Where to go next

- **[Install it and take the first run](01-install-and-first-run.md)** — get the
  app running and open its page.
- **[Set up safely](02-set-up-safely.md)** — the few choices worth making up
  front.
- **[Your first backup](03-your-first-backup.md)** — a full walkthrough, start to
  finish.
- Stuck on a word? See the **[glossary](glossary.md)**.
- Something acting up? See **[troubleshooting](troubleshooting.md)**.

## Screenshots to capture

- `../img/00-home.png` — the app's Home screen on first open, showing the left-hand
  tabs and the "Getting started" checklist.
