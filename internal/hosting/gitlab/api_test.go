package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/daidr/Ariadne/internal/hostinghttp"
	"github.com/daidr/Ariadne/internal/legacytest"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/sdk"
)

type call struct {
	Method string `json:"method"`
	Args   []any  `json:"args,omitempty"`
}
type response struct {
	Body   any `json:"body"`
	Status int `json:"status,omitempty"`
}
type scenario struct {
	Calls     []call              `json:"calls"`
	Responses map[string]response `json:"responses"`
}
type sent struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Body          any    `json:"body"`
	Authorization string `json:"authorization"`
}

func mergeRaw(number int, state, branch, at string) map[string]any {
	return map[string]any{"iid": number, "web_url": fmt.Sprintf("https://gitlab.test/group/sub/app/-/merge_requests/%d", number), "title": "修复问题", "state": state, "work_in_progress": true, "sha": nil, "diff_refs": map[string]any{"head_sha": "abc"}, "source_branch": branch, "source_project_id": 15, "target_branch": "main", "created_at": at, "author": nil, "changes_count": "1000+", "merged_at": nil, "closed_at": nil}
}
func issueRaw(number, comments int) map[string]any {
	return map[string]any{"iid": number, "web_url": fmt.Sprintf("https://gitlab.test/group/sub/app/-/issues/%d", number), "title": "任务", "state": "opened", "author": map[string]any{"username": "alice"}, "labels": []any{"bug", map[string]any{"name": "中文"}, map[string]any{}}, "user_notes_count": comments, "description": nil, "created_at": "2026-10-01", "updated_at": "2026-11-01"}
}
func noteRaw(id int, system bool) map[string]any {
	return map[string]any{"id": id, "author": nil, "body": strconv.Itoa(id), "created_at": "2026-11-01", "system": system}
}
func invoke(ctx context.Context, api *API, c call) (any, error) {
	arg := func(i int) string {
		if i >= len(c.Args) {
			return ""
		}
		value, _ := c.Args[i].(string)
		return value
	}
	number := func(i int) int { return c.Args[i].(int) }
	switch c.Method {
	case "project":
		return api.Project(ctx)
	case "mergeRequestFor":
		return api.MergeRequestFor(ctx, arg(0), number(1))
	case "mergeRequest":
		return api.MergeRequest(ctx, number(0))
	case "mergeRequestPreview":
		return api.Preview(ctx, number(0))
	case "openMergeRequest":
		data, _ := json.Marshal(c.Args[0])
		var input sdk.OpenPullRequest
		json.Unmarshal(data, &input)
		return api.OpenMergeRequest(ctx, input)
	case "mergeMergeRequest":
		input := c.Args[1].(map[string]any)
		return api.MergeMergeRequest(ctx, number(0), input["method"].(string), input["sha"].(string))
	case "checks":
		return api.Checks(ctx, arg(0))
	case "checkLog":
		return api.CheckLog(ctx, arg(0))
	case "issues":
		data, _ := json.Marshal(c.Args[0])
		var query sdk.IssueQuery
		json.Unmarshal(data, &query)
		return api.Issues(ctx, query)
	case "issue":
		return api.Issue(ctx, number(0))
	case "comment":
		return api.Comment(ctx, number(0), arg(1))
	case "setIssueState":
		return api.SetIssueState(ctx, number(0), arg(1), arg(2))
	}
	return nil, fmt.Errorf("unknown method %s", c.Method)
}
func TestAPIAndParsingMatchLegacy(t *testing.T) {
	path := "/api/v4/projects/group%2Fsub%2Fapp"
	branch := "feature/中文 ~!*"
	current := mergeRaw(7, "opened", branch, "2026-11-01")
	current["draft"] = false
	closed := mergeRaw(8, "closed", branch, "2026-11-14")
	closed["changes_count"] = "4"
	closed["closed_at"] = "2026-11-15"
	closed["draft"] = true
	merged := mergeRaw(9, "merged", branch, "2026-11-14")
	merged["merged_at"] = "2026-11-15"
	fork := mergeRaw(90, "opened", branch, "2026-11-16")
	fork["source_project_id"] = 42
	notes := []any{}
	for i := 45; i > 0; i-- {
		notes = append(notes, noteRaw(i, i%3 == 0))
	}
	statuses := []any{}
	for i, status := range []string{"created", "waiting_for_resource", "preparing", "pending", "running", "success", "failed", "canceled", "canceling", "skipped", "manual", "scheduled"} {
		statuses = append(statuses, map[string]any{"id": i + 1, "name": status, "status": status, "target_url": nil})
	}
	statuses = append(statuses, map[string]any{"id": 20, "name": "allowed", "status": "failed", "allow_failure": true, "target_url": "https://ci"}, map[string]any{"id": 50, "name": "failed", "status": "success", "target_url": nil}, map[string]any{"id": 3, "name": "failed", "status": "failed", "target_url": nil})
	flow := scenario{Calls: []call{{"project", nil}, {"mergeRequestFor", []any{branch, 15}}, {"mergeRequest", []any{7}}, {"mergeRequestPreview", []any{7}}, {"mergeRequestPreview", []any{8}}, {"mergeRequestPreview", []any{9}}, {"openMergeRequest", []any{map[string]any{"branch": branch, "base": "main", "title": "标题", "body": "内容", "draft": true}}}, {"openMergeRequest", []any{map[string]any{"branch": branch, "base": "main", "title": "DRAFT: 标题", "body": "内容", "draft": true}}}, {"checks", []any{"feature/abc"}}, {"checkLog", []any{"9"}}, {"checkLog", []any{"10"}}, {"checkLog", []any{"external:11"}}, {"issues", []any{map[string]any{"labels": []string{"bug*~", "中文"}, "search": " 中文 问题 ", "limit": 200}}}, {"issues", []any{map[string]any{"state": "all", "limit": -1}}}, {"issue", []any{1}}, {"comment", []any{1, "新评论"}}, {"setIssueState", []any{1, "open"}}, {"setIssueState", []any{1, "closed"}}}, Responses: map[string]response{
		"GET " + path: {Body: map[string]any{"id": 15, "default_branch": "main", "web_url": "https://gitlab.test/group/sub/app", "path_with_namespace": "group/sub/app", "visibility": "private"}},
		"GET " + path + "/merge_requests?source_branch=" + hostinghttp.FormComponent(branch) + "&state=all&order_by=created_at&sort=desc&per_page=20": {Body: []any{closed, fork, current, mergeRaw(2, "opened", branch, "2026-01-01")}},
		"GET " + path + "/merge_requests/7": {Body: current}, "GET " + path + "/merge_requests/8": {Body: closed}, "GET " + path + "/merge_requests/9": {Body: merged}, "POST " + path + "/merge_requests": {Body: current},
		"GET " + path + "/repository/commits/feature%2Fabc/statuses?per_page=100": {Body: statuses},
		"GET " + path + "/jobs/9/trace":                                           {Body: strings.Repeat("\x1b[31m甲😀\x1b[0m\r", 6_000) + "section_start:123:build[collapsed=true]\r\x1b[0K\r\n最后\r\nsection_end:124:build\r\x1b[0K\r\n"},
		"GET " + path + "/jobs/10/trace":                                          {Status: 404, Body: map[string]any{"message": "404 Job Not Found"}},
		"GET " + path + "/issues?order_by=updated_at&sort=desc&per_page=100&state=opened&labels=bug*%7E%2C%E4%B8%AD%E6%96%87&search=%E4%B8%AD%E6%96%87+%E9%97%AE%E9%A2%98": {Body: []any{issueRaw(1, 45)}},
		"GET " + path + "/issues?order_by=updated_at&sort=desc&per_page=1":                                                                                                 {Body: []any{}},
		"GET " + path + "/issues/1": {Body: issueRaw(1, 45)}, "GET " + path + "/issues/1/notes?sort=desc&order_by=created_at&per_page=100": {Body: notes}, "POST " + path + "/issues/1/notes": {Body: noteRaw(46, false)}, "PUT " + path + "/issues/1": {Body: issueRaw(1, 46)},
	}}
	scenarios := []scenario{flow}
	for _, status := range []int{200, 400, 405, 406, 409, 422, 403} {
		for _, conflict := range []bool{false, true} {
			body := map[string]any{"message": "Merge refused"}
			if status == 200 {
				body = map[string]any{"merge_commit_sha": nil, "squash_commit_sha": "squashed"}
			}
			scenarios = append(scenarios, scenario{Calls: []call{{"mergeMergeRequest", []any{7, map[string]any{"method": "squash", "sha": "old"}}}}, Responses: map[string]response{"PUT " + path + "/merge_requests/7/merge": {Body: body, Status: status}, "GET " + path + "/merge_requests/7": {Body: map[string]any{"has_conflicts": conflict}}}})
		}
	}
	nativeScenarios := []any{}
	for i, scenario := range scenarios {
		nativeScenarios = append(nativeScenarios, func() any {
			var mu sync.Mutex
			requests := []sent{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				json.NewDecoder(r.Body).Decode(&body)
				path := r.URL.EscapedPath()
				if r.URL.RawQuery != "" {
					path += "?" + r.URL.RawQuery
				}
				mu.Lock()
				requests = append(requests, sent{r.Method, path, body, r.Header.Get("Authorization")})
				mu.Unlock()
				response, ok := scenario.Responses[r.Method+" "+path]
				if !ok {
					t.Errorf("scenario %d unexpected %s %s", i, r.Method, path)
					http.Error(w, "unexpected", 500)
					return
				}
				status := response.Status
				if status == 0 {
					status = 200
				}
				w.WriteHeader(status)
				if text, ok := response.Body.(string); ok {
					io.WriteString(w, text)
				} else {
					json.NewEncoder(w).Encode(response.Body)
				}
			}))
			defer server.Close()
			token := "private-token"
			api, err := NewAPI(APIOptions{Base: server.URL + "/api/v4", Path: "group/sub/app", WebURL: "https://gitlab.test/group/sub/app", Token: func(context.Context) (*string, error) { return &token, nil }, Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			results := []any{}
			for _, c := range scenario.Calls {
				value, err := invoke(t.Context(), api, c)
				if err == nil {
					results = append(results, map[string]any{"value": value})
				} else {
					failure := map[string]any{"error": err.Error()}
					var status *hostinghttp.Error
					if errors.As(err, &status) {
						failure["status"], failure["reason"] = status.Status, status.Reason
					}
					results = append(results, failure)
				}
			}
			mu.Lock()
			requests = append([]sent(nil), requests...)
			mu.Unlock()
			slices.SortStableFunc(requests, func(a, b sent) int { return strings.Compare(a.Method+" "+a.Path, b.Method+" "+b.Path) })
			return map[string]any{"requests": requests, "results": results}
		}())
	}
	repositories := []map[string]string{{"input": "group/sub/app"}, {"input": "https://gitlab.test/group/app.git/"}, {"input": "https://gitlab.test/group/app/-/issues/12"}, {"input": "https://gitlab.test/gitlab/group/sub/app", "instance": "https://gitlab.test/gitlab/"}, {"input": "git@gitlab.test:group/sub/app.git"}, {"input": "ssh://git@gitlab.test:2222/group/app.git"}, {"input": "ssh://gitlab.test/group/app"}, {"input": "git@gitlab.test:group/app", "instance": "https://gitlab.test/gitlab"}, {"input": "group/app.git/"}, {"input": "group/app", "instance": "https://GitLab.Test:443/gitlab/"}, {"input": "group/app", "instance": "http://gitlab.test"}, {"input": "https://other.test/group/app", "instance": "https://gitlab.test"}, {"input": "group/../app"}, {"input": "group/a.git/b"}, {"input": "group/-app"}, {"input": "not-project"}}
	nativeRepositories := []any{}
	for _, input := range repositories {
		value, err := ParseRepository(input["input"], input["instance"])
		if err == nil {
			nativeRepositories = append(nativeRepositories, map[string]any{"value": value})
		} else {
			nativeRepositories = append(nativeRepositories, map[string]any{"error": err.Error()})
		}
	}
	instances := []string{" https://GitLab.Test:443/gitlab/// ", "https://host/a/../gitlab", "https://host/a/%2e%2e/gitlab", "http://localhost:7410/gitlab/", "http://127.0.0.2:7410", "http://[::1]:7410", "http://host", "ftp://host", "https://user:token@host", "https://host?key=x", "https://host#x", "not-address"}
	nativeInstances := []any{}
	for _, input := range instances {
		value, err := ParseInstance(input)
		if err == nil {
			nativeInstances = append(nativeInstances, map[string]any{"value": value})
		} else {
			nativeInstances = append(nativeInstances, map[string]any{"error": err.Error()})
		}
	}
	links := []map[string]string{{"url": "https://gitlab.test/group/sub/app/-/merge_requests/12/diffs#note", "instance": "https://gitlab.test"}, {"url": "https://gitlab.test/gitlab/group/app/-/merge_requests/0", "instance": "https://gitlab.test/gitlab"}, {"url": "http://localhost/group/app/-/merge_requests/12", "instance": "http://localhost"}, {"url": "https://other.test/group/app/-/merge_requests/12", "instance": "https://gitlab.test"}, {"url": "https://gitlab.test/group/app/-/merge_requests/1234567890", "instance": "https://gitlab.test"}}
	nativeLinks := []any{}
	for _, input := range links {
		address, _ := url.Parse(input["url"])
		nativeLinks = append(nativeLinks, ParseMergeRequestURL(address, input["instance"]))
	}
	registered := []map[string]string{{"instance": "https://gitlab.test", "path": "group/app", "token": "own"}, {"instance": "https://gitlab.test", "path": "group/other", "token": "other"}, {"instance": "https://gitlab.test/gitlab", "path": "group/app", "token": "nested"}, {"instance": "https://other.test", "path": "group/app", "token": "private-other-host"}}
	registry := NewInstances(map[string]string{})
	defer registry.Close()
	for _, project := range registered {
		if err := registry.Add(project["instance"], project["path"], project["token"]); err != nil {
			t.Fatal(err)
		}
	}
	find := []string{"https://gitlab.test/group/app/-/merge_requests/12", "https://gitlab.test/group/else/-/merge_requests/1", "https://gitlab.test/gitlab/group/app/-/merge_requests/2", "https://github.com/group/app/pull/12"}
	nativeFind := []any{}
	for _, input := range find {
		address, _ := url.Parse(input)
		nativeFind = append(nativeFind, registry.Find(address))
	}
	logs := []string{"section_start:123:build[collapsed=true]\r\x1b[0Khello\x1b[31m红色\x1b[0m\rnext\r\nsection_end:124:build\r\x1b[0K\r\n", "a\r\rb\r\n", "section_start:\x1b[31mx\x1b[0m", "section_start:1:x\rno\x1b[31mred", "\x1b[3\r\n", "section_end:1:broken", "section_start:1:section_end:2:end\x1b[0K", "\ufeff中文😀"}
	nativeLogs := []string{}
	for _, text := range logs {
		nativeLogs = append(nativeLogs, PlainLog(text))
		streamed, err := io.ReadAll(plainStream(strings.NewReader(text)))
		if err != nil || string(streamed) != PlainLog(text) {
			t.Fatalf("stream differs from plain log %q: %q %v", text, streamed, err)
		}
	}
	info := Provider(Options{}).Info
	// Compare the legacy fields after checking the new directory field.
	if !reflect.DeepEqual(info.Settings.Properties[local.HostedDirectory], local.HostedDirectoryField()) {
		t.Fatal("repository storage directory field is missing or invalid")
	}
	delete(info.Settings.Properties, local.HostedDirectory)
	info.Settings.FieldOrder = slices.DeleteFunc(info.Settings.FieldOrder, func(field string) bool {
		return field == local.HostedDirectory
	})
	metadata := map[string]any{"kind": info.Kind, "title": info.Title, "settings": info.Settings, "provides": info.Provides, "hosting": info.Hosting}
	native := map[string]any{"scenarios": nativeScenarios, "repositories": nativeRepositories, "instances": nativeInstances, "links": nativeLinks, "find": nativeFind, "logs": nativeLogs, "metadata": metadata}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("native tests passed; Bun absent")
	}
	input, _ := json.Marshal(map[string]any{"scenarios": scenarios, "repositories": repositories, "instances": instances, "links": links, "registered": registered, "find": find, "logs": logs})
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	cmd := legacytest.Command(t, bun, "packages/plugin-gitlab/src/testing/go-api-fixture.ts")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(string(input))
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy: %v\n%s", err, data)
	}
	var actual, want any
	encoded, _ := json.Marshal(native)
	json.Unmarshal(encoded, &actual)
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("legacy JSON: %v %s", err, data)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatal(firstDifference("$", actual, want))
	}
}
func firstDifference(path string, actual, want any) string {
	if reflect.DeepEqual(actual, want) {
		return ""
	}
	if a, ok := actual.(map[string]any); ok {
		if b, ok := want.(map[string]any); ok {
			for key, value := range a {
				if difference := firstDifference(path+"."+key, value, b[key]); difference != "" {
					return difference
				}
			}
			for key := range b {
				if _, ok := a[key]; !ok {
					return path + " missing " + key
				}
			}
		}
	}
	if a, ok := actual.([]any); ok {
		if b, ok := want.([]any); ok {
			for i := 0; i < min(len(a), len(b)); i++ {
				if difference := firstDifference(fmt.Sprintf("%s[%d]", path, i), a[i], b[i]); difference != "" {
					return difference
				}
			}
			return fmt.Sprintf("%s lengths: Go=%d TS=%d", path, len(a), len(b))
		}
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(want)
	if len(a) > 300 {
		a = a[:300]
	}
	if len(b) > 300 {
		b = b[:300]
	}
	return fmt.Sprintf("%s differs: Go=%s TS=%s", path, a, b)
}

func TestMergeRequestConflictParsing(t *testing.T) {
	cases := []struct {
		name         string
		state        string
		hasConflicts bool
		want         bool
	}{
		{name: "opened with conflicts", state: "opened", hasConflicts: true, want: true},
		{name: "opened without conflicts", state: "opened", hasConflicts: false, want: false},
		{name: "merged ignores conflict flag", state: "merged", hasConflicts: true, want: false},
		{name: "closed ignores conflict flag", state: "closed", hasConflicts: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := mergeRaw(7, tc.state, "feature", "2026-11-01")
			payload["has_conflicts"] = tc.hasConflicts
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != "/api/v4/projects/group%2Fsub%2Fapp/merge_requests/7" {
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(payload)
			}))
			defer server.Close()
			api, err := NewAPI(APIOptions{Base: server.URL + "/api/v4", Path: "group/sub/app", WebURL: "https://gitlab.test/group/sub/app", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			info, err := api.MergeRequest(t.Context(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if info.Conflicted != tc.want {
				t.Fatalf("MergeRequest.Conflicted = %v, want %v", info.Conflicted, tc.want)
			}
			preview, err := api.Preview(t.Context(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Conflicted != tc.want {
				t.Fatalf("Preview.Conflicted = %v, want %v", preview.Conflicted, tc.want)
			}
		})
	}
}
