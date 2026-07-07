package main

// main.go — HTTP server + REST API + embedded UI. One binary, no installs.
//
//   go build -o mnemosyne .          (current OS)
//   CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mnemosyne.exe .

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed ui
var uiFS embed.FS

// appVersion is overridden at release time via the linker flag
// -ldflags "-X main.appVersion=v2.1.0". It must stay a var (not a const) for the
// -X override to take effect. See .github/workflows/release.yml.
var appVersion = "2.0.0"

func main() {
	port := flag.Int("port", 7821, "listen port")
	dataDir := flag.String("data", defaultDataDir(), "data directory (catalog.json, config.json)")
	flag.Parse()

	store, err := OpenStore(*dataDir)
	if err != nil {
		log.Fatalf("open catalog: %v", err)
	}
	app := &App{DataDir: *dataDir, Store: store}

	mux := http.NewServeMux()
	api(mux, app)

	sub, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("Mnemosyne %s — http://%s  (data: %s)", appVersion, addr, *dataDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mnemo"
	}
	return filepath.Join(home, ".mnemo")
}

// ---- helpers ------------------------------------------------------------

func jsonOut(w http.ResponseWriter, v any) {
	// A nil slice marshals to `null`, which makes the browser blow up on
	// `.length`/`.map` for an empty list (e.g. an empty catalog). Emit `[]` so
	// every list endpoint is always a safe array.
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		v = []any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func body(r *http.Request) map[string]any {
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func s(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func f(m map[string]any, k string) float64 {
	v, _ := m[k].(float64)
	return v
}

func pathID(r *http.Request) int {
	id, _ := strconv.Atoi(r.PathValue("id"))
	return id
}

// resolveVolume returns the volume id for a write/burn/span request: an
// existing volume_id, or a freshly registered volume from an inline new_volume
// object, or 0 (which the write path maps to "(unregistered)").
func resolveVolume(app *App, b map[string]any) int {
	if id := int(f(b, "volume_id")); id > 0 {
		return id
	}
	if nv, ok := b["new_volume"].(map[string]any); ok && s(nv, "label") != "" {
		v := app.Store.AddVolume(Volume{Label: s(nv, "label"), Barcode: s(nv, "barcode"),
			Kind: s(nv, "kind"), Location: s(nv, "location"), Notes: s(nv, "notes")})
		return v.ID
	}
	return 0
}

// pathFree reports free bytes for the nearest existing ancestor of p, so it
// works for a destination folder that doesn't exist yet (e.g. a new burn dir).
func pathFree(p string) (int64, error) {
	for {
		if _, err := os.Stat(p); err == nil {
			return diskFree(p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return diskFree(p)
		}
		p = parent
	}
}

// runJob executes fn in a goroutine bound to a Job row the UI can poll.
func runJob(app *App, kind, label string, fn func(progress func(float64, string)) error) map[string]any {
	j := app.Store.NewJob(kind, label)
	go func() {
		prog := func(p float64, msg string) {
			l := label
			if msg != "" {
				l = label + " — " + msg
			}
			app.Store.SetJob(j.ID, p, l, "")
		}
		if err := fn(prog); err != nil {
			app.Store.SetJob(j.ID, -1, label+" — ERROR: "+err.Error(), "FAILED")
			return
		}
		app.Store.SetJob(j.ID, 1, "", "COMPLETED")
	}()
	return map[string]any{"job_id": j.ID, "status": "RUNNING", "label": label}
}

// ---- routes ---------------------------------------------------------------

// register binds a route and, for the OAIS-renamed resources, a deprecated-but-
// working alias: /api/archives -> collections handler, /api/packages -> chunks
// handler. Existing catalogs and scripts using the old paths keep functioning.
func register(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.HandleFunc(pattern, h)
	if strings.Contains(pattern, "/api/collections") {
		mux.HandleFunc(strings.Replace(pattern, "/api/collections", "/api/archives", 1), h)
	} else if strings.Contains(pattern, "/api/chunks") {
		mux.HandleFunc(strings.Replace(pattern, "/api/chunks", "/api/packages", 1), h)
	}
}

func api(mux *http.ServeMux, app *App) {
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"ok": true, "version": appVersion})
	})
	mux.HandleFunc("GET /api/preflight", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Preflight())
	})
	mux.HandleFunc("GET /api/media", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, MediaPresets) })
	mux.HandleFunc("GET /api/pathinfo", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			jsonErr(w, 400, fmt.Errorf("path query param required"))
			return
		}
		free, err := pathFree(p)
		out := map[string]any{"path": p, "free_bytes": free, "exists": err == nil}
		if err != nil {
			out["error"] = err.Error()
		}
		jsonOut(w, out)
	})

	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		cfg := app.LoadConfig()
		jsonOut(w, map[string]any{"config": cfg, "keystore_status": app.KeystoreStatus()})
	})
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := app.SaveConfig(body(r))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"config": cfg, "keystore_status": app.KeystoreStatus()})
	})

	// collections + scan
	register(mux, "GET /api/collections", func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			*Collection
			Files  int            `json:"files"`
			Bytes  int64          `json:"bytes"`
			Chunks int            `json:"chunks"`
			Drift  map[string]any `json:"drift,omitempty"`
		}
		var out []row
		for _, c := range app.Store.Collections() {
			fr := app.Store.FilesOf(c.ID)
			var b int64
			for _, x := range fr {
				b += x.SizeBytes
			}
			rw := row{Collection: c, Files: len(fr), Bytes: b, Chunks: len(app.Store.Chunks(c.ID))}
			if d := app.Store.DriftReport(c.ID); d != nil {
				info := d.InfoCounts["new"] + d.InfoCounts["modified"] + d.InfoCounts["missing"] + d.InfoCounts["moved"]
				rw.Drift = map[string]any{"at": d.At, "changes": d.Changes(), "informational": info}
			}
			out = append(out, rw)
		}
		jsonOut(w, out)
	})
	register(mux, "POST /api/collections", func(w http.ResponseWriter, r *http.Request) {
		name := s(body(r), "name")
		if name == "" {
			jsonErr(w, 400, fmt.Errorf("name required"))
			return
		}
		jsonOut(w, app.Store.AddCollection(name))
	})
	register(mux, "POST /api/collections/{id}/scan", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		root := s(body(r), "path")
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		if root == "" {
			jsonErr(w, 400, fmt.Errorf("path required"))
			return
		}
		jsonOut(w, runJob(app, "scan", "Scan "+root, func(p func(float64, string)) error {
			_, err := app.ScanFolder(id, root, p)
			return err
		}))
	})
	register(mux, "POST /api/collections/{id}/reconcile", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		c := app.Store.Collection(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		jsonOut(w, runJob(app, "reconcile", "Rescan & compare "+c.Name, func(p func(float64, string)) error {
			_, err := app.ReconcileCollection(id, p)
			return err
		}))
	})
	register(mux, "GET /api/collections/{id}/drift", func(w http.ResponseWriter, r *http.Request) {
		d := app.Store.DriftReport(pathID(r))
		if d == nil {
			jsonErr(w, 404, fmt.Errorf("no reconcile report yet — run Rescan & compare"))
			return
		}
		jsonOut(w, d)
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Store.Search(r.URL.Query().Get("q"), 200))
	})

	// planning + chunks
	mux.HandleFunc("POST /api/plan", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		encrypted := true // default ON; missing/omitted stays encrypted
		if v, ok := b["encrypted"].(bool); ok {
			encrypted = v
		}
		res, err := app.Plan(int(f(b, "collection_id")), s(b, "media_kind"), f(b, "target_gb"), int(f(b, "par2_redundancy")), encrypted)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, res)
	})
	register(mux, "GET /api/chunks", func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.Atoi(r.URL.Query().Get("collection_id"))
		jsonOut(w, app.Store.Chunks(cid))
	})
	register(mux, "GET /api/chunks/{id}", func(w http.ResponseWriter, r *http.Request) {
		c := app.Store.Chunk(pathID(r))
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		jsonOut(w, c)
	})
	register(mux, "POST /api/chunks/{id}/build", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		jsonOut(w, runJob(app, "build", "Build "+c.Name, func(p func(float64, string)) error {
			return app.BuildChunk(id, p)
		}))
	})
	register(mux, "POST /api/chunks/{id}/write", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		dest := s(b, "dest_dir")
		if dest == "" {
			jsonErr(w, 400, fmt.Errorf("dest_dir required (LTFS mount, archive drive, burn folder)"))
			return
		}
		vol := resolveVolume(app, b)
		jsonOut(w, runJob(app, "write", "Write "+c.Name+" → "+dest, func(p func(float64, string)) error {
			_, err := app.WriteChunk(id, dest, f(b, "buffer_gb"), int(f(b, "block_mb")), f(b, "throttle_mbps"), vol, p)
			return err
		}))
	})
	register(mux, "POST /api/chunks/{id}/rewrite-copy", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		vol := int(f(b, "volume_id"))
		if vol <= 0 {
			jsonErr(w, 400, fmt.Errorf("volume_id required (the volume whose copy to re-write)"))
			return
		}
		jsonOut(w, runJob(app, "write", "Re-write "+c.Name+" copy", func(p func(float64, string)) error {
			_, err := app.RewriteCopy(id, vol, f(b, "buffer_gb"), int(f(b, "block_mb")), f(b, "throttle_mbps"), p)
			return err
		}))
	})
	register(mux, "POST /api/chunks/{id}/span-write", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		dest := s(b, "dest_dir")
		if dest == "" {
			jsonErr(w, 400, fmt.Errorf("dest_dir required (the mounted tape/drive for this segment)"))
			return
		}
		vol := resolveVolume(app, b)
		jsonOut(w, runJob(app, "write", "Span-write next segment of "+c.Name+" → "+dest, func(p func(float64, string)) error {
			_, err := app.SpanWriteNext(id, dest, f(b, "buffer_gb"), int(f(b, "block_mb")), f(b, "throttle_mbps"), vol, p)
			return err
		}))
	})
	register(mux, "POST /api/chunks/{id}/verify", func(w http.ResponseWriter, r *http.Request) {
		res, err := app.VerifyChunk(pathID(r), s(body(r), "dest_dir"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, res)
	})
	register(mux, "POST /api/chunks/{id}/read-medium-manifest", func(w http.ResponseWriter, r *http.Request) {
		m, err := app.ReadMediumManifest(pathID(r), s(body(r), "mount"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, m)
	})
	mux.HandleFunc("POST /api/verify-campaign", func(w http.ResponseWriter, r *http.Request) {
		dest := s(body(r), "dest_dir")
		if dest == "" {
			jsonErr(w, 400, fmt.Errorf("dest_dir required (mounted tape/disc or archive folder)"))
			return
		}
		jsonOut(w, runJob(app, "verify", "Verify campaign — "+dest, func(p func(float64, string)) error {
			_, err := app.VerifyCampaign(dest, p)
			return err
		}))
	})
	mux.HandleFunc("POST /api/adopt", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		mount := s(b, "mount_path")
		if mount == "" {
			jsonErr(w, 400, fmt.Errorf("mount_path required (the mounted legacy medium)"))
			return
		}
		cid := int(f(b, "collection_id"))
		if app.Store.Collection(cid) == nil {
			jsonErr(w, 400, fmt.Errorf("collection_id required (the archive to adopt into)"))
			return
		}
		vol := resolveVolume(app, b)
		deep, _ := b["deep"].(bool)
		// Adoption result (adopted / skipped-duplicate / unreadable) is surfaced via
		// the job's final label and the refreshed Packages/Volumes views.
		jsonOut(w, runJob(app, "adopt", "Adopt media — "+mount, func(p func(float64, string)) error {
			_, err := app.AdoptMedia(mount, cid, vol, deep, p)
			return err
		}))
	})
	register(mux, "POST /api/chunks/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		out := s(b, "output_dir")
		if out == "" {
			jsonErr(w, 400, fmt.Errorf("output_dir required"))
			return
		}
		var members []string
		if arr, ok := b["members"].([]any); ok {
			for _, m := range arr {
				if ms, ok := m.(string); ok {
					members = append(members, ms)
				}
			}
		}
		jsonOut(w, runJob(app, "restore", "Restore "+c.Name, func(p func(float64, string)) error {
			_, err := app.RestoreChunk(id, s(b, "source_dir"), out, members, p)
			return err
		}))
	})

	// keys
	mux.HandleFunc("GET /api/keys", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"status": app.KeystoreStatus(), "keys": app.Store.KeyMetas()})
	})
	mux.HandleFunc("POST /api/keys", func(w http.ResponseWriter, r *http.Request) {
		ref, pass, fpr, err := app.GenerateKey(s(body(r), "note"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"key_ref": ref, "passphrase": pass, "fingerprint": fpr})
	})
	mux.HandleFunc("POST /api/keys/sync", func(w http.ResponseWriter, r *http.Request) {
		n, err := app.SyncKeystores()
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"key_count": n})
	})
	mux.HandleFunc("GET /api/keys/{ref}/qr.png", func(w http.ResponseWriter, r *http.Request) {
		ref := r.PathValue("ref")
		pass, err := app.Passphrase(ref)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		png, err := qrcode.Encode("MNEMO1|"+ref+"|"+pass, qrcode.Medium, 320)
		if err != nil {
			jsonErr(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})

	// burn queues — persistent, reboot-resumable optical burning
	mux.HandleFunc("GET /api/burnqueues", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Store.BurnQueues())
	})
	mux.HandleFunc("POST /api/burnqueues", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		q, err := app.CreateBurnQueue(int(f(b, "collection_id")), s(b, "media_kind"), s(b, "name"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, q)
	})
	mux.HandleFunc("POST /api/burnqueues/{id}/next", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		q := app.Store.BurnQueue(id)
		if q == nil {
			jsonErr(w, 404, fmt.Errorf("burn queue not found"))
			return
		}
		jsonOut(w, runJob(app, "burn", "Burn next disc in "+q.Name, func(p func(float64, string)) error {
			_, err := app.BurnNext(id, p)
			return err
		}))
	})
	mux.HandleFunc("POST /api/burnqueues/{id}/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := app.ResetBurnQueue(pathID(r))
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		jsonOut(w, map[string]any{"reset": n})
	})

	// recovery kit — a self-contained "restore decades from now" export
	mux.HandleFunc("POST /api/recoverykit", func(w http.ResponseWriter, r *http.Request) {
		out := s(body(r), "output_dir")
		if out == "" {
			jsonErr(w, 400, fmt.Errorf("output_dir required"))
			return
		}
		resp := runJob(app, "recoverykit", "Recovery Kit → "+out, func(p func(float64, string)) error {
			_, err := app.BuildRecoveryKit(out, p)
			return err
		})
		resp["warning"] = recoveryKitWarning
		jsonOut(w, resp)
	})

	// volumes — physical media the operator can hold + locate
	mux.HandleFunc("GET /api/volumes", func(w http.ResponseWriter, r *http.Request) {
		vols := app.Store.Volumes()
		type agg struct {
			chunks map[int]bool
			bytes  int64
			last   *time.Time
		}
		st := map[int]*agg{}
		for _, v := range vols {
			st[v.ID] = &agg{chunks: map[int]bool{}}
		}
		for _, c := range app.Store.Chunks(0) {
			for _, cp := range c.Copies {
				if cp.Superseded {
					continue // history of a rewritten medium, not a live copy
				}
				if a := st[cp.VolumeID]; a != nil {
					if !a.chunks[c.ID] {
						a.chunks[c.ID] = true
						a.bytes += c.EncBytes
					}
					if cp.LastVerifiedAt != nil && (a.last == nil || cp.LastVerifiedAt.After(*a.last)) {
						a.last = cp.LastVerifiedAt
					}
				}
			}
			for _, sg := range c.Segments {
				if a := st[sg.VolumeID]; sg.VolumeID != 0 && a != nil {
					if !a.chunks[c.ID] {
						a.chunks[c.ID] = true
					}
					a.bytes += sg.Bytes
				}
			}
		}
		out := make([]map[string]any, 0, len(vols))
		for _, v := range vols {
			a := st[v.ID]
			out = append(out, map[string]any{"volume": v, "chunk_count": len(a.chunks),
				"total_bytes": a.bytes, "last_verified_at": a.last})
		}
		jsonOut(w, out)
	})
	mux.HandleFunc("POST /api/volumes", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		if s(b, "label") == "" {
			jsonErr(w, 400, fmt.Errorf("label required"))
			return
		}
		jsonOut(w, app.Store.AddVolume(Volume{Label: s(b, "label"), Barcode: s(b, "barcode"),
			Kind: s(b, "kind"), Location: s(b, "location"), Notes: s(b, "notes")}))
	})
	mux.HandleFunc("GET /api/volumes/by-barcode", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.VolumeByBarcode(r.URL.Query().Get("barcode"))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("no volume with barcode %q", r.URL.Query().Get("barcode")))
			return
		}
		jsonOut(w, v)
	})
	mux.HandleFunc("GET /api/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		var rows []map[string]any
		for _, c := range app.Store.Chunks(0) {
			on := false
			row := map[string]any{"id": c.ID, "name": c.Name, "status": c.Status, "bytes": c.EncBytes,
				"spanned": c.Spanned, "file_count": c.FileCount, "encrypted": c.Encrypted, "private_manifest": c.PrivateManifest}
			for _, cp := range c.Copies {
				if cp.VolumeID == v.ID && !cp.Superseded {
					on = true
					row["path"], row["last_verified_at"], row["verify_ok"] = cp.Path, cp.LastVerifiedAt, cp.VerifyOK
				}
			}
			for _, sg := range c.Segments {
				if sg.VolumeID == v.ID {
					on = true
					row["holds_segment"] = sg.Index
				}
			}
			if !on {
				continue
			}
			files := make([]string, 0, len(c.Files))
			for _, cf := range c.Files {
				files = append(files, cf.RelPath)
			}
			row["files"] = files
			rows = append(rows, row)
		}
		jsonOut(w, map[string]any{"volume": v, "chunks": rows})
	})

	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, app.Store.Jobs()) })
}
