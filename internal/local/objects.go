package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

const maxObjectRefs = 100_000
const maxObjectInput = 32 << 20

var objectSHA = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)

// ObjectStore moves Git objects without loading bundles into memory. It owns
// every child process and open stream until the stream or store is closed.
type ObjectStore struct {
	directory string
	git       GitRun
	life      context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	jobs      sync.WaitGroup
	streams   map[*objectStream]struct{}
	closeDone chan struct{}
	write     chan struct{}
}

func NewObjectStore(directory string) (*ObjectStore, error) {
	absolute, err := fsutil.CanonicalPath(directory)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	return &ObjectStore{directory: absolute, git: NewGit(absolute, "file"), life: life, cancel: cancel, streams: map[*objectStream]struct{}{}, closeDone: make(chan struct{}), write: make(chan struct{}, 1)}, nil
}
func (store *ObjectStore) begin(ctx context.Context) (context.Context, func(), error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil, nil, errors.New("git object store is closed")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	stop := context.AfterFunc(store.life, cancel)
	store.jobs.Add(1)
	return ctx, func() { stop(); cancel(); store.jobs.Done() }, nil
}
func (store *ObjectStore) Capability() *sdk.GitObjects {
	return &sdk.GitObjects{Refs: store.Refs, Has: store.Has, Bundle: store.Bundle, Receive: store.Receive}
}
func refSpelling(ref string, prefix bool) bool {
	if !strings.HasPrefix(ref, "refs/") || len(ref) > 4096 || !utf8.ValidString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") || strings.HasSuffix(ref, ".") {
		return false
	}
	if prefix {
		ref = strings.TrimSuffix(ref, "/")
	} else if strings.HasSuffix(ref, "/") {
		return false
	}
	for _, r := range ref {
		if r <= 32 || r == 127 || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
func (store *ObjectStore) Refs(ctx context.Context, prefixes []string) (map[string]string, error) {
	if len(prefixes) > maxObjectRefs {
		return nil, errors.New("git ref prefix count exceeds limit")
	}
	for _, prefix := range prefixes {
		if !refSpelling(prefix, true) {
			return nil, fmt.Errorf("invalid git ref prefix %q", prefix)
		}
	}
	ctx, done, err := store.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	args := []string{"for-each-ref", "--format=%(objectname) %(refname)", "--"}
	args = append(args, prefixes...)
	result := store.git(ctx, args, GitOptions{Whole: true})
	if result.ExitCode != 0 {
		return nil, gitFailure(result, "git for-each-ref")
	}
	refs := map[string]string{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if !objectSHA.MatchString(sha) || !refSpelling(ref, false) {
			return nil, errors.New("git returned invalid refs")
		}
		refs[ref] = sha
		if len(refs) > maxObjectRefs {
			return nil, errors.New("git ref count exceeds limit")
		}
	}
	return refs, nil
}
func (store *ObjectStore) input(ctx context.Context, args []string, input io.Reader) ([]byte, error) {
	stdout := &limitedBytes{max: 64 << 20}
	stderr := &limitedBytes{max: 64 << 10}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout.cancel = cancel
	command, err := process.Start(ctx, append([]string{gitExecutable(nil)}, args...), process.Options{Directory: store.directory, ForThread: true, Env: ServerGitEnv("file"), Stdin: input, Stdout: stdout, Stderr: stderr})
	if err != nil {
		return nil, err
	}
	err = command.Wait()
	if stdout.overflow {
		return nil, errors.New("git output exceeds limit")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(stderr.data.String()))
	}
	return stdout.data.Bytes(), nil
}
func (store *ObjectStore) Has(ctx context.Context, shas []string) ([]string, error) {
	if len(shas) > maxObjectRefs {
		return nil, errors.New("git object query count exceeds limit")
	}
	for _, sha := range shas {
		if !objectSHA.MatchString(sha) {
			return nil, fmt.Errorf("invalid git object id %q", sha)
		}
	}
	ctx, done, err := store.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if len(shas) == 0 {
		return []string{}, nil
	}
	data, err := store.input(ctx, []string{"cat-file", "--batch-check=%(objectname) %(objecttype)"}, strings.NewReader(strings.Join(shas, "\n")+"\n"))
	if err != nil {
		return nil, err
	}
	present := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		sha, kind, ok := strings.Cut(line, " ")
		if ok && kind == "commit" {
			present = append(present, sha)
		}
	}
	return present, nil
}

type objectStream struct {
	reader *io.PipeReader
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}
type ownedReadCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (reader *ownedReadCloser) Close() error {
	reader.once.Do(func() { reader.err = reader.ReadCloser.Close() })
	return reader.err
}

func (stream *objectStream) Read(data []byte) (int, error) { return stream.reader.Read(data) }
func (stream *objectStream) Close() error {
	stream.once.Do(func() { stream.cancel(); stream.reader.Close() })
	<-stream.done
	return nil
}
func (store *ObjectStore) Bundle(ctx context.Context, tips, exclude []string) (io.ReadCloser, error) {
	if len(tips) == 0 {
		return nil, errors.New("git bundle needs at least one named ref")
	}
	if len(tips)+len(exclude) > maxObjectRefs {
		return nil, errors.New("git bundle revision count exceeds limit")
	}
	var input strings.Builder
	for _, ref := range tips {
		if !refSpelling(ref, false) {
			return nil, fmt.Errorf("invalid bundle ref %q", ref)
		}
		input.WriteString(ref)
		input.WriteByte('\n')
		if input.Len() > maxObjectInput {
			return nil, errors.New("git bundle revision input exceeds limit")
		}
	}
	for _, sha := range exclude {
		if !objectSHA.MatchString(sha) {
			return nil, fmt.Errorf("invalid excluded object id %q", sha)
		}
		input.WriteByte('^')
		input.WriteString(sha)
		input.WriteByte('\n')
	}
	if input.Len() > maxObjectInput {
		return nil, errors.New("git bundle revision input exceeds limit")
	}
	ctx, done, err := store.begin(ctx)
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(ctx)
	stream := &objectStream{reader: reader, cancel: cancel, done: make(chan struct{})}
	store.mu.Lock()
	if store.closed || len(store.streams) >= 128 {
		store.mu.Unlock()
		reader.Close()
		writer.Close()
		cancel()
		done()
		return nil, errors.New("git bundle stream capacity exceeded or store closed")
	}
	store.streams[stream] = struct{}{}
	store.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()); _ = reader.CloseWithError(ctx.Err()) })
	stderr := &limitedBytes{max: 64 << 10}
	command, err := process.Start(ctx, []string{gitExecutable(nil), "bundle", "create", "-", "--stdin"}, process.Options{Directory: store.directory, ForThread: true, Env: ServerGitEnv("file"), Stdin: strings.NewReader(input.String()), Stdout: writer, Stderr: stderr})
	if err != nil {
		stop()
		cancel()
		reader.Close()
		writer.Close()
		store.mu.Lock()
		delete(store.streams, stream)
		store.mu.Unlock()
		close(stream.done)
		done()
		return nil, err
	}
	go func() {
		err := command.Wait()
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil {
			err = fmt.Errorf("git bundle create failed: %s", strings.TrimSpace(stderr.data.String()))
		}
		_ = writer.CloseWithError(err)
		stop()
		cancel()
		store.mu.Lock()
		delete(store.streams, stream)
		store.mu.Unlock()
		close(stream.done)
		done()
	}()
	return stream, nil
}
func (store *ObjectStore) Receive(ctx context.Context, bundle io.ReadCloser, refs map[string]*string) error {
	var finish func()
	if bundle != nil {
		bundle = &ownedReadCloser{ReadCloser: bundle}
	}
	defer func() {
		if bundle != nil {
			_ = bundle.Close()
		}
		if finish != nil {
			finish()
		}
	}()
	if len(refs) > maxObjectRefs {
		return errors.New("git ref update count exceeds limit")
	}
	var input strings.Builder
	names := make([]string, 0, len(refs))
	for ref := range refs {
		names = append(names, ref)
	}
	slices.Sort(names)
	for _, ref := range names {
		if !refSpelling(ref, false) {
			return fmt.Errorf("invalid git ref %q", ref)
		}
		sha := refs[ref]
		if sha != nil {
			if !objectSHA.MatchString(*sha) {
				return fmt.Errorf("invalid git object id %q", *sha)
			}
			fmt.Fprintf(&input, "update %s %s\n", ref, *sha)
		} else {
			fmt.Fprintf(&input, "delete %s\n", ref)
		}
		if input.Len() > maxObjectInput {
			return errors.New("git ref update input exceeds limit")
		}
	}
	ctx, done, err := store.begin(ctx)
	if err != nil {
		return err
	}
	finish = done
	var stop func() bool
	if bundle != nil {
		stop = context.AfterFunc(ctx, func() { _ = bundle.Close() })
		defer stop()
	}
	select {
	case store.write <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-store.write }()
	if bundle != nil {
		if _, err := store.input(ctx, []string{"bundle", "unbundle", "-"}, bundle); err != nil {
			return err
		}
	}
	if input.Len() > 0 {
		_, err = store.input(ctx, []string{"update-ref", "--no-deref", "--stdin"}, strings.NewReader(input.String()))
	}
	return err
}
func (store *ObjectStore) Close() error {
	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		<-store.closeDone
		return nil
	}
	store.closed = true
	store.cancel()
	streams := make([]*objectStream, 0, len(store.streams))
	for stream := range store.streams {
		streams = append(streams, stream)
	}
	store.mu.Unlock()
	for _, stream := range streams {
		stream.Close()
	}
	store.jobs.Wait()
	close(store.closeDone)
	return nil
}
