package github

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

func TestChecksCollectFollowingPages(t *testing.T) {
	runs := make([]map[string]any, 100)
	for i := range runs {
		runs[i] = map[string]any{"id": i + 1, "name": fmt.Sprintf("run %d", i+1), "status": "completed", "conclusion": "success"}
	}
	statuses := make([]map[string]any, 30)
	for i := range statuses {
		statuses[i] = map[string]any{"context": fmt.Sprintf("status %d", i+1), "state": "success"}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/repos/acme/app/commits/abc/check-runs?per_page=100":
			json.NewEncoder(w).Encode(map[string]any{"check_runs": runs})
		case "/repos/acme/app/commits/abc/check-runs?per_page=100&page=2":
			json.NewEncoder(w).Encode(map[string]any{"check_runs": []any{map[string]any{"id": 101, "name": "last run", "status": "queued"}}})
		case "/repos/acme/app/commits/abc/status":
			json.NewEncoder(w).Encode(map[string]any{"statuses": statuses})
		case "/repos/acme/app/commits/abc/status?page=2":
			json.NewEncoder(w).Encode(map[string]any{"statuses": []any{map[string]any{"context": "last status", "state": "failure"}}})
		default:
			t.Errorf("unexpected checks request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	api, err := NewAPI(APIOptions{Base: server.URL, Owner: "acme", Name: "app", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	checks, err := api.Checks(t.Context(), "abc")
	if err != nil || len(checks) != 132 || checks[100].Name != "last run" || checks[131].Name != "last status" {
		t.Fatalf("received %d checks: %v", len(checks), err)
	}
}

func pull(number int, state, branch, at string) map[string]any {
	return map[string]any{"number": number, "html_url": fmt.Sprintf("https://github.com/acme/app/pull/%d", number), "title": "修复问题", "state": state, "merged_at": nil, "draft": false, "head": map[string]any{"sha": "abc", "ref": branch}, "base": map[string]any{"ref": "main"}, "created_at": at, "user": map[string]any{"login": "alice"}, "additions": 4, "deletions": 1, "changed_files": 2, "closed_at": nil}
}
func issueRaw(number, comments int) map[string]any {
	return map[string]any{"number": number, "html_url": fmt.Sprintf("https://github.com/acme/app/issues/%d", number), "title": "任务", "state": "open", "user": nil, "labels": []any{"bug", map[string]any{"name": "中文"}, map[string]any{}}, "comments": comments, "body": nil, "created_at": "2026-10-01", "updated_at": "2026-11-01", "repository_url": "https://api.github.com/repos/acme/app"}
}
func commentRaw(number int) map[string]any {
	return map[string]any{"id": number, "user": map[string]any{"login": "alice"}, "body": strconv.Itoa(number), "created_at": "2026-11-01", "html_url": "https://github.com/comment/" + strconv.Itoa(number)}
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
	case "repository":
		return api.Repository(ctx)
	case "pullRequestFor":
		return api.PullRequestFor(ctx, arg(0))
	case "pullRequest":
		return api.PullRequest(ctx, number(0))
	case "pullRequestPreview":
		return api.Preview(ctx, number(0))
	case "openPullRequest":
		data, _ := json.Marshal(c.Args[0])
		var input sdk.OpenPullRequest
		json.Unmarshal(data, &input)
		return api.OpenPullRequest(ctx, input)
	case "mergePullRequest":
		input := c.Args[1].(map[string]any)
		return api.MergePullRequest(ctx, number(0), input["method"].(string), input["sha"].(string))
	case "checks":
		return api.Checks(ctx, arg(0))
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
	case "checkLog":
		return api.CheckLog(ctx, arg(0))
	}
	return nil, fmt.Errorf("unknown method %s", c.Method)
}
func TestAPIAndMetadataMatchLegacy(t *testing.T) {
	branch := "feature/中文 ~!*"
	current := pull(7, "open", branch, "2026-11-01")
	merged := pull(5, "closed", branch, "2026-11-12")
	merged["merged_at"] = "2026-11-13"
	outsider := issueRaw(3, 0)
	outsider["repository_url"] = "https://api.github.com/repos/other/app"
	pr := issueRaw(4, 0)
	pr["pull_request"] = map[string]any{"url": "pull"}
	comments := []any{}
	for i := 1; i <= 20; i++ {
		comments = append(comments, commentRaw(i))
	}
	flow := scenario{Calls: []call{
		{"repository", nil}, {"pullRequestFor", []any{branch}}, {"pullRequest", []any{7}}, {"pullRequestPreview", []any{7}},
		{"openPullRequest", []any{map[string]any{"branch": branch, "base": "main", "title": "标题", "body": "内容", "draft": true}}},
		{"mergePullRequest", []any{7, map[string]any{"method": "squash", "sha": "abc"}}},
		{"checks", []any{"abc"}}, {"issues", []any{map[string]any{"labels": []string{"bug*~", "中文"}}}},
		{"issues", []any{map[string]any{"search": "bug repo:other/app OR -hello is:pr NOT   中文", "state": "all", "labels": []string{`bug"x`}, "limit": 200}}},
		{"issue", []any{1}}, {"comment", []any{1, "新的评论"}}, {"setIssueState", []any{1, "closed", "not_planned"}},
		{"setIssueState", []any{1, "open"}}, {"checkLog", []any{"status:build"}}, {"checkLog", []any{"9"}}, {"checkLog", []any{"10"}},
	}, Responses: map[string]response{
		"GET /repos/acme/app": {Body: map[string]any{"default_branch": "main", "html_url": "https://github.com/acme/app", "clone_url": "https://github.com/acme/app.git", "private": true}},
		"GET /repos/acme/app/pulls?head=" + hostinghttp.Component("acme:"+branch) + "&state=all&per_page=10": {Body: []any{merged, pull(90, "open", "else", "2026-11-15"), current, pull(1, "open", branch, "2026-01-01")}},
		"GET /repos/acme/app/pulls/7": {Body: current}, "POST /repos/acme/app/pulls": {Body: current}, "PUT /repos/acme/app/pulls/7/merge": {Body: map[string]any{"sha": "merged"}},
		"GET /repos/acme/app/commits/abc/check-runs?per_page=100":                                                                          {Body: map[string]any{"check_runs": []any{map[string]any{"id": 3, "name": "test", "status": "completed", "conclusion": "success", "html_url": nil}, map[string]any{"id": 4, "name": "lint", "status": "waiting", "conclusion": nil, "html_url": "https://ci"}, map[string]any{"id": 5, "name": "build", "status": "in_progress", "conclusion": "neutral", "html_url": nil}}}},
		"GET /repos/acme/app/commits/abc/status":                                                                                           {Body: map[string]any{"statuses": []any{map[string]any{"context": "external", "state": "pending", "target_url": nil}, map[string]any{"context": "deploy", "state": "failure", "target_url": "https://ci"}}}},
		"GET /repos/acme/app/issues?state=open&sort=updated&direction=desc&per_page=30&labels=bug*%7E%2C%E4%B8%AD%E6%96%87":                {Body: []any{issueRaw(1, 21), pr}},
		"GET /search/issues?q=" + hostinghttp.Component(`bug hello 中文 repo:acme/app is:issue label:"bugx"`) + "&sort=updated&per_page=100": {Body: map[string]any{"items": []any{issueRaw(1, 21), outsider, pr}}},
		"GET /repos/acme/app/issues/1": {Body: issueRaw(1, 21)}, "GET /repos/acme/app/issues/1/comments?per_page=20&page=2": {Body: []any{commentRaw(21)}}, "GET /repos/acme/app/issues/1/comments?per_page=20&page=1": {Body: comments},
		"POST /repos/acme/app/issues/1/comments": {Body: commentRaw(22)}, "PATCH /repos/acme/app/issues/1": {Body: issueRaw(1, 22)},
		"GET /repos/acme/app/actions/jobs/9/logs":  {Body: strings.Repeat("甲😀", 8_000)},
		"GET /repos/acme/app/actions/jobs/10/logs": {Body: map[string]any{"message": "Not Found"}, Status: 404},
		"GET /repos/acme/app/check-runs/10":        {Body: map[string]any{"output": map[string]any{"title": "失败", "summary": "原因", "text": nil}}},
	}}
	scenarios := []scenario{flow}
	for _, status := range []int{200, 409, 405, 422, 403} {
		for _, conflict := range []bool{false, true} {
			body := map[string]any{"message": "Branch protection", "errors": []any{map[string]any{"message": "review required"}}}
			if status == 200 {
				body = map[string]any{}
			}
			scenarios = append(scenarios, scenario{Calls: []call{{"mergePullRequest", []any{7, map[string]any{"method": "merge", "sha": "old"}}}}, Responses: map[string]response{"PUT /repos/acme/app/pulls/7/merge": {Body: body, Status: status}, "GET /repos/acme/app/pulls/7": {Body: map[string]any{"mergeable": !conflict, "mergeable_state": map[bool]string{false: "blocked", true: "dirty"}[conflict]}}}})
		}
	}
	scenarios = append(scenarios, scenario{Calls: []call{{"repository", nil}}, Responses: map[string]response{"GET /repos/acme/app": {Status: 422, Body: map[string]any{"message": "validation failed", "errors": []any{"a string error", map[string]any{"message": "specific detail"}}}}}})
	var nativeScenarios []any
	for i, scenario := range scenarios {
		nativeScenarios = append(nativeScenarios, func() any {
			var mu sync.Mutex
			requests := []sent{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				if r.Body != nil {
					json.NewDecoder(r.Body).Decode(&body)
				}
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
					http.Error(w, "unexpected request", 500)
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
			api, err := NewAPI(APIOptions{Base: server.URL, Owner: "acme", Name: "app", Client: server.Client(), Token: func(context.Context) (*string, error) { return &token, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			results := []any{}
			for _, c := range scenario.Calls {
				value, err := invoke(t.Context(), api, c)
				if err != nil {
					failure := map[string]any{"error": err.Error()}
					var status *hostinghttp.Error
					if errors.As(err, &status) {
						failure["status"], failure["reason"] = status.Status, status.Reason
					}
					results = append(results, failure)
				} else {
					results = append(results, map[string]any{"value": value})
				}
			}
			slices.SortStableFunc(requests, func(a, b sent) int { return strings.Compare(a.Method+" "+a.Path, b.Method+" "+b.Path) })
			return map[string]any{"results": results, "requests": requests}
		}())
	}
	repositories := []string{"acme/app", " https://github.com/acme/app.git ", "git@github.com:acme/app", "acme/app.git/", "bad", "acme/with spaces", "ACME/App.GIT", "https://other/acme/app", "acme/app///"}
	nativeRepos := []any{}
	for _, input := range repositories {
		owner, name, err := ParseRepository(input)
		if err != nil {
			nativeRepos = append(nativeRepos, map[string]any{"error": err.Error()})
		} else {
			nativeRepos = append(nativeRepos, map[string]any{"value": map[string]string{"owner": owner, "name": name}})
		}
	}
	links := []map[string]string{{"url": "https://github.com/acme/app/pull/12/files", "web": "https://github.com"}, {"url": "https://ghe.test/git/acme/app/pull/0#comment", "web": "https://ghe.test/git"}, {"url": "https://ghe.test/gitx/acme/app/pull/12", "web": "https://ghe.test/git"}, {"url": "http://github.com/acme/app/pull/12", "web": "https://github.com"}, {"url": "https://github.com/acme/app/pull/1234567890", "web": "https://github.com"}}
	nativeLinks := []any{}
	for _, input := range links {
		address, _ := url.Parse(input["url"])
		pull := ParsePullRequestURL(address, input["web"])
		if pull == nil {
			nativeLinks = append(nativeLinks, nil)
		} else {
			nativeLinks = append(nativeLinks, map[string]any{"owner": pull.Owner, "name": pull.Name, "number": pull.Number})
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
	native := map[string]any{"scenarios": nativeScenarios, "repositories": nativeRepos, "links": nativeLinks, "metadata": metadata}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("native HTTP assertions passed; Bun absent")
	}
	input, _ := json.Marshal(map[string]any{"scenarios": scenarios, "repositories": repositories, "links": links})
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	cmd := legacytest.Command(t, bun, "packages/plugin-github/src/testing/go-api-fixture.ts")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(string(input))
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy: %v\n%s", err, data)
	}
	var actual, want any
	encoded, _ := json.Marshal(native)
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
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

func TestPullRequestConflictParsing(t *testing.T) {
	cases := []struct {
		name           string
		state          string
		mergedAt       any
		mergeable      any
		mergeableState string
		want           bool
	}{
		{name: "open dirty conflict", state: "open", mergeable: false, mergeableState: "dirty", want: true},
		{name: "computing mergeability", state: "open", mergeable: nil, mergeableState: "unknown", want: false},
		{name: "null mergeable with dirty state", state: "open", mergeable: nil, mergeableState: "dirty", want: false},
		{name: "blocked by checks or review", state: "open", mergeable: false, mergeableState: "blocked", want: false},
		{name: "clean open pull request", state: "open", mergeable: true, mergeableState: "clean", want: false},
		{name: "closed pull request", state: "closed", mergeable: false, mergeableState: "dirty", want: false},
		{name: "merged pull request", state: "closed", mergedAt: "2026-11-15", mergeable: false, mergeableState: "dirty", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := pull(7, tc.state, "feature", "2026-11-01")
			payload["merged_at"] = tc.mergedAt
			payload["mergeable"] = tc.mergeable
			payload["mergeable_state"] = tc.mergeableState
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/app/pulls/7" {
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(payload)
			}))
			defer server.Close()
			api, err := NewAPI(APIOptions{Base: server.URL, Owner: "acme", Name: "app", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			info, err := api.PullRequest(t.Context(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if info.Conflicted != tc.want {
				t.Fatalf("PullRequest.Conflicted = %v, want %v", info.Conflicted, tc.want)
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
