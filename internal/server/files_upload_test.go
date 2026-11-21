package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
)

func TestMessageAttachmentUploadSupportsZipAndLargerLimit(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Uploads"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	entry, err := zw.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("hello zip")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipData := zipBuf.Bytes()

	// Message attachment upload accepts ZIP (even with Windows MIME or empty MIME).
	req := httptest.NewRequest("POST", "/api/projects/"+project.ID+"/files?for=message", bytes.NewReader(zipData))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/x-zip-compressed")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("expected 201 uploading zip, got %d: %s", rec.Code, rec.Body.String())
	}
	var uploaded struct {
		File files.Stored `json:"file"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.File.MIME != "application/zip" {
		t.Fatalf("expected MIME application/zip, got %q", uploaded.File.MIME)
	}

	// Downloading returns exact bytes with safe headers.
	getReq := httptest.NewRequest("GET", uploaded.File.URL, nil)
	getReq.Header.Set("Authorization", "Bearer test-token")
	getRec := httptest.NewRecorder()
	api.ServeHTTP(getRec, getReq)
	if getRec.Code != 200 || !bytes.Equal(getRec.Body.Bytes(), zipData) {
		t.Fatalf("download failed: status=%d bytes=%d", getRec.Code, getRec.Body.Len())
	}
	if getRec.Header().Get("Content-Disposition") != "attachment" {
		t.Fatalf("expected Content-Disposition=attachment, got %q", getRec.Header().Get("Content-Disposition"))
	}

	// Non-message image endpoint still rejects ZIP.
	imgReq := httptest.NewRequest("POST", "/api/projects/"+project.ID+"/files", bytes.NewReader(zipData))
	imgReq.Header.Set("Authorization", "Bearer test-token")
	imgReq.Header.Set("Content-Type", "application/zip")
	imgRec := httptest.NewRecorder()
	api.ServeHTTP(imgRec, imgReq)
	if imgRec.Code != 400 {
		t.Fatalf("expected 400 for non-message zip upload, got %d: %s", imgRec.Code, imgRec.Body.String())
	}

	// Non-message upload enforces 10 MiB limit, while message upload allows > 10 MiB (up to 50 MiB).
	largeBinary := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, (files.MaxImageBytes+1024)/4)
	imgLargeReq := httptest.NewRequest("POST", "/api/projects/"+project.ID+"/files", bytes.NewReader(largeBinary))
	imgLargeReq.Header.Set("Authorization", "Bearer test-token")
	imgLargeReq.Header.Set("Content-Type", "image/png")
	imgLargeRec := httptest.NewRecorder()
	api.ServeHTTP(imgLargeRec, imgLargeReq)
	if imgLargeRec.Code != 400 {
		t.Fatalf("expected 400 for >10 MiB image upload, got %d", imgLargeRec.Code)
	}

	msgLargeReq := httptest.NewRequest("POST", "/api/projects/"+project.ID+"/files?for=message", bytes.NewReader(largeBinary))
	msgLargeReq.Header.Set("Authorization", "Bearer test-token")
	msgLargeReq.Header.Set("Content-Type", "application/octet-stream")
	msgLargeRec := httptest.NewRecorder()
	api.ServeHTTP(msgLargeRec, msgLargeReq)
	if msgLargeRec.Code != 201 {
		t.Fatalf("expected 201 for >10 MiB message attachment, got %d: %s", msgLargeRec.Code, msgLargeRec.Body.String())
	}
}
