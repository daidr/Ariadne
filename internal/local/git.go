package local

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

var gitBinaryCache struct {
	mu       sync.RWMutex
	pathEnv  string
	resolved string
}

func gitExecutable(env map[string]string) string {
	for key := range env {
		if strings.EqualFold(key, "PATH") {
			return "git"
		}
	}
	pathEnv := os.Getenv("PATH")
	gitBinaryCache.mu.RLock()
	cached := ""
	if gitBinaryCache.resolved != "" && gitBinaryCache.pathEnv == pathEnv {
		cached = gitBinaryCache.resolved
	}
	gitBinaryCache.mu.RUnlock()
	if cached != "" {
		if info, err := os.Stat(cached); err == nil && !info.IsDir() {
			return cached
		}
	}
	found, err := exec.LookPath("git")
	if err != nil {
		return "git"
	}
	if !filepath.IsAbs(found) {
		return found
	}
	resolved := unwrapWindowsGit(found)
	gitBinaryCache.mu.Lock()
	gitBinaryCache.pathEnv = pathEnv
	gitBinaryCache.resolved = resolved
	gitBinaryCache.mu.Unlock()
	return resolved
}

// unwrapWindowsGit skips Git for Windows' cmd/git.exe and bin/git.exe shim
// wrappers when the underlying mingw64/clangarm64 git.exe is installed next to
// them, avoiding a second Windows process spawn on every Git call.
func unwrapWindowsGit(found string) string {
	return unwrapWindowsGitForArch(found, runtime.GOOS, runtime.GOARCH)
}

func unwrapWindowsGitForArch(found, goos, goarch string) string {
	if goos != "windows" {
		return found
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return found
	}
	if !strings.EqualFold(filepath.Base(abs), "git.exe") {
		return abs
	}
	parentDir := filepath.Dir(abs)
	parentBase := filepath.Base(parentDir)
	if !strings.EqualFold(parentBase, "cmd") && !strings.EqualFold(parentBase, "bin") {
		return abs
	}
	installRoot := filepath.Dir(parentDir)
	subdirs := []string{"mingw64", "clangarm64", "mingw32"}
	if goarch == "arm64" {
		subdirs = []string{"clangarm64", "mingw64", "mingw32"}
	}
	for _, subdir := range subdirs {
		candidate := filepath.Join(installRoot, subdir, "bin", "git.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return abs
}

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
		return process.Run(ctx, append([]string{gitExecutable(env)}, args...), process.RunOptions{Directory: cwd, Env: env, ForThread: true, Whole: options.Whole, Timeout: options.Timeout})
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
		result := process.Run(ctx, []string{gitExecutable(nil), "config", "--null", "--get-regexp", `^user\.(name|email)$`}, process.RunOptions{Directory: directory, ForThread: true})
		if result.ExitCode != 0 {
			return nil, nil
		}
		var name, email string
		for _, entry := range strings.Split(result.Stdout, "\x00") {
			if entry == "" {
				continue
			}
			key, val, ok := strings.Cut(entry, "\n")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "user.name":
				name = strings.TrimSpace(val)
			case "user.email":
				email = strings.TrimSpace(val)
			}
		}
		if name == "" || email == "" {
			return nil, nil
		}
		return &Author{Name: name, Email: email}, nil
	}
}

var worktreeConfigCache struct {
	mu      sync.RWMutex
	key     string
	args    []string
	expires time.Time
}

type configIncludeEntry struct {
	fingerprint string
	hasInclude  bool
}

var configIncludeCache struct {
	mu      sync.RWMutex
	entries map[string]configIncludeEntry
}

func isNullConfigPath(path string) bool {
	if path == "/dev/null" {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(path, "NUL") || strings.EqualFold(path, `\\.\NUL`)
	}
	return false
}

func defaultSystemGitConfigPaths() []string {
	if runtime.GOOS != "windows" {
		return []string{"/etc/gitconfig"}
	}
	var paths []string
	if pd := os.Getenv("ProgramData"); pd != "" {
		paths = append(paths, filepath.Join(pd, "Git", "config"))
	}
	exe := gitExecutable(nil)
	if filepath.IsAbs(exe) {
		dir := filepath.Dir(exe)
		parent := filepath.Dir(dir)
		baseDir := strings.ToLower(filepath.Base(dir))
		baseParent := strings.ToLower(filepath.Base(parent))
		var root string
		switch {
		case baseDir == "cmd" || baseDir == "bin":
			if baseParent == "mingw64" || baseParent == "mingw32" || baseParent == "clangarm64" {
				root = filepath.Dir(parent)
			} else {
				root = parent
			}
		}
		if root != "" {
			paths = append(paths, filepath.Join(root, "etc", "gitconfig"))
		}
	}
	return paths
}

// inspectConfigFileForGlobalCache returns a fingerprint for path and whether
// it is safe to share across repositories (i.e. absent/null or a readable
// regular file without include/includeIf directives).
func inspectConfigFileForGlobalCache(path string) (string, bool) {
	if path == "" {
		return "", true
	}
	if isNullConfigPath(path) {
		return "null", true
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "-", true
		}
		return "", false
	}
	if !info.Mode().IsRegular() {
		return "", false
	}
	fp := strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)

	configIncludeCache.mu.RLock()
	entry, hit := configIncludeCache.entries[path]
	configIncludeCache.mu.RUnlock()
	if hit && entry.fingerprint == fp {
		if entry.hasInclude {
			return "", false
		}
		return fp, true
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	hasInclude := strings.Contains(strings.ToLower(string(raw)), "include")

	configIncludeCache.mu.Lock()
	if configIncludeCache.entries == nil || len(configIncludeCache.entries) >= 64 {
		configIncludeCache.entries = make(map[string]configIncludeEntry)
	}
	configIncludeCache.entries[path] = configIncludeEntry{fingerprint: fp, hasInclude: hasInclude}
	configIncludeCache.mu.Unlock()

	if hasInclude {
		return "", false
	}
	return fp, true
}

func globalWorktreeConfigKey() (string, bool) {
	home := os.Getenv("HOME")
	userProfile := os.Getenv("USERPROFILE")
	xdg := os.Getenv("XDG_CONFIG_HOME")
	globalCfg := os.Getenv("GIT_CONFIG_GLOBAL")
	systemCfg := os.Getenv("GIT_CONFIG_SYSTEM")
	noSystem := os.Getenv("GIT_CONFIG_NOSYSTEM")

	// Note: worktreeConfigArgs runs git with ForThread: true, which strips
	// GIT_CONFIG_* variables via process.ThreadEnv. Always inspect the default
	// system, HOME/USERPROFILE, and XDG config files in addition to any explicit
	// GIT_CONFIG_* paths so conditional includes can never leak across repos.
	var b strings.Builder
	b.Grow(256)
	b.WriteString(noSystem)
	b.WriteByte('|')
	b.WriteString(systemCfg)
	b.WriteByte('|')
	if systemCfg != "" {
		if !isNullConfigPath(systemCfg) && !filepath.IsAbs(systemCfg) {
			return "", false
		}
		fp, ok := inspectConfigFileForGlobalCache(systemCfg)
		if !ok {
			return "", false
		}
		b.WriteString(fp)
	}
	b.WriteByte('|')
	for i, path := range defaultSystemGitConfigPaths() {
		if i > 0 {
			b.WriteByte(',')
		}
		fp, ok := inspectConfigFileForGlobalCache(path)
		if !ok {
			return "", false
		}
		b.WriteString(path)
		b.WriteByte('=')
		b.WriteString(fp)
	}

	b.WriteByte('|')
	b.WriteString(globalCfg)
	b.WriteByte('|')
	if globalCfg != "" {
		if !isNullConfigPath(globalCfg) && !filepath.IsAbs(globalCfg) {
			return "", false
		}
		fp, ok := inspectConfigFileForGlobalCache(globalCfg)
		if !ok {
			return "", false
		}
		b.WriteString(fp)
	}

	b.WriteByte('|')
	b.WriteString(home)
	b.WriteByte('|')
	if home != "" {
		fp1, ok := inspectConfigFileForGlobalCache(filepath.Join(home, ".gitconfig"))
		if !ok {
			return "", false
		}
		fp2, ok := inspectConfigFileForGlobalCache(filepath.Join(home, ".config", "git", "config"))
		if !ok {
			return "", false
		}
		b.WriteString(fp1)
		b.WriteByte(',')
		b.WriteString(fp2)
	}
	b.WriteByte('|')
	b.WriteString(userProfile)
	b.WriteByte('|')
	if userProfile != "" && userProfile != home {
		fp1, ok := inspectConfigFileForGlobalCache(filepath.Join(userProfile, ".gitconfig"))
		if !ok {
			return "", false
		}
		fp2, ok := inspectConfigFileForGlobalCache(filepath.Join(userProfile, ".config", "git", "config"))
		if !ok {
			return "", false
		}
		b.WriteString(fp1)
		b.WriteByte(',')
		b.WriteString(fp2)
	}
	b.WriteByte('|')
	b.WriteString(xdg)
	b.WriteByte('|')
	if xdg != "" {
		fp, ok := inspectConfigFileForGlobalCache(filepath.Join(xdg, "git", "config"))
		if !ok {
			return "", false
		}
		b.WriteString(fp)
	}
	return b.String(), true
}

func findLocalGitDir(cwd string) (string, bool) {
	dir := filepath.Clean(cwd)
	for range 32 {
		dotGit := filepath.Join(dir, ".git")
		info, err := os.Lstat(dotGit)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", false
			}
			if info.IsDir() {
				return dotGit, true
			}
			if info.Mode().IsRegular() {
				return resolveGitFileTarget(dir, dotGit)
			}
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

func localGitConfigHasLineEndingOverride(cwd string) (hasOverride bool, ok bool) {
	if os.Getenv("GIT_CONFIG_PARAMETERS") != "" || os.Getenv("GIT_CONFIG_COUNT") != "" {
		return true, false
	}
	gitDir, found := findLocalGitDir(cwd)
	if !found {
		return true, false
	}
	commonDir := gitDir
	if rawCommon, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		line, _, _ := strings.Cut(string(rawCommon), "\n")
		target := filepath.FromSlash(strings.TrimSpace(strings.TrimRight(line, "\r")))
		if target != "" {
			if !filepath.IsAbs(target) {
				target = filepath.Join(gitDir, target)
			}
			commonDir = filepath.Clean(target)
		}
	}
	candidates := []string{filepath.Join(commonDir, "config")}
	if worktreeCfg := filepath.Join(gitDir, "config.worktree"); worktreeCfg != candidates[0] {
		candidates = append(candidates, worktreeCfg)
	}
	for i, cfgPath := range candidates {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			if i == 0 {
				return true, false
			}
			if os.IsNotExist(err) {
				continue
			}
			return true, false
		}
		lower := strings.ToLower(string(raw))
		if strings.Contains(lower, "autocrlf") || strings.Contains(lower, "eol") || strings.Contains(lower, "include") {
			return true, true
		}
	}
	return false, true
}

// worktreeConfigArgs reads safe line-ending settings (core.autocrlf, core.eol)
// from git's normal config chain (system, global, local, worktree) so worktree
// operations running under ServerGitEnv normalize CRLF/LF the same way commands
// executed in the user's shell do.
func worktreeConfigArgs(ctx context.Context, cwd string) []string {
	if cwd == "" {
		return nil
	}
	hasLocalOverride, inspectedLocal := localGitConfigHasLineEndingOverride(cwd)
	cacheKey := ""
	if inspectedLocal && !hasLocalOverride {
		if key, ok := globalWorktreeConfigKey(); ok {
			cacheKey = key
			now := time.Now()
			worktreeConfigCache.mu.RLock()
			if worktreeConfigCache.key == cacheKey && now.Before(worktreeConfigCache.expires) {
				cached := slices.Clone(worktreeConfigCache.args)
				worktreeConfigCache.mu.RUnlock()
				return cached
			}
			worktreeConfigCache.mu.RUnlock()
		}
	}
	result := process.Run(ctx, []string{gitExecutable(nil), "config", "--get-regexp", `^core\.(autocrlf|eol)$`}, process.RunOptions{
		Directory: cwd,
		ForThread: true,
		Timeout:   5 * time.Second,
		Env: map[string]string{
			"GIT_ALLOW_PROTOCOL":  "",
			"GIT_TERMINAL_PROMPT": "0",
		},
	})
	var args []string
	if result.ExitCode == 0 && result.Stdout != "" {
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
		if autocrlf != "" {
			args = append(args, "-c", "core.autocrlf="+autocrlf)
		}
		if eol != "" {
			args = append(args, "-c", "core.eol="+eol)
		}
	}
	if cacheKey != "" && (result.ExitCode == 0 || result.ExitCode == 1) && ctx.Err() == nil {
		worktreeConfigCache.mu.Lock()
		worktreeConfigCache.key = cacheKey
		worktreeConfigCache.args = slices.Clone(args)
		worktreeConfigCache.expires = time.Now().Add(30 * time.Second)
		worktreeConfigCache.mu.Unlock()
	}
	return args
}
