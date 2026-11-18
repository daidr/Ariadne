// recordprovider saves model protocol samples for offline replay.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/config"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

const maxExchange = 16 << 20
const fixtureDirectory = "testdata/providers"

type wireRequest struct {
	Path string          `json:"path"`
	Body json.RawMessage `json:"body"`
}

type wireResponse struct {
	Status int    `json:"status"`
	Type   string `json:"contentType"`
	Retry  string `json:"retryAfter,omitempty"`
	Body   string `json:"body"`
}

type fixture struct {
	Provider string         `json:"provider"`
	Set      string         `json:"set"`
	Scenario string         `json:"scenario"`
	Time     string         `json:"recordedAt"`
	Settings map[string]any `json:"settings"`
	Request  sdk.Request    `json:"request"`
	Sent     wireRequest    `json:"sent"`
	Reply    wireResponse   `json:"reply"`
	Parts    []sdk.Part     `json:"parts"`
}

type capture struct {
	sent  wireRequest
	reply wireResponse
}

func (capture *capture) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxExchange+1))
	if err != nil || len(body) > maxExchange {
		return nil, errors.New("could not capture the request within the sample limit")
	}
	req.Body.Close()
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	capture.sent = wireRequest{Path: req.URL.EscapedPath(), Body: body}
	response, err := http.DefaultTransport.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExchange+1))
	response.Body.Close()
	if err != nil || len(data) > maxExchange {
		return nil, errors.New("could not capture the response within the sample limit")
	}
	capture.reply = wireResponse{Status: response.StatusCode, Type: response.Header.Get("Content-Type"), Retry: response.Header.Get("Retry-After"), Body: string(data)}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}

type scenario struct {
	name, question, depends string
	finish                  []string
	matchTool               bool
}

var scenarios = []scenario{
	{name: "text", question: "用一句话回答：一年有几个季节？", finish: []string{"stop"}},
	{name: "tool-call", question: "杭州现在天气怎么样？用 get_weather 查。", finish: []string{"tool-calls"}, matchTool: true},
	{name: "tool-result", depends: "tool-call", finish: []string{"stop"}},
	{name: "effort", question: "17 × 23 等于多少？只回答数字。", finish: []string{"stop"}},
	{name: "cut-off", question: "从 1 数到 300，用逗号隔开。", finish: []string{"length"}},
	{name: "cut-off-continue", depends: "cut-off", finish: []string{"stop", "length"}},
	{name: "bad-model", question: "hi", finish: []string{"error"}},
}

var weather = sdk.Tool{Name: "get_weather", Description: "The weather in a city.", Parameters: map[string]any{
	"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"},
}}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func arguments(raw []string) (model, set string, only []string, err error) {
	var positional []string
	for i := 0; i < len(raw); i++ {
		value := raw[i]
		if flag, given := strings.CutPrefix(value, "--only="); given {
			only = strings.Split(flag, ",")
		} else if value == "--only" && i+1 < len(raw) {
			i++
			only = strings.Split(raw[i], ",")
		} else if strings.HasPrefix(value, "-") {
			return "", "", nil, errors.New("usage: recordprovider MODEL [SET] [--only text,tool-call]")
		} else {
			positional = append(positional, value)
		}
	}
	if len(positional) > 2 {
		return "", "", nil, errors.New("too many arguments")
	}
	if len(positional) > 0 {
		model, set = positional[0], positional[0]
	}
	if len(positional) > 1 {
		set = positional[1]
	}
	for i, name := range only {
		name = strings.TrimSpace(name)
		if !slices.ContainsFunc(scenarios, func(scene scenario) bool { return scene.name == name }) {
			return "", "", nil, fmt.Errorf("unknown scenario %q", name)
		}
		only[i] = name
	}
	if set != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`).MatchString(set) {
		return "", "", nil, errors.New("set must be a short directory name. Supply it explicitly if the model ID contains punctuation")
	}
	return model, set, only, nil
}

func run(ctx context.Context, raw []string) error {
	id, set, only, err := arguments(raw)
	if err != nil {
		return err
	}
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	user, err := settings.Open(filepath.Join(cfg.Home, "config.json"))
	if err != nil {
		return err
	}
	modelsConfig, _ := user.Current()["models"].(map[string]any)
	modelList, _ := modelsConfig["models"].([]any)
	found := false
	for _, entry := range modelList {
		model, _ := entry.(map[string]any)
		if model["id"] == id {
			found = true
		}
		if id == "" {
			fmt.Printf("%v  (%v)\n", model["id"], model["model"])
		}
	}
	if id == "" {
		if len(modelList) == 0 {
			return errors.New("add a model in Ariadne settings first")
		}
		return nil
	}
	if !found {
		return fmt.Errorf("unknown model ID %q", id)
	}
	recorder := &capture{}
	builtins, err := models.NewBuiltins(models.BuiltinOptions{Context: ctx, DataDir: filepath.Join(cfg.Home, "plugins"), Client: &http.Client{Transport: recorder}})
	if err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = builtins.Close(stop)
	}()
	resolved, err := user.ResolveModel("thread", id, builtins.Registry.Kinds())
	if err != nil {
		return err
	}
	if resolved == nil || resolved.Provider.Kind != "openai-compatible" && resolved.Provider.Kind != "anthropic" {
		return errors.New("choose an OpenAI-compatible or Anthropic model")
	}
	provider, ok := builtins.Registry.Get(resolved.Provider.Kind)
	if !ok {
		return errors.New("the selected provider is unavailable")
	}
	const base = "tools/recordprovider/" + fixtureDirectory
	dir := filepath.Join(base, set)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	previous := map[string]fixture{}
	for _, scene := range scenarios {
		if len(only) > 0 && !slices.Contains(only, scene.name) && !slices.ContainsFunc(scenarios, func(follow scenario) bool {
			return slices.Contains(only, follow.name) && follow.depends == scene.name
		}) {
			continue
		}
		*recorder = capture{}
		request := requestFor(resolved, scene, previous[scene.depends])
		call, cancel := context.WithTimeout(ctx, 3*time.Minute)
		parts := observed(provider.Stream(call, resolved.Settings, request))
		cancel()
		if recorder.reply.Status == 0 {
			return fmt.Errorf("%s returned no HTTP response", scene.name)
		}
		sample := fixture{Provider: resolved.Provider.Kind, Set: set, Scenario: scene.name, Time: time.Now().UTC().Format(time.RFC3339),
			Settings: map[string]any{}, Request: request, Sent: recorder.sent, Reply: recorder.reply, Parts: parts}
		previous[scene.name] = sample
		sample.Request.Headers = nil
		var secrets []string
		for key, value := range resolved.Settings {
			if field, ok := resolved.Provider.Settings.Properties[key]; ok && field.Secret {
				if value, ok := value.(string); ok && value != "" {
					secrets = append(secrets, value)
				}
			} else {
				sample.Settings[key] = value
			}
		}
		for _, value := range request.Headers {
			if value != "" {
				secrets = append(secrets, value)
				if token, ok := strings.CutPrefix(value, "Bearer "); ok && token != "" {
					secrets = append(secrets, token)
				}
			}
		}
		payload, err := json.MarshalIndent(sample, "", "  ")
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if bytes.Contains(payload, []byte(secret)) {
				return fmt.Errorf("%s included a credential in a field or response. The sample was not saved", scene.name)
			}
			quoted, _ := json.Marshal(secret)
			if bytes.Contains(payload, quoted[1:len(quoted)-1]) {
				return fmt.Errorf("%s included an encoded credential. The sample was not saved", scene.name)
			}
		}
		destination := filepath.Join(dir, scene.name+".json")
		if err := os.WriteFile(destination, append(payload, '\n'), 0o644); err != nil {
			return err
		}
		problem := check(scene, parts)
		if problem != "" {
			fmt.Printf("%s HTTP %d: %s (%s)\n", scene.name, sample.Reply.Status, problem, destination)
		} else {
			fmt.Printf("%s HTTP %d: passed (%s)\n", scene.name, sample.Reply.Status, destination)
		}
	}
	return ctx.Err()
}

func requestFor(model *settings.ResolvedModel, scene scenario, parent fixture) sdk.Request {
	req := sdk.Request{Model: model.Model, Tools: []sdk.Tool{}, Messages: []sdk.Message{
		{Role: "system", Content: "You are a test of a model service. Follow the instruction exactly."},
		{Role: "user", Content: scene.question},
	}}
	req.Headers = map[string]string{}
	if headers, ok := model.Request["headers"].(map[string]any); ok {
		for key, value := range headers {
			if value, ok := value.(string); ok {
				req.Headers[key] = value
			}
		}
	}
	req.ExtraBody, _ = model.Request["extraBody"].(map[string]any)
	switch scene.name {
	case "tool-call":
		req.Tools = append(req.Tools, weather)
	case "tool-result":
		answer := assistant(parent.Parts)
		call := "missing"
		if len(answer.ToolCalls) > 0 {
			call = answer.ToolCalls[0].ID
		}
		req.Messages = append(parent.Request.Messages, answer, sdk.Message{Role: "tool", ToolCallID: call, Content: `{"city":"杭州","weather":"小雨","celsius":18}`})
		req.Tools = append(req.Tools, weather)
	case "effort":
		req.Effort = "high"
	case "cut-off":
		req.MaxTokens = 64
	case "cut-off-continue":
		req.MaxTokens = 64
		req.Messages = append(parent.Request.Messages, assistant(parent.Parts), sdk.Message{Role: "user", Content: "Continue from where you stopped."})
	case "bad-model":
		req.Model, req.Messages = "ariadne-no-such-model", req.Messages[1:]
	}
	return req
}

func observed(stream func(func(sdk.Part) bool)) []sdk.Part {
	parts := []sdk.Part{}
	for part := range stream {
		if part.Type != "alive" {
			part.StartedAt = 0
			parts = append(parts, part)
		}
	}
	return parts
}

func assistant(parts []sdk.Part) sdk.Message {
	out := sdk.Message{Role: "assistant"}
	for _, part := range parts {
		switch part.Type {
		case "text":
			out.Content += part.Text
		case "reasoning":
			out.Reasoning = append(out.Reasoning, sdk.Reasoning{Text: part.Text, Signature: part.Signature, Redacted: part.Redacted})
		case "tool-call":
			out.ToolCalls = append(out.ToolCalls, sdk.ToolCall{ID: part.ID, Name: part.Name, Arguments: part.Arguments})
		}
	}
	return out
}

func check(scene scenario, parts []sdk.Part) string {
	finish, usage, tool := "", false, false
	for _, part := range parts {
		switch part.Type {
		case "finish":
			finish = part.Reason
			if scene.name == "bad-model" && part.Retryable {
				return "bad model marked retryable"
			}
		case "usage":
			usage = true
		case "tool-call":
			tool = true
			if !json.Valid([]byte(part.Arguments)) {
				return "invalid tool arguments"
			}
		}
	}
	if !slices.Contains(scene.finish, finish) {
		return "unexpected finish " + finish
	}
	if scene.matchTool && !tool {
		return "missing tool call"
	}
	if finish != "error" && !usage {
		return "missing usage"
	}
	return ""
}
