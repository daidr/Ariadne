// Package workspaces connects project sources and owns their lifetimes across
// turns. Exposed views acquire a lease for each operation and stay read-only.
package workspaces

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/cache"
	"github.com/daidr/Ariadne/internal/commandpolicy"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/leases"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

type Options struct {
	Repository   *core.Repository
	Sources      *sources.Registry
	Secrets      func(string) map[string]any
	Env          func(string) map[string]string
	State        *realtime.State
	CacheTTL     time.Duration
	Now          func() time.Time
	CommandRules func(project string) []commandpolicy.Rule
	SetupTimeout func(project string) time.Duration
}
type connection struct{ caps sdk.Capabilities }

func (value *connection) Close() error {
	if value == nil {
		return nil
	}
	defer func() { value.caps = sdk.Capabilities{} }()
	var err error
	if value.caps.Close != nil {
		err = value.caps.Close()
	}
	value.caps = sdk.Capabilities{}
	return err
}

type referenceConnection struct{ value sources.ReferenceConnection }

func (value *referenceConnection) Close() error {
	if value == nil {
		return nil
	}
	defer func() { value.value = sources.ReferenceConnection{} }()
	var err error
	if value.value.Close != nil {
		err = value.value.Close()
	}
	value.value = sources.ReferenceConnection{}
	return err
}

type Manager struct {
	mu          sync.Mutex
	life        context.Context
	cancel      context.CancelFunc
	operations  sync.WaitGroup
	closed      bool
	done        chan struct{}
	closeErr    error
	options     Options
	connections *leases.Pool[*connection]
	references  *leases.Pool[*referenceConnection]
	Background  *background.Commands
	workspaces  *leases.Pool[*workspaceConnection]
	mentions    *leases.Pool[*mentionList]
	gates       threadLocks
	notes       *cache.Cache[string, setupNote]
	discarding  map[string]bool
}

func New(options Options) (*Manager, error) {
	if options.Repository == nil || options.Sources == nil {
		return nil, errors.New("workspace manager needs repository and source registry")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	life, cancel := context.WithCancel(context.Background())
	notes, err := cache.New[string, setupNote](cache.Options{MaxEntries: 4096, MaxWeight: 4 << 20, TTL: 24 * time.Hour})
	if err != nil {
		cancel()
		return nil, err
	}
	manager := &Manager{options: options, life: life, cancel: cancel, done: make(chan struct{}), connections: leases.New[*connection](leases.Options{Idle: 16, TTL: options.CacheTTL}), references: leases.New[*referenceConnection](leases.Options{Idle: 32, TTL: options.CacheTTL})}
	manager.notes = notes
	manager.discarding = map[string]bool{}
	manager.workspaces = leases.New[*workspaceConnection](leases.Options{Idle: 32, TTL: options.CacheTTL})
	manager.mentions = leases.New[*mentionList](leases.Options{Idle: 16, TTL: 30 * time.Second, LoadTimeout: 20 * time.Second, Now: options.Now})
	manager.Background = background.New(background.Options{OnChange: func(thread string) {
		if options.State == nil {
			return
		}
		conversation, err := options.Repository.GetConversation(context.Background(), thread)
		if err != nil {
			return
		}
		commands := manager.Background.List(thread)
		views := []map[string]any{}
		for _, command := range commands {
			views = append(views, map[string]any{"id": command.ID, "command": command.Command, "startedAt": command.StartedAt, "url": command.URL, "result": command.Result})
		}
		data, _ := json.Marshal(map[string]any{"projectId": conversation.ProjectID, "conversationId": thread, "commands": views})
		options.State.Set(realtime.Update{Kind: "background", ProjectID: conversation.ProjectID, ConversationID: thread, Data: data}, len(views) > 0)
	}})
	return manager, nil
}
func (manager *Manager) begin(ctx context.Context) (context.Context, func(), error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, nil, errors.New("workspace manager is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	manager.operations.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(manager.life, cancel)
	return ctx, func() { stop(); cancel(); manager.operations.Done() }, nil
}
func unreadable(err error) error {
	if err == nil {
		return nil
	}
	var known *domainerr.Error
	if errors.As(err, &known) {
		return err
	}
	return domainerr.New("error.workspace", map[string]any{"detail": err.Error()})
}
func changedSource() error {
	return unreadable(errors.New("project source changed; open its workspace or source again"))
}
func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func secretDigests(secrets map[string]any) (map[string]any, error) {
	values := make(map[string]any, len(secrets))
	for key, value := range secrets {
		if text, ok := value.(string); ok {
			sum := sha256.Sum256([]byte(text))
			values[key] = []string{"string", hex.EncodeToString(sum[:])}
			continue
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		values[key] = []string{"json", hex.EncodeToString(sum[:])}
	}
	return values, nil
}

type sourceConfig struct {
	definition                    sources.Definition
	secrets                       map[string]any
	key                           string
	generation, hostingGeneration uint64
}

func (manager *Manager) sourceConfig(ctx context.Context, projectID string) (sourceConfig, error) {
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return sourceConfig{}, err
	}
	definition, err := sources.DecodeDefinition(project.Source)
	if err != nil {
		return sourceConfig{}, unreadable(err)
	}
	if definition == nil {
		return sourceConfig{}, domainerr.New("error.noSource", nil)
	}
	secrets := map[string]any{}
	if manager.options.Secrets != nil {
		for key, value := range manager.options.Secrets(projectID) {
			if !strings.HasPrefix(key, sources.ReferenceSecret) {
				secrets[key] = value
			}
		}
	}
	config := sourceConfig{definition: *definition, secrets: secrets, generation: manager.options.Sources.Generation(definition.Kind)}
	if definition.Hosting != nil {
		config.hostingGeneration = manager.options.Sources.Generation(definition.Hosting.Kind)
	}
	hashed, err := secretDigests(secrets)
	if err != nil {
		return sourceConfig{}, unreadable(err)
	}
	config.key, err = digest([]any{definition, hashed, config.generation, config.hostingGeneration})
	if err != nil {
		return sourceConfig{}, unreadable(err)
	}
	return config, nil
}
func (manager *Manager) acquireSource(ctx context.Context, project, key string) (leases.Lease[*connection], error) {
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return leases.Lease[*connection]{}, err
	}
	if key != "" && config.key != key {
		return leases.Lease[*connection]{}, changedSource()
	}
	lease, err := manager.connections.Acquire(ctx, project, config.key, func(ctx context.Context) (*connection, error) {
		caps, err := manager.options.Sources.ConnectSource(ctx, config.definition, config.secrets)
		if err != nil {
			return nil, err
		}
		value := &connection{caps: caps}
		current, err := manager.sourceConfig(ctx, project)
		if err != nil || current.key != config.key {
			value.Close()
			if err != nil {
				return nil, err
			}
			return nil, changedSource()
		}
		return value, nil
	})
	if err != nil {
		return leases.Lease[*connection]{}, unreadable(err)
	}
	return lease, nil
}
func withSource[T any](ctx context.Context, manager *Manager, project, key string, work func(context.Context, sdk.Capabilities) (T, error)) (T, error) {
	var zero T
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return zero, err
	}
	defer done()
	lease, err := manager.acquireSource(ctx, project, key)
	if err != nil {
		return zero, err
	}
	defer lease.Release()
	ctx, stop := lease.Context(ctx)
	defer stop()
	return work(ctx, lease.Value.caps)
}
func (manager *Manager) provides(ctx context.Context, projectID, capability string) bool {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return false
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return false
	}
	definition, err := sources.DecodeDefinition(project.Source)
	if err != nil || definition == nil {
		return false
	}
	kind := definition.Kind
	if capability != "workspace" && definition.Hosting != nil {
		kind = definition.Hosting.Kind
	}
	provider, exists := manager.options.Sources.Get(kind)
	return exists && slices.Contains(provider.Info.Provides, capability)
}
func (manager *Manager) Available(ctx context.Context, project string) bool {
	return manager.provides(ctx, project, "workspace")
}
func (manager *Manager) HasRepo(ctx context.Context, project string) bool {
	return manager.provides(ctx, project, "repo")
}
func (manager *Manager) HasTracker(ctx context.Context, project string) bool {
	return manager.provides(ctx, project, "tracker")
}
func (manager *Manager) IsRemote(ctx context.Context, projectID string) bool {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return false
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return false
	}
	definition, err := sources.DecodeDefinition(project.Source)
	if err != nil || definition == nil {
		return false
	}
	provider, exists := manager.options.Sources.Get(definition.Kind)
	return exists && provider.Info.Remote
}
func (manager *Manager) Env(project string) map[string]string {
	_, done, err := manager.begin(context.Background())
	if err != nil {
		return map[string]string{}
	}
	defer done()
	if manager.options.Env == nil {
		return map[string]string{}
	}
	return maps.Clone(manager.options.Env(project))
}
func (manager *Manager) Shell(ctx context.Context, projectID string) (process.Shell, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return process.Shell{}, err
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return process.Shell{}, err
	}
	choice := project.Settings.Workspace.Shell
	if choice == "auto" {
		if manager.IsRemote(ctx, projectID) {
			return process.Shell{Name: "bash"}, nil
		}
		shell := process.DefaultShell()
		return process.Shell{Name: shell.Name}, nil
	}
	argv := map[string][]string{"bash": {"bash", "-c"}, "zsh": {"zsh", "-c"}, "sh": {"sh", "-c"}, "pwsh": {"pwsh", "-NoProfile", "-NonInteractive", "-Command"}, "powershell": {"powershell", "-NoProfile", "-NonInteractive", "-Command"}, "cmd": {"cmd", "/d", "/s", "/c"}}[choice]
	return process.Shell{Name: choice, Argv: append([]string(nil), argv...)}, nil
}
func (manager *Manager) Viewable(ctx context.Context, project string, wait time.Duration) bool {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return false
	}
	defer done()
	if !manager.Available(ctx, project) {
		return false
	}
	if wait <= 0 {
		wait = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	value, err := withSource(ctx, manager, project, "", func(_ context.Context, caps sdk.Capabilities) (bool, error) {
		return caps.Workspace != nil && caps.Workspace.View != nil, nil
	})
	return err != nil || value
}
func (manager *Manager) Describe(ctx context.Context, projectID, locale string) (*string, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	definition, err := sources.DecodeDefinition(project.Source)
	if err != nil || definition == nil {
		return nil, err
	}
	parts := []sources.Part{}
	if definition.Hosting != nil {
		parts = append(parts, *definition.Hosting)
	}
	parts = append(parts, definition.Part)
	lines := []string{}
	for _, part := range parts {
		lines = append(lines, manager.describePart(part, locale))
	}
	value := strings.Join(lines, " · ")
	return &value, nil
}
func (manager *Manager) describePart(part sources.Part, locale string) string {
	provider, exists := manager.options.Sources.Get(part.Kind)
	title := part.Kind
	if exists {
		title = provider.Info.Title[locale]
		if title == "" {
			title = provider.Info.Title["zh-CN"]
		}
	}
	summary := manager.options.Sources.Summary(part)
	if summary != nil && *summary != "" {
		return title + " " + *summary
	}
	return title
}
func (manager *Manager) Close(ctx context.Context) error {
	manager.mu.Lock()
	if !manager.closed {
		manager.closed = true
		manager.cancel()
		go func() {
			mentionsErr := manager.mentions.Close(context.Background())
			backgroundErr := manager.Background.Close(context.Background())
			manager.operations.Wait()
			workspacesErr := manager.workspaces.Close(context.Background())
			referencesErr := manager.references.Close(context.Background())
			connectionsErr := manager.connections.Close(context.Background())
			manager.notes.Close()
			manager.mu.Lock()
			manager.closeErr = errors.Join(mentionsErr, backgroundErr, referencesErr, workspacesErr, connectionsErr)
			manager.options = Options{}
			clear(manager.discarding)
			manager.mu.Unlock()
			close(manager.done)
		}()
	}
	manager.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-manager.done:
		manager.mu.Lock()
		err := manager.closeErr
		manager.mu.Unlock()
		return err
	}
}
func (manager *Manager) InvalidateProject(ctx context.Context, project string) error {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	manager.mentions.RemovePrefix(project + "\x00")
	defer func() { manager.mu.Lock(); delete(manager.discarding, project); manager.mu.Unlock() }()
	threads, err := manager.options.Repository.ListThreads(ctx, project)
	if err != nil {
		// A successfully deleted project has no threads to enumerate. The cache
		// identity still lets invalidation cancel every retained workspace.
		manager.workspaces.RemovePrefix(project + "\x00")
	} else {
		for _, thread := range threads {
			unlock, err := manager.gates.lock(ctx, workspaceID(project, thread.ID))
			if err != nil {
				return err
			}
			manager.noteSetupGone(thread.ID)
			stopErr := manager.Background.StopThread(ctx, thread.ID, false)
			closeErr := manager.workspaces.RemoveWait(ctx, workspaceID(project, thread.ID))
			manager.notes.Remove(thread.ID)
			unlock()
			if err := errors.Join(stopErr, closeErr); err != nil {
				return err
			}
		}
	}
	manager.connections.Remove(project)
	manager.references.RemovePrefix(project + "\x00")
	manager.mu.Lock()
	delete(manager.discarding, project)
	manager.mu.Unlock()
	return nil
}

type projectReference struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Settings map[string]any `json:"settings"`
}

func (manager *Manager) InvalidateReference(ctx context.Context, project, reference string) error {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return manager.references.RemoveWait(ctx, project+"\x00"+reference)
}

func references(raw any) []projectReference {
	data, _ := json.Marshal(raw)
	var values []projectReference
	json.Unmarshal(data, &values)
	return values
}
func referenceDetail(err error) string {
	var known *domainerr.Error
	if errors.As(err, &known) {
		for _, key := range []string{"detail", "what"} {
			if value := known.Params[key]; value != nil {
				return fmt.Sprint(value)
			}
		}
	}
	return err.Error()
}
