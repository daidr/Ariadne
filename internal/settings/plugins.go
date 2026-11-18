package settings

import (
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
)

func pluginPaths(config Object) []string {
	paths := []string{}
	for _, raw := range list(config["plugins"]) {
		if path, ok := raw.(string); ok && path != "" && len(path) <= 32768 && !strings.ContainsRune(path, 0) {
			paths = append(paths, path)
		}
	}
	return paths
}

func (store *Store) PluginPaths() []string { return pluginPaths(store.snapshot()) }

func (store *Store) SavePluginPath(path string, maxPaths int) error {
	return store.update(func(next Object) error {
		paths := pluginPaths(next)
		if slices.Contains(paths, path) {
			return domainerr.New("error.pluginExists", Object{"path": path})
		}
		if len(paths) >= maxPaths {
			return bad("too many plugins")
		}
		next["plugins"] = append(paths, path)
		return nil
	})
}

func (store *Store) RemovePluginPath(path string) error {
	return store.update(func(next Object) error {
		paths := pluginPaths(next)
		if !slices.Contains(paths, path) {
			return bad("path")
		}
		next["plugins"] = slices.DeleteFunc(paths, func(item string) bool { return item == path })
		return nil
	})
}
