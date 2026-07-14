# Moving to a new computer

Your files live on your drives, tapes, and discs — but everything Mnemosyne *knows*
about them (every hash, every verification, which copy sits on which volume, your
whole history) lives in its records on this computer. When you move to a new machine
or reinstall your operating system, you want that knowledge to come along, exactly as
it was. This guide shows you how, in five steps.

Nothing here touches your actual backups on media. You are only moving the app's
**brain**, not the data it protects.

Some words you'll see:

- **App backup** = one file holding everything the app knows — the catalog, your
  settings, and your job history. It is *not* your photos or documents; it's the
  record *about* them.
- **Catalog** = Mnemosyne's own record of what your backups contain.
- **Keystore** = the file holding the passphrases that unlock your *encrypted*
  backups. These are secrets, kept separate on purpose.
- **Serial** = a drive's built-in ID number. Mnemosyne recognizes a drive by its
  serial, so it knows a drive it has seen before.

---

## The three lifeboats (which one is this?)

Mnemosyne can hand you three different "lifeboats," and it's worth knowing them apart:

- **App backup** — *for moving.* One file that carries the whole brain to a new
  computer. This guide. It assumes your media still exist and you just need the app to
  know about them again.
- **Catalog snapshot on media** (written when you *seal* a volume, and gathered in the
  **Recovery Kit**) — *for disaster.* A copy of the records travels *on the media
  itself*, so even if this computer and its backups are gone, whatever media you
  recover can describe itself and be rebuilt. See [The Recovery Kit](09-the-recovery-kit.md).
- **Structure export** — *for sharing.* A data-free description of how your archive is
  organized (hashes, roles, events — no file content), safe to email or hand to a
  collaborator. See [Your first backup](03-your-first-backup.md).

If you're setting up a new PC and still have your drives, you want the **app backup**.

---

## Step 1 — Make an app backup on the old computer

On the machine you're leaving, open **Settings → Back up this app's records**, or use
the **Back up this app's records** link at the bottom of Home. Choose a destination
folder — a USB stick or a NAS folder is ideal — and click **Create backup**.

The app writes **one `.tar` file** (named like `mnemosyne-appbackup-20260714-120000.tar`)
plus a small `.sha256` checksum file beside it. The `.tar` is a plain, uncompressed
archive you could open with any standard `tar` tool; the `.sha256` lets a restore
prove the file arrived intact.

**About your encryption keys:** your keystores are **not** in this backup by default —
they're secrets you may keep on separate devices on purpose. Copy your keystore files
to the new machine yourself, **or** tick *"include them"* in the backup dialog (behind
a confirmation) to bundle them in. Only include them in a backup you'll keep private.

## Step 2 — Install Mnemosyne on the new computer

Install the app on the new machine and start it once (see
[Install it and take the first run](01-install-and-first-run.md)). You don't need to
configure anything yet — the very first screen will offer to restore.

## Step 3 — Restore your app backup

Carry the `.tar` file over (USB stick, or reach the NAS folder). On the new machine's
**first screen**, choose **Restore from an app backup**. (Already past the first
screen? It's also in **Settings → Restore from an app backup**.)

Point it at your `.tar` file and click **Check this backup** to confirm it's valid,
then **Restore**. The app:

- verifies the file's checksum and every item inside it before touching anything;
- refuses a backup made by a *newer* version of the app than you're running (it will
  tell you to update first — so update, then restore);
- backs up whatever records are already on the new machine (to a `pre-restore-…`
  folder), so the restore is itself reversible;
- then brings your catalog, settings, and history back.

When it finishes, you'll see a summary of what was recovered — how many archives,
files, volumes, packages, and jobs, and that your verification history came across.

## Step 4 — Let it settle, and check the summary

Read the post-restore summary. If your keys were **not** in the backup, it will remind
you to copy your keystore files onto this machine and point **Settings → keystores** at
them. Do that now if you use encrypted backups.

## Step 5 — Plug in a drive

Plug in any of your backup drives. Mnemosyne recognizes each one by its **serial**, so
a drive it knew on the old computer is known again here — no re-adopting, no
re-scanning. The same is true the next time you insert each tape or drive.

---

## How to know it worked

- The restore summary showed your archives, files, and volumes — the numbers you
  expected.
- Your verification history is intact (packages still show when they were last
  verified, not "never").
- A drive you plug in is recognized by name, not offered as a brand-new drive.
- If you use encryption, your keystores are listed as reachable in **Settings**.

## If something went wrong

- **"This backup was created by a newer version."** Your new machine is running an
  older app than the one that made the backup. Update Mnemosyne on the new machine,
  then restore again.
- **"Integrity check failed" / "the backup is corrupted or was altered."** The `.tar`
  didn't arrive intact. Copy it again from the source (keep the `.sha256` file next to
  it), and don't edit the `.tar` by hand.
- **A drive isn't recognized after restore.** Make sure it's fully mounted (shows as a
  drive letter), then open **Inventory drives** — a known drive is matched by serial and
  offered as a re-verify, not a re-adopt.
- **I want my old records back.** They were saved to the `pre-restore-…` folder in the
  data directory before the restore. Nothing was deleted.

## Screenshots to capture

- `../img/12-moving-backup-dialog.png` — the "Back up this app's records" dialog with a
  destination folder chosen and the keystore checkbox visible.
- `../img/12-moving-restore-firstrun.png` — the first-run "Start fresh / Restore from an
  app backup" chooser.
- `../img/12-moving-restore-summary.png` — the post-restore summary showing recovered
  counts and the "plug in any drive" next step.
