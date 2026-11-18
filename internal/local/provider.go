package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func DirectoryProvider(dataDir string, clone Cloner, disableClone bool) sdk.ContextProvider {
	return sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "local-directory", Title: sdk.LocalizedText{"zh-CN": "本地目录", "en": "Local directory"}, Provides: []string{"workspace"}, Browse: true, Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{"path": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "目录", "en": "Directory"}, Directory: true, Description: sdk.LocalizedText{"zh-CN": "git 仓库会为每个讨论串建一个独立的 worktree", "en": "A git repository gets a separate worktree per thread"}}}, Required: []string{"path"}}},
		Browse: func(ctx context.Context, _ map[string]any, typed string) (*sdk.DirectoryListing, error) {
			return BrowseLocal(ctx, typed)
		},
		Connect: func(ctx context.Context, settings map[string]any) (sdk.Capabilities, error) {
			path, _ := settings["path"].(string)
			path = textutil.Trim(path)
			if path == "" || !filepath.IsAbs(path) {
				return sdk.Capabilities{}, errors.New("the directory must be an absolute path")
			}
			info, err := os.Stat(path)
			if err == nil && info.Mode().IsRegular() {
				path, err := filepath.EvalSymlinks(path)
				if err != nil {
					return sdk.Capabilities{}, err
				}
				directory, err := NewWorkspace(filepath.Dir(path), WorkspaceOptions{ID: "source", HideGit: true})
				if err != nil {
					return sdk.Capabilities{}, err
				}
				view, err := sdk.SingleFileView(directory, filepath.Base(path), "")
				if err != nil {
					directory.Close()
					return sdk.Capabilities{}, err
				}
				return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{ViewOnly: true, View: func(ctx context.Context) (sdk.SourceView, error) { return view, ctx.Err() }, Open: func(context.Context, sdk.WorkspaceRequest) (sdk.Workspace, error) {
					return nil, fmt.Errorf("%s is a file: it can be read, not worked in", path)
				}}, Close: directory.Close}, nil
			}
			if err != nil || !info.IsDir() {
				return sdk.Capabilities{}, fmt.Errorf("%s is not a directory", path)
			}
			factory, err := NewWorkspaces(ctx, FactoryOptions{Path: path, DataDir: dataDir, Clone: clone, DisableClone: disableClone})
			if err != nil {
				return sdk.Capabilities{}, err
			}
			return sdk.Capabilities{Workspace: factory.Capability(), Close: factory.Close}, nil
		},
	}
}
func (factory *Workspaces) Capability() *sdk.WorkspaceCapability {
	capability := &sdk.WorkspaceCapability{Isolated: factory.Isolated(),
		Open: func(ctx context.Context, input sdk.WorkspaceRequest) (sdk.Workspace, error) {
			return factory.Open(ctx, OpenRequest{ThreadID: input.ThreadID, Branch: input.Branch, Base: input.Base})
		},
		View: func(ctx context.Context) (sdk.SourceView, error) { return factory.View(), ctx.Err() },
	}
	if !factory.Isolated() {
		return capability
	}
	capability.Branches = &sdk.BranchCapability{
		Changes: func(ctx context.Context, thread sdk.ThreadBranch, base string) (*sdk.BranchChanges, error) {
			return factory.Changes(ctx, thread.ThreadID, thread.Branch, base)
		},
		File: func(ctx context.Context, input sdk.FileVersionRequest) (sdk.FileVersions, error) {
			ctx, done, err := factory.begin(ctx)
			if err != nil {
				return sdk.FileVersions{}, err
			}
			defer done()
			return FileVersions(ctx, factory.git, VersionInput{From: input.From, Head: input.Head, Path: input.Path, PreviousPath: input.PreviousPath})
		},
		Merged: func(ctx context.Context, thread sdk.ThreadBranch, base string) (bool, error) {
			ctx, done, err := factory.begin(ctx)
			if err != nil {
				return false, err
			}
			defer done()
			merged := BranchMerged(ctx, factory.git, thread.Branch, "refs/heads/"+base)
			return merged, ctx.Err()
		},
		Merge: func(ctx context.Context, input sdk.MergeRequest) (sdk.MergeOutcome, error) {
			ctx, done, err := factory.begin(ctx)
			if err != nil {
				return sdk.MergeOutcome{}, err
			}
			defer done()
			return MergeBranch(ctx, factory.git, LocalIdentity(factory.repository), MergeInput{Branch: input.Branch, Base: input.Base, Method: input.Method, Message: input.Message, ExpectedHead: input.ExpectedHead})
		},
		UndoMerge: func(ctx context.Context, input sdk.UndoMergeRequest) error {
			ctx, done, err := factory.begin(ctx)
			if err != nil {
				return err
			}
			defer done()
			return UndoMerge(ctx, factory.git, UndoMergeInput{Branch: input.Branch, Base: input.Base, SHA: input.SHA})
		},
		Remove: func(ctx context.Context, threadID string, options sdk.WorktreeRemoveOptions) (bool, error) {
			return factory.Remove(ctx, threadID, RemoveOptions{Discard: options.Discard, Spare: options.Spare})
		},
		Worktrees: factory.Worktrees, SharesStorage: factory.SharesStorage, Snapshot: factory.Snapshot,
		Diff: func(ctx context.Context, from, to string) ([]sdk.ChangedFile, error) {
			ctx, done, err := factory.begin(ctx)
			if err != nil {
				return nil, err
			}
			defer done()
			return TreeDiff(ctx, factory.git, from, to)
		},
	}
	return capability
}
