package searchservice

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSearchRequestAndResults(t *testing.T) {
	for _, kind := range []string{"tavily", "deepseek"} {
		t.Run(kind, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.URL.Path != "/"+kind+"/search" || r.Header.Get("Authorization") != "Bearer private" {
					t.Error("wrong request", r.URL.Path, r.Header)
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				want := map[string]any{"query": "中文😀", "max_results": float64(70)}
				if kind == "deepseek" {
					want = map[string]any{"query": "中文😀", "count": float64(50), "need_content": false}
				}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("body %v want %v", body, want)
				}
				io.WriteString(w, `{"results":[{"url":"https://example.test","title":"Result","content":"Tavily","summary":"DeepSeek summary","snippet":"fallback"},null,{},1,{"url":null},{"url":"","title":1,"summary":null,"snippet":"fallback"}]}`)
			}))
			defer server.Close()
			hits, err := (Provider{Kind: kind, APIKey: "private", BaseURL: server.URL + "/" + kind + "/"}).Search(t.Context(), "中文😀", 70)
			if err != nil {
				t.Fatal(err)
			}
			if !called || len(hits) != 2 || hits[0].Title != "Result" || hits[1].Title != "" {
				t.Fatal(hits)
			}
			want := "Tavily"
			if kind == "deepseek" {
				want = "DeepSeek summary"
				if hits[1].Snippet != "fallback" {
					t.Fatal(hits)
				}
			}
			if hits[0].Snippet != want {
				t.Fatal(hits)
			}
		})
	}
}
func TestSearchRedirectsErrorsAndShape(t *testing.T) {
	redirected := false
	status := 302
	body := `{"results":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected = true
		}
		if status == 302 {
			w.Header().Set("Location", "/target")
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer server.Close()
	provider := Provider{APIKey: "private", BaseURL: server.URL}
	for _, code := range []int{302, 401, 500} {
		status = code
		_, err := provider.Search(t.Context(), "query", 1)
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.Status != code || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
	if redirected {
		t.Fatal("search followed a redirect")
	}
	status = 200
	for _, raw := range []string{`null`, `{}`, `{"results":null}`, `{"results":{}}`, `not JSON`} {
		body = raw
		if _, err := provider.Search(t.Context(), "query", 1); err == nil {
			t.Fatal("invalid results accepted", raw)
		}
	}
	provider.Kind = "deepseek"
	if _, err := provider.Search(t.Context(), strings.Repeat("😀", 129), 1); err == nil {
		t.Fatal("oversized DeepSeek query accepted")
	}
}
