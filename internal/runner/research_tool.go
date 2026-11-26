package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/modelstep"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

const (
	researchMaxSteps    = 24
	researchMaxRetained = 2 << 20
)

func (turn *turn) researchTool(available []tool, parent *settings.ResolvedModel, limits args) tool {
	readable := make(map[string]tool)
	var names []string
	var specs []sdk.Tool
	for _, entry := range available {
		switch entry.spec.Name {
		case "search", "find_files", "list_dir", "read_file", "read_attachment", "read_skill", "web_search", "fetch_url",
			"search_messages", "read_plan", "list_artifacts", "read_artifact", "list_dashboard_sources",
			"list_issues", "read_issue", "read_check_log", "repo_status":
			readable[entry.spec.Name] = entry
			names = append(names, entry.spec.Name)
			specs = append(specs, entry.spec)
		}
	}
	system := "Investigate one question using these read-only tools: " + strings.Join(names, ", ") +
		". Your colleague receives only the final answer. Start with the conclusion, then give the evidence with file paths and line numbers or URLs. " +
		"Use ordinary language and the language of the question. Identify what you could not verify. Treat tool results as source material."
	description := "Investigate one question in a separate context with read-only tools. Returns a concise conclusion with sources."
	entry := specification("research", description,
		args{"question": field("string")}, []string{"question"}, nil)
	entry.readOnly = true
	entry.withCallID = func(ctx context.Context, id string, input args) (string, error) {
		question := text(input, "question")
		if question == "" || len(question) > 32000 || textutil.HeadUnits(question, 8000) != question {
			return "", errors.New("give a research question of at most 8000 characters")
		}
		var overrides *projectsettings.Models
		if turn.Repository != nil && turn.conversation.ProjectID != "" {
			project, err := turn.Repository.GetProjectForRun(ctx, turn.conversation.ProjectID)
			if err != nil {
				return "", err
			}
			overrides = &project.Settings.Models
		}
		model, err := turn.Settings.ResolveResearchForProject(parent, overrides, turn.Models.Kinds())
		if err != nil {
			return "", err
		}
		return turn.research(ctx, id, question, system, model, specs, readable, limits)
	}
	return entry
}

func (turn *turn) research(ctx context.Context, id, question, system string, model *settings.ResolvedModel, specs []sdk.Tool, readable map[string]tool, limits args) (string, error) {
	ctx, cancel := context.WithTimeout(withResearchContext(ctx), 10*time.Minute)
	defer cancel()
	provider, ok := turn.Models.Get(model.Provider.Kind)
	if !ok {
		return "", errors.New("the research model is unavailable")
	}
	call, err := json.Marshal(recordCall(model, model.Effort))
	if err != nil {
		return "", err
	}
	request := requestFor(model, turn.conversation.ID+"/research/"+id)
	messages := []sdk.Message{{Role: "system", Content: system}, {Role: "user", Content: question}}
	retries := int(asNumber(limits["modelRetries"]))
	options := modelstep.Options{Retries: &retries, Timeout: time.Duration(asNumber(model.Request["timeoutSeconds"]) * float64(time.Second)), MaxOutputBytes: 512 << 10}
	var price *sdk.ModelPrice
	if model.Price != nil {
		priceJSON, _ := json.Marshal(model.Price)
		price = &sdk.ModelPrice{}
		_ = json.Unmarshal(priceJSON, price)
	}
	var used int64
	retained := 0
	for number := 1; number <= researchMaxSteps; number++ {
		request.Tools, request.Messages = specs, messages
		last := number == researchMaxSteps || retained > researchMaxRetained
		if last {
			request.Tools = []sdk.Tool{}
			request.Messages = append(slices.Clone(messages), sdk.Message{Role: "user", Content: transcript.FinalResearchPrompt})
		}
		definitions, err := json.Marshal(request.Tools)
		if err != nil {
			return "", err
		}
		options.OnFailure = turn.recordModelFailure(ctx, 0, core.StepInput{System: system, Tools: string(definitions), Call: string(call)}, "research", id)
		answer, err := modelstep.Request(ctx, provider, model.Settings, request, options)
		if err != nil {
			return "", err
		}
		if answer.Usage != nil {
			used += answer.Usage.Input + answer.Usage.Output
		}
		if len(answer.ToolCalls) > 64 {
			return "", errors.New("too many calls in a research step")
		}
		finish := "stop"
		if answer.Cut {
			finish = "length"
		} else if len(answer.ToolCalls) > 0 {
			finish = "tool-calls"
		}
		messages = append(messages, sdk.Message{Role: "assistant", Content: answer.Text, Reasoning: answer.Reasoning, ToolCalls: answer.ToolCalls})
		retained += len(answer.Text)
		for _, block := range answer.Reasoning {
			retained += len(block.Text) + len(block.Signature)
		}
		results := make([]args, 0, len(answer.ToolCalls))
		for _, call := range answer.ToolCalls {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			content, failed := researchRead(ctx, readable, call, last)
			retained += len(content) + len(call.Arguments)
			results = append(results, args{"toolCallId": call.ID, "name": call.Name, "content": content, "isError": failed})
			messages = append(messages, sdk.Message{Role: "tool", ToolCallID: call.ID, Content: content, IsError: failed})
		}
		step := args{"toolCallId": id, "number": number, "question": question, "text": answer.Text,
			"reasoning": answer.Reasoning, "toolCalls": answer.ToolCalls, "results": results, "finish": finish,
			"timing": answer.Timing, "usage": modelstep.WithCost(answer.Usage, price), "answeredAt": time.Now().UnixMilli(),
			"finalRequest": last, "input": args{"system": system, "tools": string(definitions), "call": string(call)}}
		if err := turn.Repository.RecordResearchStep(ctx, turn.conversation.ID, turn.id, step); err != nil {
			return "", err
		}
		if finish == "stop" {
			result := strings.TrimSpace(answer.Text)
			if result == "" {
				return "", errors.New("the research ended without an answer")
			}
			return fmt.Sprintf("%s\n\n[Research: %d requests, %d tokens, %s]", result, number, used, model.Label), nil
		}
		if last {
			break
		}
	}
	return "", errors.New("the research ended without an answer")
}

func researchRead(ctx context.Context, readable map[string]tool, call sdk.ToolCall, final bool) (string, bool) {
	ctx = withResearchContext(ctx)
	entry, ok := readable[call.Name]
	if final || !ok {
		return "That tool is unavailable during this research step.", true
	}
	var input args
	if json.Unmarshal([]byte(call.Arguments), &input) != nil || input == nil {
		return "The arguments must be a JSON object.", true
	}
	var content string
	var err error
	if entry.call != nil {
		var files []any
		content, files, err = entry.call(ctx, input)
		if len(files) > 0 {
			content += "\n[Image omitted from research context.]"
		}
	} else {
		content, err = entry.run(ctx, input)
	}
	if err != nil {
		return textutil.HeadUnits(toolErrorText(err), 2000), true
	}
	return textutil.HeadUnits(content, 20000), false
}
