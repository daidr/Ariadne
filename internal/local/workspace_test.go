package local

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func newTestWorkspace(t *testing.T, root string, options WorkspaceOptions) *Workspace {
	t.Helper()
	workspace, err := NewWorkspace(root, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Error(err)
		}
	})
	return workspace
}
func TestWorkspacePathsAndHiddenGit(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "private"), []byte("outside"), 0600)
	os.Mkdir(filepath.Join(root, ".git"), 0700)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("credentials"), 0600)
	workspace := newTestWorkspace(t, root, WorkspaceOptions{ID: "source", HideGit: true})
	for _, path := range []string{"../private", filepath.Join(outside, "private"), ".git/config", "sub/../.git/config", ".GIT/config"} {
		if _, err := workspace.ReadBytes(t.Context(), path, 1024); err == nil {
			t.Fatal("protected read accepted", path)
		}
		if err := workspace.WriteFile(t.Context(), path, "wrong"); err == nil {
			t.Fatal("protected write accepted", path)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "private")); string(data) != "outside" {
		t.Fatal("outside file modified")
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".git", "config")); string(data) != "credentials" {
		t.Fatal("git config modified")
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err == nil {
		for _, path := range []string{"link/private", "link/new/child"} {
			if err := workspace.WriteFile(t.Context(), path, "wrong"); err == nil {
				t.Fatal("outside symlink write accepted", path)
			}
		}
	}
	if err := os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "alias")); err == nil {
		if _, err := workspace.ReadBytes(t.Context(), "alias/config", 1024); err == nil {
			t.Fatal("git symlink accepted")
		}
	}
	if err := workspace.WriteFile(t.Context(), "src/new.go", "中文\n"); err != nil {
		t.Fatal(err)
	}
	if data, err := workspace.ReadFile(t.Context(), filepath.Join(root, "src", "new.go")); err != nil || data != "中文\n" {
		t.Fatal(data, err)
	}
	if _, err := workspace.ReadBytes(t.Context(), "src/new.go", 2); err == nil {
		t.Fatal("size limit ignored")
	} else {
		var tooLarge *sdk.FileTooLargeError
		if !errors.As(err, &tooLarge) || tooLarge.Size != 7 {
			t.Fatal(err)
		}
	}
	if err := workspace.WriteFile(t.Context(), "empty", ""); err != nil {
		t.Fatal(err)
	}
	if bytes, err := workspace.ReadBytes(t.Context(), "empty", 0); err != nil || len(bytes) != 0 {
		t.Fatal(bytes, err)
	}
}
func TestWorkspaceAcceptsOriginalRootAlias(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	workspace := newTestWorkspace(t, alias, WorkspaceOptions{HideGit: true})
	path := filepath.Join(alias, "sub", "new.txt")
	if err := workspace.WriteFile(t.Context(), path, "kept"); err != nil {
		t.Fatal(err)
	}
	if content, err := workspace.ReadFile(t.Context(), path); err != nil || content != "kept" {
		t.Fatal(content, err)
	}
	for _, relative := range []string{".git/config", "escape/new.txt"} {
		if err := workspace.WriteFile(t.Context(), filepath.Join(alias, relative), "unexpected"); err == nil {
			t.Fatalf("write through root alias accepted %s", relative)
		}
	}
}

func TestWorkspaceStaysInsideRootDuringSymlinkSwap(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "kept"), []byte("private"), 0600)
	os.Mkdir(filepath.Join(root, "dir"), 0700)
	workspace := newTestWorkspace(t, root, WorkspaceOptions{})
	probe := filepath.Join(root, "probe")
	if err := os.Symlink(outside, probe); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	os.Remove(probe)
	ctx, cancel := context.WithCancel(t.Context())
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for ctx.Err() == nil {
			os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "saved"))
			os.Symlink(outside, filepath.Join(root, "dir"))
			os.Remove(filepath.Join(root, "dir"))
			os.Rename(filepath.Join(root, "saved"), filepath.Join(root, "dir"))
		}
	}()
	for range 200 {
		_ = workspace.WriteFile(t.Context(), "dir/kept", "changed")
	}
	cancel()
	<-swapped
	if bytes, _ := os.ReadFile(filepath.Join(outside, "kept")); string(bytes) != "private" {
		t.Fatal("racing symlink redirected write")
	}
}

func TestWorkspaceRemoveDoesNotFollowAlias(t *testing.T) {
	dir := t.TempDir()
	workspace := newTestWorkspace(t, dir, WorkspaceOptions{HideGit: true})
	if err := workspace.WriteFile(t.Context(), "original", "kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "original"), filepath.Join(dir, "alias")); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	if err := workspace.RemoveFile(t.Context(), "alias"); err == nil {
		t.Fatal("remove accepted an alias")
	}
	if content, err := workspace.ReadFile(t.Context(), "original"); err != nil || content != "kept" {
		t.Fatal(content, err)
	}
	if err := workspace.RemoveFile(t.Context(), "original"); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceCloseCancelsOwnedCommand(t *testing.T) {
	workspace := newTestWorkspace(t, t.TempDir(), WorkspaceOptions{})
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(workspace.directory, "ready")
	result := make(chan sdk.ExecResult, 1)
	go func() {
		out, _ := workspace.ExecFile(t.Context(), []string{program, "-test.run=^TestWorkspaceCommandChild$", "--", "--workspace-child", ready}, sdk.ExecOptions{})
		result <- out
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-result:
		if out.ExitCode != 130 {
			t.Fatal(out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closed workspace kept a command running")
	}
}
func TestWorkspaceCommandChild(t *testing.T) {
	for index, arg := range os.Args {
		if arg == "--workspace-child" {
			os.WriteFile(os.Args[index+1], []byte("ready"), 0600)
			for {
				time.Sleep(time.Hour)
			}
		}
	}
}

func TestWorkspaceFilesAndTextMatchLegacy(t *testing.T) {
	root := t.TempDir()
	workspace := newTestWorkspace(t, root, WorkspaceOptions{ID: "source", HideGit: true})
	files := map[string][]byte{"a.txt": []byte("a"), "B.txt": []byte("b"), "_name": []byte("_"), "中文.txt": []byte("中文"), "bom.txt": append([]byte{0xef, 0xbb, 0xbf}, []byte("hello")...), "invalid.txt": {0xe2, 0x82}, "empty": {}}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.Mkdir(filepath.Join(root, "src"), 0700)
	os.Mkdir(filepath.Join(root, ".git"), 0700)
	entries, err := workspace.List(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	text := map[string]string{}
	for name := range files {
		value, err := workspace.ReadFile(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		text[name] = value
	}
	native := map[string]any{"entries": entries, "text": text}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		return
	}
	data, _ := json.Marshal(map[string]any{"root": root, "files": func() []string {
		names := []string{}
		for name := range files {
			names = append(names, name)
		}
		return names
	}()})
	command := legacytest.Command(t, bun, "packages/plugin-local/src/testing/go-workspace-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(data))
	want, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, want)
	}
	got, _ := json.Marshal(native)
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil {
		t.Fatalf("invalid fixtures Go %s\nTS %s", got, want)
	}
	// Contents and entry types must match. CLDR table versions and script
	// reordering differ between Go's collator and ICU; exact mixed-script order
	// remains an explicitly tracked migration compatibility gap.
	for _, value := range []any{actual, expected} {
		entries := value.(map[string]any)["entries"].([]any)
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].(map[string]any)["name"].(string) < entries[j].(map[string]any)["name"].(string)
		})
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go %s\nTS %s", got, want)
	}
}
