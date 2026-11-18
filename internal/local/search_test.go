package local

import (
	"context"
	"encoding/json"
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

func TestReadOnlySourcesAndSearchMatchLegacy(t *testing.T) {
	root := t.TempDir()
	workspace := newTestWorkspace(t, root, WorkspaceOptions{HideGit: true})
	for path, value := range map[string]string{
		".gitignore": "ignored\nnode_modules/\n", "code.ts": "hello world\nword word\nHELLO WORLD\n", "src/a.ts": "hello world\r\n", "src/b.md": "hello world\n", "ignored/private.ts": "hello world\n", "node_modules/a.ts": "hello world\n", "binary.dat": "hello\x00world", "emoji.txt": "🙂\nx\n", ".git/config": "private",
	} {
		writeGit(t, root, path, value)
	}
	view := SearchableView(workspace, "working tree")
	if _, ok := view.(sdk.Workspace); ok {
		t.Fatal("read-only source exposes workspace")
	}
	if _, ok := view.(interface{ Close() error }); ok {
		t.Fatal("read-only source exposes lifecycle")
	}
	if _, ok := view.(interface {
		WriteFile(context.Context, string, string) error
	}); ok {
		t.Fatal("read-only source exposes writer")
	}
	if ref := view.(sdk.SourceReference).Ref(); ref != "working tree" {
		t.Fatal(ref)
	}
	for _, path := range []string{"../outside", "/absolute", "C:relative", ".git/config", "src/../code.ts"} {
		if _, err := view.ReadBytes(t.Context(), path, 1024); err == nil {
			t.Fatal("unsafe source path accepted", path)
		}
	}
	queries := []struct {
		pattern, path, glob string
		ignore              bool
	}{
		{"hello", ".", "*.ts", false}, {"(?<=hello )world", ".", "*.ts", false}, {`(\w+)\s+\1`, ".", "*.ts", false}, {"HELLO", "src", "**/*.{ts,md}", true}, {"^.$", "emoji.txt", "", false},
		{"🙂", "emoji.txt", "", false}, {`[🙂]`, "emoji.txt", "", false}, {`\🙂`, "emoji.txt", "", false},
	}
	native := []sdk.FileSearchResult{}
	queryJSON := []map[string]any{}
	for _, q := range queries {
		result, err := sdk.SearchByReading(t.Context(), view, q.pattern, q.path, sdk.SearchOptions{Glob: q.glob, IgnoreCase: q.ignore})
		if err != nil {
			t.Fatal(err)
		}
		native = append(native, result)
		queryJSON = append(queryJSON, map[string]any{"pattern": q.pattern, "path": q.path, "glob": q.glob, "ignoreCase": q.ignore})
	}
	found, err := sdk.Walk(t.Context(), view, ".")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found.Files)
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		return
	}
	data, _ := json.Marshal(map[string]any{"root": root, "queries": queryJSON})
	command := legacytest.Command(t, bun, "packages/plugin-local/src/testing/go-search-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(data))
	want, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, want)
	}
	got, _ := json.Marshal(map[string]any{"results": native, "files": found.Files})
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go %s\nTS %s", got, want)
	}
}
func TestSearchCapsAndExpensivePattern(t *testing.T) {
	root := t.TempDir()
	workspace := newTestWorkspace(t, root, WorkspaceOptions{HideGit: true})
	for i := range 8 {
		writeGit(t, root, string(rune('a'+i))+".txt", strings.Repeat("match\n", 70))
	}
	view := SearchableView(workspace, "")
	result, err := sdk.SearchByReading(t.Context(), view, "match", ".", sdk.SearchOptions{})
	if err != nil || result.Error != nil || len(result.Lines) != 300 {
		t.Fatal(len(result.Lines), result.Error, err)
	}
	counts := map[string]int{}
	for _, line := range result.Lines {
		file, _, _ := strings.Cut(line, ":")
		counts[file]++
		if counts[file] > 50 {
			t.Fatal("per-file cap ignored")
		}
	}
	writeGit(t, root, "expensive.txt", strings.Repeat("a", 50_000)+"!")
	started := time.Now()
	_, err = sdk.SearchByReading(t.Context(), view, "(a+)+$", "expensive.txt", sdk.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("regex matching was unbounded")
	}
	file, err := sdk.SingleFileView(workspace, "a.txt", "single")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.ReadBytes(t.Context(), "b.txt", 1024); err == nil {
		t.Fatal("single-file source exposed sibling")
	}
	if listing, err := file.List(t.Context(), "./"); err != nil || len(listing) != 1 || listing[0].Name != "a.txt" {
		t.Fatal(listing, err)
	}
}
func TestWorkspaceNativeSearchAndFiles(t *testing.T) {
	root, _ := initGit(t)
	writeGit(t, root, "src/code.ts", "hello native\n")
	writeGit(t, root, ".github/test.yml", "hello hidden\n")
	writeGit(t, root, ".gitignore", "ignored/\n")
	writeGit(t, root, "ignored/private.ts", "hello private\n")
	workspace := newTestWorkspace(t, root, WorkspaceOptions{HideGit: true})
	view := SearchableView(workspace, "")
	files, err := view.(sdk.SourceFiles).Files(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(files.Files, "\n")
	if !strings.Contains(joined, "src/code.ts") || strings.Contains(joined, "ignored/private") || strings.Contains(joined, ".git/config") {
		t.Fatal(joined)
	}
	result, err := view.(sdk.SourceSearch).Search(t.Context(), "hello", ".", sdk.SearchOptions{})
	if err != nil || result.Error != nil || len(result.Lines) != 2 {
		t.Fatal(result, err)
	}
}
