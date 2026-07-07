package main

// tape_test.go — the tape-diagnostics parsers, exercised against captured sample
// outputs in testdata/. These are the tested unit; the live exec path needs a
// physical drive attached (see README). Each parser must extract flags + stats
// defensively, and finalizeTapeHealth must render the right plain-language
// severity/summary and the cleaning recommendation.

import (
	"os"
	"path/filepath"
	"testing"
)

func readTD(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// finalize mirrors what TapeCheck does after parsing, so tests assert the same
// severity/summary the UI sees.
func finalized(th TapeHealth) TapeHealth { finalizeTapeHealth(&th); return th }

func flagIDs(th TapeHealth) map[int]string {
	m := map[int]string{}
	for _, f := range th.Alerts {
		m[f.ID] = f.Severity
	}
	return m
}

func TestParseTapeinfo(t *testing.T) {
	th, err := parseTapeinfo(readTD(t, "tape_tapeinfo.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if th.Vendor != "IBM" || th.Product != "ULT3580-TD6" || th.Serial != "1013000123" {
		t.Errorf("identity: vendor=%q product=%q serial=%q", th.Vendor, th.Product, th.Serial)
	}
	ids := flagIDs(th)
	if ids[8] != "warn" || ids[20] != "clean" {
		t.Errorf("expected flag 8=warn, 20=clean; got %v", ids)
	}
	th = finalized(th)
	if !th.CleaningRecommended {
		t.Error("Clean Now (20) must set CleaningRecommended")
	}
	if th.Severity != "clean" {
		t.Errorf("with a clean flag and only warnings, severity should be clean, got %q (%s)", th.Severity, th.Summary)
	}
}

func TestParseTapeinfo_HealthyNoFlags(t *testing.T) {
	th, err := parseTapeinfo(readTD(t, "tape_tapeinfo_clean.txt"))
	if err != nil {
		t.Fatal(err)
	}
	th = finalized(th)
	if len(th.Alerts) != 0 || th.Severity != "ok" || th.CleaningRecommended {
		t.Errorf("clean drive: alerts=%d sev=%s cleaning=%v", len(th.Alerts), th.Severity, th.CleaningRecommended)
	}
}

func TestParseSgLogs(t *testing.T) {
	th, err := parseSgLogs(readTD(t, "tape_sg_logs_alerts.txt"), readTD(t, "tape_sg_logs_stats.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ids := flagIDs(th)
	// 0x5=5 read failure (error), 0x14=20 clean now (clean), 0x1e=30 hardware (error)
	if ids[5] != "error" || ids[20] != "clean" || ids[30] != "error" {
		t.Errorf("sg_logs flags wrong: %v", ids)
	}
	if th.BytesWritten != 1150*1e9 || th.BytesRead != 3400*1e9 {
		t.Errorf("stats: written=%d read=%d", th.BytesWritten, th.BytesRead)
	}
	if th.Vendor != "IBM" {
		t.Errorf("expected vendor IBM from header, got %q", th.Vendor)
	}
	th = finalized(th)
	if th.Severity != "error" {
		t.Errorf("a read-failure/hardware flag must make severity error, got %q", th.Severity)
	}
	if !th.CleaningRecommended {
		t.Error("clean-now flag must still set CleaningRecommended even under an error")
	}
}

func TestParseITDT(t *testing.T) {
	th, err := parseITDT(readTD(t, "tape_itdt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if th.Serial != "1013000123" || th.Vendor != "IBM" || th.Product != "ULT3580-HH6" {
		t.Errorf("identity: vendor=%q product=%q serial=%q", th.Vendor, th.Product, th.Serial)
	}
	if th.PowerOnHours != 8571 || th.BytesWritten != 128000000000000 || th.BytesRead != 210000000000000 {
		t.Errorf("stats: hours=%d written=%d read=%d", th.PowerOnHours, th.BytesWritten, th.BytesRead)
	}
	if flagIDs(th)[21] != "clean" {
		t.Errorf("expected periodic-clean flag 21; got %v", flagIDs(th))
	}
	th = finalized(th)
	if th.Severity != "clean" || !th.CleaningRecommended {
		t.Errorf("periodic clean should recommend cleaning: sev=%s cleaning=%v", th.Severity, th.CleaningRecommended)
	}
}

func TestParseHpLtt(t *testing.T) {
	th, err := parseHpLtt(readTD(t, "tape_hpltt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if th.Vendor != "HPE" || th.Serial != "HU1234567A" {
		t.Errorf("identity: vendor=%q serial=%q", th.Vendor, th.Serial)
	}
	if th.PowerOnHours != 12044 || th.BytesWritten != 45000000000000 {
		t.Errorf("stats: hours=%d written=%d", th.PowerOnHours, th.BytesWritten)
	}
	ids := flagIDs(th)
	if ids[20] != "clean" || ids[8] != "warn" {
		t.Errorf("expected flags 20=clean, 8=warn; got %v", ids)
	}
	th = finalized(th)
	if th.Severity != "clean" || !th.CleaningRecommended {
		t.Errorf("expected clean recommendation: sev=%s", th.Severity)
	}
}

func TestParseTape_GarbageIsError(t *testing.T) {
	for _, fn := range []func() (TapeHealth, error){
		func() (TapeHealth, error) { return parseTapeinfo([]byte("not tape output\n")) },
		func() (TapeHealth, error) { return parseITDT([]byte("random\ntext\n")) },
		func() (TapeHealth, error) { return parseHpLtt([]byte("nothing useful")) },
		func() (TapeHealth, error) { return parseSgLogs([]byte("x"), []byte("y")) },
	} {
		if _, err := fn(); err == nil {
			t.Errorf("expected an error on non-tape output")
		}
	}
}

// TestResolveTapeTool_ConfigOverride proves an explicit config path is matched to
// the right tool definition by its binary name — the resolution branch that runs
// without any tool on PATH. (The live exec path needs a physical drive; see
// README. The parsers above are the tested unit.)
func TestResolveTapeTool_ConfigOverride(t *testing.T) {
	app := dockApp(t) // App on a temp store, no external tools required
	stub := filepath.Join(t.TempDir(), "itdt.exe")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveConfig(map[string]any{"tape_tool": stub, "tape_device": `\\.\Tape3`}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	def, bin, ok := app.resolveTapeTool()
	if !ok || def.Name != "itdt" || bin != stub {
		t.Fatalf("config override should resolve to itdt at %s; got ok=%v name=%s bin=%s", stub, ok, def.Name, bin)
	}
	if !app.TapeAvailable() {
		t.Error("TapeAvailable should be true with a configured tool")
	}
	if got := app.tapeDevice(); got != `\\.\Tape3` {
		t.Errorf("tapeDevice should honor config, got %q", got)
	}
}

func TestClassifyFlag_UnknownIsWarn(t *testing.T) {
	f := classifyFlag(63) // not in the catalogue
	if f.Severity != "warn" || f.Name == "" {
		t.Errorf("unknown flag should render as amber with a name: %+v", f)
	}
}
