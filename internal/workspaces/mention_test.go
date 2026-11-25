package workspaces

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

type mentionView struct {
	tree  map[string][]sdk.DirectoryEntry
	calls int
}

func (view *mentionView) ReadBytes(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("not needed")
}
func (view *mentionView) List(_ context.Context, path string) ([]sdk.DirectoryEntry, error) {
	view.calls++
	return view.tree[path], nil
}

func TestMentionRanksAndWalk(t *testing.T) {
	t.Parallel()
	files := []string{"src/file.ts", "file.txt", "other/file.ts", "profile", "中文.txt", "İFILE.md", "ΟΣ.md", "😀.md"}
	for range 40 {
		files = append(files, "same-rank.txt")
	}
	queries := []string{"", "file", "i", "i̇", "ος", "中文", ".md", "not-found"}
	tree := map[string][]sdk.DirectoryEntry{".": {{Name: "src", Kind: "directory"}, {Name: "node_modules", Kind: "directory"}, {Name: "a.txt", Kind: "file"}}, "src": {{Name: "dir", Kind: "directory"}, {Name: "b.ts", Kind: "file"}}, "src/dir": {{Name: "c.ts", Kind: "file"}}}
	view := &mentionView{tree: tree}
	walked, err := WalkMentionFiles(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	ranks := [][]*int{}
	selected := [][]string{}
	for _, query := range queries {
		row := []*int{}
		for _, file := range files {
			row = append(row, MentionRank(file, query))
		}
		ranks = append(ranks, row)
		selected = append(selected, selectMentions(files, query))
	}
	if len(selected[0]) != MentionFiles || len(walked) != 3 || view.calls != 3 || len(ranks) != len(queries) {
		t.Fatal("mention result unbounded or entered ignored folder")
	}
}
func TestMentionWalkBoundsEmptyTreesFilesAndPathBytes(t *testing.T) {
	t.Parallel()
	view := &mentionView{tree: map[string][]sdk.DirectoryEntry{}}
	for i := range maxMentionFiles + 10 {
		view.tree["."] = append(view.tree["."], sdk.DirectoryEntry{Name: "dir-" + string(rune(0x1000+i)), Kind: "directory"})
	}
	if _, err := WalkMentionFiles(t.Context(), view); err != nil || view.calls > maxMentionFiles {
		t.Fatal("empty directories not bounded", view.calls, err)
	}
	view = &mentionView{tree: map[string][]sdk.DirectoryEntry{}}
	for range maxMentionFiles + 100 {
		view.tree["."] = append(view.tree["."], sdk.DirectoryEntry{Name: strings.Repeat("x", 200), Kind: "file"})
	}
	files, err := WalkMentionFiles(t.Context(), view)
	if err != nil || len(files) > maxMentionFiles || len(files)*200 > maxMentionBytes {
		t.Fatal("file path bytes not bounded", len(files), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := WalkMentionFiles(ctx, view); !errors.Is(err, context.Canceled) {
		t.Fatal("walk ignored cancellation", err)
	}
}
func TestMentionCacheRefreshesHotEntriesAndClosesOnDelete(t *testing.T) {
	t.Parallel()
	repo, project, _ := testRepository(t)
	view := &mentionView{tree: map[string][]sdk.DirectoryEntry{".": {{Name: "original.txt", Kind: "file"}}}}
	registry := sources.New()
	registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test", Provides: []string{"workspace"}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{View: func(context.Context) (sdk.SourceView, error) { return view, nil }}}, nil
	}})
	var clock atomic.Int64
	clock.Store(30000)
	manager, err := New(Options{Repository: repo, Sources: registry, Now: func() time.Time { return time.UnixMilli(clock.Load()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	read := func() []string {
		t.Helper()
		files, err := manager.MentionFiles(t.Context(), project.ID, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	if got := read(); len(got) != 1 {
		t.Fatal(got)
	}
	view.tree["."] = append(view.tree["."], sdk.DirectoryEntry{Name: "new.txt", Kind: "file"})
	clock.Store(59000)
	if len(read()) != 1 {
		t.Fatal("fresh cache not reused")
	}
	clock.Store(61000)
	if len(read()) != 2 {
		t.Fatal("constant access prevented refresh")
	}
	if err := manager.InvalidateProject(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	// Resource closure releases its backing slice after invalidation/close.
	config, err := manager.sourceConfig(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := config.key + "/2"
	lease, err := manager.mentions.Acquire(t.Context(), workspaceID(project.ID, ""), key, func(context.Context) (*mentionList, error) { return &mentionList{files: []string{"retained"}}, nil })
	if err != nil {
		t.Fatal(err)
	}
	value := lease.Value
	lease.Release()
	closeManager(t, manager)
	if value.files != nil {
		t.Fatal("closed mention list retains files")
	}
}
