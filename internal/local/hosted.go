package local

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/sdk"
)

const noPushURL = "no-push://use-the-push_branch-tool"

type GitCredential struct{ User, Token string }

func HostedGitEnv(remote string, credential *GitCredential) (map[string]string, error) {
	address, err := url.Parse(remote)
	if err != nil || address.User != nil || address.Scheme != "https" && address.Scheme != "http" && address.Scheme != "file" {
		return nil, errors.New("hosted git requires an http, https or file URL without credentials")
	}
	env := ServerGitEnv(address.Scheme)
	add := func(key, value string) {
		index, _ := strconv.Atoi(env["GIT_CONFIG_COUNT"])
		env["GIT_CONFIG_COUNT"] = strconv.Itoa(index + 1)
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", index)] = key
		env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", index)] = value
	}
	add("http.followRedirects", "false")
	// The repository can be written by a thread. Its credential helpers and
	// submodule configuration must not launch programs with the server's token.
	add("credential.helper", "")
	add("credential.interactive", "false")
	add("core.askPass", os.DevNull)
	env["GIT_ASKPASS"], env["SSH_ASKPASS"], env["GIT_TERMINAL_PROMPT"] = os.DevNull, os.DevNull, "0"
	add("fetch.recurseSubmodules", "false")
	if credential != nil && address.Scheme != "file" {
		basic := base64.StdEncoding.EncodeToString([]byte(credential.User + ":" + credential.Token))
		add("http."+remote+".extraHeader", "Authorization: Basic "+basic)
	}
	return env, nil
}

type HostedOptions struct {
	DataDir, Path, RemoteURL, User string
	Namespace                      []string
	Token                          func(context.Context) (*string, error)
	DefaultBranch                  func(context.Context) (string, error)
	Clone                          Cloner
	DisableClone                   bool
	Now                            func() time.Time
}
type Hosted struct {
	options   HostedOptions
	owner     *Workspaces
	bare      string
	objects   *ObjectStore
	fetchedAt time.Time
}

func hostedPaths(data, path string, namespace []string) (string, string, error) {
	segments := append(append([]string(nil), namespace...), strings.Split(path, "/")...)
	for _, part := range segments {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\\x00") {
			return "", "", errors.New("invalid hosted repository path")
		}
	}
	data, err := filepath.Abs(data)
	if err != nil {
		return "", "", err
	}
	last := segments[len(segments)-1]
	parents := segments[:len(segments)-1]
	bare := filepath.Join(append([]string{data, "repos"}, append(append([]string(nil), parents...), last+".git")...)...)
	worktrees := filepath.Join(append([]string{data, "worktrees"}, segments...)...)
	return bare, worktrees, nil
}

func NewHosted(options HostedOptions) (*Hosted, error) {
	if options.DataDir == "" || options.Path == "" || options.DefaultBranch == nil {
		return nil, errors.New("hosted clone requires a data directory, repository path and default branch")
	}
	if _, err := HostedGitEnv(options.RemoteURL, nil); err != nil {
		return nil, err
	}
	bare, worktrees, err := hostedPaths(options.DataDir, options.Path, options.Namespace)
	if err != nil {
		return nil, err
	}
	clone := options.Clone
	if clone == nil && !options.DisableClone {
		clone = PlatformCloner
	}
	if options.DisableClone {
		clone = nil
	}
	owner := newWorkspaceOwner()
	owner.path, owner.repository, owner.common = bare, bare, bare
	owner.roots = []string{worktrees}
	owner.git = NewGit(bare, "file")
	owner.clone = clone
	objects, err := NewObjectStore(bare)
	if err != nil {
		owner.Close()
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Hosted{options: options, owner: owner, bare: bare, objects: objects}, nil
}
func (host *Hosted) serial(ctx context.Context, work func(context.Context) error) error {
	ctx, done, err := host.owner.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	release, err := lockHosted(ctx, host.bare)
	if err != nil {
		return err
	}
	defer release()
	return work(ctx)
}
func (host *Hosted) network(ctx context.Context, args ...string) sdk.ExecResult {
	var credential *GitCredential
	if host.options.Token != nil {
		token, err := host.options.Token(ctx)
		if err != nil {
			return sdk.ExecResult{ExitCode: 1, Stderr: err.Error()}
		}
		if token != nil && *token != "" {
			credential = &GitCredential{User: host.options.User, Token: *token}
		}
	}
	env, err := HostedGitEnv(host.options.RemoteURL, credential)
	if err != nil {
		return sdk.ExecResult{ExitCode: 1, Stderr: err.Error()}
	}
	result := process.Run(ctx, append([]string{"git"}, args...), process.RunOptions{Directory: host.bare, Timeout: 10 * time.Minute, ForThread: true, Env: env})
	if credential != nil {
		private := []string{credential.Token, base64.StdEncoding.EncodeToString([]byte(credential.User + ":" + credential.Token))}
		result.Stdout = secrets.Redact(result.Stdout, private)
		result.Stderr = secrets.Redact(result.Stderr, private)
	}
	return result
}
func (host *Hosted) ensureClone(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(host.bare, "HEAD")); os.IsNotExist(err) {
		if err := os.MkdirAll(host.bare, 0700); err != nil {
			return err
		}
		for _, args := range [][]string{{"init", "--bare", "-q"}} {
			result := host.owner.git(ctx, args, GitOptions{})
			if result.ExitCode != 0 {
				return gitFailure(result, "git "+args[0])
			}
		}
	} else if err != nil {
		return err
	}
	for _, args := range [][]string{{"config", "remote.origin.url", host.options.RemoteURL}, {"config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"}} {
		result := host.owner.git(ctx, args, GitOptions{})
		if result.ExitCode != 0 {
			return gitFailure(result, "git config")
		}
	}
	configured := host.owner.git(ctx, []string{"config", "remote.origin.pushurl", noPushURL}, GitOptions{})
	if configured.ExitCode != 0 {
		return gitFailure(configured, "git config")
	}
	result := host.network(ctx, "fetch", "--no-recurse-submodules", "--prune", "-q", host.options.RemoteURL, "+refs/heads/*:refs/remotes/origin/*")
	if result.ExitCode != 0 {
		return gitFailure(result, "git fetch")
	}
	host.fetchedAt = host.options.Now()
	return nil
}
func (host *Hosted) readyLocked(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(host.bare, "HEAD")); os.IsNotExist(err) {
		return host.ensureClone(ctx)
	} else {
		return err
	}
}
func (host *Hosted) ready(ctx context.Context) error { return host.serial(ctx, host.readyLocked) }
func (host *Hosted) Fetch(ctx context.Context) error { return host.serial(ctx, host.ensureClone) }
func (host *Hosted) Push(ctx context.Context, branch string) (string, error) {
	var pushedSHA string
	err := host.serial(ctx, func(ctx context.Context) error {
		if err := host.readyLocked(ctx); err != nil {
			return err
		}
		ref := "refs/heads/" + branch
		if err := validRef(ctx, host.owner.git, ref); err != nil {
			return err
		}
		sha := commitSHA(ctx, host.owner.git, ref, "")
		if sha == "" {
			return fmt.Errorf("%s does not exist", branch)
		}
		// Push the captured commit even if a command advances the workspace's
		// branch while credentials are being resolved or the network is slow.
		result := host.network(ctx, "push", "-q", "--no-verify", host.options.RemoteURL, sha+":"+ref)
		if result.ExitCode != 0 {
			lower := strings.ToLower(result.Stderr)
			if strings.Contains(lower, "rejected") || strings.Contains(lower, "non-fast-forward") || strings.Contains(lower, "fetch first") {
				return fmt.Errorf("git push was rejected: origin/%s has commits your branch does not have. Fetch them (sync_branch), merge origin/%s into your branch, then push again. Never force-push.", branch, branch)
			}
			return gitFailure(result, "git push")
		}
		updated := host.owner.git(ctx, []string{"update-ref", "refs/remotes/origin/" + branch, sha}, GitOptions{})
		if updated.ExitCode != 0 {
			return gitFailure(updated, "git update-ref")
		}
		pushedSHA = sha
		return nil
	})
	return pushedSHA, err
}
func (host *Hosted) View(ctx context.Context) (sdk.SourceView, error) {
	var base string
	err := host.serial(ctx, func(ctx context.Context) error {
		var err error
		base, err = host.options.DefaultBranch(ctx)
		if err != nil {
			return err
		}
		if host.fetchedAt.IsZero() || host.options.Now().Sub(host.fetchedAt) > time.Minute {
			return host.ensureClone(ctx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ref := "refs/remotes/origin/" + base
	readGit := func(ctx context.Context, args []string, options GitOptions) sdk.ExecResult {
		ctx, done, err := host.owner.begin(ctx)
		if err != nil {
			return sdk.ExecResult{ExitCode: 130, Stderr: err.Error()}
		}
		defer done()
		return host.owner.git(ctx, args, options)
	}
	readBytes := func(ctx context.Context, args []string, limit int64) ([]byte, error) {
		ctx, done, err := host.owner.begin(ctx)
		if err != nil {
			return nil, err
		}
		defer done()
		return NewGitBytes(host.bare, "file")(ctx, args, limit)
	}
	return sdk.ReadOnlyView(NewTreeView(readGit, readBytes, ref, "origin/"+base)), nil
}
func (host *Hosted) sibling(threadID string) string {
	entries, err := os.ReadDir(host.owner.roots[0])
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != threadID && safeThreadID(entry.Name()) {
			dir, err := host.owner.checkedDirectory(entry.Name())
			if err != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				return dir
			}
		}
	}
	return ""
}
func (host *Hosted) SharesStorage(ctx context.Context) (bool, error) {
	var shares bool
	err := host.serial(ctx, func(ctx context.Context) error {
		if _, err := os.Stat(filepath.Join(host.bare, "HEAD")); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		root, err := host.owner.cloningRoot(ctx)
		shares = root != ""
		return err
	})
	return shares, err
}
func (host *Hosted) Open(ctx context.Context, input sdk.WorkspaceRequest) (sdk.Workspace, error) {
	var workspace *Workspace
	err := host.serial(ctx, func(ctx context.Context) error {
		release, err := host.owner.thread(ctx, input.ThreadID)
		if err != nil {
			return err
		}
		defer release()
		branch := BranchFor(input.ThreadID)
		if input.Branch != nil {
			branch = *input.Branch
		}
		if !refSpelling("refs/heads/"+branch, false) {
			return errors.New("invalid workspace branch")
		}
		base := ""
		if input.Base != nil && *input.Base != nil {
			base = **input.Base
		} else {
			base, err = host.options.DefaultBranch(ctx)
			if err != nil {
				return err
			}
		}
		dir, err := host.owner.checkedDirectory(input.ThreadID)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			if err := host.ensureClone(ctx); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
				return err
			}
			pruned := host.owner.git(ctx, []string{"worktree", "prune"}, GitOptions{})
			if pruned.ExitCode != 0 {
				return gitFailure(pruned, "git worktree prune")
			}
			hasBranch := commitSHA(ctx, host.owner.git, "refs/heads/"+branch, "") != ""
			from := ""
			if host.owner.clone != nil {
				root, err := host.owner.cloningRoot(ctx)
				if err != nil {
					return err
				}
				if root != "" {
					from = host.sibling(input.ThreadID)
				}
			}
			args := []string{"worktree", "add"}
			if from != "" {
				args = append(args, "--no-checkout")
			}
			if hasBranch {
				args = append(args, "--", dir, branch)
			} else {
				args = append(args, "--no-track", "-b", branch, "--", dir, "refs/remotes/origin/"+base)
			}
			result := host.owner.git(ctx, args, GitOptions{})
			if result.ExitCode != 0 {
				return gitFailure(result, "git worktree add")
			}
		} else if err != nil {
			return err
		}
		if err := host.owner.verifiedWorktree(ctx, dir); err != nil {
			return err
		}
		pending, err := CheckoutPending(ctx, host.owner.git, dir)
		if err != nil {
			return err
		}
		if pending {
			from := ""
			if host.owner.clone != nil {
				root, err := host.owner.cloningRoot(ctx)
				if err != nil {
					return err
				}
				if root != "" {
					from = host.sibling(input.ThreadID)
				}
			}
			if _, err := CheckoutCloned(ctx, from, dir, host.owner.git, host.owner.clone); err != nil {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				host.owner.git(cleanup, []string{"worktree", "remove", "--force", "--force", "--", dir}, GitOptions{Timeout: 10 * time.Second})
				cancel()
				return err
			}
		}
		workspace, err = NewWorkspace(dir, WorkspaceOptions{ID: input.ThreadID, Branch: &branch, Base: &base})
		if err != nil {
			return err
		}
		workspace, err = host.owner.track(ctx, workspace)
		return err
	})
	if err != nil {
		return nil, err
	}
	return workspace, nil
}
func (host *Hosted) Objects() *sdk.GitObjects {
	return &sdk.GitObjects{
		Refs: func(ctx context.Context, prefixes []string) (map[string]string, error) {
			if err := host.ready(ctx); err != nil {
				return nil, err
			}
			return host.objects.Refs(ctx, prefixes)
		},
		Has: func(ctx context.Context, shas []string) ([]string, error) {
			if len(shas) == 0 {
				return []string{}, nil
			}
			if err := host.ready(ctx); err != nil {
				return nil, err
			}
			return host.objects.Has(ctx, shas)
		},
		Bundle: func(ctx context.Context, tips, exclude []string) (io.ReadCloser, error) {
			if err := host.ready(ctx); err != nil {
				return nil, err
			}
			return host.objects.Bundle(ctx, tips, exclude)
		},
		Receive: func(ctx context.Context, bundle io.ReadCloser, refs map[string]*string) error {
			if bundle != nil {
				bundle = &ownedReadCloser{ReadCloser: bundle}
				defer bundle.Close()
			}
			return host.serial(ctx, func(ctx context.Context) error {
				if err := host.readyLocked(ctx); err != nil {
					return err
				}
				return host.objects.Receive(ctx, bundle, refs)
			})
		},
	}
}
func (host *Hosted) Capability() *sdk.WorkspaceCapability {
	capability := host.owner.Capability()
	capability.Open = host.Open
	capability.View = host.View
	branches := capability.Branches
	branches.Merge = nil
	branches.UndoMerge = nil
	branches.Snapshot = nil
	branches.Diff = nil
	branches.SharesStorage = host.SharesStorage
	branches.Revisions = func(ctx context.Context, thread sdk.ThreadBranch, base string) (sdk.BranchRevisions, error) {
		return host.owner.revisions(ctx, thread.Branch, "refs/remotes/origin/"+base)
	}
	branches.Changes = func(ctx context.Context, thread sdk.ThreadBranch, base string) (*sdk.BranchChanges, error) {
		ctx, done, err := host.owner.begin(ctx)
		if err != nil {
			return nil, err
		}
		defer done()
		dir, err := host.owner.checkedDirectory(thread.ThreadID)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			dir = ""
		} else if err != nil {
			return nil, err
		}
		return BranchChanges(ctx, host.owner.git, BranchInput{Branch: thread.Branch, Base: base, BaseRef: "refs/remotes/origin/" + base, Worktree: dir})
	}
	branches.Merged = func(ctx context.Context, thread sdk.ThreadBranch, base string) (bool, error) {
		ctx, done, err := host.owner.begin(ctx)
		if err != nil {
			return false, err
		}
		defer done()
		merged := BranchMerged(ctx, host.owner.git, thread.Branch, "refs/remotes/origin/"+base)
		return merged, ctx.Err()
	}
	remove := branches.Remove
	branches.Remove = func(ctx context.Context, threadID string, options sdk.WorktreeRemoveOptions) (bool, error) {
		var removed bool
		err := host.serial(ctx, func(ctx context.Context) error {
			var err error
			removed, err = remove(ctx, threadID, options)
			return err
		})
		return removed, err
	}
	removeOrphaned := branches.RemoveOrphaned
	branches.RemoveOrphaned = func(ctx context.Context, threadID, label string) (bool, error) {
		var removed bool
		err := host.serial(ctx, func(ctx context.Context) error {
			var err error
			removed, err = removeOrphaned(ctx, threadID, label)
			return err
		})
		return removed, err
	}
	return capability
}
func (host *Hosted) Close() error { return errors.Join(host.owner.Close(), host.objects.Close()) }
