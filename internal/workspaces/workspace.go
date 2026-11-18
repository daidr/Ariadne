package workspaces

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/leases"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/sdk"
)

type workspaceConnection struct {
	workspace sdk.Workspace
	source    leases.Lease[*connection]
}

func (value *workspaceConnection) Close() error {
	defer func() { value.workspace = nil; value.source.Release(); value.source = leases.Lease[*connection]{} }()
	return value.workspace.Close()
}

type workspaceView struct {
	manager              *Manager
	project, thread, key string
	info                 sdk.WorkspaceInfo
}
type connectedWorkspace struct{ *workspaceView }

func workspaceID(project, thread string) string { return project + "\x00" + thread }
func optionalString(value any) *string {
	if text, ok := value.(string); ok {
		return &text
	}
	return nil
}
func copyInfo(info sdk.WorkspaceInfo) sdk.WorkspaceInfo {
	if info.Branch != nil {
		value := *info.Branch
		info.Branch = &value
	}
	if info.Base != nil {
		value := *info.Base
		info.Base = &value
	}
	return info
}
func (view *workspaceView) Info() sdk.WorkspaceInfo { return copyInfo(view.info) }
func (manager *Manager) ForThread(ctx context.Context, project, thread string) (sdk.Workspace, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	unlock, err := manager.gates.lock(ctx, workspaceID(project, thread))
	if err != nil {
		return nil, err
	}
	defer unlock()
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return nil, err
	}
	lease, err := manager.acquireWorkspace(ctx, project, thread, config.key)
	if err != nil {
		return nil, unreadable(err)
	}
	defer lease.Release()
	view := &workspaceView{manager: manager, project: strings.Clone(project), thread: strings.Clone(thread), key: config.key, info: copyInfo(lease.Value.workspace.Info())}
	if _, ok := lease.Value.workspace.(sdk.WorkspaceConnector); ok {
		return &connectedWorkspace{view}, nil
	}
	return view, nil
}
func (manager *Manager) acquireWorkspace(ctx context.Context, project, thread, key string) (leases.Lease[*workspaceConnection], error) {
	var empty leases.Lease[*workspaceConnection]
	manager.mu.Lock()
	blocked := manager.discarding[project]
	manager.mu.Unlock()
	if blocked {
		return empty, errors.New("project workspaces are being discarded")
	}
	current, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return empty, err
	}
	if current.key != key {
		return empty, changedSource()
	}
	known, err := manager.options.Repository.GetThread(ctx, thread)
	if err != nil {
		return empty, err
	}
	if known.ProjectID != project {
		return empty, domainerr.NotFound(thread)
	}
	return manager.workspaces.Acquire(ctx, workspaceID(project, thread), key, func(ctx context.Context) (*workspaceConnection, error) {
		source, err := manager.acquireSource(ctx, project, key)
		if err != nil {
			return nil, err
		}
		keep := false
		defer func() {
			if !keep {
				source.Release()
			}
		}()
		ctx, stop := source.Context(ctx)
		defer stop()
		capability := source.Value.caps.Workspace
		if capability == nil || capability.Open == nil {
			return nil, domainerr.New("error.noSource", nil)
		}
		// Re-read after connecting: another process may have recorded the branch.
		known, err := manager.options.Repository.GetThread(ctx, thread)
		if err != nil {
			return nil, err
		}
		if known.ProjectID != project {
			return nil, domainerr.NotFound(thread)
		}
		settings, err := manager.options.Repository.GetProject(ctx, project)
		if err != nil {
			return nil, err
		}
		branch := projectsettings.ThreadBranch(settings.Settings.Workspace.BranchPrefix, thread, optionalString(known.BranchSlug))
		request := sdk.WorkspaceRequest{ThreadID: thread, Branch: &branch}
		fresh := known.Workspace == nil || known.Workspace.Removed
		if known.Workspace != nil {
			if previous := optionalString(known.Workspace.Branch); previous != nil {
				request.Branch = previous
			}
			base := optionalString(known.Workspace.Base)
			request.Base = &base
		}
		workspace, err := capability.Open(ctx, request)
		if err != nil {
			return nil, err
		}
		if workspace == nil {
			return nil, errors.New("source returned no workspace")
		}
		valid := false
		defer func() {
			if !valid {
				workspace.Close()
			}
		}()
		info := workspace.Info()
		if err := manager.options.Repository.RecordWorkspace(ctx, thread, info.Label, info.Branch, info.Base); err != nil {
			return nil, err
		}
		value := &workspaceConnection{workspace: workspace, source: source}
		if fresh {
			manager.prepareSetup(ctx, settings, known, value, key, capability.Isolated)
		}
		valid, keep = true, true
		return value, nil
	})
}
func withWorkspace[T any](ctx context.Context, view *workspaceView, work func(context.Context, sdk.Workspace) (T, error)) (T, error) {
	var zero T
	ctx, done, err := view.manager.begin(ctx)
	if err != nil {
		return zero, err
	}
	defer done()
	unlock, err := view.manager.gates.lock(ctx, workspaceID(view.project, view.thread))
	if err != nil {
		return zero, err
	}
	lease, err := view.manager.acquireWorkspace(ctx, view.project, view.thread, view.key)
	unlock()
	if err != nil {
		return zero, unreadable(err)
	}
	defer lease.Release()
	ctx, stop := lease.Context(ctx)
	defer stop()
	ctx, stopSource := lease.Value.source.Context(ctx)
	defer stopSource()
	return work(ctx, lease.Value.workspace)
}
func (view *workspaceView) ReadBytes(ctx context.Context, path string, max int64) ([]byte, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) ([]byte, error) { return w.ReadBytes(ctx, path, max) })
}
func (view *workspaceView) List(ctx context.Context, path string) ([]sdk.DirectoryEntry, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) ([]sdk.DirectoryEntry, error) { return w.List(ctx, path) })
}
func (view *workspaceView) ReadFile(ctx context.Context, path string) (string, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) (string, error) { return w.ReadFile(ctx, path) })
}
func (view *workspaceView) FileExists(ctx context.Context, path string) (bool, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, workspace sdk.Workspace) (bool, error) {
		files, ok := workspace.(sdk.WorkspaceFiles)
		if !ok {
			return false, errors.New("the workspace source does not support file patches")
		}
		return files.FileExists(ctx, path)
	})
}

func (view *workspaceView) RemoveFile(ctx context.Context, path string) error {
	_, err := withWorkspace(ctx, view, func(ctx context.Context, workspace sdk.Workspace) (bool, error) {
		files, ok := workspace.(sdk.WorkspaceFiles)
		if !ok {
			return false, errors.New("the workspace source does not support file deletion")
		}
		return true, files.RemoveFile(ctx, path)
	})
	return err
}
func (view *workspaceView) WriteFile(ctx context.Context, path, text string) error {
	_, err := withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) (bool, error) { return true, w.WriteFile(ctx, path, text) })
	return err
}
func (view *workspaceView) Exec(ctx context.Context, command string, options sdk.ExecOptions) (sdk.ExecResult, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) (sdk.ExecResult, error) {
		return w.Exec(ctx, command, options)
	})
}
func (view *workspaceView) ExecFile(ctx context.Context, argv []string, options sdk.ExecOptions) (sdk.ExecResult, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) (sdk.ExecResult, error) {
		return w.ExecFile(ctx, argv, options)
	})
}
func (view *workspaceView) Files(ctx context.Context, path string) (sdk.FileListing, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) (sdk.FileListing, error) {
		if fast, ok := w.(sdk.SourceFiles); ok {
			return fast.Files(ctx, path)
		}
		return sdk.Walk(ctx, w, path)
	})
}
func (view *workspaceView) Search(ctx context.Context, path, pattern string, options sdk.SearchOptions) (sdk.FileSearchResult, error) {
	return withWorkspace(ctx, view, func(ctx context.Context, w sdk.Workspace) (sdk.FileSearchResult, error) {
		if fast, ok := w.(sdk.SourceSearch); ok {
			return fast.Search(ctx, path, pattern, options)
		}
		return sdk.SearchByReading(ctx, w, path, pattern, options)
	})
}
func (view *workspaceView) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx, done, err := view.manager.begin(ctx)
	if err != nil {
		return nil
	}
	defer done()
	unlock, err := view.manager.gates.lock(ctx, workspaceID(view.project, view.thread))
	if err != nil {
		return err
	}
	defer unlock()
	return view.manager.workspaces.RemoveWait(ctx, workspaceID(view.project, view.thread))
}
func (view *connectedWorkspace) Connect(ctx context.Context, port int) (net.Conn, error) {
	// The connection itself pins the workspace until Close; see connectWorkspace.
	return view.manager.connectWorkspace(ctx, view.workspaceView, port)
}
