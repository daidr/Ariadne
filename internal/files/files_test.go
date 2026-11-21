package files

import (
	"archive/zip"
	"bytes"
	"errors"
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
		if result.Code != 200 || result.Header().Get("Cache-Control") != "private, no-store" || result.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("file headers: %d %v", result.Code, result.Header())
		}
		if method == "HEAD" && result.Body.Len() != 0 {
			t.Fatal("HEAD returned file bytes")
		}
		if method == "GET" && result.Body.String() != "正文\n" {
			t.Fatal("GET changed file bytes")
		}
	}
}
