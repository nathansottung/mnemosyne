package main

// span.go — spanning a single chunk whose payload is larger than one medium.
//
// Doctrine preserved: the payload (payloadName: <chunk>.tar.gpg when encrypted,
// <chunk>.tar when not) is byte-split into segment files <chunk>.segNNN sized to
// the medium; par2 is still computed over the WHOLE payload at build time;
// restore is rejoin (copy /b | cat) then the usual par2 -> gpg -> tar. Each
// segment is streamed through the ring buffer and read-back verified, so every
// tape proves it holds its verified bytes.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// segmentBytes is the usable payload per medium: the whole target minus a small
// reserve for the filesystem and the per-tape manifest + RESTORE.txt sidecars.
func segmentBytes(target int64) int64 {
	if target <= 0 {
		return 0
	}
	return int64(float64(target) * 0.98)
}

// planSegments splits payloadBytes into PENDING data segments no larger than one
// medium. Byte offsets are implicit (the cumulative sum of prior segment Bytes).
func planSegments(payloadBytes, target int64) []Segment {
	seg := segmentBytes(target)
	if seg <= 0 {
		seg = payloadBytes
	}
	n := int((payloadBytes + seg - 1) / seg)
	if n < 1 {
		n = 1
	}
	out := make([]Segment, 0, n)
	remaining := payloadBytes
	for i := 1; i <= n; i++ {
		b := seg
		if remaining < b {
			b = remaining
		}
		out = append(out, Segment{Index: i, Bytes: b, Status: "PENDING"})
		remaining -= b
	}
	return out
}

// par2SetFiles returns the par2 set beside the payload, whose names follow the
// payload (<payload>.par2, <payload>.volNNN+MM.par2). It tries the current
// payload name first, then the legacy .tar.gpg name so a plaintext package built
// before the rename still resolves its <name>.tar.gpg.par2 set.
func par2SetFiles(dir string, c *Chunk) []string {
	for _, n := range payloadNameCandidates(c) {
		if m, _ := filepath.Glob(filepath.Join(dir, n+"*.par2")); len(m) > 0 {
			sort.Strings(m)
			return m
		}
	}
	return nil
}

func par2SetSize(dir string, c *Chunk) int64 {
	var total int64
	for _, m := range par2SetFiles(dir, c) {
		if st, err := os.Stat(m); err == nil {
			total += st.Size()
		}
	}
	return total
}

// finalizeSegments computes the definitive segment plan once the real payload
// size and par2 set size are known (called from BuildChunk). The par2 volumes
// ride on the last data tape if they fit, otherwise they get their own tape.
func (a *App) finalizeSegments(c *Chunk, work string) {
	segs := planSegments(c.EncBytes, c.TargetBytes)
	par2Total := par2SetSize(work, c)
	last := segs[len(segs)-1]
	const sidecarReserve = 512 * 1024 // manifest + RESTORE.txt cushion
	if last.Bytes+par2Total+sidecarReserve > c.TargetBytes {
		segs = append(segs, Segment{Index: len(segs) + 1, Bytes: par2Total, Status: "PENDING", Par2: true})
	}
	c.Segments = segs
}

// dataSegmentCount returns how many segments carry payload bytes (excludes a
// dedicated par2 tape).
func dataSegmentCount(segs []Segment) int {
	n := 0
	for _, s := range segs {
		if !s.Par2 {
			n++
		}
	}
	return n
}

// SpanWriteNext writes the next PENDING segment of a spanned chunk to destDir,
// streaming its byte range of the staged payload through the ring buffer (honoring
// throttle), then reads it back and verifies before marking VERIFIED. Sidecars
// (manifest + RESTORE.txt) land on every tape; the par2 set rides the last data
// tape or its own, per the build-time plan.
func (a *App) SpanWriteNext(id int, destDir string, bufferGB float64, blockMB int, throttleMbps float64, volumeID int, progress func(float64, string)) (map[string]any, error) {
	cfg := a.LoadConfig()
	if bufferGB <= 0 {
		bufferGB = cfg.BufferGB
	}
	if blockMB <= 0 {
		blockMB = cfg.BlockMB
	}
	if throttleMbps <= 0 {
		throttleMbps = cfg.ThrottleMbps
	}
	c := a.Store.Chunk(id)
	if c == nil {
		return nil, fmt.Errorf("package %d not found", id)
	}
	if !c.Spanned || len(c.Segments) == 0 {
		return nil, fmt.Errorf("package %s is not a spanned package", c.Name)
	}
	if c.StagedDir == "" {
		return nil, fmt.Errorf("package %s is not built yet", c.Name)
	}
	if strings.TrimSpace(destDir) == "" {
		return nil, fmt.Errorf("dest_dir (the mounted tape/drive) required")
	}
	segIdx := -1
	for i := range c.Segments {
		if c.Segments[i].Status == "PENDING" || c.Segments[i].Status == "FAILED" {
			segIdx = i
			break
		}
	}
	if segIdx < 0 {
		return nil, fmt.Errorf("no pending segments in %s — all tapes written", c.Name)
	}
	if volumeID <= 0 {
		volumeID = a.Store.EnsureUnregistered().ID
	}
	seg := &c.Segments[segIdx]
	seg.VolumeID = volumeID
	N := len(c.Segments)
	payload := payloadPathIn(c.StagedDir, c)
	if payload == "" {
		return nil, fmt.Errorf("staged payload for %s missing under %s", c.Name, c.StagedDir)
	}
	destChunk := filepath.Join(destDir, c.Name)
	if err := os.MkdirAll(destChunk, 0o755); err != nil {
		return nil, err
	}
	set := func(status string) { seg.Status, seg.Dest = status, destDir; a.Store.UpdateChunk(c) }
	fail := func(err error) (map[string]any, error) {
		seg.Status, seg.Dest = "FAILED", destDir
		a.Store.UpdateChunk(c)
		a.Store.AppendVerifyEvent(c, VerifyEvent{At: time.Now().UTC(), OK: false, Path: destChunk, Note: fmt.Sprintf("span segment %d/%d: %v", seg.Index, N, err)})
		return nil, err
	}
	set("WRITING")

	if seg.Par2 {
		// Dedicated par2 tape.
		progress(0.1, fmt.Sprintf("writing par2 set (tape %d/%d)", seg.Index, N))
		if err := copyPar2Set(c.StagedDir, destChunk, c); err != nil {
			return fail(err)
		}
	} else {
		// Data segment: stream its byte range of the payload.
		var offset int64
		for i := 0; i < segIdx; i++ {
			if !c.Segments[i].Par2 {
				offset += c.Segments[i].Bytes
			}
		}
		segFile := filepath.Join(destChunk, fmt.Sprintf("%s.seg%03d", c.Name, seg.Index))
		progress(0.05, fmt.Sprintf("writing segment %d/%d (%d bytes)", seg.Index, N, seg.Bytes))
		streamHash, stats, err := ringCopy(payload, segFile, offset, seg.Bytes, blockMB, bufferGB, throttleMbps,
			func(p float64) { progress(0.05+p*0.6, "") })
		c.RingStats = &stats
		if err != nil {
			return fail(err)
		}
		seg.Hash = streamHash
		set("WRITTEN")
		progress(0.7, fmt.Sprintf("read-back verify segment %d/%d", seg.Index, N))
		rb, err := hashFileHex(segFile)
		if err != nil {
			return fail(err)
		}
		if rb != streamHash {
			return fail(fmt.Errorf("segment %d read-back mismatch (medium=%s)", seg.Index, rb))
		}
		// par2 rides the last data tape when it wasn't given its own.
		if seg.Index == dataSegmentCount(c.Segments) && !c.Segments[N-1].Par2 {
			progress(0.85, "writing par2 set alongside last segment")
			if err := copyPar2Set(c.StagedDir, destChunk, c); err != nil {
				return fail(err)
			}
		}
	}

	// manifest + RESTORE.txt on EVERY tape (copied verbatim from the staged build).
	if err := copySidecars(c.StagedDir, destChunk, c); err != nil {
		return fail(err)
	}

	now := time.Now().UTC()
	seg.Status, seg.Dest = "VERIFIED", destDir
	a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: true, Path: destChunk, Note: fmt.Sprintf("span segment %d/%d read-back verified", seg.Index, N)})

	done := 0
	for _, sg := range c.Segments {
		if sg.Status == "VERIFIED" {
			done++
		}
	}
	label := fmt.Sprintf("label this tape %s-tape-%d-of-%d", c.Name, seg.Index, N)
	if done == N {
		c.Status, c.Error = "VERIFIED", ""
		c.WrittenDest, c.WrittenAt, c.VerifiedAt = destDir, &now, &now
		ok := true
		c.VerifyOK = &ok
		// The whole spanned set is one verified copy; record it against the
		// volume of the first segment as representative (each tape's own volume
		// is on its Segment.VolumeID for the Volumes view).
		firstVol := volumeID
		for _, sg := range c.Segments {
			if sg.VolumeID != 0 {
				firstVol = sg.VolumeID
				break
			}
		}
		a.Store.RecordCopy(c, firstVol, fmt.Sprintf("spanned across %d media", N), true)
		a.Store.Log("span", fmt.Sprintf("%s fully written across %d media", c.Name, N))
	} else {
		a.Store.UpdateChunk(c)
	}
	msg := fmt.Sprintf("Segment %d/%d verified — eject and %s, mount the next tape, then continue.", seg.Index, N, label)
	if done == N {
		msg = fmt.Sprintf("Segment %d/%d verified — %s. ALL %d tapes done; package VERIFIED.", seg.Index, N, label, N)
	}
	progress(1.0, msg)
	return map[string]any{"chunk": c.Name, "segment": seg.Index, "of": N, "done": done, "complete": done == N, "message": msg}, nil
}

func copyPar2Set(stagedDir, destChunk string, c *Chunk) error {
	files := par2SetFiles(stagedDir, c)
	if len(files) == 0 {
		return fmt.Errorf("no par2 files found in staged dir for %s", c.Name)
	}
	for _, src := range files {
		dst := filepath.Join(destChunk, filepath.Base(src))
		if err := copyFile(src, dst); err != nil {
			return err
		}
		// read-back verify each par2 file against the staged original
		sh, err := hashFileHex(src)
		if err != nil {
			return err
		}
		dh, err := hashFileHex(dst)
		if err != nil {
			return err
		}
		if sh != dh {
			return fmt.Errorf("par2 file %s read-back mismatch", filepath.Base(src))
		}
	}
	return nil
}

func copySidecars(stagedDir, destChunk string, c *Chunk) error {
	manifest := c.Name + ".manifest.json"
	if c.PrivateManifest {
		manifest = c.Name + ".manifest.json.gpg" // private: encrypted listing only
	}
	for _, base := range []string{manifest, "RESTORE.txt"} {
		src := filepath.Join(stagedDir, base)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, filepath.Join(destChunk, base)); err != nil {
			return err
		}
	}
	return nil
}

// rejoinSegments concatenates <chunk>.segNNN files found in sourceDir into
// outputDir/<payload> and copies the par2 set beside it, so the existing restore
// path (par2 -> gpg -> tar) runs unchanged. This is the automated form of the
// `cat seg* > payload` step RESTORE.txt documents by hand. The joined file is
// named after whichever par2 set is present (current <payload>.par2, or a legacy
// <name>.tar.gpg.par2 set) so `par2 verify <payload>.par2` matches either way.
func rejoinSegments(sourceDir, outputDir string, c *Chunk, progress func(float64, string)) (string, error) {
	segs, _ := filepath.Glob(filepath.Join(sourceDir, c.Name+".seg*"))
	if len(segs) == 0 {
		// maybe segments are in per-tape subfolders under sourceDir
		segs, _ = filepath.Glob(filepath.Join(sourceDir, "*", c.Name+".seg*"))
	}
	if len(segs) == 0 {
		return "", fmt.Errorf("no segment files (%s.segNNN) found under %s — copy every tape's segment into one folder first", c.Name, sourceDir)
	}
	sort.Strings(segs) // segNNN zero-padded => lexical order == segment order
	want := dataSegmentCount(c.Segments)
	if want > 0 && len(segs) != want {
		return "", fmt.Errorf("found %d segment files but package needs %d — some tapes are missing", len(segs), want)
	}
	// Pick the payload base name from the par2 set actually on the media.
	base := payloadName(c)
	for _, cand := range payloadNameCandidates(c) {
		if m, _ := filepath.Glob(filepath.Join(sourceDir, cand+"*.par2")); len(m) > 0 {
			base = cand
			break
		}
		if m, _ := filepath.Glob(filepath.Join(sourceDir, "*", cand+"*.par2")); len(m) > 0 {
			base = cand
			break
		}
	}
	joined := filepath.Join(outputDir, base)
	out, err := os.Create(joined)
	if err != nil {
		return "", err
	}
	defer out.Close()
	buf := make([]byte, 8<<20)
	for i, sp := range segs {
		progress(float64(i)/float64(len(segs))*0.2, fmt.Sprintf("rejoin %d/%d", i+1, len(segs)))
		in, err := os.Open(sp)
		if err != nil {
			return "", err
		}
		if _, err := io.CopyBuffer(out, in, buf); err != nil {
			in.Close()
			return "", err
		}
		in.Close()
	}
	// bring the par2 set next to the joined payload for verify/repair
	for _, glob := range []string{filepath.Join(sourceDir, base+"*.par2"), filepath.Join(sourceDir, "*", base+"*.par2")} {
		if m, _ := filepath.Glob(glob); len(m) > 0 {
			for _, p := range m {
				_ = copyFile(p, filepath.Join(outputDir, filepath.Base(p)))
			}
			break
		}
	}
	return joined, nil
}
