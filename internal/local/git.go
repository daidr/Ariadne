package local

import (
	"context"
	"fmt"
	"maps"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

type GitOptions struct {
	Directory string
	Whole     bool
	Env       map[string]string
	Timeout   time.Duration
}
type GitRun func(context.Context, []string, GitOptions) sdk.ExecResult

// ServerGitEnv keeps repository config from executing hooks and fsmonitor,
// and prevents protocol helpers from being used for local repository operations.
func ServerGitEnv(protocols ...string) map[string]string {
	if len(protocols) == 0 {
		protocols = []string{"https"}
	}
	env := map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
		"GIT_ALLOW_PROTOCOL": strings.Join(protocols, ":"),
		"GIT_CONFIG_COUNT":   "2", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": os.DevNull,
		"GIT_CONFIG_KEY_1": "core.fsmonitor", "GIT_CONFIG_VALUE_1": "false",
	}
	if runtime.GOOS == "windows" {
		env["GIT_CONFIG_COUNT"] = "3"
		env["GIT_CONFIG_KEY_2"] = "core.longpaths"
		env["GIT_CONFIG_VALUE_2"] = "true"
	}
	return env
}
func NewGit(directory string, protocols ...string) GitRun {
	return func(ctx context.Context, args []string, options GitOptions) sdk.ExecResult {
		cwd := options.Directory
		if cwd == "" {
			cwd = directory
		}
		env := ServerGitEnv(protocols...)
		maps.Copy(env, options.Env)
		return process.Run(ctx, append([]string{"git"}, args...), process.RunOptions{Directory: cwd, Env: env, ForThread: true, Whole: options.Whole, Timeout: options.Timeout})
	}
}
func gitFailure(result sdk.ExecResult, what string) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = fmt.Sprintf("exit %d", result.ExitCode)
	}
	return fmt.Errorf("%s failed: %s", what, detail)
}
func commitSHA(ctx context.Context, git GitRun, ref, cwd string) string {
	result := git(ctx, []string{"rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}"}, GitOptions{Directory: cwd})
	if result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}
func GitRoot(ctx context.Context, path string) string {
	result := NewGit(path, "file")(ctx, []string{"rev-parse", "--show-toplevel"}, GitOptions{})
	if result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

type Author struct{ Name, Email string }
type Identity func(context.Context) (*Author, error)

func LocalIdentity(directory string) Identity {
	return func(ctx context.Context) (*Author, error) {
		values := []string{}
		for _, key := range []string{"user.name", "user.email"} {
			result := process.Run(ctx, []string{"git", "config", "--get", key}, process.RunOptions{Directory: directory, ForThread: true})
			if result.ExitCode != 0 {
				return nil, nil
			}
			values = append(values, strings.TrimSpace(result.Stdout))
		}
		if values[0] == "" || values[1] == "" {
			return nil, nil
		}
		return &Author{Name: values[0], Email: values[1]}, nil
	}
}

// worktreeConfigArgs reads safe line-ending settings (core.autocrlf, core.eol)
// from git's normal config chain (system, global, local, worktree) so worktree
// operations running under ServerGitEnv normalize CRLF/LF the same way commands
// executed in the user's shell do.
func worktreeConfigArgs(ctx context.Context, cwd string) []string {
	if cwd == "" {
		return nil
	}
	result := process.Run(ctx, []string{"git", "config", "--get-regexp", `^core\.(autocrlf|eol)$`}, process.RunOptions{
		Directory: cwd,
		ForThread: true,
		Timeout:   5 * time.Second,
		Env: map[string]string{
			"GIT_ALLOW_PROTOCOL":  "",
			"GIT_TERMINAL_PROMPT": "0",
		},
	})
	if result.ExitCode != 0 || result.Stdout == "" {
		return nil
	}
	var autocrlf, eol string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		switch strings.ToLower(key) {
		case "core.autocrlf":
			switch value {
			case "true", "false", "input":
				autocrlf = value
			}
		case "core.eol":
			switch value {
			case "lf", "crlf", "native":
				eol = value
			}
		}
	}
	var args []string
	if autocrlf != "" {
		args = append(args, "-c", "core.autocrlf="+autocrlf)
	}
	if eol != "" {
		args = append(args, "-c", "core.eol="+eol)
	}
	return args
}
