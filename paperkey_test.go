package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLineCodeIsCRC16(t *testing.T) {
	// Four uppercase hex digits.
	code := lineCode("Q7xM2fKp9zLa")
	if !regexp.MustCompile(`^[0-9A-F]{4}$`).MatchString(code) {
		t.Fatalf("line code should be 4 uppercase hex (CRC-16), got %q", code)
	}
	// CRC-16 catches an adjacent-character transposition — the classic retyping
	// slip that a byte-wise-sum or a 2-char hash prefix can miss.
	if lineCode("ABCD") == lineCode("ABDC") {
		t.Error("CRC-16 must differ under an adjacent transposition")
	}
	// Known CRC-16/CCITT-FALSE vector: "123456789" → 0x29B1.
	if got := lineCode("123456789"); got != "29B1" {
		t.Errorf("CRC-16/CCITT-FALSE(\"123456789\") = %s, want 29B1", got)
	}
}

// TestRecoveryKitWritesKeyPages drives the real export: two keystores, a generated
// key, then BuildRecoveryKit — and proves the printable one-key-per-page KEYS.html
// (QR + typable grid + standing instruction) and the .sheet.txt land in keys/.
func TestRecoveryKitWritesKeyPages(t *testing.T) {
	a := dockApp(t)
	ks1 := filepath.Join(t.TempDir(), "keystore.json")
	ks2 := filepath.Join(t.TempDir(), "keystore.json")
	if _, err := a.SaveConfig(map[string]any{
		"staging_dir":    filepath.Join(t.TempDir(), "stage"),
		"keystore_paths": []string{ks1, ks2},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	ref, _, _, err := a.GenerateKey("photos 2024")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	out := t.TempDir()
	res, err := a.BuildRecoveryKit(out, func(float64, string) {})
	if err != nil {
		t.Fatalf("BuildRecoveryKit: %v", err)
	}
	keysDir := filepath.Join(res["output_dir"].(string), "keys")
	// The typable sheet and the printable page both exist.
	if _, err := os.Stat(filepath.Join(keysDir, ref+".sheet.txt")); err != nil {
		t.Errorf("expected %s.sheet.txt: %v", ref, err)
	}
	html, err := os.ReadFile(filepath.Join(keysDir, "KEYS.html"))
	if err != nil {
		t.Fatalf("KEYS.html not written: %v", err)
	}
	for _, want := range []string{ref, "this page IS the key", "data:image/png;base64,", "CRC-16", "page-break-after"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("KEYS.html missing %q", want)
		}
	}
}

func TestKeyPageHTMLPrintable(t *testing.T) {
	pass := "Q7xM2fKp9zLa3nBv7wQe1rTy5uIo0pAs"
	page := keyPageHTML("K-page", "photos", passFingerprint(pass), pass, "data:image/png;base64,AAAA")
	for _, want := range []string{"K-page", "this page IS the key", "data:image/png;base64,AAAA", passFingerprint(pass), "CRC-16"} {
		if !strings.Contains(page, want) {
			t.Errorf("printable key page missing %q", want)
		}
	}
	doc := keyPagesDocument(page)
	if !strings.Contains(doc, "page-break-after") {
		t.Error("the key-pages document must force one key per printed page")
	}
	if !strings.Contains(doc, "paperkey") {
		t.Error("the document should note why paperkey does not apply (symmetric passphrases)")
	}
}

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
