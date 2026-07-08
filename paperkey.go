package main

// paperkey.go — a typable key sheet: the SAME secret as the QR card, but as
// characters a human can read and retype, with a per-line checksum so they can
// prove they typed it right.
//
// The QR card is fast but needs a scanner (a "decoder"). Thirty years out, the
// surest input device is still a keyboard and a pair of eyes. So beside every QR
// we print the passphrase itself, split into short groups, each LINE carrying a
// CRC-16 check value over that line's characters. A curator retypes the sheet;
// Mnemosyne checks each line's CRC and catches the one transposed character before
// it silently corrupts the key. The tool-free FINAL proof is the whole-passphrase
// fingerprint (SHA-256), the same one printed on the key card — retype the lot,
// hash it with `sha256sum`, and if it equals the fingerprint you typed it right.
//
// CRC-16 per line (fine-grained typo/transposition catch, checked by the app) plus
// SHA-256 over the whole passphrase (the ubiquitous, hand-verifiable proof): no
// decoder, no special format to still have in 2056, and the payload IS the
// passphrase. SHA-256 is the one hash allowed on media (see hashing.go).
//
// These are symmetric AES-256 passphrases, not GPG keypairs, so the `paperkey`
// tool (which extracts a GPG secret key's secret bytes) does not apply — the
// printable backup here is the passphrase itself, checksummed for safe retyping.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strings"
)

const (
	keySheetGroup       = 4 // characters per group
	keySheetGroupsPerLn = 5 // groups per line → 20 payload chars/line
)

// crc16 computes CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF, no reflection,
// xorout 0x0000). It is spelled out in full so a curator in 2056 can reimplement
// the line check from this function alone. It catches every single-bit error, all
// odd numbers of bit errors, and the adjacent-character transpositions that a
// truncated hash would miss — exactly the mistakes made when retyping by hand.
func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, bb := range data {
		crc ^= uint16(bb) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// lineCode is the per-line check value: the CRC-16 of the line's payload
// characters, as four uppercase hex digits.
func lineCode(payload string) string { return fmt.Sprintf("%04X", crc16([]byte(payload))) }

// passFingerprint is the whole-passphrase proof (full SHA-256 hex) — the same
// fingerprint printed on the key card, and the tool-free final check.
func passFingerprint(pass string) string {
	h := sha256.Sum256([]byte(pass))
	return hex.EncodeToString(h[:])
}

// keyLine is one indexed, grouped, checksummed line of a passphrase — the shared
// model behind both the text sheet and the printable HTML page.
type keyLine struct {
	No     int    // 1-based line index
	Groups string // space-separated groups of keySheetGroup chars, e.g. "Q7xM 2fKp …"
	Code   string // CRC-16 hex over Chars
	Chars  string // the concatenated payload chars (no spaces)
}

// keyLines splits a passphrase into keySheetGroupsPerLn groups of keySheetGroup
// characters per line, each with its CRC-16 check value.
func keyLines(pass string) []keyLine {
	var lines []keyLine
	no := 0
	for i := 0; i < len(pass); i += keySheetGroup * keySheetGroupsPerLn {
		no++
		end := i + keySheetGroup*keySheetGroupsPerLn
		if end > len(pass) {
			end = len(pass)
		}
		chars := pass[i:end]
		var groups []string
		for g := 0; g < len(chars); g += keySheetGroup {
			ge := g + keySheetGroup
			if ge > len(chars) {
				ge = len(chars)
			}
			groups = append(groups, chars[g:ge])
		}
		lines = append(lines, keyLine{No: no, Groups: strings.Join(groups, " "), Code: lineCode(chars), Chars: chars})
	}
	return lines
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
	b.WriteString("  • The [XXXX] code after each line is that line's CRC-16 (CCITT) check —\n")
	b.WriteString("    Mnemosyne ▸ Keys ▸ \"Enter key from sheet\" checks every line and pinpoints\n")
	b.WriteString("    the exact line to fix if a single character is wrong.\n")
	b.WriteString("  • Tool-free FINAL proof: the SHA-256 of the whole passphrase equals the\n")
	b.WriteString("    fingerprint above. Retype it all, then check by hand with:\n")
	b.WriteString("        printf %s \"THEWHOLEPASSPHRASE\" | sha256sum\n\n")
	b.WriteString("PAYLOAD:\n")

	for _, ln := range keyLines(pass) {
		fmt.Fprintf(&b, "  L%02d  %-*s  [%s]\n", ln.No,
			keySheetGroupsPerLn*(keySheetGroup+1), ln.Groups, ln.Code)
	}
	b.WriteString("\nEND — reconstruct the passphrase = every group above, concatenated, no spaces.\n")
	return b.String()
}

var keySheetLineRe = regexp.MustCompile(`(?i)^\s*L\d+\s+(.*?)\s*\[([0-9a-f]{4})\]\s*$`)

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

// ---- printable one-key-per-page layout ----------------------------------
//
// keyPageHTML + keyPagesDocument render the Recovery Kit's printable key pages:
// one key per page, each carrying the SAME secret twice — the QR (scan) and the
// typable, CRC-checked character grid (retype) — under the standing instruction
// that the page IS the key. Open KEYS.html in a browser and print; the stylesheet
// forces a page break per key.

// keyPageHTML renders one printable key page. qrDataURI is a self-contained
// "data:image/png;base64,…" so the printout needs no external files.
func keyPageHTML(ref, note, fingerprint, pass, qrDataURI string) string {
	var b strings.Builder
	b.WriteString(`<section class="keypage">`)
	b.WriteString(`<h1>Mnemosyne key — <span class="mono">` + html.EscapeString(ref) + `</span></h1>`)
	b.WriteString(`<p class="standing">Store this page as securely as the keystores — <strong>this page IS the key.</strong> The QR and the characters below are the <em>same secret</em>; either one decrypts every package sealed with <span class="mono">` + html.EscapeString(ref) + `</span>.</p>`)
	b.WriteString(`<div class="top">`)
	if qrDataURI != "" {
		b.WriteString(`<img class="qr" alt="QR for ` + html.EscapeString(ref) + `" src="` + qrDataURI + `">`)
	}
	b.WriteString(`<table class="meta"><tbody>`)
	b.WriteString(`<tr><td>key_ref</td><td class="mono">` + html.EscapeString(ref) + `</td></tr>`)
	if note != "" {
		b.WriteString(`<tr><td>note</td><td>` + html.EscapeString(note) + `</td></tr>`)
	}
	b.WriteString(fmt.Sprintf(`<tr><td>length</td><td>%d characters</td></tr>`, len(pass)))
	b.WriteString(`<tr><td>fingerprint</td><td class="mono fp">` + html.EscapeString(fingerprint) + `</td></tr>`)
	b.WriteString(`</tbody></table></div>`)
	b.WriteString(`<p class="howto">Retype every group below left-to-right, top-to-bottom, <strong>no spaces</strong> — that string is the passphrase. The <code>CRC-16</code> after each line lets <em>Keys ▸ Enter key from sheet</em> pinpoint a mistyped line; the tool-free final proof is that the whole passphrase's SHA-256 equals the <em>fingerprint</em> above.</p>`)
	b.WriteString(`<table class="payload"><thead><tr><th>#</th><th>characters (groups of 4)</th><th>CRC-16</th></tr></thead><tbody>`)
	for _, ln := range keyLines(pass) {
		b.WriteString(fmt.Sprintf(`<tr><td class="ln">L%02d</td><td class="mono chars">%s</td><td class="mono crc">%s</td></tr>`,
			ln.No, html.EscapeString(ln.Groups), ln.Code))
	}
	b.WriteString(`</tbody></table>`)
	b.WriteString(`<p class="warn">⚠ SECRET IN THE CLEAR — anyone who reads this page can decrypt the packages. Locked, off-site, access-controlled, exactly like a keystore.</p>`)
	b.WriteString(`</section>`)
	return b.String()
}

// keyPagesDocument wraps the accumulated per-key sections in a self-contained,
// printable HTML document (one key per printed page).
func keyPagesDocument(sections string) string {
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>Mnemosyne key pages — retypable backups</title>
<style>
  :root{ --ink:#111; --line:#bbb; --muted:#555; }
  body{ font:14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif; color:var(--ink); margin:0; padding:24px; }
  .intro,.keypage{ max-width:52em; margin:0 auto; }
  .keypage{ padding:24px 0; border-top:2px solid var(--ink); }
  h1{ font-size:20px; margin:0 0 8px; }
  .standing{ font-weight:600; }
  .warn{ font-weight:700; }
  .howto{ color:var(--muted); font-size:13px; }
  .top{ display:flex; gap:20px; align-items:flex-start; flex-wrap:wrap; }
  .qr{ width:220px; height:220px; image-rendering:pixelated; border:1px solid var(--line); }
  .mono{ font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; }
  .meta td{ padding:2px 12px 2px 0; vertical-align:top; }
  .fp{ word-break:break-all; }
  table.payload{ width:100%; border-collapse:collapse; margin:12px 0; }
  .payload th,.payload td{ border:1px solid var(--line); padding:4px 10px; text-align:left; }
  .payload .chars{ letter-spacing:.14em; font-size:15px; }
  .payload .ln,.payload .crc{ text-align:center; white-space:nowrap; }
  @media print{
    body{ padding:0; }
    .intro{ page-break-after:always; }
    .keypage{ page-break-after:always; border-top:none; padding:0; min-height:100vh; }
  }
</style></head><body>
<div class="intro">
  <h1>Mnemosyne key pages</h1>
  <p>One page per key. Each page carries the passphrase <strong>twice as the same secret</strong>: a QR code (scan it) and the typable characters below it (retype them if no scanner survives). <strong>Print this, then store it as securely as your keystores — these pages ARE the keys.</strong></p>
  <p class="howto">These are symmetric AES-256 passphrases, not GPG keypairs, so the <code>paperkey</code> tool does not apply — the printable backup here is the passphrase itself, checksummed for safe retyping.</p>
</div>
` + sections + `
</body></html>
`
}
