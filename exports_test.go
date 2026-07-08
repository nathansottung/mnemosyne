package main

// exports_test.go — export the hash-keyed knowledge from one catalog, import into a
// fresh one, and confirm search / locations / plan answers match. Exports carry
// paths + hashes but no file content, so this reconstructs KNOWLEDGE, not data.

import (
	"encoding/json"
	"testing"
	"time"
)

// searchFingerprint reduces a search result set to a comparable summary: per file,
// its path/hash/role/event and the set of "label (location)" homes.
func searchFingerprint(rows []map[string]any) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		rel, _ := r["rel_path"].(string)
		hash, _ := r["hash"].(string)
		role, _ := r["role"].(string)
		event, _ := r["event"].(string)
		homes := []string{}
		if cps, ok := r["copies"].([]map[string]any); ok {
			for _, c := range cps {
				lbl, _ := c["volume_label"].(string)
				loc, _ := c["location"].(string)
				homes = append(homes, lbl+"|"+loc)
			}
		}
		// stable
		for i := 0; i < len(homes); i++ {
			for j := i + 1; j < len(homes); j++ {
				if homes[j] < homes[i] {
					homes[i], homes[j] = homes[j], homes[i]
				}
			}
		}
		out[hash] = rel + "|" + role + "|" + event + "|" + joinList(homes)
	}
	return out
}

func TestExportImport_StructureAndPlanRoundTrip(t *testing.T) {
	// --- source catalog ---
	srcDir := t.TempDir()
	s1, err := OpenStore(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	src := &App{DataDir: srcDir, Store: s1}

	coll := src.Store.AddCollectionKind("Photos", ArchiveSourceless)
	loc := src.Store.AddLocation("Shoe Box #1", false, "")
	vol := src.Store.AddVolume(Volume{Label: "DRIVE-01", Serial: "S1", Kind: "HDD", LocationID: loc.ID, Location: "Shoe Box #1"})
	ev := src.Store.AddEvent(&Event{Name: "Henderson", EventType: "wedding", Year: 2019, CollectionID: coll.ID})
	shot := time.Date(2019, 6, 15, 10, 0, 0, 0, time.UTC)
	ids, _ := src.Store.UpsertUnionFiles(coll.ID, []unionFile{
		{RelPath: "a.jpg", Hash: "hashA", Size: 100, Role: RoleEditedExport, ShotAt: shot},
		{RelPath: "shared/s.jpg", Hash: "hashS", Size: 200, Role: RoleRAW},
	})
	src.Store.AssignFilesToEvent([]int{ids[0]}, ev.ID)
	src.upsertMirrorChunk(coll.ID, vol, "X:/", []ChunkFileRef{
		{FileID: ids[0], RelPath: "a.jpg", SizeBytes: 100, Hash: "hashA"},
		{FileID: ids[1], RelPath: "shared/s.jpg", SizeBytes: 200, Hash: "hashS"},
	}, "mirror")

	// A snapshot for the drive + a compiled plan (for the Plan Export).
	src.Store.PutVolumeSnapshot(&VolumeSnapshot{VolumeID: vol.ID, Serial: "S1", Label: "DRIVE-01",
		Files: []SnapFile{
			{RelPath: "a.jpg", Hash: "hashA", SizeBytes: 100, Role: RoleEditedExport},
			{RelPath: "shared/s.jpg", Hash: "hashS", SizeBytes: 200, Role: RoleRAW},
		}, TotalFiles: 2})
	tmpl := src.Store.AddTemplate(&Template{Name: "Move", Routes: map[string]string{
		RoleEditedExport: "photos/{orig_name}", RoleRAW: "photos/{orig_name}"}})
	plan := src.Store.AddPlan(&Plan{Name: "NAS Move", TemplateID: tmpl.ID, DestinationRoot: "/nas"})
	if rep, _ := src.CompilePlan(plan.ID); !rep.Compilable || rep.Files != 2 {
		t.Fatalf("source plan should compile 2 files, got %+v", rep)
	}

	// --- export (through JSON, as if emailed) ---
	structExp, err := src.StructureExport(coll.ID)
	if err != nil {
		t.Fatal(err)
	}
	planExp, err := src.PlanExport(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The export must carry NO content — only paths, hashes, locations.
	sjson := exportJSON(structExp)
	if len(structExp.Files) != 2 || structExp.Files[0].PlannedPath == "" {
		t.Fatalf("structure export should list 2 files with planned paths: %+v", structExp.Files)
	}
	var structRT StructureExport
	if err := json.Unmarshal(sjson, &structRT); err != nil {
		t.Fatal(err)
	}
	var planRT PlanExport
	if err := json.Unmarshal(exportJSON(planExp), &planRT); err != nil {
		t.Fatal(err)
	}

	// --- import into a FRESH catalog ---
	dstDir := t.TempDir()
	s2, err := OpenStore(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	dst := &App{DataDir: dstDir, Store: s2}
	sres, err := dst.ImportStructure(structRT)
	if err != nil {
		t.Fatal(err)
	}
	newCID := sres["archive_id"].(int)
	pres, err := dst.ImportPlan(planRT)
	if err != nil {
		t.Fatal(err)
	}

	// --- search / locations answers must match ---
	srcFP := searchFingerprint(src.Store.Search(SearchQuery{CollectionID: coll.ID, Limit: 1000}))
	dstFP := searchFingerprint(dst.Store.Search(SearchQuery{CollectionID: newCID, Limit: 1000}))
	if len(srcFP) != len(dstFP) || len(srcFP) != 2 {
		t.Fatalf("search sets differ in size: src %d dst %d", len(srcFP), len(dstFP))
	}
	for hash, want := range srcFP {
		if dstFP[hash] != want {
			t.Errorf("hash %s: search answer differs\n src: %s\n dst: %s", hash, want, dstFP[hash])
		}
	}
	// Event membership survived (hashA belongs to the Henderson event).
	if !containsSub(dstFP["hashA"], "Henderson") {
		t.Errorf("event membership lost on import: %q", dstFP["hashA"])
	}

	// --- plan answers must match ---
	newPlanID := pres["plan_id"].(int)
	np := dst.Store.Plan(newPlanID)
	if np == nil || np.Status != PlanCompiled {
		t.Fatalf("imported plan should be compiled, got %+v", np)
	}
	if len(np.Mapping) != len(plan.Mapping) {
		t.Errorf("plan mapping size differs: src %d dst %d", len(plan.Mapping), len(np.Mapping))
	}
	for h, dest := range plan.Mapping {
		if np.Mapping[h] != dest {
			t.Errorf("plan mapping for %s: src %q dst %q", h, dest, np.Mapping[h])
		}
	}
	srcCov, dstCov := src.PlanCoverage(plan), dst.PlanCoverage(np)
	if srcCov.Total != dstCov.Total || srcCov.Pct != dstCov.Pct {
		t.Errorf("plan coverage differs: src %+v dst %+v", srcCov, dstCov)
	}
	// The imported plan is executable on this machine: the drive (by serial) is known.
	if dst.Store.VolumeBySerial("S1") == nil {
		t.Error("plan import should reconstruct the source drive by serial for execution")
	}

	// CSV + Markdown render and carry the hashes + the no-content note.
	if !containsSub(string(StructureCSV(structExp)), "hashA") {
		t.Error("CSV render missing hash")
	}
	md := StructureMarkdown(structExp)
	if !containsSub(md, "NO file content") || !containsSub(md, "Henderson") {
		t.Errorf("markdown companion missing key content")
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
