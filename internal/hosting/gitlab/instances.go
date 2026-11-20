package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daidr/Ariadne/internal/cache"
	"github.com/daidr/Ariadne/internal/hostinghttp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

// Instances contains credentials held by source connections and explicit seeds.
// Closing a source lease removes its credentials. Close releases all entries.
// The separate environment/CLI token cache is bounded and expires after 20 min.
type Instances struct {
	mu               sync.RWMutex
	entries          map[string]*instanceEntry
	order            []string
	projects, weight int
	closed           bool
	next             atomic.Uint64
}
type instanceEntry struct {
	seed   bool
	tokens map[string]string
	order  []string
}

func NewInstances(env map[string]string) *Instances {
	instances := &Instances{entries: map[string]*instanceEntry{}, order: []string{GitLabCom}}
	instances.entries[GitLabCom] = &instanceEntry{seed: true, tokens: map[string]string{}}
	if host := textutil.Trim(environment(env)["GITLAB_HOST"]); host != "" {
		if !strings.Contains(host, "://") {
			host = "https://" + host
		}
		if instance, err := ParseInstance(host); err == nil && instance != GitLabCom {
			instances.entries[instance] = &instanceEntry{seed: true, tokens: map[string]string{}}
			instances.order = append(instances.order, instance)
		}
	}
	return instances
}
func (instances *Instances) Add(instance, path, token string) error {
	return instances.add(instance, path, path, token)
}

// Register ties a credential to one connection, including temporary validation
// connections. Projects using the same repository may have separate tokens.
func (instances *Instances) Register(instance, path, token string) (func(), error) {
	key := path + "\x00" + strconv.FormatUint(instances.next.Add(1), 10)
	if err := instances.add(instance, path, key, token); err != nil {
		return nil, err
	}
	return func() { instances.Remove(instance, key) }, nil
}

func (instances *Instances) add(instance, path, key, token string) error {
	normalized, err := ParseInstance(instance)
	if err != nil {
		return err
	}
	if _, err := checkPath(path, path); err != nil {
		return err
	}
	instance = normalized
	token = textutil.Trim(token)
	if len(token) > 64*1024 {
		return errors.New("GitLab token exceeds 64 KiB")
	}
	instances.mu.Lock()
	defer instances.mu.Unlock()
	if instances.closed {
		return errors.New("GitLab instances are closed")
	}
	entry := instances.entries[instance]
	if entry == nil && len(instances.entries) >= 128 {
		return errors.New("too many GitLab instances")
	}
	old := ""
	exists := false
	if entry != nil {
		old, exists = entry.tokens[key]
	}
	if !exists && instances.projects >= 4096 {
		return errors.New("too many GitLab projects")
	}
	weight := instances.weight - len(old) + len(token)
	if !exists {
		weight += len(instance) + len(key)
	}
	if weight > 2*1024*1024 {
		return errors.New("GitLab project credentials exceed 2 MiB")
	}
	if entry == nil {
		entry = &instanceEntry{tokens: map[string]string{}}
		instances.entries[instance] = entry
		instances.order = append(instances.order, instance)
	}
	if !exists {
		instances.projects++
		entry.order = append(entry.order, key)
	}
	entry.tokens[key] = token
	instances.weight = weight
	return nil
}
func (instances *Instances) Remove(instance, path string) {
	normalized, err := ParseInstance(instance)
	if err != nil {
		return
	}
	instance = normalized
	instances.mu.Lock()
	defer instances.mu.Unlock()
	entry := instances.entries[instance]
	if entry == nil {
		return
	}
	token, exists := entry.tokens[path]
	if !exists {
		return
	}
	delete(entry.tokens, path)
	instances.projects--
	instances.weight -= len(instance) + len(path) + len(token)
	for i, item := range entry.order {
		if item == path {
			entry.order = slices.Delete(entry.order, i, i+1)
			break
		}
	}
	if len(entry.tokens) == 0 && !entry.seed {
		delete(instances.entries, instance)
		for i, item := range instances.order {
			if item == instance {
				instances.order = slices.Delete(instances.order, i, i+1)
				break
			}
		}
	}
}

type InstanceMatch struct {
	Instance string   `json:"instance"`
	Tokens   []string `json:"tokens"`
	Path     string   `json:"path"`
	IID      int      `json:"iid"`
}

func (instances *Instances) Find(address *url.URL) *InstanceMatch {
	return instances.find(address, true)
}
func (instances *Instances) find(address *url.URL, credentials bool) *InstanceMatch {
	instances.mu.RLock()
	defer instances.mu.RUnlock()
	if instances.closed {
		return nil
	}
	var found *InstanceMatch
	for _, instance := range instances.order {
		mr := ParseMergeRequestURL(address, instance)
		if mr == nil || found != nil && len(instance) <= len(found.Instance) {
			continue
		}
		entry := instances.entries[instance]
		tokens := []string{}
		seen := map[string]bool{}
		if credentials {
			for _, own := range []bool{true, false} {
				for _, key := range entry.order {
					path, _, _ := strings.Cut(key, "\x00")
					token := entry.tokens[key]
					if (path == mr.Path) == own && token != "" && !seen[token] {
						tokens = append(tokens, token)
						seen[token] = true
					}
				}
			}
		}
		found = &InstanceMatch{Instance: instance, Tokens: tokens, Path: mr.Path, IID: mr.IID}
	}
	return found
}
func (instances *Instances) Close() error {
	instances.mu.Lock()
	defer instances.mu.Unlock()
	instances.closed = true
	clear(instances.entries)
	instances.order = nil
	instances.projects = 0
	instances.weight = 0
	return nil
}

func Links(options Options) sdk.LinkProvider {
	instances := options.Instances
	owned := instances == nil
	if instances == nil {
		instances = NewInstances(options.Env)
	}
	fallbackCache, _ := cache.New[string, string](cache.Options{MaxEntries: 32, MaxWeight: 1024 * 1024, TTL: 20 * time.Minute})
	life, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var jobs sync.WaitGroup
	closed := false
	active := 0
	done := make(chan struct{})
	type lookup struct {
		done  chan struct{}
		token *string
		err   error
	}
	pending := map[string]*lookup{}
	fallback := func(ctx context.Context, instance string) (*string, error) {
		if token, ok := fallbackCache.Get(instance); ok {
			return &token, nil
		}
		mu.Lock()
		if closed {
			mu.Unlock()
			return nil, errors.New("GitLab link provider is closed")
		}
		running := pending[instance]
		if running == nil {
			running = &lookup{done: make(chan struct{})}
			pending[instance] = running
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				ctx, stop := context.WithTimeout(life, 10*time.Second)
				defer stop()
				running.token, running.err = FindToken(ctx, "", instance, environment(options.Env), options.Run)
				mu.Lock()
				defer mu.Unlock()
				if !closed && running.err == nil && running.token != nil {
					fallbackCache.Put(instance, *running.token, int64(len(instance)+len(*running.token)))
				}
				delete(pending, instance)
				close(running.done)
			}()
		}
		mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-running.done:
			return running.token, running.err
		}
	}
	return sdk.LinkProvider{Kind: "gitlab", Title: sdk.LocalizedText{"zh-CN": "GitLab 合并请求", "en": "GitLab merge requests"}, Match: func(address *url.URL) *sdk.LinkMatch {
		mr := instances.find(address, false)
		if mr == nil {
			return nil
		}
		return &sdk.LinkMatch{Kind: "pull-request", Label: fmt.Sprintf("%s!%d", mr.Path, mr.IID)}
	},
		Preview: func(ctx context.Context, address *url.URL) (*sdk.PullRequestPreview, error) {
			mr := instances.Find(address)
			if mr == nil {
				return nil, nil
			}
			mu.Lock()
			if closed || active >= 128 {
				mu.Unlock()
				return nil, errors.New("GitLab link provider is closed or busy")
			}
			active++
			jobs.Add(1)
			mu.Unlock()
			ctx, stop := context.WithTimeout(ctx, 10*time.Second)
			after := context.AfterFunc(life, stop)
			defer func() {
				after()
				stop()
				mu.Lock()
				active--
				mu.Unlock()
				jobs.Done()
			}()
			read := func(token func(context.Context) (*string, error)) (*sdk.PullRequestPreview, error) {
				api, err := NewAPI(APIOptions{Base: mr.Instance + "/api/v4", Path: mr.Path, WebURL: mr.Instance + "/" + mr.Path, Token: token, Client: options.Client})
				if err != nil {
					return nil, err
				}
				defer api.Close()
				return api.Preview(ctx, mr.IID)
			}
			if value := pluginToken(options, mr.Instance); value != "" && !slices.Contains(mr.Tokens, value) {
				mr.Tokens = append(mr.Tokens, value)
			}
			for i := 0; i <= len(mr.Tokens); i++ {
				token := func(ctx context.Context) (*string, error) { return fallback(ctx, mr.Instance) }
				if i < len(mr.Tokens) {
					value := mr.Tokens[i]
					token = func(context.Context) (*string, error) { return &value, nil }
				}
				preview, err := read(token)
				if err == nil {
					return preview, nil
				}
				var failure *hostinghttp.Error
				if !errors.As(err, &failure) || failure.Status != 401 && failure.Status != 403 && failure.Status != 404 {
					return nil, err
				}
			}
			return nil, nil
		}, Close: func() error {
			mu.Lock()
			if closed {
				mu.Unlock()
				<-done
				return nil
			}
			closed = true
			cancel()
			mu.Unlock()
			jobs.Wait()
			options = Options{}
			fallbackCache.Close()
			if owned {
				instances.Close()
			}
			close(done)
			return nil
		},
	}
}
