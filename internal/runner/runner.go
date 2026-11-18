// Package runner executes native turns and durably records the request, response
// and results in the order seen by the model.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/browser"
	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/modelstep"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/skills"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/trace"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

type Runner struct {
	Repository    *core.Repository
	Settings      *settings.Store
	Models        *models.Registry
	Drafts        *realtime.ReplyDrafts
	Live          *realtime.LiveCommands
	Workspaces    *workspaces.Manager
	MCP           *mcp.Hub
	SkillsDirs    []string
	SearchPlugins func() []sdk.SearchProvider
	HTTPClient    *http.Client
	Rooms         *collab.Rooms
	Dashboards    *dashboard.Registry
	Browser       *browser.Pages
	Sources       *sources.Registry
	Home          string
}

type turn struct {
	*Runner
	conversation      core.ConversationView
	id                string
	draft             *realtime.DraftHandle
	draftCall         *int
	draftTool         string
	artifactDrafts    map[int]*collab.Draft
	skills            *skills.Set
	external          []tool
	instructionsDirty bool
	handledBefore     int64
	userSpoke         bool
}

func (runner *Runner) Resolve(ctx context.Context, conversation core.ConversationView) (*settings.ResolvedModel, error) {
	kinds := runner.Models.Kinds()
	if conversation.Role == "thread" {
		thread, err := runner.Repository.GetThread(ctx, conversation.ID)
		if err != nil {
			return nil, err
		}
		model, _ := thread.Model.(string)
		return runner.Settings.ResolveModel("thread", model, kinds)
	}
	triage, err := runner.Repository.PendingIsTriage(ctx, conversation.ID)
	if err != nil {
		return nil, err
	}
	return runner.Settings.ResolveCoordinator(triage, float64(conversation.ContextTokens), kinds)
}

func (runner *Runner) Run(ctx context.Context, conversation core.ConversationView) error {
	model, err := runner.Resolve(ctx, conversation)
	if err != nil {
		return err
	}
	if model == nil {
		return domainerr.New("error.noModel", nil)
	}
	id, err := runner.Repository.StartScheduledTurn(ctx, conversation.ID, model.Label)
	if err != nil || id == "" {
		return err
	}
	turn := &turn{Runner: runner, conversation: conversation, id: id, instructionsDirty: true}
	defer turn.clearPreviews()
	if conversation.Role == "coordinator" {
		turn.handledBefore, err = runner.Repository.LastHandledSeq(ctx, conversation.ID)
		if err != nil {
			return err
		}
	}
	turn.skills, err = skills.Conversation(ctx, runner.Repository, conversation, runner.Workspaces, runner.SkillsDirs)
	if err != nil {
		return err
	}
	project, err := runner.Repository.GetProject(ctx, conversation.ProjectID)
	if err != nil {
		return err
	}
	turn.external = turn.mcpTools(ctx, project)
	return turn.run(ctx, model)
}

func asNumber(value any) float64 {
	switch v := value.(type) {
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v
		}
	case int64:
		return float64(v)
	}
	return 0
}

func recordCall(model *settings.ResolvedModel, effort string) *trace.RecordedCall {
	record := &trace.RecordedCall{Provider: model.Provider.Kind, Model: model.Model, Settings: map[string]any{}, Program: model.Provider.Program, MaxTokens: int64(asNumber(model.Request["maxTokens"]))}
	record.ExtraBody, _ = model.Request["extraBody"].(map[string]any)
	if effort != "" {
		record.Effort = new(effort)
	}
	for key, value := range model.Settings {
		if field, ok := model.Provider.Settings.Properties[key]; ok && !field.Secret {
			record.Settings[key] = value
		}
	}
	return record
}

func requestFor(model *settings.ResolvedModel, conversation string) sdk.Request {
	request := sdk.Request{Model: model.Model, Effort: model.Effort, Conversation: conversation, MaxTokens: int64(asNumber(model.Request["maxTokens"]))}
	request.ExtraBody, _ = model.Request["extraBody"].(map[string]any)
	if headers, ok := model.Request["headers"].(map[string]any); ok {
		request.Headers = map[string]string{}
		for key, value := range headers {
			if header, ok := value.(string); ok {
				request.Headers[key] = header
			}
		}
	}
	return request
}

func budgetError(project core.ProjectView, conversation core.ConversationView) error {
	over := func(limit *float64, usage map[string]any, key string) error {
		used := asNumber(usage["input"]) + asNumber(usage["output"])
		if limit == nil || used < *limit {
			return nil
		}
		format := func(n float64) string {
			if n < 1000 {
				return fmt.Sprintf("%.0f", n)
			}
			return fmt.Sprintf("%.1fk", n/1000)
		}
		return domainerr.New(key, map[string]any{"used": format(used), "limit": format(*limit)})
	}
	if err := over(project.Settings.Budget.Project, project.Usage, "error.budgetProject"); err != nil {
		return err
	}
	if conversation.Role == "thread" {
		return over(project.Settings.Budget.Thread, conversation.Usage, "error.budgetThread")
	}
	return nil
}

func (turn *turn) history(ctx context.Context) (int64, []transcript.Entry, error) {
	seq, err := turn.Repository.Store.LastSeq(ctx)
	if err != nil {
		return 0, nil, err
	}
	entries, err := turn.Repository.ListTranscript(ctx, turn.conversation.ID, core.TranscriptOptions{UpTo: &seq, ModelContext: true})
	if err != nil {
		return 0, nil, err
	}
	out := make([]transcript.Entry, len(entries))
	for index, entry := range entries {
		out[index] = entry
	}
	return seq, out, nil
}

func (turn *turn) withImages(ctx context.Context, messages []transcript.Message) []sdk.Message {
	store := files.Store{Events: turn.Repository.Store}
	left := int64(transcript.MaxInputImageBytes)
	return transcript.WithImageData(messages, func(attachment transcript.Attachment) (sdk.Image, bool) {
		size, err := store.Info(ctx, turn.conversation.ProjectID, attachment.FileID)
		if err != nil || size == nil || size.Size > left || !files.IsImage(size.MIME) {
			return sdk.Image{}, false
		}
		file, err := store.Read(ctx, attachment.FileID)
		if err != nil {
			return sdk.Image{}, false
		}
		left -= int64(len(file.Data))
		return sdk.Image{MIME: file.MIME, Data: base64.StdEncoding.EncodeToString(file.Data)}, true
	})
}

func (turn *turn) run(ctx context.Context, model *settings.ResolvedModel) error {
	conversation := turn.conversation
	config := turn.Settings.TurnSettings(conversation.ProjectID)
	limits, _ := config["limits"].(map[string]any)
	repeats := max(2, int(asNumber(limits["loopRepeats"])))
	const maxCycle = 3
	var recent [][32]byte
	cuts, lastInput := 0, 0
	hintedUnsent, compactionFailed := false, false

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		provider, ok := turn.Models.Get(model.Provider.Kind)
		if !ok {
			return domainerr.New("error.modelUnknown", map[string]any{"provider": model.Provider.Kind})
		}
		project, err := turn.Repository.GetProject(ctx, conversation.ProjectID)
		if err != nil {
			return err
		}
		current, err := turn.Repository.GetConversation(ctx, conversation.ID)
		if err != nil {
			return err
		}
		if err := budgetError(project, current); err != nil {
			return err
		}
		if turn.instructionsDirty {
			if err := turn.syncInstructions(ctx, project); err != nil {
				return err
			}
			turn.instructionsDirty = false
		}

		tools := append(turn.tools(ctx, project), turn.external...)
		if search := turn.webSearch(project, model); search != nil {
			tools = append(tools, *search)
		}
		if !slices.Contains(project.Settings.Tools, "fetch") {
			tools = append(tools, turn.fetchTool())
		}
		if turn.Browser != nil && !slices.Contains(project.Settings.Tools, "browser") {
			tools = append(tools, turn.browserTool())
		}
		if !slices.Contains(project.Settings.Tools, "research") {
			tools = append(tools, turn.researchTool(tools, model, limits))
		}
		sections := turn.sections(ctx, project, config)
		saw, entries, err := turn.history(ctx)
		if err != nil {
			return err
		}
		prompt, changed, err := turn.prompt(ctx, model.Provider.Program, tools, sections, entries)
		if err != nil {
			return err
		}
		if changed {
			saw, entries, err = turn.history(ctx)
			if err != nil {
				return err
			}
		}
		if conversation.Role == "coordinator" && !turn.userSpoke {
			turn.userSpoke = userSpokeSince(entries, turn.handledBefore)
		}
		record := recordCall(model, model.Effort)
		chat, title := trace.ModelInput(ctx, turn.Repository, conversation.ProjectID, record, model.Images)
		messages := append([]transcript.Message{{Role: "system", Content: prompt.system}}, transcript.ToMessages(entries, title, chat)...)
		request := requestFor(model, conversation.ID)
		request.Tools = prompt.specs
		retries := int(asNumber(limits["modelRetries"]))
		options := modelstep.Options{
			Retries: &retries, Timeout: time.Duration(asNumber(model.Request["timeoutSeconds"]) * float64(time.Second)),
			OnRetry: func(retry modelstep.Retry) error {
				return turn.Repository.RecordModelRetry(ctx, conversation.ID, turn.id, retry.Attempt, retry.DelayMS, retry.Detail)
			},
			OnInput: func(call *int, name, arguments string) {
				turn.preview(call, name, arguments)
				turn.previewArtifact(ctx, call, name, arguments)
			},
			Reset: turn.clearPreviews,
		}
		threshold := compactThreshold(model, limits)
		var price *sdk.ModelPrice
		if model.Price != nil {
			price = &sdk.ModelPrice{}
			priceJSON, _ := json.Marshal(model.Price)
			_ = json.Unmarshal(priceJSON, price)
		}
		estimate := transcript.EstimateTokens(messages)
		if size := max(lastInput, estimate); size >= threshold {
			cut := transcript.CompactionCut(entries, float64(threshold)/4)
			if cut != nil {
				cleared := false
				if *cut > latestCleared(entries) {
					trial := chat
					trial.ClearUpTo = cut
					after := transcript.EstimateTokens(append([]transcript.Message{{Role: "system", Content: prompt.system}}, transcript.ToMessages(entries, title, trial)...))
					if float64(after) < float64(estimate)*.75 && float64(size)*float64(after)/float64(estimate) < float64(threshold)*.8 {
						err = turn.Repository.ClearToolResults(ctx, conversation.ID, turn.id, *cut, saw)
						cleared = true
					}
				}
				if err != nil {
					return err
				}
				if cleared {
					lastInput = 0
					continue
				}
				if !compactionFailed {
					budget := transcript.UserWordBudget(float64(threshold))
					ask := requestFor(model, "")
					ask.Effort = ""
					ask.Messages = turn.withImages(ctx, transcript.SummaryRequest(entries, *cut, title, conversation.Role, chat, float64(budget)))
					summary, err := modelstep.Summarise(ctx, provider, model.Settings, ask, options)
					if err != nil {
						return err
					}
					if summary.OK {
						encoded, err := json.Marshal(recordCall(model, ""))
						if err != nil {
							return err
						}
						if err := turn.Repository.CompactConversation(ctx, conversation.ID, turn.id, core.Compaction{Summary: summary.Text, UpTo: *cut, SawSeq: saw, Usage: modelstep.WithCost(summary.Usage, price), Call: string(encoded), Timing: summary.Timing, Reasoning: summary.Reasoning, UserTokens: int64(budget)}); err != nil {
							return err
						}
						lastInput = 0
						compactionFailed = true
						continue
					}
					compactionFailed = true
					if err := turn.Repository.RecordCompactionFailure(ctx, conversation.ID, turn.id, textutil.HeadUnits(summary.Detail, 500)); err != nil {
						return err
					}
				}
			}
		}

		request.Messages = turn.withImages(ctx, messages)
		called, err := json.Marshal(record)
		if err != nil {
			return err
		}
		answer, err := modelstep.Request(ctx, provider, model.Settings, request, options)
		if err != nil {
			var failure *modelstep.Failure
			if ctx.Err() == nil && model.Fallback != nil && errors.As(err, &failure) && failure.Exhausted != nil {
				if err := turn.Repository.RecordModelFallback(ctx, conversation.ID, turn.id, model.Label, model.Fallback.Label, *failure.Exhausted); err != nil {
					return err
				}
				model = model.Fallback
				lastInput, compactionFailed = 0, false
				continue
			}
			return err
		}
		if answer.Usage != nil {
			lastInput = int(answer.Usage.Input)
			if answer.Usage.ContextInput != nil {
				lastInput = int(*answer.Usage.ContextInput)
			}
		}
		if err := turn.Repository.RecordStep(ctx, conversation.ID, turn.id, core.Step{
			Text: answer.Text, Reasoning: answer.Reasoning, ToolCalls: answer.ToolCalls, SawSeq: saw, Usage: modelstep.WithCost(answer.Usage, price),
			Input: core.StepInput{System: prompt.system, Tools: prompt.tools, Call: string(called), Images: model.Images}, Timing: answer.Timing, Cut: answer.Cut,
		}); err != nil {
			return err
		}
		if answer.Cut {
			cuts++
			if cuts >= 3 {
				return domainerr.New("error.truncated", map[string]any{"count": cuts})
			}
		} else {
			cuts = 0
		}
		if len(answer.ToolCalls) == 0 {
			turn.clearPreviews()
			if answer.Cut {
				continue
			}
			if strings.TrimSpace(answer.Text) != "" && !hintedUnsent {
				hintedUnsent = true
				continue
			}
			return turn.Repository.CompleteModelTurn(ctx, conversation.ID, turn.id)
		}
		hash, err := turn.execute(ctx, tools, answer.ToolCalls)
		if err != nil {
			return err
		}
		recent = append(recent, hash)
		recent = recent[max(0, len(recent)-repeats*maxCycle):]
		for width := 1; width <= min(maxCycle, len(recent)/repeats); width++ {
			cycle := recent[len(recent)-width*repeats:]
			if slices.Equal(cycle[width:], cycle[:len(cycle)-width]) {
				return domainerr.New("error.repeating", map[string]any{"count": repeats})
			}
		}
	}
}

func userSpokeSince(entries []transcript.Entry, since int64) bool {
	for _, entry := range entries {
		if asNumber(entry["seq"]) <= float64(since) {
			continue
		}
		switch entry["kind"] {
		case "message":
			sent, _ := entry["message"].(*core.MessageView)
			if sent != nil && sent.Author == "user" {
				return true
			}
		case "note":
			note, _ := entry["note"].(map[string]any)
			if note["type"] == "user-in-thread" || note["type"] == "answer" {
				return true
			}
		}
	}
	return false
}

func latestCleared(entries []transcript.Entry) float64 {
	latest := -1.0
	for _, entry := range entries {
		if entry["kind"] == "cleared" {
			latest = max(latest, asNumber(entry["upTo"]))
		}
	}
	return latest
}

func compactThreshold(model *settings.ResolvedModel, limits map[string]any) int {
	tokens := 128000.0
	if model.ContextTokens != nil {
		tokens = *model.ContextTokens
	}
	return max(1, transcript.CompactAt(tokens, asNumber(limits["compactShare"])))
}

func (turn *turn) execute(ctx context.Context, tools []tool, calls []sdk.ToolCall) ([32]byte, error) {
	byName := map[string]tool{}
	for _, entry := range tools {
		byName[entry.spec.Name] = entry
	}
	hash := sha256.New()
	defer turn.clearPreviews()
	for index, call := range calls {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		started := time.Now().UnixMilli()
		var args map[string]any
		var content string
		var attachments []any
		var err error
		if len(call.Arguments) > 2<<20 || json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
			err = errors.New("tool arguments must be a JSON object within 2 MiB")
		} else if entry, available := byName[call.Name]; !available {
			err = fmt.Errorf("tool %q is unavailable", textutil.HeadUnits(call.Name, 128))
		} else {
			if turn.draft != nil && replyTool(call.Name) && (*turn.draftCall != index || turn.draftTool != call.Name) {
				// A preview belongs to exactly one call in this response.
				turn.clearDraft()
			}
			if entry.withCallID != nil {
				content, err = entry.withCallID(ctx, call.ID, args)
			} else if entry.call != nil {
				content, attachments, err = entry.call(ctx, args)
			} else {
				content, err = entry.run(ctx, args)
			}
		}
		if err != nil && turn.draft != nil && *turn.draftCall == index {
			turn.clearDraft()
		}
		if draft := turn.artifactDrafts[index]; draft != nil {
			draft.Close()
			delete(turn.artifactDrafts, index)
		}
		if ctx.Err() != nil {
			return [32]byte{}, ctx.Err()
		}
		if err != nil && content == "" {
			content = textutil.HeadUnits(err.Error(), 2000)
		}
		content = strings.Clone(textutil.HeadUnits(content, 200_000))
		encoded, _ := json.Marshal([]any{call.Name, call.Arguments, content, err != nil, attachments})
		_, _ = hash.Write(encoded)
		if err := turn.Repository.RecordToolResult(ctx, turn.conversation.ID, turn.id, core.ToolResult{
			ToolCallID: call.ID, Name: call.Name, Content: content, IsError: err != nil, Files: attachments,
			Timing: map[string]int64{"startedAt": started, "finishedAt": time.Now().UnixMilli()},
		}); err != nil {
			return [32]byte{}, err
		}
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func namesOfTools(tools []tool) []sdk.Tool {
	specs := make([]sdk.Tool, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, tool.spec)
	}
	return specs
}
