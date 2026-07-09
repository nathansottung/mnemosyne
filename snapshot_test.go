package main

// snapshot_test.go — the drive-snapshot deliverables: ingest two overlapping
// "drives" (folders standing in for docked disks), then prove (1) the drive's tree
// is browsable from the catalog with the drive UNPLUGGED (its files deleted),
// (2) two content-identical drives are detected as a MIRROR pair, and (3) the
// location-aware verdict flips: same location = redundancy at risk, different
// location = a healthy offsite pair. Also covers role classification (RAW vs
// EDITED-EXPORT vs CATALOG) and the "already on DRIVE-x" duplicate report.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDriveSnapshot_OfflineBrowseMirrorAndLocationVerdict(t *testing.T) {
	app := dockApp(t)

	// An archive on the "NAS" — a photo shoot with a raw, an export, a sidecar, and
	// a Lightroom catalog (CRITICAL role).
	src := t.TempDir()
	files := map[string]string{
		"shoot/DSC0001.nef":       "raw-negative-bytes-0001\n",
		"shoot/DSC0001.jpg":       "exported-jpeg-0001\n",
		"shoot/DSC0001.xmp":       "<x:xmpmeta>edits</x:xmpmeta>\n",
		"shoot/DSC0002.nef":       "raw-negative-bytes-0002\n",
		"catalog/Lightroom.lrcat": "SQLite format 3\x00 proprietary catalog\n",
	}
	writeTree(t, src, files)
	coll := app.Store.AddCollection("Shoot")
	if _, err := app.ScanFolder(coll.ID, src, func(float64, string) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}

	ds, err := app.StartDockSession([]int{coll.ID})
	if err != nil {
		t.Fatalf("StartDockSession: %v", err)
	}

	// Two drives with IDENTICAL content — a mirror pair.
	drive1, drive2 := t.TempDir(), t.TempDir()
	writeTree(t, drive1, files)
	writeTree(t, drive2, files)

	r1, err := app.IngestDrive(ds.ID, drive1, "SER-1", "DRIVE-01", "", "", false, func(float64, string) {})
	if err != nil {
		t.Fatalf("ingest 1: %v", err)
	}
	vol1 := app.Store.VolumeBySerial("SER-1")
	r2, err := app.IngestDrive(ds.ID, drive2, "SER-2", "DRIVE-02", "", "", false, func(float64, string) {})
	if err != nil {
		t.Fatalf("ingest 2: %v", err)
	}
	vol2 := app.Store.VolumeBySerial("SER-2")
	_ = r1

	// --- (1) Role classification in the snapshot -------------------------------
	snap1 := app.Store.VolumeSnapshot(vol1.ID)
	if snap1 == nil || snap1.TotalFiles != len(files) {
		t.Fatalf("snapshot 1 should record all %d files, got %+v", len(files), snap1)
	}
	roleOf := map[string]string{}
	for _, f := range snap1.Files {
		roleOf[f.RelPath] = f.Role
	}
	if roleOf["shoot/DSC0001.nef"] != RoleOriginals {
		t.Errorf(".nef should be RAW, got %q", roleOf["shoot/DSC0001.nef"])
	}
	if roleOf["shoot/DSC0001.jpg"] != RoleDeliverables {
		t.Errorf(".jpg should be EDITED-EXPORT, got %q", roleOf["shoot/DSC0001.jpg"])
	}
	if roleOf["shoot/DSC0001.xmp"] != RoleSidecars {
		t.Errorf(".xmp should be SIDECAR, got %q", roleOf["shoot/DSC0001.xmp"])
	}
	if roleOf["catalog/Lightroom.lrcat"] != RoleProject {
		t.Errorf(".lrcat should be CATALOG, got %q", roleOf["catalog/Lightroom.lrcat"])
	}

	// --- (2) Offline browse: unplug the drive, tree still comes from the catalog -
	if err := os.RemoveAll(drive1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(drive1); !os.IsNotExist(err) {
		t.Fatal("precondition: drive1 should be gone")
	}
	root := snapshotTreemap(snap1, "") // no disk access — snapshot only
	var names []string
	for _, c := range root.Children {
		names = append(names, c.Name)
	}
	if !contains(names, "shoot") || !contains(names, "catalog") {
		t.Errorf("offline treemap root should list shoot/ and catalog/, got %v", names)
	}
	// Zoom into shoot/ — its files must be enumerable with the drive unplugged.
	shoot := snapshotTreemap(snap1, "shoot")
	if shoot.Files != 4 {
		t.Errorf("shoot/ should hold 4 files from the snapshot, got %d", shoot.Files)
	}

	// --- (3a) Mirror detected, SAME location = redundancy at risk ---------------
	shoe := app.Store.AddLocation("Shoe Box #1", false, "")
	if err := app.Store.SetVolumeLocation(vol1.ID, shoe.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.SetVolumeLocation(vol2.ID, shoe.ID); err != nil {
		t.Fatal(err)
	}
	rep := app.driveReport(app.Store.VolumeSnapshot(vol2.ID))
	if rep.DuplicateOf == nil || rep.DuplicateOf.Label != "DRIVE-01" {
		t.Fatalf("drive 2 should report drive 1 as its top duplicate, got %+v", rep.DuplicateOf)
	}
	if rep.Mirror == nil {
		t.Fatal("two identical drives must be flagged a MIRROR pair")
	}
	if !rep.Mirror.SameLocation || !rep.Mirror.AtRisk {
		t.Errorf("mirrors in the same location must be at-risk, got %+v", rep.Mirror)
	}
	if !strings.Contains(rep.Mirror.Verdict, "Shoe Box #1") {
		t.Errorf("same-location verdict should name the location, got %q", rep.Mirror.Verdict)
	}
	if !anyContains(rep.Notes, "AT RISK") {
		t.Errorf("report notes should carry the redundancy-at-risk warning, got %v", rep.Notes)
	}
	// The duplicate headline (X of Y files already exist on DRIVE-01).
	if !anyContains(rep.Notes, "already exist on DRIVE-01") {
		t.Errorf("report should note files already exist on the peer drive, got %v", rep.Notes)
	}

	// --- (3b) Move one offsite: same pair is now a HEALTHY offsite pair ---------
	house := app.Store.AddLocation("Grandma's house", true, "")
	if err := app.Store.SetVolumeLocation(vol2.ID, house.ID); err != nil {
		t.Fatal(err)
	}
	rep2 := app.driveReport(app.Store.VolumeSnapshot(vol2.ID))
	if rep2.Mirror == nil || rep2.Mirror.SameLocation || rep2.Mirror.AtRisk {
		t.Errorf("mirrors in different locations must NOT be at-risk, got %+v", rep2.Mirror)
	}
	if !strings.Contains(strings.ToLower(rep2.Mirror.Verdict), "healthy") {
		t.Errorf("different-location verdict should read as a healthy pair, got %q", rep2.Mirror.Verdict)
	}
	_ = r2
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func anyContains(xs []string, sub string) bool {
	for _, v := range xs {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}

// A minimal end-to-end check of the stdlib EXIF reader against a hand-built JPEG
// carrying an APP1 Exif block with DateTimeOriginal + BodySerialNumber.
func TestExtractShotMeta_JPEG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "img.jpg")
	if err := os.WriteFile(p, buildExifJPEG("2021:06:15 09:41:07", "BODY-SN-12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	shot, serial := extractShotMeta(p)
	if shot.IsZero() {
		t.Fatal("expected a capture time from the EXIF block")
	}
	if shot.Year() != 2021 || shot.Month() != 6 || shot.Day() != 15 || shot.Hour() != 9 {
		t.Errorf("wrong capture time parsed: %v", shot)
	}
	if serial != "BODY-SN-12345" {
		t.Errorf("wrong camera serial: %q", serial)
	}
	// A non-image file must yield empty fields, never an error/panic.
	txt := filepath.Join(dir, "note.txt")
	_ = os.WriteFile(txt, []byte("hello"), 0o644)
	if s, c := extractShotMeta(txt); !s.IsZero() || c != "" {
		t.Errorf("non-image must return empty meta, got %v %q", s, c)
	}
}

// buildExifJPEG hand-builds the smallest JPEG that carries an APP1 "Exif" segment
// with a little-endian TIFF block holding DateTimeOriginal (0x9003) and
// BodySerialNumber (0xA431) in the Exif sub-IFD. Enough to exercise the reader
// end-to-end without a binary fixture or a third-party encoder.
func buildExifJPEG(dt, serial string) []byte {
	le := binary.LittleEndian
	dtB := append([]byte(dt), 0)      // NUL-terminated ASCII
	serB := append([]byte(serial), 0) //
	// Fixed layout (offsets relative to the TIFF header start):
	//   0 header · 8 IFD0(1 entry) · 26 ExifIFD(2 entries) · 56 dt · 56+len serial
	const exifIFDOff = 26
	const dtOff = 56
	serOff := dtOff + len(dtB)

	tiff := make([]byte, serOff+len(serB))
	copy(tiff[0:2], "II")
	le.PutUint16(tiff[2:], 0x2A)
	le.PutUint32(tiff[4:], 8) // IFD0 at offset 8

	// IFD0: one entry — the Exif sub-IFD pointer.
	le.PutUint16(tiff[8:], 1)
	putTIFFEntry(tiff[10:], le, 0x8769, 4, 1, exifIFDOff) // ExifIFD, type LONG
	le.PutUint32(tiff[22:], 0)                            // next IFD = none

	// Exif IFD: DateTimeOriginal + BodySerialNumber (both ASCII).
	le.PutUint16(tiff[exifIFDOff:], 2)
	putTIFFEntry(tiff[exifIFDOff+2:], le, 0x9003, 2, uint32(len(dtB)), dtOff)
	putTIFFEntry(tiff[exifIFDOff+14:], le, 0xA431, 2, uint32(len(serB)), uint32(serOff))
	le.PutUint32(tiff[exifIFDOff+26:], 0) // next IFD = none
	copy(tiff[dtOff:], dtB)
	copy(tiff[serOff:], serB)

	payload := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xFF, 0xD8}     // SOI
	out = append(out, 0xFF, 0xE1) // APP1
	seg := make([]byte, 2)
	binary.BigEndian.PutUint16(seg, uint16(len(payload)+2))
	out = append(out, seg...)
	out = append(out, payload...)
	out = append(out, 0xFF, 0xD9) // EOI
	return out
}

// putTIFFEntry writes one 12-byte IFD entry (tag, type, count, value/offset). The
// value field is written as a 4-byte LONG — correct for the pointer/offset entries
// this builder uses (ASCII values here are all >4 bytes, so they carry an offset).
func putTIFFEntry(b []byte, le binary.ByteOrder, tag, typ uint16, count, valOrOff uint32) {
	le.PutUint16(b[0:], tag)
	le.PutUint16(b[2:], typ)
	le.PutUint32(b[4:], count)
	le.PutUint32(b[8:], valOrOff)
}
