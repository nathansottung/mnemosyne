package main

// metadata.go — one discipline-neutral entry point for "when was this captured/
// created?", dispatching by media kind: the stdlib EXIF reader for still images
// (exif.go), and OPTIONAL ffprobe for audio and video. ffprobe is treated exactly
// like smartctl — auto-detected on PATH (or a configured Tools override), never
// required: absent, the audio/video created-date and camera fields simply stay
// empty, and ingest is never failed for it. This is what lets a musician's or
// filmmaker's library cluster into sessions by created date the same way a
// photographer's clusters by EXIF capture date.

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// ffprobeInstallHint is shown in Preflight/Settings when ffprobe is absent.
const ffprobeInstallHint = "ffprobe (bundled with FFmpeg) is optional — install it to read created dates and duration from audio and video during ingest. ffmpeg.org · choco/apt/brew install ffmpeg. Absent, those fields stay empty and ingest still succeeds; it never replaces hash verification."

// ffprobeBin resolves the ffprobe binary (config Tools override, then PATH).
func (a *App) ffprobeBin() (string, error) { return a.tool("ffprobe") }

// ffprobeAvailable reports whether audio/video created-date reads can run at all.
func (a *App) ffprobeAvailable() bool { _, err := a.ffprobeBin(); return err == nil }

// image / audio / video extension sets — which metadata reader (if any) to try.
// Deliberately broad and discipline-spanning; membership decides the reader, not
// the role (a role like ORIGINALS spans stills, stems, and camera footage).
var (
	imageExts = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true,
		".webp": true, ".heic": true, ".heif": true, ".gif": true, ".bmp": true,
		".dng": true, ".nef": true, ".cr2": true, ".cr3": true, ".arw": true,
		".raf": true, ".orf": true, ".rw2": true, ".pef": true, ".3fr": true,
		".psd": true, ".psb": true,
	}
	audioExts = map[string]bool{
		".wav": true, ".aif": true, ".aiff": true, ".flac": true, ".mp3": true,
		".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".alac": true,
	}
	videoExts = map[string]bool{
		".mp4": true, ".mov": true, ".m4v": true, ".mkv": true, ".avi": true,
		".webm": true, ".mts": true, ".m2ts": true, ".wmv": true, ".flv": true,
		".braw": true, ".r3d": true, ".mxf": true,
	}
)

// mediaKindOf classes a file by extension as "image" | "audio" | "video" | "".
func mediaKindOf(rel string) string {
	e := normExt(pathExt(rel))
	switch {
	case imageExts[e]:
		return "image"
	case audioExts[e]:
		return "audio"
	case videoExts[e]:
		return "video"
	}
	return ""
}

// extractMediaMeta returns a capture/created time and (images only) camera body
// serial for a file, dispatching by media kind: the stdlib EXIF reader for stills,
// ffprobe for audio/video when it is installed. Best-effort and NEVER an error — a
// non-media, unreadable, or metadata-less file just yields empty fields. The role
// is accepted for call-site clarity but classification is by extension, so any
// discipline's media is handled uniformly.
func (a *App) extractMediaMeta(path, role string) (time.Time, string) {
	switch mediaKindOf(path) {
	case "image":
		return extractShotMeta(path)
	case "audio", "video":
		if bin, err := a.ffprobeBin(); err == nil {
			if t := probeCreated(bin, path); !t.IsZero() {
				return t, ""
			}
		}
	}
	return time.Time{}, ""
}

// probeCreated shells out to ffprobe for a file's container-level creation time.
// Mirrors the smartctl exec pattern (bounded context, parse stdout). Returns zero
// time on any failure — a missing tool, an unreadable file, or a container with no
// creation tag.
func probeCreated(bin, path string) time.Time {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-v", "quiet", "-print_format", "json", "-show_format", path).Output()
	if err != nil {
		return time.Time{}
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &probe) != nil {
		return time.Time{}
	}
	// Match tags case-insensitively; different muxers name the field differently.
	low := make(map[string]string, len(probe.Format.Tags))
	for k, v := range probe.Format.Tags {
		low[strings.ToLower(k)] = v
	}
	for _, k := range []string{"creation_time", "com.apple.quicktime.creationdate", "date", "date_recorded", "originaldate"} {
		if v := strings.TrimSpace(low[k]); v != "" {
			if t := parseCreatedTime(v); !t.IsZero() {
				return t
			}
		}
	}
	return time.Time{}
}

// parseCreatedTime parses the datetime forms ffprobe emits (RFC3339 and a few
// tolerant variants), normalizing to UTC. Zero time on anything unrecognized.
func parseCreatedTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0000") {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05",
		"2006-01-02T15:04:05", "2006-01-02", "2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
