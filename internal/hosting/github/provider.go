package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/hostinghttp"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type Settings struct{ Repository, Token string }
type Options struct {
	DataDir, APIBase, GitBase string
	Client                    *http.Client
	Env                       map[string]string
	Run                       func(context.Context, []string, process.RunOptions) sdk.ExecResult
	Clone                     local.Cloner
	DisableClone              bool
}

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var repositoryPrefix = regexp.MustCompile(`^(?:https?://github\.com/|git@github\.com:)`)

func ParseRepository(input string) (owner, name string, err error) {
	text := repositoryPrefix.ReplaceAllString(textutil.Trim(input), "")
	text = strings.TrimRight(strings.TrimSuffix(text, ".git"), "/")
	parts := strings.Split(text, "/")
	if len(parts) != 2 || !ownerPattern.MatchString(parts[0]) || !namePattern.MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." {
		return "", "", fmt.Errorf("\"%s\" is not a GitHub repository (owner/name)", input)
	}
	return parts[0], parts[1], nil
}
func environment(given map[string]string) map[string]string {
	copy := map[string]string{}
	if given != nil {
		for key, value := range given {
			copy[key] = value
		}
		return copy
	}
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			copy[key] = value
		}
	}
	return copy
}
func FindToken(ctx context.Context, configured string, env map[string]string, run func(context.Context, []string, process.RunOptions) sdk.ExecResult) (*string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, candidate := range []string{configured, env["GH_TOKEN"], env["GITHUB_TOKEN"]} {
		if token := textutil.Trim(candidate); token != "" {
			return &token, nil
		}
	}
	if run == nil {
		run = process.Run
	}
	root := string(filepath.Separator)
	if cwd, err := os.Getwd(); err == nil {
		root = filepath.VolumeName(cwd) + string(filepath.Separator)
	}
	result := run(ctx, []string{"gh", "auth", "token"}, process.RunOptions{Directory: root, Timeout: 10 * time.Second, ForThread: true, Env: map[string]string{"GH_TOKEN": env["GH_TOKEN"], "GITHUB_TOKEN": env["GITHUB_TOKEN"], "GH_HOST": env["GH_HOST"]}})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if token := textutil.Trim(result.Stdout); result.ExitCode == 0 && token != "" {
		return &token, nil
	}
	return nil, nil
}
func tokenMemo(configured string, options Options) *hostinghttp.Memo[*string] {
	env := environment(options.Env)
	return hostinghttp.NewMemo(20*time.Minute, func(ctx context.Context) (*string, error) { return FindToken(ctx, configured, env, options.Run) }, func(token *string) bool { return token != nil })
}

type Connection struct {
	Capabilities sdk.Capabilities
	API          *API
}

func Connect(settings Settings, options Options) (*Connection, error) {
	owner, name, err := ParseRepository(settings.Repository)
	if err != nil {
		return nil, err
	}
	token := tokenMemo(settings.Token, options)
	api, err := NewAPI(APIOptions{Base: options.APIBase, Owner: owner, Name: name, Token: token.Get, Client: options.Client})
	if err != nil {
		token.Close()
		return nil, err
	}
	metadata := hostinghttp.NewMemo(5*time.Minute, api.Repository, nil)
	defaultBranch := func(ctx context.Context) (string, error) {
		repository, err := metadata.Get(ctx)
		return repository.DefaultBranch, err
	}
	gitBase := options.GitBase
	if gitBase == "" {
		gitBase = "https://github.com"
	}
	gitBase = strings.TrimRight(gitBase, "/")
	clone, err := local.NewHosted(local.HostedOptions{DataDir: options.DataDir, Path: owner + "/" + name, RemoteURL: gitBase + "/" + owner + "/" + name + ".git", User: "x-access-token", Token: token.Get, DefaultBranch: defaultBranch, Clone: options.Clone, DisableClone: options.DisableClone})
	if err != nil {
		metadata.Close()
		api.Close()
		token.Close()
		return nil, err
	}
	web := "https://github.com"
	if strings.HasPrefix(gitBase, "http") {
		web = gitBase
	}
	repo := &sdk.RepoCapability{Remote: owner + "/" + name, WebURL: web + "/" + owner + "/" + name, DefaultBranch: defaultBranch, IsPublic: func(ctx context.Context) (bool, error) {
		repository, err := metadata.Get(ctx)
		return !repository.Private, err
	}, Fetch: clone.Fetch, Push: clone.Push, Objects: clone.Objects(), PullRequestFor: api.PullRequestFor, PullRequest: api.PullRequest, Checks: api.Checks, MergePullRequest: api.MergePullRequest, CheckLog: api.CheckLog}
	repo.OpenPullRequest = func(ctx context.Context, input sdk.OpenPullRequest) (sdk.PullRequestInfo, error) {
		if input.Base == nil {
			base, err := defaultBranch(ctx)
			if err != nil {
				return sdk.PullRequestInfo{}, err
			}
			input.Base = &base
		}
		return api.OpenPullRequest(ctx, input)
	}
	tracker := &sdk.TrackerCapability{Name: repo.Remote, WebURL: repo.WebURL + "/issues", List: api.Issues, Issue: api.Issue, Comment: api.Comment, SetState: api.SetIssueState}
	var once sync.Once
	var closeErr error
	close := func() error {
		once.Do(func() { closeErr = errors.Join(clone.Close(), metadata.Close(), api.Close(), token.Close()) })
		return closeErr
	}
	return &Connection{API: api, Capabilities: sdk.Capabilities{Workspace: clone.Capability(), Repo: repo, Tracker: tracker, Close: close}}, nil
}
func Provider(options Options) sdk.ContextProvider {
	return sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "github", Title: sdk.LocalizedText{"zh-CN": "GitHub 仓库", "en": "GitHub repository"}, Hosting: true, Provides: []string{"workspace", "repo", "tracker"}, Settings: sdk.SettingsSchema{Type: "object", FieldOrder: []string{"repository", "token"}, Required: []string{"repository"}, Properties: map[string]sdk.SettingField{
			"repository": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "仓库", "en": "Repository"}, Description: sdk.LocalizedText{"zh-CN": "owner/name 或仓库地址；每个讨论串在自己的分支上工作", "en": "owner/name or the URL; each thread works on its own branch"}},
			"token":      {Type: "string", Secret: true, Title: sdk.LocalizedText{"zh-CN": "访问令牌", "en": "Access token"}, Description: sdk.LocalizedText{"zh-CN": "留空时使用 GH_TOKEN、GITHUB_TOKEN 环境变量或 gh auth token", "en": "Left empty: GH_TOKEN, GITHUB_TOKEN, or gh auth token"}},
		}}},
		Connect: func(ctx context.Context, values map[string]any) (sdk.Capabilities, error) {
			repository, _ := values["repository"].(string)
			token, _ := values["token"].(string)
			connected, err := Connect(Settings{Repository: repository, Token: token}, options)
			if err != nil {
				return sdk.Capabilities{}, err
			}
			if _, err := connected.Capabilities.Repo.DefaultBranch(ctx); err != nil {
				connected.Capabilities.Close()
				return sdk.Capabilities{}, err
			}
			return connected.Capabilities, nil
		},
	}
}

type PullURL struct {
	Owner, Name string
	Number      int
}

var pullPath = regexp.MustCompile(`^/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)/pull/(\d{1,9})(?:/[\w/.-]*)?$`)

func ParsePullRequestURL(address *url.URL, web string) *PullURL {
	home, err := url.Parse(web)
	if address == nil || err != nil || address.Scheme != "https" || !strings.EqualFold(address.Host, home.Host) {
		return nil
	}
	prefix := strings.TrimRight(home.EscapedPath(), "/")
	path := address.EscapedPath()
	if prefix != "" && !strings.HasPrefix(path, prefix+"/") {
		return nil
	}
	match := pullPath.FindStringSubmatch(strings.TrimPrefix(path, prefix))
	if match == nil {
		return nil
	}
	var number int
	fmt.Sscanf(match[3], "%d", &number)
	return &PullURL{Owner: match[1], Name: match[2], Number: number}
}
func Links(options Options) sdk.LinkProvider {
	web := "https://github.com"
	if strings.HasPrefix(options.GitBase, "https://") {
		web = options.GitBase
	}
	token := tokenMemo("", options)
	life, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var jobs sync.WaitGroup
	closed := false
	active := 0
	done := make(chan struct{})
	return sdk.LinkProvider{Kind: "github", Title: sdk.LocalizedText{"zh-CN": "GitHub 拉取请求", "en": "GitHub pull requests"},
		Match: func(address *url.URL) *sdk.LinkMatch {
			pull := ParsePullRequestURL(address, web)
			if pull == nil {
				return nil
			}
			return &sdk.LinkMatch{Kind: "pull-request", Label: fmt.Sprintf("%s/%s#%d", pull.Owner, pull.Name, pull.Number)}
		},
		Preview: func(ctx context.Context, address *url.URL) (*sdk.PullRequestPreview, error) {
			pull := ParsePullRequestURL(address, web)
			if pull == nil {
				return nil, nil
			}
			mu.Lock()
			if closed || active >= 128 {
				mu.Unlock()
				return nil, errors.New("GitHub link provider is closed or busy")
			}
			jobs.Add(1)
			active++
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
			api, err := NewAPI(APIOptions{Base: options.APIBase, Owner: pull.Owner, Name: pull.Name, Token: token.Get, Client: options.Client})
			if err != nil {
				return nil, err
			}
			defer api.Close()
			preview, err := api.Preview(ctx, pull.Number)
			var failure *hostinghttp.Error
			if errors.As(err, &failure) && failure.Status == 404 {
				return nil, nil
			}
			return preview, err
		},
		Close: func() error {
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
			err := token.Close()
			close(done)
			return err
		},
	}
}
