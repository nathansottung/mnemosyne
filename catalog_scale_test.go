package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestCatalogScale is a synthetic scalability benchmark for a 1M+ file catalog.
// It is SKIPPED by default (it builds a ~hundreds-of-MB catalog); run it with:
//
//	MNEMO_SCALE=1 go test -run TestCatalogScale -v -timeout 20m
//
// Override sizes with MNEMO_FILES / MNEMO_MIRROR. It measures load time, save
// time, per-mutation save cost (save frequency proxy), memory footprint, and
// search latency, and prints a table — the numbers the decision gate needs.
func TestCatalogScale(t *testing.T) {
	if os.Getenv("MNEMO_SCALE") == "" {
		t.Skip("set MNEMO_SCALE=1 to run the large-catalog benchmark")
	}
	nFiles := envInt("MNEMO_FILES", 1_000_000)
	nMirror := envInt("MNEMO_MIRROR", 500_000)

	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	// ---- build the catalog directly (bypassing UpsertFile so this measures
	// persistence, not the insert path) ----
	tBuild := time.Now()
	coll := &Collection{ID: 1, Name: "Scale", CreatedAt: time.Now().UTC()}
	folder := &Folder{ID: 1, CollectionID: 1, Path: "/srv/scale"}
	st.c.Collections = []*Collection{coll}
	st.c.Folders = []*Folder{folder}
	st.c.Files = make([]*File, nFiles)
	for i := 0; i < nFiles; i++ {
		st.c.Files[i] = &File{
			ID: i + 1, CollectionID: 1, FolderID: 1,
			RelPath:   fmt.Sprintf("dir%03d/file%08d.nef", i%1000, i),
			SizeBytes: 20_000_000 + int64(i%1000),
			HashAlg:   "SHA256", Hash: fakeHash(i),
		}
	}
	// nMirror mirror-copy records, spread across a handful of volumes/chunks. Each
	// is a verbose ChunkFileRef duplicating rel path + hash(es) from the File.
	const volumes = 5
	per := nMirror / volumes
	for v := 0; v < volumes; v++ {
		refs := make([]ChunkFileRef, per)
		for j := 0; j < per; j++ {
			fi := (v*per + j) % nFiles
			f := st.c.Files[fi]
			refs[j] = ChunkFileRef{FileID: f.ID, RelPath: f.RelPath, SizeBytes: f.SizeBytes, Hash: f.Hash, SampleHash: fakeHash(fi + 7)}
		}
		ok := true
		now := time.Now().UTC()
		st.c.Volumes = append(st.c.Volumes, &Volume{ID: v + 1, Label: fmt.Sprintf("MIR-%d", v+1), Kind: "HDD"})
		st.c.Chunks = append(st.c.Chunks, &Chunk{
			ID: v + 1, CollectionID: 1, Name: fmt.Sprintf("MIRROR-V%d", v+1), Status: StatusAdoptedVerified,
			Mirror: true, FileCount: per, Files: refs,
			Copies: []Copy{{VolumeID: v + 1, Path: fmt.Sprintf("M%d:/mirror", v+1), VerifyOK: &ok, LastVerifiedAt: &now}},
		})
	}
	st.c.NextID = map[string]int{"file": nFiles, "collection": 1, "folder": 1, "chunk": volumes, "volume": volumes}
	buildDur := time.Since(tBuild)

	// ---- save (marshal + atomic write, incl. daily backup as in production) ----
	tSave := time.Now()
	if err := st.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	saveDur := time.Since(tSave)
	fi, _ := os.Stat(st.path)
	sizeMB := float64(fi.Size()) / 1e6

	// ---- load (OpenStore parses the JSON) + memory footprint ----
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	tLoad := time.Now()
	st2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	loadDur := time.Since(tLoad)
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	heapMB := float64(m1.HeapInuse) / 1e6

	// ---- search latency ----
	searchDur := func(q SearchQuery) time.Duration { t0 := time.Now(); st2.Search(q); return time.Since(t0) }
	sPath := searchDur(SearchQuery{Text: "file00000042", Limit: 50})
	sHash := searchDur(SearchQuery{Hash: fakeHash(123)[:12], Limit: 50})
	sExt := searchDur(SearchQuery{Ext: ".nef", Limit: 50})

	// ---- insert throughput: UpsertFile into the loaded 1M catalog. With the
	// file index this is O(1) per file (the first call builds the index once);
	// without it, dedup was a linear scan → O(n²) per full scan (100k inserts
	// into 1M would be ~10 billion comparisons). Batch so we measure the insert
	// path, not the writes.
	insN := 100_000
	st2.BeginBatch()
	tIns := time.Now()
	for i := 0; i < insN; i++ {
		st2.UpsertFile(File{CollectionID: 1, FolderID: 1, RelPath: fmt.Sprintf("newdir/n%08d.cr3", i),
			SizeBytes: 100, HashAlg: "SHA256", Hash: fakeHash(i + 99)})
	}
	insDur := time.Since(tIns)
	st2.batchDepth = 0 // drop the pending write; we only measured inserts
	insRate := float64(insN) / insDur.Seconds()

	// ---- save frequency proxy: what a per-mutation save costs at this size.
	// A job that saves per mutation pays saveDur EACH time; batching collapses
	// that to a handful. Report the cost of e.g. 1000 per-mutation saves.
	perMut := saveDur
	batched := saveDur * 2 // scan/adoption batched: ~2 saves total (mid + end)

	t.Logf("\n"+
		"=== Catalog scale benchmark (%s files, %s mirror-copy records) ===\n"+
		"build (in-memory)      %10s\n"+
		"catalog.json size      %10.1f MB\n"+
		"SAVE  (marshal+write)  %10s   %s\n"+
		"LOAD  (OpenStore)      %10s   %s\n"+
		"heap in use after load %10.1f MB\n"+
		"search by path         %10s\n"+
		"search by hash prefix  %10s\n"+
		"search by extension    %10s\n"+
		"insert 100k (UpsertFile) %8s  (%s files/s, O(1) dedup)\n"+
		"per-mutation save cost  %9s  (×1000 mutations = %s if unbatched)\n"+
		"batched job saves (~2)  %9s\n"+
		"=== decision gate: SQLite if LOAD or SAVE > ~3s ===",
		comma(nFiles), comma(nMirror),
		round(buildDur), sizeMB,
		round(saveDur), gate(saveDur), round(loadDur), gate(loadDur),
		heapMB, round(sPath), round(sHash), round(sExt),
		round(insDur), comma(int(insRate)),
		round(perMut), round(perMut*1000), round(batched))
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// fakeHash returns a 64-hex-char string of the right shape/size as a real
// SHA-256, cheaply (this benchmark measures storage, not hashing).
func fakeHash(i int) string {
	const hexd = "0123456789abcdef"
	b := make([]byte, 64)
	x := uint64(i)*2654435761 + 1
	for k := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[k] = hexd[x&0xf]
	}
	return string(b)
}

func round(d time.Duration) string { return d.Round(time.Millisecond).String() }
func gate(d time.Duration) string {
	if d > 3*time.Second {
		return "OVER 3s"
	}
	return "ok"
}
func comma(n int) string {
	s := strconv.Itoa(n)
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}
