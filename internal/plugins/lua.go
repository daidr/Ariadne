package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
	lua "github.com/yuin/gopher-lua"
	"github.com/yuin/gopher-lua/parse"
)

var luaCalls = make(chan struct{}, 32)
var errLuaBusy = errors.New("Lua plugin has too many active calls")

type luaProvider struct {
	group, kind         string
	index               int
	methods             map[string]bool
	title               sdk.LocalizedText
	localizedDesc       sdk.LocalizedText
	settings            sdk.SettingsSchema
	capabilities        sdk.ModelProviderCapabilities
	hasNetworkOrigin    bool
	description         string
	needsPluginSettings bool
}

type luaProgram struct {
	mu             sync.Mutex
	active         sync.WaitGroup
	proto          *lua.FunctionProto
	life           context.Context
	client         *http.Client
	origins        map[string]bool
	slots          chan struct{}
	settingsSchema *sdk.SettingsSchema
	settings       func() map[string]any
}

func newLuaState(ctx context.Context, bridge *luaHTTP) luaValues {
	state := lua.NewState(lua.Options{
		SkipOpenLibs: true, RegistrySize: 128, RegistryMaxSize: 8192,
		RegistryGrowStep: 128, CallStackSize: 128, MinimizeStackMemory: true,
	})
	for _, library := range []lua.LGFunction{lua.OpenBase, lua.OpenTable, lua.OpenString, lua.OpenMath} {
		state.Push(state.NewFunction(library))
		state.Call(0, 0)
	}
	for _, name := range []string{"collectgarbage", "dofile", "loadfile", "load", "loadstring", "module", "require", "print", "newproxy", "_printregs"} {
		state.SetGlobal(name, lua.LNil)
	}
	state.SetContext(ctx)
	values := luaValues{state: state, array: state.NewTable(), null: state.NewUserData()}
	values.array.RawSetString("__metatable", lua.LFalse)
	api := state.NewTable()
	api.RawSetString("null", values.null)
	state.SetFuncs(api, map[string]lua.LGFunction{
		"array": func(state *lua.LState) int {
			table := state.OptTable(1, nil)
			if table == nil {
				table = state.NewTable()
			}
			table.Metatable = values.array
			state.Push(table)
			return 1
		},
		"json_encode": func(state *lua.LState) int {
			data, err := values.encode(state.Get(1))
			if err != nil {
				state.RaiseError("%s", err)
			}
			state.Push(lua.LString(string(data)))
			return 1
		},
		"json_decode": func(state *lua.LState) int {
			text := state.CheckString(1)
			if len(text) > maxLuaValue {
				state.RaiseError("JSON value exceeds 8 MiB")
			}
			value, err := values.fromJSON([]byte(text))
			if err != nil {
				state.RaiseError("%s", err)
			}
			state.Push(value)
			return 1
		},
		"http": func(state *lua.LState) int {
			if bridge == nil || bridge.origins == nil {
				state.RaiseError("HTTP is unavailable in this plugin function")
			}
			value, err := bridge.request(values, state.CheckTable(1), state.OptFunction(2, nil))
			if err != nil {
				state.RaiseError("%s", err)
			}
			state.Push(value)
			return 1
		},
		"http_sse": func(state *lua.LState) int {
			if bridge == nil || bridge.origins == nil {
				state.RaiseError("HTTP is unavailable in this plugin function")
			}
			value, err := bridge.sse(values, state.CheckTable(1), state.CheckFunction(2))
			if err != nil {
				state.RaiseError("%s", err)
			}
			state.Push(value)
			return 1
		},
	})
	state.SetGlobal("ariadne", api)
	return values
}

func luaDefinition(values luaValues, proto *lua.FunctionProto) (*lua.LTable, error) {
	state := values.state
	if err := state.CallByParam(lua.P{Fn: state.NewFunctionFromProto(proto), NRet: 1, Protect: true}); err != nil {
		return nil, err
	}
	value, ok := state.Get(-1).(*lua.LTable)
	state.Pop(1)
	if !ok {
		return nil, errors.New("Lua plugin must return a table")
	}
	return value, nil
}

var luaGroups = []struct {
	name     string
	required []string
	optional []string
}{
	{"models", []string{"stream"}, []string{"models", "check", "describe", "search"}},
	{"contexts", []string{"list", "read"}, []string{"check", "browse"}},
	{"dashboards", []string{"fetch"}, nil},
	{"searches", []string{"search"}, []string{"check"}},
	{"links", []string{"match", "preview"}, nil},
}

func compilePlugin(ctx context.Context, name string, source []byte) (plugin sdk.Plugin, err error) {
	defer func() {
		if recover() != nil {
			plugin, err = sdk.Plugin{}, errors.New("Lua plugin could not be compiled or initialized")
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	chunk, err := parse.Parse(bytes.NewReader(source), filepath.Base(name))
	if err != nil {
		return sdk.Plugin{}, err
	}
	proto, err := lua.Compile(chunk, filepath.Base(name))
	if err != nil {
		return sdk.Plugin{}, err
	}
	values := newLuaState(ctx, nil)
	defer values.state.Close()
	definition, err := luaDefinition(values, proto)
	if err != nil {
		return sdk.Plugin{}, err
	}
	if definition.RawGetString("version") != lua.LNumber(1) {
		return sdk.Plugin{}, errors.New("Lua plugin version must be 1")
	}
	id, ok := definition.RawGetString("id").(lua.LString)
	if !ok || !pluginID.MatchString(string(id)) {
		return sdk.Plugin{}, errors.New("Lua plugin requires a valid id")
	}
	var title sdk.LocalizedText
	if err := values.decode(definition.RawGetString("name"), &title); err != nil || textutil.Trim(title["zh-CN"]) == "" {
		return sdk.Plugin{}, errors.New("Lua plugin requires a Chinese display name")
	}
	origins := map[string]bool{}
	if network := definition.RawGetString("network"); network != lua.LNil {
		var addresses []string
		if err := values.decode(network, &addresses); err != nil || len(addresses) > 16 {
			return sdk.Plugin{}, errors.New("network must be an array of at most 16 HTTP origins")
		}
		for _, raw := range addresses {
			address, err := url.Parse(raw)
			if err != nil || len(raw) > 1024 || httpOrigin(address) == "" || address.RawQuery != "" ||
				address.ForceQuery || address.Fragment != "" || (address.Path != "" && address.Path != "/") {
				return sdk.Plugin{}, errors.New("network entries must be HTTP origins without paths, queries or credentials")
			}
			origins[httpOrigin(address)] = true
		}
	}
	providers, err := luaProviders(values, definition)
	if err != nil {
		return sdk.Plugin{}, err
	}
	var schema *sdk.SettingsSchema
	if raw := definition.RawGetString("settings"); raw != lua.LNil {
		if err := values.decode(raw, &schema); err != nil || schema == nil {
			return sdk.Plugin{}, errors.New("invalid plugin settings schema")
		}
	}
	return sdk.Plugin{ID: string(id), Name: title, Settings: schema, Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
		program := &luaProgram{proto: proto, life: pc.Context, client: newPluginHTTP(), origins: origins, slots: make(chan struct{}, 8), settingsSchema: schema, settings: pc.Settings}
		if err := pc.OnDispose(program.close); err != nil {
			return nil, err
		}
		for _, provider := range providers {
			if err := program.install(pc, provider); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}}, nil
}

func luaProviders(values luaValues, definition *lua.LTable) ([]luaProvider, error) {
	var providers []luaProvider
	metadataBytes := 0
	for _, group := range luaGroups {
		raw := definition.RawGetString(group.name)
		if raw == lua.LNil {
			continue
		}
		list, ok := raw.(*lua.LTable)
		if !ok || list.Len() > 16 {
			return nil, fmt.Errorf("%s must be an array of providers", group.name)
		}
		count := 0
		list.ForEach(func(_, _ lua.LValue) { count++ })
		if count != list.Len() || len(providers)+count > 16 {
			return nil, errors.New("Lua plugin must declare at most 16 providers in consecutive arrays")
		}
		for i := 1; i <= count; i++ {
			entry, ok := list.RawGetInt(i).(*lua.LTable)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a table", group.name, i)
			}
			kind, ok := entry.RawGetString("kind").(lua.LString)
			if !ok || !pluginID.MatchString(string(kind)) {
				return nil, fmt.Errorf("%s[%d] requires a valid kind", group.name, i)
			}
			provider := luaProvider{group: group.name, kind: string(kind), index: i, methods: map[string]bool{}}
			provider.needsPluginSettings = entry.RawGetString("requiresPluginSettings") == lua.LTrue
			if err := values.decode(entry.RawGetString("title"), &provider.title); err != nil || textutil.Trim(provider.title["zh-CN"]) == "" {
				return nil, fmt.Errorf("%s requires a Chinese title", kind)
			}
			provider.settings = sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{}}
			if raw := entry.RawGetString("settings"); raw != lua.LNil {
				data, err := values.encode(raw)
				metadataBytes += len(data)
				if err != nil || json.Unmarshal(data, &provider.settings) != nil || provider.settings.Type != "object" {
					return nil, fmt.Errorf("%s has an invalid settings schema", kind)
				}
				for _, field := range provider.settings.Properties {
					if field.Secret && field.Default != nil {
						return nil, fmt.Errorf("%s cannot declare a default value for a secret field", kind)
					}
					if field.NetworkOrigin {
						if field.Type != "string" || field.Secret || field.Directory {
							return nil, fmt.Errorf("%s networkOrigin must be used on a non-secret string field", kind)
						}
						if field.Default != nil {
							def, ok := field.Default.(string)
							if !ok {
								return nil, fmt.Errorf("%s has an invalid networkOrigin default", kind)
							}
							if textutil.Trim(def) != "" {
								if _, ok := parseNetworkOrigin(def); !ok {
									return nil, fmt.Errorf("%s has an invalid networkOrigin default", kind)
								}
							}
						}
						provider.hasNetworkOrigin = true
					}
				}
			}
			if group.name == "models" {
				if raw := entry.RawGetString("capabilities"); raw != lua.LNil {
					if err := values.decode(raw, &provider.capabilities); err != nil {
						return nil, fmt.Errorf("%s has invalid capabilities", kind)
					}
					switch provider.capabilities.ReasoningScope {
					case "", sdk.ReasoningScopeModel, sdk.ReasoningScopeProvider:
					default:
						return nil, fmt.Errorf("%s has an unsupported reasoningScope", kind)
					}
				}
			}
			if text, ok := entry.RawGetString("description").(lua.LString); ok {
				provider.description = string(text)
			} else if raw := entry.RawGetString("description"); raw != lua.LNil {
				var localized sdk.LocalizedText
				if err := values.decode(raw, &localized); err != nil {
					return nil, fmt.Errorf("%s has an invalid description", kind)
				}
				provider.localizedDesc = localized
				for locale, text := range localized {
					metadataBytes += len(locale) + len(text)
				}
			}
			for locale, title := range provider.title {
				metadataBytes += len(locale) + len(title)
			}
			metadataBytes += len(provider.description)
			if metadataBytes > 128<<10 {
				return nil, errors.New("Lua plugin provider metadata exceeds 128 KiB")
			}
			for _, method := range group.required {
				if _, ok := entry.RawGetString(method).(*lua.LFunction); !ok {
					return nil, fmt.Errorf("%s.%s must be a function", kind, method)
				}
				provider.methods[method] = true
			}
			for _, method := range group.optional {
				raw := entry.RawGetString(method)
				if raw == lua.LNil {
					continue
				}
				if _, ok := raw.(*lua.LFunction); !ok {
					return nil, fmt.Errorf("%s.%s must be a function", kind, method)
				}
				provider.methods[method] = true
			}
			providers = append(providers, provider)
		}
	}
	if len(providers) == 0 {
		return nil, errors.New("Lua plugin declares no providers")
	}
	return providers, nil
}

func (program *luaProgram) close(ctx context.Context) error {
	program.mu.Lock()
	program.proto = nil
	program.origins = nil
	client := program.client
	program.client = nil
	program.mu.Unlock()
	client.CloseIdleConnections()
	done := make(chan struct{})
	go func() {
		program.active.Wait()
		client.CloseIdleConnections()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (program *luaProgram) call(ctx context.Context, provider luaProvider, method string, budget time.Duration, allowHTTP bool, callSettings map[string]any, run func(luaValues, *lua.LFunction) error) (err error) {
	select {
	case luaCalls <- struct{}{}:
		defer func() { <-luaCalls }()
	default:
		return errLuaBusy
	}
	select {
	case program.slots <- struct{}{}:
		defer func() { <-program.slots }()
	default:
		return errLuaBusy
	}
	program.mu.Lock()
	proto, client, origins := program.proto, program.client, program.origins
	if proto != nil {
		program.active.Add(1)
	}
	program.mu.Unlock()
	if proto == nil {
		return errors.New("Lua plugin has been unloaded")
	}
	defer program.active.Done()
	ctx, cancel := context.WithTimeout(ctx, budget)
	stop := context.AfterFunc(program.life, cancel)
	defer cancel()
	defer stop()
	if program.life.Err() != nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if cause := recover(); cause != nil {
			err = errors.New("Lua plugin runtime panic")
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil {
			err = errors.New(strings.Clone(textutil.HeadUnits(err.Error(), 1024)))
		}
	}()
	httpBridge := &luaHTTP{client: client}
	values := newLuaState(ctx, httpBridge)
	defer values.state.Close()
	definition, err := luaDefinition(values, proto)
	if err != nil {
		return err
	}
	if program.settings != nil {
		api := values.state.GetGlobal("ariadne").(*lua.LTable)
		api.RawSetString("settings", values.state.NewFunction(func(state *lua.LState) int {
			raw, err := json.Marshal(program.settings())
			if err != nil {
				state.RaiseError("cannot read plugin settings")
			}
			value, err := values.fromJSON(raw)
			if err != nil {
				state.RaiseError("cannot read plugin settings")
			}
			state.Push(value)
			return 1
		}))
	}
	group, ok := definition.RawGetString(provider.group).(*lua.LTable)
	if !ok {
		return errors.New("Lua plugin provider declaration changed")
	}
	entry, ok := group.RawGetInt(provider.index).(*lua.LTable)
	if !ok || entry.RawGetString("kind") != lua.LString(provider.kind) {
		return errors.New("Lua plugin provider declaration changed")
	}
	fn, ok := entry.RawGetString(method).(*lua.LFunction)
	if !ok {
		return errors.New("Lua plugin provider function changed")
	}
	if allowHTTP {
		var pluginSettings map[string]any
		if program.settings != nil {
			pluginSettings = program.settings()
		}
		httpBridge.origins = collectNetworkOrigins(
			origins,
			schemaValues{schema: program.settingsSchema, values: pluginSettings},
			schemaValues{schema: &provider.settings, values: callSettings},
		)
	}
	return run(values, fn)
}

func luaArgs(values luaValues, args []any) ([]lua.LValue, error) {
	result := make([]lua.LValue, 0, len(args))
	total := 0
	for _, item := range args {
		data, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxLuaValue {
			return nil, errors.New("plugin arguments exceed 8 MiB")
		}
		arg, err := values.fromJSON(data)
		if err != nil {
			return nil, err
		}
		result = append(result, arg)
	}
	return result, nil
}

func (program *luaProgram) result(ctx context.Context, provider luaProvider, method string, callSettings map[string]any, out any, args ...any) error {
	budget, allowHTTP := 30*time.Second, true
	if method == "match" || method == "describe" {
		budget, allowHTTP = 100*time.Millisecond, false
	}
	return program.call(ctx, provider, method, budget, allowHTTP, callSettings, func(values luaValues, fn *lua.LFunction) error {
		input, err := luaArgs(values, args)
		if err != nil {
			return err
		}
		state := values.state
		if err := state.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, input...); err != nil {
			return err
		}
		return values.decode(state.Get(-1), out)
	})
}
