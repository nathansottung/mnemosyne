package main

// exif.go — a minimal, stdlib-only EXIF reader for the dock snapshot. It pulls
// just two facts from a photo: WHEN it was taken (DateTimeOriginal) and WHICH
// camera body took it (BodySerialNumber). No third-party dependency — the same
// "one static binary, hand-restorable" bargain as the rest of the tree.
//
// Scope is deliberately narrow: the common containers that hold a TIFF-structured
// EXIF block — baseline JPEG (APP1 "Exif") and the TIFF-based raws (NEF, CR2, ARW,
// DNG, ORF, RW2, PEF, 3FR, plain TIFF). CR3/HEIF (ISO-BMFF) and video are NOT
// parsed here; they fall through to empty fields. Parsing is best-effort and
// TOTALLY non-fatal: a truncated file, an odd byte order, a MakerNote we don't
// understand — any of it just yields empty fields, NEVER an error that could fail
// an ingest. Only the header region is read (a bounded prefix), so a 60 MB raw
// costs a few KB here, not a second full read.

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"time"
)

// exifReadCap bounds how much of a file's front we pull looking for EXIF. The
// TIFF header, IFD0, the Exif sub-IFD, and their ASCII value bytes all live near
// the start in every container we handle; offsets beyond this are treated as
// absent rather than chased into a multi-gigabyte raw.
const exifReadCap = 1 << 20 // 1 MiB

// EXIF tag numbers we care about (TIFF/EXIF standard, vendor-neutral).
const (
	tagDateTime          = 0x0132 // IFD0 file-change datetime (fallback)
	tagExifIFD           = 0x8769 // IFD0 → pointer to the Exif sub-IFD
	tagDateTimeOriginal  = 0x9003 // Exif capture datetime (preferred)
	tagDateTimeDigitized = 0x9004
	tagBodySerialNumber  = 0xA431 // Exif camera body serial (standardized in EXIF 2.3)
	tagCameraSerialDNG   = 0xC62F // DNG CameraSerialNumber (fallback)
)

// extractShotMeta returns the EXIF capture time and camera body serial for a
// file, or zero/empty when it cannot be parsed. It never returns an error: the
// snapshot treats unparseable metadata as simply absent (per the ingest spec).
func extractShotMeta(path string) (time.Time, string) {
	f, err := os.Open(path) // O_RDONLY — read-only toward the drive, like every other read
	if err != nil {
		return time.Time{}, ""
	}
	defer f.Close()
	buf := make([]byte, exifReadCap)
	n, _ := f.Read(buf)
	if n <= 0 {
		return time.Time{}, ""
	}
	buf = buf[:n]

	tiff := locateTIFF(buf)
	if tiff == nil {
		return time.Time{}, ""
	}
	return parseTIFFExif(tiff)
}

// locateTIFF returns the byte slice starting at the TIFF header ("II*\0"/"MM\0*")
// for the containers we handle: a raw TIFF/raw file (the whole buffer) or a JPEG
// whose APP1 segment carries an "Exif\0\0"-prefixed TIFF block. Returns nil when
// no TIFF header is found.
func locateTIFF(b []byte) []byte {
	if len(b) < 8 {
		return nil
	}
	// Bare TIFF / TIFF-based raw: the file itself starts with the TIFF header.
	if isTIFFHeader(b) {
		return b
	}
	// JPEG: 0xFFD8 start, then a chain of marker segments. Find APP1 (0xFFE1)
	// carrying "Exif\0\0"; the TIFF block begins right after that 6-byte prefix.
	if b[0] != 0xFF || b[1] != 0xD8 {
		return nil
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return nil // not a marker where one was expected — give up
		}
		marker := b[i+1]
		// Standalone markers without a length payload.
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if i+4 > len(b) {
			return nil
		}
		segLen := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if segLen < 2 {
			return nil
		}
		payload := i + 4
		end := i + 2 + segLen
		if marker == 0xE1 && payload+6 <= len(b) && bytes.Equal(b[payload:payload+6], []byte("Exif\x00\x00")) {
			t := payload + 6
			if end > len(b) {
				end = len(b)
			}
			if t < end {
				return b[t:end]
			}
			return b[t:]
		}
		if marker == 0xDA { // start of scan — image data follows, no more headers
			return nil
		}
		i = end
	}
	return nil
}

func isTIFFHeader(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	return (b[0] == 'I' && b[1] == 'I' && b[2] == 0x2A && b[3] == 0x00) ||
		(b[0] == 'M' && b[1] == 'M' && b[2] == 0x00 && b[3] == 0x2A)
}

// parseTIFFExif reads IFD0 for a fallback DateTime and the Exif sub-IFD pointer,
// then the Exif sub-IFD for DateTimeOriginal and the body serial. tiff[0] is the
// TIFF header (byte-order marker); all offsets are relative to it.
func parseTIFFExif(tiff []byte) (time.Time, string) {
	if !isTIFFHeader(tiff) {
		return time.Time{}, ""
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if tiff[0] == 'M' {
		bo = binary.BigEndian
	}
	ifd0 := int(bo.Uint32(tiff[4:8]))
	dt0, _, exifPtr := scanIFD(tiff, ifd0, bo, false)

	var shot time.Time
	var serial string
	if exifPtr > 0 {
		exifDT, exifSerial, _ := scanIFD(tiff, exifPtr, bo, true)
		shot = parseExifTime(exifDT)
		serial = exifSerial
	}
	if shot.IsZero() {
		shot = parseExifTime(dt0) // fall back to IFD0 DateTime
	}
	return shot, strings.TrimSpace(serial)
}

// scanIFD walks one Image File Directory. In IFD0 mode (exif=false) it returns the
// DateTime string and the Exif sub-IFD offset. In Exif mode it returns the
// DateTimeOriginal (or Digitized) string and the body serial. Bounds are checked
// at every step; a malformed IFD returns zero values.
func scanIFD(tiff []byte, off int, bo binary.ByteOrder, exif bool) (dateStr, serial string, subIFD int) {
	if off <= 0 || off+2 > len(tiff) {
		return "", "", 0
	}
	count := int(bo.Uint16(tiff[off : off+2]))
	base := off + 2
	for e := 0; e < count; e++ {
		p := base + e*12
		if p+12 > len(tiff) {
			break
		}
		tag := bo.Uint16(tiff[p : p+2])
		typ := bo.Uint16(tiff[p+2 : p+4])
		cnt := bo.Uint32(tiff[p+4 : p+8])
		valField := tiff[p+8 : p+12]
		switch {
		case !exif && tag == tagExifIFD:
			subIFD = int(bo.Uint32(valField))
		case !exif && tag == tagDateTime:
			dateStr = asciiValue(tiff, bo, typ, cnt, valField)
		case exif && (tag == tagDateTimeOriginal || (tag == tagDateTimeDigitized && dateStr == "")):
			dateStr = asciiValue(tiff, bo, typ, cnt, valField)
		case exif && tag == tagBodySerialNumber:
			serial = asciiValue(tiff, bo, typ, cnt, valField)
		case exif && tag == tagCameraSerialDNG && serial == "":
			serial = asciiValue(tiff, bo, typ, cnt, valField)
		}
	}
	return dateStr, serial, subIFD
}

// asciiValue extracts an ASCII (type 2) EXIF value. Values ≤4 bytes live inline in
// the 4-byte value field; longer ones live at the offset the field points to.
func asciiValue(tiff []byte, bo binary.ByteOrder, typ uint16, cnt uint32, valField []byte) string {
	if typ != 2 || cnt == 0 { // 2 = ASCII
		return ""
	}
	n := int(cnt)
	var raw []byte
	if n <= 4 {
		raw = valField[:n]
	} else {
		off := int(bo.Uint32(valField))
		if off < 0 || off+n > len(tiff) {
			return ""
		}
		raw = tiff[off : off+n]
	}
	return strings.TrimRight(string(raw), "\x00 ")
}

// parseExifTime parses the EXIF datetime format "2006:01:02 15:04:05" (and a few
// tolerant variants). Returns zero time on anything it cannot read. EXIF carries
// no timezone, so the stamp is treated as UTC — a stable, comparable instant.
func parseExifTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0000") {
		return time.Time{}
	}
	for _, layout := range []string{"2006:01:02 15:04:05", "2006:01:02 15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}
