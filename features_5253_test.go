package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpticalRestoreNote proves optical packages get the dvdisaster ECC paragraph
// that explicitly says par2 works regardless — and non-optical packages don't.
func TestOpticalRestoreNote(t *testing.T) {
	read := func(c *Chunk) string {
		dir := t.TempDir()
		writeRestoreTxt(dir, c, false)
		b, err := os.ReadFile(filepath.Join(dir, "RESTORE.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	optical := read(&Chunk{Name: "OPT-1", MediaKind: "BD-R25", HashAlg: "SHA256", EncHash: "e"})
	if !strings.Contains(optical, "dvdisaster") {
		t.Error("optical RESTORE.txt should mention dvdisaster")
	}
	if !strings.Contains(optical, "par2 repair of the payload works\nregardless") &&
		!strings.Contains(optical, "regardless") {
		t.Error("optical RESTORE.txt must state par2 repair works regardless")
	}
	hdd := read(&Chunk{Name: "HDD-1", MediaKind: "HDD", HashAlg: "SHA256", EncHash: "e"})
	if strings.Contains(hdd, "dvdisaster") {
		t.Error("non-optical RESTORE.txt should NOT carry the ECC paragraph")
	}
}

func TestDefaultBurnerIsXorriso(t *testing.T) {
	if !strings.Contains(defaultBurnCommand, "xorriso") {
		t.Error("the documented default burn command should be xorriso")
	}
	if !isOpticalKind("DVD-R") || isOpticalKind("HDD") {
		t.Error("optical-kind detection is wrong")
	}
}

// TestDriveEncryptionWarnsInKit proves a stenc/drive-encrypted volume is flagged
// loudly in the Recovery Kit's media inventory.
func TestDriveEncryptionWarnsInKit(t *testing.T) {
	volm := map[int]*Volume{
		1: {ID: 1, Label: "LTO-0007", Kind: "TAPE", Barcode: "NSP-0007", DriveEncrypted: true, DriveEncNote: "SKLM vault-01"},
		2: {ID: 2, Label: "LTO-0008", Kind: "TAPE"},
	}
	chunks := []*Chunk{{Name: "PKG-1", MediaKind: "TAPE", EncHash: "abc", HashAlg: "SHA256"}}
	md := mediaInventoryMD(chunks, volm)
	if !strings.Contains(md, "DRIVE-ENCRYPTED") {
		t.Fatalf("kit inventory must shout about drive-encrypted media:\n%s", md)
	}
	if !strings.Contains(md, "UNRECOVERABLE") {
		t.Error("the warning should state the lost-key consequence plainly")
	}
	if !strings.Contains(md, "SKLM vault-01") {
		t.Error("the recorded drive-key location should be surfaced")
	}
	// The safe (non-encrypted) tape should not be flagged.
	if strings.Contains(md, "LTO-0008 ⚠") {
		t.Error("a non-encrypted volume must not be flagged")
	}
}
