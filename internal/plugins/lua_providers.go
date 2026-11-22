package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/sdk"
	lua "github.com/yuin/gopher-lua"
)

func (program *luaProgram) install(pc sdk.PluginContext, provider luaProvider) error {
	switch provider.group {
	case "models":
		_, err := pc.RegisterModel(program.model(provider))
		return err
	case "contexts":
		_, err := pc.RegisterContext(program.source(provider))
		return err
	case "dashboards":
		_, err := pc.RegisterDashboard(sdk.DashboardSource{
			Info: sdk.DashboardSourceInfo{Kind: provider.kind, Title: provider.title, Description: provider.description, Settings: provider.settings},
			Fetch: func(ctx context.Context, settings map[string]any, project string) (map[string]any, error) {
				var out map[string]any
				err := program.result(ctx, provider, "fetch", settings, &out, settings, project)
				return out, err
			},
		})
		return err
	case "searches":
		_, err := pc.RegisterSearch(program.search(provider))
		return err
	case "links":
		_, err := pc.RegisterLink(sdk.LinkProvider{
			Kind: provider.kind, Title: provider.title,
			Match: func(address *url.URL) *sdk.LinkMatch {
				var label *string
				if err := program.result(context.Background(), provider, "match", nil, &label, address.String()); err != nil || label == nil || *label == "" || len(*label) > 1024 {
					return nil
				}
				return &sdk.LinkMatch{Kind: provider.kind, Label: *label}
			},
			Preview: func(ctx context.Context, address *url.URL) (*sdk.PullRequestPreview, error) {
				var preview *sdk.PullRequestPreview
				err := program.result(ctx, provider, "preview", nil, &preview, address.String())
				if preview != nil {
					preview.Kind = provider.kind
				}
				return preview, err
			},
		})
		return err
	}
	return errors.New("unknown Lua plugin provider type")
}

func (program *luaProgram) search(provider luaProvider) sdk.SearchProvider {
	run := func(ctx context.Context, settings map[string]any, query string, limit int) (sdk.SearchResponse, error) {
		limit = min(max(limit, 0), 100)
		var raw json.RawMessage
		var err error
		if settings != nil {
			err = program.result(ctx, provider, "search", settings, &raw, query, limit, settings)
		} else {
			err = program.result(ctx, provider, "search", nil, &raw, query, limit)
		}
		if err != nil {
			return sdk.SearchResponse{}, err
		}
		return decodeLuaSearchResponse(raw, limit)
	}
	searchProvider := sdk.SearchProvider{
		Kind:        provider.kind,
		Title:       provider.title,
		Description: provider.localizedDesc,
		Settings:    provider.settings,
		Execute: func(ctx context.Context, settings map[string]any, query sdk.SearchQuery) (sdk.SearchResponse, error) {
			return run(ctx, settings, query.Query, query.Limit)
		},
		Search: func(ctx context.Context, query string, limit int) ([]sdk.SearchResult, error) {
			response, err := run(ctx, nil, query, limit)
			if err != nil {
				return nil, err
			}
			return response.Results, nil
		},
	}
	if provider.hasNetworkOrigin {
		searchProvider.ValidateSettings = func(values map[string]any) error {
			return validateNetworkOriginSettings(provider.settings, values)
		}
	}
	if provider.methods["check"] {
		searchProvider.Capabilities.SupportsCheck = true
		searchProvider.Check = func(ctx context.Context, settings map[string]any) (sdk.SearchCheckResult, error) {
			var raw json.RawMessage
			if err := program.result(ctx, provider, "check", settings, &raw, settings); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return sdk.SearchCheckResult{}, err
				}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
					return sdk.SearchCheckResult{OK: false}, nil
				}
				return sdk.SearchCheckResult{
					OK:      false,
					Message: sdk.LocalizedText{"zh-CN": err.Error(), "en": err.Error()},
				}, nil
			}
			return decodeLuaSearchCheck(raw), nil
		}
	}
	return searchProvider
}

func decodeLuaSearchResponse(raw json.RawMessage, limit int) (sdk.SearchResponse, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return sdk.SearchResponse{Results: []sdk.SearchResult{}}, nil
	}
	if trimmed[0] == '[' {
		var out []sdk.SearchResult
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return sdk.SearchResponse{}, err
		}
		if len(out) > limit {
			out = slices.Clone(out[:limit])
		}
		if out == nil {
			out = []sdk.SearchResult{}
		}
		return sdk.SearchResponse{Results: out}, nil
	}
	if trimmed[0] == '{' {
		var out sdk.SearchResponse
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return sdk.SearchResponse{}, err
		}
		if len(out.Results) > limit {
			out.Results = slices.Clone(out.Results[:limit])
		}
		if out.Results == nil {
			out.Results = []sdk.SearchResult{}
		}
		return out, nil
	}
	return sdk.SearchResponse{}, errors.New("invalid Lua search response")
}

func decodeLuaSearchCheck(raw json.RawMessage) sdk.SearchCheckResult {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("true")) {
		return sdk.SearchCheckResult{OK: true}
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return sdk.SearchCheckResult{OK: false}
	}
	if trimmed[0] == '{' {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &probe); err != nil {
			return sdk.SearchCheckResult{OK: false}
		}
		if _, hasOK := probe["ok"]; hasOK {
			var result sdk.SearchCheckResult
			if err := json.Unmarshal(trimmed, &result); err != nil {
				return sdk.SearchCheckResult{OK: false}
			}
			return result
		}
		var message sdk.LocalizedText
		if err := json.Unmarshal(trimmed, &message); err == nil && len(message) > 0 {
			return sdk.SearchCheckResult{OK: false, Message: message}
		}
	}
	return sdk.SearchCheckResult{OK: false}
}

func (program *luaProgram) model(provider luaProvider) sdk.ModelProvider {
	model := sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:         provider.kind,
			Title:        provider.title,
			Settings:     provider.settings,
			Capabilities: provider.capabilities,
		},
		Stream: func(ctx context.Context, settings map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				program.stream(ctx, provider, settings, request, yield)
			}
		},
	}
	if provider.hasNetworkOrigin {
		model.ValidateSettings = func(values map[string]any) error {
			return validateNetworkOriginSettings(provider.settings, values)
		}
	}
	if provider.methods["models"] {
		model.Models = func(ctx context.Context, settings map[string]any, options sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			var out []sdk.ListedModel
			err := program.result(ctx, provider, "models", settings, &out, settings, options.Headers)
			return out, err
		}
	}
	if provider.methods["check"] {
		model.Check = func(ctx context.Context, settings map[string]any) (sdk.LocalizedText, error) {
			var out sdk.LocalizedText
			err := program.result(ctx, provider, "check", settings, &out, settings)
			return out, err
		}
	}
	if provider.methods["describe"] {
		model.Describe = func(settings map[string]any, request sdk.Request) any {
			var out any
			if err := program.result(context.Background(), provider, "describe", settings, &out, settings, request); err != nil {
				return nil
			}
			return out
		}
	}
	if provider.methods["search"] {
		model.Search = func(ctx context.Context, settings map[string]any, request sdk.SearchRequest) (sdk.SearchAnswer, error) {
			var out sdk.SearchAnswer
			err := program.result(ctx, provider, "search", settings, &out, settings, request)
			return out, err
		}
	}
	return model
}

func (program *luaProgram) stream(ctx context.Context, provider luaProvider, settings map[string]any, request sdk.Request, yield func(sdk.Part) bool) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	emitted, bytes := 0, 0
	stopped := false
	var finish *sdk.Part
	var streamError error
	err := program.call(ctx, provider, "stream", 10*time.Minute, true, settings, func(values luaValues, fn *lua.LFunction) error {
		input, err := luaArgs(values, []any{settings, request})
		if err != nil {
			return err
		}
		state := values.state
		emit := state.NewFunction(func(state *lua.LState) int {
			fail := func(err error) {
				streamError = err
				state.RaiseError("%s", err)
			}
			if streamError != nil {
				fail(streamError)
			}
			if stopped || state.Context().Err() != nil {
				state.RaiseError("stream was cancelled")
			}
			data, err := values.encode(state.CheckTable(1))
			emitted++
			bytes += len(data)
			if err != nil || emitted > 65536 || bytes > 32<<20 {
				fail(errors.New("invalid or oversized Lua stream output"))
			}
			var part sdk.Part
			if err := unmarshalLuaPart(data, &part); err != nil {
				fail(err)
			}
			if finish != nil {
				fail(errors.New("plugin emitted data after finish"))
			}
			if part.Type == "finish" {
				finish = &part
			} else if !yield(part) {
				stopped = true
				stop()
				state.RaiseError("stream was cancelled")
			}
			return 0
		})
		err = state.CallByParam(lua.P{Fn: fn, Protect: true}, append(input, emit)...)
		return errors.Join(streamError, err)
	})
	if stopped || ctx.Err() != nil {
		return
	}
	if err == nil && finish != nil {
		yield(*finish)
		return
	}
	if err == nil {
		err = errors.New("Lua model plugin did not emit a finish part")
	}
	busy := errors.Is(err, errLuaBusy)
	yield(sdk.Part{Type: "finish", Reason: "error", Error: err.Error(), Retryable: busy, Busy: busy})
}

func unmarshalLuaPart(data []byte, part *sdk.Part) error {
	if err := json.Unmarshal(data, part); err != nil {
		return err
	}
	switch part.Type {
	case "text", "reasoning", "usage":
		return nil
	case "tool-input":
		if part.Call != nil && *part.Call >= 0 {
			return nil
		}
	case "tool-call":
		if part.ID != "" && part.Name != "" {
			return nil
		}
	case "finish":
		if part.Reason != "" {
			return nil
		}
	}
	return errors.New("invalid Lua model stream part")
}

func (program *luaProgram) source(provider luaProvider) sdk.ContextProvider {
	source := sdk.ContextProvider{
		NeedsPluginSettings: provider.needsPluginSettings,
		Info:                sdk.ContextProviderInfo{Kind: provider.kind, Title: provider.title, Settings: provider.settings, Provides: []string{"workspace"}, Remote: true},
		Connect: func(ctx context.Context, settings map[string]any) (sdk.Capabilities, error) {
			if provider.methods["check"] {
				var result any
				if err := program.result(ctx, provider, "check", settings, &result, settings); err != nil {
					return sdk.Capabilities{}, err
				}
			}
			view := &luaSource{program: program, provider: provider, settings: settings}
			return sdk.Capabilities{
				Workspace: &sdk.WorkspaceCapability{ViewOnly: true, View: func(context.Context) (sdk.SourceView, error) { return view, nil }},
				Close: func() error {
					view.mu.Lock()
					view.settings = nil
					view.closed = true
					view.mu.Unlock()
					return nil
				},
			}, nil
		},
	}
	if provider.methods["browse"] {
		source.Browse = func(ctx context.Context, settings map[string]any, folder string) (*sdk.DirectoryListing, error) {
			var out *sdk.DirectoryListing
			err := program.result(ctx, provider, "browse", settings, &out, settings, folder)
			return out, err
		}
	}
	return source
}

type luaSource struct {
	program  *luaProgram
	provider luaProvider
	mu       sync.RWMutex
	settings map[string]any
	closed   bool
}

func (view *luaSource) input(path string) (map[string]any, string, error) {
	path, err := sdk.ViewPath(path)
	if err != nil {
		return nil, "", err
	}
	view.mu.RLock()
	defer view.mu.RUnlock()
	if view.closed {
		return nil, "", errors.New("plugin source was closed")
	}
	return view.settings, path, nil
}

func (view *luaSource) List(ctx context.Context, path string) ([]sdk.DirectoryEntry, error) {
	settings, path, err := view.input(path)
	if err != nil {
		return nil, err
	}
	var out []sdk.DirectoryEntry
	if err := view.program.result(ctx, view.provider, "list", settings, &out, settings, path); err != nil {
		return nil, err
	}
	if len(out) > 20000 {
		return nil, errors.New("plugin source directory has too many entries")
	}
	for _, entry := range out {
		if entry.Name == "" || entry.Name == "." || entry.Name == ".." || len(entry.Name) > 1024 ||
			(entry.Kind != "file" && entry.Kind != "directory") || strings.ContainsAny(entry.Name, "/\\") {
			return nil, errors.New("plugin source returned an invalid directory entry")
		}
		if _, err := sdk.ViewPath(entry.Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (view *luaSource) ReadBytes(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	settings, path, err := view.input(path)
	if err != nil {
		return nil, err
	}
	var out []byte
	limit := int64(maxLuaValue)
	if maxBytes > 0 {
		limit = min(limit, maxBytes)
	}
	err = view.program.call(ctx, view.provider, "read", 30*time.Second, true, settings, func(values luaValues, fn *lua.LFunction) error {
		input, err := luaArgs(values, []any{settings, path, limit})
		if err != nil {
			return err
		}
		state := values.state
		if err := state.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, input...); err != nil {
			return err
		}
		value, ok := state.Get(-1).(lua.LString)
		if !ok || int64(len(value)) > limit {
			return errors.New("plugin source must return a byte string within the read limit")
		}
		out = []byte(value)
		return nil
	})
	return out, err
}
