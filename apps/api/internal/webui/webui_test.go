package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

var bigJS = strings.Repeat("export const x = 'stockastic';\n", 100)

func site() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte(`<!doctype html><script type="module" src="/assets/app-abc123.js"></script>`)},
		"assets/app-abc123.js":  {Data: []byte(bigJS)},
		"assets/app-abc123.css": {Data: []byte("body{margin:0}")},
		"favicon.ico":           {Data: []byte{0, 0, 1, 0}},
		".gitkeep":              {Data: nil},
	}
}

func newHandler(t *testing.T, fsys fstest.MapFS) *Handler {
	t.Helper()
	h, err := New(fsys, "/api/", "/ws")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func do(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServesIndexAtRootAndTheEmbeddedBuildIsWellFormed(t *testing.T) {
	h := newHandler(t, site())
	w := do(h, "GET", "/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<script") || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if !h.Built() {
		t.Error("Built()")
	}
	// The real embedded FS must always be constructible, even before the frontend has been built.
	efs, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(efs, "/api/"); err != nil {
		t.Fatalf("the embedded build must always load: %v", err)
	}
}

func TestClientSideRoutesFallBackToIndexButMissingAssetsAre404(t *testing.T) {
	h := newHandler(t, site())
	for _, route := range []string{"/admin", "/admin/", "/some/deep/route"} {
		if w := do(h, "GET", route); w.Code != 200 || !strings.Contains(w.Body.String(), "<script") {
			t.Errorf("%s: %d — an SPA route must serve index.html", route, w.Code)
		}
	}
	for _, missing := range []string{"/assets/gone.js", "/nope.css", "/x.png"} {
		w := do(h, "GET", missing)
		if w.Code != 404 || strings.Contains(w.Body.String(), "<script") {
			t.Errorf("%s: %d %q — a missing file must be a real 404, never index.html", missing, w.Code, w.Body.String())
		}
	}
}

func TestAPIPathsNeverGetTheSPA(t *testing.T) {
	h := newHandler(t, site())
	for _, p := range []string{"/api/nope", "/api/", "/api", "/ws", "/ws/x"} {
		w := do(h, "GET", p)
		if w.Code != 404 || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), "not_found") {
			t.Errorf("%s: %d %q %q — a mistyped API path must be a JSON 404", p, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}
	if w := do(h, "GET", "/apiary"); w.Code != 200 {
		t.Errorf("/apiary is not under /api/ and is an ordinary SPA route: %d", w.Code)
	}
}

func TestPathTraversalCannotEscapeTheAssetMap(t *testing.T) {
	fsys := site()
	fsys["secret.txt"] = &fstest.MapFile{Data: []byte("top-secret")}
	delete(fsys, "secret.txt") // not part of the build: must be unreachable
	h := newHandler(t, fsys)
	for _, p := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/assets/../../secret.txt", "//etc/passwd", "/..%2f..%2fsecret.txt"} {
		w := do(h, "GET", p)
		if strings.Contains(w.Body.String(), "top-secret") || strings.Contains(w.Body.String(), "root:") {
			t.Errorf("%s leaked content", p)
		}
	}
}

func TestCachingPolicy(t *testing.T) {
	h := newHandler(t, site())
	if cc := do(h, "GET", "/assets/app-abc123.js").Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("hashed assets are immutable, got %q", cc)
	}
	if cc := do(h, "GET", "/").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index.html must revalidate so a new build is picked up, got %q", cc)
	}
	first := do(h, "GET", "/assets/app-abc123.css")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	again := do(h, "GET", "/assets/app-abc123.css", "If-None-Match", etag)
	if again.Code != 304 || again.Body.Len() != 0 {
		t.Errorf("conditional GET: %d, body %d bytes", again.Code, again.Body.Len())
	}
	if other := do(h, "GET", "/assets/app-abc123.css", "If-None-Match", `"different"`); other.Code != 200 {
		t.Errorf("a non-matching ETag must return the body: %d", other.Code)
	}
}

func TestGzipIsNegotiatedAndRoundTrips(t *testing.T) {
	h := newHandler(t, site())
	plain := do(h, "GET", "/assets/app-abc123.js")
	if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != bigJS {
		t.Fatal("without Accept-Encoding the identity body must be served")
	}
	gz := do(h, "GET", "/assets/app-abc123.js", "Accept-Encoding", "gzip, br")
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers: %v", gz.Header())
	}
	if gz.Body.Len() >= len(bigJS) {
		t.Errorf("gzip body (%d) should be smaller than the original (%d)", gz.Body.Len(), len(bigJS))
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != bigJS {
		t.Error("gzip body does not decompress to the original")
	}
	if gz.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Error("the gzip and identity representations must have different ETags")
	}
	// A tiny file is not worth compressing.
	if do(h, "GET", "/assets/app-abc123.css", "Accept-Encoding", "gzip").Header().Get("Content-Encoding") != "" {
		t.Error("small files should be served as-is")
	}
	// Binary assets are never re-compressed.
	if do(h, "GET", "/favicon.ico", "Accept-Encoding", "gzip").Header().Get("Content-Encoding") != "" {
		t.Error("binary assets must not be gzipped")
	}
}

func TestContentTypesAndSecurityHeaders(t *testing.T) {
	h := newHandler(t, site())
	if ct := do(h, "GET", "/assets/app-abc123.js").Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("js content type = %q (browsers refuse text/plain modules)", ct)
	}
	if ct := do(h, "GET", "/assets/app-abc123.css").Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("css = %q", ct)
	}
	idx := do(h, "GET", "/")
	if !strings.Contains(idx.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("index must carry a CSP: %v", idx.Header())
	}
	if idx.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff")
	}
	if do(h, "GET", "/assets/app-abc123.js").Header().Get("Content-Security-Policy") != "" {
		t.Error("the CSP belongs on documents, not scripts")
	}
}

func TestMethods(t *testing.T) {
	h := newHandler(t, site())
	head := do(h, "HEAD", "/assets/app-abc123.js")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: %d body=%d cl=%q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		w := do(h, m, "/")
		if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: %d %q", m, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestUnbuiltFrontendIsAClearErrorNotAPanicOrABlankPage(t *testing.T) {
	h := newHandler(t, fstest.MapFS{".gitkeep": {}})
	if h.Built() {
		t.Error("Built() must be false")
	}
	w := do(h, "GET", "/")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "npm run build") {
		t.Errorf("%d %q", w.Code, w.Body.String())
	}
	// The API 404 still works so the backend is usable without a frontend.
	if do(h, "GET", "/api/x").Code != 404 {
		t.Error("api prefix")
	}
}

func TestConcurrentRequestsAreSafe(t *testing.T) {
	h := newHandler(t, site())
	var wg sync.WaitGroup
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths := []string{"/", "/admin", "/assets/app-abc123.js", "/assets/app-abc123.css", "/api/x"}
			w := do(h, "GET", paths[i%len(paths)], "Accept-Encoding", "gzip")
			if w.Code != 200 && w.Code != 404 {
				t.Errorf("unexpected %d", w.Code)
			}
		}(i)
	}
	wg.Wait()
}

var assetRef = regexp.MustCompile(`(?:src|href)="(/[^"]+)"`)

// TestRealBuildResolvesEveryReferencedAsset runs against the actual embedded build (skipped when the
// frontend has not been built): each script/style the built index.html points at must be served with
// the right content type, and gzip must round-trip on the real bundle.
func TestRealBuildResolvesEveryReferencedAsset(t *testing.T) {
	efs, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(efs, "/api/", "/ws")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Built() {
		t.Skip("frontend not built (run `npm run build` in apps/web)")
	}
	idx := do(h, "GET", "/")
	refs := assetRef.FindAllStringSubmatch(idx.Body.String(), -1)
	if len(refs) == 0 {
		t.Fatalf("built index.html references no assets: %s", idx.Body.String())
	}
	for _, m := range refs {
		w := do(h, "GET", m[1], "Accept-Encoding", "gzip")
		if w.Code != 200 {
			t.Errorf("%s -> %d", m[1], w.Code)
			continue
		}
		ct := w.Header().Get("Content-Type")
		switch {
		case strings.HasSuffix(m[1], ".js") && !strings.HasPrefix(ct, "text/javascript"):
			t.Errorf("%s served as %q", m[1], ct)
		case strings.HasSuffix(m[1], ".css") && !strings.HasPrefix(ct, "text/css"):
			t.Errorf("%s served as %q", m[1], ct)
		}
		if strings.HasPrefix(m[1], "/assets/") && w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s is content-hashed and must be immutable", m[1])
		}
		if w.Header().Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
			if err != nil {
				t.Errorf("%s: bad gzip: %v", m[1], err)
				continue
			}
			if _, err := io.Copy(io.Discard, zr); err != nil {
				t.Errorf("%s: gzip does not decode: %v", m[1], err)
			}
		}
	}
	t.Logf("verified %d assets referenced by the built index.html", len(refs))
}
