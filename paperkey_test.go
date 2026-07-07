package main

import (
	"strings"
	"testing"
)

func TestKeySheetRoundTrip(t *testing.T) {
	// A realistic ~288-bit base64-ish passphrase.
	pass := "s3cr3t-Passphrase_ABCDEFGHIJKLMNOPQRSTUVWX0123456789+/abcdefghij"
	fp := passFingerprint(pass)
	sheet := keySheet("K-demo", "photos 2024", fp, pass)

	// The sheet carries the secret (like the QR) and the fingerprint.
	if !strings.Contains(sheet, fp) {
		t.Fatal("sheet should print the fingerprint")
	}
	// Retyping it back reconstructs the EXACT passphrase, all lines verified.
	got, res := parseKeySheet(sheet)
	if got != pass {
		t.Fatalf("reconstruct mismatch:\n got %q\nwant %q", got, pass)
	}
	if !res.OK || len(res.BadLines) != 0 {
		t.Fatalf("all lines should verify, got %+v", res)
	}
	if res.Fingerprint != fp {
		t.Fatalf("reconstructed fingerprint should match: got %s want %s", res.Fingerprint, fp)
	}
}

func TestKeySheetCatchesTypo(t *testing.T) {
	pass := "ABCDwxyz1234ABCDwxyz1234ABCDwxyz1234ABCDwxyz1234"
	sheet := keySheet("K-typo", "", passFingerprint(pass), pass)
	lines := strings.Split(sheet, "\n")
	// Corrupt a single character inside the FIRST payload line (find "  L01").
	changed := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "L01") {
			// flip one payload char (the group right after "L01  ")
			idx := strings.Index(ln, "L01") + 5
			b := []byte(ln)
			if b[idx] == 'A' {
				b[idx] = 'B'
			} else {
				b[idx] = 'A'
			}
			lines[i] = string(b)
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("could not locate L01 payload line to corrupt")
	}
	_, res := parseKeySheet(strings.Join(lines, "\n"))
	if res.OK {
		t.Fatal("a mistyped line must fail verification")
	}
	found := false
	for _, bl := range res.BadLines {
		if bl == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failing line (1) should be pinpointed, got bad_lines=%v", res.BadLines)
	}
}
