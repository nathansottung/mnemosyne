module github.com/nsottung/mnemosyne

go 1.22.2

require (
	// Code128 barcode rendering for printable volume labels. Small, pure-Go, no
	// CGO or transitive deps — the same "one static binary, hand-restorable"
	// bargain as go-qrcode. Justified in docs/ARCHITECTURE.md ("Dependencies").
	github.com/boombuler/barcode v1.1.0
	// QR rendering for key recovery cards and volume-ID labels.
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
)
