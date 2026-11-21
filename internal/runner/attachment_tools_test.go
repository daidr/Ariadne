package runner

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/trace"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/internal/workspaces"
)

func buildTestZip(t *testing.T, entries map[string][]byte) []byte {
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

func TestReadAttachmentInspectsZipTextAndBinary(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "attachments.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := repo.CreateProject(ctx, new("Other"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+X2JcAAAAASUVORK5CYII="
	pngBytes, err := base64.StdEncoding.DecodeString(pngB64)
	if err != nil {
		t.Fatal(err)
	}

	store := files.Store{Events: repo.Store}
	zipBytes := buildTestZip(t, map[string][]byte{
		"src/main.go":  []byte("package main\n\nfunc main() {}\n"),
		"assets/a.png": pngBytes,
		"raw.bin":      {0x00, 0x01, 0x02, 0xff},
	})
	zipFile, err := store.Save(ctx, project.ID, "application/x-zip-compressed", zipBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	otherZip, err := store.Save(ctx, otherProject.ID, "application/zip", zipBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	textFile, err := store.Save(ctx, project.ID, "text/plain", []byte("line 1\nline 2\nline 3\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	binFile, err := store.Save(ctx, project.ID, "application/pdf", []byte("%PDF-1.7\nbody\n"), true)
	if err != nil {
		t.Fatal(err)
	}

	active := &turn{
		Runner:       &Runner{Repository: repo},
		conversation: core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
	}

	// List ZIP entries
	listing, attached, err := active.readAttachment(ctx, args{"fileId": zipFile.ID})
	if err != nil || len(attached) != 0 {
		t.Fatalf("list zip: %v, attached=%v", err, attached)
	}
	for _, want := range []string{"ZIP archive", "src/main.go", "assets/a.png", "raw.bin"} {
		if !strings.Contains(listing, want) {
			t.Errorf("zip listing missing %q:\n%s", want, listing)
		}
	}

	// Read a text entry inside the ZIP with offset/limit
	code, _, err := active.readAttachment(ctx, args{"fileId": zipFile.ID, "entry": "src/main.go", "offset": float64(2), "limit": float64(2)})
	if err != nil {
		t.Fatalf("read zip entry: %v", err)
	}
	if !strings.Contains(code, "3\tfunc main() {}") {
		t.Errorf("unexpected zip entry text:\n%s", code)
	}

	// Read an image entry inside the ZIP
	imgText, imgAttached, err := active.readAttachment(ctx, args{"fileId": zipFile.ID, "entry": "assets/a.png"})
	if err != nil || len(imgAttached) != 1 || !strings.Contains(imgText, "assets/a.png") {
		t.Fatalf("read zip image: text=%q attached=%v err=%v", imgText, imgAttached, err)
	}

	// Binary entry inside ZIP reports binary metadata
	binEntry, _, err := active.readAttachment(ctx, args{"fileId": zipFile.ID, "entry": "raw.bin"})
	if err != nil || !strings.Contains(binEntry, "binary file") {
		t.Fatalf("read zip binary entry: %q %v", binEntry, err)
	}

	// Missing entry errors cleanly
	if _, _, err := active.readAttachment(ctx, args{"fileId": zipFile.ID, "entry": "missing.txt"}); err == nil {
		t.Fatal("expected error for missing zip entry")
	}

	// Text attachment pagination
	paged, _, err := active.readAttachment(ctx, args{"fileId": textFile.ID, "offset": float64(2), "limit": float64(1)})
	if err != nil || !strings.Contains(paged, "2\tline 2") || !strings.Contains(paged, "Continue at offset 3") {
		t.Fatalf("paged text attachment: %q %v", paged, err)
	}

	// Non-ZIP binary attachment metadata
	meta, _, err := active.readAttachment(ctx, args{"fileId": binFile.ID})
	if err != nil || !strings.Contains(meta, "application/pdf") || !strings.Contains(meta, "save_attachment") {
		t.Fatalf("binary attachment metadata: %q %v", meta, err)
	}

	// Cross-project access is refused
	if _, _, err := active.readAttachment(ctx, args{"fileId": otherZip.ID}); err == nil {
		t.Fatal("readAttachment crossed project boundary")
	}
}

func TestSaveAttachmentWritesExactBinaryToWorkspace(t *testing.T) {
	ctx := t.Context()
	home, dir := t.TempDir(), t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(filepath.Join(home, "local"), nil, true)); err != nil {
		t.Fatal(err)
	}
	work, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close(context.Background())

	project, err := repo.CreateProject(ctx, new("WorkspaceProject"), core.ProjectStart{
		Source: args{"kind": "local-directory", "settings": args{"path": dir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Unzip task"})
	if err != nil {
		t.Fatal(err)
	}

	store := files.Store{Events: repo.Store}
	zipBytes := buildTestZip(t, map[string][]byte{"data.txt": []byte("hello from zip\n")})
	saved, err := store.Save(ctx, project.ID, "application/zip", zipBytes, true)
	if err != nil {
		t.Fatal(err)
	}

	active := &turn{
		Runner:       &Runner{Repository: repo, Workspaces: work},
		conversation: core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"},
	}

	result, err := active.saveAttachment(ctx, args{"fileId": saved.ID, "path": "tmp/archive.zip"})
	if err != nil {
		t.Fatalf("saveAttachment failed: %v", err)
	}
	if !strings.Contains(result, "tmp/archive.zip") || !strings.Contains(result, "application/zip") {
		t.Fatalf("unexpected saveAttachment result: %q", result)
	}

	written, err := os.ReadFile(filepath.Join(dir, "tmp", "archive.zip"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !bytes.Equal(written, zipBytes) {
		t.Fatalf("saved binary differed: got %d bytes, want %d bytes", len(written), len(zipBytes))
	}

	// Path traversal refused
	if _, err := active.saveAttachment(ctx, args{"fileId": saved.ID, "path": "../escape.zip"}); err == nil {
		t.Fatal("saveAttachment allowed path traversal")
	}

	// Coordinator cannot save into workspace
	coordTurn := &turn{
		Runner:       &Runner{Repository: repo, Workspaces: work},
		conversation: core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
	}
	if _, err := coordTurn.saveAttachment(ctx, args{"fileId": saved.ID, "path": "x.zip"}); err == nil {
		t.Fatal("coordinator was allowed to call saveAttachment")
	}
}

func TestBinaryAttachmentReachesCoordinatorAndRelayedThread(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "binary-relay.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Relay target"})
	if err != nil {
		t.Fatal(err)
	}
	store := files.Store{Events: repo.Store}
	zipBytes := buildTestZip(t, map[string][]byte{"note.txt": []byte("from zip")})
	archive, err := store.Save(ctx, project.ID, "application/x-zip-compressed", zipBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	message, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "请解压看看",
		Attachments: []any{attachment(archive, "release.zip")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{thread.ID, project.CoordinatorID, target.ID} {
		if id == target.ID {
			if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
				ConversationID: target.ID, Author: "coordinator", Kind: "relay", Cites: []string{message.ID},
			}); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := repo.ListTranscript(ctx, id, core.TranscriptOptions{ModelContext: true})
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]transcript.Entry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, row)
		}
		options, title := trace.ModelInput(ctx, repo, project.ID, nil, true)
		var sawZip bool
		for _, msg := range transcript.ToMessages(entries, title, options) {
			if strings.Contains(msg.Content, archive.ID) && strings.Contains(msg.Content, "release.zip") && strings.Contains(msg.Content, "read_attachment") {
				sawZip = true
			}
		}
		if !sawZip {
			t.Errorf("conversation %s did not receive zip attachment metadata", id)
		}
	}
}
