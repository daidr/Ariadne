package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

func newStaticTestAPI(t *testing.T, web fstest.MapFS) *API {
	t.Helper()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	api, err := New(Options{Repository: repo, Token: "secret", Web: web})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	return api
}

func gunzipBytes(t *testing.T, compressed []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	defer reader.Close()
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip read failed: %v", err)
	}
	return out
}

func TestStaticCompressionNegotiationAndHeaders(t *testing.T) {
	t.Parallel()
	jsBody := []byte(strings.Repeat("console.log('ariadne static compression test');\n", 64))
	brFakePayload := []byte("fake-brotli-stream-shorter-than-raw")
	gzPrePayload := func() []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(jsBody)
		_ = w.Close()
		return buf.Bytes()
	}()
	cssBody := []byte(strings.Repeat(".ariadne-panel{display:flex;gap:8px;}\n", 48))
	pngBody := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, 128)
	tinyJS := []byte("export default 1;")
	now := time.Unix(1800000000, 0)

	api := newStaticTestAPI(t, fstest.MapFS{
		"index.html":              {Data: []byte(strings.Repeat("<!doctype html><div>Ariadne</div>", 32)), ModTime: now},
		"assets/app-abc123.js":    {Data: jsBody, ModTime: now},
		"assets/app-abc123.js.br": {Data: brFakePayload, ModTime: now},
		"assets/app-abc123.js.gz": {Data: gzPrePayload, ModTime: now},
		"assets/style-def.css":    {Data: cssBody, ModTime: now},
		"assets/tiny.js":          {Data: tinyJS, ModTime: now},
		"icons/icon-192.png":      {Data: pngBody, ModTime: now},
	})

	t.Run("prefers brotli when precompressed and accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/app-abc123.js", nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Fatalf("Content-Encoding = %q, want br", got)
		}
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("Vary = %q, want Accept-Encoding", got)
		}
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(brFakePayload)) {
			t.Fatalf("Content-Length = %q, want %d", got, len(brFakePayload))
		}
		if !bytes.Equal(rec.Body.Bytes(), brFakePayload) {
			t.Fatalf("body did not match precompressed br payload")
		}
	})

	t.Run("respects q-values and falls back to gzip", func(t *testing.T) {
		for _, accept := range []string{"gzip;q=1.0, br;q=0.5", "br;q=0, gzip"} {
			req := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/app-abc123.js", nil)
			req.Header.Set("Accept-Encoding", accept)
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("Accept-Encoding %q: status=%d encoding=%q", accept, rec.Code, rec.Header().Get("Content-Encoding"))
			}
			if got := gunzipBytes(t, rec.Body.Bytes()); !bytes.Equal(got, jsBody) {
				t.Fatalf("Accept-Encoding %q: gunzipped body mismatch", accept)
			}
		}
	})

	t.Run("dynamically gzips compressible assets without sidecar", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/style-def.css", nil)
		req.Header.Set("Accept-Encoding", "br, gzip")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("Vary = %q, want Accept-Encoding", got)
		}
		if rec.Body.Len() >= len(cssBody) {
			t.Fatalf("expected gzipped css (%d) < raw (%d)", rec.Body.Len(), len(cssBody))
		}
		if got := gunzipBytes(t, rec.Body.Bytes()); !bytes.Equal(got, cssBody) {
			t.Fatalf("gunzipped css did not match original")
		}

		// HEAD matches Content-Length and Content-Encoding with empty body.
		headReq := httptest.NewRequest(http.MethodHead, "http://test.invalid/assets/style-def.css", nil)
		headReq.Header.Set("Accept-Encoding", "br, gzip")
		headRec := httptest.NewRecorder()
		api.ServeHTTP(headRec, headReq)
		if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
			t.Fatalf("HEAD status=%d bodyLen=%d", headRec.Code, headRec.Body.Len())
		}
		if headRec.Header().Get("Content-Encoding") != "gzip" || headRec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
			t.Fatalf("HEAD headers mismatch: encoding=%q len=%q want=%d", headRec.Header().Get("Content-Encoding"), headRec.Header().Get("Content-Length"), rec.Body.Len())
		}
	})

	t.Run("serves uncompressed when client omits or disables encodings", func(t *testing.T) {
		for _, accept := range []string{"", "identity", "br;q=0, gzip;q=0"} {
			req := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/app-abc123.js", nil)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("Accept-Encoding %q: status=%d", accept, rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("Accept-Encoding %q: unexpected Content-Encoding %q", accept, got)
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("Accept-Encoding %q: Vary = %q, want Accept-Encoding", accept, got)
			}
			if !bytes.Equal(rec.Body.Bytes(), jsBody) {
				t.Fatalf("Accept-Encoding %q: raw body mismatch", accept)
			}
		}
	})

	t.Run("skips compression for binary and tiny assets", func(t *testing.T) {
		pngReq := httptest.NewRequest(http.MethodGet, "http://test.invalid/icons/icon-192.png", nil)
		pngReq.Header.Set("Accept-Encoding", "br, gzip")
		pngRec := httptest.NewRecorder()
		api.ServeHTTP(pngRec, pngReq)
		if pngRec.Code != http.StatusOK || pngRec.Header().Get("Content-Encoding") != "" || pngRec.Header().Get("Vary") != "" {
			t.Fatalf("binary asset compressed unexpectedly: status=%d encoding=%q vary=%q", pngRec.Code, pngRec.Header().Get("Content-Encoding"), pngRec.Header().Get("Vary"))
		}
		if !bytes.Equal(pngRec.Body.Bytes(), pngBody) {
			t.Fatalf("png body altered")
		}

		tinyReq := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/tiny.js", nil)
		tinyReq.Header.Set("Accept-Encoding", "gzip")
		tinyRec := httptest.NewRecorder()
		api.ServeHTTP(tinyRec, tinyReq)
		if tinyRec.Code != http.StatusOK || tinyRec.Header().Get("Content-Encoding") != "" || !bytes.Equal(tinyRec.Body.Bytes(), tinyJS) {
			t.Fatalf("tiny asset response unexpected: status=%d encoding=%q", tinyRec.Code, tinyRec.Header().Get("Content-Encoding"))
		}
	})

	t.Run("blocks direct access to precompressed sidecar files", func(t *testing.T) {
		for _, sidecar := range []string{"/assets/app-abc123.js.br", "/assets/app-abc123.js.gz"} {
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://test.invalid"+sidecar, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("sidecar %s returned %d, want 404", sidecar, rec.Code)
			}
		}
	})
}

func TestStaticETagNotModifiedAndStaleSidecar(t *testing.T) {
	t.Parallel()
	now := time.Unix(1800000000, 0)
	htmlBody := []byte(strings.Repeat("<!doctype html><title>Ariadne</title>", 32))
	jsBody := []byte(strings.Repeat("export const v = 1;\n", 64))

	api := newStaticTestAPI(t, fstest.MapFS{
		"index.html":         {Data: htmlBody, ModTime: now},
		"index.html.br":      {Data: []byte("stale-br"), ModTime: now.Add(-time.Minute)},
		"assets/app-1.js":    {Data: jsBody, ModTime: now},
		"assets/app-1.js.br": {Data: []byte("fresh-br-payload"), ModTime: now},
	})

	// Stale .br sidecar older than index.html is ignored in favor of dynamic gzip.
	req := httptest.NewRequest(http.MethodGet, "http://test.invalid/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("stale sidecar fallback failed: status=%d encoding=%q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", rec.Header().Get("Cache-Control"))
	}
	gzipETag := rec.Header().Get("ETag")
	if gzipETag == "" {
		t.Fatal("missing ETag on index.html")
	}

	// Matching If-None-Match (exact, weak, list, wildcard) returns 304 without body.
	for _, inm := range []string{gzipETag, "W/" + gzipETag, `"other", ` + gzipETag, "*"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			condReq := httptest.NewRequest(method, "http://test.invalid/", nil)
			condReq.Header.Set("Accept-Encoding", "br, gzip")
			condReq.Header.Set("If-None-Match", inm)
			condRec := httptest.NewRecorder()
			api.ServeHTTP(condRec, condReq)
			if condRec.Code != http.StatusNotModified {
				t.Fatalf("%s If-None-Match=%q: status=%d, want 304", method, inm, condRec.Code)
			}
			if condRec.Body.Len() != 0 {
				t.Fatalf("%s 304 returned %d body bytes", method, condRec.Body.Len())
			}
			if condRec.Header().Get("ETag") != gzipETag || condRec.Header().Get("Cache-Control") != "no-cache" || condRec.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatalf("304 headers incomplete: %v", condRec.Header())
			}
		}
	}

	// Different representations produce distinct ETags so br ETag does not 304 identity.
	brReq := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/app-1.js", nil)
	brReq.Header.Set("Accept-Encoding", "br")
	brRec := httptest.NewRecorder()
	api.ServeHTTP(brRec, brReq)
	brETag := brRec.Header().Get("ETag")
	if brETag == "" || !strings.HasSuffix(brETag, `-br"`) {
		t.Fatalf("unexpected br ETag: %q", brETag)
	}

	rawReq := httptest.NewRequest(http.MethodGet, "http://test.invalid/assets/app-1.js", nil)
	rawReq.Header.Set("If-None-Match", brETag)
	rawRec := httptest.NewRecorder()
	api.ServeHTTP(rawRec, rawReq)
	if rawRec.Code != http.StatusOK || rawRec.Header().Get("ETag") == brETag {
		t.Fatalf("br ETag incorrectly matched identity representation: status=%d etag=%q", rawRec.Code, rawRec.Header().Get("ETag"))
	}
}

func TestStaticCacheInvalidatesWhenDiskFileChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	v1 := []byte(strings.Repeat("<!doctype html><div>v1</div>", 20))
	if err := os.WriteFile(indexPath, v1, 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	api, err := New(Options{Repository: repo, Token: "secret", Web: os.DirFS(dir)})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	rec1 := httptest.NewRecorder()
	api.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "http://test.invalid/", nil))
	etag1 := rec1.Header().Get("ETag")
	if rec1.Code != http.StatusOK || etag1 == "" {
		t.Fatalf("initial fetch failed: %d %q", rec1.Code, etag1)
	}

	v2 := []byte(strings.Repeat("<!doctype html><div>v2-updated</div>", 22))
	if err := os.WriteFile(indexPath, v2, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(indexPath, future, future); err != nil {
		t.Fatal(err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://test.invalid/", nil)
	req2.Header.Set("If-None-Match", etag1)
	rec2 := httptest.NewRecorder()
	api.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("updated index returned %d with old ETag, want 200", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), v2) {
		t.Fatalf("updated index returned stale body")
	}
	if rec2.Header().Get("ETag") == etag1 {
		t.Fatalf("updated index kept old ETag %q", etag1)
	}
}

func TestBuiltFrontendInitialPayloadCompressionAnd304(t *testing.T) {
	t.Parallel()
	distDir := filepath.Join("..", "..", "packages", "web", "dist")
	indexHTML, err := os.ReadFile(filepath.Join(distDir, "index.html"))
	if err != nil {
		t.Skipf("built frontend dist not present (%v); skipping real-bundle measurement", err)
	}

	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	api, err := New(Options{Repository: repo, Token: "secret", Web: os.DirFS(distDir)})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	refRe := regexp.MustCompile(`(?:src|href)="(/[^"]+)"`)
	seen := map[string]bool{"/": true}
	paths := []string{"/"}
	for _, match := range refRe.FindAllStringSubmatch(string(indexHTML), -1) {
		p := match[1]
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	measure := func(acceptEncoding string) (totalBytes int, compressedCount int, etags map[string]string) {
		etags = make(map[string]string, len(paths))
		for _, p := range paths {
			req := httptest.NewRequest(http.MethodGet, "http://test.invalid"+p, nil)
			if acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", acceptEncoding)
			}
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s (%q): status=%d", p, acceptEncoding, rec.Code)
			}
			totalBytes += rec.Body.Len()
			if rec.Header().Get("Content-Encoding") != "" {
				compressedCount++
			}
			etags[p] = rec.Header().Get("ETag")
		}
		return totalBytes, compressedCount, etags
	}

	rawBytes, _, _ := measure("")
	gzipBytes, gzipCount, _ := measure("gzip")
	brBytes, brCount, brETags := measure("gzip, deflate, br")

	t.Logf("initial load (%d files): raw=%.1f kB, gzip=%.1f kB (%d compressed), br=%.1f kB (%d compressed)",
		len(paths), float64(rawBytes)/1024, float64(gzipBytes)/1024, gzipCount, float64(brBytes)/1024, brCount)

	if brBytes >= 550*1024 {
		t.Fatalf("brotli initial payload %.1f kB exceeds 550 kB budget", float64(brBytes)/1024)
	}
	if gzipBytes >= rawBytes/2 {
		t.Fatalf("gzip initial payload %.1f kB did not achieve expected compression vs raw %.1f kB", float64(gzipBytes)/1024, float64(rawBytes)/1024)
	}

	// Second load with ETag hits 304 on every entry with 0 body bytes.
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, "http://test.invalid"+p, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		req.Header.Set("If-None-Match", brETags[p])
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Fatalf("revalidation %s: status=%d bodyLen=%d, want 304/0", p, rec.Code, rec.Body.Len())
		}
	}
}
