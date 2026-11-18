package plugins

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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

func (bridge *luaHTTP) request(values luaValues, input *lua.LTable, callback *lua.LFunction) (lua.LValue, error) {
	bridge.calls++
	if bridge.calls > 16 {
		return nil, errors.New("plugin exceeded HTTP request limit")
	}
	var params struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := values.decode(input, &params); err != nil {
		return nil, err
	}
	address, err := url.Parse(params.URL)
	if err != nil || !bridge.origins[httpOrigin(address)] {
		return nil, errors.New("HTTP origin is not declared by the plugin")
	}
	if len(params.URL) > 8192 || len(params.Headers) > 64 {
		return nil, errors.New("plugin HTTP request is too large")
	}
	if params.Method == "" {
		params.Method = http.MethodGet
	}
	switch params.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, errors.New("unsupported plugin HTTP method")
	}
	state := values.state
	req, err := http.NewRequestWithContext(state.Context(), params.Method, params.URL, strings.NewReader(params.Body))
	if err != nil {
		return nil, errors.New("invalid plugin HTTP request")
	}
	headerBytes := 0
	for key, value := range params.Headers {
		headerBytes += len(key) + len(value)
		req.Header.Set(key, value)
	}
	bridge.bytes += len(params.Body)
	if headerBytes > 32<<10 || bridge.bytes > 2*maxLuaValue {
		return nil, errors.New("plugin exceeded HTTP data limit")
	}
	res, err := bridge.client.Do(req)
	if err != nil {
		if state.Context().Err() != nil {
			return nil, state.Context().Err()
		}
		return nil, errors.New("plugin HTTP request failed")
	}
	defer res.Body.Close()
	response := state.CreateTable(0, 3)
	response.RawSetString("status", lua.LNumber(res.StatusCode))
	headers := state.CreateTable(0, len(res.Header))
	for key, vals := range res.Header {
		headers.RawSetString(strings.ToLower(key), lua.LString(strings.Join(vals, ", ")))
	}
	response.RawSetString("headers", headers)
	var body strings.Builder
	received := 0
	buffer := make([]byte, 16<<10)
	for {
		n, readErr := res.Body.Read(buffer)
		received += n
		bridge.bytes += n
		if received > maxLuaValue || bridge.bytes > 2*maxLuaValue {
			return nil, errors.New("plugin exceeded HTTP response limit")
		}
		if n > 0 && callback == nil {
			body.Write(buffer[:n])
		} else if n > 0 {
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
	if callback == nil {
		response.RawSetString("body", lua.LString(body.String()))
	}
	return response, nil
}
