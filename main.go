package main

// main.go — HTTP server + REST API + embedded UI. One binary, no installs.
//
//   go build -o mnemosyne .          (current OS)
//   CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mnemosyne.exe .

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
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
	listen := flag.String("listen", "127.0.0.1:7821", "listen address host:port. Default is localhost-only; use 0.0.0.0:7821 in a container (which then REQUIRES an auth token).")
	port := flag.Int("port", 0, "DEPRECATED: listen port on 127.0.0.1 (use -listen). When set, overrides the port of -listen.")
	dataDir := flag.String("data", defaultDataDir(), "data directory (catalog.json, config.json)")
	flag.Parse()

	addr := *listen
	if *port != 0 { // backward-compat: -port overrides just the port of -listen
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = "127.0.0.1"
		}
		addr = net.JoinHostPort(host, strconv.Itoa(*port))
	}

	store, err := OpenStore(*dataDir)
	if err != nil {
		log.Fatalf("open catalog: %v", err)
	}
	app := &App{DataDir: *dataDir, Store: store}

	// The bearer token: env MNEMO_AUTH_TOKEN wins (container-friendly), else the
	// config's auth_token. A non-localhost bind without a token is REFUSED — the
	// tool must never be reachable off-box unauthenticated.
	token := strings.TrimSpace(os.Getenv("MNEMO_AUTH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(app.LoadConfig().AuthToken)
	}
	if !isLocalhostAddr(addr) && token == "" {
		log.Fatalf("refusing to bind non-localhost address %q without an auth token.\n"+
			"Set a strong secret in one of:\n"+
			"  • env  MNEMO_AUTH_TOKEN=<token>   (recommended for containers)\n"+
			"  • config.json  \"auth_token\": \"<token>\"  in the data dir (%s)\n"+
			"…or bind localhost only (-listen 127.0.0.1:7821).", addr, *dataDir)
	}

	mux := http.NewServeMux()
	api(mux, app)

	sub, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	auth := "off (localhost only)"
	if token != "" {
		auth = "ON — Authorization: Bearer <token> required for /api"
	}
	log.Printf("Mnemosyne %s — http://%s  (data: %s · auth: %s)", appVersion, addr, *dataDir, auth)
	log.Fatal(http.ListenAndServe(addr, authMiddleware(mux, token)))
}

// isLocalhostAddr reports whether addr binds only the loopback interface, so it
// is unreachable from other machines and safe without a token.
func isLocalhostAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" { // empty host means "all interfaces" — NOT localhost
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authMiddleware gates every /api/ request behind a bearer token when one is set.
// The static UI (everything not under /api/) stays public so a browser can load
// it and prompt for the token. Browser-native GETs that cannot set a header
// (opening the label/QR/report in a new tab) may pass the token as ?token=.
// A blank token means localhost-only operation with no auth.
func authMiddleware(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			ok := subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) == 1
			if !ok {
				if q := r.URL.Query().Get("token"); q != "" {
					ok = subtle.ConstantTimeCompare([]byte(q), []byte(token)) == 1
				}
			}
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mnemosyne"`)
				jsonErr(w, http.StatusUnauthorized, fmt.Errorf("authorization required — send Authorization: Bearer <token>"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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

func bl(m map[string]any, k string) bool {
	v, _ := m[k].(bool)
	return v
}

// strList reads a JSON array of strings from body[key] into []string, trimming
// blanks. Used for a profile's allowed-media-kinds list.
func strList(m map[string]any, k string) []string {
	arr, _ := m[k].([]any)
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if sv, ok := v.(string); ok && strings.TrimSpace(sv) != "" {
			out = append(out, strings.TrimSpace(sv))
		}
	}
	return out
}

// intList reads a JSON array of numbers from body[key] into []int (JSON numbers
// decode as float64), dropping anything non-numeric.
func intList(m map[string]any, k string) []int {
	arr, _ := m[k].([]any)
	out := make([]int, 0, len(arr))
	for _, v := range arr {
		if fv, ok := v.(float64); ok {
			out = append(out, int(fv))
		}
	}
	return out
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
			Kind: s(nv, "kind"), Location: s(nv, "location"), Offsite: bl(nv, "offsite"), Notes: s(nv, "notes")})
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
	// Space advice — the single source of truth for "do I have room?" so the UI
	// never re-implements the staging math. See space.go.
	mux.HandleFunc("GET /api/space-advice", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cid, _ := strconv.Atoi(q.Get("collection_id"))
		chid, _ := strconv.Atoi(q.Get("chunk_id"))
		jsonOut(w, app.SpaceAdvice(cid, chid, q.Get("dest")))
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
	// The source folders scanned into an archive — the mirror dialog's folder picker.
	register(mux, "GET /api/collections/{id}/folders", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		files := app.Store.FilesOf(id)
		count := map[int]int{}
		var bytes = map[int]int64{}
		for _, f := range files {
			count[f.FolderID]++
			bytes[f.FolderID] += f.SizeBytes
		}
		out := []map[string]any{}
		for _, fo := range app.Store.FoldersOf(id) {
			out = append(out, map[string]any{"id": fo.ID, "path": fo.Path, "files": count[fo.ID], "bytes": bytes[fo.ID]})
		}
		jsonOut(w, out)
	})
	// Native MIRROR backup: copy an archive's files to one or more volumes as PLAIN
	// FILES (copy-then-verify each), one background job PER VOLUME so several
	// spinning drives fill concurrently. The complement to sealed packages.
	mux.HandleFunc("POST /api/mirror", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		cid := int(f(b, "collection_id"))
		coll := app.Store.Collection(cid)
		if coll == nil {
			jsonErr(w, 400, fmt.Errorf("collection_id required (the archive to mirror)"))
			return
		}
		folderIDs := intList(b, "folder_ids")
		throttle := f(b, "throttle_mbps")
		targets, _ := b["targets"].([]any)
		if len(targets) == 0 {
			jsonErr(w, 400, fmt.Errorf("at least one target {dest_dir, volume_id} required"))
			return
		}
		jobs := []map[string]any{}
		for _, t := range targets {
			tm, _ := t.(map[string]any)
			dest := s(tm, "dest_dir")
			if strings.TrimSpace(dest) == "" {
				jsonErr(w, 400, fmt.Errorf("each target needs a dest_dir"))
				return
			}
			vol := resolveVolume(app, tm) // volume_id or inline new_volume
			if vol <= 0 {
				jsonErr(w, 400, fmt.Errorf("each target needs a volume_id or new_volume"))
				return
			}
			label := fmt.Sprintf("Mirror %s → %s", coll.Name, dest)
			// One job per volume — they run concurrently (v1's multi-volume copy).
			resp := runJob(app, "mirror", label, func(p func(float64, string)) error {
				_, err := app.MirrorToVolume(cid, folderIDs, dest, vol, throttle, p)
				return err
			})
			resp["volume_id"] = vol
			resp["dest_dir"] = dest
			jobs = append(jobs, resp)
		}
		jsonOut(w, map[string]any{"jobs": jobs})
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
		v := app.Store.AddVolume(Volume{Label: s(b, "label"), Barcode: s(b, "barcode"),
			Kind: s(b, "kind"), Location: s(b, "location"), Offsite: bl(b, "offsite"), Notes: s(b, "notes")})
		// If the operator pointed us at where the drive is mounted, capture the
		// physical device identity now (best-effort; a masked serial is non-fatal).
		var detected *DeviceIdentity
		if mp := s(b, "mount_path"); strings.TrimSpace(mp) != "" {
			if id, changed := app.resolveVolumeIdentity(v, mp); changed {
				app.Store.UpdateVolume(v)
				detected = &id
			}
		}
		jsonOut(w, map[string]any{"volume": v, "detected": detected})
	})
	// Resolve (or refresh) a volume's physical device identity from a mounted path
	// — the "Detect drive identity" action for volumes registered by barcode alone.
	mux.HandleFunc("POST /api/volumes/{id}/identify", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		mp := s(body(r), "mount_path")
		if strings.TrimSpace(mp) == "" {
			jsonErr(w, 400, fmt.Errorf("mount_path required (where the drive is mounted, e.g. E:\\ or /mnt/disk)"))
			return
		}
		id, changed := app.resolveVolumeIdentity(v, mp)
		if changed {
			app.Store.UpdateVolume(v)
			app.Store.Log("volume", fmt.Sprintf("%s: device identity resolved (serial=%q model=%q)", v.Label, v.Serial, v.Model))
		}
		if !id.resolved() {
			jsonErr(w, 422, fmt.Errorf("could not resolve a physical device for %s (external docks/USB bridges can mask this; nothing changed)", mp))
			return
		}
		jsonOut(w, map[string]any{"volume": v, "detected": id, "changed": changed})
	})
	// Assign the next barcode from the configured scheme (prefix + counter) to a
	// volume that has none yet — used at label time so every label is scannable.
	mux.HandleFunc("POST /api/volumes/{id}/assign-barcode", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		if strings.TrimSpace(v.Barcode) != "" {
			jsonOut(w, map[string]any{"volume": v, "assigned": false, "barcode": v.Barcode})
			return
		}
		v.Barcode = app.Store.NextBarcode(app.LoadConfig().BarcodeScheme)
		app.Store.UpdateVolume(v)
		app.Store.Log("volume", fmt.Sprintf("%s: assigned barcode %s", v.Label, v.Barcode))
		jsonOut(w, map[string]any{"volume": v, "assigned": true, "barcode": v.Barcode})
	})
	// Preview the next barcode the scheme would assign (no mutation).
	mux.HandleFunc("GET /api/volumes/next-barcode", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"next": app.Store.NextBarcode(app.LoadConfig().BarcodeScheme)})
	})
	// Printable HTML label (opens in a new tab, print-ready at common sizes).
	mux.HandleFunc("GET /api/volumes/{id}/label", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			http.Error(w, "volume not found", 404)
			return
		}
		htmlPage, err := volumeLabelHTML(v, v.Barcode)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlPage))
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
		// smart_available drives whether the volume view shows the Media health card
		// with a "Check now" action or the install hint. The volume itself carries
		// its SMART snapshot history (Volume.SmartHistory).
		out := map[string]any{"volume": v, "chunks": rows, "smart_available": app.smartAvailable()}
		if !app.smartAvailable() {
			out["smart_hint"] = smartInstallHint
		}
		jsonOut(w, out)
	})
	// Read drive-mortality (SMART) signals for a volume from a mounted path and
	// record a snapshot in its history. Complements — never replaces — hash
	// verification. Never in a write path; timeouts + logged failures inside.
	mux.HandleFunc("POST /api/volumes/{id}/health", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		if !app.smartAvailable() {
			jsonOut(w, map[string]any{"available": false, "hint": smartInstallHint})
			return
		}
		mp := s(body(r), "mount_path")
		if strings.TrimSpace(mp) == "" {
			jsonErr(w, 400, fmt.Errorf("mount_path required (where the drive is mounted, e.g. E:\\ or /mnt/disk)"))
			return
		}
		snap, err := app.VolumeHealth(v, mp)
		if err != nil {
			// Silent-but-logged in VolumeHealth; surface a soft error to the UI.
			jsonOut(w, map[string]any{"available": true, "error": err.Error(), "history": v.SmartHistory})
			return
		}
		jsonOut(w, map[string]any{"available": true, "snapshot": snap, "history": v.SmartHistory})
	})

	// tape diagnostics — OPTIONAL, strictly outside the write path, read-only
	// toward the drive. Availability + the last snapshot for the "check before a
	// big write" nudge; a check that runs the detected tool on demand.
	mux.HandleFunc("GET /api/tape/status", func(w http.ResponseWriter, r *http.Request) {
		st := app.TapeToolStatus()
		st["last"] = app.Store.LastTapeCheck()
		jsonOut(w, st)
	})
	mux.HandleFunc("POST /api/tape/check", func(w http.ResponseWriter, r *http.Request) {
		if !app.TapeAvailable() {
			jsonOut(w, app.TapeToolStatus()) // {available:false, hints:[...]}
			return
		}
		th, err := app.TapeCheck(s(body(r), "device"))
		if err != nil {
			// Read-only + non-critical: surface a soft error, not a 500.
			jsonOut(w, map[string]any{"available": true, "error": err.Error(), "last": app.Store.LastTapeCheck()})
			return
		}
		jsonOut(w, map[string]any{"available": true, "snapshot": th})
	})

	// dock — guided, resumable ingest of a stack of legacy drives, one at a time
	dockView := func(ds *DockSession) map[string]any {
		if ds == nil {
			return map[string]any{"session": nil}
		}
		archives := []map[string]any{}
		for _, id := range ds.ArchiveIDs {
			if c := app.Store.Collection(id); c != nil {
				archives = append(archives, map[string]any{"id": c.ID, "name": c.Name})
			}
		}
		return map[string]any{"session": ds, "archives": archives, "coverage": app.archiveCoverage(ds.ArchiveIDs)}
	}
	mux.HandleFunc("POST /api/dock/sessions", func(w http.ResponseWriter, r *http.Request) {
		ds, err := app.StartDockSession(intList(body(r), "archive_ids"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, dockView(ds))
	})
	// The active session a reopened app resumes (Prompt: resumable across days).
	mux.HandleFunc("GET /api/dock/session", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, dockView(app.Store.ActiveDockSession()))
	})
	mux.HandleFunc("GET /api/dock/session/{id}", func(w http.ResponseWriter, r *http.Request) {
		ds := app.Store.DockSession(pathID(r))
		if ds == nil {
			jsonErr(w, 404, fmt.Errorf("dock session not found"))
			return
		}
		jsonOut(w, dockView(ds))
	})
	// Watcher poll: drives that have appeared since the session started.
	mux.HandleFunc("GET /api/dock/session/{id}/candidates", func(w http.ResponseWriter, r *http.Request) {
		cands, err := app.DockCandidates(pathID(r))
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		jsonOut(w, cands)
	})
	mux.HandleFunc("POST /api/dock/session/{id}/ingest", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		mount := s(b, "mount_path")
		if strings.TrimSpace(mount) == "" {
			jsonErr(w, 400, fmt.Errorf("mount_path (the docked drive) required"))
			return
		}
		serial, label, mode := s(b, "serial"), s(b, "label"), s(b, "mode")
		jsonOut(w, runJob(app, "dock", "Ingest "+mount, func(p func(float64, string)) error {
			_, err := app.IngestDrive(id, mount, serial, label, mode, p)
			return err
		}))
	})
	mux.HandleFunc("POST /api/dock/session/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		ds, err := app.CloseDockSession(pathID(r))
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		jsonOut(w, dockView(ds))
	})
	mux.HandleFunc("GET /api/dock/session/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		md, err := app.SessionReportMarkdown(pathID(r))
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=dock-session-%d.md", pathID(r)))
		_, _ = w.Write([]byte(md))
	})

	// ---- protection profiles + assignments + the six-status model ----------

	// Toggle a volume's onsite/offsite flag — the "1" in 3-2-1. Triggers a status
	// recompute because it can change the offsite dimension of every file whose
	// copies land on this medium; deltas ride back for the toast.
	mux.HandleFunc("POST /api/volumes/{id}/offsite", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		v.Offsite = bl(body(r), "offsite")
		app.Store.UpdateVolume(v)
		app.Store.Log("volume", fmt.Sprintf("%s: marked %s", v.Label, offsiteWord(v.Offsite)))
		jsonOut(w, map[string]any{"volume": v, "recompute": app.recomputeJob()})
	})

	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Store.Profiles())
	})
	mux.HandleFunc("POST /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		if strings.TrimSpace(s(b, "name")) == "" {
			jsonErr(w, 400, fmt.Errorf("name required"))
			return
		}
		p := app.Store.AddProfile(profileFromBody(b))
		app.Store.Log("profile", fmt.Sprintf("created custom profile %q (%d copies / %d kinds / %d offsite)", p.Name, p.RequiredCopies, p.RequiredDistinctMediaKinds, p.RequiredOffsiteCopies))
		jsonOut(w, p)
	})
	// Duplicate any profile (including a built-in) as an editable custom starting
	// point — the sanctioned way to base a custom profile on a shipped one.
	mux.HandleFunc("POST /api/profiles/{id}/duplicate", func(w http.ResponseWriter, r *http.Request) {
		src := app.Store.Profile(r.PathValue("id"))
		if src == nil {
			jsonErr(w, 404, fmt.Errorf("profile not found"))
			return
		}
		dup := *src
		dup.ID = ""
		dup.Builtin = false
		dup.Name = strings.TrimSpace(s(body(r), "name"))
		if dup.Name == "" {
			dup.Name = src.Name + " (copy)"
		}
		p := app.Store.AddProfile(dup)
		jsonOut(w, p)
	})
	mux.HandleFunc("PUT /api/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		p := profileFromBody(body(r))
		p.ID = r.PathValue("id")
		if err := app.Store.UpdateProfile(p); err != nil {
			jsonErr(w, 400, err)
			return
		}
		app.Store.Log("profile", fmt.Sprintf("edited profile %q", p.Name))
		// An edit can invalidate prior compliance anywhere the profile is assigned.
		jsonOut(w, map[string]any{"profile": app.Store.Profile(p.ID), "recompute": app.recomputeJob()})
	})
	mux.HandleFunc("DELETE /api/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := app.Store.DeleteProfile(r.PathValue("id")); err != nil {
			jsonErr(w, 409, err)
			return
		}
		jsonOut(w, map[string]any{"deleted": true})
	})

	// Assign a profile to an Archive (path "") or a folder path within it. An
	// empty profile_id clears the assignment (the node falls back to inheriting).
	register(mux, "POST /api/collections/{id}/assign", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		b := body(r)
		if err := app.Store.SetAssignment(id, s(b, "path"), s(b, "profile_id")); err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"ok": true, "recompute": app.recomputeJob()})
	})
	register(mux, "GET /api/collections/{id}/protection", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		res := app.Store.Protection(id)
		out := map[string]any{"protection": res, "assignments": app.Store.AssignmentsOf(id)}
		jsonOut(w, out)
	})

	// Dashboard-facing rollup of the persisted per-collection status summaries.
	mux.HandleFunc("GET /api/protection", func(w http.ResponseWriter, r *http.Request) {
		sums := app.Store.ProtectionSummaries()
		totals := map[string]int{}
		for _, sm := range sums {
			for st, n := range sm.Files {
				totals[st] += n
			}
		}
		jsonOut(w, map[string]any{"totals": totals, "summaries": sums})
	})
	mux.HandleFunc("POST /api/protection/recompute", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.recomputeJob())
	})

	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, app.Store.Jobs()) })
}

func offsiteWord(off bool) string {
	if off {
		return "offsite"
	}
	return "onsite"
}

// profileFromBody reads the editable profile fields from a request body.
func profileFromBody(b map[string]any) Profile {
	return Profile{
		Name:                       strings.TrimSpace(s(b, "name")),
		Description:                s(b, "description"),
		RequiredCopies:             int(f(b, "required_copies")),
		RequiredDistinctMediaKinds: int(f(b, "required_distinct_media_kinds")),
		RequiredOffsiteCopies:      int(f(b, "required_offsite_copies")),
		MediaKindsAllowed:          strList(b, "media_kinds_allowed"),
		VerifyDueMonths:            int(f(b, "verify_due_months")),
	}
}
