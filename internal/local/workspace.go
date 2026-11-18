// Package local implements rooted files and local repository workspaces.
package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
	"golang.org/x/text/encoding/unicode"
)

const MaxTextBytes = 64 << 20

type WorkspaceOptions struct {
	ID           string
	Branch, Base *string
	HideGit      bool
}
type Workspace struct {
	mu          sync.RWMutex
	root        *os.Root
	directory   string
	inputDir    string
	options     WorkspaceOptions
	life        context.Context
	cancel      context.CancelFunc
	probeMu     sync.Mutex
	probes      map[string]*probeResult
	probeJobs   sync.WaitGroup
	onClose     func()
	closeHook   sync.Once
	commandJobs sync.WaitGroup
}

func NewWorkspace(directory string, options WorkspaceOptions) (*Workspace, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, err
	}
	if options.Branch != nil {
		options.Branch = new(*options.Branch)
	}
	if options.Base != nil {
		options.Base = new(*options.Base)
	}
	life, cancel := context.WithCancel(context.Background())
	return &Workspace{root: root, directory: resolved, inputDir: absolute, options: options, life: life, cancel: cancel}, nil
}
func (workspace *Workspace) Info() sdk.WorkspaceInfo {
	info := sdk.WorkspaceInfo{ID: workspace.options.ID, Label: workspace.directory, Local: true}
	if workspace.options.Branch != nil {
		info.Branch = new(*workspace.options.Branch)
	}
	if workspace.options.Base != nil {
		info.Base = new(*workspace.options.Base)
	}
	return info
}
func (workspace *Workspace) active(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(workspace.life, cancel)
	if workspace.life.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
func within(target, root string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
func pathError(path string) error { return &sdk.WorkspacePathError{Path: path} }
func (workspace *Workspace) resolve(path string) (string, error) {
	target := filepath.Clean(filepath.Join(workspace.directory, filepath.FromSlash(path)))
	if filepath.IsAbs(path) {
		target = filepath.Clean(path)
		if !within(target, workspace.directory) && within(target, workspace.inputDir) {
			// The caller may use the spelling with which the workspace was
			// opened, such as /var on macOS or an 8.3 path on Windows.
			relative, _ := filepath.Rel(workspace.inputDir, target)
			target = filepath.Join(workspace.directory, relative)
		}
	}
	if !within(target, workspace.directory) {
		return "", pathError(path)
	}
	existing := target
	missing := []string{}
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || existing == filepath.Dir(existing) {
			return "", pathError(path)
		}
		missing = append(missing, filepath.Base(existing))
		existing = filepath.Dir(existing)
	}
	found, err := filepath.EvalSymlinks(existing)
	if err != nil || !within(found, workspace.directory) {
		return "", pathError(path)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		found = filepath.Join(found, missing[index])
	}
	if workspace.options.HideGit {
		relative, err := filepath.Rel(workspace.directory, found)
		if err != nil {
			return "", pathError(path)
		}
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			if strings.EqualFold(part, ".git") {
				return "", &sdk.WorkspacePathError{Path: path, Reason: "is kept out of reach"}
			}
		}
	}
	return found, nil
}
func (workspace *Workspace) Resolve(path string) (string, error) {
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	if workspace.root == nil {
		return "", errors.New("workspace is closed")
	}
	return workspace.resolve(path)
}
func (workspace *Workspace) relative(path string) (string, error) {
	resolved, err := workspace.resolve(path)
	if err != nil {
		return "", err
	}
	return filepath.Rel(workspace.directory, resolved)
}
func (workspace *Workspace) open(path string, flags int) (*os.File, error) {
	relative, err := workspace.relative(path)
	if err != nil {
		return nil, err
	}
	file, err := workspace.root.OpenFile(relative, flags, 0666)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	if err != nil {
		return nil, pathError(path)
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	now, err := workspace.relative(path)
	if err != nil {
		file.Close()
		return nil, err
	}
	current, err := workspace.root.Stat(now)
	if err != nil || !os.SameFile(opened, current) {
		file.Close()
		return nil, pathError(path)
	}
	return file, nil
}
func (workspace *Workspace) ReadBytes(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	ctx, done := workspace.active(ctx)
	defer done()
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	if workspace.root == nil {
		return nil, errors.New("workspace is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 0 || maxBytes > MaxTextBytes {
		return nil, errors.New("workspace read limit must be between 0 and 64 MiB")
	}
	file, err := workspace.open(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, &sdk.FileTooLargeError{Path: path, Size: info.Size(), Max: maxBytes}
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("workspace path is not a regular file")
	}
	reader := io.LimitReader(file, maxBytes+1)
	result := make([]byte, 0, min(info.Size(), maxBytes))
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if int64(len(result))+int64(count) > maxBytes {
				return nil, &sdk.FileTooLargeError{Path: path, Size: int64(len(result)) + int64(count), Max: maxBytes}
			}
			result = append(result, buffer[:count]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if result == nil {
		result = []byte{}
	}
	return result, nil
}
func (workspace *Workspace) ReadFile(ctx context.Context, path string) (string, error) {
	data, err := workspace.ReadBytes(ctx, path, MaxTextBytes)
	if err != nil {
		return "", err
	}
	decoded, err := unicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded), err
}
func (workspace *Workspace) FileExists(ctx context.Context, path string) (bool, error) {
	ctx, done := workspace.active(ctx)
	defer done()
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	if workspace.root == nil {
		return false, errors.New("workspace is closed")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	relative, err := workspace.relative(path)
	if err != nil {
		return false, err
	}
	info, err := workspace.root.Stat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, &sdk.WorkspacePathError{Path: path, Reason: "is not a regular file"}
	}
	return true, nil
}

func (workspace *Workspace) RemoveFile(ctx context.Context, path string) error {
	ctx, done := workspace.active(ctx)
	defer done()
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.root == nil {
		return errors.New("workspace is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lexical, err := sdk.ViewPath(path)
	if err != nil {
		return err
	}
	relative, err := workspace.relative(path)
	if err != nil {
		return err
	}
	if difference, err := filepath.Rel(relative, filepath.Clean(filepath.FromSlash(lexical))); err != nil || difference != "." {
		return pathError(path)
	}
	info, err := workspace.root.Lstat(relative)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return &sdk.WorkspacePathError{Path: path, Reason: "is not a regular file"}
	}
	return workspace.root.Remove(relative)
}

func (workspace *Workspace) WriteFile(ctx context.Context, path, content string) error {
	ctx, done := workspace.active(ctx)
	defer done()
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	if workspace.root == nil {
		return errors.New("workspace is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	relative, err := workspace.relative(path)
	if err != nil {
		return err
	}
	if err := workspace.root.MkdirAll(filepath.Dir(relative), 0755); err != nil {
		return err
	}
	file, err := workspace.open(path, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		piece := content[:min(len(content), 32<<10)]
		count, err := file.WriteString(piece)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		content = content[count:]
	}
	return nil
}
func (workspace *Workspace) List(ctx context.Context, path string) ([]sdk.DirectoryEntry, error) {
	ctx, done := workspace.active(ctx)
	defer done()
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	if workspace.root == nil {
		return nil, errors.New("workspace is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	relative, err := workspace.relative(path)
	if err != nil {
		return nil, err
	}
	directory, err := workspace.root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	result := []sdk.DirectoryEntry{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := directory.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() == ".git" {
				continue
			}
			kind := "file"
			if entry.IsDir() {
				kind = "directory"
			}
			result = append(result, sdk.DirectoryEntry{Name: entry.Name(), Kind: kind})
			if len(result) > 100000 {
				return nil, errors.New("workspace directory entry count exceeded its limit")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	compare := nameComparator()
	sort.SliceStable(result, func(i, j int) bool { return compare(result[i].Name, result[j].Name) < 0 })
	return result, nil
}
func (workspace *Workspace) Exec(ctx context.Context, command string, options sdk.ExecOptions) (sdk.ExecResult, error) {
	shell := options.Shell
	if len(shell) == 0 {
		shell = process.DefaultShell().Argv
	}
	return workspace.ExecFile(ctx, process.ShellArgv(shell, command), options)
}
func (workspace *Workspace) ExecFile(ctx context.Context, argv []string, options sdk.ExecOptions) (sdk.ExecResult, error) {
	ctx, done := workspace.active(ctx)
	defer done()
	directory := options.Directory
	if directory == "" {
		directory = "."
	}
	workspace.mu.RLock()
	if workspace.root == nil || workspace.life.Err() != nil {
		workspace.mu.RUnlock()
		return sdk.ExecResult{}, errors.New("workspace is closed")
	}
	resolved, err := workspace.resolve(directory)
	if err != nil {
		workspace.mu.RUnlock()
		return sdk.ExecResult{}, err
	}
	workspace.commandJobs.Add(1)
	workspace.mu.RUnlock()
	defer workspace.commandJobs.Done()
	return process.Run(ctx, argv, process.RunOptions{Directory: resolved, Timeout: options.Timeout, Env: options.Env, ForThread: true, OnOutput: options.OnOutput}), nil
}
func (workspace *Workspace) Close() error {
	defer workspace.closeHook.Do(func() {
		if workspace.onClose != nil {
			workspace.onClose()
		}
	})
	workspace.cancel()
	workspace.probeMu.Lock()
	workspace.probeMu.Unlock()
	workspace.probeJobs.Wait()
	workspace.mu.Lock()
	if workspace.root == nil {
		workspace.mu.Unlock()
		workspace.commandJobs.Wait()
		return nil
	}
	err := workspace.root.Close()
	workspace.root = nil
	workspace.mu.Unlock()
	workspace.commandJobs.Wait()
	return err
}
