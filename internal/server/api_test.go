package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
func TestDomainHTTPResponsesMatchLegacy(t *testing.T) {
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
	var expected []any
	bun, legacyErr := exec.LookPath("bun")
	if legacyErr == nil {
		input := encoded(object{"seed": seed, "sql": sql, "requests": requests})
		command := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		command.Dir = filepath.Join("..", "..")
		command.Stdin = strings.NewReader(input)
		data, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("legacy: %v\n%s", err, data)
		}
		if err := json.Unmarshal(data, &expected); err != nil {
			t.Fatalf("%v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(legacyErr)
	}
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
		actual := object{"status": response.Code, "body": value}
		if expected != nil {
			want := normalizeLegacyAPIResponse(expected[i])
			if !reflect.DeepEqual(canonical(actual), canonical(want)) {
				t.Fatalf("request %d %s %s differs\ngot %s\nwant%s", i, item.Method, item.Path, encoded(canonical(actual)), encoded(canonical(want)))
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
	if len(api.routes) == 0 {
		t.Fatal("expected native HTTP API routes to be registered")
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
	// A successful bearer request grants native image reads only, never API or RPC calls.
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

func normalizeLegacyAPIResponse(raw any) any {
	resp, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	body, ok := resp["body"].(map[string]any)
	if !ok {
		return raw
	}
	if sync, ok := body["sync"].(map[string]any); ok {
		sync["cursor"] = "epoch.cursor"
	}
	if needs, ok := body["needsMe"].([]any); ok {
		normalized := make([]any, 0, len(needs))
		for _, entry := range needs {
			item, ok := entry.(map[string]any)
			if !ok {
				normalized = append(normalized, entry)
				continue
			}
			switch item["kind"] {
			case "thread":
				var id any
				if thread, ok := item["thread"].(map[string]any); ok {
					id = thread["id"]
				}
				normalized = append(normalized, map[string]any{"kind": "thread", "threadId": id})
			case "attention":
				var id any
				if thread, ok := item["thread"].(map[string]any); ok {
					id = thread["id"]
				}
				joinedIDs := []any{}
				if joined, ok := item["joined"].([]any); ok {
					for _, rawThread := range joined {
						if thread, ok := rawThread.(map[string]any); ok {
							joinedIDs = append(joinedIDs, thread["id"])
						}
					}
				}
				normalized = append(normalized, map[string]any{
					"kind":      "attention",
					"attention": item["attention"],
					"threadId":  id,
					"joinedIds": joinedIDs,
				})
			default:
				normalized = append(normalized, entry)
			}
		}
		body["needsMe"] = normalized
	}
	return resp
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
