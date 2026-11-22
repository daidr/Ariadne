package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

type RecordedCall struct {
	Provider         string             `json:"provider"`
	Model            string             `json:"model"`
	Effort           *string            `json:"effort"`
	Settings         map[string]any     `json:"settings"`
	MaxTokens        int64              `json:"maxTokens,omitempty"`
	ExtraBody        map[string]any     `json:"extraBody,omitempty"`
	Program          bool               `json:"program,omitempty"`
	StatefulSessions bool               `json:"statefulSessions,omitempty"`
	ReasoningScope   sdk.ReasoningScope `json:"reasoningScope,omitempty"`
}
type Call struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Settings map[string]any `json:"settings"`
}
type RequestDetail struct {
	Messages      []transcript.Message `json:"messages"`
	Tools         []sdk.Tool           `json:"tools"`
	Effort        *string              `json:"effort"`
	Call          *Call                `json:"call"`
	NewFrom       int                  `json:"newFrom"`
	SystemChanged bool                 `json:"systemChanged"`
}
type requestReader struct {
	ctx          context.Context
	repo         *core.Repository
	conversation string
}

// ModelInput uses the same attachment, model identity, and title projection as
// the historical request reader.
func ModelInput(ctx context.Context, repo *core.Repository, project string, call *RecordedCall, images bool) (transcript.ChatOptions, func(string) string) {
	reader := requestReader{ctx: ctx, repo: repo}
	options := transcript.ChatOptions{Images: images, ReadText: reader.readText}
	if call != nil {
		options.KeepReasoning = reader.writtenBy(project, call)
		if call.StatefulSessions || call.Program {
			options.PromptUpdates = new(false)
		}
	}
	return options, reader.title
}

func parseCall(content *string) *RecordedCall {
	if content == nil {
		return nil
	}
	var call RecordedCall
	if json.Unmarshal([]byte(*content), &call) != nil {
		return nil
	}
	return &call
}
func (reader requestReader) call(project, hash string) (*RecordedCall, error) {
	content, err := reader.repo.GetPrompt(reader.ctx, project, hash)
	if err != nil {
		return nil, err
	}
	return parseCall(content), nil
}
func (reader requestReader) title(id string) string {
	if thread, err := reader.repo.GetThread(reader.ctx, id); err == nil {
		return thread.Title
	}
	return id
}
func (reader requestReader) readText(id string) (string, bool) {
	file, err := (files.Store{Events: reader.repo.Store}).Read(reader.ctx, id)
	if err != nil {
		return "", false
	}
	data := file.Data
	// Limit decoding to slightly past MaxAttachedChars so multi-MB text attachments
	// still detect truncation cleanly without converting the full blob to string.
	const maxReadBytes = (transcript.MaxAttachedChars + 2) * 4
	if len(data) > maxReadBytes {
		end := maxReadBytes
		for end > 0 && !utf8.RuneStart(data[end]) {
			end--
		}
		data = data[:end]
	}
	value := strings.Map(func(r rune) rune { return r }, string(data))
	return strings.TrimPrefix(value, "\ufeff"), true
}
func (reader requestReader) writtenBy(project string, target *RecordedCall) func(string) bool {
	settings, _ := jsonutil.Encode(target.Settings)
	known := map[string]bool{}
	return func(hash string) bool {
		if same, ok := known[hash]; ok {
			return same
		}
		recorded, err := reader.call(project, hash)
		same := false
		if err == nil && recorded != nil && recorded.Provider == target.Provider && (target.ReasoningScope == sdk.ReasoningScopeProvider || recorded.Model == target.Model) {
			data, _ := jsonutil.Encode(recorded.Settings)
			same = bytes.Equal(data, settings)
		}
		if len(known) < 512 {
			known[hash] = same
		}
		return same
	}
}
func (reader requestReader) history(upTo int64) ([]transcript.Entry, error) {
	entries, err := reader.repo.ListTranscript(reader.ctx, reader.conversation, core.TranscriptOptions{UpTo: &upTo, ModelContext: true})
	if err != nil {
		return nil, err
	}
	out := make([]transcript.Entry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]any(entry))
	}
	return out, nil
}
func eventData(event eventstore.Event) (object, error) {
	var data object
	err := json.Unmarshal(event.Data, &data)
	return data, err
}
func belongsTo(event eventstore.Event, conversation string) bool {
	if event.Type != "turn.step" && event.Type != "conversation.compacted" && event.Type != "research.step" {
		return false
	}
	data, err := eventData(event)
	return err == nil && data["conversationId"] == conversation
}
func (reader requestReader) requestOf(event eventstore.Event) ([]transcript.Message, []sdk.Tool, error) {
	data, err := eventData(event)
	if err != nil {
		return nil, nil, err
	}
	piece := func(hash string) (string, error) {
		content, err := reader.repo.GetPrompt(reader.ctx, event.ProjectID, hash)
		if err != nil {
			return "", err
		}
		if content == nil {
			return "", domainerr.NotFound(hash)
		}
		return *content, nil
	}
	if event.Type == "conversation.compacted" {
		entries, err := reader.history(int64(number(data["sawSeq"])))
		if err != nil {
			return nil, nil, err
		}
		conversation, err := reader.repo.GetConversation(reader.ctx, reader.conversation)
		if err != nil {
			return nil, nil, err
		}
		call, err := reader.call(event.ProjectID, text(data["call"]))
		if err != nil {
			return nil, nil, err
		}
		options := transcript.ChatOptions{ReadText: reader.readText}
		if call != nil {
			options.KeepReasoning = reader.writtenBy(event.ProjectID, call)
		}
		budget := float64(transcript.KeptUserTokens)
		if data["userTokens"] != nil {
			budget = number(data["userTokens"])
		}
		messages := transcript.SummaryRequest(entries, number(data["upTo"]), reader.title, conversation.Role, options, budget)
		return messages, []sdk.Tool{}, reader.ctx.Err()
	}
	if event.Type != "turn.step" && event.Type != "research.step" {
		return nil, nil, domainerr.NotFound(event.ID)
	}
	input := obj(data["input"])
	system, err := piece(text(input["system"]))
	if err != nil {
		return nil, nil, err
	}
	definitions, err := piece(text(input["tools"]))
	if err != nil {
		return nil, nil, err
	}
	tools := []sdk.Tool{}
	if err := json.Unmarshal([]byte(definitions), &tools); err != nil {
		return nil, nil, err
	}
	messages := []transcript.Message{{Role: "system", Content: system}}
	if event.Type == "research.step" {
		previous, err := reader.repo.ResearchSteps(reader.ctx, reader.conversation, text(data["toolCallId"]), event.Seq-1)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, transcript.Message{Role: "user", Content: text(data["question"])})
		for _, step := range previous {
			body, err := eventData(step)
			if err != nil {
				return nil, nil, err
			}
			message := transcript.Message{Role: "assistant", Content: text(body["text"])}
			for _, raw := range list(body["toolCalls"]) {
				call := obj(raw)
				message.ToolCalls = append(message.ToolCalls, sdk.ToolCall{ID: text(call["id"]), Name: text(call["name"]), Arguments: text(call["arguments"])})
			}
			for _, raw := range list(body["reasoning"]) {
				block := obj(raw)
				message.Reasoning = append(message.Reasoning, sdk.Reasoning{Text: text(block["text"]), Signature: text(block["signature"]), Redacted: truth(block["redacted"])})
			}
			messages = append(messages, message)
			for _, raw := range list(body["results"]) {
				result := obj(raw)
				messages = append(messages, transcript.Message{Role: "tool", ToolCallID: text(result["toolCallId"]), Content: text(result["content"]), IsError: truth(result["isError"])})
			}
		}
		if truth(data["finalRequest"]) {
			messages = append(messages, transcript.Message{Role: "user", Content: transcript.FinalResearchPrompt})
		}
		return messages, tools, reader.ctx.Err()
	}
	entries, err := reader.history(int64(number(data["sawSeq"])))
	if err != nil {
		return nil, nil, err
	}
	call, err := reader.call(event.ProjectID, text(input["call"]))
	if err != nil {
		return nil, nil, err
	}
	options, title := ModelInput(reader.ctx, reader.repo, event.ProjectID, call, truth(input["images"]))
	messages = append(messages, transcript.ToMessages(entries, title, options)...)
	return messages, tools, reader.ctx.Err()
}
func same(a, b transcript.Message) bool {
	left, _ := jsonutil.Encode(a)
	right, _ := jsonutil.Encode(b)
	return bytes.Equal(left, right)
}
func RebuildRequest(ctx context.Context, repo *core.Repository, conversation string, seq int64) (RequestDetail, *sdk.Request, error) {
	page, err := repo.Store.Read(ctx, eventstore.ReadOptions{AfterSeq: seq - 1, Limit: 1})
	if err != nil {
		return RequestDetail{}, nil, err
	}
	if len(page) == 0 || page[0].Seq != seq || !belongsTo(page[0], conversation) {
		return RequestDetail{}, nil, domainerr.NotFound(strconv.FormatInt(seq, 10))
	}
	event := page[0]
	data, err := eventData(event)
	if err != nil {
		return RequestDetail{}, nil, err
	}
	reader := requestReader{ctx: ctx, repo: repo, conversation: conversation}
	messages, tools, err := reader.requestOf(event)
	if err != nil {
		return RequestDetail{}, nil, err
	}
	var previous *eventstore.Event
	if event.Type == "research.step" {
		steps, err := repo.ResearchSteps(ctx, conversation, text(data["toolCallId"]), seq-1)
		if err != nil {
			return RequestDetail{}, nil, err
		}
		if len(steps) > 0 {
			previous = &steps[len(steps)-1]
		}
	} else {
		previous, err = repo.LastStepBefore(ctx, conversation, seq)
		if err != nil {
			return RequestDetail{}, nil, err
		}
	}
	before := []transcript.Message{}
	if previous != nil {
		before, _, err = reader.requestOf(*previous)
		if err != nil {
			return RequestDetail{}, nil, err
		}
	}
	detail := RequestDetail{Messages: messages, Tools: tools}
	if len(before) > 0 {
		detail.SystemChanged = !same(before[0], messages[0])
		detail.NewFrom = 1
	}
	for detail.NewFrom < len(before) && detail.NewFrom < len(messages) && same(before[detail.NewFrom], messages[detail.NewFrom]) {
		detail.NewFrom++
	}
	hash := text(obj(data["input"])["call"])
	if event.Type == "conversation.compacted" {
		hash = text(data["call"])
	}
	call, err := reader.call(event.ProjectID, hash)
	if err != nil {
		return RequestDetail{}, nil, err
	}
	if call == nil {
		return detail, nil, nil
	}
	detail.Effort = call.Effort
	detail.Call = &Call{Provider: call.Provider, Model: call.Model, Settings: call.Settings}
	request := &sdk.Request{Model: call.Model, Messages: transcript.WithImageData(messages, func(file transcript.Attachment) (sdk.Image, bool) {
		return sdk.Image{MIME: file.MIME, Data: fmt.Sprintf("<%d bytes of file %s>", file.Size, file.FileID)}, true
	}), Tools: tools, MaxTokens: call.MaxTokens, ExtraBody: call.ExtraBody}
	if call.Effort != nil {
		request.Effort = *call.Effort
	}
	switch event.Type {
	case "turn.step":
		request.Conversation = conversation
	case "research.step":
		request.Conversation = conversation + "/research/" + text(data["toolCallId"])
	case "conversation.compacted":
		request.Cache = new(false)
	}
	return detail, request, nil
}
