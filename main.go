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

// appVersion is the SINGLE SOURCE OF TRUTH for the version, surfaced everywhere
// (startup banner, /api/health, About/escrow status, BagIt Bag-Software-Agent,
// package manifests, Recovery Kit). It is injected at release time from the git tag
// via the linker flag -ldflags "-X main.appVersion=v0.9.0" (see
// .github/workflows/release.yml and the Dockerfile). It MUST stay a var (not a
// const) for the -X override to take effect. The in-repo default marks any
// non-release build as a development build of the upcoming release.
var appVersion = "0.9.0-dev"

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
	if ro, why := store.ReadOnly(); ro {
		log.Printf("⚠ READ-ONLY: %s", why)
	}

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

// parseAsOf accepts a date (2006-01-02) or a full RFC3339 timestamp for the
// "restore the version current as of <when>" selector. A bare date is taken as
// the END of that day (23:59:59 UTC) so "as of 2024-03-12" includes anything
// written that day.
func parseAsOf(sv string) (time.Time, error) {
	sv = strings.TrimSpace(sv)
	if t, err := time.Parse(time.RFC3339, sv); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", sv); err == nil {
		return t.Add(24*time.Hour - time.Second).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("as_of %q: use YYYY-MM-DD or an RFC3339 timestamp", sv)
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

// parseSearchDate parses a capture-date filter: a plain date (2019-10-01) or a full
// RFC3339. endOfDay pushes a bare date to 23:59:59 so a "to" bound covers the whole
// day. Returns the zero time on empty/unparseable input (the filter is then off).
func parseSearchDate(v string, endOfDay bool) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		if endOfDay {
			return t.Add(24*time.Hour - time.Second)
		}
		return t
	}
	return time.Time{}
}

// sealGuard refuses a write targeting a SEALED (finalized, vault-ready) volume.
// A newly-registered inline volume (id 0 here, created by resolveVolume) is never
// sealed, so only an explicit existing volume_id can trip this.
func sealGuard(app *App, volumeID int) error {
	if volumeID <= 0 {
		return nil
	}
	if v := app.Store.Volume(volumeID); v != nil && v.Sealed {
		return fmt.Errorf("volume %q is SEALED (vault-ready) — unseal it first to write; unseal is audit-logged", v.Label)
	}
	return nil
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

// progBytes formats a progress message that also carries byte counters for live
// telemetry: runJob parses the "\x1f<done>\x1f<total>\x1f<human>" prefix into
// MB/s + ETA and displays only the human tail. Byte-moving jobs (write/mirror/
// span) emit this so the job row shows throughput, not just a percent.
// progStats encodes structured live telemetry into a progress message: byte
// counters (for throughput/ETA), file counters (for "X / Y files"), and a human
// step label — delimited by the unit-separator control char so runJob can parse
// them out. A plain (unencoded) message is shown verbatim. progBytes is the
// byte-only wrapper; count-based jobs pass 0 bytes and real file counts.
func progStats(bytesDone, bytesTotal, filesDone, filesTotal int64, human string) string {
	return fmt.Sprintf("\x1f%d\x1f%d\x1f%d\x1f%d\x1f%s", bytesDone, bytesTotal, filesDone, filesTotal, human)
}

func progBytes(done, total int64, human string) string {
	return progStats(done, total, 0, 0, human)
}

// runJob executes fn in a goroutine bound to a Job row the UI can poll. fn's
// returned map is captured as the job's Result so the UI can show the artifact.
func runJob(app *App, kind, label string, fn func(progress func(float64, string)) (map[string]any, error)) map[string]any {
	j := app.Store.NewJob(kind, label)
	go func() {
		start := time.Now()
		var lastDone int64
		var lastT time.Time
		var lastRate float64
		prog := func(p float64, msg string) {
			done, total := int64(0), int64(0) // bytes → throughput + ETA
			var filesDone, filesTotal int64
			human := msg
			if strings.HasPrefix(msg, "\x1f") {
				if parts := strings.SplitN(msg, "\x1f", 6); len(parts) == 6 {
					done, _ = strconv.ParseInt(parts[1], 10, 64)
					total, _ = strconv.ParseInt(parts[2], 10, 64)
					filesDone, _ = strconv.ParseInt(parts[3], 10, 64)
					filesTotal, _ = strconv.ParseInt(parts[4], 10, 64)
					human = parts[5]
				} else if p2 := strings.SplitN(msg, "\x1f", 4); len(p2) == 4 { // legacy bytes-only
					done, _ = strconv.ParseInt(p2[1], 10, 64)
					total, _ = strconv.ParseInt(p2[2], 10, 64)
					human = p2[3]
				}
			}
			l := label
			if human != "" {
				l = label + " — " + human
			}
			app.Store.SetJob(j.ID, p, l, "")
			// Telemetry: a recent-window MB/s from byte deltas, ETA from what's left.
			var rate, eta float64
			now := time.Now()
			if total > 0 {
				if lastT.IsZero() {
					lastDone, lastT = done, now
				} else if dt := now.Sub(lastT).Seconds(); dt >= 0.5 {
					if r := float64(done-lastDone) / 1e6 / dt; r >= 0 {
						lastRate = round1(r)
					}
					lastDone, lastT = done, now
				}
				rate = lastRate
				if rate > 0 && total > done {
					eta = float64(total-done) / (rate * 1e6)
				}
			} else if p > 0.01 && p < 1 {
				eta = time.Since(start).Seconds() * (1 - p) / p
			}
			app.Store.SetJobTelemetry(j.ID, rate, eta, done, total, filesDone, filesTotal)
		}
		res, err := fn(prog)
		if err != nil {
			app.Store.SetJob(j.ID, -1, label+" — ERROR: "+err.Error(), "FAILED")
			app.Store.SetJobTelemetry(j.ID, 0, 0, 0, 0, 0, 0)
			return
		}
		app.Store.SetJob(j.ID, 1, "", "COMPLETED")
		app.Store.SetJobResult(j.ID, res)
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
		out := map[string]any{"ok": true, "version": appVersion, "schema_version": currentSchemaVersion}
		if ro, why := app.Store.ReadOnly(); ro {
			out["read_only"], out["read_only_reason"] = true, why
		}
		jsonOut(w, out)
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

	// Read-only folder browser for the path picker (lists subdirectories only).
	mux.HandleFunc("GET /api/browse", func(w http.ResponseWriter, r *http.Request) {
		res, err := app.Browse(r.URL.Query().Get("path"))
		if err != nil {
			jsonErr(w, 400, fmt.Errorf("cannot read folder: %w", err))
			return
		}
		jsonOut(w, res)
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
	// Integrity presets — unify the assurance knobs into ARCHIVAL/BALANCED/FAST,
	// globally or per archive. Individual knobs stay editable (→ "Custom").
	mux.HandleFunc("GET /api/integrity", func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.Atoi(r.URL.Query().Get("collection_id"))
		jsonOut(w, app.integrityView(cid))
	})
	mux.HandleFunc("PUT /api/integrity", func(w http.ResponseWriter, r *http.Request) {
		iv, err := app.applyGlobalIntegrity(body(r))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		app.Store.Log("integrity", fmt.Sprintf("global → %s (build_verify=%s, par2=%d%%, routine=%s, due=%dmo)",
			iv.Preset, iv.BuildVerify, iv.Par2Redundancy, iv.RoutineVerifyLevel, iv.VerifyDueMonths))
		jsonOut(w, app.integrityView(0))
	})
	register(mux, "GET /api/collections/{id}/integrity", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		jsonOut(w, app.integrityView(id))
	})
	register(mux, "PUT /api/collections/{id}/integrity", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		c := app.Store.Collection(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		iv, err := app.applyArchiveIntegrity(id, body(r))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		app.Store.Log("integrity", fmt.Sprintf("%s → %s (build_verify=%s, par2=%d%%)", c.Name, iv.Preset, iv.BuildVerify, iv.Par2Redundancy))
		jsonOut(w, app.integrityView(id))
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
		b := body(r)
		name := s(b, "name")
		if name == "" {
			jsonErr(w, 400, fmt.Errorf("name required"))
			return
		}
		// The one plain question at create time: sourceless (scattered drives) or
		// sourced (one main place / NAS). Accept either kind or a sourceless bool.
		kind := ArchiveSourced
		if strings.EqualFold(s(b, "kind"), ArchiveSourceless) || bl(b, "sourceless") {
			kind = ArchiveSourceless
		}
		jsonOut(w, app.Store.AddCollectionKind(name, kind))
	})
	register(mux, "POST /api/collections/{id}/scan", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		root := s(body(r), "path")
		c := app.Store.Collection(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		if c.IsSourceless() {
			jsonErr(w, 400, fmt.Errorf("%q is a sourceless archive — it has no source folder to scan. Its files come from the media you adopt into it (Adopt a drive…)", c.Name))
			return
		}
		if root == "" {
			jsonErr(w, 400, fmt.Errorf("path required"))
			return
		}
		jsonOut(w, runJob(app, "scan", "Scan "+root, func(p func(float64, string)) (map[string]any, error) {
			n, err := app.ScanFolder(id, root, p)
			return map[string]any{"files": n}, err
		}))
	})
	// Adopt a folder-"drive" of loose files into a SOURCELESS archive (union by hash).
	register(mux, "POST /api/collections/{id}/adopt-folder", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		c := app.Store.Collection(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		b := body(r)
		path := s(b, "path")
		if path == "" {
			jsonErr(w, 400, fmt.Errorf("path (the drive/folder) required"))
			return
		}
		vol := resolveVolume(app, b)
		jsonOut(w, runJob(app, "adopt-folder", "Adopt drive → "+c.Name, func(p func(float64, string)) (map[string]any, error) {
			return app.AdoptFolder(path, id, vol, p)
		}))
	})
	register(mux, "POST /api/collections/{id}/reconcile", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		c := app.Store.Collection(id)
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		if c.IsSourceless() {
			jsonErr(w, 400, fmt.Errorf("%q is a sourceless archive — there is no source folder to reconcile against; its truth is the union of adopted media", c.Name))
			return
		}
		jsonOut(w, runJob(app, "reconcile", "Rescan & compare "+c.Name, func(p func(float64, string)) (map[string]any, error) {
			d, err := app.ReconcileCollection(id, p)
			var res map[string]any
			if d != nil {
				res = map[string]any{"collection_id": id, "changes": d.Changes()}
			}
			return res, err
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
			if err := sealGuard(app, vol); err != nil {
				jsonErr(w, 409, err)
				return
			}
			label := fmt.Sprintf("Mirror %s → %s", coll.Name, dest)
			// One job per volume — they run concurrently (v1's multi-volume copy).
			resp := runJob(app, "mirror", label, func(p func(float64, string)) (map[string]any, error) {
				mr, err := app.MirrorToVolume(cid, folderIDs, dest, vol, throttle, p)
				var res map[string]any
				if mr != nil {
					res = map[string]any{"mirrored": mr.Mirrored, "bytes": mr.Bytes, "sidecar": mr.Sidecar, "failed": mr.Failed}
				}
				return res, err
			})
			resp["volume_id"] = vol
			resp["dest_dir"] = dest
			jobs = append(jobs, resp)
		}
		jsonOut(w, map[string]any{"jobs": jobs})
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cid, _ := strconv.Atoi(q.Get("collection_id"))
		eid, _ := strconv.Atoi(q.Get("event_id")) // >0 = that event; -1 = only unassigned
		sq := SearchQuery{
			Text: q.Get("q"), Hash: q.Get("hash"), Ext: q.Get("ext"),
			Status: q.Get("status"), CollectionID: cid, Limit: 200,
			Role: q.Get("role"), EventID: eid,
		}
		// Date filters accept a plain date (2019-10-01) or full RFC3339. "To" without
		// a time covers the whole day.
		sq.From = parseSearchDate(q.Get("from"), false)
		sq.To = parseSearchDate(q.Get("to"), true)
		jsonOut(w, app.Store.Search(sq))
	})

	// ---- Events -----------------------------------------------------------
	// List events (optionally scoped to an archive) with their protection rollup.
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.Atoi(r.URL.Query().Get("collection_id"))
		jsonOut(w, map[string]any{"events": app.Store.Events(cid), "rollups": app.Store.EventRollups(cid)})
	})
	// Create one event (manual, or a confirmed harvested/clustered proposal).
	mux.HandleFunc("POST /api/events", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		name := strings.TrimSpace(s(b, "name"))
		if name == "" {
			jsonErr(w, 400, fmt.Errorf("event name required"))
			return
		}
		ev := app.Store.AddEvent(&Event{
			Name: name, EventType: s(b, "event_type"), Year: int(f(b, "year")),
			CaptureStart: parseSearchDate(s(b, "capture_start"), false),
			CaptureEnd:   parseSearchDate(s(b, "capture_end"), true),
			Notes:        s(b, "notes"), CollectionID: int(f(b, "collection_id")),
			FolderHint: s(b, "folder_hint"), Source: nonEmpty(s(b, "source"), "MANUAL"),
		})
		if ids := intList(b, "file_ids"); len(ids) > 0 { // clustered proposal carries its members
			app.Store.AssignFilesToEvent(ids, ev.ID)
		}
		jsonOut(w, ev)
	})
	// Save a batch of proposed events (from clustering or inference harvest).
	mux.HandleFunc("POST /api/events/batch", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			CollectionID int             `json:"collection_id"`
			Source       string          `json:"source"`
			Events       []ProposedEvent `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			jsonErr(w, 400, err)
			return
		}
		created := []*Event{}
		for _, pe := range in.Events {
			if strings.TrimSpace(pe.Name) == "" {
				continue
			}
			ev := app.Store.AddEvent(&Event{
				Name: pe.Name, EventType: pe.EventType, Year: pe.Year,
				CaptureStart: pe.CaptureStart, CaptureEnd: pe.CaptureEnd,
				CollectionID: in.CollectionID, FolderHint: pe.FolderHint,
				Source: nonEmpty(in.Source, "CLUSTERED"),
			})
			if len(pe.FileIDs) > 0 {
				app.Store.AssignFilesToEvent(pe.FileIDs, ev.ID)
			}
			created = append(created, ev)
		}
		jsonOut(w, map[string]any{"created": created})
	})
	// Cluster a (chaotic) archive's dated files into proposed events by EXIF density.
	mux.HandleFunc("POST /api/events/cluster", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		cid := int(f(b, "collection_id"))
		if cid == 0 {
			jsonErr(w, 400, fmt.Errorf("collection_id required"))
			return
		}
		props := app.ClusterEvents(cid, int(f(b, "min_files")), int(f(b, "span_days")))
		jsonOut(w, map[string]any{"proposed": props, "count": len(props)})
	})
	// Event detail: the record, its rollup, and its member files (with locations).
	mux.HandleFunc("GET /api/events/{id}", func(w http.ResponseWriter, r *http.Request) {
		ev := app.Store.Event(pathID(r))
		if ev == nil {
			jsonErr(w, 404, fmt.Errorf("event not found"))
			return
		}
		files := app.Store.Search(SearchQuery{EventID: ev.ID, Limit: 100000})
		var roll *EventRollup
		for _, rr := range app.Store.EventRollups(ev.CollectionID) {
			if rr.EventID == ev.ID {
				r2 := rr
				roll = &r2
				break
			}
		}
		jsonOut(w, map[string]any{"event": ev, "rollup": roll, "files": files})
	})
	// Update an event's editable fields.
	mux.HandleFunc("POST /api/events/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		ev := app.Store.Event(pathID(r))
		if ev == nil {
			jsonErr(w, 404, fmt.Errorf("event not found"))
			return
		}
		b := body(r)
		if v := strings.TrimSpace(s(b, "name")); v != "" {
			ev.Name = v
		}
		if _, ok := b["event_type"]; ok {
			ev.EventType = s(b, "event_type")
		}
		if _, ok := b["year"]; ok {
			ev.Year = int(f(b, "year"))
		}
		if _, ok := b["notes"]; ok {
			ev.Notes = s(b, "notes")
		}
		if v := s(b, "capture_start"); v != "" {
			ev.CaptureStart = parseSearchDate(v, false)
		}
		if v := s(b, "capture_end"); v != "" {
			ev.CaptureEnd = parseSearchDate(v, true)
		}
		app.Store.UpdateEvent(ev)
		jsonOut(w, ev)
	})
	mux.HandleFunc("POST /api/events/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		app.Store.DeleteEvent(pathID(r))
		jsonOut(w, map[string]any{"ok": true})
	})
	// Magnet suggestions: unassigned files whose capture dates fall in this event's
	// range, grouped for accept/reject.
	mux.HandleFunc("GET /api/events/{id}/suggestions", func(w http.ResponseWriter, r *http.Request) {
		if app.Store.Event(pathID(r)) == nil {
			jsonErr(w, 404, fmt.Errorf("event not found"))
			return
		}
		groups := app.SuggestForEvent(pathID(r))
		jsonOut(w, map[string]any{"groups": groups})
	})
	// Accept/assign files to an event (file_ids), or unassign (event_id omitted → 0).
	mux.HandleFunc("POST /api/events/{id}/assign", func(w http.ResponseWriter, r *http.Request) {
		ev := app.Store.Event(pathID(r))
		if ev == nil {
			jsonErr(w, 404, fmt.Errorf("event not found"))
			return
		}
		b := body(r)
		n := app.Store.AssignFilesToEvent(intList(b, "file_ids"), ev.ID)
		jsonOut(w, map[string]any{"assigned": n})
	})
	mux.HandleFunc("POST /api/events/{id}/unassign", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		n := app.Store.AssignFilesToEvent(intList(b, "file_ids"), 0)
		jsonOut(w, map[string]any{"unassigned": n})
	})

	// ---- Templates --------------------------------------------------------
	mux.HandleFunc("GET /api/templates", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"templates": app.Store.Templates(), "tokens": routeTokens,
			"roles": []string{RoleRAW, RoleEditedExport, RoleSidecar, RoleCatalog, RoleVideo, RoleOther}})
	})
	mux.HandleFunc("POST /api/templates", func(w http.ResponseWriter, r *http.Request) {
		var t Template
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			jsonErr(w, 400, err)
			return
		}
		if strings.TrimSpace(t.Name) == "" {
			jsonErr(w, 400, fmt.Errorf("template name required"))
			return
		}
		t.BuiltIn = false
		if len(t.EventTypes) == 0 {
			t.EventTypes = append([]string(nil), defaultEventVocabulary...)
		}
		jsonOut(w, app.Store.AddTemplate(&t))
	})
	mux.HandleFunc("POST /api/templates/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		t := app.Store.Template(pathID(r))
		if t == nil {
			jsonErr(w, 404, fmt.Errorf("template not found"))
			return
		}
		var in Template
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			jsonErr(w, 400, err)
			return
		}
		if strings.TrimSpace(in.Name) != "" {
			t.Name = in.Name
		}
		if in.EventTypes != nil {
			t.EventTypes = in.EventTypes
		}
		if in.Routes != nil {
			t.Routes = in.Routes
		}
		app.Store.UpdateTemplate(t)
		jsonOut(w, t)
	})
	mux.HandleFunc("POST /api/templates/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		app.Store.DeleteTemplate(pathID(r))
		jsonOut(w, map[string]any{"ok": true})
	})
	// Live consequence preview for an UNSAVED (being-edited) template against the real
	// catalog: how many files it places, match no route, and collide.
	mux.HandleFunc("POST /api/templates/preview", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Template     Template `json:"template"`
			CollectionID int      `json:"collection_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, app.RoutePreview(&in.Template, in.CollectionID))
	})

	// ---- Structure inference ----------------------------------------------
	// Point at an organized tree → detect its {year}/{event_type}/{event} pattern,
	// propose a template, and harvest an Event per leaf folder (read-only walk).
	mux.HandleFunc("POST /api/infer-structure", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		root := strings.TrimSpace(s(b, "root"))
		if root == "" {
			jsonErr(w, 400, fmt.Errorf("root (the organized folder to learn from) required"))
			return
		}
		inf, err := app.InferStructure(root)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"structure": inf,
			"template_proposal": app.ProposeTemplateFromInference(inf, s(b, "template_name"))})
	})

	// ---- Conflicts (same logical file, different bytes) -------------------
	// The review queue: true content conflicts with both versions side by side.
	// open=1 (default) filters to unresolved; collection_id scopes to an archive.
	mux.HandleFunc("GET /api/conflicts", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cid, _ := strconv.Atoi(q.Get("collection_id"))
		openOnly := q.Get("open") != "0"
		jsonOut(w, map[string]any{"conflicts": app.Store.ConflictViews(cid, openOnly),
			"open_total": app.Store.OpenConflictCount(cid)})
	})
	// Re-run detection for an archive (usually automatic at ingest; this is a manual
	// refresh, e.g. after editing metadata).
	mux.HandleFunc("POST /api/conflicts/detect", func(w http.ResponseWriter, r *http.Request) {
		cid := int(f(body(r), "collection_id"))
		if cid == 0 {
			jsonErr(w, 400, fmt.Errorf("collection_id required"))
			return
		}
		jsonOut(w, app.DetectConflicts(cid))
	})
	// Record a human decision: {"resolution":"CANONICAL","canonical_file_id":N} keeps
	// one version and folds the rest into its retained history; {"resolution":
	// "KEEP-BOTH"} keeps both as independent files (renamed on plan).
	mux.HandleFunc("POST /api/conflicts/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		c := app.Store.Conflict(pathID(r))
		if c == nil {
			jsonErr(w, 404, fmt.Errorf("conflict not found"))
			return
		}
		b := body(r)
		if err := app.Store.ResolveConflict(c.ID, strings.ToUpper(strings.TrimSpace(s(b, "resolution"))), int(f(b, "canonical_file_id"))); err != nil {
			jsonErr(w, 400, err)
			return
		}
		app.Store.Log("conflict", fmt.Sprintf("conflict %d resolved: %s", c.ID, s(b, "resolution")))
		jsonOut(w, map[string]any{"ok": true, "conflict": app.Store.Conflict(c.ID),
			"open_total": app.Store.OpenConflictCount(c.CollectionID)})
	})

	// format sustainability — the registry and the per-archive census (0 = all).
	mux.HandleFunc("GET /api/formats", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.formatRegistry())
	})
	mux.HandleFunc("GET /api/census", func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.Atoi(r.URL.Query().Get("collection_id"))
		jsonOut(w, app.FormatCensus(cid))
	})
	register(mux, "GET /api/collections/{id}/census", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		jsonOut(w, app.FormatCensus(id))
	})

	// treemap — "where is my risk": one zoom level of the size×status treemap,
	// computed from catalog sizes (never the disk). ?path= zooms in, ?color=drift
	// re-tints by reconcile state where a report exists.
	register(mux, "GET /api/collections/{id}/treemap", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		jsonOut(w, app.Store.Treemap(id, r.URL.Query().Get("path"), r.URL.Query().Get("color")))
	})

	// BagIt conformant-bag export — institutional handoff. Writes a valid bag
	// (data/ payload + manifests + COMPARISON.md) for the archive.
	register(mux, "POST /api/collections/{id}/bagit-export", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Collection(id) == nil {
			jsonErr(w, 404, fmt.Errorf("archive not found"))
			return
		}
		out := s(body(r), "output_dir")
		if out == "" {
			jsonErr(w, 400, fmt.Errorf("output_dir required"))
			return
		}
		jsonOut(w, runJob(app, "bagit-export", "BagIt export → "+out, func(p func(float64, string)) (map[string]any, error) {
			return app.ExportBag(id, out, p)
		}))
	})
	// Per-package "Export as BagIt" — a conformant bag for a single package.
	register(mux, "POST /api/chunks/{id}/bagit-export", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		if app.Store.Chunk(id) == nil {
			jsonErr(w, 404, fmt.Errorf("package not found"))
			return
		}
		out := s(body(r), "output_dir")
		if out == "" {
			jsonErr(w, 400, fmt.Errorf("output_dir required"))
			return
		}
		jsonOut(w, runJob(app, "bagit-export", "BagIt export → "+out, func(p func(float64, string)) (map[string]any, error) {
			return app.ExportPackageBag(id, out, p)
		}))
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
		jsonOut(w, runJob(app, "build", "Build "+c.Name, func(p func(float64, string)) (map[string]any, error) {
			return nil, app.BuildChunk(id, p)
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
		if err := sealGuard(app, vol); err != nil {
			jsonErr(w, 409, err)
			return
		}
		jsonOut(w, runJob(app, "write", "Write "+c.Name+" → "+dest, func(p func(float64, string)) (map[string]any, error) {
			return app.WriteChunk(id, dest, f(b, "buffer_gb"), int(f(b, "block_mb")), f(b, "throttle_mbps"), vol, p)
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
		if err := sealGuard(app, vol); err != nil {
			jsonErr(w, 409, err)
			return
		}
		jsonOut(w, runJob(app, "write", "Re-write "+c.Name+" copy", func(p func(float64, string)) (map[string]any, error) {
			return app.RewriteCopy(id, vol, f(b, "buffer_gb"), int(f(b, "block_mb")), f(b, "throttle_mbps"), p)
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
		if err := sealGuard(app, vol); err != nil {
			jsonErr(w, 409, err)
			return
		}
		jsonOut(w, runJob(app, "write", "Span-write next segment of "+c.Name+" → "+dest, func(p func(float64, string)) (map[string]any, error) {
			return app.SpanWriteNext(id, dest, f(b, "buffer_gb"), int(f(b, "block_mb")), f(b, "throttle_mbps"), vol, p)
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
	mux.HandleFunc("GET /api/verify-levels", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, verifyLevelsMeta())
	})
	mux.HandleFunc("POST /api/verify-campaign", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		dest := s(b, "dest_dir")
		if dest == "" {
			jsonErr(w, 400, fmt.Errorf("dest_dir required (mounted tape/disc or archive folder)"))
			return
		}
		level := s(b, "level")
		jsonOut(w, runJob(app, "verify", "Verify campaign — "+dest, func(p func(float64, string)) (map[string]any, error) {
			return app.VerifyCampaign(dest, level, p)
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
		if err := sealGuard(app, vol); err != nil {
			jsonErr(w, 409, err)
			return
		}
		deep, _ := b["deep"].(bool)
		// Adoption result (adopted / skipped-duplicate / unreadable) is surfaced via
		// the job's final label and the refreshed Packages/Volumes views.
		jsonOut(w, runJob(app, "adopt", "Adopt media — "+mount, func(p func(float64, string)) (map[string]any, error) {
			return app.AdoptMedia(mount, cid, vol, deep, p)
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
		jsonOut(w, runJob(app, "restore", "Restore "+c.Name, func(p func(float64, string)) (map[string]any, error) {
			return app.RestoreChunk(id, s(b, "source_dir"), out, members, p)
		}))
	})

	// file detail — the full retained-version history of one file, each version
	// located to the package(s)/volume(s) that still hold its bytes.
	mux.HandleFunc("GET /api/files/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		f := app.Store.FileByID(id)
		if f == nil {
			jsonErr(w, 404, fmt.Errorf("file %d not found", id))
			return
		}
		versions := app.Store.FileVersions(id)
		lines := make([]string, 0, len(versions))
		for _, v := range versions {
			lines = append(lines, v.LocatorLine())
		}
		jsonOut(w, map[string]any{"file": f, "versions": versions, "locators": lines})
	})

	// version-selectable file restore — default newest; {version:N} or
	// {as_of:"2024-03-12"} picks a specific retained version and hash-verifies it.
	mux.HandleFunc("POST /api/files/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r)
		b := body(r)
		if app.Store.FileByID(id) == nil {
			jsonErr(w, 404, fmt.Errorf("file %d not found", id))
			return
		}
		out := s(b, "output_dir")
		if out == "" {
			jsonErr(w, 400, fmt.Errorf("output_dir required"))
			return
		}
		sel := VersionSelector{Hash: s(b, "hash")}
		if v := f(b, "version"); v > 0 {
			sel.Index = int(v)
		}
		if as := strings.TrimSpace(s(b, "as_of")); as != "" {
			t, err := parseAsOf(as)
			if err != nil {
				jsonErr(w, 400, err)
				return
			}
			sel.AsOf = &t
		}
		jsonOut(w, runJob(app, "restore", fmt.Sprintf("Restore file %d", id), func(p func(float64, string)) (map[string]any, error) {
			return app.RestoreFileVersion(id, sel, s(b, "source_dir"), out, p)
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
		jsonOut(w, runJob(app, "burn", "Burn next disc in "+q.Name, func(p func(float64, string)) (map[string]any, error) {
			return app.BurnNext(id, p)
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
		resp := runJob(app, "recoverykit", "Recovery Kit → "+out, func(p func(float64, string)) (map[string]any, error) {
			return app.BuildRecoveryKit(out, p)
		})
		resp["warning"] = recoveryKitWarning
		jsonOut(w, resp)
	})

	// key sheet verify — "prove they typed it right". Reconstructs the passphrase
	// from a retyped key sheet, checks each line's CRC-16 code, and (when a key_ref
	// is given) confirms the reconstructed SHA-256 fingerprint matches that key. The
	// secret itself is NEVER returned.
	mux.HandleFunc("POST /api/keysheet/verify", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		sheet := s(b, "sheet")
		if strings.TrimSpace(sheet) == "" {
			jsonErr(w, 400, fmt.Errorf("sheet text required (paste the L01/L02/… PAYLOAD lines)"))
			return
		}
		_, res := parseKeySheet(sheet)
		out := map[string]any{"ok": res.OK, "lines": res.Lines, "bad_lines": res.BadLines,
			"fingerprint": res.Fingerprint, "length": res.Length, "notes": res.Notes}
		if ref := strings.TrimSpace(s(b, "key_ref")); ref != "" {
			for _, k := range app.Store.KeyMetas() {
				if k.Ref == ref {
					match := strings.EqualFold(k.Fingerprint, res.Fingerprint)
					out["key_ref"], out["fingerprint_matches"] = ref, match
					out["ok"] = res.OK && match
				}
			}
		}
		jsonOut(w, out)
	})

	// escrow bundle — "the archive preserves its own reader": budget/status + the
	// explicit (network-touching) cache fetch. Writing bundles never hits the
	// network; this endpoint is how the cache gets populated.
	mux.HandleFunc("GET /api/escrow", func(w http.ResponseWriter, r *http.Request) {
		cfg := app.LoadConfig()
		census := app.FormatCensus(0)
		full := app.planEscrow(EscrowFull, cfg.EscrowIncludeReaders, census)
		bin := app.planEscrow(EscrowBinariesOnly, cfg.EscrowIncludeReaders, census)
		jsonOut(w, map[string]any{
			"version": appVersion, "fetchable": looksLikeReleaseTag(appVersion),
			"cache_dir": app.escrowCacheDir(), "policy": normEscrowMode(cfg.EscrowOnMedia),
			"include_readers": cfg.EscrowIncludeReaders,
			"full":            map[string]any{"present_bytes": full.PresentBytes, "estimated_bytes": full.estimatedBundleBytes(), "missing": full.MissingNames, "components": full.Components},
			"binaries_only":   map[string]any{"present_bytes": bin.PresentBytes, "estimated_bytes": bin.estimatedBundleBytes(), "missing": bin.MissingNames},
		})
	})
	mux.HandleFunc("POST /api/escrow/fetch", func(w http.ResponseWriter, r *http.Request) {
		cfg := app.LoadConfig()
		census := app.FormatCensus(0)
		resp := runJob(app, "escrow-fetch", "Fetch escrow cache", func(p func(float64, string)) (map[string]any, error) {
			return app.FetchEscrowCache(cfg.EscrowIncludeReaders, census, p)
		})
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
			row := map[string]any{"volume": v, "chunk_count": len(a.chunks),
				"total_bytes": a.bytes, "last_verified_at": a.last}
			if snap := app.Store.VolumeSnapshot(v.ID); snap != nil {
				row["has_snapshot"] = true
				row["snapshot_at"] = snap.CapturedAt
			}
			out = append(out, row)
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
	// Mark (or clear) a volume as drive-encrypted (stenc/LTO hardware AES). This is
	// AWARENESS only — Mnemosyne never sets drive encryption; it records that the
	// operator did, so inventories and the Recovery Kit can warn loudly.
	mux.HandleFunc("POST /api/volumes/{id}/drive-encryption", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		b := body(r)
		v.DriveEncrypted = bl(b, "drive_encrypted")
		v.DriveEncNote = s(b, "drive_enc_note")
		if !v.DriveEncrypted {
			v.DriveEncNote = ""
		}
		app.Store.UpdateVolume(v)
		what := "cleared drive-encryption flag"
		if v.DriveEncrypted {
			what = "flagged DRIVE-ENCRYPTED (stenc/LTO hardware AES — outside the gpg restore story)"
		}
		app.Store.Log("volume", v.Label+": "+what)
		jsonOut(w, map[string]any{"volume": v, "warning": driveEncWarning})
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
				"spanned": c.Spanned, "file_count": c.FileCount, "encrypted": c.Encrypted, "private_manifest": c.PrivateManifest, "mirror": c.Mirror}
			for _, cp := range c.Copies {
				if cp.VolumeID == v.ID && !cp.Superseded {
					on = true
					row["path"], row["last_verified_at"], row["verify_ok"] = cp.Path, cp.LastVerifiedAt, cp.VerifyOK
					row["last_check_level"], row["last_check_ok"], row["last_check_at"] = cp.LastCheckLevel, cp.LastCheckOK, cp.LastCheckAt
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
		if snap := app.Store.VolumeSnapshot(v.ID); snap != nil {
			out["has_snapshot"] = true
			out["snapshot_at"] = snap.CapturedAt
			out["snapshot_files"] = snap.TotalFiles
			out["snapshot_bytes"] = snap.TotalBytes
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

	// A volume's SNAPSHOT — the offline inventory captured at dock ingest. Summary +
	// per-drive report (role breakdown, duplicates vs. other drives, mirror verdict).
	// The full file list is NOT inlined here; the treemap endpoint browses it lazily.
	mux.HandleFunc("GET /api/volumes/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		snap := app.Store.VolumeSnapshot(v.ID)
		if snap == nil {
			jsonOut(w, map[string]any{"has_snapshot": false})
			return
		}
		jsonOut(w, map[string]any{
			"has_snapshot": true,
			"captured_at":  snap.CapturedAt, "session_id": snap.SessionID,
			"serial": snap.Serial, "model": snap.Model, "device_size": snap.DeviceSize,
			"total_files": snap.TotalFiles, "total_bytes": snap.TotalBytes, "unreadable": snap.Unreadable,
			"role_files": snap.RoleFiles, "role_bytes": snap.RoleBytes,
			"smart": snap.Smart, "report": app.driveReport(snap),
		})
	})
	// One zoom level of a volume's snapshot treemap (offline — no disk access),
	// colored by file role. path="" is the drive root; echo a child's path to zoom.
	mux.HandleFunc("GET /api/volumes/{id}/treemap", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		snap := app.Store.VolumeSnapshot(v.ID)
		if snap == nil {
			jsonErr(w, 404, fmt.Errorf("no snapshot for this volume — dock-ingest the drive first"))
			return
		}
		res := snapshotTreemap(snap, r.URL.Query().Get("path"))
		res.Name = v.Label
		jsonOut(w, res)
	})

	// mirror re-verify — re-check the mirror(s) on a volume at a chosen level
	// (A census · B full · C sample). Only B satisfies protection / refreshes
	// verify-due; A and C are advisory.
	mux.HandleFunc("POST /api/volumes/{id}/verify-mirror", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		b := body(r)
		mount, level := s(b, "mount"), s(b, "level")
		jsonOut(w, runJob(app, "verify", fmt.Sprintf("Mirror re-verify (%s) — %s", levelTag(level), v.Label), func(p func(float64, string)) (map[string]any, error) {
			return app.VerifyMirrorVolume(v.ID, mount, level, p)
		}))
	})

	// finalize — the "close the box and label it" ceremony. Preview reads the
	// preconditions live for the dialog; finalize enforces them (a forced override
	// needs a typed reason and is audit-logged) and seals the volume.
	mux.HandleFunc("GET /api/volumes/{id}/finalize-preview", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		as := app.AssessFinalize(v, r.URL.Query().Get("mount_path"), app.LoadConfig())
		jsonOut(w, map[string]any{"assessment": as, "sealed": v.Sealed})
	})
	mux.HandleFunc("POST /api/volumes/{id}/finalize", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		b := body(r)
		res, err := app.FinalizeVolume(v, s(b, "mount_path"), s(b, "by"), bl(b, "force"), s(b, "force_reason"))
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, res)
	})
	mux.HandleFunc("POST /api/volumes/{id}/unseal", func(w http.ResponseWriter, r *http.Request) {
		v := app.Store.Volume(pathID(r))
		if v == nil {
			jsonErr(w, 404, fmt.Errorf("volume not found"))
			return
		}
		b := body(r)
		if err := app.UnsealVolume(v, s(b, "by"), s(b, "reason")); err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"volume": v})
	})

	// tape diagnostics — OPTIONAL, strictly outside the write path, read-only
	// toward the drive. Availability + the last snapshot for the "check before a
	// big write" nudge; a check that runs the detected tool on demand.
	mux.HandleFunc("GET /api/tape/status", func(w http.ResponseWriter, r *http.Request) {
		st := app.TapeToolStatus()
		st["last"] = app.Store.LastTapeCheck()
		st["stenc"] = app.StencStatus() // drive-level AES awareness (optional, Linux)
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
	// Drive-level (hardware) tape AES via stenc — AWARENESS, not dependence. This
	// endpoint reads the drive's current encryption status (SPIN, read-only). Absent
	// stenc (or non-Linux) it returns availability + an OS-aware hint, never an error.
	mux.HandleFunc("GET /api/tape/encryption", func(w http.ResponseWriter, r *http.Request) {
		st := app.StencStatus()
		if st["available"] == true {
			if enc, err := app.DriveEncryptionStatus(r.URL.Query().Get("device")); err == nil {
				st["status"] = enc
			} else {
				st["error"] = err.Error()
			}
		}
		jsonOut(w, st)
	})
	// Set or clear the drive key via stenc (SPOUT — a control command, never tape
	// movement). Explicitly gated: the caller must pass confirm:true, having shown
	// the operator the warning. This is OUTSIDE the gpg restore story; never silent.
	mux.HandleFunc("POST /api/tape/drive-key", func(w http.ResponseWriter, r *http.Request) {
		if !app.stencAvailable() {
			jsonOut(w, map[string]any{"available": false, "hint": stencInstallHint()})
			return
		}
		b := body(r)
		if !bl(b, "confirm") {
			jsonErr(w, 400, fmt.Errorf("refusing to change the drive key without explicit confirmation (confirm:true)"))
			return
		}
		action := strings.ToLower(strings.TrimSpace(s(b, "action")))
		dev := s(b, "device")
		var err error
		switch action {
		case "set":
			err = app.SetDriveKey(dev, s(b, "key_file"), int(f(b, "algorithm")))
		case "clear", "off":
			err = app.ClearDriveKey(dev)
		default:
			jsonErr(w, 400, fmt.Errorf("action must be 'set' or 'clear'"))
			return
		}
		if err != nil {
			jsonErr(w, 500, err)
			return
		}
		out := map[string]any{"ok": true, "action": action, "warning": driveEncWarning}
		if enc, e := app.DriveEncryptionStatus(dev); e == nil {
			out["status"] = enc
		}
		jsonOut(w, out)
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
		serial, label, mode, level := s(b, "serial"), s(b, "label"), s(b, "mode"), s(b, "level")
		confirm := bl(b, "confirm") // proceed past the SMART failure gate (operator acknowledged)
		jsonOut(w, runJob(app, "dock", "Ingest "+mount, func(p func(float64, string)) (map[string]any, error) {
			return app.IngestDrive(id, mount, serial, label, mode, level, confirm, p)
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

	// ---- locations: first-class physical places (the "1" in 3-2-1) --------
	mux.HandleFunc("GET /api/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, app.Store.LocationStats())
	})
	mux.HandleFunc("POST /api/locations", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		if strings.TrimSpace(s(b, "name")) == "" {
			jsonErr(w, 400, fmt.Errorf("name required (e.g. \"Shoe Box #1\" or \"Grandma's house\")"))
			return
		}
		l := app.Store.AddLocation(s(b, "name"), bl(b, "offsite"), s(b, "notes"))
		app.Store.Log("location", fmt.Sprintf("created location %q (%s)", l.Name, offsiteWord(l.Offsite)))
		jsonOut(w, l)
	})
	// Edit a location (name/offsite/notes). Flipping offsite re-homes every volume in
	// it, so a protection recompute rides back.
	mux.HandleFunc("POST /api/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		l := app.Store.UpdateLocation(pathID(r), s(b, "name"), bl(b, "offsite"), s(b, "notes"))
		if l == nil {
			jsonErr(w, 404, fmt.Errorf("location not found"))
			return
		}
		app.Store.Log("location", fmt.Sprintf("updated location %q (%s)", l.Name, offsiteWord(l.Offsite)))
		jsonOut(w, map[string]any{"location": l, "recompute": app.recomputeJob()})
	})
	// Reassign a volume to a location (location_id 0 clears it). Recompute rides back.
	mux.HandleFunc("POST /api/volumes/{id}/location", func(w http.ResponseWriter, r *http.Request) {
		if err := app.Store.SetVolumeLocation(pathID(r), int(f(body(r), "location_id"))); err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]any{"volume": app.Store.Volume(pathID(r)), "recompute": app.recomputeJob()})
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
