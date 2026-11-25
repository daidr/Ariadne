package local

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestGitTreeReadsCommittedBinaryAndSearches(t *testing.T) {
	if testing.Short() {
		t.Skip("git tree read/search covered by full suite")
	}
	t.Parallel()
	root, git := initGit(t)
	binary := []byte{0xff, 0, 0xef, 0xbb, 0xbf, 1, 2, 3}
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), binary, 0600); err != nil {
		t.Fatal(err)
	}
	writeGit(t, root, "src/code.go", "hello tree\n")
	writeGit(t, root, "src/readme.md", "hello notes\n")
	gitCommit(t, git, "tree")
	writeGit(t, root, "src/code.go", "dirty source\n")
	view := NewTreeView(git, NewGitBytes(root, "file"), "refs/heads/main", "main")
	data, err := view.ReadBytes(t.Context(), "binary.dat", 1024)
	if err != nil || !reflect.DeepEqual(data, binary) {
		t.Fatal(data, err)
	}
	if _, err := view.ReadBytes(t.Context(), "binary.dat", 3); err == nil {
		t.Fatal("blob size limit ignored")
	} else {
		var tooLarge *sdk.FileTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatal(err)
		}
	}
	data, err = view.ReadBytes(t.Context(), "src/code.go", 1024)
	if err != nil || string(data) != "hello tree\n" {
		t.Fatal(string(data), err)
	}
	files, err := view.Files(t.Context(), "src")
	if err != nil || !reflect.DeepEqual(files.Files, []string{"src/code.go", "src/readme.md"}) {
		t.Fatal(files, err)
	}
	entries, err := view.List(t.Context(), "src")
	if err != nil || len(entries) != 2 || entries[0].Name != "code.go" {
		t.Fatal(entries, err)
	}
	result, err := view.Search(t.Context(), "hello", "src", sdk.SearchOptions{Glob: "*.go"})
	if err != nil || result.Error != nil || !reflect.DeepEqual(result.Lines, []string{"src/code.go:1:hello tree"}) {
		t.Fatal(result, err)
	}
	for _, path := range []string{"../file.txt", "/file.txt", `C:\file.txt`, `\file.txt`} {
		if _, err := view.ReadBytes(t.Context(), path, 1024); err == nil {
			t.Fatal("unsafe tree path accepted", path)
		}
	}
	if _, err := NewGitBytes(root, "file")(t.Context(), []string{"cat-file", "blob", "HEAD:binary.dat"}, 2); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal("raw git reader ignored output limit", err)
	}
}
