package main

// tape.go — OPTIONAL tape-drive diagnostics, strictly OUTSIDE the write path.
//
// Reads a tape drive's own self-report (TapeAlert flags + LOG SENSE pages) via an
// external vendor tool and renders it in plain language: "cleaning cartridge
// recommended", amber/red media/drive advisories, power-on hours, and lifetime
// bytes read/written. It NEVER issues movement or write commands — it only asks
// the drive how it feels. Like SMART, this is a mortality/maintenance SIGNAL that
// COMPLEMENTS hash verification; it never proves the data on a cartridge intact.
//
// Each supported tool has its own small parser (tape_parsers.go) exercised
// against captured sample outputs in testdata/. The exec wrappers here are thin;
// the parsers are the tested unit. Live validation requires the physical drive.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// tapeAlertCatalog maps the standard T10 TapeAlert flag numbers to a plain-
// language name and a severity (clean | warn | error). Unlisted active flags are
// surfaced as amber "drive-reported condition" so nothing is silently ignored.
var tapeAlertCatalog = map[int]struct {
	Name, Severity, Text string
}{
	1:  {"Read warning", "warn", "The drive is having problems reading data. No data has been lost, but performance may drop."},
	2:  {"Write warning", "warn", "The drive is having problems writing data. No data has been lost, but performance may drop."},
	3:  {"Hard error", "error", "An unrecoverable drive error occurred during a read/write operation."},
	4:  {"Media", "error", "The tape can no longer be written or read reliably — replace the cartridge."},
	5:  {"Read failure", "error", "A read failed and could not be recovered — the cartridge or drive is faulty."},
	6:  {"Write failure", "error", "A write failed and could not be recovered — the cartridge or drive is faulty."},
	7:  {"Media life", "warn", "The cartridge has reached the end of its calculated useful life."},
	8:  {"Not data grade", "warn", "The cartridge is not data-grade; the drive may not read/write it reliably."},
	9:  {"Write protect", "warn", "A write was attempted to a write-protected cartridge."},
	12: {"Unsupported format", "warn", "The drive does not support this cartridge's format."},
	13: {"Recoverable mechanical cartridge failure", "error", "The cartridge has a recoverable mechanical fault (e.g. snapped tape)."},
	14: {"Unrecoverable mechanical cartridge failure", "error", "The cartridge has an unrecoverable mechanical fault."},
	15: {"Memory chip failure", "error", "The cartridge memory (MIC) chip has failed — capacity/position data may be lost."},
	16: {"Forced eject", "warn", "The cartridge was manually/forcibly ejected during an operation."},
	17: {"Read-only format", "warn", "The cartridge is in a read-only format."},
	18: {"Tape directory corrupted", "warn", "The tape directory was corrupted on load; find/space may be slow."},
	19: {"Nearing media life", "warn", "The cartridge is nearing the end of its useful life."},
	20: {"Clean now", "clean", "The drive needs cleaning NOW."},
	21: {"Clean periodic", "clean", "The drive is due for its periodic cleaning."},
	22: {"Expired cleaning media", "warn", "The cleaning cartridge has expired — use a fresh one."},
	23: {"Invalid cleaning tape", "warn", "An invalid cleaning cartridge was used."},
	26: {"Cooling fan failure", "error", "The drive's cooling fan has failed."},
	27: {"Power supply failure", "error", "The drive's power supply has failed."},
	30: {"Hardware A", "error", "The drive has a hardware fault requiring a reset."},
	31: {"Hardware B", "error", "The drive has a hardware fault; run diagnostics."},
	32: {"Interface", "warn", "The drive has a problem with the host interface (SCSI/SAS/FC)."},
	33: {"Eject media", "warn", "Eject and re-load the cartridge, then retry the operation."},
	34: {"Download failed", "warn", "A firmware download to the drive failed."},
	35: {"Drive humidity", "warn", "The drive's operating humidity limits were exceeded."},
	36: {"Drive temperature", "error", "The drive's operating temperature limits were exceeded."},
	37: {"Drive voltage", "error", "The drive's voltage limits were exceeded."},
	38: {"Predictive failure", "error", "The drive predicts a hardware failure (SMART-style)."},
	39: {"Diagnostics required", "warn", "The drive has a fault; run extended diagnostics."},
	51: {"Tape directory invalid at unload", "warn", "The tape directory could not be updated at unload."},
	52: {"Tape system area write failure", "error", "The tape's system area could not be written at unload."},
	53: {"Tape system area read failure", "error", "The tape's system area could not be read at load."},
	54: {"No start of data", "error", "The start of data could not be found on the tape."},
	55: {"Loading failure", "warn", "The drive could not load the cartridge and threaded the tape back out."},
	56: {"Unrecoverable unload failure", "error", "The drive could not unload the cartridge."},
	59: {"WORM integrity check failed", "error", "WORM medium integrity check failed — possible tampering."},
	60: {"WORM overwrite attempted", "warn", "An overwrite of WORM medium was attempted and blocked."},
}

// classifyFlag turns an active flag number into a rendered TapeAlertFlag.
func classifyFlag(id int) TapeAlertFlag {
	if e, ok := tapeAlertCatalog[id]; ok {
		return TapeAlertFlag{ID: id, Name: e.Name, Severity: e.Severity, Text: e.Text}
	}
	return TapeAlertFlag{ID: id, Name: fmt.Sprintf("Flag %d", id), Severity: "warn",
		Text: "Drive-reported condition (flag not in the plain-language catalogue)."}
}

// tapeToolDef describes one supported diagnostics CLI: the binaries to probe, an
// install hint, the command(s) to run for a device, and the parser for their
// output. Parse receives one []byte per command, in order.
type tapeToolDef struct {
	Name     string
	Bins     []string
	Hint     string
	Commands func(device string) [][]string
	Parse    func(outs [][]byte) (TapeHealth, error)
}

// tapeTools is the ordered registry — the first available wins when none is
// explicitly configured. Parsers live in tape_parsers.go.
var tapeTools = []tapeToolDef{
	{
		Name: "itdt", Bins: []string{"itdt", "itdt.exe"},
		Hint:     "IBM Tape Diagnostic Tool (ITDT) — free download from IBM Fix Central (Windows/Linux). Best for IBM/LTO drives.",
		Commands: func(dev string) [][]string { return [][]string{{"-f", dev, "tapealert"}} },
		Parse:    func(o [][]byte) (TapeHealth, error) { return parseITDT(firstOut(o)) },
	},
	{
		Name: "tapeinfo", Bins: []string{"tapeinfo"},
		Hint:     "sg3_utils (Linux: `apt install sg3-utils`; macOS: `brew install sg3_utils`). `tapeinfo` renders TapeAlert flags directly.",
		Commands: func(dev string) [][]string { return [][]string{{"-f", dev}} },
		Parse:    func(o [][]byte) (TapeHealth, error) { return parseTapeinfo(firstOut(o)) },
	},
	{
		Name: "sg_logs", Bins: []string{"sg_logs"},
		Hint: "sg3_utils (Linux/macOS). Reads the TapeAlert log page (0x2e) and sequential-access stats page (0x0c).",
		Commands: func(dev string) [][]string {
			return [][]string{{"--page=0x2e", dev}, {"--page=0xc", dev}}
		},
		Parse: func(o [][]byte) (TapeHealth, error) { return parseSgLogs(nthOut(o, 0), nthOut(o, 1)) },
	},
	{
		Name: "hpe-ltt", Bins: []string{"hp_ltt", "ltt", "hpe_ltt"},
		Hint:     "HPE Library & Tape Tools (L&TT) CLI — free download from HPE. Best for HPE StoreEver drives.",
		Commands: func(dev string) [][]string { return [][]string{{"-d", dev, "health"}} },
		Parse:    func(o [][]byte) (TapeHealth, error) { return parseHpLtt(firstOut(o)) },
	},
}

func firstOut(o [][]byte) []byte { return nthOut(o, 0) }
func nthOut(o [][]byte, i int) []byte {
	if i < len(o) {
		return o[i]
	}
	return nil
}

// defaultTapeDevice is the conventional first tape device per OS.
func defaultTapeDevice() string {
	if runtime.GOOS == "windows" {
		return `\\.\Tape0`
	}
	return "/dev/nst0"
}

// resolveTapeTool picks the diagnostics tool to use: an explicit config path
// (matched to a known def by its binary name), else the first registry tool
// found on PATH. Returns the def, the resolved binary path, and ok.
func (a *App) resolveTapeTool() (tapeToolDef, string, bool) {
	cfg := a.LoadConfig()
	if p := strings.TrimSpace(cfg.TapeTool); p != "" {
		base := strings.ToLower(filepath.Base(p))
		for _, d := range tapeTools {
			for _, b := range d.Bins {
				if base == strings.ToLower(b) || strings.HasPrefix(base, strings.TrimSuffix(strings.ToLower(b), ".exe")) {
					if _, err := os.Stat(p); err == nil {
						return d, p, true
					}
				}
			}
		}
	}
	for _, d := range tapeTools {
		for _, b := range d.Bins {
			if lp, err := exec.LookPath(b); err == nil {
				return d, lp, true
			}
		}
	}
	return tapeToolDef{}, "", false
}

// tapeDevice returns the device path to probe: config override, else per-OS default.
func (a *App) tapeDevice() string {
	if d := strings.TrimSpace(a.LoadConfig().TapeDevice); d != "" {
		return d
	}
	return defaultTapeDevice()
}

// TapeAvailable reports whether any tape-diagnostics tool is present (drives
// whether the whole feature is shown or hidden behind an install hint).
func (a *App) TapeAvailable() bool { _, _, ok := a.resolveTapeTool(); return ok }

// TapeToolStatus is the availability summary for the UI/preflight.
func (a *App) TapeToolStatus() map[string]any {
	def, bin, ok := a.resolveTapeTool()
	if !ok {
		hints := make([]string, 0, len(tapeTools))
		for _, d := range tapeTools {
			hints = append(hints, d.Name+": "+d.Hint)
		}
		return map[string]any{"available": false, "hints": hints,
			"summary": "No tape-diagnostics tool found. This optional panel reads drive health only — it never moves the tape or writes to it."}
	}
	return map[string]any{"available": true, "tool": def.Name, "bin": bin, "device": a.tapeDevice()}
}

// TapeCheck runs the resolved tool against the device (override optional), parses
// the output, records a snapshot, and returns it. READ-ONLY toward the drive and
// never on a write path; each command runs under a timeout, and failures are
// returned to the caller and logged.
func (a *App) TapeCheck(deviceOverride string) (*TapeHealth, error) {
	def, bin, ok := a.resolveTapeTool()
	if !ok {
		return nil, fmt.Errorf("no tape-diagnostics tool found — install ITDT, sg3_utils, or HPE L&TT")
	}
	dev := strings.TrimSpace(deviceOverride)
	if dev == "" {
		dev = a.tapeDevice()
	}
	var outs [][]byte
	for _, argv := range def.Commands(dev) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		out, err := exec.CommandContext(ctx, bin, argv...).Output()
		cancel()
		if err != nil && len(out) == 0 {
			a.Store.Log("tape", fmt.Sprintf("%s %v on %s: %v", def.Name, argv, dev, err))
			return nil, fmt.Errorf("%s failed on %s: %w", def.Name, dev, err)
		}
		outs = append(outs, out)
	}
	th, perr := def.Parse(outs)
	if perr != nil {
		a.Store.Log("tape", fmt.Sprintf("%s parse (%s): %v", def.Name, dev, perr))
		return nil, perr
	}
	th.At = time.Now().UTC()
	th.Tool = def.Name
	if th.Device == "" {
		th.Device = dev
	}
	finalizeTapeHealth(&th)
	a.Store.AddTapeCheck(th)
	a.Store.Log("tape", fmt.Sprintf("%s (%s): %s", def.Name, dev, th.Summary))
	return &th, nil
}

// finalizeTapeHealth derives the overall severity, cleaning flag, and a plain-
// language summary from the parsed alerts. Precedence: error > clean > warn > ok.
func finalizeTapeHealth(th *TapeHealth) {
	hasError, hasClean, hasWarn := false, false, false
	var cleanNames, errNames []string
	for _, f := range th.Alerts {
		switch f.Severity {
		case "error":
			hasError = true
			errNames = append(errNames, f.Name)
		case "clean":
			hasClean = true
			th.CleaningRecommended = true
			cleanNames = append(cleanNames, f.Name)
		case "warn":
			hasWarn = true
		}
	}
	switch {
	case hasError:
		th.Severity = "error"
		th.Summary = "Drive reports a media/hardware error (" + strings.Join(errNames, ", ") + ") — verify your copies and migrate off this drive/cartridge."
	case hasClean:
		th.Severity = "clean"
		th.Summary = "Cleaning cartridge recommended (" + strings.Join(cleanNames, ", ") + ")."
	case hasWarn:
		th.Severity = "warn"
		th.Summary = fmt.Sprintf("Drive reported %d warning flag(s) — review before a big write.", countSeverity(th.Alerts, "warn"))
	default:
		th.Severity = "ok"
		th.Summary = "No TapeAlert flags active."
	}
}

func countSeverity(fs []TapeAlertFlag, sev string) int {
	n := 0
	for _, f := range fs {
		if f.Severity == sev {
			n++
		}
	}
	return n
}
