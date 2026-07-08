package main

// dvdisaster_test.go — the optional disc-level ECC layer. The pure pieces
// (method normalisation, command construction, device derivation, filename) are
// deterministic and always run. The real dvdisaster chain is never exercised here:
// like smartctl it is a complement that must hide cleanly when absent, so we only
// assert detection + the "skipped, never fatal" contract.

import (
	"strings"
	"testing"
)

func TestNormBurnEcc(t *testing.T) {
	cases := map[string]string{
		"":        "off",
		"off":     "off",
		"OFF":     "off",
		" rs02 ":  "rs02",
		"RS02":    "rs02",
		"rs03":    "rs03",
		"RS03":    "rs03",
		"rs01":    "off", // unknown/ungeneratable → off (opt-in, never surprise)
		"garbage": "off",
	}
	for in, want := range cases {
		if got := normBurnEcc(in); got != want {
			t.Errorf("normBurnEcc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEccMethodArgAndFileName(t *testing.T) {
	if eccMethodArg("rs02") != "RS02" || eccMethodArg("rs03") != "RS03" {
		t.Error("method arg mapping is wrong")
	}
	if eccMethodArg("off") != "" || eccMethodArg("nope") != "" {
		t.Error("off/unknown methods must map to an empty -m value")
	}
	if eccFileName("PKG-7") != "PKG-7.ecc" {
		t.Errorf("eccFileName = %q, want PKG-7.ecc", eccFileName("PKG-7"))
	}
}

func TestEccGenArgs(t *testing.T) {
	args := eccGenArgs("/dev/sr0", "rs03", "/stage/PKG-1.ecc")
	got := strings.Join(args, " ")
	// Must read the medium at the device (-d), write the external ecc (-e), and
	// encode (-c) with the chosen method.
	for _, want := range []string{"-d /dev/sr0", "-e /stage/PKG-1.ecc", "-mRS03", "-c"} {
		if !strings.Contains(got, want) {
			t.Errorf("eccGenArgs missing %q in %q", want, got)
		}
	}
	// Off / missing device / missing path all yield no command.
	if eccGenArgs("/dev/sr0", "off", "/x.ecc") != nil {
		t.Error("off method must produce no command")
	}
	if eccGenArgs("", "rs02", "/x.ecc") != nil || eccGenArgs("/dev/sr0", "rs02", "") != nil {
		t.Error("a blank device or ecc path must produce no command")
	}
}

func TestDeriveOpticalDevice(t *testing.T) {
	if d := deriveOpticalDevice(defaultBurnCommand); d != "/dev/sr0" {
		t.Errorf("expected /dev/sr0 derived from the default burn command, got %q", d)
	}
	if d := deriveOpticalDevice(`growisofs -Z /dev/dvd=iso`); d != "/dev/dvd" {
		t.Errorf("expected /dev/dvd, got %q", d)
	}
	// ImgBurn / anything without a recognisable /dev node → blank (operator sets it).
	if d := deriveOpticalDevice(`"C:\Program Files\ImgBurn\ImgBurn.exe" /MODE BUILD /SRC "{SRC}"`); d != "" {
		t.Errorf("expected no device derivable from an ImgBurn command, got %q", d)
	}
}

func TestBurnEccConfigPredicates(t *testing.T) {
	off := Config{BurnEcc: "off"}
	if off.burnEccOn() || off.eccIntended() {
		t.Error("off config must not enable ECC or the RESTORE note")
	}
	on := Config{BurnEcc: "rs02"}
	if !on.burnEccOn() || !on.eccIntended() {
		t.Error("rs02 must enable both generation and the RESTORE note")
	}
	// The legacy docs-only flag still lights the RESTORE note without generating.
	note := Config{BurnEcc: "off", OpticalEcc: true}
	if note.burnEccOn() {
		t.Error("optical_ecc alone must NOT turn on auto-generation")
	}
	if !note.eccIntended() {
		t.Error("optical_ecc alone must still add the RESTORE note")
	}
}

// TestGenerateDiscEcc_MissingToolIsNonFatal proves the smartctl-style contract:
// with dvdisaster absent, generation returns an error but is otherwise harmless —
// callers (BurnNext) ignore it, so the burn is never failed by a missing ECC tool.
func TestGenerateDiscEcc_MissingToolIsNonFatal(t *testing.T) {
	app := dockApp(t)
	if app.dvdisasterAvailable() {
		t.Skip("dvdisaster installed on this machine — the missing-tool path can't be exercised here")
	}
	c := &Chunk{Name: "PKG-1", MediaKind: "BD-R25", StagedDir: t.TempDir()}
	cfg := Config{BurnEcc: "rs03"}
	path, err := app.generateDiscEcc(c, cfg, func(float64, string) {})
	if err == nil {
		t.Error("expected an error when dvdisaster is missing")
	}
	if path != "" {
		t.Errorf("no ecc file should be produced when the tool is absent, got %q", path)
	}
}
