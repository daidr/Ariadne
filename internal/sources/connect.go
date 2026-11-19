package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const HostingSecret = "hosting."
const ReferenceSecret = "reference."
const LegacySourceID = "primary"

func ReferenceSecretPrefix(id string) string { return ReferenceSecret + id + "." }

func FileSourceSecretPrefix(id string) string {
	if id == "" || id == LegacySourceID {
		return ""
	}
	return ReferenceSecretPrefix(id)
}

func FileSourceSecrets(all map[string]any, id string) map[string]any {
	result := map[string]any{}
	prefix := FileSourceSecretPrefix(id)
	for key, value := range all {
		if prefix == "" {
			if !strings.HasPrefix(key, ReferenceSecret) {
				result[key] = value
			}
		} else if name, ok := strings.CutPrefix(key, prefix); ok {
			result[name] = value
		}
	}
	return result
}

// Reference secrets retain their own namespace. Borrow only from the actual
// primary, which may have moved away from the historical unprefixed source.
func ReferenceSecretInputs(all map[string]any, primaryID string) map[string]any {
	values := FileSourceSecrets(all, primaryID)
	for key, value := range all {
		if strings.HasPrefix(key, ReferenceSecret) {
			values[key] = value
		}
	}
	return values
}

type Part struct {
	Kind     string         `json:"kind"`
	Settings map[string]any `json:"settings"`
}
type Definition struct {
	Part
	Hosting *Part `json:"hosting,omitempty"`
}
type Reference struct {
	Part
	ID string `json:"id,omitempty"`
}

func DecodeDefinition(raw any) (*Definition, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var definition Definition
	if err := json.Unmarshal(data, &definition); err != nil {
		return nil, err
	}
	return &definition, nil
}
func workspaceError(err error) error {
	var known *domainerr.Error
	if errors.As(err, &known) {
		return err
	}
	return domainerr.New("error.workspace", map[string]any{"detail": err.Error()})
}
func merged(settings, secrets map[string]any) map[string]any {
	result := maps.Clone(settings)
	if result == nil {
		result = map[string]any{}
	}
	maps.Copy(result, secrets)
	return result
}
func connect(ctx context.Context, provider sdk.ContextProvider, settings map[string]any) (caps sdk.Capabilities, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("source provider failed")
		}
	}()
	if err := ctx.Err(); err != nil {
		return sdk.Capabilities{}, err
	}
	caps, err = provider.Connect(ctx, settings)
	if caps.Close != nil {
		close := caps.Close
		var once sync.Once
		var closeErr error
		caps.Close = func() error { once.Do(func() { closeErr = close() }); return closeErr }
	}
	if err != nil && caps.Close != nil {
		_ = caps.Close()
	}
	if err == nil && ctx.Err() != nil {
		if caps.Close != nil {
			_ = caps.Close()
		}
		return sdk.Capabilities{}, ctx.Err()
	}
	return caps, err
}
func (registry *Registry) ConnectSource(ctx context.Context, source Definition, secrets map[string]any) (sdk.Capabilities, error) {
	provider, exists := registry.Get(source.Kind)
	if !exists {
		return sdk.Capabilities{}, domainerr.NotFound(source.Kind)
	}
	var hostingProvider sdk.ContextProvider
	if source.Hosting != nil {
		var exists bool
		hostingProvider, exists = registry.Get(source.Hosting.Kind)
		if !exists || !hostingProvider.Info.Hosting {
			return sdk.Capabilities{}, domainerr.NotFound(source.Hosting.Kind)
		}
		if !provider.Info.Remote {
			return sdk.Capabilities{}, workspaceError(errors.New("only a remote source takes a hosting"))
		}
	}
	own, hostingSecrets := map[string]any{}, map[string]any{}
	for key, value := range secrets {
		if strings.HasPrefix(key, ReferenceSecret) {
			continue
		}
		if strings.HasPrefix(key, HostingSecret) {
			hostingSecrets[strings.TrimPrefix(key, HostingSecret)] = value
		} else {
			own[key] = value
		}
	}
	caps, err := connect(ctx, provider, merged(source.Settings, own))
	if err != nil {
		return sdk.Capabilities{}, workspaceError(err)
	}
	closeSource := func() {
		if caps.Close != nil {
			_ = caps.Close()
		}
	}
	if caps.Workspace != nil && caps.Workspace.ViewOnly {
		closeSource()
		return sdk.Capabilities{}, workspaceError(errors.New("a single file can only be a reference source; threads work in a directory"))
	}
	if source.Hosting == nil {
		return caps, nil
	}
	hosting, err := connect(ctx, hostingProvider, merged(source.Hosting.Settings, hostingSecrets))
	if err != nil {
		closeSource()
		return sdk.Capabilities{}, workspaceError(err)
	}
	combined, err := WithHosting(ctx, caps, hosting)
	if err != nil {
		closeSource()
		if hosting.Close != nil {
			_ = hosting.Close()
		}
		return sdk.Capabilities{}, workspaceError(err)
	}
	return combined, nil
}

type ReferenceConnection struct {
	View  func(context.Context) (sdk.SourceView, error)
	Close func() error
}

func (registry *Registry) ConnectReference(ctx context.Context, reference Part, secrets map[string]any) (ReferenceConnection, error) {
	provider, exists := registry.Get(reference.Kind)
	if !exists {
		return ReferenceConnection{}, domainerr.NotFound(reference.Kind)
	}
	caps, err := connect(ctx, provider, merged(reference.Settings, secrets))
	if err != nil {
		return ReferenceConnection{}, workspaceError(err)
	}
	if caps.Workspace == nil || caps.Workspace.View == nil {
		if caps.Close != nil {
			_ = caps.Close()
		}
		return ReferenceConnection{}, workspaceError(errors.New("this kind of source cannot be read without working in it"))
	}
	close := caps.Close
	if close == nil {
		close = func() error { return nil }
	}
	return ReferenceConnection{View: func(ctx context.Context) (sdk.SourceView, error) {
		view, err := caps.Workspace.View(ctx)
		if err != nil {
			return nil, workspaceError(err)
		}
		if view == nil {
			return nil, workspaceError(errors.New("source returned no view"))
		}
		return sdk.ReadOnlyView(view), nil
	}, Close: close}, nil
}
func sameDestination(provider sdk.ContextProvider, left, right map[string]any) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	if provider.SecretsFor == nil {
		return true
	}
	l, err := provider.SecretsFor(left)
	if err != nil {
		return false
	}
	r, err := provider.SecretsFor(right)
	return err == nil && l == r
}
func (registry *Registry) ReferenceSecrets(source *Definition, reference Reference, secrets map[string]any) map[string]any {
	picked := map[string]any{}
	take := func(prefix string, strict bool) {
		for key, value := range secrets {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			name := strings.TrimPrefix(key, prefix)
			if strict || !strings.ContainsRune(name, '.') {
				picked[name] = value
			}
		}
	}
	lends := func(other *Part) bool {
		if other == nil || other.Kind != reference.Kind {
			return false
		}
		provider, _ := registry.Get(reference.Kind)
		return sameDestination(provider, reference.Settings, other.Settings)
	}
	if source != nil {
		if lends(source.Hosting) {
			take(HostingSecret, true)
		}
		if lends(&source.Part) {
			take("", false)
		}
	}
	if reference.ID != "" {
		take(ReferenceSecretPrefix(reference.ID), true)
	}
	return picked
}

var summaryField = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

func summaryValue(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i] = summaryValue(item)
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	default:
		return fmt.Sprint(value)
	}
}
func (registry *Registry) Summary(part Part) *string {
	provider, _ := registry.Get(part.Kind)
	if template := provider.Info.Summary; template != nil && *template != "" {
		value := summaryField.ReplaceAllStringFunc(*template, func(match string) string { return summaryValue(part.Settings[match[1:len(match)-1]]) })
		return new(textutil.Trim(value))
	}
	// The form follows the provider's field order. Go JSON maps do not retain
	// insertion order, so prefer that same order for a source's fallback label.
	keys := append([]string(nil), provider.Info.Settings.FieldOrder...)
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
	}
	other := []string{}
	for key := range part.Settings {
		if !seen[key] {
			other = append(other, key)
		}
	}
	slices.Sort(other)
	keys = append(keys, other...)
	for _, key := range keys {
		if value, ok := part.Settings[key].(string); ok && value != "" {
			return &value
		}
	}
	return nil
}
