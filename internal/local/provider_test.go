package local

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestDirectoryProviderFileReferenceAndLifecycle(t *testing.T) {
	root := t.TempDir()
	writeGit(t, root, "notes.txt", "read only")
	writeGit(t, root, "sibling.txt", "private")
	provider := DirectoryProvider(t.TempDir(), nil, true)
	capabilities, err := provider.Connect(t.Context(), map[string]any{"path": filepath.Join(root, "notes.txt")})
	if err != nil {
		t.Fatal(err)
	}
	defer capabilities.Close()
	if !capabilities.Workspace.ViewOnly || capabilities.Workspace.Isolated {
		t.Fatal("file provider offers working directory")
	}
	if _, err := capabilities.Workspace.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "thread"}); err == nil {
		t.Fatal("file source opened for work")
	}
	view, err := capabilities.Workspace.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if data, err := view.ReadBytes(t.Context(), "notes.txt", 1024); err != nil || string(data) != "read only" {
		t.Fatal(string(data), err)
	}
	if _, err := view.ReadBytes(t.Context(), "sibling.txt", 1024); err == nil {
		t.Fatal("file reference exposes sibling")
	}
	if err := capabilities.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := view.ReadBytes(t.Context(), "notes.txt", 1024); err == nil {
		t.Fatal("closed reference retained handle")
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal("closed provider retained directory lock", err)
	}
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
}
