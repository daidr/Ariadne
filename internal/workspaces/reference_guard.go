package workspaces

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

var credentialPaths = []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".config/git/credentials", ".password-store", ".config/gh", ".config/glab-cli", ".config/gcloud", ".config/op", ".local/share/keyrings", "Library/Keychains"}
var drivePart = regexp.MustCompile(`^[A-Za-z]:$`)

// Resolve the longest existing ancestor too: a link followed by a missing child
// must not make an otherwise protected reference look safe.
func realPath(path string) string {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	parent := absolute
	var missing []string
	for {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return filepath.Clean(absolute)
		}
		missing = append(missing, filepath.Base(parent))
		parent = next
	}
}
func pathInside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
func LocalRefusal(path string, own []string, home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	real := realPath(path)
	protected := make([]string, 0, len(own)+len(credentialPaths))
	for _, path := range own {
		if path != "" {
			protected = append(protected, realPath(path))
		}
	}
	for _, path := range credentialPaths {
		protected = append(protected, realPath(filepath.Join(home, filepath.FromSlash(path))))
	}
	for _, pathRoot := range protected {
		if pathInside(real, pathRoot) {
			return path + " is kept out of reach: credentials and Ariadne's own data are never read this way"
		}
	}
	for _, pathRoot := range append(protected, realPath(home)) {
		if pathInside(pathRoot, real) {
			return path + " is too wide: it holds places that are kept out of reach. Ask for the one directory or file the task needs"
		}
	}
	return ""
}
func remoteParts(path string) ([]string, bool) {
	typed := strings.ReplaceAll(path, "\\", "/")
	rooted := strings.HasPrefix(typed, "/")
	parts := []string{}
	for _, part := range strings.Split(typed, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) > 0 && parts[len(parts)-1] != "~" && !drivePart.MatchString(parts[len(parts)-1]) {
				parts = parts[:len(parts)-1]
			} else if !rooted || len(parts) > 0 {
				return nil, false
			}
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 && drivePart.MatchString(parts[0]) {
		parts = parts[1:]
	}
	return parts, true
}
func RemoteRefusal(path string) string {
	parts, ok := remoteParts(textutil.Trim(path))
	if !ok {
		return path + " climbs out of where it starts with \"..\": spell the directory out"
	}
	spelled := "/" + strings.Join(parts, "/") + "/"
	wide := len(parts) == 0 || len(parts) == 1 && (parts[0] == "~" || parts[0] == "root" || parts[0] == "home" || parts[0] == "Users") || len(parts) == 2 && (parts[0] == "home" || parts[0] == "Users")
	for _, name := range credentialPaths {
		components := strings.Split(name, "/")
		for i := 1; i < len(components); i++ {
			if strings.HasSuffix(spelled, "/"+strings.Join(components[:i], "/")+"/") {
				wide = true
			}
		}
	}
	if wide {
		return path + " is too wide: ask for the one directory or file the task needs"
	}
	for _, name := range credentialPaths {
		if strings.Contains(spelled, "/"+name+"/") {
			return path + " is kept out of reach: credentials are never read this way"
		}
	}
	return ""
}

// ReferenceRefusal is applied to AI requests to add references. References a
// user configures manually keep the existing, unrestricted configuration flow.
func ReferenceRefusal(provider sdk.ContextProviderInfo, settings map[string]any, own []string) string {
	for _, name := range provider.Settings.FieldOrder {
		field := provider.Settings.Properties[name]
		if field.Type != "string" || !field.Directory {
			continue
		}
		path, ok := settings[name].(string)
		if !ok || textutil.Trim(path) == "" {
			continue
		}
		refusal := ""
		if provider.Remote {
			refusal = RemoteRefusal(path)
		} else {
			refusal = LocalRefusal(path, own, "")
		}
		if refusal != "" {
			return refusal
		}
	}
	// Schemas assembled natively need not declare field order.
	for name, field := range provider.Settings.Properties {
		if field.Type != "string" || !field.Directory {
			continue
		}
		path, ok := settings[name].(string)
		if !ok || textutil.Trim(path) == "" {
			continue
		}
		if provider.Remote {
			if why := RemoteRefusal(path); why != "" {
				return why
			}
		} else if why := LocalRefusal(path, own, ""); why != "" {
			return why
		}
	}
	return ""
}
func (manager *Manager) ReferenceRefusal(reference sources.Definition, own []string) string {
	_, done, err := manager.begin(context.Background())
	if err != nil {
		return err.Error()
	}
	defer done()
	provider, ok := manager.options.Sources.Get(reference.Kind)
	if !ok {
		return ""
	}
	return ReferenceRefusal(provider.Info, reference.Settings, own)
}
