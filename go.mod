module github.com/nsottung/mnemosyne

go 1.22.2

require (
	// Code128 barcode rendering for printable volume labels. Small, pure-Go, no
	// CGO or transitive deps — the same "one static binary, hand-restorable"
	// bargain as go-qrcode. Justified in docs/ARCHITECTURE.md ("Dependencies").
	github.com/boombuler/barcode v1.1.0
	// QR rendering for key recovery cards and volume-ID labels.
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	// BLAKE3 for the hot-loop content hash (scans, drift, dock first-passes),
	// computed alongside SHA-256 in one read pass. Pure-Go with optional SIMD
	// assembly, no CGO — keeps the "one static binary" bargain. SHA-256 remains
	// the ONLY hash on media; BLAKE3 is catalog-internal (see docs/ARCHITECTURE.md).
	github.com/zeebo/blake3 v0.2.4
)

// klauspost/cpuid is BLAKE3's only transitive dep — a tiny pure-Go CPU-feature
// probe used to pick the SIMD path. No CGO.
require github.com/klauspost/cpuid/v2 v2.0.12 // indirect
