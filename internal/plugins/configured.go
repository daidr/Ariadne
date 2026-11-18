package plugins

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/textutil"
)

func (host *Host) loadPath(ctx context.Context, path string) error {
	host.mu.Lock()
	for _, entry := range host.loaded {
		if entry.record.Path != nil && *entry.record.Path == path {
			host.mu.Unlock()
			return domainerr.New("error.pluginExists", map[string]any{"path": path})
		}
	}
	host.mu.Unlock()
	root := host.options.ManagedRoot
	if root == "" {
		root = host.options.DataRoot
	}
	file, source, err := pluginFile(path, root, host.options.DataRoot)
	if err != nil {
		return err
	}
	host.mu.Lock()
	for _, entry := range host.loaded {
		if entry.file == file || (runtime.GOOS == "windows" && strings.EqualFold(entry.file, file)) {
			host.mu.Unlock()
			return domainerr.New("error.pluginExists", map[string]any{"path": path})
		}
	}
	host.mu.Unlock()
	plugin, err := compilePlugin(ctx, file, source)
	if err != nil {
		return err
	}
	if err := host.load(ctx, plugin, &path); err != nil {
		return err
	}
	host.mu.Lock()
	host.loaded[len(host.loaded)-1].file = file
	host.failed = slices.DeleteFunc(host.failed, func(entry Record) bool { return entry.Path != nil && *entry.Path == path })
	host.mu.Unlock()
	return nil
}

// Restore isolates failed paths so a removed or legacy script does not prevent
// the server or the other plugins from starting.
func (host *Host) Restore(ctx context.Context, store *settings.Store) error {
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	seen := map[string]bool{}
	for _, path := range store.PluginPaths() {
		if seen[path] {
			continue
		}
		if len(seen) >= MaxPlugins {
			break
		}
		seen[path] = true
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := host.loadPath(ctx, path); err != nil {
			var localized *domainerr.Error
			if errors.As(err, &localized) && localized.Key == "error.pluginExists" {
				// Restore can be called more than once for the same host.
				host.mu.Lock()
				exact := slices.ContainsFunc(host.loaded, func(entry *loaded) bool { return entry.record.Path != nil && *entry.record.Path == path })
				host.mu.Unlock()
				if exact {
					continue
				}
			}
			host.rememberFailure(path, err)
		}
	}
	return ctx.Err()
}

func (host *Host) AddPath(ctx context.Context, store *settings.Store, raw any) error {
	path, _ := raw.(string)
	path = textutil.Trim(path)
	if path == "" || len(path) > 32768 || strings.ContainsRune(path, 0) {
		return domainerr.New("error.invalidBody", map[string]any{"reason": "path"})
	}
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if paths := store.PluginPaths(); slices.Contains(paths, path) {
		return domainerr.New("error.pluginExists", map[string]any{"path": path})
	} else if len(paths) >= MaxPlugins {
		return domainerr.New("error.pluginLoad", map[string]any{"detail": "too many configured plugins"})
	}
	if err := host.loadPath(ctx, path); err != nil {
		if errors.Is(err, ErrWorkspacePlugin) {
			return domainerr.New("error.pluginInWorkspace", nil)
		}
		var localized *domainerr.Error
		if errors.As(err, &localized) {
			return localized
		}
		return domainerr.New("error.pluginLoad", map[string]any{"detail": strings.Clone(textutil.HeadUnits(err.Error(), 1024))})
	}
	if err := ctx.Err(); err != nil {
		host.report(host.unloadPath(path))
		return err
	}
	if err := store.SavePluginPath(path, MaxPlugins); err != nil {
		host.report(host.unloadPath(path))
		return err
	}
	return nil
}

func (host *Host) RemovePath(ctx context.Context, store *settings.Store, raw any) error {
	path, ok := raw.(string)
	if !ok || path == "" {
		return domainerr.New("error.invalidBody", map[string]any{"reason": "path"})
	}
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep the running plugin if the settings update cannot be written.
	if err := store.RemovePluginPath(path); err != nil {
		return err
	}
	host.report(host.unloadPath(path))
	return nil
}
