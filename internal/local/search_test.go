package local

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func TestReadOnlySourcesAndSearch(t *testing.T) {
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
	for _, q := range queries {
		result, err := sdk.SearchByReading(t.Context(), view, q.pattern, q.path, sdk.SearchOptions{Glob: q.glob, IgnoreCase: q.ignore})
		if err != nil {
			t.Fatal(err)
		}
		native = append(native, result)
	}
	found, err := sdk.Walk(t.Context(), view, ".")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found.Files)
	if len(native) != len(queries) || len(found.Files) == 0 {
		t.Fatalf("unexpected search results: queries=%d files=%v", len(native), found.Files)
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

func TestWorkspaceFilesRespectsGitignoreAndAllowsExplicitIgnoredSubdir(t *testing.T) {
	t.Parallel()
	assertGitRepoListing := func(t *testing.T, workspace *Workspace) {
		t.Helper()
		view := SearchableView(workspace, "")

		rootFiles, err := view.(sdk.SourceFiles).Files(t.Context(), ".")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(rootFiles.Files, "\n")
		if !strings.Contains(joined, "src/code.ts") || !strings.Contains(joined, ".github/ci.yml") {
			t.Fatalf("root listing missing expected files:\n%s", joined)
		}
		if strings.Contains(joined, "ignored/private.ts") || strings.Contains(joined, "node_modules/pkg/index.d.ts") || strings.Contains(joined, ".git/") {
			t.Fatalf("root listing leaked ignored files:\n%s", joined)
		}

		emptyPathFiles, err := workspace.Files(t.Context(), "")
		if err != nil || !strings.Contains(strings.Join(emptyPathFiles.Files, "\n"), "src/code.ts") {
			t.Fatalf("empty path listing failed: %v %v", emptyPathFiles.Files, err)
		}

		pkgFiles, err := view.(sdk.SourceFiles).Files(t.Context(), "node_modules/pkg")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(pkgFiles.Files, "\n"), "node_modules/pkg/index.d.ts") {
			t.Fatalf("explicit ignored subdir listing missed file: %v", pkgFiles.Files)
		}

		missingIgnored, err := view.(sdk.SourceFiles).Files(t.Context(), "ignored/missing")
		if err != nil {
			t.Fatal(err)
		}
		if len(missingIgnored.Files) != 0 {
			t.Fatalf("nonexistent ignored path returned phantom files: %v", missingIgnored.Files)
		}
	}

	t.Run("git repo", func(t *testing.T) {
		t.Parallel()
		root, _ := initGit(t)
		writeGit(t, root, "src/code.ts", "hello\n")
		writeGit(t, root, ".github/ci.yml", "name: ci\n")
		writeGit(t, root, ".gitignore", "ignored/\nnode_modules/\n")
		writeGit(t, root, "ignored/private.ts", "secret\n")
		writeGit(t, root, "node_modules/pkg/index.d.ts", "export {}\n")

		// Default path (uses rg when installed, falls back to git + sdk.Walk otherwise).
		assertGitRepoListing(t, newTestWorkspace(t, root, WorkspaceOptions{HideGit: true}))

		// Explicitly exercise the no-rg fallback path even on hosts with rg installed.
		noRG := newTestWorkspace(t, root, WorkspaceOptions{HideGit: true})
		done := make(chan struct{})
		close(done)
		noRG.probes = map[string]*probeResult{"rg": {done: done, value: false}}
		assertGitRepoListing(t, noRG)
	})

	t.Run("non-git directory with gitignore", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeGit(t, root, "src/code.ts", "hello\n")
		writeGit(t, root, ".gitignore", "ignored/\nnode_modules/\n")
		writeGit(t, root, "ignored/private.ts", "secret\n")
		writeGit(t, root, "node_modules/pkg/index.d.ts", "export {}\n")
		workspace := newTestWorkspace(t, root, WorkspaceOptions{HideGit: true})
		view := SearchableView(workspace, "")

		rootFiles, err := view.(sdk.SourceFiles).Files(t.Context(), ".")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(rootFiles.Files, "\n")
		if !strings.Contains(joined, "src/code.ts") {
			t.Fatalf("non-git root listing missing src/code.ts:\n%s", joined)
		}
		if strings.Contains(joined, "ignored/private.ts") || strings.Contains(joined, "node_modules/pkg/index.d.ts") {
			t.Fatalf("non-git root listing ignored .gitignore:\n%s", joined)
		}

		pkgFiles, err := view.(sdk.SourceFiles).Files(t.Context(), "node_modules/pkg")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(pkgFiles.Files, "\n"), "node_modules/pkg/index.d.ts") {
			t.Fatalf("non-git explicit ignored subdir missed file: %v", pkgFiles.Files)
		}
	})
}
