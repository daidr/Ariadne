package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/sdk"
)

const HostedDirectory = "storageDirectory"

func HostedDirectoryField() sdk.SettingField {
	return sdk.SettingField{Type: "string", Directory: true,
		Title: sdk.LocalizedText{"zh-CN": "仓库存储目录", "en": "Repository storage directory"},
		Description: sdk.LocalizedText{
			"zh-CN": "克隆和工作区放在所选目录下的 ariadne 子目录。留空使用 Ariadne 数据目录。",
			"en":    "Clones and worktrees go in an ariadne subdirectory. Leave blank to use the Ariadne data directory.",
		}}
}

func DefaultHostedDirectoryField() sdk.SettingField {
	field := HostedDirectoryField()
	field.Title = sdk.LocalizedText{"zh-CN": "默认仓库存储目录", "en": "Default repository storage directory"}
	field.Description = sdk.LocalizedText{
		"zh-CN": "添加仓库时预填，可为每个来源修改。",
		"en":    "Prefilled when adding a repository. You can choose a different directory for each source.",
	}
	return field
}

func HostedSourceDefaults(pluginSettings func() map[string]any) map[string]any {
	if pluginSettings != nil {
		if directory, ok := pluginSettings()[HostedDirectory].(string); ok && directory != "" {
			return map[string]any{HostedDirectory: directory}
		}
	}
	return nil
}

func BrowseHostedDirectory(ctx context.Context, _ map[string]any, path string) (*sdk.DirectoryListing, error) {
	return BrowseLocal(ctx, path)
}

func HostedDataDir(defaultDir string, raw any) (string, error) {
	picked, ok := raw.(string)
	if raw == nil || ok && strings.TrimSpace(picked) == "" {
		if defaultDir == "" {
			return "", nil
		}
		return fsutil.CanonicalPath(defaultDir)
	}
	if !ok || !filepath.IsAbs(picked) || strings.ContainsRune(picked, 0) {
		return "", domainerr.New("error.hostedDirectoryAbsolute", nil)
	}
	picked = filepath.Clean(picked)
	if info, err := os.Stat(picked); err == nil && !info.IsDir() {
		return "", domainerr.New("error.hostedDirectoryAbsolute", nil)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	resolved, err := fsutil.CanonicalPath(picked)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, "ariadne", filepath.Base(defaultDir)), nil
}

func CheckHostedDirectoryChange(defaultDir, path string, namespace []string, current, previous any) error {
	next, err := HostedDataDir(defaultDir, current)
	if err != nil {
		return err
	}
	old, err := HostedDataDir(defaultDir, previous)
	if err != nil || cleanPathKey(old) == cleanPathKey(next) {
		return err
	}
	oldBare, oldTrees, err := hostedPaths(old, path, namespace)
	if err != nil {
		return err
	}
	nextBare, nextTrees, err := hostedPaths(next, path, namespace)
	if err != nil {
		return err
	}
	if cleanPathKey(oldBare) == cleanPathKey(nextBare) && cleanPathKey(oldTrees) == cleanPathKey(nextTrees) {
		return nil
	}
	for _, folder := range []string{oldBare, oldTrees} {
		if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
			return domainerr.New("error.hostedDirectoryInUse", nil)
		}
	}
	return nil
}
