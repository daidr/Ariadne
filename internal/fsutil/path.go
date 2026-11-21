package fsutil

import (
	"errors"
	"os"
	"path/filepath"
)

// CanonicalPath resolves symlinks and Windows 8.3 short names on the deepest
// existing ancestor of path, then reattaches any not-yet-created segments.
func CanonicalPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return filepath.Clean(absolute), nil
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}
}
