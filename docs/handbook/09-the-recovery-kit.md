# The Recovery Kit (your break-glass box)

Think of the Recovery Kit as the sealed "break glass in emergency" box for your whole archive. It is a small folder you create once, print or copy, and tuck away somewhere safe. If your computer, your app, and even Mnemosyne itself all vanish, the Recovery Kit — together with your media — is everything a careful person needs to get your files back.

A few words first:

- A **"passphrase"** is the long secret password that unlocks an encrypted (scrambled) package.
- A **"QR code"** is that square, phone-scannable barcode. Mnemosyne puts one QR code per encryption key into the kit.
- A **"keystore"** is the small file that normally holds your passphrases. The Recovery Kit is a **separate** paper-and-file backup of those secrets, meant for the day your keystores are gone.

Big reassurance: making a Recovery Kit only **reads** from your archive and **writes** the kit into a folder you choose. It never touches your originals, never deletes anything, and never sends anything over the internet.

---

## Part 1: Create the kit

1. Plug in any keystores that hold your encryption keys (if you use encryption), so the kit can include their QR cards.
   You should now see them recognized on the **Keys** tab.

2. Click the **Keys** tab on the left.
   You should now see your keys and, near the top or bottom, an **Export Recovery Kit…** button.
   ![The Keys tab with the Export Recovery Kit button](../img/09-keys-export.png)

3. Click **Export Recovery Kit…**.
   A window opens asking where to save the kit.

4. Click **📁 Browse…** and choose an output folder — ideally on a **removable drive** like a USB stick, not inside any of your scanned source folders.
   You should now see your chosen folder in the box.

5. Start the export.
   You should now see a job begin — check the **Jobs** tab for progress.

6. When it finishes, open your chosen folder.
   You should now see a new folder named **`mnemosyne-recovery-kit`** inside it.

---

## Part 2: What's inside

Open the `mnemosyne-recovery-kit` folder. You should now see:

- **`MEDIA_INVENTORY.md`** — a plain list of every package and which volume (drive, disc, or tape) holds it. This is your map: "the 2019 photos are on the blue drive."
- **`README_RECOVERY.md`** — a short "start here" note explaining what the kit is and what to do first.
- **`RESTORE_RUNBOOK.md`** — the authoritative, long-form restore instructions, written to still make sense **30 years** from now. This is the deep, careful version of the by-hand method (par2 → gpg → tar).
- **A `keys/` folder** — inside it, for **each** encryption key, one **QR-code image** (a `.png` file) plus a matching **text card** (a `.txt` file). The text card lists the key's name; the QR image encodes the actual passphrase.

![The contents of the recovery kit folder](../img/09-kit-contents.png)

If you do not use encryption, the `keys/` folder will be empty or absent — that is normal, and nothing about your restore needs a passphrase.

---

## Part 3: The passphrase warning — read this carefully

**The QR cards encode your real passphrases, in the clear.** Anyone who scans a QR card gets the actual secret that unlocks your encrypted data. That is exactly what makes the kit powerful — and exactly why you must guard it.

Treat the Recovery Kit **as securely as the keystores themselves**:

- Keep it in a **fireproof box**, a **bank safe-deposit box**, or another genuinely secure place.
- Do **not** email it, upload it to a cloud drive, or leave it on a shared computer.
- If you print it, store the paper somewhere locked, not pinned to a corkboard.

Nothing in the app forces this on you — it is your responsibility. The reward is enormous: a secret kept safe here means your data survives even total loss of your computers and keystores.

---

## Part 4: Where to physically keep it

The whole point is that the kit survives disasters your computer will not. So:

1. **Print** the kit (at least `README_RECOVERY.md`, `RESTORE_RUNBOOK.md`, `MEDIA_INVENTORY.md`, and the QR cards) and keep the paper in a fireproof box or safe-deposit box.
2. **Also copy** the `mnemosyne-recovery-kit` folder onto a **couple of USB sticks**.
3. Keep those copies in **separate physical locations** — for example, one at home and one at a trusted family member's house or a safe-deposit box. A fire or flood in one place should never take out every copy.

You should now have the kit in at least two places that would not be destroyed by the same accident.

---

## Part 5: "Hand this to a stranger in 2040"

Here is the idea that should let you sleep at night. Imagine it is the year 2040. Mnemosyne the app no longer exists. You are not around to help. Someone technical — a grown child, an archivist, a helpful IT person — is handed **your backup media and this Recovery Kit**.

Can they get the files back? **Yes.** Because:

- The **`RESTORE_RUNBOOK.md`** in the kit explains the whole process in plain steps.
- Those steps use only **three free tools** that have existed for decades and run on Windows, Mac, and Linux: **`par2`** (checks and repairs damage), **`gpg`** (decrypts, if the data was encrypted), and **`tar`** (unpacks the files).
- For any **encrypted** data, the **QR card** in the `keys/` folder hands over the passphrase `gpg` will ask for.

No special software, no account, no company that has to still be in business. Just the media, the kit, and three tools anyone can download. That is what "future-proof" really means, and it is why the Recovery Kit is the most important thing you will ever make with Mnemosyne.

---

## How to know it worked

- Your chosen output folder now contains a **`mnemosyne-recovery-kit`** folder.
- Inside it you can see `MEDIA_INVENTORY.md`, `README_RECOVERY.md`, `RESTORE_RUNBOOK.md`, and a `keys/` folder.
- If you use encryption, the `keys/` folder holds one QR `.png` and one `.txt` card per key.
- You have decided where the printed copy and the USB copies will physically live, in at least two separate places.

## If something went wrong

- **The export finished but a key's QR card is missing.** The kit only makes a QR card for a key whose passphrase it can actually reach. Plug in the keystore that holds that key, confirm it on the **Keys** tab, and export again.
- **"Refusing: <path> is inside a source root."** You pointed the kit's output at a folder inside one of your scanned source folders. The app never writes into your originals — choose a different folder, such as a USB stick.
- **You cannot find the Export Recovery Kit button.** It lives on the **Keys** tab. If the tab looks empty, make sure the app finished loading and refresh the page (press F5).
- **You are unsure whether to encrypt at all.** If you never encrypted your packages, you do not need the QR cards, and restoring needs no passphrase — the kit is still worth keeping for its inventory and runbook.

## Screenshots to capture

- `../img/09-keys-export.png` — The Keys tab with the Export Recovery Kit button.
- `../img/09-kit-contents.png` — The `mnemosyne-recovery-kit` folder open, showing its files and the `keys/` folder.
- `../img/09-qr-card.png` — One QR card image and its matching text card.
