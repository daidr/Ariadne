package plugins

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxScript = 256 << 10

var ErrWorkspacePlugin = errors.New("plugins cannot be loaded from application data or a managed worktree")

func pluginFile(input string, managedRoots ...string) (string, []byte, error) {
	if strings.HasPrefix(input, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		input = filepath.Join(home, filepath.FromSlash(input[2:]))
	}
	if !filepath.IsAbs(input) || len(input) > 32768 {
		return "", nil, errors.New("an absolute plugin path is required")
	}
	roots := make([]string, 0, 2*len(managedRoots))
	for _, name := range managedRoots {
		root, err := filepath.EvalSymlinks(name)
		if err != nil {
			return "", nil, err
		}
		roots = append(roots, filepath.Clean(name), root)
	}
	resolve := func(path string) (string, os.FileInfo, error) {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", nil, err
		}
		for _, candidate := range []string{path, resolved} {
			for _, root := range roots {
				relative, err := filepath.Rel(root, candidate)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return "", nil, ErrWorkspacePlugin
				}
			}
			for _, part := range strings.FieldsFunc(candidate, func(r rune) bool { return r == '/' || r == '\\' }) {
				if strings.EqualFold(part, ".ariadne-worktrees") {
					return "", nil, ErrWorkspacePlugin
				}
			}
		}
		info, err := os.Stat(resolved)
		return resolved, info, err
	}
	resolved, info, err := resolve(filepath.Clean(input))
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		resolved, info, err = resolve(filepath.Join(resolved, "plugin.lua"))
		if err != nil {
			return "", nil, err
		}
	}
	if !strings.EqualFold(filepath.Ext(resolved), ".lua") {
		return "", nil, errors.New("expected a .lua file or a directory containing plugin.lua")
	}
	if !info.Mode().IsRegular() || info.Size() > maxScript {
		return "", nil, errors.New("plugin script must be a regular file of at most 256 KiB")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("plugin script must be a regular file")
	}
	source, err := io.ReadAll(io.LimitReader(file, maxScript+1))
	if err != nil {
		return "", nil, err
	}
	if len(source) > maxScript {
		return "", nil, errors.New("plugin script exceeds 256 KiB")
	}
	return resolved, source, nil
}
