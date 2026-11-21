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
	PluginSettings            func() map[string]any
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
	result := run(ctx, []string{"gh", "auth", "token", "--hostname", "github.com"}, process.RunOptions{
		Directory: root, Timeout: 10 * time.Second, ForThread: true, Env: map[string]string{"GH_PROMPT_DISABLED": "1"},
	})
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
		NeedsPluginSettings: true,
		PluginFields:        []string{"token"},
		Browse:              local.BrowseHostedDirectory,
		NewSourceDefaults:   func() map[string]any { return local.HostedSourceDefaults(options.PluginSettings) },
		Info: sdk.ContextProviderInfo{Kind: "github", Title: sdk.LocalizedText{"zh-CN": "GitHub 仓库", "en": "GitHub repository"}, Hosting: true, Provides: []string{"workspace", "repo", "tracker"}, Settings: sdk.SettingsSchema{Type: "object", FieldOrder: []string{"repository", local.HostedDirectory, "token"}, Required: []string{"repository"}, Properties: map[string]sdk.SettingField{
			"repository":          {Type: "string", Title: sdk.LocalizedText{"zh-CN": "仓库", "en": "Repository"}, Description: sdk.LocalizedText{"zh-CN": "owner/name 或仓库地址；每个讨论串在自己的分支上工作", "en": "owner/name or the URL; each thread works on its own branch"}},
			"token":               {Type: "string", Secret: true, Title: sdk.LocalizedText{"zh-CN": "访问令牌", "en": "Access token"}, Description: sdk.LocalizedText{"zh-CN": "留空时使用 GH_TOKEN、GITHUB_TOKEN 环境变量或 gh auth token", "en": "Left empty: GH_TOKEN, GITHUB_TOKEN, or gh auth token"}},
			local.HostedDirectory: local.HostedDirectoryField(),
		}}},
		ValidateSettings: func(values, previous map[string]any) error {
			if previous == nil {
				return nil
			}
			repository, _ := previous["repository"].(string)
			owner, name, err := ParseRepository(repository)
			if err != nil {
				return err
			}
			return local.CheckHostedDirectoryChange(options.DataDir, owner+"/"+name, nil, values[local.HostedDirectory], previous[local.HostedDirectory])
		},
		Connect: func(ctx context.Context, values map[string]any) (sdk.Capabilities, error) {
			repository, _ := values["repository"].(string)
			token, _ := values["token"].(string)
			if token == "" && options.PluginSettings != nil {
				token, _ = options.PluginSettings()["token"].(string)
			}
			dataDir, err := local.HostedDataDir(options.DataDir, values[local.HostedDirectory])
			if err != nil {
				return sdk.Capabilities{}, err
			}
			configured := options
			configured.DataDir = dataDir
			connected, err := Connect(Settings{Repository: repository, Token: token}, configured)
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

func PluginSchema() *sdk.SettingsSchema {
	return &sdk.SettingsSchema{Type: "object", FieldOrder: []string{"token", local.HostedDirectory}, Properties: map[string]sdk.SettingField{
		"token": {Type: "string", Secret: true, Title: sdk.LocalizedText{"zh-CN": "Token", "en": "Token"}, Description: sdk.LocalizedText{
			"zh-CN": "留空时使用运行 Ariadne 的机器上的 GH_TOKEN、GITHUB_TOKEN 或 GitHub CLI 登录。",
			"en":    "Leave blank to use GH_TOKEN, GITHUB_TOKEN, or the GitHub CLI login on the machine running Ariadne.",
		}},
		local.HostedDirectory: local.DefaultHostedDirectoryField(),
	}}
}

func PluginReadiness(options Options) (func(context.Context, map[string]any) bool, func() error) {
	available := hostinghttp.NewMemo(5*time.Second, func(ctx context.Context) (bool, error) {
		token, err := FindToken(ctx, "", environment(options.Env), options.Run)
		return token != nil, err
	}, nil)
	return func(ctx context.Context, values map[string]any) bool {
		if token, _ := values["token"].(string); textutil.Trim(token) != "" {
			return true
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ready, _ := available.Get(ctx)
		return ready
	}, available.Close
}

type PullURL struct {
	Owner, Name string
	Number      int
}

var linkPath = regexp.MustCompile(`^/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)/(pull|issues)/(\d{1,9})(?:/[\w/.-]*)?$`)

func ParsePullRequestURL(address *url.URL, web string) *PullURL {
	link, kind := parseLinkURL(address, web)
	if kind != "pull-request" {
		return nil
	}
	return link
}

func parseLinkURL(address *url.URL, web string) (*PullURL, string) {
	home, err := url.Parse(web)
	if address == nil || err != nil || address.Scheme != "https" || !strings.EqualFold(address.Host, home.Host) {
		return nil, ""
	}
	prefix := strings.TrimRight(home.EscapedPath(), "/")
	path := address.EscapedPath()
	if prefix != "" && !strings.HasPrefix(path, prefix+"/") {
		return nil, ""
	}
	match := linkPath.FindStringSubmatch(strings.TrimPrefix(path, prefix))
	if match == nil {
		return nil, ""
	}
	var number int
	fmt.Sscanf(match[4], "%d", &number)
	kind := "pull-request"
	if match[3] == "issues" {
		kind = "issue"
	}
	return &PullURL{Owner: match[1], Name: match[2], Number: number}, kind
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
	return sdk.LinkProvider{Kind: "github", Title: sdk.LocalizedText{"zh-CN": "GitHub 链接", "en": "GitHub links"},
		Match: func(address *url.URL) *sdk.LinkMatch {
			link, kind := parseLinkURL(address, web)
			if link == nil {
				return nil
			}
			repository := link.Owner + "/" + link.Name
			reference := fmt.Sprintf("#%d", link.Number)
			match := &sdk.LinkMatch{Kind: kind, Label: repository + reference}
			if kind == "issue" {
				match.Reference = reference
				match.RepositoryURL = strings.TrimRight(web, "/") + "/" + repository
			}
			return match
		},
		Preview: func(ctx context.Context, address *url.URL) (*sdk.LinkPreview, error) {
			link, kind := parseLinkURL(address, web)
			if link == nil {
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
			credentials := token.Get
			if options.PluginSettings != nil {
				if value, _ := options.PluginSettings()["token"].(string); value != "" {
					credentials = func(context.Context) (*string, error) { return &value, nil }
				}
			}
			api, err := NewAPI(APIOptions{Base: options.APIBase, Owner: link.Owner, Name: link.Name, Token: credentials, Client: options.Client})
			if err != nil {
				return nil, err
			}
			defer api.Close()
			read := api.Preview
			if kind == "issue" {
				read = api.PreviewIssue
			}
			preview, err := read(ctx, link.Number)
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
