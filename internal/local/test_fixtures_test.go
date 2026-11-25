package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	localFixtureOnce sync.Once
	localFixtureRoot string
	localFixtureErr  error

	baseGitOnce sync.Once
	baseGitDir  string
	baseGitErr  error

	bareGitOnce sync.Once
	bareGitDir  string
	bareGitErr  error

	remoteCloneOnce    sync.Once
	remoteCloneBareDir string
	remoteCloneWorkDir string
	remoteCloneErr     error

	embeddedSubOnce    sync.Once
	embeddedSubBareDir string
	embeddedSubWorkDir string
	embeddedSubSHA     string
	embeddedSubErr     error

	orphanSubOnce     sync.Once
	orphanSubBareDir  string
	orphanSubBareURL  string
	orphanSubCloneDir string
	orphanSubModDir   string
	orphanSubFilesDir string
	orphanSubSHA      string
	orphanSubErr      error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if localFixtureRoot != "" {
		_ = os.RemoveAll(localFixtureRoot)
	}
	os.Exit(code)
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	localFixtureOnce.Do(func() {
		localFixtureRoot, localFixtureErr = os.MkdirTemp("", "ad-local-tpl-*")
	})
	if localFixtureErr != nil {
		t.Fatalf("create local fixture root: %v", localFixtureErr)
	}
	return localFixtureRoot
}

func runFixtureGit(dir string, args ...string) (string, error) {
	res := NewGit(dir, "file")(context.Background(), args, GitOptions{Whole: true})
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %v in %s: %s", args, dir, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

func writeFixtureFile(root, rel, value string) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(value), 0o644)
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyTreeErr(src, dst); err != nil {
		t.Fatalf("copy fixture %s -> %s: %v", src, dst, err)
	}
}

func copyTreeErr(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTreeErr(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		// Keep copied objects writable so Windows TempDir cleanup never fails on 0444 git objects.
		mode := info.Mode().Perm() | 0o200
		if err := os.WriteFile(dstPath, data, mode); err != nil {
			return err
		}
	}
	return nil
}

func stripGitHooks(gitDir string) {
	_ = os.RemoveAll(filepath.Join(gitDir, "hooks"))
}

func appendGitUserConfig(gitDir string) error {
	cfgPath := filepath.Join(gitDir, "config")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "[user]") {
		return nil
	}
	raw = append(raw, []byte("\n[user]\n\tname = Ariadne Test\n\temail = test@example.invalid\n")...)
	return os.WriteFile(cfgPath, raw, 0o644)
}

func writeGitRef(t *testing.T, gitDir, relRef, content string) {
	t.Helper()
	full := filepath.Join(gitDir, filepath.FromSlash(relRef))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRefSHA(t *testing.T, gitDir, relRef string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(relRef)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func removeGitRef(t *testing.T, gitDir, relRef string) {
	t.Helper()
	full := filepath.Join(gitDir, filepath.FromSlash(relRef))
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func appendGitConfig(t *testing.T, gitDir, snippet string) {
	t.Helper()
	cfgPath := filepath.Join(gitDir, "config")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(snippet)...)
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGitExclude(t *testing.T, repoDir, patterns string) {
	t.Helper()
	excludePath := filepath.Join(repoDir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excludePath, []byte(patterns), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSingleWorktreeFast(t *testing.T, hostRepoDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(hostRepoDir, ".git", "worktrees"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("host %s left dangling worktree registrations: %v", hostRepoDir, names)
	}
}

func ensureBareTemplate(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t)
	bareGitOnce.Do(func() {
		dir := filepath.Join(root, "bare-empty.git")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			bareGitErr = err
			return
		}
		if _, err := runFixtureGit(dir, "init", "--bare", "-q", "-b", "main"); err != nil {
			bareGitErr = err
			return
		}
		stripGitHooks(dir)
		bareGitDir = dir
	})
	if bareGitErr != nil {
		t.Fatalf("build bare git template: %v", bareGitErr)
	}
	return bareGitDir
}

func readHeadSHA(t *testing.T, repoDir string) string {
	t.Helper()
	gitDir := directWorktreeGitDir(repoDir)
	if gitDir != "" {
		if raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
			line := strings.TrimSpace(string(raw))
			if target, ok := strings.CutPrefix(line, "ref:"); ok {
				ref := strings.TrimSpace(target)
				commonDir := gitDir
				if rawCommon, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
					cd := filepath.FromSlash(strings.TrimSpace(string(rawCommon)))
					if cd != "" {
						if !filepath.IsAbs(cd) {
							cd = filepath.Join(gitDir, cd)
						}
						commonDir = filepath.Clean(cd)
					}
				}
				if refData, err := os.ReadFile(filepath.Join(commonDir, filepath.FromSlash(ref))); err == nil {
					sha := strings.TrimSpace(string(refData))
					if isObjectID(sha) {
						return sha
					}
				}
			} else if isObjectID(line) {
				return line
			}
		}
	}
	return gitOK(t, NewGit(repoDir, "file"), "rev-parse", "HEAD")
}

func ensureBaseGitTemplate(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t)
	baseGitOnce.Do(func() {
		dir := filepath.Join(root, "base-git")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			baseGitErr = err
			return
		}
		if _, err := runFixtureGit(dir, "init", "-q", "-b", "main"); err != nil {
			baseGitErr = err
			return
		}
		if err := appendGitUserConfig(filepath.Join(dir, ".git")); err != nil {
			baseGitErr = err
			return
		}
		if err := writeFixtureFile(dir, "file.txt", "base\n"); err != nil {
			baseGitErr = err
			return
		}
		if err := writeFixtureFile(dir, "untouched.txt", "kept\n"); err != nil {
			baseGitErr = err
			return
		}
		if _, err := runFixtureGit(dir, "add", "--all"); err != nil {
			baseGitErr = err
			return
		}
		if _, err := runFixtureGit(dir, "commit", "-qm", "base"); err != nil {
			baseGitErr = err
			return
		}
		stripGitHooks(filepath.Join(dir, ".git"))
		baseGitDir = dir
	})
	if baseGitErr != nil {
		t.Fatalf("build base git template: %v", baseGitErr)
	}
	return baseGitDir
}

func ensureRemoteAndCloneTemplate(t *testing.T) (bareDir, workDir string) {
	t.Helper()
	base := ensureBaseGitTemplate(t)
	root := fixtureRoot(t)
	remoteCloneOnce.Do(func() {
		parent := filepath.Join(root, "remote-clone")
		bare := filepath.Join(parent, "origin.git")
		work := filepath.Join(parent, "work")
		if err := os.MkdirAll(parent, 0o755); err != nil {
			remoteCloneErr = err
			return
		}
		if _, err := runFixtureGit(base, "clone", "--bare", "-q", base, bare); err != nil {
			remoteCloneErr = err
			return
		}
		if _, err := runFixtureGit(parent, "clone", "-q", filepath.ToSlash(bare), work); err != nil {
			remoteCloneErr = err
			return
		}
		if err := appendGitUserConfig(filepath.Join(work, ".git")); err != nil {
			remoteCloneErr = err
			return
		}
		stripGitHooks(bare)
		stripGitHooks(filepath.Join(work, ".git"))
		remoteCloneBareDir = bare
		remoteCloneWorkDir = work
	})
	if remoteCloneErr != nil {
		t.Fatalf("build remote/clone template: %v", remoteCloneErr)
	}
	return remoteCloneBareDir, remoteCloneWorkDir
}

func ensureEmbeddedSubRepoTemplate(t *testing.T) (bareDir, workDir, sha string) {
	t.Helper()
	root := fixtureRoot(t)
	embeddedSubOnce.Do(func() {
		parent := filepath.Join(root, "embedded-sub")
		bare := filepath.Join(parent, "remote.git")
		work := filepath.Join(parent, "repo")
		if err := os.MkdirAll(bare, 0o755); err != nil {
			embeddedSubErr = err
			return
		}
		if err := os.MkdirAll(work, 0o755); err != nil {
			embeddedSubErr = err
			return
		}
		if _, err := runFixtureGit(bare, "init", "--bare", "-q", "-b", "main"); err != nil {
			embeddedSubErr = err
			return
		}
		if _, err := runFixtureGit(work, "init", "-q", "-b", "main"); err != nil {
			embeddedSubErr = err
			return
		}
		if err := appendGitUserConfig(filepath.Join(work, ".git")); err != nil {
			embeddedSubErr = err
			return
		}
		if _, err := runFixtureGit(work, "remote", "add", "origin", fileURL(bare)); err != nil {
			embeddedSubErr = err
			return
		}
		if err := writeFixtureFile(work, "hello.txt", "v1\n"); err != nil {
			embeddedSubErr = err
			return
		}
		for _, args := range [][]string{
			{"add", "hello.txt"},
			{"commit", "-qm", "initial"},
			{"push", "-q", "-u", "origin", "main"},
		} {
			if _, err := runFixtureGit(work, args...); err != nil {
				embeddedSubErr = err
				return
			}
		}
		if err := writeFixtureFile(work, ".git/refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main\n"); err != nil {
			embeddedSubErr = err
			return
		}
		rawHead, err := os.ReadFile(filepath.Join(work, ".git", "refs", "heads", "main"))
		if err != nil {
			embeddedSubErr = err
			return
		}
		stripGitHooks(bare)
		stripGitHooks(filepath.Join(work, ".git"))
		embeddedSubBareDir = bare
		embeddedSubWorkDir = work
		embeddedSubSHA = strings.TrimSpace(string(rawHead))
	})
	if embeddedSubErr != nil {
		t.Fatalf("build embedded sub-repo template: %v", embeddedSubErr)
	}
	return embeddedSubBareDir, embeddedSubWorkDir, embeddedSubSHA
}

func ensureOrphanSubmoduleTemplate(t *testing.T) {
	t.Helper()
	root := fixtureRoot(t)
	orphanSubOnce.Do(func() {
		parent := filepath.Join(root, "orphan-sub")
		bare := filepath.Join(parent, "pkg-remote.git")
		seed := filepath.Join(parent, "seed")
		mainSeed := filepath.Join(parent, "main-seed")
		subMod := filepath.Join(parent, "sub-mod")
		filesDir := filepath.Join(parent, "sub-files")
		for _, d := range []string{bare, seed, mainSeed} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				orphanSubErr = err
				return
			}
		}
		bareURL := fileURL(bare)

		// 1. Seed orphan branch "vendor-orphan".
		if _, err := runFixtureGit(seed, "init", "-q", "-b", "vendor-orphan"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(seed, "bundle.js", "export const version = 1;\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(seed, "assets/info.txt", "orphan bundle\n"); err != nil {
			orphanSubErr = err
			return
		}
		for _, args := range [][]string{
			{"add", "--all"},
			{"-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial orphan bundle"},
		} {
			if _, err := runFixtureGit(seed, args...); err != nil {
				orphanSubErr = err
				return
			}
		}
		rawSHA, err := os.ReadFile(filepath.Join(seed, ".git", "refs", "heads", "vendor-orphan"))
		if err != nil {
			orphanSubErr = err
			return
		}
		sha := strings.TrimSpace(string(rawSHA))

		// 2. Seed unrelated "main" branch with submodule "vendor-bundle" using relative url = ./
		if _, err := runFixtureGit(mainSeed, "init", "-q", "-b", "main"); err != nil {
			orphanSubErr = err
			return
		}
		if err := appendGitUserConfig(filepath.Join(mainSeed, ".git")); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(mainSeed, "README.md", "# pkg\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(mainSeed, "src/index.ts", "export const main = true;\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(mainSeed, "src/util.ts", "export const util = 1;\n"); err != nil {
			orphanSubErr = err
			return
		}
		gitmodules := "[submodule \"vendor-bundle\"]\n\tpath = vendor-bundle\n\turl = ./\n\tbranch = vendor-orphan\n"
		if err := writeFixtureFile(mainSeed, ".gitmodules", gitmodules); err != nil {
			orphanSubErr = err
			return
		}
		if err := os.MkdirAll(filepath.Join(mainSeed, "vendor-bundle"), 0o755); err != nil {
			orphanSubErr = err
			return
		}
		for _, args := range [][]string{
			{"add", "--all"},
			{"update-index", "--add", "--cacheinfo", "160000," + sha + ",vendor-bundle"},
			{"commit", "-qm", "initial main with vendor-bundle orphan submodule"},
		} {
			if _, err := runFixtureGit(mainSeed, args...); err != nil {
				orphanSubErr = err
				return
			}
		}
		rawMainSHA, err := os.ReadFile(filepath.Join(mainSeed, ".git", "refs", "heads", "main"))
		if err != nil {
			orphanSubErr = err
			return
		}
		mainSHA := strings.TrimSpace(string(rawMainSHA))
		if err := writeFixtureFile(mainSeed, ".git/refs/remotes/origin/main", mainSHA+"\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(mainSeed, ".git/refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main\n"); err != nil {
			orphanSubErr = err
			return
		}
		mainCfgPath := filepath.Join(mainSeed, ".git", "config")
		mainCfg, err := os.ReadFile(mainCfgPath)
		if err != nil {
			orphanSubErr = err
			return
		}
		mainCfg = append(mainCfg, []byte("\n[remote \"origin\"]\n\turl = "+bareURL+"\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n")...)
		if err := os.WriteFile(mainCfgPath, mainCfg, 0o644); err != nil {
			orphanSubErr = err
			return
		}

		// Create an unrelated root commit inside seed's object store so origin/main is a valid object.
		unrelatedMainSHA, err := runFixtureGit(seed, "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit-tree", sha+"^{tree}", "-m", "unrelated main")
		if err != nil {
			orphanSubErr = err
			return
		}
		// 3. Build the template module gitdir directly from seed/.git without git clone/push.
		if err := copyTreeErr(filepath.Join(seed, ".git"), subMod); err != nil {
			orphanSubErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(subMod, "HEAD"), []byte(sha+"\n"), 0o644); err != nil {
			orphanSubErr = err
			return
		}
		_ = os.Remove(filepath.Join(subMod, "refs", "heads", "vendor-orphan"))
		if err := writeFixtureFile(subMod, "refs/remotes/origin/vendor-orphan", sha+"\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(subMod, "refs/remotes/origin/main", unrelatedMainSHA+"\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(subMod, "refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := appendGitUserConfig(subMod); err != nil {
			orphanSubErr = err
			return
		}
		subCfgPath := filepath.Join(subMod, "config")
		subCfg, err := os.ReadFile(subCfgPath)
		if err != nil {
			orphanSubErr = err
			return
		}
		subCfg = append(subCfg, []byte("\n[core]\n\tworktree = PLACEHOLDER\n[remote \"origin\"]\n\turl = "+bareURL+"\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")...)
		if err := os.WriteFile(subCfgPath, subCfg, 0o644); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(filesDir, "bundle.js", "export const version = 1;\n"); err != nil {
			orphanSubErr = err
			return
		}
		if err := writeFixtureFile(filesDir, "assets/info.txt", "orphan bundle\n"); err != nil {
			orphanSubErr = err
			return
		}
		stripGitHooks(filepath.Join(mainSeed, ".git"))
		stripGitHooks(subMod)

		orphanSubBareDir = bare
		orphanSubBareURL = bareURL
		orphanSubCloneDir = mainSeed
		orphanSubModDir = subMod
		orphanSubFilesDir = filesDir
		orphanSubSHA = sha
	})
	if orphanSubErr != nil {
		t.Fatalf("build orphan submodule template: %v", orphanSubErr)
	}
}

func cloneOrphanSubmoduleRepo(t *testing.T, remoteBare, destDir string) GitRun {
	t.Helper()
	ensureOrphanSubmoduleTemplate(t)
	copyTree(t, orphanSubCloneDir, destDir)
	cfgPath := filepath.Join(destDir, ".git", "config")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.ReplaceAll(string(raw), orphanSubBareURL, fileURL(remoteBare))
	if err := os.WriteFile(cfgPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewGit(destDir, "file")
}

func addSubmoduleFast(t *testing.T, repoDir string, repoGit GitRun, subPath, remoteURL, branch, sha string) {
	t.Helper()
	gitmodulesPath := filepath.Join(repoDir, ".gitmodules")
	existing, _ := os.ReadFile(gitmodulesPath)
	entry := fmt.Sprintf("[submodule %q]\n\tpath = %s\n\turl = %s\n\tbranch = %s\n", subPath, subPath, remoteURL, branch)
	if err := os.WriteFile(gitmodulesPath, append(existing, []byte(entry)...), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOK(t, repoGit, "add", ".gitmodules")
	gitOK(t, repoGit, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+subPath)
	gitOK(t, repoGit, "commit", "-qm", "add submodule "+subPath)
}

func resolveWorktreeGitDir(t *testing.T, wtDir string) string {
	t.Helper()
	dotGit := filepath.Join(wtDir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		t.Fatalf("stat %s: %v", dotGit, err)
	}
	if info.IsDir() {
		return dotGit
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("unexpected .git file content %q in %s", line, dotGit)
	}
	target := strings.TrimSpace(line[len(prefix):])
	if !filepath.IsAbs(target) {
		target = filepath.Join(wtDir, filepath.FromSlash(target))
	}
	return filepath.Clean(target)
}

// initOrphanSubmoduleFast materializes the prebuilt orphan submodule into a worktree
// using the exact per-worktree <gitdir>/modules/<subPath> layout created by git submodule update --init.
func initOrphanSubmoduleFast(t *testing.T, wtDir, subPath, remoteURL string) GitRun {
	t.Helper()
	ensureOrphanSubmoduleTemplate(t)
	wtGitDir := resolveWorktreeGitDir(t, wtDir)
	modDir := filepath.Join(wtGitDir, "modules", filepath.FromSlash(subPath))
	subDir := filepath.Join(wtDir, filepath.FromSlash(subPath))

	copyTree(t, orphanSubModDir, modDir)
	copyTree(t, orphanSubFilesDir, subDir)

	if err := os.WriteFile(filepath.Join(subDir, ".git"), []byte("gitdir: "+filepath.ToSlash(modDir)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(modDir, "config")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "url = "):
			lines[i] = "\turl = " + remoteURL
		case strings.HasPrefix(trimmed, "worktree = "):
			lines[i] = "\tworktree = " + filepath.ToSlash(subDir)
		}
	}
	if err := os.WriteFile(cfgPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewGit(subDir, "file")
}
