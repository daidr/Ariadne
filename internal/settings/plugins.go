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

func (store *Store) PluginValues(id string) Object {
	return cloneMap(object(object(store.snapshot()["pluginSettings"])[id]))
}

func (store *Store) SavePluginValues(id string, changes Object) error {
	return store.update(func(config Object) error {
		plugins := object(config["pluginSettings"])
		values := object(plugins[id])
		for key, value := range changes {
			if value == nil {
				delete(values, key)
			} else {
				values[key] = value
			}
		}
		if len(values) == 0 {
			delete(plugins, id)
		} else {
			plugins[id] = values
		}
		if len(plugins) == 0 {
			delete(config, "pluginSettings")
		} else {
			config["pluginSettings"] = plugins
		}
		return nil
	})
}

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
