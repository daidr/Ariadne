package skills

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/sdk"
)

func decimal(value int) string { return strconv.Itoa(value) }

type directoryFiles struct{ dir string }

// Linked skills are allowed at the top level. Once inside a skill, use an
// os.Root so symlink changes cannot escape its real folder between check/read.
func (files directoryFiles) open(name string) (*os.File, error) {
	if name == "." {
		return os.Open(files.dir)
	}
	name = inside(name)
	if name == "" {
		return nil, errors.New("outside the skill")
	}
	first, rest, sub := strings.Cut(name, "/")
	if !sub {
		return os.Open(filepath.Join(files.dir, first))
	}
	folder, err := filepath.EvalSymlinks(filepath.Join(files.dir, first))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(filepath.FromSlash(rest))
}
func (files directoryFiles) ReadBytes(ctx context.Context, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := files.open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a file")
	}
	if info.Size() > limit {
		return nil, &sdk.FileTooLargeError{Path: name, Size: info.Size(), Max: limit}
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, &sdk.FileTooLargeError{Path: name, Size: int64(len(data)), Max: limit}
	}
	if err == nil {
		err = ctx.Err()
	}
	return data, err
}
func (files directoryFiles) List(ctx context.Context, name string) ([]sdk.DirectoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := files.open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	found := []sdk.DirectoryEntry{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mode := entry.Type()
		kind := ""
		if mode.IsRegular() {
			kind = "file"
		} else if mode.IsDir() {
			kind = "directory"
		} else if mode&os.ModeSymlink != 0 {
			child, err := files.open(under(name, entry.Name()))
			if err != nil {
				continue
			}
			info, err := child.Stat()
			child.Close()
			if err != nil {
				continue
			}
			if info.IsDir() && name == "." {
				kind = "directory"
			} else if info.Mode().IsRegular() {
				kind = "file"
			}
		}
		if kind != "" {
			found = append(found, sdk.DirectoryEntry{Name: entry.Name(), Kind: kind})
		}
	}
	return found, nil
}
