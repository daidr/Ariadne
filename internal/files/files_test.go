package files

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/httpauth"
)

func newStore(t *testing.T) Store {
	t.Helper()
	events, err := eventstore.Open(t.Context(), eventstore.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.Close() })
	if _, err := events.DB().Exec(Schema + "CREATE TABLE projects (id TEXT PRIMARY KEY); INSERT INTO projects VALUES ('p1'), ('p2');"); err != nil {
		t.Fatal(err)
	}
	return Store{Events: events}
}

func TestAttachmentsDeduplicateOnlyWithinProjectAndMIME(t *testing.T) {
	store := newStore(t)
	first, err := store.Save(t.Context(), "p1", "application/octet-stream", []byte("中文附件\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Save(t.Context(), "p1", "text/plain; charset=utf-8", []byte("中文附件\n"), true)
	if err != nil || first.ID != again.ID || first.MIME != "text/plain" {
		t.Fatalf("dedupe: %+v %+v %v", first, again, err)
	}
	other, err := store.Save(t.Context(), "p2", "text/plain", []byte("中文附件\n"), true)
	if err != nil || other.ID == first.ID {
		t.Fatal("deduplication crossed project boundary")
	}
	if info, err := store.Info(t.Context(), "p2", first.ID); err != nil || info != nil {
		t.Fatalf("fileInfo crossed project: %+v %v", info, err)
	}
	content, err := store.Read(t.Context(), first.ID)
	if err != nil || string(content.Data) != "中文附件\n" {
		t.Fatalf("read changed bytes: %+v %v", content, err)
	}
}

func makeTestZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInvalidAttachmentAndOversizeAreRefused(t *testing.T) {
	store := newStore(t)
	zipData := makeTestZip(t, map[string][]byte{"readme.txt": []byte("hello")})
	for _, test := range []struct {
		mime            string
		data            []byte
		allowAttachment bool
		key             string
	}{
		{"image/svg+xml", []byte("<svg/>"), false, "error.fileType"},
		{"application/zip", zipData, false, "error.fileType"},
		{"text/plain", []byte("hello"), false, "error.fileType"},
		{"image/png", make([]byte, MaxImageBytes+1), false, "error.fileTooLarge"},
		{"application/octet-stream", make([]byte, MaxFileBytes+1), true, "error.fileTooLarge"},
	} {
		_, err := store.Save(t.Context(), "p1", test.mime, test.data, test.allowAttachment)
		var localized *domainerr.Error
		if !errors.As(err, &localized) || localized.Key != test.key {
			t.Fatalf("%s (allowAttachment=%t): got %v, want %s", test.mime, test.allowAttachment, err, test.key)
		}
	}
	if _, err := store.Save(t.Context(), "gone", "image/png", []byte("image"), false); err == nil {
		t.Fatal("wrote file for missing project")
	}
}

func TestGeneralAttachmentsStoreSafeMIMEAndHeaders(t *testing.T) {
	store := newStore(t)
	zipData := makeTestZip(t, map[string][]byte{"hello.txt": []byte("world")})
	emptyZip := makeTestZip(t, nil)
	docxMIME := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

	for _, tc := range []struct {
		name               string
		givenMIME          string
		data               []byte
		wantMIME           string
		wantAttachmentDisp bool
	}{
		{"zip with windows mime", "application/x-zip-compressed", zipData, "application/zip", true},
		{"zip with empty mime", "", zipData, "application/zip", true},
		{"empty zip archive", "application/zip", emptyZip, "application/zip", true},
		{"docx preserves safe zip mime", docxMIME, zipData, docxMIME, true},
		{"pdf detected", "application/octet-stream", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"), "application/pdf", true},
		{"7z detected", "", append([]byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c}, 0, 0), "application/x-7z-compressed", true},
		{"binary labeled text", "text/plain", []byte{'a', 0, 'b'}, "application/octet-stream", true},
		{"invalid utf8 binary", "text/plain", []byte{0xff, 0xfe}, "application/octet-stream", true},
		{"svg downgraded to plain text", "image/svg+xml", []byte("<svg><script>alert(1)</script></svg>"), "text/plain", false},
		{"large utf8 text accepted", "text/plain", []byte(strings.Repeat("x", MaxTextBytes+1024)), "text/plain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved, err := store.Save(t.Context(), "p1", tc.givenMIME, tc.data, true)
			if err != nil {
				t.Fatalf("save failed: %v", err)
			}
			if saved.MIME != tc.wantMIME {
				t.Fatalf("got MIME %q, want %q", saved.MIME, tc.wantMIME)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/files/{fileId}", store.Serve)
			req := httptest.NewRequest("GET", "http://localhost"+saved.URL, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("serve status: %d", rec.Code)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("missing nosniff header: %v", rec.Header())
			}
			if rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
				t.Fatalf("missing CSP header: %v", rec.Header())
			}
			gotDisp := rec.Header().Get("Content-Disposition")
			if tc.wantAttachmentDisp && gotDisp != "attachment" {
				t.Fatalf("expected Content-Disposition=attachment, got %q", gotDisp)
			}
			if !tc.wantAttachmentDisp && gotDisp != "" {
				t.Fatalf("expected inline serve without Content-Disposition, got %q", gotDisp)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.data) {
				t.Fatalf("served bytes differed from saved data")
			}
		})
	}
}

func TestVideoAttachmentsUseDetectedType(t *testing.T) {
	store := newStore(t)
	for _, file := range []struct {
		data []byte
		mime string
	}{
		{[]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), "video/mp4"},
		{[]byte("\x1a\x45\xdf\xa3"), "video/webm"},
	} {
		saved, err := store.Save(t.Context(), "p1", "application/octet-stream", file.data, true)
		if err != nil || saved.MIME != file.mime {
			t.Fatalf("save %s: %+v %v", file.mime, saved, err)
		}
	}
}

func TestFileHTTPRequiresAuthenticationAndPreservesHeaders(t *testing.T) {
	store := newStore(t)
	file, err := store.Save(t.Context(), "p1", "text/plain", []byte("正文\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/files/{fileId}", store.Serve)
	auth := httpauth.Auth{Token: "secret"}
	handler := auth.Middleware(mux)
	for _, method := range []string{"GET", "HEAD"} {
		request := httptest.NewRequest(method, "http://localhost"+file.URL, nil)
		denied := httptest.NewRecorder()
		handler.ServeHTTP(denied, request)
		if denied.Code != 401 {
			t.Fatal("file read bypassed authentication")
		}
		request.Header.Set("Authorization", "Bearer secret")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != 200 || result.Header().Get("Cache-Control") != "private, no-cache" || result.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("file headers: %d %v", result.Code, result.Header())
		}
		if result.Header().Get("ETag") == "" || result.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatalf("missing ETag or Accept-Ranges: %v", result.Header())
		}
		if method == "HEAD" && result.Body.Len() != 0 {
			t.Fatal("HEAD returned file bytes")
		}
		if method == "GET" && result.Body.String() != "正文\n" {
			t.Fatal("GET changed file bytes")
		}
	}
}

func TestFileServeETagAndNotModified(t *testing.T) {
	store := newStore(t)
	payload := []byte("cached attachment body\n")
	saved, err := store.Save(t.Context(), "p1", "text/plain", payload, true)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	wantETag := fmt.Sprintf("%q", hex.EncodeToString(sum[:]))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/files/{fileId}", store.Serve)

	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first GET status = %d, want 200", first.Code)
	}
	if got := first.Header().Get("ETag"); got != wantETag {
		t.Fatalf("ETag = %q, want %q", got, wantETag)
	}
	if got := first.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q, want private, no-cache", got)
	}

	for _, inm := range []string{wantETag, "W/" + wantETag, `"other", ` + wantETag, "*"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := httptest.NewRequest(method, "http://localhost"+saved.URL, nil)
			req.Header.Set("If-None-Match", inm)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotModified {
				t.Fatalf("%s If-None-Match=%q: status = %d, want 304", method, inm, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("%s 304 returned %d bytes", method, rec.Body.Len())
			}
			if rec.Header().Get("ETag") != wantETag || rec.Header().Get("Cache-Control") != "private, no-cache" {
				t.Fatalf("304 cache headers mismatch: %v", rec.Header())
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
				t.Fatalf("304 missing security headers: %v", rec.Header())
			}
		}
	}

	missReq := httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil)
	missReq.Header.Set("If-None-Match", `"different-hash"`)
	missRec := httptest.NewRecorder()
	mux.ServeHTTP(missRec, missReq)
	if missRec.Code != http.StatusOK || !bytes.Equal(missRec.Body.Bytes(), payload) {
		t.Fatalf("mismatched ETag returned status=%d body=%q", missRec.Code, missRec.Body.String())
	}
}

func TestFileServeRangeAndChunkedStreaming(t *testing.T) {
	store := newStore(t)
	// Build a 2.5 MiB binary payload containing NUL bytes to span multiple 1 MiB SQLite chunks.
	total := 2*fileBlobChunkBytes + 512*1024
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	saved, err := store.Save(t.Context(), "p1", "application/octet-stream", payload, true)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	etag := fmt.Sprintf("%q", hex.EncodeToString(sum[:]))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/files/{fileId}", store.Serve)

	t.Run("full streamed download matches exact binary payload", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Header().Get("Content-Disposition") != "attachment" {
			t.Fatalf("Content-Disposition = %q, want attachment", rec.Header().Get("Content-Disposition"))
		}
		if rec.Header().Get("Content-Length") != fmt.Sprint(total) {
			t.Fatalf("Content-Length = %q, want %d", rec.Header().Get("Content-Length"), total)
		}
		if !bytes.Equal(rec.Body.Bytes(), payload) {
			t.Fatalf("streamed binary body mismatch (got %d bytes, want %d)", rec.Body.Len(), total)
		}
	})

	t.Run("single ranges across chunk boundaries and suffix", func(t *testing.T) {
		cases := []struct {
			name     string
			rangeHdr string
			start    int
			end      int
		}{
			{"prefix", "bytes=0-1023", 0, 1023},
			{"across chunk boundary", fmt.Sprintf("bytes=%d-%d", fileBlobChunkBytes-64, fileBlobChunkBytes+127), fileBlobChunkBytes - 64, fileBlobChunkBytes + 127},
			{"open end", fmt.Sprintf("bytes=%d-", total-500), total - 500, total - 1},
			{"suffix", "bytes=-256", total - 256, total - 1},
		}
		for _, tc := range cases {
			req := httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil)
			req.Header.Set("Range", tc.rangeHdr)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			wantLen := tc.end - tc.start + 1
			wantContentRange := fmt.Sprintf("bytes %d-%d/%d", tc.start, tc.end, total)
			if rec.Code != http.StatusPartialContent {
				t.Fatalf("%s: status = %d, want 206", tc.name, rec.Code)
			}
			if rec.Header().Get("Content-Range") != wantContentRange {
				t.Fatalf("%s: Content-Range = %q, want %q", tc.name, rec.Header().Get("Content-Range"), wantContentRange)
			}
			if rec.Header().Get("Content-Length") != fmt.Sprint(wantLen) {
				t.Fatalf("%s: Content-Length = %q, want %d", tc.name, rec.Header().Get("Content-Length"), wantLen)
			}
			if !bytes.Equal(rec.Body.Bytes(), payload[tc.start:tc.end+1]) {
				t.Fatalf("%s: range bytes mismatch", tc.name)
			}
		}
	})

	t.Run("HEAD with range returns partial headers and no body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "http://localhost"+saved.URL, nil)
		req.Header.Set("Range", "bytes=10-109")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent || rec.Body.Len() != 0 {
			t.Fatalf("HEAD range: status=%d bodyLen=%d", rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("Content-Length") != "100" || rec.Header().Get("Content-Range") != fmt.Sprintf("bytes 10-109/%d", total) {
			t.Fatalf("HEAD range headers: %v", rec.Header())
		}
	})

	t.Run("unsatisfiable range returns 416", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", total))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("status = %d, want 416", rec.Code)
		}
		if rec.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", total) {
			t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
		}
	})

	t.Run("If-Range respects ETag", func(t *testing.T) {
		matchReq := httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil)
		matchReq.Header.Set("Range", "bytes=0-15")
		matchReq.Header.Set("If-Range", etag)
		matchRec := httptest.NewRecorder()
		mux.ServeHTTP(matchRec, matchReq)
		if matchRec.Code != http.StatusPartialContent || !bytes.Equal(matchRec.Body.Bytes(), payload[:16]) {
			t.Fatalf("matching If-Range failed: status=%d len=%d", matchRec.Code, matchRec.Body.Len())
		}

		missReq := httptest.NewRequest(http.MethodGet, "http://localhost"+saved.URL, nil)
		missReq.Header.Set("Range", "bytes=0-15")
		missReq.Header.Set("If-Range", `"stale-etag"`)
		missRec := httptest.NewRecorder()
		mux.ServeHTTP(missRec, missReq)
		if missRec.Code != http.StatusOK || missRec.Body.Len() != total {
			t.Fatalf("mismatched If-Range should return full 200: status=%d len=%d", missRec.Code, missRec.Body.Len())
		}
	})

	t.Run("empty file handles GET and unsatisfiable range", func(t *testing.T) {
		empty, err := store.Save(t.Context(), "p1", "text/plain", []byte{}, true)
		if err != nil {
			t.Fatal(err)
		}
		okRec := httptest.NewRecorder()
		mux.ServeHTTP(okRec, httptest.NewRequest(http.MethodGet, "http://localhost"+empty.URL, nil))
		if okRec.Code != http.StatusOK || okRec.Body.Len() != 0 || okRec.Header().Get("Content-Length") != "0" {
			t.Fatalf("empty GET: status=%d len=%d cl=%q", okRec.Code, okRec.Body.Len(), okRec.Header().Get("Content-Length"))
		}

		rangeReq := httptest.NewRequest(http.MethodGet, "http://localhost"+empty.URL, nil)
		rangeReq.Header.Set("Range", "bytes=0-0")
		rangeRec := httptest.NewRecorder()
		mux.ServeHTTP(rangeRec, rangeReq)
		if rangeRec.Code != http.StatusOK || rangeRec.Body.Len() != 0 || rangeRec.Header().Get("Content-Length") != "0" {
			t.Fatalf("empty range: status=%d len=%d cl=%q", rangeRec.Code, rangeRec.Body.Len(), rangeRec.Header().Get("Content-Length"))
		}
	})

	t.Run("sqliteBlobReader performs single bounded query for small range", func(t *testing.T) {
		reader := &sqliteBlobReader{
			ctx:  t.Context(),
			db:   store.Events.DB(),
			id:   saved.ID,
			size: int64(total),
		}
		if _, err := reader.Seek(int64(fileBlobChunkBytes+100), io.SeekStart); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 256)
		n, err := io.ReadFull(reader, buf)
		if err != nil || n != 256 {
			t.Fatalf("ReadFull: n=%d err=%v", n, err)
		}
		if reader.reads != 1 {
			t.Fatalf("expected 1 SQLite read for 256-byte range, got %d", reader.reads)
		}
		if !bytes.Equal(buf, payload[fileBlobChunkBytes+100:fileBlobChunkBytes+356]) {
			t.Fatalf("read slice mismatch")
		}
	})
}
