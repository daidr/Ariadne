package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/google/uuid"
)

func CanClone(ctx context.Context, clone Cloner, gitDir, targetDir string) bool {
	if clone == nil || ctx.Err() != nil {
		return false
	}
	tag := uuid.NewString()
	source := filepath.Join(gitDir, "ariadne-clone-probe-"+tag)
	target := filepath.Join(targetDir, ".ariadne-clone-probe-"+tag)
	if os.MkdirAll(targetDir, 0755) != nil {
		return false
	}
	file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	_, err = file.WriteString("probe")
	closed := file.Close()
	defer os.Remove(source)
	defer os.Remove(target)
	return err == nil && closed == nil && clone(ctx, source, target)
}
func worktreeGitDir(git GitRun, ctx context.Context, directory string) (string, error) {
	if direct := directWorktreeGitDir(directory); direct != "" && isValidGitDirectory(direct) {
		return direct, nil
	}
	result := git(ctx, []string{"rev-parse", "--absolute-git-dir"}, GitOptions{Directory: directory})
	if result.ExitCode != 0 {
		return "", gitFailure(result, "git rev-parse")
	}
	return strings.TrimSpace(result.Stdout), nil
}
func CheckoutPending(ctx context.Context, git GitRun, directory string) (bool, error) {
	dir, err := worktreeGitDir(git, ctx, directory)
	if err != nil {
		return false, err
	}
	_, markErr := os.Stat(filepath.Join(dir, "ariadne-checkout-pending"))
	_, indexErr := os.Stat(filepath.Join(dir, "index"))
	if markErr != nil && !os.IsNotExist(markErr) {
		return false, markErr
	}
	if indexErr != nil && !os.IsNotExist(indexErr) {
		return false, indexErr
	}
	return markErr == nil || os.IsNotExist(indexErr), nil
}
func CheckoutCloned(ctx context.Context, from, worktree string, git GitRun, clone Cloner) (int, error) {
	dir, err := worktreeGitDir(git, ctx, worktree)
	if err != nil {
		return 0, err
	}
	mark := filepath.Join(dir, "ariadne-checkout-pending")
	if err := os.WriteFile(mark, nil, 0600); err != nil {
		return 0, err
	}
	cloned := 0
	if from != "" && clone != nil {
		cloned, err = cloneTracked(ctx, from, worktree, git, clone)
		if err != nil {
			return 0, err
		}
	}
	for _, args := range [][]string{{"reset", "-q"}, {"reset", "-q", "--hard"}} {
		result := git(ctx, args, GitOptions{Directory: worktree})
		if result.ExitCode != 0 {
			return 0, gitFailure(result, "git reset")
		}
	}
	if err := os.Remove(mark); err != nil {
		return 0, err
	}
	return cloned, nil
}
func cloneTracked(ctx context.Context, from, worktree string, git GitRun, clone Cloner) (int, error) {
	listed := git(ctx, []string{"ls-tree", "-r", "-z", "--name-only", "HEAD"}, GitOptions{Directory: worktree, Whole: true})
	if listed.ExitCode != 0 {
		return 0, gitFailure(listed, "git ls-tree")
	}
	source, err := NewWorkspace(from, WorkspaceOptions{})
	if err != nil {
		return 0, err
	}
	defer source.Close()
	target, err := NewWorkspace(worktree, WorkspaceOptions{})
	if err != nil {
		return 0, err
	}
	defer target.Close()
	paths := []string{}
	for _, path := range strings.Split(listed.Stdout, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	var mu sync.Mutex
	next, cloned := 0, 0
	var jobs sync.WaitGroup
	for range min(32, len(paths)) {
		jobs.Go(func() {
			for ctx.Err() == nil {
				mu.Lock()
				if next >= len(paths) {
					mu.Unlock()
					return
				}
				path := paths[next]
				next++
				mu.Unlock()
				if _, err := TreePath(path); err != nil {
					continue
				}
				from, err := source.Resolve(path)
				if err != nil {
					continue
				}
				to, err := target.Resolve(path)
				if err != nil {
					continue
				}
				info, err := os.Lstat(from)
				if err != nil || !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
					continue
				}
				if os.MkdirAll(filepath.Dir(to), 0755) != nil {
					continue
				}
				if info.Mode()&os.ModeSymlink != 0 {
					link, err := os.Readlink(from)
					if err == nil {
						os.Symlink(link, to)
					}
				} else if clone(ctx, from, to) {
					mu.Lock()
					cloned++
					mu.Unlock()
				}
			}
		})
	}
	jobs.Wait()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return cloned, nil
}
func SeedWorktree(ctx context.Context, checkout, worktree string, git GitRun, clone Cloner) ([]string, error) {
	seeded := []string{}
	if clone == nil {
		return seeded, nil
	}
	listed := git(ctx, []string{"ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z"}, GitOptions{Directory: checkout, Whole: true})
	if listed.ExitCode != 0 {
		return seeded, nil
	}
	dirs := []string{}
	for _, entry := range strings.Split(listed.Stdout, "\x00") {
		entry = strings.TrimSuffix(entry, "/")
		if entry != "" && seedDirs[filepath.Base(entry)] {
			dirs = append(dirs, entry)
		}
	}
	if len(dirs) == 0 {
		return seeded, nil
	}
	common := git(ctx, []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, GitOptions{Directory: checkout})
	if common.ExitCode != 0 || !CanClone(ctx, clone, strings.TrimSpace(common.Stdout), worktree) {
		return seeded, nil
	}
	source, err := NewWorkspace(checkout, WorkspaceOptions{})
	if err != nil {
		return nil, err
	}
	defer source.Close()
	target, err := NewWorkspace(worktree, WorkspaceOptions{})
	if err != nil {
		return nil, err
	}
	defer target.Close()
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		from, err := source.Resolve(dir)
		if err != nil {
			continue
		}
		to, err := target.Resolve(dir)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(to); !os.IsNotExist(err) {
			continue
		}
		info, err := os.Lstat(from)
		if err != nil || !info.IsDir() {
			continue
		}
		if os.MkdirAll(filepath.Dir(to), 0755) != nil {
			continue
		}
		if clone(ctx, from, to) {
			seeded = append(seeded, dir)
		} else {
			// The clone may have created only part of a dependency directory.
			// Remove only that validated child of this owned worktree.
			if err := fsutil.RemoveTree(context.WithoutCancel(ctx), worktree, to); err != nil {
				return nil, fmt.Errorf("clean incomplete dependency clone: %w", err)
			}
		}
	}
	return seeded, nil
}
