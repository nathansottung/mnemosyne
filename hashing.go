package main

// hashing.go — two hashes, one read, strict roles.
//
// SHA-256 is the ONLY hash that ever touches media. It is what a stranger in 2050
// can recompute on any machine with `sha256sum`, `certutil`, or `Get-FileHash`;
// it is the anchor of the custody chain and appears in every manifest, sidecar,
// BagIt file, and Recovery-Kit inventory. That will never change.
//
// BLAKE3 lives PURELY in the hot loops — scans, drift/scrub comparisons, and dock
// first-passes — as a fast, media-decoupled content-identity hash. It is computed
// in the SAME read pass as SHA-256 (one file read, both hashers) and stored only
// in the catalog (File.Blake3), never emitted to a medium. The rule is written
// into docs/ARCHITECTURE.md: **BLAKE3 never appears on media.** Its job is speed
// and internal comparison; SHA-256's job is durability and universal legibility.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/zeebo/blake3"
)

// hashBufSize is the streaming buffer for whole-file hashing (matches the scan's
// original 8 MiB copy buffer).
const hashBufSize = 8 << 20

// hashFileBoth reads a file ONCE and returns both its SHA-256 (durable, on-media)
// and its BLAKE3 (internal, fast-compare) as lowercase hex. This is the single
// read pass the hot loops use so BLAKE3 costs only marginal CPU on top of the
// SHA-256 we must compute anyway.
func hashFileBoth(path string) (sha256hex, blake3hex string, err error) {
	// SOURCE READ-ONLY: os.Open is O_RDONLY — read, hash, close. Never a write.
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	sh := sha256.New()
	bh := blake3.New()
	if _, err := io.CopyBuffer(io.MultiWriter(sh, bh), f, make([]byte, hashBufSize)); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(sh.Sum(nil)), hex.EncodeToString(bh.Sum(nil)), nil
}

// blake3Hex returns the BLAKE3 of b as lowercase hex — used for in-memory
// content-identity comparisons that never leave the process.
func blake3Hex(b []byte) string {
	h := blake3.Sum256(b)
	return hex.EncodeToString(h[:])
}
