package sdk

import (
	"context"
	"fmt"
	"time"
)

type ExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type DirectoryEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type ExecOptions struct {
	Directory string
	Timeout   time.Duration
	Shell     []string
	Env       map[string]string
	OnOutput  func(string, string)
}
type WorkspaceInfo struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Branch *string `json:"branch"`
	Base   *string `json:"base"`
	Local  bool    `json:"local,omitempty"`
}
type SourceView interface {
	ReadBytes(context.Context, string, int64) ([]byte, error)
	List(context.Context, string) ([]DirectoryEntry, error)
}
type FileListing struct {
	Files   []string `json:"files"`
	Partial bool     `json:"partial"`
}
type FileSearchResult struct {
	Lines []string `json:"lines"`
	Error *string  `json:"error"`
	More  bool     `json:"more,omitempty"`
}
type SearchOptions struct {
	Glob       string
	IgnoreCase bool
}
type SourceFiles interface {
	Files(context.Context, string) (FileListing, error)
}
type SourceSearch interface {
	Search(context.Context, string, string, SearchOptions) (FileSearchResult, error)
}
type SourceReference interface{ Ref() string }
type Workspace interface {
	SourceView
	Info() WorkspaceInfo
	ReadFile(context.Context, string) (string, error)
	WriteFile(context.Context, string, string) error
	Exec(context.Context, string, ExecOptions) (ExecResult, error)
	ExecFile(context.Context, []string, ExecOptions) (ExecResult, error)
	Close() error
}

type WorkspaceFiles interface {
	FileExists(context.Context, string) (bool, error)
	RemoveFile(context.Context, string) error
}
type WorkspacePathError struct{ Path, Reason string }

func (err *WorkspacePathError) Error() string {
	reason := err.Reason
	if reason == "" {
		reason = "is outside the workspace"
	}
	return err.Path + " " + reason
}

type FileTooLargeError struct {
	Path      string
	Size, Max int64
}

func (err *FileTooLargeError) Error() string {
	return fmt.Sprintf("%s has %d bytes, more than %d", err.Path, err.Size, err.Max)
}
