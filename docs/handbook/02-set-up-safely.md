# Set up safely

This guide gets Mnemosyne ready before you make your first backup. Take your time. None of these steps touch your original photos or files. Mnemosyne only ever reads your files, never changes or deletes them, and never sends anything over the internet.

Before you start, make sure Mnemosyne is running and open in your web browser at http://127.0.0.1:7821. On the left you will see tabs: Home, Vault, Protection, Integrity, Packages, Burn, Volumes, Dock, Keys, Jobs, and Settings. At the bottom-left there is a small status "lamp" (a little indicator that tells you if the app is ready).

---

## Part 1: Choose a staging folder

A "staging folder" (a big scratch work folder) is a temporary workspace on your computer. When Mnemosyne builds a "package" (one media-sized sealed unit of your files), it does the work in the staging folder first, then writes the finished result to your drive.

Two good-to-know facts:

- The staging folder should be **big and fast**. Fast means an internal drive (like your computer's main disk) or a fast external SSD, not a slow USB stick. This makes building quicker.
- The staging folder is **reused for each package, one at a time**. So it only needs enough room for your **single biggest package**, not your whole archive. For example, if your biggest package is sized for a 100 GB disc, you need a little more than 100 GB free, not room for everything you own.

Here is how to set it:

1. Click the **Settings** tab on the left.
   You should now see the settings page with boxes for folder paths.

2. Under **Pipeline**, find the box labeled **"Staging folder (big + fast; the NAS itself is ideal)."** Click the **Browse…** button next to it.
   A folder picker window opens.

3. Pick a folder on a big, fast drive with plenty of free space. If you are not sure, make a new folder named something like `Mnemosyne-Staging` on your main drive and choose that.
   The folder's path now appears in the box.

4. Click **Save settings** at the bottom of that section.
   You should now see a "Settings saved" message, and the staging folder path stays filled in.

![The Settings page with the staging folder path filled in](../img/02-staging-folder.png)

That's it for staging. If you never plan to encrypt (scramble) your packages, you can skip the rest of this guide and move on to your first backup.

---

## Part 2: Decide about encryption (optional)

"Encryption" means scrambling your packages so only someone with the secret passphrase (a long password) can read them. This is useful if a drive might be lost or stolen. It is completely **optional**. If you want to keep things simple, you can skip encryption, and you will not need any of the keystore steps below.

If you do want encryption, read on carefully. There is one rule that protects you from a heartbreaking mistake.

### The two-keystore rule, in plain terms

A "keystore" (a small file that holds the secret passphrases for encrypted packages) is what unlocks your encrypted packages later. If you lose the only keystore, your encrypted data is **gone forever** — no one, including you, can recover it.

To protect you from that, Mnemosyne **refuses to build encrypted packages until you have at least TWO keystores registered**. Two keystores means two separate copies of the secret, so losing one is not a disaster. The app also recommends you keep them on **two different devices** — for example, one on your computer and one on a USB stick you store somewhere safe.

Important: Mnemosyne never stores the passphrase itself in its catalog. It only keeps a "fingerprint" (a short code that can confirm a passphrase is correct but cannot reveal it). So you are the only keeper of the real secret. That is exactly why two copies matter.

### Register two keystores

1. Click the **Settings** tab.
   You should see the settings page.

2. Find the box labeled **"Keystore paths — one per line, minimum 2, different physical devices."** Enter **two paths, one per line** — one for each keystore file, ideally on two different devices.
   (This box is a list: one file path on each line.)

3. Click **Save settings**.
   You should now see both keystore paths listed.

4. Look at the status **lamp** at the bottom-left. When your setup is complete it reads **"tar · gpg · par2 · keys ready"**. If it still says **"setup needed — see Settings"**, something is missing — recheck your paths.

5. Click the **Keys** tab to confirm both keystores are found.
   You should see both keystores listed, each with a green **reachable** tag. (If one shows **unreachable**, its file could not be found — recheck that path.)

![The Keys tab showing two keystores listed as reachable](../img/02-two-keystores.png)

---

## Part 3: Print QR key cards and store them safely (optional but wise)

A "QR code" is that square barcode you scan with a phone. Mnemosyne can print a QR card for each key, so you have a paper backup of the secret that unlocks your data.

1. On the **Keys** tab, in the **Key registry** list, find the key you want a card for and click **Print QR card**.
   A printable card opens in a new browser tab.

2. Print the card.
   You should now hold a paper card with a QR code on it.

3. **Store it like a key to a safe.** The QR card encodes a **real passphrase**. Anyone who has the card and a drive could unlock your data. Keep it somewhere protected — for example a **fireproof box**, and ideally keep a second card in a **different physical location** (like a trusted family member's house or a safe deposit box).

![A printed QR key card](../img/02-qr-card.png)

You can also export a **Recovery Kit** for safekeeping: on the **Keys** tab, click **Export Recovery Kit…**, choose a folder, and store the result somewhere safe alongside your cards. (The [Recovery Kit guide](09-the-recovery-kit.md) explains what's inside.)

---

## How to know it worked

- The **Settings** page shows your staging folder path saved.
- If you chose encryption: the **Settings** page shows **two** keystore paths, the **Keys** tab lists both as reachable, and the bottom-left **lamp** reads **"tar · gpg · par2 · keys ready"**.
- If you printed QR cards, you are holding at least one printed card and have decided where to store it.

## If something went wrong

- **Lamp still says "setup needed — see Settings".** Open Settings and recheck the paths you typed. A path pointing at a device that is unplugged (like a USB stick that is not connected) will not be found — plug it in and reload.
- **The app will not build an encrypted package and mentions keystores.** This is the two-keystore rule doing its job. Register a second keystore in Settings, ideally on a second device.
- **Browse… does not open a picker.** Make sure Mnemosyne is still running and refresh the page in your browser (press F5).
- **Not enough space when building later.** Your staging folder's drive may be too full. Free up space, or choose a staging folder on a drive with more room. Remember it only needs room for your single biggest package.
- Need a reminder on any screen? Click the small **ⓘ** help button on that screen.

## Screenshots to capture

- `../img/02-staging-folder.png` — The Settings page with the staging folder path filled in.
- `../img/02-two-keystores.png` — The Keys tab showing two keystores listed as reachable.
- `../img/02-qr-card.png` — A printed QR key card (or the print preview of one).
