package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

type apiRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Body    any               `json:"body,omitempty"`
	Raw     *string           `json:"raw,omitempty"`
	Auth    *bool             `json:"auth,omitempty"`
	Save    string            `json:"save,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func encoded(value any) string { data, _ := json.Marshal(value); return string(data) }

var randomID = regexp.MustCompile(`[a-z]{3}_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func canonical(raw any) any {
	var value any
	json.Unmarshal([]byte(encoded(raw)), &value)
	seen := map[string]string{}
	counts := map[string]int{}
	var visit func(any) any
	visit = func(value any) any {
		switch node := value.(type) {
		case map[string]any:
			keys := []string{}
			for key := range node {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				node[key] = visit(node[key])
			}
		case []any:
			for i := range node {
				node[i] = visit(node[i])
			}
		case string:
			return randomID.ReplaceAllStringFunc(node, func(id string) string {
				if name, ok := seen[id]; ok {
					return name
				}
				prefix := id[:3]
				counts[prefix]++
				name := fmt.Sprintf("%s_%d", prefix, counts[prefix])
				seen[id] = name
				return name
			})
		}
		return value
	}
	return visit(value)
}
func TestDomainHTTPResponses(t *testing.T) {
	seed := []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)},
	}
	hash := sha256.Sum256([]byte("abc"))
	sql := fmt.Sprintf(`INSERT INTO files VALUES ('fil_text','prj_a','text/plain',3,'%s',x'616263',0)`, hex.EncodeToString(hash[:]))
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "api.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	type expectation struct {
		method  string
		path    string
		auth    bool
		body    string
		headers map[string]string
		status  int
	}
	cases := []expectation{
		{method: "GET", path: "/api/health", auth: false, status: 200},
		{method: "HEAD", path: "/api/health", auth: true, status: 404},
		{method: "POST", path: "/api/health", auth: true, status: 404},
		{method: "GET", path: "/api/files/fil_text", auth: false, status: 401},
		{method: "GET", path: "/api/files/fil_missing", auth: false, status: 401},
		{method: "GET", path: "/api/events?token=test-token", auth: false, status: 401},
		{method: "GET", path: "/api/projects?token=test-token", auth: false, status: 401},
		{method: "GET", path: "/api/unknown", auth: false, status: 401},
		{method: "GET", path: "/api/unknown", auth: true, status: 404},
		{method: "GET", path: "/api/events", auth: true, status: 404},
		{method: "GET", path: "/api/projects", auth: true, status: 404},
		{method: "GET", path: "/api/files/fil_text/sheet", auth: false, status: 401},
		{method: "GET", path: "/api/files/fil_text/sheet", auth: true, status: 404},
		{method: "GET", path: "/api/files/fil_text", auth: true, status: 200},
		{method: "HEAD", path: "/api/files/fil_text", auth: true, status: 200},
		{method: "POST", path: "/api/projects/prj_a/files?for=message", auth: true, body: "abc", headers: map[string]string{"Content-Type": "text/plain"}, status: 201},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(tc.method, "http://test.invalid"+tc.path, strings.NewReader(tc.body))
		if tc.auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		for key, value := range tc.headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("%s %s (auth=%v): got %d, want %d (body=%s)", tc.method, tc.path, tc.auth, response.Code, tc.status, response.Body.String())
		}
	}
}

func TestEveryRegisteredAPIRouteAuthenticatesBeforeLookup(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "auth.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	api, err := New(Options{Repository: repo, Token: "secret", Web: fstest.MapFS{"index.html": {Data: []byte("app shell")}}})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	parameter := regexp.MustCompile(`:[a-zA-Z]+`)
		wantNativeRoutes := []string{
			"GET /api/artifacts/:artifactId/live",
			"GET /api/conversations/:conversationId/trace/export",
			"GET /api/files/:fileId",
			"GET /api/health",
			"GET /api/projects/:projectId/export",
			"GET /api/service/status",
			"HEAD /api/files/:fileId",
			"POST /api/projects/:projectId/files",
			"POST /api/service/cancel-drain",
			"POST /api/service/drain",
			"POST /api/service/shutdown",
		}
	gotNativeRoutes := slices.Clone(api.routes)
	slices.Sort(gotNativeRoutes)
	if !slices.Equal(gotNativeRoutes, wantNativeRoutes) {
		t.Fatalf("unexpected native HTTP API routes:\ngot:  %v\nwant: %v", gotNativeRoutes, wantNativeRoutes)
	}
	if len(api.rpcProcedures) == 0 {
		t.Fatal("expected ConnectRPC procedures to be registered")
	}
	t.Logf("%d native API routes and %d ConnectRPC procedures registered", len(api.routes), len(api.rpcProcedures))
	for _, route := range api.routes {
		parts := strings.SplitN(route, " ", 2)
		if route == "GET /api/health" {
			continue
		}
		request := httptest.NewRequest(parts[0], "http://test.invalid"+parameter.ReplaceAllString(parts[1], "missing"), nil)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 401 || !strings.Contains(response.Body.String(), "error.unauthorized") {
			t.Fatalf("unauthenticated %s: %d %s", route, response.Code, response.Body.String())
		}
	}
	for _, procedure := range api.rpcProcedures {
		request := httptest.NewRequest(http.MethodPost, "http://test.invalid"+rpcPrefix+procedure, nil)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 401 || !strings.Contains(response.Body.String(), "error.unauthorized") {
			t.Fatalf("unauthenticated ConnectRPC %s: %d %s", procedure, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest("GET", "http://test.invalid/any/client/path", nil))
	if response.Code != 200 || response.Body.String() != "app shell" {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, unknown := range []string{"/api/missing", rpcPrefix + "/ariadne.v1.MissingService/Unknown"} {
		response = httptest.NewRecorder()
		api.ServeHTTP(response, httptest.NewRequest("POST", "http://test.invalid"+unknown, nil))
		if response.Code != 401 {
			t.Fatalf("unauthenticated %s did not fail before lookup: %d", unknown, response.Code)
		}
	}
	// A successful bearer ConnectRPC request grants native file reads only, never API or RPC calls.
	request := httptest.NewRequest(http.MethodPost, "http://test.invalid"+rpcPrefix+ariadnev1connect.MetaServicePingProcedure, strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("authenticated Ping returned %d: %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	for _, path := range []string{"/api/events", "/api/events?token=secret", "/api/projects", "/api/projects?token=secret", "/api/artifacts/missing", "/api/settings/models", "/api/conversations/missing/trace/export"} {
		request := httptest.NewRequest("GET", "http://test.invalid"+path, nil)
		request.AddCookie(cookies[0])
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("file cookie authorized %s", path)
		}
	}
	for _, procedure := range api.rpcProcedures {
		request := httptest.NewRequest(http.MethodPost, "http://test.invalid"+rpcPrefix+procedure, nil)
		request.AddCookie(cookies[0])
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("file cookie authorized ConnectRPC %s", procedure)
		}
	}
	request = httptest.NewRequest("GET", "http://test.invalid/api/files/fil_missing", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatal(response.Code, response.Body.String())
	}
	request.Header.Set("Authorization", "Bearer incorrect")
	response = httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("invalid explicit bearer fell back to cookie")
	}
}

func TestEventStreamAuthCursorAndDisconnect(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "stream.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"A","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()

	// Legacy SSE endpoint with query token is rejected before lookup.
	resp, err := server.Client().Get(server.URL + "/api/events?token=secret&afterSeq=0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("legacy /api/events?token=secret returned %d, want 401", resp.StatusCode)
	}

	// Unauthenticated ConnectRPC stream fails with CodeUnauthenticated.
	unauthClient := ariadnev1connect.NewEventServiceClient(server.Client(), server.URL+rpcPrefix)
	unauthStream, err := unauthClient.StreamEvents(t.Context(), connect.NewRequest(&ariadnev1.StreamEventsRequest{}))
	if err == nil {
		if unauthStream.Receive() {
			t.Fatal("unauthenticated StreamEvents yielded a message")
		}
		err = unauthStream.Err()
		_ = unauthStream.Close()
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", err)
	}

	authClient := ariadnev1connect.NewEventServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("secret")),
	)

	// Replay from cursor 0 delivers ready + durable event, then closes cleanly on cancel.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	zero := int64(0)
	stream, err := authClient.StreamEvents(ctx, connect.NewRequest(&ariadnev1.StreamEventsRequest{AfterSeq: &zero}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected ready frame, err=%v", stream.Err())
	}
	if ready := stream.Msg().GetReady(); ready == nil || ready.GetLastSeq() != 0 {
		t.Fatalf("unexpected first frame: %+v", stream.Msg())
	}
	if !stream.Receive() {
		t.Fatalf("expected replayed event frame, err=%v", stream.Err())
	}
	ev := stream.Msg().GetEvent()
	if ev == nil || ev.GetType() != "project.created" || ev.GetProjectId() != "prj_a" || ev.GetSeq() != 1 {
		t.Fatalf("unexpected event frame: %+v", stream.Msg())
	}
	cancel()
	_ = stream.Close()

	// Negative afterSeq returns CodeInvalidArgument.
	neg := int64(-1)
	badStream, err := authClient.StreamEvents(t.Context(), connect.NewRequest(&ariadnev1.StreamEventsRequest{AfterSeq: &neg}))
	if err == nil {
		if badStream.Receive() {
			t.Fatal("negative afterSeq yielded a message")
		}
		err = badStream.Err()
		_ = badStream.Close()
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for negative afterSeq, got %v", err)
	}
}

func TestPingRequiresAuthAndSucceedsWithToken(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ping.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	api, err := New(Options{Repository: repo, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	server := httptest.NewServer(api)
	defer server.Close()
	unauthClient := ariadnev1connect.NewMetaServiceClient(server.Client(), server.URL+rpcPrefix)
	if _, err := unauthClient.Ping(t.Context(), connect.NewRequest(&ariadnev1.PingRequest{})); err == nil {
		t.Fatal("expected unauthenticated Ping to fail")
	} else if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unexpected unauthenticated Ping error: %v", err)
	}

	authClient := ariadnev1connect.NewMetaServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("secret")),
	)
	res, err := authClient.Ping(t.Context(), connect.NewRequest(&ariadnev1.PingRequest{}))
	if err != nil {
		t.Fatalf("authenticated Ping failed: %v", err)
	}
	if !res.Msg.GetOk() {
		t.Fatalf("authenticated Ping returned ok=false")
	}
}

func TestStaticAssetsAndPWAMIME(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "static.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	api, err := New(Options{
		Repository: repo,
		Token:      "secret",
		Web: fstest.MapFS{
			"index.html":           {Data: []byte("<!doctype html>")},
			"manifest.webmanifest": {Data: []byte(`{"name":"Ariadne"}`)},
			"favicon.svg":          {Data: []byte(`<svg></svg>`)},
			"favicon.ico":          {Data: []byte("ico")},
			"icons/icon-192.png":   {Data: []byte("png")},
			"sw.js":                {Data: []byte("self.addEventListener('install', () => {})")},
			"registerSW.js":        {Data: []byte("navigator.serviceWorker.register('/sw.js')")},
			"workbox-test1234.js":  {Data: []byte("// workbox")},
			"assets/app.js":        {Data: []byte("console.log('ok')")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	for _, tc := range []struct {
		path        string
		contentType string
		cache       string
	}{
		{"/manifest.webmanifest", "application/manifest+json", "no-cache"},
		{"/favicon.svg", "image/svg+xml", "no-cache"},
		{"/favicon.ico", "image/x-icon", "no-cache"},
		{"/icons/icon-192.png", "image/png", "no-cache"},
		{"/sw.js", "text/javascript", "no-cache"},
		{"/registerSW.js", "text/javascript", "no-cache"},
		{"/workbox-test1234.js", "text/javascript", "no-cache"},
		{"/assets/app.js", "text/javascript", "public, max-age=31536000, immutable"},
	} {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, httptest.NewRequest("GET", "http://test.invalid"+tc.path, nil))
		if response.Code != 200 {
			t.Fatalf("%s: status %d", tc.path, response.Code)
		}
		if !strings.Contains(response.Header().Get("Content-Type"), tc.contentType) {
			t.Fatalf("%s: Content-Type %q does not contain %q", tc.path, response.Header().Get("Content-Type"), tc.contentType)
		}
		if response.Header().Get("Cache-Control") != tc.cache {
			t.Fatalf("%s: Cache-Control %q != %q", tc.path, response.Header().Get("Cache-Control"), tc.cache)
		}
	}

	// Missing static assets must return 404 instead of falling back to index.html.
	for _, missing := range []string{"/icons/missing.png", "/apple-touch-icon.png", "/missing.webmanifest", "/missing-sw.js", "/assets/missing.js"} {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, httptest.NewRequest("GET", "http://test.invalid"+missing, nil))
		if response.Code != 404 {
			t.Fatalf("missing static asset %s: got %d (%s)", missing, response.Code, response.Body.String())
		}
	}

	// Client-side SPA routes still fall back to index.html.
	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest("GET", "http://test.invalid/projects/prj_123", nil))
	if response.Code != 200 || response.Body.String() != "<!doctype html>" {
		t.Fatalf("client route fallback failed: %d %s", response.Code, response.Body.String())
	}
}
