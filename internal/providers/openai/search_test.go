package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestModelsAndSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Error("provider key missing")
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"old","created":1},{"id":5},{"id":"new","created":2}]}`)
		case "/v1/responses":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if !strings.Contains(body["input"].(string), "Query: 问题") {
				t.Error("search query missing")
			}
			_, _ = io.WriteString(w, `{"output":[{"type":"web_search_call","action":{"sources":[{"url":"https://a.example"},{"url":"https://b.example"}]}},{"type":"message","content":[{"type":"output_text","text":"答案","annotations":[{"type":"url_citation","url":"https://b.example","title":"B"}]}]}],"usage":{"input_tokens":50,"output_tokens":10,"input_tokens_details":{"cached_tokens":5}}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	provider := Provider{Client: server.Client()}
	settings := Settings{BaseURL: server.URL + "/v1/", APIKey: "key"}
	models, err := provider.Models(t.Context(), settings, nil)
	if err != nil || !reflect.DeepEqual(models, []string{"new", "old"}) {
		t.Fatalf("models: %v, %v", models, err)
	}
	answer, err := provider.Search(t.Context(), settings, sdk.SearchRequest{Model: "m", Query: "问题", Limit: 5})
	if err != nil || answer.Text != "答案" || len(answer.Sources) != 2 || answer.Sources[0].Title != "B" || answer.Sources[1].URL != "https://a.example" || answer.Usage.Cached != 5 {
		t.Fatalf("search: %+v, %v", answer, err)
	}
	if !SearchAvailable(Settings{BaseURL: "https://api.openai.com/v1"}) || SearchAvailable(settings) || SearchAvailable(Settings{BaseURL: "https://api.openai.com.attacker.invalid/v1"}) {
		t.Fatal("search availability crossed provider host")
	}
}

func TestSearchRefusalAndIncompleteAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	_, err := (Provider{Client: server.Client()}).Search(t.Context(), Settings{BaseURL: server.URL}, sdk.SearchRequest{})
	var refused *SearchRefused
	if !errors.As(err, &refused) || refused.Status != 403 {
		t.Fatalf("refusal: %v", err)
	}
	if _, err := ReadSearch([]byte(`{"status":"incomplete","output":[]}`), 5); err == nil {
		t.Fatal("empty incomplete search passed")
	}
}
