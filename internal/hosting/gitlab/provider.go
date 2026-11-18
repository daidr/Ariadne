package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/hostinghttp"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type Settings struct{ Repository, Instance, Token string }
type Options struct {
	DataDir, GitBase string
	Client           *http.Client
	Env              map[string]string
	Run              func(context.Context, []string, process.RunOptions) sdk.ExecResult
	Clone            local.Cloner
	DisableClone     bool
	Instances        *Instances
}

func environment(given map[string]string) map[string]string {
	values := map[string]string{}
	if given != nil {
		for key, value := range given {
			values[key] = value
		}
		return values
	}
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	return values
}
func FindToken(ctx context.Context, configured, instance string, env map[string]string, run func(context.Context, []string, process.RunOptions) sdk.ExecResult) (*string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if configured = textutil.Trim(configured); configured != "" {
		return &configured, nil
	}
	address, err := webURL(instance)
	if err != nil {
		return nil, err
	}
	host := address.Host
	envHost := "gitlab.com"
	if value := textutil.Trim(env["GITLAB_HOST"]); value != "" {
		if !strings.Contains(value, "://") {
			value = "https://" + value
		}
		if given, err := webURL(value); err == nil {
			envHost = given.Host
		} else {
			envHost = ""
		}
	}
	if envHost == host {
		for _, candidate := range []string{env["GITLAB_TOKEN"], env["GITLAB_ACCESS_TOKEN"], env["GL_TOKEN"]} {
			if token := textutil.Trim(candidate); token != "" {
				return &token, nil
			}
		}
	}
	if run == nil {
		run = process.Run
	}
	root := string(filepath.Separator)
	if cwd, err := os.Getwd(); err == nil {
		root = filepath.VolumeName(cwd) + string(filepath.Separator)
	}
	result := run(ctx, []string{"glab", "config", "get", "token", "--host", host}, process.RunOptions{Directory: root, Timeout: 10 * time.Second, ForThread: true, Env: map[string]string{"GITLAB_TOKEN": "", "GITLAB_ACCESS_TOKEN": "", "OAUTH_TOKEN": "", "GL_TOKEN": "", "CI_JOB_TOKEN": ""}})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if token := textutil.Trim(result.Stdout); result.ExitCode == 0 && token != "" {
		return &token, nil
	}
	return nil, nil
}
func tokenMemo(configured, instance string, options Options) *hostinghttp.Memo[*string] {
	env := environment(options.Env)
	return hostinghttp.NewMemo(20*time.Minute, func(ctx context.Context) (*string, error) {
		return FindToken(ctx, configured, instance, env, options.Run)
	}, func(value *string) bool { return value != nil })
}

type Connection struct {
	Capabilities   sdk.Capabilities
	API            *API
	Instance, Path string
}

func Connect(settings Settings, options Options) (*Connection, error) {
	parsed, err := ParseRepository(settings.Repository, settings.Instance)
	if err != nil {
		return nil, err
	}
	token := tokenMemo(settings.Token, parsed.Instance, options)
	web := parsed.Instance + "/" + parsed.Path
	api, err := NewAPI(APIOptions{Base: parsed.Instance + "/api/v4", Path: parsed.Path, WebURL: web, Token: token.Get, Client: options.Client})
	if err != nil {
		token.Close()
		return nil, err
	}
	metadata := hostinghttp.NewMemo(5*time.Minute, api.Project, nil)
	defaultBranch := func(ctx context.Context) (string, error) {
		project, err := metadata.Get(ctx)
		if err != nil {
			return "", err
		}
		if project.DefaultBranch == nil || *project.DefaultBranch == "" {
			return "", fmt.Errorf("%s has no default branch yet: push a first commit to it", parsed.Path)
		}
		return *project.DefaultBranch, nil
	}
	gitBase := parsed.Instance
	if options.GitBase != "" {
		gitBase = strings.TrimRight(options.GitBase, "/")
	}
	clone, err := local.NewHosted(local.HostedOptions{DataDir: options.DataDir, Path: parsed.Path, Namespace: InstanceDirs(parsed.Instance), RemoteURL: gitBase + "/" + parsed.Path + ".git", User: "oauth2", Token: token.Get, DefaultBranch: defaultBranch, Clone: options.Clone, DisableClone: options.DisableClone})
	if err != nil {
		metadata.Close()
		api.Close()
		token.Close()
		return nil, err
	}
	repo := &sdk.RepoCapability{Remote: parsed.Path, WebURL: web, DefaultBranch: defaultBranch, IsPublic: func(ctx context.Context) (bool, error) {
		project, err := metadata.Get(ctx)
		return project.Visibility == nil || *project.Visibility != "private", err
	}, Fetch: clone.Fetch, Push: clone.Push, Objects: clone.Objects(), PullRequest: api.MergeRequest, Checks: api.Checks, MergePullRequest: api.MergeMergeRequest, CheckLog: api.CheckLog}
	repo.PullRequestFor = func(ctx context.Context, branch string) (*sdk.PullRequestInfo, error) {
		project, err := metadata.Get(ctx)
		if err != nil {
			return nil, err
		}
		return api.MergeRequestFor(ctx, branch, project.ID)
	}
	repo.OpenPullRequest = func(ctx context.Context, input sdk.OpenPullRequest) (sdk.PullRequestInfo, error) {
		if input.Base == nil {
			base, err := defaultBranch(ctx)
			if err != nil {
				return sdk.PullRequestInfo{}, err
			}
			input.Base = &base
		}
		return api.OpenMergeRequest(ctx, input)
	}
	tracker := &sdk.TrackerCapability{Name: parsed.Path, WebURL: web + "/-/issues", List: api.Issues, Issue: api.Issue, Comment: api.Comment, SetState: api.SetIssueState}
	var once sync.Once
	var closeErr error
	close := func() error {
		once.Do(func() { closeErr = errors.Join(clone.Close(), metadata.Close(), api.Close(), token.Close()) })
		return closeErr
	}
	return &Connection{Capabilities: sdk.Capabilities{Workspace: clone.Capability(), Repo: repo, Tracker: tracker, Close: close}, API: api, Instance: parsed.Instance, Path: parsed.Path}, nil
}
func Provider(options Options) sdk.ContextProvider {
	return sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "gitlab", Title: sdk.LocalizedText{"zh-CN": "GitLab 仓库", "en": "GitLab repository"}, Hosting: true, Provides: []string{"workspace", "repo", "tracker"}, Settings: sdk.SettingsSchema{Type: "object", FieldOrder: []string{"repository", "instance", "token"}, Required: []string{"repository"}, Properties: map[string]sdk.SettingField{
		"repository": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "仓库", "en": "Repository"}, Description: sdk.LocalizedText{"zh-CN": "group/project（可含子组）或项目地址；每个讨论串在自己的分支上工作", "en": "group/project (subgroups too) or its URL; each thread works on its own branch"}},
		"instance":   {Type: "string", Title: sdk.LocalizedText{"zh-CN": "实例地址", "en": "Instance"}, Description: sdk.LocalizedText{"zh-CN": "自建实例填 https://gitlab.example.com；留空时用 GitLab.com，或仓库地址里的实例", "en": "For a self-managed instance, https://gitlab.example.com; left empty, GitLab.com or the instance in the repository URL"}},
		"token":      {Type: "string", Secret: true, Title: sdk.LocalizedText{"zh-CN": "访问令牌", "en": "Access token"}, Description: sdk.LocalizedText{"zh-CN": "需要 api 权限。留空时使用 GITLAB_TOKEN 环境变量（只用于 GITLAB_HOST 指的实例，未设置时为 GitLab.com）或 glab 为这个实例保存的令牌", "en": "Needs the api scope. Left empty: GITLAB_TOKEN (only for the GITLAB_HOST instance, GitLab.com when unset) or the token glab keeps for this instance"}},
	}}}, SecretsFor: func(values map[string]any) (string, error) {
		repository, _ := values["repository"].(string)
		instance, _ := values["instance"].(string)
		parsed, err := ParseRepository(repository, instance)
		return parsed.Instance, err
	},
		Connect: func(ctx context.Context, values map[string]any) (sdk.Capabilities, error) {
			repository, _ := values["repository"].(string)
			instance, _ := values["instance"].(string)
			token, _ := values["token"].(string)
			connected, err := Connect(Settings{Repository: repository, Instance: instance, Token: token}, options)
			if err != nil {
				return sdk.Capabilities{}, err
			}
			if _, err := connected.Capabilities.Repo.IsPublic(ctx); err != nil {
				connected.Capabilities.Close()
				return sdk.Capabilities{}, err
			}
			if options.Instances != nil {
				release, err := options.Instances.Register(connected.Instance, connected.Path, token)
				if err != nil {
					connected.Capabilities.Close()
					return sdk.Capabilities{}, err
				}
				close := connected.Capabilities.Close
				connected.Capabilities.Close = func() error {
					defer release()
					return close()
				}
			}
			return connected.Capabilities, nil
		}}
}
