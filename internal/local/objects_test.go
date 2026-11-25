package local

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func objects(t *testing.T, directory string) *ObjectStore {
	t.Helper()
	store, err := NewObjectStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
func TestGitObjectsStreamRoundTripIncrementalAndAtomicRefs(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	source := objects(t, root)
	targetDir := t.TempDir()
	copyTree(t, ensureBareTemplate(t), targetDir)
	targetGit := NewGit(targetDir, "file")
	target := objects(t, targetDir)
	base := readHeadSHA(t, root)
	refs, err := source.Refs(t.Context(), []string{"refs/heads/"})
	if err != nil || refs["refs/heads/main"] != base {
		t.Fatal(refs, err)
	}
	bundle, err := source.Bundle(t.Context(), []string{"refs/heads/main"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Receive(t.Context(), bundle, map[string]*string{"refs/remotes/origin/main": &base}); err != nil {
		t.Fatal(err)
	}
	if present, err := target.Has(t.Context(), []string{base}); err != nil || !reflect.DeepEqual(present, []string{base}) {
		t.Fatal(present, err)
	}
	if text := gitOK(t, targetGit, "show", base+":file.txt"); text != "base" {
		t.Fatal(text)
	}
	writeGit(t, root, "file.txt", "incremental\n")
	gitCommitTracked(t, git, "incremental")
	head := readHeadSHA(t, root)
	bundle, err = source.Bundle(t.Context(), []string{"refs/heads/main"}, []string{base})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Receive(t.Context(), bundle, map[string]*string{"refs/remotes/origin/main": &head}); err != nil {
		t.Fatal(err)
	}
	if text := gitOK(t, targetGit, "show", head+":file.txt"); text != "incremental" {
		t.Fatal(text)
	}
	missing := strings.Repeat("f", 40)
	if err := target.Receive(t.Context(), nil, map[string]*string{"refs/heads/valid": &head, "refs/heads/invalid": &missing}); err == nil {
		t.Fatal("unknown object accepted")
	}
	refs, err = target.Refs(t.Context(), []string{"refs/heads/"})
	if err != nil || len(refs) != 0 {
		t.Fatal("failed transaction changed refs", refs, err)
	}
	writeGitRef(t, targetDir, "refs/remotes/origin/alias", "ref: refs/remotes/origin/main")
	if err := target.Receive(t.Context(), nil, map[string]*string{"refs/remotes/origin/alias": nil}); err != nil {
		t.Fatal(err)
	}
	if got := readRefSHA(t, targetDir, "refs/remotes/origin/main"); got != head {
		t.Fatal("symbolic deletion removed target branch", got)
	}
	if err := target.Receive(t.Context(), nil, map[string]*string{"refs/remotes/origin/main": nil}); err != nil {
		t.Fatal(err)
	}
	refs, err = target.Refs(t.Context(), []string{"refs/remotes/origin/"})
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
}

type countCloser struct {
	io.ReadCloser
	closed  atomic.Int32
	started chan struct{}
	once    sync.Once
}

func (reader *countCloser) Read(data []byte) (int, error) {
	if reader.started != nil {
		reader.once.Do(func() { close(reader.started) })
	}
	return reader.ReadCloser.Read(data)
}

func (reader *countCloser) Close() error { reader.closed.Add(1); return reader.ReadCloser.Close() }
func TestGitObjectStoreCloseEndsUnreadBundleAndBlockedReceive(t *testing.T) {
	t.Parallel()
	root, _ := initGit(t)
	source := objects(t, root)
	stream, err := source.Bundle(t.Context(), []string{"refs/heads/main"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- source.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close blocked on unread bundle")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Bundle(t.Context(), []string{"refs/heads/main"}, nil); err == nil {
		t.Fatal("closed store started bundle")
	}
	directory := t.TempDir()
	copyTree(t, ensureBareTemplate(t), directory)
	target := objects(t, directory)
	reader, writer := io.Pipe()
	defer writer.Close()
	owned := &countCloser{ReadCloser: reader, started: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- target.Receive(ctx, owned, map[string]*string{}) }()
	select {
	case <-owned.started:
	case <-time.After(3 * time.Second):
		t.Fatal("bundle input never started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel blocked on bundle input")
	}
	if owned.closed.Load() != 1 {
		t.Fatal("input stream not released exactly once", owned.closed.Load())
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestGitObjectValidationRejectsInjectedRefsAndRevisions(t *testing.T) {
	t.Parallel()
	root, _ := initGit(t)
	store := objects(t, root)
	for _, ref := range []string{"--all", "refs/heads/a\nupdate refs/heads/main dead", "refs/heads/../main", "refs/heads/.hidden", "refs/heads/a.lock", "refs/heads/a@{1}", "refs/heads/a\\b"} {
		if _, err := store.Bundle(t.Context(), []string{ref}, nil); err == nil {
			t.Fatal("invalid bundle ref accepted", ref)
		}
		if err := store.Receive(t.Context(), nil, map[string]*string{ref: new(strings.Repeat("a", 40))}); err == nil {
			t.Fatal("invalid update accepted", ref)
		}
	}
	if _, err := store.Has(t.Context(), []string{"HEAD\nrefs/heads/main"}); err == nil {
		t.Fatal("object query injection accepted")
	}
	reader := &countCloser{ReadCloser: io.NopCloser(strings.NewReader("not a bundle"))}
	if err := store.Receive(t.Context(), reader, map[string]*string{"invalid": nil}); err == nil || reader.closed.Load() != 1 {
		t.Fatal(err, reader.closed.Load())
	}
}
