# Glossary (plain-language definitions)

Every word here is defined the way a careful non-programmer would want it
explained. Terms are also defined the first time they appear in each guide.

### Adopt (adopt media)
To bring backups you *already* made — before using this app, or by hand — into the
app's records **without rewriting anything**. Found on the **Volumes** tab
("Adopt media…"). The app reads the existing packages, fingerprints them, and
lists them as verified copies.

### Archive
A body of work you keep together and manage as a unit — for example "Family
Photos" or "Wedding Business." You create archives on the **Vault** tab. An
archive is just a grouping; your real files stay where they are.

### Catalog
The app's own record book: the list of your archives, files, packages, volumes,
and their fingerprints. It lives in a single file on your computer. It never
contains your actual file contents — only records *about* them.

### Copy
One package sitting on one volume (one physical drive/tape/disc). The goal of
"3 copies" means the same package exists on three different volumes.

### Dock (dock session)
A guided way to feed a **box of old drives** into the app one at a time (the
**Dock** tab). You plug a drive in, the app fingerprints its files and matches
them by content against an archive, and records the matches as copies. It only
reads the drives.

### Drift
What has **changed in your source folders since you backed them up**: files added
("unarchived"), changed, missing, or moved. You check for drift with "Rescan &
compare" on the **Vault** tab.

### Encryption
Scrambling a package so only someone with the secret passphrase can read it. It is
**optional**. If you turn it on, the app uses the tool **gpg** and requires at
least **two keystores** (see below).

### Finalize / Seal
Declaring a volume "done and put away." A sealed volume refuses further writes
until you deliberately **unseal** it (which is logged). Found on a volume's detail
page.

### gpg
A free, standard tool that encrypts and decrypts files. Mnemosyne uses it only if
you choose encryption. It is one of the three tools that make your backups
restorable by hand.

### Hash
A short **fingerprint of a file's exact contents**. If even one byte of the file
changes, the fingerprint changes completely. This is how the app can later tell
whether a stored file is still perfect or has quietly gone bad. Mnemosyne uses a
kind called SHA-256.

### Integrity preset
A single named choice for "how hard to prove each copy": **ARCHIVAL** (the most
thorough, the default), **BALANCED**, or **FAST** (only for data you could
recreate). Set on the **Integrity** tab. Reading a copy back after writing is
*always* on and cannot be turned off.

### Keystore
A small file that holds the secret passphrases for your **encrypted** packages.
The app requires **two** of them (ideally on two different devices, like your
computer and a USB stick) so a single lost file can't lock you out. The catalog
never stores the passphrase itself — only a fingerprint of it.

### LTO / tape
LTO is a type of magnetic **tape cartridge** used for long-term backup. One
cartridge holds a lot (many terabytes) and lasts for decades, but needs a special
tape drive and an **LTFS** driver.

### LTFS
A free driver that makes an LTO tape show up like an ordinary drive letter you can
copy files to. Mnemosyne does not include it; you install it separately. See
[the tape guide](05-tape.md).

### Manifest
A file (`NAME.manifest.json`) written next to each package on the media, listing
exactly which files are inside it. Human-readable.

### Mirror
Plain files copied straight onto a drive (via "Mirror backup…" on the **Vault**
tab), so you can open them with any file manager — no unpacking, no key. The
browsable cousin of a sealed **package**.

### Mount / mounted
When a drive or tape **shows up on your computer** as a drive letter (like `E:\`
on Windows) or a folder you can open. "Mount the tape" means "get the tape to
appear as a drive you can write to."

### Offsite / Onsite
Whether a volume lives **somewhere else physically** (offsite — a friend's house,
a bank box) or in the same place as your computer (onsite). The "1" in 3-2-1. You
set it when registering a volume or with "Mark offsite" on its detail page.

### Package
One media-sized, **sealed unit** built from an archive's files. On the media it is
a folder holding: `NAME.tar` (or `NAME.tar.gpg` if encrypted), a `NAME.par2`
recovery set, a `NAME.manifest.json` file list, and a `RESTORE.txt`. The unit the
app writes, verifies, and restores.

### par2
A free, standard tool that creates **repair data**. If a limited amount of a
package gets damaged (a disc scratch, a few bad spots on a drive), par2 can rebuild
the missing pieces. The "% par2" you choose is how much repair data to add
(default 10%).

### Profile (protection profile)
A named rule for "how protected should this be": **Single Copy**, **3-2-1
Standard** (the default), or **Pre-Deletion Hold**. You assign one to an archive
(and, if you like, to specific folders) on the **Protection** tab. See
[the 3-2-1 guide](04-the-3-2-1-setup.md).

### Read-back verify
Right after writing a copy, the app **reads it back off the medium** and checks the
fingerprint matches. Only then is the copy trusted. This is always on.

### Recovery Kit
A folder you export (from the **Keys** tab) that contains a media inventory, a
restore runbook, and QR-code cards for your encryption keys — everything a person
would need to restore your files years from now, even without this app. See
[the Recovery Kit guide](09-the-recovery-kit.md).

### RESTORE.txt
A plain-text instruction sheet written onto the media with every package,
explaining how to get the files back by hand using par2, gpg, and tar. Proof that
you are never locked in.

### Spanning
When a package is **too big for one tape or disc**, the app splits it across
several, one at a time, each independently verified. See
[the tape guide](05-tape.md).

### Staging folder
A big, fast **scratch work folder** where a package is built before it's written
to media. It is reused for each package, so it only needs room for the **biggest
single package**, not your whole archive. You set it on the **Settings** tab.

### Status (the six statuses)
Each file gets exactly one, always shown as **colour + icon + words** together:
**UNASSIGNED** (gray), **NOT_BACKED_UP** (red), **PARTIAL** (amber),
**COMPLETE** (green), **OVER_COMPLETE** (blue), **OUT_OF_POLICY** (purple).
Only a fully verified copy counts toward reaching COMPLETE.

### tar
A free, standard tool that bundles many files into one `.tar` file (and unbundles
it). The third of the three restore tools.

### Verify levels (A / B / C)
How thoroughly a re-check reads a copy: **A (Census)** — the file exists and is the
right size (seconds); **B (Full)** — the complete fingerprint is re-checked (the
only level that counts as truly verified); **C (Sample)** — just the beginning and
end are checked (fast). Default and recommended is **B**.

### Volume
A **physical thing you can hold** and store a copy on: an external hard drive, an
LTO tape, or a disc. It has a label, a kind (HDD/SSD/TAPE/OPTICAL/OTHER), a
free-text location, and an Onsite/Offsite flag. Managed on the **Volumes** tab.
