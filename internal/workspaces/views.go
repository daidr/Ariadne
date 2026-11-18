package workspaces

import (
	"context"
	"errors"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

type sourceView struct {
	use func(context.Context, func(context.Context, sdk.SourceView) error) error
	ref string
}

func (view *sourceView) Ref() string { return view.ref }
func (view *sourceView) ReadBytes(ctx context.Context, path string, max int64) ([]byte, error) {
	var value []byte
	err := view.use(ctx, func(ctx context.Context, source sdk.SourceView) error {
		var err error
		value, err = source.ReadBytes(ctx, path, max)
		return err
	})
	return value, err
}
func (view *sourceView) List(ctx context.Context, path string) ([]sdk.DirectoryEntry, error) {
	var value []sdk.DirectoryEntry
	err := view.use(ctx, func(ctx context.Context, source sdk.SourceView) error {
		var err error
		value, err = source.List(ctx, path)
		return err
	})
	return value, err
}
func (view *sourceView) Files(ctx context.Context, path string) (sdk.FileListing, error) {
	var value sdk.FileListing
	err := view.use(ctx, func(ctx context.Context, source sdk.SourceView) error {
		var err error
		if fast, ok := source.(sdk.SourceFiles); ok {
			value, err = fast.Files(ctx, path)
		} else {
			value, err = sdk.Walk(ctx, source, path)
		}
		return err
	})
	return value, err
}
func (view *sourceView) Search(ctx context.Context, path, pattern string, options sdk.SearchOptions) (sdk.FileSearchResult, error) {
	var value sdk.FileSearchResult
	err := view.use(ctx, func(ctx context.Context, source sdk.SourceView) error {
		var err error
		if fast, ok := source.(sdk.SourceSearch); ok {
			value, err = fast.Search(ctx, path, pattern, options)
		} else {
			value, err = sdk.SearchByReading(ctx, source, path, pattern, options)
		}
		return err
	})
	return value, err
}
func (manager *Manager) View(ctx context.Context, project string) (sdk.SourceView, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return nil, err
	}
	key := config.key
	use := func(ctx context.Context, work func(context.Context, sdk.SourceView) error) error {
		_, err := withSource(ctx, manager, project, key, func(ctx context.Context, caps sdk.Capabilities) (struct{}, error) {
			if caps.Workspace == nil || caps.Workspace.View == nil {
				return struct{}{}, errors.New("this source cannot be read from here; a thread can read it in its workspace")
			}
			view, err := caps.Workspace.View(ctx)
			if err != nil {
				return struct{}{}, err
			}
			if view == nil {
				return struct{}{}, errors.New("source returned no view")
			}
			return struct{}{}, work(ctx, view)
		})
		return unreadable(err)
	}
	view := &sourceView{use: use}
	if err := use(ctx, func(_ context.Context, source sdk.SourceView) error {
		if reference, ok := source.(sdk.SourceReference); ok {
			view.ref = reference.Ref()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return sdk.ReadOnlyView(view), nil
}

type referenceConfig struct {
	reference sources.Reference
	secrets   map[string]any
	key, id   string
}

func (manager *Manager) referenceConfig(ctx context.Context, projectID, name, threadID string) (referenceConfig, error) {
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return referenceConfig{}, err
	}
	var own []projectReference
	if threadID != "" {
		if thread, err := manager.options.Repository.GetThread(ctx, threadID); err == nil && thread.ProjectID == projectID {
			own = references(thread.References)
		}
	}
	var picked *projectReference
	for _, reference := range append(own, references(project.References)...) {
		if reference.Name == name {
			copy := reference
			picked = &copy
			break
		}
	}
	if picked == nil {
		return referenceConfig{}, domainerr.New("error.noReference", map[string]any{"name": name})
	}
	definition, err := sources.DecodeDefinition(project.Source)
	if err != nil {
		return referenceConfig{}, err
	}
	secrets := map[string]any{}
	if manager.options.Secrets != nil {
		secrets = manager.options.Secrets(projectID)
	}
	reference := sources.Reference{Part: sources.Part{Kind: picked.Kind, Settings: picked.Settings}, ID: picked.ID}
	pickedSecrets := manager.options.Sources.ReferenceSecrets(definition, reference, secrets)
	hashed, err := secretDigests(pickedSecrets)
	if err != nil {
		return referenceConfig{}, unreadable(err)
	}
	key, err := digest([]any{reference.Part, hashed, manager.options.Sources.Generation(reference.Kind)})
	if err != nil {
		return referenceConfig{}, unreadable(err)
	}
	return referenceConfig{reference: reference, secrets: pickedSecrets, key: key, id: projectID + "\x00" + reference.ID}, nil
}
func (manager *Manager) ReferenceView(ctx context.Context, projectID, name, threadID string) (sdk.SourceView, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	config, err := manager.referenceConfig(ctx, projectID, name, threadID)
	if err != nil {
		return nil, err
	}
	id, key := config.id, config.key
	use := func(ctx context.Context, work func(context.Context, sdk.SourceView) error) error {
		ctx, done, err := manager.begin(ctx)
		if err != nil {
			return err
		}
		defer done()
		current, err := manager.referenceConfig(ctx, projectID, name, threadID)
		if err != nil {
			return domainerr.New("error.referenceUnreadable", map[string]any{"name": name, "detail": referenceDetail(err)})
		}
		if current.id != id || current.key != key {
			return referenceChanged(name)
		}
		lease, err := manager.references.Acquire(ctx, id, key, func(ctx context.Context) (*referenceConnection, error) {
			value, err := manager.options.Sources.ConnectReference(ctx, current.reference.Part, current.secrets)
			if err != nil {
				return nil, err
			}
			latest, err := manager.referenceConfig(ctx, projectID, name, threadID)
			if err != nil || latest.key != key || latest.id != id {
				value.Close()
				if err != nil {
					return nil, err
				}
				return nil, referenceChanged(name)
			}
			return &referenceConnection{value: value}, nil
		})
		if err != nil {
			return domainerr.New("error.referenceUnreadable", map[string]any{"name": name, "detail": referenceDetail(err)})
		}
		defer lease.Release()
		ctx, stop := lease.Context(ctx)
		defer stop()
		view, err := lease.Value.value.View(ctx)
		if err == nil {
			err = work(ctx, view)
		}
		if err != nil {
			return domainerr.New("error.referenceUnreadable", map[string]any{"name": name, "detail": referenceDetail(err)})
		}
		return nil
	}
	view := &sourceView{use: use}
	if err := use(ctx, func(_ context.Context, source sdk.SourceView) error {
		if ref, ok := source.(sdk.SourceReference); ok {
			view.ref = ref.Ref()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return sdk.ReadOnlyView(view), nil
}
func (manager *Manager) CheckReference(ctx context.Context, projectID string, reference sources.Part, secrets map[string]any) (string, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	definition, err := sources.DecodeDefinition(project.Source)
	if err != nil {
		return "", err
	}
	all := map[string]any{}
	if manager.options.Secrets != nil {
		all = manager.options.Secrets(projectID)
	}
	borrowed := manager.options.Sources.ReferenceSecrets(definition, sources.Reference{Part: reference}, all)
	for key, value := range secrets {
		borrowed[key] = value
	}
	connected, err := manager.options.Sources.ConnectReference(ctx, reference, borrowed)
	if err != nil {
		return "", err
	}
	defer connected.Close()
	view, err := connected.View(ctx)
	if err == nil {
		_, err = view.List(ctx, ".")
	}
	if err != nil {
		return "", unreadable(err)
	}
	if summary := manager.options.Sources.Summary(reference); summary != nil {
		return *summary, nil
	}
	return reference.Kind, nil
}

type ReferenceDescription struct {
	Name string `json:"name"`
	Line string `json:"line"`
	Own  bool   `json:"own,omitempty"`
}

func (manager *Manager) References(ctx context.Context, projectID, locale, threadID string) ([]ReferenceDescription, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := []ReferenceDescription{}
	for _, ref := range references(project.References) {
		out = append(out, ReferenceDescription{Name: ref.Name, Line: manager.describePart(sources.Part{Kind: ref.Kind, Settings: ref.Settings}, locale)})
	}
	if threadID != "" {
		if thread, err := manager.options.Repository.GetThread(ctx, threadID); err == nil && thread.ProjectID == projectID {
			for _, ref := range references(thread.References) {
				out = append(out, ReferenceDescription{Name: ref.Name, Line: manager.describePart(sources.Part{Kind: ref.Kind, Settings: ref.Settings}, locale), Own: true})
			}
		}
	}
	return out, nil
}
func referenceChanged(name string) error {
	return domainerr.New("error.referenceUnreadable", map[string]any{"name": name, "detail": "reference source changed; read it again"})
}
