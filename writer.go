package main

// writer.go — RAM ring buffer (goroutines + a bounded channel), read-back
// verify, and the three-tool restore. This is the file that would host a
// raw-tape (/dev/nst0) backend later; everything above it is agnostic.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type RingStats struct {
	BufferBlocks  int     `json:"buffer_blocks"`
	BlockBytes    int     `json:"block_bytes"`
	MinFill       int     `json:"min_fill"`
	StarvedEvents int     `json:"starved_events"`
	Bytes         int64   `json:"bytes"`
	Seconds       float64 `json:"seconds"`
	ReadMBps      float64 `json:"read_mbps"`
	WriteMBps     float64 `json:"write_mbps"`
}

// ringCopy streams src -> dst through a bounded channel of blocks,
// hashing on the READ side (so the hash proves what left the source).
//
// throttleMbps > 0 paces the WRITER (drain) side only — the reader keeps
// filling the ring unpaced, which is the whole point of the buffer: read fast,
// write at a steady capped rate (e.g. to keep an SSD from overheating). Pacing
// is against cumulative bytes vs elapsed time, so it self-corrects into a
// smooth, steady rate instead of bursts.
// offset/length select a byte range of src; length <= 0 means "from offset to
// EOF". This lets a spanned chunk stream one segment's range through the same
// ring buffer as a whole-file write.
func ringCopy(src, dst string, offset, length int64, blockMB int, bufferGB, throttleMbps float64, progress func(float64)) (string, RingStats, error) {
	block := blockMB << 20
	depth := int(bufferGB * float64(1<<30) / float64(block))
	if depth < 2 {
		depth = 2
	}
	stats := RingStats{BufferBlocks: depth, BlockBytes: block, MinFill: depth}

	in, err := os.Open(src)
	if err != nil {
		return "", stats, err
	}
	defer in.Close()
	if offset > 0 {
		if _, err := in.Seek(offset, io.SeekStart); err != nil {
			return "", stats, err
		}
	}
	total := length
	if total <= 0 {
		if st, err := in.Stat(); err == nil {
			total = st.Size() - offset
		}
	}
	out, err := os.Create(dst)
	if err != nil {
		return "", stats, err
	}
	defer out.Close()

	ch := make(chan []byte, depth)
	errCh := make(chan error, 1)
	h := sha256.New()
	var readBytes int64
	var readSecs float64
	var readerDone int32 // set when the reader has produced its last block

	go func() {
		t0 := time.Now()
		remaining := length // when > 0, read exactly this many bytes
		for {
			nb := block
			if length > 0 {
				if remaining <= 0 {
					break
				}
				if int64(nb) > remaining {
					nb = int(remaining)
				}
			}
			b := make([]byte, nb)
			n, err := io.ReadFull(in, b)
			if n > 0 {
				h.Write(b[:n])
				ch <- b[:n]
				readBytes += int64(n)
				remaining -= int64(n)
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if err != nil {
				errCh <- err
				break
			}
		}
		readSecs = time.Since(t0).Seconds()
		atomic.StoreInt32(&readerDone, 1)
		close(ch)
	}()

	throttleBps := throttleMbps * 1e6
	start := time.Now()
	var written int64
	for b := range ch {
		// Sample buffer occupancy only in steady state: skip the first block
		// (buffer still warming) and everything after the reader has finished
		// (the tail always drains to empty — counting it would peg min at 0 and
		// hide whether the writer ever actually starved mid-stream).
		if written > 0 && atomic.LoadInt32(&readerDone) == 0 {
			if fill := len(ch); fill < stats.MinFill {
				stats.MinFill = fill
				if fill == 0 {
					stats.StarvedEvents++
				}
			}
		}
		if _, err := out.Write(b); err != nil {
			return "", stats, err
		}
		written += int64(len(b))
		if total > 0 {
			progress(float64(written) / float64(total))
		}
		// Writer-side pacing only: sleep until cumulative bytes match the target
		// rate. Self-correcting against wall clock, so the rate stays smooth.
		if throttleBps > 0 {
			target := time.Duration(float64(written) / throttleBps * float64(time.Second))
			if el := time.Since(start); target > el {
				time.Sleep(target - el)
			}
		}
	}
	select {
	case err := <-errCh:
		return "", stats, err
	default:
	}
	secs := time.Since(start).Seconds()
	stats.Bytes, stats.Seconds = written, round2(secs)
	stats.WriteMBps = round1(float64(written) / 1e6 / secs)
	if readSecs > 0 {
		stats.ReadMBps = round1(float64(readBytes) / 1e6 / readSecs)
	}
	return hex.EncodeToString(h.Sum(nil)), stats, nil
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// ---- chunk-level operations -------------------------------------------

// stagedPayloadPresent reports whether the package's staged payload still exists
// on disk — the local artifact a fresh copy can be re-written from.
func (a *App) stagedPayloadPresent(c *Chunk) bool {
	return c.StagedDir != "" && payloadPathIn(c.StagedDir, c) != ""
}

// refreshChunkStatus recomputes a package's lifecycle status from the best
// available evidence, per the multi-copy model: one bad copy never drags the
// whole package to FAILED while the staged artifact or another verified copy
// survives. FAILED stays reserved for build/staging failures (set directly by
// those paths); this never sets FAILED. PLANNED/BUILDING are pre-staging states
// owned by the build and left untouched (a write/verify can't run on them). The
// mid-write WRITING state IS resolved here — the write calls this at the end to
// transition to its evidence-based status.
func (a *App) refreshChunkStatus(c *Chunk) {
	switch c.Status {
	case "PLANNED", "BUILDING":
		return
	}
	switch {
	case c.VerifiedCopyCount() > 0:
		ok := true
		c.Status, c.Error, c.VerifyOK = "VERIFIED", "", &ok
	case c.CurrentCopyCount() > 0:
		// A copy exists but none verifies — written yet under-protected; the
		// per-copy state carries the failure, the package is not FAILED.
		bad := false
		c.Status, c.Error, c.VerifyOK = "WRITTEN", "", &bad
	case a.stagedPayloadPresent(c):
		c.Status, c.Error, c.VerifyOK = "STAGED", "", nil
	}
}

func (a *App) WriteChunk(id int, destDir string, bufferGB float64, blockMB int, throttleMbps float64, volumeID int, progress func(float64, string)) (map[string]any, error) {
	cfg := a.LoadConfig()
	if bufferGB <= 0 {
		bufferGB = cfg.BufferGB
	}
	if blockMB <= 0 {
		blockMB = cfg.BlockMB
	}
	if throttleMbps <= 0 { // 0 = use configured default (which is itself 0 = unthrottled)
		throttleMbps = cfg.ThrottleMbps
	}
	c := a.Store.Chunk(id)
	if c == nil {
		return nil, fmt.Errorf("package %d not found", id)
	}
	if c.Spanned {
		return nil, fmt.Errorf("package %s is spanned; use span-write (one tape at a time)", c.Name)
	}
	if c.StagedDir == "" || (c.Status != "STAGED" && c.Status != "WRITTEN" && c.Status != "VERIFIED" && c.Status != "FAILED") {
		return nil, fmt.Errorf("package %s is %s; build it first", c.Name, c.Status)
	}
	if err := a.Store.AssertOutsideSources(destDir); err != nil {
		return nil, err
	}
	enc := payloadPathIn(c.StagedDir, c)
	if enc == "" {
		return nil, fmt.Errorf("staged payload missing under %s", c.StagedDir)
	}
	// Preserve whatever name the payload was staged under (current <name>.tar /
	// <name>.tar.gpg, or the legacy .tar.gpg for a plaintext package built before
	// the rename) so the on-medium payload stays consistent with its sidecars.
	payloadBase := filepath.Base(enc)
	dest := filepath.Join(destDir, c.Name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	var need int64
	entries, _ := os.ReadDir(c.StagedDir)
	for _, e := range entries {
		n := e.Name()
		// Skip staging-only files: dirs, the filelist, and the intermediate tar
		// that sits beside the ciphertext for encrypted packages. For a plaintext
		// package the tar IS the payload (n == payloadBase), so it is counted.
		if e.IsDir() || n == "filelist.txt" {
			continue
		}
		if n == c.Name+".tar" && n != payloadBase {
			continue
		}
		if st, err := e.Info(); err == nil {
			need += st.Size()
		}
	}
	if free, err := diskFree(destDir); err == nil && free < need {
		return nil, fmt.Errorf("destination too small: need %.1f GB, free %.1f GB", float64(need)/1e9, float64(free)/1e9)
	}

	c.Status = "WRITING"
	a.Store.UpdateChunk(c)
	// mediumFail: a write/medium error. The staged artifact is fine, so the
	// package falls back to its evidence-based status (STAGED, or still VERIFIED
	// via another copy) — never FAILED for a bad medium.
	mediumFail := func(err error) (map[string]any, error) {
		a.refreshChunkStatus(c)
		a.Store.UpdateChunk(c)
		return nil, err
	}
	// stagedFail: the staged artifact itself is corrupt (its bytes no longer hash
	// to enc_hash). That IS a package-level FAILED.
	stagedFail := func(err error) (map[string]any, error) {
		c.Status, c.Error = "FAILED", err.Error()
		a.Store.UpdateChunk(c)
		return nil, err
	}

	progress(0.02, "writing payload")
	destPayload := filepath.Join(dest, payloadBase)
	streamHash, stats, err := ringCopy(enc, destPayload, 0, 0, blockMB, bufferGB, throttleMbps,
		func(p float64) { progress(0.02+p*0.66, "") })
	c.RingStats = &stats // telemetry: proof the buffer decoupled read from a throttled write
	if err != nil {
		return mediumFail(err)
	}
	if streamHash != c.EncHash {
		return stagedFail(fmt.Errorf("stream hash mismatch while writing (staged payload corrupted — rebuild)"))
	}

	progress(0.70, "writing sidecars")
	for _, e := range entries {
		n := e.Name()
		// Skip the payload (streamed above) and the staging-only intermediate tar
		// and filelist; copy every real sidecar (par2 set, manifest, RESTORE.txt).
		if e.IsDir() || n == payloadBase || n == c.Name+".tar" || n == "filelist.txt" {
			continue
		}
		if c.PrivateManifest && n == c.Name+".manifest.json" {
			continue // private: the ENCRYPTED manifest.json.gpg ships instead
		}
		if err := copyFile(filepath.Join(c.StagedDir, n), filepath.Join(dest, n)); err != nil {
			return mediumFail(err)
		}
	}

	progress(0.78, "read-back verify")
	rb, err := hashFileHex(destPayload)
	if err != nil {
		return mediumFail(err)
	}
	ok := rb == c.EncHash
	now := time.Now().UTC()
	c.WrittenDest, c.WrittenAt = dest, &now
	if ok {
		c.VerifiedAt = &now
	}
	if volumeID <= 0 {
		volumeID = a.Store.EnsureUnregistered().ID
	}
	// Record the result on THIS copy; derive package status from all copies. A
	// bad read-back marks only this copy failed — the package stays healthy if
	// the staged payload or another verified copy is intact.
	a.Store.RecordCopy(c, volumeID, dest, ok)
	note := "write read-back"
	if !ok {
		note = "write read-back: hash mismatch medium=" + rb
	}
	a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: ok, Path: destPayload, Note: note})
	a.refreshChunkStatus(c)
	a.Store.UpdateChunk(c)
	a.Store.Log("write", fmt.Sprintf("%s -> %s verify_ok=%v", c.Name, dest, ok))
	if ok {
		progress(1.0, "verified")
	} else {
		progress(1.0, "read-back MISMATCH — copy marked failed")
	}
	return map[string]any{"chunk": c.Name, "dest": dest, "verify_ok": ok,
		"status": c.Status, "verified_copies": c.VerifiedCopyCount(), "ring_buffer": stats}, nil
}

// RewriteCopy re-writes the package's copy on volumeID from staging to the same
// destination folder, superseding the existing (typically FAILED) copy. The old
// record is retained in history (superseded=true); the write creates a fresh
// Copy. This is the "Re-write this copy" affordance for a failed medium.
func (a *App) RewriteCopy(id, volumeID int, bufferGB float64, blockMB int, throttleMbps float64, progress func(float64, string)) (map[string]any, error) {
	c := a.Store.Chunk(id)
	if c == nil {
		return nil, fmt.Errorf("package %d not found", id)
	}
	if c.Spanned {
		return nil, fmt.Errorf("package %s is spanned; re-write its segments via span-write", c.Name)
	}
	var dest string
	for i := range c.Copies {
		if c.Copies[i].VolumeID == volumeID && !c.Copies[i].Superseded {
			dest = c.Copies[i].Path
			break
		}
	}
	if dest == "" {
		return nil, fmt.Errorf("no current copy of %s on volume %d to re-write", c.Name, volumeID)
	}
	a.Store.SupersedeCopy(c, volumeID)
	// WriteChunk rebuilds dest as destDir/<name>, so pass the copy folder's parent.
	return a.WriteChunk(id, filepath.Dir(dest), bufferGB, blockMB, throttleMbps, volumeID, progress)
}

func (a *App) VerifyChunk(id int, destDir string) (map[string]any, error) {
	c := a.Store.Chunk(id)
	if c == nil {
		return nil, fmt.Errorf("package %d not found", id)
	}
	base := destDir
	if base == "" {
		base = c.WrittenDest
	}
	enc := findPayload(base, c)
	if enc == "" {
		return nil, fmt.Errorf("payload for %s not found under %s (looked for %s, flat or in a %s/ folder)",
			c.Name, base, strings.Join(payloadNameCandidates(c), " or "), c.Name)
	}
	rb, err := hashFileHex(enc)
	if err != nil {
		return nil, err
	}
	ok := rb == c.EncHash
	now := time.Now().UTC()
	if ok {
		c.VerifiedAt = &now
	}
	// Record the result on the copy that lives at this medium; the package status
	// derives from ALL copies, so a single bad medium never marks the package
	// FAILED while another copy or the staged payload is intact.
	note := "media verify"
	if !ok {
		note = "media verify: hash mismatch medium=" + rb
	}
	a.Store.AppendVerifyEvent(c, VerifyEvent{At: now, OK: ok, Path: enc, Note: note})
	a.Store.UpdateCopyVerify(c, base, ok)
	a.refreshChunkStatus(c)
	a.Store.UpdateChunk(c)
	return map[string]any{"chunk": c.Name, "path": enc, "verify_ok": ok, "expected": c.EncHash, "actual": rb,
		"status": c.Status, "verified_copies": c.VerifiedCopyCount()}, nil
}

// VerifyCampaign scans dest_dir for chunk folders/payloads whose names match
// cataloged chunks and re-verifies each against its enc_hash — "insert the
// tape/disc, verify everything on it in one click". Strictly read-only with
// respect to media; only the catalog's verify history/status is updated.
func (a *App) VerifyCampaign(destDir string, progress func(float64, string)) (map[string]any, error) {
	if strings.TrimSpace(destDir) == "" {
		return nil, fmt.Errorf("dest_dir required")
	}
	byName := map[string]*Chunk{}
	for _, c := range a.Store.Chunks(0) {
		byName[c.Name] = c
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", destDir, err)
	}
	type cand struct {
		c    *Chunk
		path string
	}
	var cands []cand
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() { // dest/NAME/<payload> (current or legacy name)
			if c, ok := byName[name]; ok {
				if p := payloadPathIn(filepath.Join(destDir, name), c); p != "" {
					cands = append(cands, cand{c, p})
				}
			}
			continue
		}
		// flat payload: dest/NAME.tar or dest/NAME.tar.gpg
		var base string
		if strings.HasSuffix(name, ".tar.gpg") {
			base = strings.TrimSuffix(name, ".tar.gpg")
		} else if strings.HasSuffix(name, ".tar") {
			base = strings.TrimSuffix(name, ".tar")
		} else {
			continue
		}
		if c, ok := byName[base]; ok {
			cands = append(cands, cand{c, filepath.Join(destDir, name)})
		}
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("no cataloged packages found on %s (looked for NAME/<payload> or NAME.tar[.gpg])", destDir)
	}
	var okCount int
	results := make([]map[string]any, 0, len(cands))
	for i, cd := range cands {
		progress(float64(i)/float64(len(cands)), "verify "+cd.c.Name)
		h, herr := hashFileHex(cd.path)
		ok := herr == nil && h == cd.c.EncHash
		note := "campaign"
		if herr != nil {
			note = "campaign: " + herr.Error()
		} else if !ok {
			note = "campaign: hash mismatch"
		}
		now := time.Now().UTC()
		if ok {
			cd.c.VerifiedAt = &now
			okCount++
		}
		// Copy-level result; package status derives from all copies (a bad medium
		// on this campaign does not fail a package with other verified copies).
		a.Store.AppendVerifyEvent(cd.c, VerifyEvent{At: now, OK: ok, Path: cd.path, Note: note})
		a.Store.UpdateCopyVerify(cd.c, destDir, ok)
		a.refreshChunkStatus(cd.c)
		a.Store.UpdateChunk(cd.c)
		results = append(results, map[string]any{"chunk": cd.c.Name, "ok": ok, "path": cd.path,
			"status": cd.c.Status, "verified_copies": cd.c.VerifiedCopyCount()})
	}
	progress(1.0, fmt.Sprintf("verified %d/%d ok", okCount, len(cands)))
	a.Store.Log("verify-campaign", fmt.Sprintf("%s: %d/%d ok", destDir, okCount, len(cands)))
	return map[string]any{"dest_dir": destDir, "checked": len(cands), "ok": okCount, "results": results}, nil
}

func (a *App) RestoreChunk(id int, sourceDir, outputDir string, members []string, progress func(float64, string)) (map[string]any, error) {
	c := a.Store.Chunk(id)
	if c == nil {
		return nil, fmt.Errorf("package %d not found", id)
	}
	if sourceDir == "" {
		sourceDir = c.WrittenDest
	}
	// Restore WRITES extracted files into outputDir — it must never target source
	// data (that would overwrite the very originals we exist to protect).
	if err := a.Store.AssertOutsideSources(outputDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	enc := findPayload(sourceDir, c)
	if enc == "" {
		// Spanned restore drill: if the joined payload isn't present but the
		// segment files are (all copied into one scratch dir), rejoin them —
		// the same `cat seg* > payload` step RESTORE.txt documents by hand.
		if c.Spanned {
			joined, jerr := rejoinSegments(sourceDir, outputDir, c, progress)
			if jerr != nil {
				return nil, jerr
			}
			enc = joined
		} else {
			return nil, fmt.Errorf("payload for %s not found under %s (point source at the package folder on the medium)", c.Name, sourceDir)
		}
	}
	par2Bin, err := a.tool("par2")
	if err != nil {
		return nil, err
	}
	tarBin, err := a.tool("tar")
	if err != nil {
		return nil, err
	}
	var gpgBin string
	if c.Encrypted {
		if gpgBin, err = a.tool("gpg"); err != nil {
			return nil, err
		}
	}

	repaired := false
	par2f := enc + ".par2"
	progress(0.05, "par2 verify")
	if _, err := os.Stat(par2f); err == nil {
		if verr := run(par2Bin, "", "verify", par2f); verr != nil {
			progress(0.10, "par2 repair")
			if rerr := run(par2Bin, "", "repair", par2f); rerr != nil {
				return nil, fmt.Errorf("par2 repair failed: %v", rerr)
			}
			repaired = true
		}
	} else if h, _ := hashFileHex(enc); h != c.EncHash {
		return nil, fmt.Errorf("no par2 present and ciphertext hash mismatch — data damaged")
	}

	if !c.Encrypted {
		// Unencrypted: the payload is a plain tar; extract it directly, no gpg.
		progress(0.25, "extract")
		targs := []string{"-xf", enc, "-C", outputDir}
		targs = append(targs, members...)
		if err := run(tarBin, "", targs...); err != nil {
			return nil, fmt.Errorf("tar extract failed: %v", err)
		}
		progress(1.0, "restored")
		a.Store.Log("restore", fmt.Sprintf("%s -> %s (repaired=%v)", c.Name, outputDir, repaired))
		return map[string]any{"chunk": c.Name, "repaired": repaired, "output": outputDir}, nil
	}

	progress(0.25, "decrypt + extract")
	pass, err := a.Passphrase(c.KeyRef)
	if err != nil {
		return nil, err
	}
	gpg := exec.Command(gpgBin, "--batch", "--yes", "--pinentry-mode", "loopback",
		"--passphrase-fd", "0", "-d", enc)
	gpg.Stdin = strings.NewReader(pass)
	pipe, err := gpg.StdoutPipe()
	if err != nil {
		return nil, err
	}
	targs := []string{"-xf", "-", "-C", outputDir}
	targs = append(targs, members...)
	tarc := exec.Command(tarBin, targs...)
	tarc.Stdin = pipe
	var tarErr strings.Builder
	tarc.Stderr = &tarErr
	var gpgErr strings.Builder
	gpg.Stderr = &gpgErr

	if err := gpg.Start(); err != nil {
		return nil, err
	}
	if err := tarc.Start(); err != nil {
		return nil, err
	}
	terr := tarc.Wait()
	gerr := gpg.Wait()
	if gerr != nil {
		return nil, fmt.Errorf("gpg decrypt failed: %s", tail(gpgErr.String(), 400))
	}
	if terr != nil {
		return nil, fmt.Errorf("tar extract failed: %s", tail(tarErr.String(), 400))
	}
	progress(1.0, "restored")
	a.Store.Log("restore", fmt.Sprintf("%s -> %s (repaired=%v)", c.Name, outputDir, repaired))
	return map[string]any{"chunk": c.Name, "repaired": repaired, "output": outputDir}, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
