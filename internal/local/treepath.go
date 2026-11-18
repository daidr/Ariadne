package local

import "strings"

func TreePath(path string) (string, error) {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") || len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\') || strings.ContainsRune(path, 0) {
		return "", pathError(path)
	}
	parts := []string{}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", pathError(path)
		}
		if part != "." {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/"), nil
}
