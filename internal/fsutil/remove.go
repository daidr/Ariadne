// Package fsutil contains owned-workspace cleanup. Rooted filesystem handles
// prevent a changed symlink or junction from redirecting a recursive removal.
package fsutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func RemoveTree(ctx context.Context, allowedRoot, target string) error {
	root, err := filepath.Abs(allowedRoot)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || !childPath(relative) {
		// An owner may use an alias for its root while its child paths use
		// the canonical spelling. Resolve only the owner, so the target
		// cannot redirect cleanup through a symlink inside the tree.
		resolved, resolveErr := filepath.EvalSymlinks(root)
		if resolveErr == nil {
			relative, err = filepath.Rel(resolved, absolute)
			if err == nil && childPath(relative) {
				root = resolved
			}
		}
		if resolveErr != nil || err != nil || !childPath(relative) {
			return fmt.Errorf("cleanup target is outside its owned directory: %s", target)
		}
	}
	directory, err := os.OpenRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	return RemoveTreeAt(ctx, directory, relative)
}

// RemoveTreeAt removes a subtree below an already opened root, with the same retries as RemoveTree.
func RemoveTreeAt(ctx context.Context, directory *os.Root, relative string) error {
	relative = filepath.Clean(relative)
	if !childPath(relative) {
		return fmt.Errorf("cleanup target is outside its owned directory: %s", relative)
	}
	until := time.Now().Add(3 * time.Second)
	delay := 5 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := directory.RemoveAll(relative)
		if err == nil || !retryRemoval(err) || !time.Now().Before(until) {
			return err
		}
		timer := time.NewTimer(min(delay, time.Until(until)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 100*time.Millisecond)
	}
}

func childPath(path string) bool {
	return path != "." && !filepath.IsAbs(path) && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator))
}
