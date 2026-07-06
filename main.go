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
	"strconv"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed ui
var uiFS embed.FS

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

func api(mux *http.ServeMux, app *App) {
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"ok": true, "version": appVersion})
	})
	mux.HandleFunc("GET /api/preflight", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Preflight())
	})
	mux.HandleFunc("GET /api/media", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, MediaPresets) })

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
	mux.HandleFunc("GET /api/collections", func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			*Collection
			Files  int   `json:"files"`
			Bytes  int64 `json:"bytes"`
			Chunks int   `json:"chunks"`
		}
		var out []row
		for _, c := range app.Store.Collections() {
			fr := app.Store.FilesOf(c.ID)
			var b int64
			for _, x := range fr {
				b += x.SizeBytes
			}
			out = append(out, row{c, len(fr), b, len(app.Store.Chunks(c.ID))})
		}
		jsonOut(w, out)
	})
	mux.HandleFunc("POST /api/collections", func(w http.ResponseWriter, r *http.Request) {
		name := s(body(r), "name")
		if name == "" {
			jsonErr(w, 400, fmt.Errorf("name required"))
			return
		}
		jsonOut(w, app.Store.AddCollection(name))
	})
	mux.HandleFunc("POST /api/collections/{id}/scan", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		root := s(body(r), "path")
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("collection not found"))
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
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Store.Search(r.URL.Query().Get("q"), 200))
	})

	// planning + chunks
	mux.HandleFunc("POST /api/plan", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		res, err := app.Plan(int(f(b, "collection_id")), s(b, "media_kind"), f(b, "target_gb"), int(f(b, "par2_redundancy")))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, res)
	})
	mux.HandleFunc("GET /api/chunks", func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.Atoi(r.URL.Query().Get("collection_id"))
		jsonOut(w, app.Store.Chunks(cid))
	})
	mux.HandleFunc("GET /api/chunks/{id}", func(w http.ResponseWriter, r *http.Request) {
		c := app.Store.Chunk(pathID(r))
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("chunk not found"))
			return
		}
		jsonOut(w, c)
	})
	mux.HandleFunc("POST /api/chunks/{id}/build", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("chunk not found"))
			return
		}
		jsonOut(w, runJob(app, "build", "Build "+c.Name, func(p func(float64, string)) error {
			return app.BuildChunk(id, p)
		}))
	})
	mux.HandleFunc("POST /api/chunks/{id}/write", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("chunk not found"))
			return
		}
		dest := s(b, "dest_dir")
		if dest == "" {
			jsonErr(w, 400, fmt.Errorf("dest_dir required (LTFS mount, archive drive, burn folder)"))
			return
		}
		jsonOut(w, runJob(app, "write", "Write "+c.Name+" → "+dest, func(p func(float64, string)) error {
			_, err := app.WriteChunk(id, dest, f(b, "buffer_gb"), int(f(b, "block_mb")), p)
			return err
		}))
	})
	mux.HandleFunc("POST /api/chunks/{id}/verify", func(w http.ResponseWriter, r *http.Request) {
		res, err := app.VerifyChunk(pathID(r), s(body(r), "dest_dir"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, res)
	})
	mux.HandleFunc("POST /api/chunks/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		c := app.Store.Chunk(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("chunk not found"))
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

	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, app.Store.Jobs()) })
}
