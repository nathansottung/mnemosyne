package main

// verify_levels.go — tiered verification so huge mirror sets can be checked at
// practical cost without ever weakening what "verified" means.
//
// Exactly three levels:
//
//	A  Census  file exists + size matches catalog          seconds/TB   advisory only
//	B  Full    complete content hash equals catalog hash    full read    the ONLY level that satisfies COMPLETE
//	C  Sample  exists + size + hash of first & last 4 MiB   fast         advisory only
//
// Levels A and C are advisory: they record intact-so-far evidence but never flip
// a file to COMPLETE and never refresh the verify-due clock — only a level-B pass
// does. Package payload verification and write read-back are ALWAYS level B.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	VerifyA = "A" // census
	VerifyB = "B" // full
	VerifyC = "C" // sample

	sampleWindow = 4 << 20 // 4 MiB head + 4 MiB tail
)

// normLevel canonicalises a requested level; anything unrecognised is treated as
// B (full) so the strong default is never silently downgraded.
func normLevel(l string) string {
	switch strings.ToUpper(strings.TrimSpace(l)) {
	case "A":
		return VerifyA
	case "C":
		return VerifyC
	default:
		return VerifyB
	}
}

// levelName is the human word for a level (census / full / sample).
func levelName(l string) string {
	switch normLevel(l) {
	case VerifyA:
		return "census"
	case VerifyC:
		return "sample"
	default:
		return "full"
	}
}

// levelTag renders a level for a verify note, e.g. "B, full" / "C, sample".
func levelTag(l string) string { return normLevel(l) + ", " + levelName(l) }

// levelSatisfiesComplete reports whether a level counts toward protection — only B.
func levelSatisfiesComplete(l string) bool { return normLevel(l) == VerifyB }

// verifyLevelsMeta is the canonical table (kept in one place for the API + docs).
func verifyLevelsMeta() []map[string]any {
	return []map[string]any{
		{"level": "A", "name": "Census", "checks": "file exists + size matches catalog", "cost": "seconds/TB", "satisfies_complete": false},
		{"level": "B", "name": "Full", "checks": "complete content hash equals catalog hash", "cost": "full read", "satisfies_complete": true},
		{"level": "C", "name": "Sample", "checks": "exists + size + hash of first and last 4 MiB", "cost": "fast", "satisfies_complete": false},
	}
}

// sampleHashHex is the level-C fingerprint: SHA-256 over the file size plus its
// first and last 4 MiB. It deliberately never reads the middle — which is exactly
// why a mid-file corruption is invisible to level C. Small files (< one window)
// are hashed once, whole.
func sampleHashHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	h := sha256.New()
	fmt.Fprintf(h, "%d\n", size) // size is part of the fingerprint
	head := sampleWindow
	if size < int64(head) {
		head = int(size)
	}
	buf := make([]byte, sampleWindow)
	if _, err := io.ReadFull(f, buf[:head]); err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	h.Write(buf[:head])
	if size > int64(sampleWindow) {
		if _, err := f.Seek(size-int64(sampleWindow), io.SeekStart); err != nil {
			return "", err
		}
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			return "", err
		}
		h.Write(buf[:n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyFileAtLevel checks one plain file on a medium against its catalog record
// at the given level. wantSample may be empty for legacy records — level C then
// degrades to exists+size (best-effort, still advisory).
func verifyFileAtLevel(path string, wantSize int64, wantHash, wantSample, level string) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, "missing/unreadable"
	}
	if st.Size() != wantSize {
		return false, fmt.Sprintf("size %d≠%d", st.Size(), wantSize)
	}
	switch normLevel(level) {
	case VerifyA:
		return true, "exists · size ok"
	case VerifyC:
		if strings.TrimSpace(wantSample) == "" {
			return true, "exists · size ok · no sample baseline"
		}
		s, err := sampleHashHex(path)
		if err != nil {
			return false, "sample read error"
		}
		if s != wantSample {
			return false, "sample (ends) mismatch"
		}
		return true, "exists · size · ends ok"
	default: // B
		if strings.TrimSpace(wantHash) == "" {
			return false, "no catalog hash to compare"
		}
		h, err := hashFileHex(path)
		if err != nil {
			return false, "read error"
		}
		if h != wantHash {
			return false, "content hash mismatch"
		}
		return true, "full content ok"
	}
}

// verifyMirrorChunk checks every file of a mirror package at (base + rel path)
// against the catalog at the given level, returning the tally and the first bad
// file. Path-addressable: correct for native mirrors (and legacy mirror drives
// that hold the source tree at its rel paths).
func verifyMirrorChunk(c *Chunk, base, level string) (ok bool, checked, bad int, firstBad string) {
	for _, ref := range c.Files {
		p := filepath.Join(base, filepath.FromSlash(ref.RelPath))
		good, _ := verifyFileAtLevel(p, ref.SizeBytes, ref.Hash, ref.SampleHash, level)
		checked++
		if !good {
			bad++
			if firstBad == "" {
				firstBad = ref.RelPath
			}
		}
	}
	return bad == 0, checked, bad, firstBad
}

// VerifyMirrorVolume re-verifies the mirror package(s) with a current copy on a
// volume at the chosen level. B is full-content and the only level that can
// satisfy COMPLETE / refresh verify-due; A and C are advisory. mount overrides
// where the files are read from (else the recorded copy path).
func (a *App) VerifyMirrorVolume(volumeID int, mount, level string, progress func(float64, string)) (map[string]any, error) {
	level = normLevel(level)
	vol := a.Store.Volume(volumeID)
	if vol == nil {
		return nil, fmt.Errorf("volume %d not found", volumeID)
	}
	type target struct {
		c    *Chunk
		base string
	}
	var targets []target
	for _, c := range a.Store.Chunks(0) {
		if !c.Mirror {
			continue
		}
		for _, cp := range c.Copies {
			if cp.VolumeID == volumeID && !cp.Superseded {
				base := strings.TrimSpace(mount)
				if base == "" {
					base = cp.Path
				}
				targets = append(targets, target{c, base})
				break
			}
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no mirror packages with a current copy on %s", vol.Label)
	}
	results := make([]map[string]any, 0, len(targets))
	okAll := true
	for ti, t := range targets {
		if progress != nil {
			progress(float64(ti)/float64(len(targets)), "verify "+t.c.Name)
		}
		ok, checked, bad, firstBad := verifyMirrorChunk(t.c, t.base, level)
		if !ok {
			okAll = false
		}
		now := time.Now().UTC()
		note := fmt.Sprintf("mirror re-verify (%s): %d/%d ok", levelTag(level), checked-bad, checked)
		if !ok {
			note += " · first bad: " + firstBad
		}
		a.Store.AppendVerifyEvent(t.c, VerifyEvent{At: now, OK: ok, Path: t.base, Note: note, Level: level, Advisory: !levelSatisfiesComplete(level)})
		a.Store.UpdateCopyVerifyLevel(t.c, t.base, ok, level)
		if level == VerifyB {
			if ok {
				t.c.VerifiedAt = &now
			}
			a.refreshChunkStatus(t.c)
			a.Store.UpdateChunk(t.c)
		}
		results = append(results, map[string]any{"chunk": t.c.Name, "ok": ok, "level": level,
			"checked": checked, "bad": bad, "advisory": !levelSatisfiesComplete(level)})
	}
	// Only a level-B pass can change protection state; refresh the dashboard tally.
	if level == VerifyB {
		a.Store.RecomputeProtection(nil)
	}
	if progress != nil {
		progress(1.0, "done")
	}
	a.Store.Log("mirror-verify", fmt.Sprintf("%s: level %s over %d mirror(s), all_ok=%v", vol.Label, level, len(targets), okAll))
	return map[string]any{"level": level, "advisory": !levelSatisfiesComplete(level), "all_ok": okAll, "results": results}, nil
}
