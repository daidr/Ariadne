package sdk

import (
	"context"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type BrowsableDirectory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type DirectoryListing struct {
	Path        string               `json:"path"`
	Parent      *string              `json:"parent"`
	Directories []BrowsableDirectory `json:"directories"`
}
type DirectoryNames struct {
	Path  string
	Names []string
}
type ListDirectory func(context.Context, string) (*DirectoryNames, error)
type PathFlavor struct {
	Dir, Base func(string) string
	Join      func(...string) string
	IsAbs     func(string) bool
	Separator string
}

var NativePaths = PathFlavor{Dir: filepath.Dir, Base: filepath.Base, Join: filepath.Join, IsAbs: filepath.IsAbs, Separator: string(filepath.Separator)}
var POSIXPaths = PathFlavor{Dir: path.Dir, Base: path.Base, Join: path.Join, IsAbs: path.IsAbs, Separator: "/"}

type BrowseOptions struct {
	Paths   *PathFlavor
	Compare func(string, string) int
	Lower   func(string) string
	Trim    func(string) string
}
type DirectoryNotFound struct{ Path string }

func (err *DirectoryNotFound) Error() string {
	at := err.Path
	if at == "" {
		at = "the home directory"
	}
	return at + " is not a directory"
}

func BrowseTyped(ctx context.Context, typed string, list ListDirectory, options BrowseOptions) (*DirectoryListing, error) {
	flavor := NativePaths
	if options.Paths != nil {
		flavor = *options.Paths
	}
	trim := strings.TrimSpace
	if options.Trim != nil {
		trim = options.Trim
	}
	text := trim(typed)
	absolute := text != "" && flavor.IsAbs(text)
	dot := absolute && (strings.HasSuffix(text, "/.") || strings.HasSuffix(text, `\.`))
	var found *DirectoryNames
	var err error
	if !dot {
		given := ""
		if absolute {
			given = text
		}
		found, err = list(ctx, given)
		if err != nil {
			return nil, err
		}
	}
	prefix := ""
	endsSep := strings.HasSuffix(text, "/") || strings.HasSuffix(text, `\`)
	if found == nil && absolute && !endsSep {
		prefix = flavor.Base(text)
		// filepath/path.Base clean a final dot, unlike Node's basename.
		if dot {
			prefix = "."
		}
		found, err = list(ctx, flavor.Dir(text))
		if err != nil {
			return nil, err
		}
	}
	if found == nil {
		return nil, &DirectoryNotFound{Path: text}
	}
	lower := strings.ToLower
	if options.Lower != nil {
		lower = options.Lower
	}
	part := lower(prefix)
	names := []string{}
	for _, name := range found.Names {
		if name == "." || name == ".." {
			continue
		}
		if !strings.HasPrefix(prefix, ".") && strings.HasPrefix(name, ".") {
			continue
		}
		if part == "" || strings.HasPrefix(lower(name), part) {
			names = append(names, name)
		}
	}
	compare := options.Compare
	if compare == nil {
		compare = collate.New(language.Und, collate.Numeric, collate.IgnoreCase, collate.IgnoreDiacritics).CompareString
	}
	sort.SliceStable(names, func(i, j int) bool { return compare(names[i], names[j]) < 0 })
	if len(names) > 500 {
		names = names[:500]
	}
	directories := make([]BrowsableDirectory, 0, len(names))
	for _, name := range names {
		directories = append(directories, BrowsableDirectory{Name: name, Path: flavor.Join(found.Path, name)})
	}
	above := flavor.Dir(found.Path)
	var parent *string
	if above != found.Path {
		parent = &above
	}
	return &DirectoryListing{Path: found.Path, Parent: parent, Directories: directories}, nil
}
