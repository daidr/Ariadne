package files

import (
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

func TestInvalidAttachmentAndOversizeAreRefused(t *testing.T) {
	store := newStore(t)
	for _, test := range []struct {
		mime string
		data []byte
		text bool
		key  string
	}{
		{"image/svg+xml", []byte("<svg/>"), false, "error.fileType"},
		{"image/svg+xml", []byte("<svg/>"), true, "error.attachmentType"},
		{"text/plain", []byte{'a', 0}, true, "error.attachmentType"},
		{"text/plain", []byte{255}, true, "error.attachmentType"},
		{"text/plain", []byte(strings.Repeat("x", MaxTextBytes+1)), true, "error.fileTooLarge"},
	} {
		_, err := store.Save(t.Context(), "p1", test.mime, test.data, test.text)
		var localized *domainerr.Error
		if !errors.As(err, &localized) || localized.Key != test.key {
			t.Fatalf("%s: %v", test.mime, err)
		}
	}
	if _, err := store.Save(t.Context(), "gone", "image/png", []byte("image"), false); err == nil {
		t.Fatal("wrote file for missing project")
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
