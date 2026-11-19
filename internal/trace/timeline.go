// Package trace rebuilds request and tool timelines from durable events. It does
// not keep a separate history cache or retain completed conversations.
package trace

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
)

type object = map[string]any
type Timeline struct {
	Turns []object `json:"turns"`
	More  bool     `json:"more"`
}
type Options struct {
	Before, From        *int64
	Turns               *int
	MaxBytes, MaxEvents int64
}

func obj(raw any) object {
	value, _ := raw.(map[string]any)
	if value == nil {
		return object{}
	}
	return value
}
func list(raw any) []any {
	value, _ := raw.([]any)
	if value == nil {
		return []any{}
	}
	return value
}
func text(raw any) string { value, _ := raw.(string); return value }
func number(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	}
	return 0
}
func truth(raw any) bool { return raw != nil && raw != false && raw != "" && raw != float64(0) }
func optional(raw object, key string, fallback any) any {
	if value, ok := raw[key]; ok && value != nil {
		return value
	}
	return fallback
}

type recordedCall struct {
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
	Effort   any     `json:"effort"`
}

func reasoning(raw any, allowRedacted bool) []object {
	out := []object{}
	for _, raw := range list(raw) {
		block := obj(raw)
		redacted := allowRedacted && truth(block["redacted"])
		content := text(block["text"])
		if redacted {
			content = ""
		}
		out = append(out, object{"text": content, "redacted": redacted})
	}
	return out
}
func requestBase(seq int64, number int64, call *recordedCall, timing object, finishedAt any, usage any) object {
	var provider, model, effort any
	if call != nil {
		provider, model, effort = call.Provider, call.Model, call.Effort
	}
	return object{"kind": "request", "seq": seq, "number": number, "provider": provider, "model": model, "effort": effort, "startedAt": timing["startedAt"], "firstOutputAt": timing["firstOutputAt"], "finishedAt": finishedAt, "usage": usage}
}
func Build(ctx context.Context, repo *core.Repository, conversation string, options Options) (Timeline, error) {
	turns := 10
	if options.Turns != nil {
		turns = *options.Turns
	}
	bytes, events := int64(64<<20), int64(100000)
	if options.MaxBytes > 0 {
		bytes = min(bytes, options.MaxBytes)
	}
	if options.MaxEvents > 0 {
		events = min(events, options.MaxEvents)
	}
	var page core.TurnEventPage
	for {
		var err error
		page, err = repo.ListTurnEvents(ctx, conversation, core.TurnEventOptions{Before: options.Before, From: options.From, Turns: &turns, MaxBytes: bytes, MaxEvents: events})
		if err == nil {
			break
		}
		var limit *domainerr.Error
		if turns <= 1 || !errors.As(err, &limit) || limit.Key != "error.invalidBody" || limit.Params["reason"] != core.TraceLimitReason {
			return Timeline{}, err
		}
		turns = max(1, turns/2)
	}
	var err error
	output := Timeline{Turns: []object{}, More: page.More}
	var ordinal int64
	if len(page.Events) > 0 {
		ordinal, err = repo.CountRequests(ctx, conversation, page.Events[0].Seq)
		if err != nil {
			return output, err
		}
	}
	calls := map[string]*recordedCall{}
	callOf := func(project, hash string) (*recordedCall, error) {
		if call, ok := calls[hash]; ok {
			return call, nil
		}
		content, err := repo.GetPrompt(ctx, project, hash)
		if err != nil {
			return nil, err
		}
		var call *recordedCall
		if content != nil {
			var decoded recordedCall
			if json.Unmarshal([]byte(*content), &decoded) == nil {
				call = &decoded
			}
		}
		calls[hash] = call
		return call, nil
	}
	var turn object
	toolsFrom := float64(0)
	running := map[string]object{}
	appendItem := func(item object) { turn["items"] = append(turn["items"].([]object), item) }
	addUsage := func(raw any) {
		usage := obj(raw)
		if len(usage) == 0 {
			return
		}
		sum := turn["usage"].(map[string]any)
		sum["input"] = number(sum["input"]) + number(usage["input"])
		sum["output"] = number(sum["output"]) + number(usage["output"])
		for _, key := range []string{"cached", "cacheWrite", "reasoning"} {
			if n := number(usage[key]); n != 0 {
				sum[key] = number(sum[key]) + n
			}
		}
	}
	for _, event := range page.Events {
		if err := ctx.Err(); err != nil {
			return output, err
		}
		var data object
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return output, err
		}
		if event.Type == "turn.started" {
			turn = object{"turnId": data["turnId"], "seq": event.Seq, "model": data["model"], "startedAt": event.At, "finishedAt": nil, "outcome": nil, "error": nil, "usage": object{"input": float64(0), "output": float64(0)}, "items": []object{}}
			output.Turns = append(output.Turns, turn)
			clear(running)
			continue
		}
		if turn == nil {
			continue
		}
		switch event.Type {
		case "turn.step":
			addUsage(data["usage"])
			call, err := callOf(event.ProjectID, text(obj(data["input"])["call"]))
			if err != nil {
				return output, err
			}
			toolsFrom = float64(event.At)
			toolCalls := []object{}
			for _, raw := range list(data["toolCalls"]) {
				entry := maps.Clone(obj(raw))
				entry["startedAt"], entry["result"] = event.At, nil
				running[text(entry["id"])] = entry
				toolCalls = append(toolCalls, entry)
			}
			ordinal++
			request := requestBase(event.Seq, ordinal, call, obj(data["timing"]), event.At, data["usage"])
			request["purpose"], request["finish"], request["text"], request["reasoning"], request["toolCalls"] = "step", data["finish"], data["text"], reasoning(data["reasoning"], true), toolCalls
			appendItem(request)
		case "research.step":
			addUsage(data["usage"])
			parent := running[text(data["toolCallId"])]
			if parent == nil {
				continue
			}
			toolCalls := []object{}
			for _, raw := range list(data["toolCalls"]) {
				tool := maps.Clone(obj(raw))
				tool["startedAt"], tool["result"] = data["answeredAt"], nil
				for _, raw := range list(data["results"]) {
					result := obj(raw)
					if result["toolCallId"] == tool["id"] {
						tool["result"] = object{"content": result["content"], "isError": result["isError"], "finishedAt": event.At, "files": []any{}}
						break
					}
				}
				toolCalls = append(toolCalls, tool)
			}
			call, err := callOf(event.ProjectID, text(obj(data["input"])["call"]))
			if err != nil {
				return output, err
			}
			request := requestBase(event.Seq, int64(number(data["number"])), call, obj(data["timing"]), data["answeredAt"], data["usage"])
			request["purpose"], request["finish"], request["text"], request["reasoning"], request["toolCalls"] = "research", data["finish"], data["text"], reasoning(data["reasoning"], true), toolCalls
			research, _ := parent["research"].([]object)
			parent["research"] = append(research, request)
		case "tool.finished":
			timing := obj(data["timing"])
			id := text(data["toolCallId"])
			if tool := running[id]; tool != nil {
				tool["startedAt"] = optional(timing, "startedAt", toolsFrom)
				tool["result"] = object{"content": data["content"], "isError": data["isError"], "finishedAt": optional(timing, "finishedAt", event.At), "files": optional(data, "files", []any{})}
				delete(running, id)
			}
			toolsFrom = math.Max(toolsFrom, number(optional(timing, "finishedAt", event.At)))
		case "conversation.model_retry":
			appendItem(object{"kind": "retry", "at": event.At, "attempt": data["attempt"], "delayMs": data["delayMs"], "detail": data["detail"]})
		case "conversation.model_fallback":
			appendItem(object{"kind": "fallback", "at": event.At, "from": data["from"], "to": data["to"], "detail": data["detail"]})
		case "conversation.compacted":
			addUsage(data["usage"])
			call, err := callOf(event.ProjectID, text(data["call"]))
			if err != nil {
				return output, err
			}
			ordinal++
			request := requestBase(event.Seq, ordinal, call, obj(data["timing"]), event.At, data["usage"])
			request["purpose"], request["finish"], request["text"], request["reasoning"], request["toolCalls"] = "summary", "stop", data["summary"], reasoning(data["reasoning"], false), []object{}
			appendItem(request)
		case "conversation.results_cleared":
			appendItem(object{"kind": "results-cleared", "at": event.At})
		case "conversation.compaction_failed":
			appendItem(object{"kind": "compaction-failed", "at": event.At, "detail": data["detail"]})
		case "conversation.instructions":
			appendItem(object{"kind": "instructions", "at": event.At, "path": data["path"], "changed": data["changed"], "removed": data["content"] == nil, "uncommitted": optional(data, "uncommitted", false)})
		case "turn.finished":
			turn["finishedAt"], turn["outcome"], turn["error"] = event.At, data["outcome"], data["error"]
			turn = nil
		}
	}
	return output, nil
}
