package workspaces

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daidr/Ariadne/internal/local"
)

var (
	wsBaseGitOnce sync.Once
	wsBaseGitDir  string
	wsBaseGitSHA  string
	wsBaseGitErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if wsBaseGitDir != "" {
		_ = os.RemoveAll(wsBaseGitDir)
	}
	os.Exit(code)
}

func copyWorkspaceTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyWorkspaceTreeErr(src, dst); err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

func copyWorkspaceTreeErr(src, dst string) error {
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
			if err := copyWorkspaceTreeErr(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func ensureBaseGitTemplate(t *testing.T) (dir, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	wsBaseGitOnce.Do(func() {
		root, err := os.MkdirTemp("", "ad-ws-git-*")
		if err != nil {
			wsBaseGitErr = err
			return
		}
		git := local.NewGit(root, "file")
		run := func(args ...string) error {
			res := git(context.Background(), args, local.GitOptions{})
			if res.ExitCode != 0 {
				return fmt.Errorf("git %v failed: %s", args, res.Stderr)
			}
			return nil
		}
		if err := run("init", "-q", "-b", "main"); err != nil {
			wsBaseGitErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
			wsBaseGitErr = err
			return
		}
		if err := run("add", "README.md"); err != nil {
			wsBaseGitErr = err
			return
		}
		if err := run("-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "initial"); err != nil {
			wsBaseGitErr = err
			return
		}
		raw, err := os.ReadFile(filepath.Join(root, ".git", "refs", "heads", "main"))
		if err != nil {
			wsBaseGitErr = err
			return
		}
		_ = os.RemoveAll(filepath.Join(root, ".git", "hooks"))
		wsBaseGitDir = root
		wsBaseGitSHA = strings.TrimSpace(string(raw))
	})
	if wsBaseGitErr != nil {
		t.Fatalf("build workspace base git template: %v", wsBaseGitErr)
	}
	return wsBaseGitDir, wsBaseGitSHA
}

func initTemplateRepo(t *testing.T, dir string) string {
	t.Helper()
	tpl, sha := ensureBaseGitTemplate(t)
	copyWorkspaceTree(t, tpl, dir)
	return sha
}

func writeWorkspaceGitExclude(t *testing.T, repoDir, patterns string) {
	t.Helper()
	path := filepath.Join(repoDir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing, _ := os.ReadFile(path)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		existing = append(existing, '\n')
	}
	if err := os.WriteFile(path, append(existing, []byte(patterns)...), 0o644); err != nil {
		t.Fatal(err)
	}
}
