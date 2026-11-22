package main

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/sdk"
)

type replayTransport func(*http.Request) (*http.Response, error)

func (replay replayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return replay(request)
}

func TestRecordedProviderSamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(fixtureDirectory, "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var sample fixture
			if err := json.Unmarshal(data, &sample); err != nil {
				t.Fatal(err)
			}
			parts := replaySample(t, sample)
			got, err := json.Marshal(parts)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(sample.Parts)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("stream changed\ngot  %s\nwant %s", got, want)
			}
		})
	}
}

func TestReplayProviderResponse(t *testing.T) {
	sample := fixture{
		Provider: "openai-compatible", Settings: map[string]any{"baseUrl": "https://example.invalid/v1"},
		Request: sdk.Request{Model: "mock", Messages: []sdk.Message{{Role: "user", Content: "hi"}}},
		Sent:    wireRequest{Path: "/v1/chat/completions"},
		Reply: wireResponse{Status: http.StatusOK, Type: "text/event-stream", Body: `data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}

data: [DONE]

`},
	}
	parts := replaySample(t, sample)
	if answer := assistant(parts); answer.Content != "hello" {
		t.Fatalf("answer = %#v", answer)
	}
	if problem := check(scenarios[0], parts); problem != "" {
		t.Fatal(problem)
	}
}

func replaySample(t *testing.T, sample fixture) []sdk.Part {
	t.Helper()
	called := false
	client := &http.Client{Transport: replayTransport(func(request *http.Request) (*http.Response, error) {
		called = true
		defer request.Body.Close()
		if path := request.URL.EscapedPath(); path != sample.Sent.Path {
			t.Errorf("request path = %q, want %q", path, sample.Sent.Path)
		}
		if len(sample.Sent.Body) != 0 {
			var got, want any
			if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal(sample.Sent.Body, &want); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("request body changed\ngot  %v\nwant %v", got, want)
			}
		}
		header := http.Header{"Content-Type": {sample.Reply.Type}}
		if sample.Reply.Retry != "" {
			header.Set("Retry-After", sample.Reply.Retry)
		}
		return &http.Response{StatusCode: sample.Reply.Status, Header: header,
			Body: io.NopCloser(strings.NewReader(sample.Reply.Body)), Request: request}, nil
	})}
	builtins, err := models.NewBuiltins(models.BuiltinOptions{Context: t.Context(), DataDir: t.TempDir(), Client: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = builtins.Close(context.Background()) })
	for _, provider := range models.HTTPBuiltinProviders(models.HTTPOptions{Client: client}) {
		remove, err := builtins.Registry.Register(provider)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(remove)
	}
	provider, ok := builtins.Registry.Get(sample.Provider)
	if !ok {
		t.Fatalf("unknown provider %q", sample.Provider)
	}
	settings := map[string]any{"apiKey": "fixture-key"}
	maps.Copy(settings, sample.Settings)
	parts := observed(provider.Stream(t.Context(), settings, sample.Request))
	if !called {
		t.Fatalf("provider did not send the recorded request: %#v", parts)
	}
	return parts
}
