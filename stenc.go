package main

// stenc.go — drive-level (hardware) tape AES, handled with appropriate fear.
//
// Some operators enable an LTO drive's built-in AES encryption with `stenc`
// (SCSI SPIN/SPOUT key management). Mnemosyne neither sets nor manages it — but it
// MUST be aware of it, because a drive-encrypted tape is a different, far more
// dangerous animal than a Mnemosyne-encrypted package:
//
//   - Mnemosyne encryption (gpg) is IN the restore story: the ciphertext is a
//     `.tar.gpg` file, and anyone with the passphrase and `gpg` reads it. The QR
//     card, the paper key sheet, and the keystore all carry that passphrase.
//
//   - Drive-level AES is OUTSIDE the restore story entirely. The bytes recorded on
//     the tape are hardware ciphertext. Without the *drive's* key, loaded into a
//     compatible drive, NOTHING reads them — not gpg, not tar, not par2, not
//     another drive of the same model without the key. par2 can't even see the
//     data to repair it. A lost drive key = the tape is scrap.
//
// So the rule is: record it, and shout about it everywhere a human might pick up
// the tape — inventories, the finalize sidecar, and the Recovery Kit — with the
// one instruction that actually matters: preserve the drive key separately, or the
// tape is unrecoverable.

const driveEncWarning = "⚠ DRIVE-LEVEL AES (e.g. stenc/LTO hardware encryption) — this medium's bytes are " +
	"hardware ciphertext that ONLY a compatible drive holding the drive key can read. It is OUTSIDE the " +
	"par2→gpg→tar restore story: gpg cannot help, par2 cannot repair what it cannot read. If the drive key " +
	"is lost, this medium is UNRECOVERABLE by any tool. Preserve the drive key separately from Mnemosyne's keystores."

// driveEncShort is the compact inventory-cell flag.
const driveEncShort = "⚠ DRIVE-ENCRYPTED (stenc/LTO hardware; drive key required — outside gpg)"

// anyDriveEncrypted reports whether any volume in the map is drive-encrypted — the
// trigger for the loud Recovery-Kit banner.
func anyDriveEncrypted(volm map[int]*Volume) bool {
	for _, v := range volm {
		if v != nil && v.DriveEncrypted {
			return true
		}
	}
	return false
}
