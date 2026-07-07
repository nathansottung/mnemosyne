# The 3-2-1 setup (make it bulletproof)

One backup is good. But a single copy on a single drive can still be lost — drives die, houses flood, things get stolen. The "3-2-1 rule" is the trusted way archivists and photographers make backups truly safe. This guide explains it in plain language and shows you how to reach it in Mnemosyne.

As always: Mnemosyne only reads your originals, never changes or deletes them, and never sends anything over the internet.

Before you start, you should already have done "Your first backup" — that is, you have an Archive that is scanned, and at least one package written and **VERIFIED** on one drive.

---

## What 3-2-1 means (and why each part matters)

**3-2-1** stands for:

- **3 copies** of your data.
  Why: if one copy fails, you still have two. One backup plus your working files is a start, but real safety means having spare copies made on purpose.

- **2 different kinds of media.**
  "Kinds" means different types of storage — for example a hard drive (HDD) and an LTO tape, or a hard drive and Blu-ray discs. Why: a whole technology can fail, or a single bad manufacturing batch can spoil several drives at once. Two different kinds means one weakness cannot wipe out everything.

- **1 copy kept offsite.**
  "Offsite" means physically somewhere else — a relative's house, a safe deposit box, an office. Why: a fire, flood, or burglary at your home could destroy every copy in the building at once. A copy stored elsewhere survives that.

Mnemosyne has a built-in profile for exactly this, called **3-2-1 Standard**. When you assign it to an archive, the app tracks all three parts for you and tells you clearly when you have met the goal.

Important promise: **only a fully verified copy counts** toward the goal. A half-written or unverified copy will not fool the app into saying you are safe.

---

## Step 1: Assign the 3-2-1 Standard profile

A "Profile" is a set of safety rules you attach to an archive. The profiles available are **Single Copy**, **3-2-1 Standard** (the default), and **Pre-Deletion Hold**. We want 3-2-1 Standard.

Good news: **new archives already start on 3-2-1 Standard**, so you may not need to
change anything. Here is how to check or set it.

1. Click the **Protection** tab on the left.
   You should see your archives listed, each with a status breakdown.

2. Find your archive (for example `Family Photos`) and click **Manage folders & assignment…**.
   You should now see that archive's protection page, with an **Archive-level profile** panel near the top.

3. In the **Archive-level profile** panel, click **Assign profile…**, choose **3-2-1 Standard**, and save.
   The panel should now show the archive is using **3-2-1 Standard**.

![The Protection tab assigning the 3-2-1 Standard profile](../img/04-assign-profile.png)

You can also reach this from the **Vault** tab using the **Protection…** button on the archive's row.

---

## Step 2: Read the status colours and icons

Once a profile is assigned, every file shows exactly one status. Each status is **always shown three ways at once — a colour, an icon, and words** — so it is easy to read even if colours are hard for you to tell apart. The six statuses are:

- **UNASSIGNED** — gray ○ — no profile or no plan yet.
- **NOT_BACKED_UP** — red ✕ — no verified copy exists. Not safe.
- **PARTIAL** — amber ◐ — some of the goal is met, but not all.
- **COMPLETE** — green ✓ — the full goal is met. This is what you want.
- **OVER_COMPLETE** — blue ✓+ — more copies than the goal asks for. Extra safe.
- **OUT_OF_POLICY** — purple ⚠ — something breaks the rules and needs attention.

After your first backup, your archive will likely show **PARTIAL (amber ◐)**, because you have one copy but not yet three, two kinds, and one offsite.

An example PARTIAL message reads: **"2/3 copies · kinds ok · 0/1 offsite"**. Here is how to read that:

- **2/3 copies** — you have 2 verified copies out of the 3 you need. One more to go.
- **kinds ok** — you already have enough different **kinds** of media (2 kinds).
- **0/1 offsite** — you have 0 of the 1 required offsite copies. You still need to send one copy somewhere else.

![A file showing PARTIAL status with the 2/3 copies message](../img/04-partial-status.png)

The goal is to turn every part of that message green: 3/3 copies, kinds ok, and 1/1 offsite. When all parts are met, the status becomes **COMPLETE (green ✓)**.

---

## Step 3: Make a second copy on a second drive

More copies come from writing the same package to more volumes.

1. Plug in a **second** drive and make sure it is mounted (showing a drive letter like `F:\`).

2. Go to the **Packages** tab and click **Write to volume…** on your VERIFIED package.
   The write dialog opens.

3. Click **📁 Browse…** and choose the second drive.
   The path appears in the box.

4. **Register the second drive as a new Volume.** Give it a **label** (like `Backup Drive B`), a **kind**, a **location**, and an **Onsite/Offsite** choice.
   - To also satisfy the **2 kinds** rule, make this a **different kind** of media if you can — for example, if Drive A was an HDD, use an SSD, tape, or discs here. (If your plan targets discs or tape, that naturally becomes a second kind.)

5. Start the write and watch the **Jobs** tab until the package reaches **VERIFIED**.
   Your **copies** count should now go up (for example, from 1/3 to 2/3).

![Writing the package to a second volume of a different kind](../img/04-second-copy.png)

Repeat for a third copy so you reach **3/3 copies**.

---

## Step 4: Mark one copy Offsite

The offsite part is met by having a verified copy on a volume that is marked **Offsite**. You can set this when you register the volume, or change it later.

To mark an existing volume offsite:

1. Click the **Volumes** tab.
   You should see all your volumes listed with their labels and locations.

2. Click the volume you plan to store elsewhere to open its **detail page**.
   You should see that volume's details.

3. Click **Mark offsite**.
   The volume should now show as **Offsite**.

![A volume's detail page with the Mark offsite button](../img/04-mark-offsite.png)

4. Now physically take that drive (or tape, or discs) to its offsite home — a relative's house, a safe deposit box, or your workplace.

Once a **verified** copy lives on an offsite-marked volume, the offsite part of your goal reads **1/1 offsite**.

Tip: update the volume's **location** text to match where it now lives (for example `Sister's house`), so you always know where each copy is.

---

## How to know it worked

- The archive uses the **3-2-1 Standard** profile (shown on the Protection tab).
- Your files show **COMPLETE (green ✓)**, and the status message reads all-met: **3/3 copies · kinds ok · 1/1 offsite**.
- On the **Volumes** tab you can see three volumes, at least two of different **kinds**, and one marked **Offsite**.
- Every copy that counts reached **VERIFIED** — remember, only verified copies count toward the goal.

## If something went wrong

- **Status still shows PARTIAL (amber ◐).** Read the message. It tells you exactly what is missing — for example `2/3 copies` means make one more copy; `0/1 offsite` means mark a copy's volume Offsite (and physically move it).
- **Status shows OUT_OF_POLICY (purple ⚠).** Something breaks the rules — for example, a required copy's volume can no longer be verified, or two copies are on the same kind of media when two kinds are required. Open the archive on the Protection tab to see the detail, then add or fix a copy.
- **A new copy did not raise the count.** Check the **Jobs** tab — the write may still be running or may have failed verify. Only a **VERIFIED** copy counts. Re-run **Write to volume…** if needed.
- **"kinds ok" never appears.** Both your copies may be the same kind of media (for example two HDDs). Make one copy on a genuinely different kind — SSD, tape, or optical discs.
- **You cannot mark a volume Offsite.** Make sure you opened the volume's **detail page** first, then look for **Mark offsite**. You can also set Onsite/Offsite when registering a volume during a write.
- Need a reminder on any screen? Click the small **ⓘ** help button.

## Screenshots to capture

- `../img/04-assign-profile.png` — The Protection tab assigning the 3-2-1 Standard profile.
- `../img/04-partial-status.png` — A file showing PARTIAL status with the "2/3 copies · kinds ok · 0/1 offsite" message.
- `../img/04-second-copy.png` — Writing the package to a second volume of a different kind.
- `../img/04-mark-offsite.png` — A volume's detail page with the Mark offsite button.
