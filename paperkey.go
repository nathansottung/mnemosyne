package main

// paperkey.go — a typable key sheet: the SAME secret as the QR card, but as
// characters a human can read and retype, with a per-line checksum so they can
// prove they typed it right.
//
// The QR card is fast but needs a scanner (a "decoder"). Thirty years out, the
// surest input device is still a keyboard and a pair of eyes. So beside every QR
// we print the passphrase itself, split into short groups, each LINE carrying a
// two-character code = the first two hex of the SHA-256 of that line's characters.
// A curator retypes the sheet; Mnemosyne (or, in a pinch, `sha256sum` by hand)
// checks each line's code and catches the one transposed character before it
// silently corrupts the key. The final proof is the whole-passphrase fingerprint
// (SHA-256), the same one printed on the key card.
//
// No decoder, no special tool, no format to still have in 2056: the payload IS
// the passphrase, and the checksum is plain SHA-256 — the one hash allowed on
// media (see hashing.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const (
	keySheetGroup       = 4 // characters per group
	keySheetGroupsPerLn = 5 // groups per line → 20 payload chars/line
)

// lineCode is the per-line checksum: the first two hex characters of the SHA-256
// of the line's payload characters. Verifiable anywhere with `sha256sum`.
func lineCode(payload string) string {
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:])[:2]
}

// passFingerprint is the whole-passphrase proof (full SHA-256 hex) — the same
// fingerprint printed on the key card.
func passFingerprint(pass string) string {
	h := sha256.Sum256([]byte(pass))
	return hex.EncodeToString(h[:])
}

// keySheet renders the typable sheet for one key. ref/note/fingerprint come from
// the key metadata; pass is the secret (identical to the QR payload's tail).
func keySheet(ref, note, fingerprint, pass string) string {
	var b strings.Builder
	b.WriteString("MNEMOSYNE KEY SHEET — TYPABLE BACKUP OF A PASSPHRASE\n")
	b.WriteString("===================================================\n\n")
	b.WriteString("key_ref:      " + ref + "\n")
	if note != "" {
		b.WriteString("note:         " + note + "\n")
	}
	b.WriteString("fingerprint:  " + fingerprint + "   (SHA-256 of the passphrase)\n")
	b.WriteString("length:       " + fmt.Sprintf("%d characters", len(pass)) + "\n\n")
	b.WriteString("⚠ SECRET: these characters ARE the passphrase in the clear — the same secret\n")
	b.WriteString("as the QR card. Store this sheet like a key: locked, off-site, access-controlled.\n\n")
	b.WriteString("HOW TO USE (no scanner, no special software):\n")
	b.WriteString("  • Retype every GROUP left-to-right, top-to-bottom, with NO spaces. That\n")
	b.WriteString("    exact string is the passphrase.\n")
	b.WriteString("  • The [xx] code after each line is the first two hex digits of the SHA-256\n")
	b.WriteString("    of that line's characters. To check a line by hand:\n")
	b.WriteString("        printf %s \"THELINECHARS\" | sha256sum        (first two digits = [xx])\n")
	b.WriteString("    Mnemosyne ▸ Keys ▸ \"Enter key from sheet\" checks every line for you.\n")
	b.WriteString("  • When the whole passphrase's SHA-256 equals the fingerprint above, you\n")
	b.WriteString("    have typed it correctly.\n\n")
	b.WriteString("PAYLOAD:\n")

	lineNo := 0
	for i := 0; i < len(pass); i += keySheetGroup * keySheetGroupsPerLn {
		lineNo++
		end := i + keySheetGroup*keySheetGroupsPerLn
		if end > len(pass) {
			end = len(pass)
		}
		lineChars := pass[i:end]
		// space-separated groups for readability
		var groups []string
		for g := 0; g < len(lineChars); g += keySheetGroup {
			ge := g + keySheetGroup
			if ge > len(lineChars) {
				ge = len(lineChars)
			}
			groups = append(groups, lineChars[g:ge])
		}
		fmt.Fprintf(&b, "  L%02d  %-*s  [%s]\n", lineNo,
			keySheetGroupsPerLn*(keySheetGroup+1), strings.Join(groups, " "), lineCode(lineChars))
	}
	b.WriteString("\nEND — reconstruct the passphrase = every group above, concatenated, no spaces.\n")
	return b.String()
}

var keySheetLineRe = regexp.MustCompile(`(?i)^\s*L\d+\s+(.*?)\s*\[([0-9a-f]{2})\]\s*$`)

// KeySheetResult reports a parsed/verified sheet without ever returning the
// secret itself.
type KeySheetResult struct {
	OK          bool     `json:"ok"`          // every line's code matched
	Lines       int      `json:"lines"`       // payload lines parsed
	BadLines    []int    `json:"bad_lines"`   // 1-based line numbers whose code failed
	Fingerprint string   `json:"fingerprint"` // SHA-256 of the reconstructed passphrase
	Length      int      `json:"length"`      // reconstructed passphrase length
	Notes       []string `json:"notes,omitempty"`
}

// parseKeySheet reconstructs the passphrase from a typed/retyped sheet and
// validates each line's checksum. It returns the passphrase (for the caller to
// use) plus a result summarizing which lines, if any, failed — so a retyped sheet
// pinpoints the exact line to fix.
func parseKeySheet(text string) (pass string, res KeySheetResult) {
	var sb strings.Builder
	n := 0
	for _, raw := range strings.Split(text, "\n") {
		m := keySheetLineRe.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		n++
		payload := strings.ReplaceAll(strings.ReplaceAll(m[1], " ", ""), "\t", "")
		sb.WriteString(payload)
		if !strings.EqualFold(lineCode(payload), m[2]) {
			res.BadLines = append(res.BadLines, n)
		}
	}
	res.Lines = n
	pass = sb.String()
	res.Length = len(pass)
	res.Fingerprint = passFingerprint(pass)
	res.OK = n > 0 && len(res.BadLines) == 0
	if n == 0 {
		res.Notes = append(res.Notes, "no payload lines found — paste the L01/L02/… PAYLOAD lines of the sheet")
	} else if len(res.BadLines) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d line(s) failed their checksum — a typo on: %v", len(res.BadLines), res.BadLines))
	}
	return pass, res
}
