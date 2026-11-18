package workspaces

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

func withRepo[T any](ctx context.Context, manager *Manager, project, key string, work func(context.Context, *sdk.RepoCapability) (T, error)) (T, error) {
	return withSource(ctx, manager, project, key, func(ctx context.Context, caps sdk.Capabilities) (T, error) {
		if caps.Repo == nil {
			var zero T
			return zero, domainerr.New("error.noRepo", nil)
		}
		return work(ctx, caps.Repo)
	})
}
func withTracker[T any](ctx context.Context, manager *Manager, project, key string, work func(context.Context, *sdk.TrackerCapability) (T, error)) (T, error) {
	return withSource(ctx, manager, project, key, func(ctx context.Context, caps sdk.Capabilities) (T, error) {
		if caps.Tracker == nil {
			var zero T
			return zero, domainerr.New("error.noTracker", nil)
		}
		return work(ctx, caps.Tracker)
	})
}
func (manager *Manager) Repo(ctx context.Context, project string) (*sdk.RepoCapability, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return nil, err
	}
	key := config.key
	raw, err := withRepo(ctx, manager, project, key, func(_ context.Context, repo *sdk.RepoCapability) (sdk.RepoCapability, error) { return *repo, nil })
	if err != nil {
		return nil, err
	}
	proxy := &sdk.RepoCapability{Remote: strings.Clone(raw.Remote), WebURL: strings.Clone(raw.WebURL)}
	if raw.DefaultBranch != nil {
		proxy.DefaultBranch = func(ctx context.Context) (string, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (string, error) { return repo.DefaultBranch(ctx) })
		}
	}
	if raw.IsPublic != nil {
		proxy.IsPublic = func(ctx context.Context) (bool, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (bool, error) { return repo.IsPublic(ctx) })
		}
	}
	if raw.Fetch != nil {
		proxy.Fetch = func(ctx context.Context) error {
			_, err := withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (struct{}, error) {
				return struct{}{}, repo.Fetch(ctx)
			})
			return err
		}
	}
	if raw.Push != nil {
		proxy.Push = func(ctx context.Context, branch string) (string, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (string, error) { return repo.Push(ctx, branch) })
		}
	}
	if raw.PullRequestFor != nil {
		proxy.PullRequestFor = func(ctx context.Context, branch string) (*sdk.PullRequestInfo, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (*sdk.PullRequestInfo, error) {
				return repo.PullRequestFor(ctx, branch)
			})
		}
	}
	if raw.PullRequest != nil {
		proxy.PullRequest = func(ctx context.Context, number int) (sdk.PullRequestInfo, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (sdk.PullRequestInfo, error) {
				return repo.PullRequest(ctx, number)
			})
		}
	}
	if raw.OpenPullRequest != nil {
		proxy.OpenPullRequest = func(ctx context.Context, input sdk.OpenPullRequest) (sdk.PullRequestInfo, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (sdk.PullRequestInfo, error) {
				return repo.OpenPullRequest(ctx, input)
			})
		}
	}
	if raw.Checks != nil {
		proxy.Checks = func(ctx context.Context, sha string) ([]sdk.CheckInfo, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) ([]sdk.CheckInfo, error) {
				return repo.Checks(ctx, sha)
			})
		}
	}
	if raw.MergePullRequest != nil {
		proxy.MergePullRequest = func(ctx context.Context, number int, method, sha string) (sdk.PullRequestMergeOutcome, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (sdk.PullRequestMergeOutcome, error) {
				return repo.MergePullRequest(ctx, number, method, sha)
			})
		}
	}
	if raw.CheckLog != nil {
		proxy.CheckLog = func(ctx context.Context, id string) (string, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (string, error) { return repo.CheckLog(ctx, id) })
		}
	}
	if raw.Objects != nil {
		proxy.Objects = manager.objects(project, key, *raw.Objects)
	}
	return proxy, nil
}
func (manager *Manager) Tracker(ctx context.Context, project string) (*sdk.TrackerCapability, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return nil, err
	}
	key := config.key
	raw, err := withTracker(ctx, manager, project, key, func(_ context.Context, tracker *sdk.TrackerCapability) (sdk.TrackerCapability, error) {
		return *tracker, nil
	})
	if err != nil {
		return nil, err
	}
	proxy := &sdk.TrackerCapability{Name: strings.Clone(raw.Name), WebURL: strings.Clone(raw.WebURL)}
	if raw.List != nil {
		proxy.List = func(ctx context.Context, query sdk.IssueQuery) ([]sdk.IssueSummary, error) {
			return withTracker(ctx, manager, project, key, func(ctx context.Context, tracker *sdk.TrackerCapability) ([]sdk.IssueSummary, error) {
				return tracker.List(ctx, query)
			})
		}
	}
	if raw.Issue != nil {
		proxy.Issue = func(ctx context.Context, number int) (sdk.IssueDetail, error) {
			return withTracker(ctx, manager, project, key, func(ctx context.Context, tracker *sdk.TrackerCapability) (sdk.IssueDetail, error) {
				return tracker.Issue(ctx, number)
			})
		}
	}
	if raw.Comment != nil {
		proxy.Comment = func(ctx context.Context, number int, body string) (sdk.IssueComment, error) {
			return withTracker(ctx, manager, project, key, func(ctx context.Context, tracker *sdk.TrackerCapability) (sdk.IssueComment, error) {
				return tracker.Comment(ctx, number, body)
			})
		}
	}
	if raw.SetState != nil {
		proxy.SetState = func(ctx context.Context, number int, state, reason string) (sdk.IssueSummary, error) {
			return withTracker(ctx, manager, project, key, func(ctx context.Context, tracker *sdk.TrackerCapability) (sdk.IssueSummary, error) {
				return tracker.SetState(ctx, number, state, reason)
			})
		}
	}
	return proxy, nil
}

type ownedReader struct {
	reader  io.ReadCloser
	release func()
	mu      sync.Mutex
	once    sync.Once
	stop    func() bool
	err     error
}

func newOwnedReader(ctx context.Context, reader io.ReadCloser, release func()) *ownedReader {
	owned := &ownedReader{reader: reader, release: release}
	owned.mu.Lock()
	owned.stop = context.AfterFunc(ctx, func() { owned.Close() })
	owned.mu.Unlock()
	return owned
}
func (reader *ownedReader) Read(data []byte) (int, error) {
	reader.mu.Lock()
	raw := reader.reader
	reader.mu.Unlock()
	if raw == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := raw.Read(data)
	if err != nil {
		reader.Close()
	}
	return n, err
}
func (reader *ownedReader) Close() error {
	reader.once.Do(func() {
		reader.mu.Lock()
		stop := reader.stop
		reader.stop = nil
		raw := reader.reader
		reader.reader = nil
		reader.mu.Unlock()
		defer func() {
			if reader.release != nil {
				reader.release()
				reader.release = nil
			}
		}()
		defer func() {
			if recover() != nil {
				reader.err = errors.New("bundle stream close failed")
			}
		}()
		if stop != nil {
			stop()
		}
		reader.err = raw.Close()
	})
	return reader.err
}

type onceReader struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (reader *onceReader) Close() error {
	reader.once.Do(func() { reader.err = reader.ReadCloser.Close() })
	return reader.err
}
func (manager *Manager) objects(project, key string, available sdk.GitObjects) *sdk.GitObjects {
	proxy := &sdk.GitObjects{}
	if available.Refs != nil {
		proxy.Refs = func(ctx context.Context, prefixes []string) (map[string]string, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (map[string]string, error) {
				return repo.Objects.Refs(ctx, prefixes)
			})
		}
	}
	if available.Has != nil {
		proxy.Has = func(ctx context.Context, shas []string) ([]string, error) {
			return withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) ([]string, error) {
				return repo.Objects.Has(ctx, shas)
			})
		}
	}
	if available.Bundle != nil {
		proxy.Bundle = func(ctx context.Context, tips, exclude []string) (io.ReadCloser, error) {
			ctx, done, err := manager.begin(ctx)
			if err != nil {
				return nil, err
			}
			lease, err := manager.acquireSource(ctx, project, key)
			if err != nil {
				done()
				return nil, err
			}
			ctx, stop := lease.Context(ctx)
			repo := lease.Value.caps.Repo
			if repo == nil || repo.Objects == nil || repo.Objects.Bundle == nil {
				stop()
				lease.Release()
				done()
				return nil, domainerr.New("error.noRepo", nil)
			}
			reader, err := repo.Objects.Bundle(ctx, tips, exclude)
			if err != nil {
				stop()
				lease.Release()
				done()
				return nil, err
			}
			if reader == nil {
				stop()
				lease.Release()
				done()
				return nil, errors.New("repository returned no bundle stream")
			}
			return newOwnedReader(ctx, reader, func() { stop(); lease.Release(); done() }), nil
		}
	}
	if available.Receive != nil {
		proxy.Receive = func(ctx context.Context, input io.ReadCloser, refs map[string]*string) error {
			var reader *onceReader
			if input != nil {
				reader = &onceReader{ReadCloser: input}
				defer reader.Close()
			}
			_, err := withRepo(ctx, manager, project, key, func(ctx context.Context, repo *sdk.RepoCapability) (struct{}, error) {
				var body io.ReadCloser
				if reader != nil {
					body = reader
				}
				return struct{}{}, repo.Objects.Receive(ctx, body, refs)
			})
			return err
		}
	}
	return proxy
}
