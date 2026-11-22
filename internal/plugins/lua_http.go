package plugins

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
	lua "github.com/yuin/gopher-lua"
)

func httpOrigin(address *url.URL) string {
	if address == nil || address.User != nil || address.Hostname() == "" || address.Opaque != "" ||
		(address.Scheme != "http" && address.Scheme != "https") {
		return ""
	}
	port := address.Port()
	if port == "" {
		port = "443"
		if address.Scheme == "http" {
			port = "80"
		}
	}
	return address.Scheme + "://" + strings.ToLower(address.Hostname()) + ":" + port
}

func parseNetworkOrigin(raw string) (string, bool) {
	trimmed := textutil.Trim(raw)
	if trimmed == "" || len(trimmed) > 2048 || strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", false
	}
	address, err := url.Parse(trimmed)
	if err != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return "", false
	}
	origin := httpOrigin(address)
	return origin, origin != ""
}

func validateNetworkOriginSettings(schema sdk.SettingsSchema, values map[string]any) error {
	for key, field := range schema.Properties {
		if !field.NetworkOrigin {
			continue
		}
		raw, exists := values[key]
		if !exists || raw == nil {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return domainerr.New("error.invalidBody", map[string]any{"reason": key})
		}
		if textutil.Trim(value) == "" {
			continue
		}
		if _, ok := parseNetworkOrigin(value); !ok {
			return domainerr.New("error.invalidBody", map[string]any{"reason": key})
		}
	}
	return nil
}

type schemaValues struct {
	schema *sdk.SettingsSchema
	values map[string]any
}

func collectNetworkOrigins(base map[string]bool, sources ...schemaValues) map[string]bool {
	merged := make(map[string]bool, len(base))
	for origin, allowed := range base {
		if allowed {
			merged[origin] = true
		}
	}
	for _, src := range sources {
		if src.schema == nil {
			continue
		}
		for key, field := range src.schema.Properties {
			if !field.NetworkOrigin || field.Type != "string" || field.Secret || field.Directory {
				continue
			}
			var raw string
			if value, ok := src.values[key].(string); ok && textutil.Trim(value) != "" {
				raw = value
			} else if def, ok := field.Default.(string); ok && textutil.Trim(def) != "" {
				raw = def
			}
			if raw == "" {
				continue
			}
			if origin, ok := parseNetworkOrigin(raw); ok {
				merged[origin] = true
			}
		}
	}
	return merged
}

func newPluginHTTP() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

type luaHTTP struct {
	client  *http.Client
	origins map[string]bool
	calls   int
	bytes   int
}

func (bridge *luaHTTP) doRequest(values luaValues, input *lua.LTable) (*http.Response, *lua.LTable, error) {
	bridge.calls++
	if bridge.calls > 16 {
		return nil, nil, errors.New("plugin exceeded HTTP request limit")
	}
	var params struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := values.decode(input, &params); err != nil {
		return nil, nil, err
	}
	address, err := url.Parse(params.URL)
	if err != nil || !bridge.origins[httpOrigin(address)] {
		return nil, nil, errors.New("HTTP origin is not declared by the plugin")
	}
	if len(params.URL) > 8192 || len(params.Headers) > 64 {
		return nil, nil, errors.New("plugin HTTP request is too large")
	}
	if params.Method == "" {
		params.Method = http.MethodGet
	}
	switch params.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, nil, errors.New("unsupported plugin HTTP method")
	}
	state := values.state
	req, err := http.NewRequestWithContext(state.Context(), params.Method, params.URL, strings.NewReader(params.Body))
	if err != nil {
		return nil, nil, errors.New("invalid plugin HTTP request")
	}
	headerBytes := 0
	for key, value := range params.Headers {
		headerBytes += len(key) + len(value)
		req.Header.Set(key, value)
	}
	bridge.bytes += len(params.Body)
	if headerBytes > 32<<10 || bridge.bytes > 2*maxLuaValue {
		return nil, nil, errors.New("plugin exceeded HTTP data limit")
	}
	res, err := bridge.client.Do(req)
	if err != nil {
		if state.Context().Err() != nil {
			return nil, nil, state.Context().Err()
		}
		return nil, nil, errors.New("plugin HTTP request failed")
	}
	response := state.CreateTable(0, 3)
	response.RawSetString("status", lua.LNumber(res.StatusCode))
	headers := state.CreateTable(0, len(res.Header))
	for key, vals := range res.Header {
		headers.RawSetString(strings.ToLower(key), lua.LString(strings.Join(vals, ", ")))
	}
	response.RawSetString("headers", headers)
	return res, response, nil
}

func (bridge *luaHTTP) readBody(body io.Reader) (string, error) {
	var text strings.Builder
	received := 0
	buffer := make([]byte, 16<<10)
	for {
		n, readErr := body.Read(buffer)
		received += n
		bridge.bytes += n
		if received > maxLuaValue || bridge.bytes > 2*maxLuaValue {
			return "", errors.New("plugin exceeded HTTP response limit")
		}
		if n > 0 {
			text.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			return text.String(), nil
		}
		if readErr != nil {
			return "", errors.New("plugin HTTP response ended prematurely")
		}
	}
}

func (bridge *luaHTTP) request(values luaValues, input *lua.LTable, callback *lua.LFunction) (lua.LValue, error) {
	res, response, err := bridge.doRequest(values, input)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if callback == nil {
		text, err := bridge.readBody(res.Body)
		if err != nil {
			return nil, err
		}
		response.RawSetString("body", lua.LString(text))
		return response, nil
	}
	state := values.state
	received := 0
	buffer := make([]byte, 16<<10)
	for {
		n, readErr := res.Body.Read(buffer)
		received += n
		bridge.bytes += n
		if received > maxLuaValue || bridge.bytes > 2*maxLuaValue {
			return nil, errors.New("plugin exceeded HTTP response limit")
		}
		if n > 0 {
			if err := state.CallByParam(lua.P{Fn: callback, Protect: true}, lua.LString(string(buffer[:n])), lua.LNumber(res.StatusCode)); err != nil {
				return nil, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, errors.New("plugin HTTP response ended prematurely")
		}
	}
	return response, nil
}

type boundedLuaReader struct {
	reader   io.Reader
	bridge   *luaHTTP
	received int
	limitErr error
}

func (r *boundedLuaReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.received += n
	r.bridge.bytes += n
	if r.received > maxLuaValue || r.bridge.bytes > 2*maxLuaValue {
		r.limitErr = errors.New("plugin exceeded HTTP response limit")
		return 0, r.limitErr
	}
	return n, err
}

func (bridge *luaHTTP) sse(values luaValues, input *lua.LTable, onEvent *lua.LFunction) (lua.LValue, error) {
	res, response, err := bridge.doRequest(values, input)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		text, err := bridge.readBody(res.Body)
		if err != nil {
			return nil, err
		}
		response.RawSetString("body", lua.LString(text))
		return response, nil
	}
	state := values.state
	bounded := &boundedLuaReader{reader: res.Body, bridge: bridge}
	var callbackErr error
	readErr := modelhttp.ReadSSELimit(bounded, maxLuaValue, func(event modelhttp.Event) bool {
		if event.Comment {
			return true
		}
		table := state.CreateTable(0, 2)
		table.RawSetString("event", lua.LString(event.Name))
		table.RawSetString("data", lua.LString(event.Data))
		if err := state.CallByParam(lua.P{Fn: onEvent, NRet: 1, Protect: true}, table, lua.LNumber(res.StatusCode)); err != nil {
			callbackErr = err
			return false
		}
		keep := state.Get(-1)
		state.Pop(1)
		return keep != lua.LFalse
	})
	if callbackErr != nil {
		return nil, callbackErr
	}
	if bounded.limitErr != nil {
		return nil, bounded.limitErr
	}
	if readErr != nil {
		if state.Context().Err() != nil {
			return nil, state.Context().Err()
		}
		if errors.Is(readErr, modelhttp.ErrSSELineLimit) || errors.Is(readErr, modelhttp.ErrSSEEventLimit) {
			return nil, errors.New("plugin exceeded HTTP response limit")
		}
		return nil, errors.New("plugin HTTP response ended prematurely")
	}
	return response, nil
}
