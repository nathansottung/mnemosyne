package main

// tools.go — the External Tools catalog the Settings panel renders: every optional
// helper Mnemosyne can use, each with a plain "what it adds" line, a live detected
// status, the config key a manually-browsed binary path saves into, and a direct
// link to the official free download page for the user's OS. Required tools (tar,
// gpg, par2 — the whole restore story) are marked so the UI can separate them from
// the strictly-optional ones. Detection reuses the same a.tool()/status probes
// Preflight uses, so the two never disagree.

import "runtime"

// ToolInfo is one row in the External Tools panel.
type ToolInfo struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`             // true = needed for core features (tar/gpg/par2)
	Adds       string `json:"adds"`                 // one-line "what it adds"
	Detected   bool   `json:"detected"`             // found on PATH or at the configured path
	Path       string `json:"path,omitempty"`       // resolved binary path when detected
	Version    string `json:"version,omitempty"`    // first line of `--version`, when quick
	Configured string `json:"configured,omitempty"` // explicit path currently pinned in config
	SaveKey    string `json:"save_key"`             // where a browsed path is stored: "tools:<name>" or a flat config key
	Download   string `json:"download"`             // official free download page for this OS
	OSNote     string `json:"os_note,omitempty"`    // e.g. "Linux only" when the tool doesn't apply here
}

// pickOS returns the value matching the running OS (linux is the default bucket).
func pickOS(win, mac, linux string) string {
	switch runtime.GOOS {
	case "windows":
		return win
	case "darwin":
		return mac
	default:
		return linux
	}
}

// ToolsView resolves the whole catalog against the current machine + config.
func (a *App) ToolsView() []ToolInfo {
	cfg := a.LoadConfig()
	out := []ToolInfo{}

	// generic PATH/config-resolved tools (stored in the Tools map).
	generic := func(name string, required bool, adds, win, mac, linux string) ToolInfo {
		ti := ToolInfo{Name: name, Required: required, Adds: adds,
			SaveKey: "tools:" + name, Configured: cfg.Tools[name], Download: pickOS(win, mac, linux)}
		if p, err := a.tool(name); err == nil {
			ti.Detected, ti.Path = true, p
			ti.Version = toolVersionLine(p)
		}
		return ti
	}

	out = append(out,
		generic("tar", true, "Bundles your files into one archive — the core package format.",
			"https://learn.microsoft.com/windows-server/administration/windows-commands/tar",
			"https://www.gnu.org/software/tar/", "https://www.gnu.org/software/tar/"),
		generic("gpg", true, "Encrypts backups so a lost tape or disc reveals nothing.",
			"https://www.gpg4win.org/", "https://gpgtools.org/", "https://gnupg.org/download/"),
		generic("par2", true, "Creates repair data so minor media damage can self-heal.",
			"https://github.com/Parchive/par2cmdline/releases",
			"https://github.com/Parchive/par2cmdline/releases",
			"https://github.com/Parchive/par2cmdline/releases"),
		generic("smartctl", false, "Reads each drive's own health report (SMART) so failing media is flagged early.",
			"https://www.smartmontools.org/wiki/Download",
			"https://www.smartmontools.org/wiki/Download", "https://www.smartmontools.org/wiki/Download"),
		generic("ffprobe", false, "Reads created dates and duration from audio and video during ingest.",
			"https://ffmpeg.org/download.html", "https://ffmpeg.org/download.html", "https://ffmpeg.org/download.html"),
		generic("dvdisaster", false, "Adds disc-level scratch-repair (ECC) to burned Blu-ray/DVD discs.",
			"https://dvdisaster.jcea.es/", "https://dvdisaster.jcea.es/", "https://dvdisaster.jcea.es/"),
		generic("czkawka", false, "Finds duplicate files quickly when tidying source folders before backup.",
			"https://github.com/qarmin/czkawka/releases",
			"https://github.com/qarmin/czkawka/releases", "https://github.com/qarmin/czkawka/releases"),
		generic("rclone", false, "Syncs a verified copy to a cloud provider as an extra offsite copy.",
			"https://rclone.org/downloads/", "https://rclone.org/downloads/", "https://rclone.org/downloads/"),
		generic("xorriso", false, "Burns Blu-ray/DVD discs — the free default burner.",
			"https://www.gnu.org/software/xorriso/", "https://www.gnu.org/software/xorriso/",
			"https://www.gnu.org/software/xorriso/"),
	)

	// stenc — Linux-only drive-level tape AES; detection via its own status probe.
	stenc := ToolInfo{Name: "stenc", Required: false,
		Adds:    "Manages the tape drive's built-in AES encryption key (Linux only).",
		SaveKey: "tools:stenc", Configured: cfg.Tools["stenc"],
		Download: "https://github.com/scsitape/stenc"}
	if st := a.StencStatus(); st != nil {
		if av, _ := st["available"].(bool); av {
			stenc.Detected = true
			if b, ok := st["bin"].(string); ok {
				stenc.Path = b
			}
		}
	}
	if runtime.GOOS != "linux" {
		stenc.OSNote = "Linux only"
	}
	out = append(out, stenc)

	// Tape diagnostics — ITDT / tapeinfo / sg_logs / HPE L&TT. The chosen binary is
	// pinned in the flat tape_tool config, so its browse target saves there.
	tape := ToolInfo{Name: "tape diagnostics", Required: false,
		Adds:    "Reads tape-drive health, cleaning status, hours, and bytes written (read-only).",
		SaveKey: "tape_tool", Configured: cfg.TapeTool,
		Download: "https://www.ibm.com/support/pages/ibm-tape-diagnostic-tool-itdt"}
	if ts := a.TapeToolStatus(); ts != nil {
		if av, _ := ts["available"].(bool); av {
			tape.Detected = true
			if b, ok := ts["bin"].(string); ok {
				tape.Path = b
			}
			if t, ok := ts["tool"].(string); ok {
				tape.Version = t
			}
		}
	}
	out = append(out, tape)

	return out
}
