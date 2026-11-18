package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

var scpRepository = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):([^/].*)$`)
var schemeRepository = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
var gitRepositorySuffix = regexp.MustCompile(`(?i)\.git$`)

func RepositoryOf(address string) string {
	text := textutil.Trim(address)
	host, path := "", ""
	if match := scpRepository.FindStringSubmatch(text); match != nil && !schemeRepository.MatchString(text) {
		host, path = match[1], match[2]
	} else {
		parsed, err := url.Parse(text)
		if err != nil || parsed.Host == "" {
			return ""
		}
		host = parsed.Hostname()
		if strings.ContainsRune(host, ':') {
			host = "[" + host + "]"
		}
		path = parsed.EscapedPath()
	}
	path = gitRepositorySuffix.ReplaceAllString(strings.Trim(path, "/"), "")
	if host == "" || path == "" {
		return ""
	}
	return textutil.Lower(host + "/" + path)
}

const originRefs = "refs/remotes/origin/"

type hostingSync struct {
	checkout *sdk.GitObjects
	repo     *sdk.RepoCapability
	life     context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	jobs     sync.WaitGroup
	gate     chan struct{}
}

func (syncer *hostingSync) serial(ctx context.Context, work func(context.Context) error) error {
	syncer.mu.Lock()
	if syncer.closed {
		syncer.mu.Unlock()
		return errors.New("hosting connection is closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(syncer.life, cancel)
	syncer.jobs.Add(1)
	syncer.mu.Unlock()
	defer func() { stop(); cancel(); syncer.jobs.Done() }()
	select {
	case syncer.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-syncer.gate }()
	return work(ctx)
}
func values(refs map[string]string) []string {
	set := map[string]bool{}
	for _, sha := range refs {
		set[sha] = true
	}
	out := make([]string, 0, len(set))
	for sha := range set {
		out = append(out, sha)
	}
	slices.Sort(out)
	return out
}
func (syncer *hostingSync) down(ctx context.Context) error {
	clone := syncer.repo.Objects
	if err := syncer.repo.Fetch(ctx); err != nil {
		return err
	}
	refs, err := clone.Refs(ctx, []string{originRefs})
	if err != nil {
		return err
	}
	present, err := syncer.checkout.Has(ctx, values(refs))
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, sha := range present {
		have[sha] = true
	}
	missing := []string{}
	updates := map[string]*string{}
	for ref, sha := range refs {
		updates[ref] = new(sha)
		if !have[sha] {
			missing = append(missing, ref)
		}
	}
	slices.Sort(missing)
	theirs, err := syncer.checkout.Refs(ctx, []string{originRefs, "refs/heads/"})
	if err != nil {
		return err
	}
	for ref := range theirs {
		if strings.HasPrefix(ref, originRefs) && ref != originRefs+"HEAD" {
			if _, exists := refs[ref]; !exists {
				updates[ref] = nil
			}
		}
	}
	var bundle io.ReadCloser
	if len(missing) > 0 {
		shared, err := clone.Has(ctx, values(theirs))
		if err != nil {
			return err
		}
		bundle, err = clone.Bundle(ctx, missing, shared)
		if err != nil {
			return err
		}
	}
	// Receive takes ownership even when ref validation or unpacking fails.
	return syncer.checkout.Receive(ctx, bundle, updates)
}
func (syncer *hostingSync) up(ctx context.Context, branch string) error {
	clone := syncer.repo.Objects
	ref := "refs/heads/" + branch
	refs, err := syncer.checkout.Refs(ctx, []string{ref})
	if err != nil {
		return err
	}
	sha := refs[ref]
	if sha == "" {
		return fmt.Errorf("there is no branch %s to push", branch)
	}
	has, err := clone.Has(ctx, []string{sha})
	if err != nil {
		return err
	}
	var bundle io.ReadCloser
	if len(has) == 0 {
		ours, err := clone.Refs(ctx, []string{originRefs, "refs/heads/"})
		if err != nil {
			return err
		}
		shared, err := syncer.checkout.Has(ctx, values(ours))
		if err != nil {
			return err
		}
		bundle, err = syncer.checkout.Bundle(ctx, []string{ref}, shared)
		if err != nil {
			return err
		}
	}
	return clone.Receive(ctx, bundle, map[string]*string{ref: &sha})
}

// WithHosting binds a remote checkout to a trusted hosted repository. Credentials
// stay in the hosting implementation; only Git object streams cross the link.
// On success the returned capabilities own both input connections.
func WithHosting(ctx context.Context, source, hosting sdk.Capabilities) (sdk.Capabilities, error) {
	if source.Workspace == nil || source.Workspace.Checkout == nil {
		return sdk.Capabilities{}, errors.New("the directory is not a git checkout")
	}
	checkout := source.Workspace.Checkout
	repo := hosting.Repo
	if repo == nil || repo.Objects == nil {
		return sdk.Capabilities{}, errors.New("this source cannot be used as the hosting")
	}
	validObjects := func(objects *sdk.GitObjects) bool {
		return objects != nil && objects.Refs != nil && objects.Has != nil && objects.Bundle != nil && objects.Receive != nil
	}
	if repo.Fetch == nil || repo.Push == nil || repo.DefaultBranch == nil || !validObjects(repo.Objects) || !validObjects(&checkout.Objects) {
		return sdk.Capabilities{}, errors.New("hosting repository has incomplete operations")
	}
	if checkout.OriginURL == nil || checkout.Tracking == nil {
		return sdk.Capabilities{}, errors.New("the directory is not a git checkout")
	}
	origin, err := checkout.OriginURL(ctx)
	if err != nil {
		return sdk.Capabilities{}, err
	}
	expected := RepositoryOf(repo.WebURL)
	if expected == "" {
		return sdk.Capabilities{}, errors.New("this source has no valid repository URL")
	}
	if origin == nil || RepositoryOf(*origin) != expected {
		if origin == nil {
			return sdk.Capabilities{}, fmt.Errorf("the checkout has no origin; it must be a clone of %s", repo.WebURL)
		}
		return sdk.Capabilities{}, fmt.Errorf("the checkout's origin is %s, not %s", *origin, repo.WebURL)
	}
	tracked := checkout.Tracking()
	if tracked == nil || tracked.Open == nil {
		return sdk.Capabilities{}, errors.New("the directory is not a git checkout")
	}
	life, cancel := context.WithCancel(context.Background())
	syncer := &hostingSync{checkout: &checkout.Objects, repo: repo, life: life, cancel: cancel, gate: make(chan struct{}, 1)}
	workspace := *tracked
	workspace.Open = func(ctx context.Context, input sdk.WorkspaceRequest) (sdk.Workspace, error) {
		if input.Base != nil && (input.Ref == nil || *input.Ref == "") {
			return tracked.Open(ctx, input)
		}
		if err := syncer.serial(ctx, syncer.down); err != nil {
			return nil, err
		}
		base := ""
		if input.Base != nil && *input.Base != nil {
			base = **input.Base
		} else {
			var err error
			base, err = repo.DefaultBranch(ctx)
			if err != nil {
				return nil, err
			}
		}
		input.Base = new(new(base))
		if input.Ref == nil {
			input.Ref = new(originRefs + base)
		}
		return tracked.Open(ctx, input)
	}
	hostedRepo := *repo
	hostedRepo.Fetch = func(ctx context.Context) error { return syncer.serial(ctx, syncer.down) }
	hostedRepo.Push = func(ctx context.Context, branch string) (string, error) {
		var sha string
		err := syncer.serial(ctx, func(ctx context.Context) error {
			if err := syncer.up(ctx, branch); err != nil {
				return err
			}
			var err error
			sha, err = repo.Push(ctx, branch)
			if err != nil {
				return err
			}
			return checkout.Objects.Receive(ctx, nil, map[string]*string{originRefs + branch: &sha})
		})
		return sha, err
	}
	var once sync.Once
	var closedErr error
	close := func() error {
		once.Do(func() {
			syncer.mu.Lock()
			syncer.closed = true
			cancel()
			syncer.mu.Unlock()
			if source.Close != nil {
				closedErr = errors.Join(closedErr, source.Close())
			}
			if hosting.Close != nil {
				closedErr = errors.Join(closedErr, hosting.Close())
			}
			syncer.jobs.Wait()
		})
		return closedErr
	}
	return sdk.Capabilities{Workspace: &workspace, Repo: &hostedRepo, Tracker: hosting.Tracker, Close: close}, nil
}
