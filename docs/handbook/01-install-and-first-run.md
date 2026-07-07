# Install it and take the first run

Mnemosyne is a **single program** — one file. There is nothing to install in the
usual sense, no setup wizard, and no account to create. You download it, run it,
and it opens a page in your web browser.

This guide gets you from "nothing" to "the app is open and ready."

## 1. Download the right file for your computer

1. Go to the project's **Releases** page (the download page on its code site).
2. Download the file that matches your computer:
   - **Windows** — the file ending in `.exe` (for example `mnemosyne.exe`).
   - **Mac** — the file with `macos` in its name.
   - **Linux** — the file with `linux` in its name.

You should now have one downloaded file. Put it somewhere you can find it, like a
new folder called `Mnemosyne` in your Documents.

![The Releases download page with the three files](../img/01-download.png)

## 2. Run it

Running it starts a small **local web server** — a program on your own computer
that serves a web page only to you. Nothing is exposed to the internet.

- **Windows:** double-click `mnemosyne.exe`. If Windows shows a blue "Windows
  protected your PC" box (because the file is new and unsigned), click **More
  info → Run anyway**. You should now see a small black window with a line like:
  `Mnemosyne 2.x — http://127.0.0.1:7821`. Leave that window open — it *is* the
  app. Closing it stops the app.
- **Mac / Linux:** it is usually easiest to run it from the **Terminal** (the text
  window where you type commands — on Mac, open the app called "Terminal"). Type
  the path to the file and press Enter. You should see the same
  `http://127.0.0.1:7821` line. (On Mac you may first need to allow it in **System
  Settings → Privacy & Security**.)

That web address — `http://127.0.0.1:7821` — always means "this same computer."
`127.0.0.1` is the standard address a computer uses to talk to itself.

## 3. Open the page

1. Open your web browser (Chrome, Edge, Firefox, or Safari).
2. In the address bar, type `http://127.0.0.1:7821` and press Enter.

You should now see the Mnemosyne page: a column of tabs on the left (Home, Vault,
Protection, and more) and, because nothing is set up yet, a **"Getting started"**
checklist in the middle.

![The Getting started checklist on first run](../img/01-getting-started.png)

## 4. Install the three helper tools

Mnemosyne leans on three small, free, standard tools to do its work. They are the
same tools that make your backups restorable by hand later, so they matter:

- **tar** — bundles many files into one file.
- **gpg** — encrypts (scrambles) packages, if you choose encryption.
- **par2** — creates repair data that can fix limited damage.

Look at the small **status lamp** at the bottom of the left-hand tabs. If it says
**"setup needed — see Settings,"** one or more tools are missing. Click the
**Settings** tab. At the top you'll see each tool with **found** or **missing**,
and a short hint for how to install the missing ones on your system:

- **Windows:** `tar` already comes with Windows 10 and newer. Install **gpg** with
  Gpg4win (from gpg4win.org). Install **par2** with a package manager like
  Chocolatey (`choco install par2cmdline`).
- **Mac:** use Homebrew — `brew install gnupg par2` (tar is already there).
- **Linux:** use your package manager — for example `apt install gnupg par2`.

After installing a tool, return to Settings. You should now see it change to
**found**. When all three say **found**, the status lamp turns to
**"tar · gpg · par2 · keys ready."**

![Settings showing the three tools as found](../img/01-tools-found.png)

## 5. Follow the Getting Started checklist

Go back to the **Home** tab. The **Getting started** checklist has seven steps and
each one detects its own state and turns green when done:

1. Install the three tools *(you just did this)*.
2. Set a staging folder *(a scratch work folder — see [Set up safely](02-set-up-safely.md))*.
3. Register 2 keystores *(only needed if you want encryption)*.
4. Create your first archive.
5. Scan a folder into it.
6. Plan and build a package.
7. Write a verified copy.

Each step has a button that jumps you to the exact spot. You can do them in order.
The rest of this handbook walks through them.

## How to know it worked

- The black window (or Terminal) shows a line with `http://127.0.0.1:7821`.
- The browser page loads and shows the tabs on the left.
- After installing the tools, the status lamp reads **"tar · gpg · par2 · keys
  ready."**

## If something went wrong

- **The browser says it can't connect.** The app isn't running, or you typed the
  address wrong. Make sure the black window/Terminal is still open and shows the
  address, then re-type `http://127.0.0.1:7821` exactly.
- **The page loads but the lamp says "setup needed."** One of tar/gpg/par2 isn't
  installed yet. Open Settings and follow the install hint for whichever shows
  **missing**.
- **The very first time you open Settings it feels slow, then is fine.** That's
  normal — the app is checking your tools for the first time. It is fast
  afterward.
- **Windows blocked the file.** Click **More info → Run anyway** on the blue box.
  The file is safe; it is just new and unsigned.
- Still stuck? See **[troubleshooting](troubleshooting.md)**.

## Screenshots to capture

- `../img/01-download.png` — the Releases page with the Windows/Mac/Linux files.
- `../img/01-getting-started.png` — the Getting started checklist on first run.
- `../img/01-tools-found.png` — Settings with tar/gpg/par2 all showing "found."
