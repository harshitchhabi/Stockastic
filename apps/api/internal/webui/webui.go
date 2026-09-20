// Package webui serves the built frontend from the Go binary itself, so the event runs as one process
// on one origin: no Node server, no CORS, nothing extra to keep alive for five hours.
//
// Every asset is read, hashed and gzip-compressed ONCE at startup and then served from immutable
// in-memory maps. There is no per-request file access, so path traversal is impossible by
// construction, and 750 clients loading at once cost a map lookup each.
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Embedded returns the frontend build embedded in the binary (apps/web builds into ./dist).
func Embedded() (fs.FS, error) { return fs.Sub(distFS, "dist") }

type asset struct {
	data      []byte
	gz        []byte // nil when compression would not help
	etag      string
	gzEtag    string
	ctype     string
	immutable bool
	html      bool
}

// Handler serves the SPA. Construct it once with New; it is safe for concurrent use.
type Handler struct {
	assets      map[string]*asset
	index       *asset
	apiPrefixes []string
}

// Explicit content types: Go's mime package reads the OS registry on Windows and can report .js as
// text/plain, which browsers refuse to run as a module.
var types = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".map":   "application/json",
	".txt":   "text/plain; charset=utf-8",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := types[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func compressible(ct string) bool {
	return strings.HasPrefix(ct, "text/") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "image/svg")
}

// New preloads every file in fsys. Requests whose path starts with one of apiPrefixes are never
// answered with the SPA: an API typo gets a JSON 404, not an HTML page that parses as garbage.
func New(fsys fs.FS, apiPrefixes ...string) (*Handler, error) {
	h := &Handler{assets: map[string]*asset{}, apiPrefixes: apiPrefixes}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Base(p) == ".gitkeep" {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tag := hex.EncodeToString(sum[:8])
		a := &asset{
			data: data, etag: `"` + tag + `"`, gzEtag: `"` + tag + `-gz"`, ctype: contentType(p),
			immutable: strings.HasPrefix(p, "assets/"), html: strings.HasSuffix(p, ".html"),
		}
		if compressible(a.ctype) && len(data) > 256 {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(data)
			_ = zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		h.assets[p] = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.index = h.assets["index.html"]
	return h, nil
}

// Built reports whether a frontend build (an index.html) is present.
func (h *Handler) Built() bool { return h.index != nil }

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self' ws: wss:; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

func (h *Handler) isAPI(p string) bool {
	for _, pre := range h.apiPrefixes {
		if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	if h.isAPI(p) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
		return
	}
	if h.index == nil {
		http.Error(w, "frontend not built: run `npm run build` in apps/web", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(p, "/")
	a, ok := h.assets[name]
	if !ok {
		// A path with a file extension that is not a real asset is a genuine 404 (a missing script must
		// not be answered with index.html); extensionless paths are client-side routes like /admin.
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		a = h.index
	}
	h.write(w, r, a)
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request, a *asset) {
	body, tag := a.data, a.etag
	hd := w.Header()
	hd.Set("Content-Type", a.ctype)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "same-origin")
	if a.html {
		hd.Set("Content-Security-Policy", csp)
	}
	if compressible(a.ctype) {
		hd.Set("Vary", "Accept-Encoding")
	}
	if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		body, tag = a.gz, a.gzEtag
		hd.Set("Content-Encoding", "gzip")
	}
	hd.Set("ETag", tag)
	if a.immutable {
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hd.Set("Cache-Control", "no-cache") // revalidate: cheap 304s, and a new build is picked up at once
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
