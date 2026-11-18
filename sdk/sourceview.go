package sdk

import (
	"context"
	"strings"
)

// ReadOnlyView deliberately exposes no write, command or lifecycle methods,
// including through type assertions on the returned interface.
func ReadOnlyView(source SourceView) SourceView { return readOnlyView{source: source} }

type readOnlyView struct{ source SourceView }

func (view readOnlyView) ReadBytes(ctx context.Context, path string, max int64) ([]byte, error) {
	return view.source.ReadBytes(ctx, path, max)
}
func (view readOnlyView) List(ctx context.Context, path string) ([]DirectoryEntry, error) {
	return view.source.List(ctx, path)
}
func (view readOnlyView) Files(ctx context.Context, path string) (FileListing, error) {
	if files, ok := view.source.(SourceFiles); ok {
		return files.Files(ctx, path)
	}
	return Walk(ctx, view.source, path)
}
func (view readOnlyView) Search(ctx context.Context, pattern, path string, options SearchOptions) (FileSearchResult, error) {
	if source, ok := view.source.(SourceSearch); ok {
		return source.Search(ctx, pattern, path, options)
	}
	return SearchByReading(ctx, view.source, pattern, path, options)
}
func (view readOnlyView) Ref() string {
	if source, ok := view.source.(SourceReference); ok {
		return source.Ref()
	}
	return ""
}
func ViewPath(path string) (string, error) {
	if path == "" {
		return ".", nil
	}
	if path[0] == '/' || path[0] == '\\' || len(path) >= 2 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':' {
		return "", &WorkspacePathError{Path: path}
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", &WorkspacePathError{Path: path}
		}
		if strings.EqualFold(part, ".git") {
			return "", &WorkspacePathError{Path: path, Reason: "is kept out of reach"}
		}
	}
	if strings.ContainsRune(path, 0) {
		return "", &WorkspacePathError{Path: path}
	}
	return path, nil
}

type singleFileView struct {
	source    SourceView
	name, ref string
}

func SingleFileView(source SourceView, name, ref string) (SourceView, error) {
	if checked, err := ViewPath(name); err != nil || checked == "." || strings.ContainsAny(name, "/\\") {
		return nil, &WorkspacePathError{Path: name}
	}
	return ReadOnlyView(singleFileView{source: source, name: name, ref: ref}), nil
}
func (view singleFileView) Ref() string { return view.ref }
func (view singleFileView) parts(path string) ([]string, error) {
	if _, err := ViewPath(path); err != nil {
		return nil, err
	}
	parts := []string{}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part != "." {
			parts = append(parts, part)
		}
	}
	return parts, nil
}
func (view singleFileView) ReadBytes(ctx context.Context, path string, max int64) ([]byte, error) {
	parts, err := view.parts(path)
	if err != nil {
		return nil, err
	}
	if len(parts) != 1 || parts[0] != view.name {
		return nil, &WorkspacePathError{Path: path}
	}
	return view.source.ReadBytes(ctx, view.name, max)
}
func (view singleFileView) List(ctx context.Context, path string) ([]DirectoryEntry, error) {
	parts, err := view.parts(path)
	if err != nil {
		return nil, err
	}
	if len(parts) != 0 {
		return nil, &WorkspacePathError{Path: path}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []DirectoryEntry{{Name: view.name, Kind: "file"}}, nil
}
