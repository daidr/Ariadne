package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
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
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"讨论串","nodeId":null,"branch":null,"model":null}`)},
		{Type: "thread.reported", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","report":"ready","summary":"可以交付","headline":"完成","artifacts":[]}`)},
		{Type: "message.posted", ProjectID: "prj_a", Data: json.RawMessage(`{"messageId":"msg_agent","conversationId":"thr_a","author":"thread","kind":"chat","content":"代理消息"}`)},
	}
	hash := sha256.Sum256([]byte("abc"))
	sql := fmt.Sprintf(`INSERT INTO files VALUES ('fil_text','prj_a','text/plain',3,'%s',x'616263',0)`, hex.EncodeToString(hash[:]))
	requests := []apiRequest{}
	add := func(method, path string, body any) {
		requests = append(requests, apiRequest{Method: method, Path: path, Body: body})
	}
	no := false
	requests = append(requests, apiRequest{Method: "GET", Path: "/api/health", Auth: &no})
	for _, path := range []string{"/api/projects", "/api/files/fil_text", "/api/files/fil_missing", "/api/projects?token=test-token", "/api/unknown"} {
		requests = append(requests, apiRequest{Method: "GET", Path: path, Auth: &no})
	}
	add("HEAD", "/api/health", nil)
	add("POST", "/api/health", nil)
	add("GET", "/api/unknown", nil)
	add("GET", "/api/projects", nil)
	add("GET", "/api/projects/prj_a", nil)
	add("GET", "/api/projects/prj_a/source", nil)
	add("GET", "/api/projects/prj_a?since=@cursor@", nil)
	add("PUT", "/api/projects/prj_a", object{"name": " 新名称 "})
	add("PUT", "/api/projects/prj_a", object{"name": " "})
	add("PUT", "/api/projects/prj_a/slots", object{"slots": 2})
	add("PUT", "/api/projects/prj_a/slots", object{"slots": 0})
	add("PUT", "/api/projects/prj_a/slots", object{"slots": nil})
	add("PUT", "/api/projects/prj_a/autopilot", object{"paused": true})
	add("GET", "/api/conversations/crd_a", nil)
	for _, query := range []string{"limit=0", "limit=1000", "since=-1", "since=1.5", "limit=500", "before=missing", "since=0"} {
		add("GET", "/api/conversations/crd_a?"+query, nil)
	}
	add("GET", "/api/conversations/missing", nil)
	add("POST", "/api/projects/prj_a/messages", object{"conversationId": "crd_a", "content": "用户消息", "mentions": []object{{"kind": "thread", "id": "thr_a"}}})
	add("POST", "/api/projects/prj_a/messages", object{"conversationId": "thr_a", "attachments": "invalid"})
	add("POST", "/api/projects/prj_a/messages", object{"conversationId": "thr_a", "content": "附件", "attachments": []object{{"fileId": "fil_text", "name": "资料.txt"}}})
	add("GET", "/api/conversations/thr_a", nil)
	add("POST", "/api/conversations/thr_a/read", object{"upTo": "msg_agent"})
	add("POST", "/api/messages/msg_agent/reactions", object{"emoji": " 👍 "})
	add("POST", "/api/messages/msg_agent/reactions", object{"emoji": " 👍 ", "remove": true})
	add("GET", "/api/projects/prj_a/search?q=代理", nil)
	add("POST", "/api/projects/prj_a/memory", object{"text": " "})
	add("POST", "/api/projects/prj_a/memory", object{"text": " 常用规则 "})
	add("GET", "/api/projects/prj_a/memory", nil)
	add("GET", "/api/files/fil_text", nil)
	raw := "abc"
	requests = append(requests, apiRequest{Method: "POST", Path: "/api/projects/prj_a/files?for=message", Raw: &raw, Headers: map[string]string{"Content-Type": "text/plain"}})
	add("GET", "/api/projects/prj_a/plan", nil)
	add("POST", "/api/projects/prj_a/plan/revise", object{"reason": "新增任务", "ops": []object{{"op": "add", "id": "n", "kind": "task", "title": "执行", "description": "", "acceptance": "测试通过"}}})
	add("POST", "/api/projects/prj_a/plan/revise", object{"reason": "缺少节点", "ops": []object{{"op": "remove", "id": "missing"}}})
	add("POST", "/api/projects/prj_a/artifacts", object{"kind": "invalid", "title": "产物"})
	requests = append(requests, apiRequest{Method: "POST", Path: "/api/projects/prj_a/artifacts", Body: object{"kind": "document", "title": "文档"}, Save: "doc"}, apiRequest{Method: "POST", Path: "/api/projects/prj_a/artifacts", Body: object{"kind": "whiteboard", "title": "画板"}, Save: "board"})
	add("GET", "/api/artifacts/@doc@", nil)
	add("PUT", "/api/artifacts/@doc@", object{"content": "# 文档内容", "baseVersion": 1, "quotes": object{"unused": 1}})
	add("PUT", "/api/artifacts/@doc@", object{"content": "覆盖冲突", "baseVersion": 1})
	for _, version := range []string{"bad", "", "0x1", "%201%20", "1.5", "1_0", "0x1p0", "inf"} {
		add("GET", "/api/artifacts/@doc@?version="+version, nil)
	}
	add("POST", "/api/artifacts/@doc@/comments", object{"type": "create", "threadId": "comment_a", "commentId": "item_a", "quote": "文档内容", "body": "修改意见"})
	add("POST", "/api/artifacts/@board@/comments", object{"type": "create", "threadId": "board_a", "commentId": "board_item", "anchor": object{"node": "a"}, "body": "画板意见"})
	add("POST", "/api/artifacts/@board@/comments", object{"type": "create", "threadId": "board_bad", "commentId": "board_item", "anchor": nil, "body": "无效"})
	add("GET", "/api/projects/prj_a/artifacts", nil)
	add("POST", "/api/threads/thr_a/stop", object{"reason": " 暂停 "})
	add("POST", "/api/threads/thr_a/resume", object{})
	add("GET", "/api/threads/thr_a/reviews", nil)
	add("GET", "/api/threads/thr_a/line-comments", nil)
	add("POST", "/api/conversations/crd_a/stop", nil)
	add("POST", "/api/conversations/crd_a/retry", nil)
	add("POST", "/api/projects/prj_a/return", object{"away": 600000})
	add("GET", "/api/projects/prj_a?since=@cursor@", nil)
	invalid := "[]"
	requests = append(requests, apiRequest{Method: "PUT", Path: "/api/projects/prj_a", Raw: &invalid})
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
	aliases := map[string]string{}
	aliasPattern := regexp.MustCompile(`@([a-z]+)@`)
	for i, item := range requests {
		path := aliasPattern.ReplaceAllStringFunc(item.Path, func(value string) string { return aliases[value[1:len(value)-1]] })
		body := ""
		if item.Raw != nil {
			body = *item.Raw
		} else if item.Body != nil {
			body = encoded(item.Body)
		}
		request := httptest.NewRequest(item.Method, "http://test.invalid"+path, strings.NewReader(body))
		if item.Auth == nil || *item.Auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		for key, value := range item.Headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var value any = response.Body.String()
		json.Unmarshal(response.Body.Bytes(), &value)
		if object, ok := value.(map[string]any); ok {
			if artifact, ok := object["artifact"].(map[string]any); ok && item.Save != "" {
				aliases[item.Save] = artifact["id"].(string)
			}
			if sync, ok := object["sync"].(map[string]any); ok {
				cursor := sync["cursor"].(string)
				aliases["cursor"] = cursor
				sync["cursor"] = "epoch.cursor"
			}
		}
		if response.Code >= 500 {
			t.Fatalf("request %d failed: %s", i, response.Body.String())
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
	contract, err := os.ReadFile(filepath.Join("..", "compat", "testdata", "backend-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Routes []struct{ Method, Path string }
	}
	if err := json.Unmarshal(contract, &manifest); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, route := range manifest.Routes {
		method := route.Method
		if method == "WS" {
			method = "GET"
		}
		known[method+" "+route.Path] = true
	}
	// Routes added after the frozen Bun contract also require authentication.
	known["GET /api/ping"] = true
	known["GET /api/files/:fileId/sheet"] = true
	known["GET /api/projects/:projectId/sources"] = true
	known["GET /api/projects/:projectId/activity"] = true
	known["GET /api/threads/:threadId/pull-request/checks"] = true
	known["PUT /api/threads/:threadId/node"] = true
	known["GET /api/conversations/:conversationId/trace/export"] = true
	known["PUT /api/projects/:projectId/sources"] = true
	known["GET /api/plugins/:pluginId/settings"] = true
	known["PUT /api/plugins/:pluginId/settings"] = true
	known["GET /api/updates"] = true
	known["PUT /api/updates"] = true
	known["POST /api/updates/check"] = true
	known["POST /api/updates/install"] = true
	t.Logf("%d native API routes registered", len(api.routes))
	registered := map[string]bool{}
	for _, route := range api.routes {
		registered[route] = true
	}
	for route := range known {
		if !registered[route] {
			t.Errorf("missing native route: %s", route)
		}
	}
	for _, route := range api.routes {
		if !known[route] {
			t.Fatalf("route missing from the tested route inventory: %s", route)
		}
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
	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest("GET", "http://test.invalid/any/client/path", nil))
	if response.Code != 200 || response.Body.String() != "app shell" {
		t.Fatal(response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest("GET", "http://test.invalid/api/missing", nil))
	if response.Code != 401 {
		t.Fatal("API request fell through to static files")
	}
	// A successful bearer request grants native image reads only, never API calls.
	request := httptest.NewRequest("GET", "http://test.invalid/api/projects", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	api.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	for _, path := range []string{"/api/projects", "/api/projects?token=secret", "/api/artifacts/missing", "/api/settings/models", "/api/conversations/missing/trace/export"} {
		request := httptest.NewRequest("GET", "http://test.invalid"+path, nil)
		request.AddCookie(cookies[0])
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("file cookie authorized %s", path)
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
	request, err := http.NewRequestWithContext(t.Context(), "GET", server.URL+"/api/events?token=secret&afterSeq=not-a-number", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "0")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(response.StatusCode, response.Header)
	}
	reader := make([]byte, 512)
	n, err := response.Body.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reader[:n]), "event: ariadne") {
		t.Fatal(string(reader[:n]))
	}
	response.Body.Close()
	request, err = http.NewRequestWithContext(t.Context(), "GET", server.URL+"/api/events?token=secret&afterSeq=-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal(response.StatusCode)
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

	unauth := httptest.NewRecorder()
	api.ServeHTTP(unauth, httptest.NewRequest("GET", "http://test.invalid/api/ping", nil))
	if unauth.Code != 401 {
		t.Fatalf("unauthenticated /api/ping returned %d", unauth.Code)
	}

	req := httptest.NewRequest("GET", "http://test.invalid/api/ping", nil)
	req.Header.Set("Authorization", "Bearer secret")
	auth := httptest.NewRecorder()
	api.ServeHTTP(auth, req)
	if auth.Code != 200 || strings.TrimSpace(auth.Body.String()) != `{"ok":true}` {
		t.Fatalf("authenticated /api/ping returned %d %s", auth.Code, auth.Body.String())
	}
}

func TestSetThreadNodeRoute(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "thread-node.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("A"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, project.ID, core.PlanRevision{Author: "user", Reason: "计划", Ops: []map[string]any{
		{"op": "add", "id": "nod_1", "kind": "task", "title": "任务一", "description": "", "acceptance": ""},
	}}); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "讨论串"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	put := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("PUT", "http://test.invalid/api/threads/"+thread.ID+"/node", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}

	if res := put(`{}`); res.Code != 400 {
		t.Fatalf("missing nodeId: %d %s", res.Code, res.Body.String())
	}
	if res := put(`{"nodeId":"nod_missing"}`); res.Code != 404 {
		t.Fatalf("unknown nodeId: %d %s", res.Code, res.Body.String())
	}
	if res := put(`{"nodeId":"nod_1"}`); res.Code != 200 || !strings.Contains(res.Body.String(), `"nodeId":"nod_1"`) {
		t.Fatalf("link thread node: %d %s", res.Code, res.Body.String())
	}
	if res := put(`{"nodeId":null}`); res.Code != 200 || !strings.Contains(res.Body.String(), `"nodeId":null`) {
		t.Fatalf("unlink thread node: %d %s", res.Code, res.Body.String())
	}
}
