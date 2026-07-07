package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSampleLevelMissesMiddleCorruption is the load-bearing guarantee: a level-C
// (sample) pass over a mirror with a mid-file corruption must NOT detect it and
// must NOT mark anything COMPLETE, while a level-B (full) pass MUST catch it.
func TestSampleLevelMissesMiddleCorruption(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	app := &App{DataDir: filepath.Dir(st.path), Store: st}
	coll := st.AddCollection("Mirrors")
	vol := st.AddVolume(Volume{Label: "MIR-01", Kind: "HDD"})

	// A 10 MiB mirror file — big enough that the middle sits outside the 4 MiB
	// head and 4 MiB tail that level C samples.
	medium := t.TempDir()
	rel := "big.bin"
	p := filepath.Join(medium, rel)
	data := make([]byte, 10<<20)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write medium file: %v", err)
	}
	full, err := hashFileHex(p)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sample, err := sampleHashHex(p)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	// A mirror package with one file, and an UNVERIFIED copy on the volume (so we
	// can prove the advisory levels never promote it).
	c := st.AddChunk(Chunk{CollectionID: coll.ID, Name: "MIRROR-M-V1", Status: StatusAdoptedVerified, Mirror: true,
		Files: []ChunkFileRef{{FileID: 1, RelPath: rel, SizeBytes: int64(len(data)), Hash: full, SampleHash: sample}}})
	c.Copies = []Copy{{VolumeID: vol.ID, Path: medium}}
	st.UpdateChunk(c)

	// Corrupt a single byte at 5 MiB — squarely in the untested middle, size unchanged.
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open for corrupt: %v", err)
	}
	if _, err := f.WriteAt([]byte{data[5<<20] ^ 0xFF}, 5<<20); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	f.Close()

	// Level C: advisory, and blind to the middle — must report OK (not detected).
	resC, err := app.VerifyMirrorVolume(vol.ID, medium, "C", func(float64, string) {})
	if err != nil {
		t.Fatalf("C verify: %v", err)
	}
	if resC["all_ok"] != true {
		t.Fatalf("level C must NOT detect a mid-file corruption, got %v", resC)
	}
	if resC["advisory"] != true {
		t.Fatal("level C must be advisory")
	}
	cc := st.Chunk(c.ID)
	if cc.Copies[0].VerifyOK != nil {
		t.Fatal("advisory C pass must NOT set VerifyOK (no COMPLETE)")
	}
	if cc.Copies[0].LastCheckLevel != "C" || cc.Copies[0].LastCheckOK == nil || !*cc.Copies[0].LastCheckOK {
		t.Fatalf("C pass should record an advisory check, got %+v", cc.Copies[0])
	}

	// Level B: full read — must catch the corruption and fail the copy.
	resB, err := app.VerifyMirrorVolume(vol.ID, medium, "B", func(float64, string) {})
	if err != nil {
		t.Fatalf("B verify: %v", err)
	}
	if resB["all_ok"] != false {
		t.Fatalf("level B must catch the mid-file corruption, got %v", resB)
	}
	if resB["advisory"] != false {
		t.Fatal("level B is not advisory")
	}
	cb := st.Chunk(c.ID)
	if cb.Copies[0].VerifyOK == nil || *cb.Copies[0].VerifyOK {
		t.Fatalf("level B must mark the copy failed, got %+v", cb.Copies[0])
	}
}

// TestLevelBSatisfiesFullVerify confirms an intact mirror passes level B and the
// copy becomes B-verified (the only level that qualifies for protection).
func TestLevelBSatisfiesFullVerify(t *testing.T) {
	st, _ := OpenStore(t.TempDir())
	app := &App{DataDir: filepath.Dir(st.path), Store: st}
	coll := st.AddCollection("Mirrors")
	vol := st.AddVolume(Volume{Label: "MIR-02", Kind: "HDD"})
	medium := t.TempDir()
	p := filepath.Join(medium, "f.bin")
	data := []byte("hello mnemosyne — a small intact mirror file")
	os.WriteFile(p, data, 0o644)
	full, _ := hashFileHex(p)
	sample, _ := sampleHashHex(p)
	c := st.AddChunk(Chunk{CollectionID: coll.ID, Name: "MIRROR-M2", Status: StatusAdoptedVerified, Mirror: true,
		Files: []ChunkFileRef{{FileID: 1, RelPath: "f.bin", SizeBytes: int64(len(data)), Hash: full, SampleHash: sample}}})
	c.Copies = []Copy{{VolumeID: vol.ID, Path: medium}}
	st.UpdateChunk(c)

	res, err := app.VerifyMirrorVolume(vol.ID, medium, "B", func(float64, string) {})
	if err != nil {
		t.Fatalf("B verify: %v", err)
	}
	if res["all_ok"] != true {
		t.Fatalf("intact mirror should pass level B, got %v", res)
	}
	cc := st.Chunk(c.ID)
	if cc.Copies[0].VerifyOK == nil || !*cc.Copies[0].VerifyOK || cc.Copies[0].LastVerifiedAt == nil {
		t.Fatalf("level B pass should B-verify the copy, got %+v", cc.Copies[0])
	}
}
