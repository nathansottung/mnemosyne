package main

// adopt.go — bring pre-existing media into the catalog without rewriting a byte.
//
// Archives written before Mnemosyne (or by hand: `tar` + `par2`) become
// first-class cataloged packages. We scan a mount for payload candidates
// (*.tar / *.tar.gpg), hash each, import its manifest if one rode along
// (decrypting via the keystores when it's a .gpg), and record an ADOPTED-VERIFIED
// package with a verified Copy on the operator's chosen volume. Adoption is
// idempotent: a payload whose hash is already cataloged is skipped, so re-running
// it — or pointing it at one of Mnemosyne's own written chunks — is a no-op
// beyond the report.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// adoptCand is one payload found on the medium plus where it lives.
type adoptCand struct {
	payloadPath string // absolute path to the .tar / .tar.gpg
	dir         string // folder holding the payload (+ its par2/manifest sidecars)
	name        string // payload base name (without .tar / .tar.gpg)
	encrypted   bool
}

// scanAdoptCandidates finds payloads directly under mount and one level deep
// (the NAME/NAME.tar folder layout Mnemosyne itself writes).
func scanAdoptCandidates(mount string) []adoptCand {
	var out []adoptCand
	consider := func(dir, fname string) {
		switch {
		case strings.HasSuffix(fname, ".tar.gpg"):
			out = append(out, adoptCand{filepath.Join(dir, fname), dir, strings.TrimSuffix(fname, ".tar.gpg"), true})
		case strings.HasSuffix(fname, ".tar"):
			out = append(out, adoptCand{filepath.Join(dir, fname), dir, strings.TrimSuffix(fname, ".tar"), false})
		}
	}
	ents, _ := os.ReadDir(mount)
	for _, e := range ents {
		if e.IsDir() {
			sub := filepath.Join(mount, e.Name())
			subents, _ := os.ReadDir(sub)
			for _, se := range subents {
				if !se.IsDir() {
					consider(sub, se.Name())
				}
			}
		} else {
			consider(mount, e.Name())
		}
	}
	return out
}

// hasPar2Set reports whether a par2 set accompanies the payload (either name).
func hasPar2Set(cand adoptCand) bool {
	for _, n := range []string{cand.name + ".tar", cand.name + ".tar.gpg"} {
		if m, _ := filepath.Glob(filepath.Join(cand.dir, n+"*.par2")); len(m) > 0 {
			return true
		}
	}
	return false
}

// readAdoptManifest loads NAME.manifest.json (plaintext), or decrypts
// NAME.manifest.json.gpg by trying each keystore passphrase. Returns the parsed
// manifest, the key_ref that decrypted it (if any), and whether one was found.
func (a *App) readAdoptManifest(dir, name string) (m map[string]any, keyRef string, found bool, err error) {
	if b, e := os.ReadFile(filepath.Join(dir, name+".manifest.json")); e == nil {
		if e := json.Unmarshal(b, &m); e != nil {
			return nil, "", true, fmt.Errorf("manifest is not valid JSON: %w", e)
		}
		return m, "", true, nil
	}
	encPath := filepath.Join(dir, name+".manifest.json.gpg")
	if _, e := os.Stat(encPath); e != nil {
		return nil, "", false, nil // no manifest at all
	}
	gpgBin, e := a.tool("gpg")
	if e != nil {
		return nil, "", true, e
	}
	for _, k := range a.Store.KeyMetas() {
		pass, pe := a.Passphrase(k.Ref)
		if pe != nil {
			continue
		}
		plain, de := gpgDecryptToMem(gpgBin, pass, encPath)
		if de != nil {
			continue
		}
		if e := json.Unmarshal(plain, &m); e == nil {
			return m, k.Ref, true, nil
		}
	}
	return nil, "", true, fmt.Errorf("found an encrypted manifest but no keystore passphrase decrypts it")
}

// filesFromManifest converts a manifest "files" array into ChunkFileRefs.
func filesFromManifest(m map[string]any) []ChunkFileRef {
	arr, _ := m["files"].([]any)
	refs := make([]ChunkFileRef, 0, len(arr))
	for _, it := range arr {
		fm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		rel, _ := fm["rel_path"].(string)
		if rel == "" {
			continue
		}
		var size int64
		if s, ok := fm["size_bytes"].(float64); ok {
			size = int64(s)
		}
		hash, _ := fm["hash"].(string)
		refs = append(refs, ChunkFileRef{RelPath: rel, SizeBytes: size, Hash: hash})
	}
	return refs
}

// tocTime matches the HH:MM[:SS] field `tar -tvf` prints just before the path,
// across GNU tar and bsdtar. Everything after it is the member name.
var tocTime = regexp.MustCompile(`\s\d{1,2}:\d{2}(:\d{2})?\s`)

func parseTarTOC(output string) []ChunkFileRef {
	var refs []ChunkFileRef
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "d") {
			continue // blank or directory
		}
		loc := tocTime.FindStringIndex(line)
		if loc == nil {
			continue
		}
		path := strings.TrimSpace(line[loc[1]:])
		// tar renders hardlinks/symlinks as "name -> target"; keep just the name.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[:i]
		}
		if path == "" || strings.HasSuffix(path, "/") {
			continue
		}
		var size int64
		for _, tok := range strings.Fields(line[:loc[0]]) {
			if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
				size = n // last numeric field before the time is the byte size
			}
		}
		refs = append(refs, ChunkFileRef{RelPath: filepath.ToSlash(path), SizeBytes: size})
	}
	return refs
}

// payloadTOC streams the payload through `tar -tvf` to list its members without
// extracting. Plaintext tars stream directly; encrypted ones are piped through
// gpg first, which needs a known key passphrase.
func (a *App) payloadTOC(payloadPath, keyRef string, encrypted bool) ([]ChunkFileRef, error) {
	tarBin, err := a.tool("tar")
	if err != nil {
		return nil, err
	}
	if !encrypted {
		out, err := exec.Command(tarBin, "-tvf", payloadPath).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("tar -tvf failed: %v: %s", err, tail(string(out), 300))
		}
		return parseTarTOC(string(out)), nil
	}
	if keyRef == "" {
		return nil, fmt.Errorf("encrypted payload needs a known key to list contents")
	}
	gpgBin, err := a.tool("gpg")
	if err != nil {
		return nil, err
	}
	pass, err := a.Passphrase(keyRef)
	if err != nil {
		return nil, err
	}
	gpg := exec.Command(gpgBin, "--batch", "--yes", "--pinentry-mode", "loopback", "--passphrase-fd", "0", "-d", payloadPath)
	gpg.Stdin = strings.NewReader(pass)
	pipe, err := gpg.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tarc := exec.Command(tarBin, "-tvf", "-")
	tarc.Stdin = pipe
	var out, tarErr bytes.Buffer
	tarc.Stdout, tarc.Stderr = &out, &tarErr
	if err := gpg.Start(); err != nil {
		return nil, err
	}
	if err := tarc.Start(); err != nil {
		return nil, err
	}
	terr := tarc.Wait()
	gerr := gpg.Wait()
	if gerr != nil {
		return nil, fmt.Errorf("gpg decrypt failed: %v", gerr)
	}
	if terr != nil {
		return nil, fmt.Errorf("tar -tvf failed: %v: %s", terr, tail(tarErr.String(), 300))
	}
	return parseTarTOC(out.String()), nil
}

// AdoptMedia catalogs every payload found under mountPath as an ADOPTED-VERIFIED
// package with a verified Copy on volumeID. It is idempotent by payload hash.
// deep=true enumerates manifest-less payloads via `tar -tvf` where possible.
func (a *App) AdoptMedia(mountPath string, collectionID, volumeID int, deep bool, progress func(float64, string)) (map[string]any, error) {
	if strings.TrimSpace(mountPath) == "" {
		return nil, fmt.Errorf("mount_path required")
	}
	if a.Store.Collection(collectionID) == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	if _, err := os.Stat(mountPath); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", mountPath, err)
	}
	// Batch catalog writes across the adoption (idempotent: already-cataloged
	// payloads are skipped by hash on a re-run).
	a.Store.BeginBatch()
	defer a.Store.EndBatch()
	if volumeID <= 0 {
		volumeID = a.Store.EnsureUnregistered().ID
	}
	vol := a.Store.Volume(volumeID)
	// Capture the physical device identity of the medium we're adopting from, so
	// the volume carries the drive's real serial/model/capacity. Best-effort: a
	// masked serial or an unmapped mount (network path, optical) changes nothing.
	if vol != nil {
		if _, changed := a.resolveVolumeIdentity(vol, mountPath); changed {
			a.Store.UpdateVolume(vol)
		}
	}

	cands := scanAdoptCandidates(mountPath)
	if len(cands) == 0 {
		return nil, fmt.Errorf("no payload candidates (*.tar / *.tar.gpg) found under %s", mountPath)
	}

	// Idempotency index: payload hash -> already-cataloged package name.
	seen := map[string]string{}
	for _, c := range a.Store.Chunks(0) {
		if c.EncHash != "" {
			seen[c.EncHash] = c.Name
		}
	}

	adopted := []map[string]any{}
	skipped := []map[string]any{}
	unreadable := []map[string]any{}

	// MEDIA READ-ONLY: adoption only READS the medium — os.Stat, hashFileHex
	// (O_RDONLY), and (deep adopt) `tar -tvf` / `gpg -d` streaming. It never writes
	// to the mount; only the catalog gains ADOPTED-VERIFIED packages + copies.
	for i, cand := range cands {
		progress(float64(i)/float64(len(cands)), "hashing "+cand.name)
		st, serr := os.Stat(cand.payloadPath)
		if serr != nil {
			unreadable = append(unreadable, map[string]any{"name": cand.name, "error": serr.Error()})
			continue
		}
		h, herr := hashFileHex(cand.payloadPath)
		if herr != nil {
			unreadable = append(unreadable, map[string]any{"name": cand.name, "error": herr.Error()})
			continue
		}
		if dupName, dup := seen[h]; dup {
			skipped = append(skipped, map[string]any{"name": cand.name, "payload_hash": h, "duplicate_of": dupName})
			continue
		}

		// Manifest import (plaintext, or decrypted via keystores).
		var files []ChunkFileRef
		var keyRef, tarHash, listingSource string
		par2Pct := 0
		if m, kr, found, merr := a.readAdoptManifest(cand.dir, cand.name); found && merr == nil {
			files = filesFromManifest(m)
			if len(files) > 0 {
				listingSource = "manifest"
			}
			if kr != "" {
				keyRef = kr
			}
			if s, _ := m["key_ref"].(string); s != "" && keyRef == "" {
				keyRef = s
			}
			tarHash, _ = m["tar_hash"].(string)
			if p, ok := m["par2_redundancy_percent"].(float64); ok {
				par2Pct = int(p)
			}
		}

		// Deep adopt: enumerate contents from the tar TOC when no manifest listing.
		var deepNote string
		if len(files) == 0 && deep {
			if toc, terr := a.payloadTOC(cand.payloadPath, keyRef, cand.encrypted); terr == nil {
				files = toc
				listingSource = "tar-toc"
			} else {
				deepNote = "deep adopt skipped: " + terr.Error()
			}
		}

		c := Chunk{
			CollectionID: collectionID,
			Name:         cand.name,
			Status:       StatusAdoptedVerified,
			MediaKind:    adoptMediaKind(vol),
			EncBytes:     st.Size(),
			EncHash:      h,
			Encrypted:    cand.encrypted,
			KeyRef:       keyRef,
			HashAlg:      "SHA256",
			TarHash:      tarHash,
			Par2:         par2Pct,
			Files:        files,
			FileCount:    len(files),
			Adopted:      true,
			WrittenDest:  cand.dir,
		}
		if len(files) == 0 {
			c.ListingUnknown = true
		}
		now := time.Now().UTC()
		ok := true
		c.WrittenAt, c.VerifiedAt, c.VerifyOK = &now, &now, &ok

		nc := a.Store.AddChunk(c)
		a.Store.RecordCopy(nc, volumeID, cand.dir, true)
		a.Store.AppendVerifyEvent(nc, VerifyEvent{At: now, OK: true, Path: cand.payloadPath, Note: "adopt: payload hashed"})
		seen[h] = nc.Name // dedupe identical candidates within this same run

		rec := map[string]any{
			"name": nc.Name, "id": nc.ID, "encrypted": nc.Encrypted, "payload_hash": h,
			"files": nc.FileCount, "par2": hasPar2Set(cand), "listing": listingSourceLabel(listingSource),
		}
		if deepNote != "" {
			rec["note"] = deepNote
		}
		adopted = append(adopted, rec)
	}

	summary := fmt.Sprintf("adopted %d · skipped-duplicate %d · unreadable %d", len(adopted), len(skipped), len(unreadable))
	a.Store.Log("adopt", mountPath+": "+summary)
	progress(1.0, summary)
	return map[string]any{
		"mount_path": mountPath, "volume_id": volumeID,
		"adopted": adopted, "skipped_duplicate": skipped, "unreadable": unreadable,
		"summary": summary,
	}, nil
}

// AdoptFolder brings a folder of LOOSE files into a SOURCELESS archive: every file
// is hashed, folded into the archive's union (deduped by content hash), and
// recorded as a verified copy on the drive's volume. The union of all folders
// adopted this way IS the archive's file list — the union is the truth. A file
// present on N drives shows N copies across their locations; identical content is
// one union entry. READ-ONLY toward the folder (only hashes; the catalog changes).
func (a *App) AdoptFolder(mountPath string, collectionID, volumeID int, progress func(float64, string)) (map[string]any, error) {
	coll := a.Store.Collection(collectionID)
	if coll == nil {
		return nil, fmt.Errorf("archive %d not found", collectionID)
	}
	if !coll.IsSourceless() {
		return nil, fmt.Errorf("archive %q is a sourced archive — adopt a loose folder only into a SOURCELESS archive (for sourced archives use scan / mirror / dock)", coll.Name)
	}
	if strings.TrimSpace(mountPath) == "" {
		return nil, fmt.Errorf("mount_path (the drive/folder) required")
	}
	if fi, err := os.Stat(mountPath); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("cannot read folder %s", mountPath)
	}
	if volumeID <= 0 {
		return nil, fmt.Errorf("a volume (the drive these files live on) is required so the copy can be located")
	}
	vol := a.Store.Volume(volumeID)
	if vol == nil {
		return nil, fmt.Errorf("volume %d not found", volumeID)
	}
	if progress == nil {
		progress = func(float64, string) {}
	}
	a.Store.BeginBatch()
	defer a.Store.EndBatch()
	if _, changed := a.resolveVolumeIdentity(vol, mountPath); changed {
		a.Store.UpdateVolume(vol)
	}

	// Walk loose files (skip our own sidecar dir + OS junk, like the dock).
	var paths []string
	_ = filepath.WalkDir(mountPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDockDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if len(paths) == 0 {
		return nil, fmt.Errorf("no files found under %s", mountPath)
	}

	total := len(paths)
	reg := a.formatRegistry() // extension → role, for media metadata on still images
	var mu sync.Mutex
	hashed := map[string]unionFile{} // abs path -> file
	// MEDIA READ-ONLY: only hashes are read; nothing is written to the folder.
	parallelHash(paths,
		func(done int) {
			progress(0.06+float64(done)/float64(total)*0.72, progStats(0, 0, int64(done), int64(total), "hashing drive files"))
		},
		func(p, sha, b3 string, size int64, _ time.Time) {
			rel, e := filepath.Rel(mountPath, p)
			if e != nil {
				rel = filepath.Base(p)
			}
			role, _ := classifyRole(reg, rel)
			uf := unionFile{RelPath: filepath.ToSlash(rel), Hash: sha, Size: size, Role: role}
			uf.ShotAt, uf.CameraSerial = a.extractMediaMeta(p, role)
			mu.Lock()
			hashed[p] = uf
			mu.Unlock()
		})
	unreadable := len(paths) - len(hashed)

	items := make([]unionFile, 0, len(hashed))
	for _, it := range hashed {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RelPath < items[j].RelPath })

	// Fold into the archive's union (dedup by hash); new = files not seen on any
	// prior drive, duplicate = already in the union (the same content on another drive).
	progress(0.85, "folding into union")
	ids, added := a.Store.UpsertUnionFiles(collectionID, items)
	dupInUnion := len(items) - added

	// One ref per distinct FileID for THIS drive (dedup identical content within
	// the drive), so the mirror package records exactly one copy per union file.
	seen := map[int]bool{}
	refs := make([]ChunkFileRef, 0, len(items))
	for i, it := range items {
		fid := ids[i]
		if seen[fid] {
			continue
		}
		seen[fid] = true
		refs = append(refs, ChunkFileRef{FileID: fid, RelPath: it.RelPath, SizeBytes: it.Size, Hash: it.Hash})
	}
	progress(0.92, "recording copies")
	a.upsertMirrorChunk(collectionID, vol, mountPath, refs, "mirror-adopt")

	// Collision pass: same-logical-file/different-bytes across the union. Class-(a)
	// second-shooter frames auto-pass (noted); true conflicts (b/c) open a review item.
	progress(0.96, "checking for content conflicts")
	scan := a.DetectConflicts(collectionID)

	union := a.Store.CollectionFileCount(collectionID)
	summary := fmt.Sprintf("adopted %d file(s) from %s — %d new to the union, %d already present · union now %d",
		len(refs), vol.Label, added, dupInUnion, union)
	if scan.SecondShooter > 0 {
		summary += fmt.Sprintf(" · %d second-shooter group(s) kept (different camera bodies)", scan.SecondShooter)
	}
	if scan.Open > 0 {
		summary += fmt.Sprintf(" · ⚠ %d content conflict(s) need review", scan.Open)
	}
	a.Store.Log("adopt", coll.Name+": "+summary)
	progress(1.0, summary)
	return map[string]any{
		"archive": coll.Name, "volume_id": vol.ID, "volume": vol.Label,
		"files_on_drive": len(refs), "new_to_union": added, "duplicate_in_union": dupInUnion,
		"union_files": union, "unreadable": unreadable, "summary": summary,
		"conflicts": scan.Open, "new_conflicts": scan.New, "second_shooter": scan.SecondShooter,
	}, nil
}

func adoptMediaKind(v *Volume) string {
	if v != nil && v.Kind != "" && v.Kind != "OTHER" {
		return v.Kind
	}
	return "ADOPTED"
}

func listingSourceLabel(src string) string {
	switch src {
	case "manifest":
		return "from manifest"
	case "tar-toc":
		return "from tar TOC (hashes unknown)"
	default:
		return "unknown — restore to enumerate contents"
	}
}
