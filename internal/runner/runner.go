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
	"sync"
	"sync/atomic"
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
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/secrets"
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
	Repository      *core.Repository
	Settings        *settings.Store
	Models          *models.Registry
	Drafts          *realtime.ReplyDrafts
	Live            *realtime.LiveCommands
	Workspaces      *workspaces.Manager
	MCP             *mcp.Hub
	SkillsDirs      []string
	Searches        *searches.Registry
	HTTPClient      *http.Client
	Rooms           *collab.Rooms
	Dashboards      *dashboard.Registry
	Browser         *browser.Pages
	Sources         *sources.Registry
	Home            string
	ShouldSuspend   func() bool
	OnModelResolved func(conversationID, modelLabel string, stateful bool)
	files           fileState
}

type turn struct {
	*Runner
	conversation        core.ConversationView
	id                  string
	draft               *realtime.DraftHandle
	draftCall           *int
	draftTool           string
	artifactDrafts      map[int]*collab.Draft
	artifactPreviewAt   map[int]time.Time
	skills              *skills.Set
	external            []tool
	instructionsDirty   atomic.Bool
	handledBefore       int64
	userSpoke           bool
	directUserInput     bool
	mergeRequestID      string
	quiet               bool
	quietThreads        map[string]bool
	quietMessageThreads map[string]string
	routedToOtherThread bool
	statefulSession     bool
	changes             *turnChanges
	looks               coordinatorLookState
	todo                todoHintState
	reviewChecked       bool
	isReview            bool
}

const (
	maxToolResultChars = 200_000

	coordinatorLookHint           = 6
	coordinatorLookLimit          = 12
	coordinatorResearchLookWeight = 3
	coordinatorLookHintText       = "[Ariadne: you have looked at a lot this turn. Looking further is doing the work yourself, and work is a thread's: put what you found into a brief and open a thread with create_thread (or pass it to the thread that has it with message_thread). Answer yourself only if what you have already answers the user.]"
	coordinatorLookRefusedText    = "[Ariadne: not run. You have looked as far as a coordinator does in one turn without handing anything to a thread. Open a thread with create_thread and put what you found into its brief, or message the thread that has the work; after that you may look again for the next one.]"

	todoMissingAfter = 3
	todoStaleAfter   = 10
	todoMissingHint  = "[Ariadne: you are working without a todo list, so the user cannot see what you are doing or what comes next. Write your steps with update_todo now, before your next step, and keep it current as you go. A rough outline is enough; change it as you learn more.]"
	todoFinishedHint = "[Ariadne: your todo list is the finished one of earlier work, so the user cannot see what you are doing now. Write the steps of this work with update_todo now, before your next step (keep the earlier ones as done if they still matter), and keep it current as you go. A rough outline is enough; change it as you learn more.]"
	todoStaleHint    = "[Ariadne: your todo list has not changed for many steps. If it no longer shows where you are, bring it up to date with update_todo: mark finished steps done and the one you are on as doing.]"
)

type coordinatorLookState struct {
	looks  int
	hinted bool
}

type lookDecision struct {
	refused bool
	hint    bool
}

func coordinatorLookWeight(name string) int {
	switch name {
	case "research":
		return coordinatorResearchLookWeight
	case "list_dir", "read_file", "search", "find_files", "fetch_url", "web_search", "browser":
		return 1
	default:
		return 0
	}
}

func coordinatorHandsOver(name string) bool {
	return name == "create_thread" || name == "message_thread"
}

func (state *coordinatorLookState) reset() {
	state.looks = 0
	state.hinted = false
}

func (state *coordinatorLookState) reserve(name string) lookDecision {
	weight := coordinatorLookWeight(name)
	if weight == 0 {
		return lookDecision{}
	}
	if state.looks+weight > coordinatorLookLimit {
		return lookDecision{refused: true}
	}
	state.looks += weight
	hint := !state.hinted && state.looks >= coordinatorLookHint
	if hint {
		state.hinted = true
	}
	return lookDecision{hint: hint}
}

type todoHintState struct {
	workSteps     int
	sinceTodo     int
	todoThisTurn  bool
	hintedMissing bool
	hintedStale   bool
}

func appendHint(content, hint string) string {
	if hint == "" {
		return content
	}
	if content == "" {
		return hint
	}
	return content + "\n\n" + hint
}

func (turn *turn) isReviewThread(ctx context.Context) bool {
	if turn.conversation.Role != "thread" {
		return false
	}
	if !turn.reviewChecked {
		turn.reviewChecked = true
		if thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID); err == nil {
			author, ok := thread.ReviewOf.(string)
			turn.isReview = ok && author != ""
		}
	}
	return turn.isReview
}

func (turn *turn) todoHint(ctx context.Context, state *todoHintState) (string, error) {
	todo, err := turn.Repository.GetTodo(ctx, turn.conversation.ID)
	if err != nil {
		return "", err
	}
	items, _ := todo["items"].([]any)
	finished := len(items) > 0
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if text(item, "status") != "done" {
			finished = false
			break
		}
	}
	if len(items) == 0 || (finished && !state.todoThisTurn) {
		if state.hintedMissing || state.workSteps < todoMissingAfter {
			return "", nil
		}
		state.hintedMissing = true
		if len(items) > 0 {
			return todoFinishedHint, nil
		}
		return todoMissingHint, nil
	}
	if state.hintedStale || state.sinceTodo < todoStaleAfter || finished {
		return "", nil
	}
	state.hintedStale = true
	return todoStaleHint, nil
}

func (runner *Runner) Resolve(ctx context.Context, conversation core.ConversationView) (*settings.ResolvedModel, error) {
	kinds := runner.Models.Kinds()
	if conversation.Role == "thread" {
		thread, err := runner.Repository.GetThread(ctx, conversation.ID)
		if err != nil {
			return nil, err
		}
		model, _ := thread.Model.(string)
		effort, _ := thread.Effort.(string)
		return runner.Settings.ResolveModel("thread", model, effort, kinds)
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
	if runner.OnModelResolved != nil {
		runner.OnModelResolved(conversation.ID, model.Label, model.Provider.HasStatefulSessions())
	}
	id, err := runner.Repository.StartScheduledTurn(ctx, conversation.ID, model.Label)
	if err != nil || id == "" {
		return err
	}
	turn := &turn{Runner: runner, conversation: conversation, id: id, changes: newTurnChanges(runner, conversation)}
	turn.instructionsDirty.Store(true)
	defer turn.clearPreviews()
	turn.handledBefore, err = runner.Repository.LastHandledSeq(ctx, conversation.ID)
	if err != nil {
		return err
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
	err = turn.run(ctx, model)
	if err != nil {
		turn.changes.record(ctx, turn.id)
	}
	return err
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
	record := &trace.RecordedCall{
		Provider:         model.Provider.Kind,
		Model:            model.Model,
		Settings:         map[string]any{},
		Program:          model.Provider.Program,
		StatefulSessions: model.Provider.HasStatefulSessions(),
		ReasoningScope:   model.Provider.Capabilities.ReasoningScope,
		MaxTokens:        int64(asNumber(model.Request["maxTokens"])),
	}
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
	turn.statefulSession = model != nil && model.Provider.HasStatefulSessions()
	defer func() {
		if ctx.Err() != nil {
			turn.fileTracker().forgetReads(conversation.ID)
		}
	}()
	config := turn.Settings.TurnSettings(conversation.ProjectID)
	limits, _ := config["limits"].(map[string]any)
	repeats := max(2, int(asNumber(limits["loopRepeats"])))
	const maxCycle = 3
	var recent [][32]byte
	cuts, lastInput, lastInputEstimate := 0, 0, 0
	hintedUnsent, compactionFailed, compactedBeforeStep := false, false, false
	remindedFeedback := false
	recoveredContext := false

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		provider, ok := turn.Models.Get(model.Provider.Kind)
		if !ok {
			return domainerr.New("error.modelUnknown", map[string]any{"provider": model.Provider.Kind})
		}
		project, err := turn.Repository.GetProjectForRun(ctx, conversation.ProjectID)
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
		if turn.instructionsDirty.Load() {
			if err := turn.syncInstructions(ctx, project); err != nil {
				return err
			}
			turn.instructionsDirty.Store(false)
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
		prompt, changed, err := turn.prompt(ctx, model.Provider.HasStatefulSessions(), tools, sections, entries)
		if err != nil {
			return err
		}
		if changed {
			saw, entries, err = turn.history(ctx)
			if err != nil {
				return err
			}
		}
		if conversation.Role == "coordinator" {
			turn.trackCoordinatorInput(entries)
		} else {
			turn.trackThreadInput(entries)
		}
		record := recordCall(model, model.Effort)
		called, err := json.Marshal(record)
		if err != nil {
			return err
		}
		stepInput := core.StepInput{System: prompt.system, Tools: prompt.tools, Call: string(called), Images: model.Images}
		chat, title := trace.ModelInput(ctx, turn.Repository, conversation.ProjectID, record, model.Images)
		messages := append([]transcript.Message{{Role: "system", Content: prompt.system}}, transcript.ToMessages(entries, title, chat)...)
		request := requestFor(model, conversation.ID)
		request.Tools = prompt.specs
		retries := int(asNumber(limits["modelRetries"]))
		options := modelstep.Options{
			Retries: &retries, Timeout: time.Duration(asNumber(model.Request["timeoutSeconds"]) * float64(time.Second)),
			OnFailure: turn.recordModelFailure(ctx, saw, stepInput, "step", ""),
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
		if size := effectiveContextSize(lastInput, lastInputEstimate, estimate); size >= threshold {
			cut := transcript.CompactionCut(entries, float64(threshold)/4)
			if cut != nil && !compactionFailed && !compactedBeforeStep {
				budget := transcript.UserWordBudget(float64(threshold))
				ask := requestFor(model, "")
				ask.Effort = ""
				ask.Messages = turn.withImages(ctx, transcript.SummaryRequest(entries, *cut, title, conversation.Role, chat, float64(budget)))
				encoded, err := json.Marshal(recordCall(model, ""))
				if err != nil {
					return err
				}
				summaryOptions := options
				summaryInput := core.StepInput{System: ask.Messages[0].Content, Tools: "[]", Call: string(encoded), Images: model.Images}
				summaryOptions.OnFailure = turn.recordModelFailure(ctx, saw, summaryInput, "summary", "")
				summary, err := modelstep.Summarise(ctx, provider, model.Settings, ask, summaryOptions)
				if err != nil {
					return err
				}
				if summary.OK {
					if err := turn.Repository.CompactConversation(ctx, conversation.ID, turn.id, core.Compaction{Summary: summary.Text, UpTo: *cut, SawSeq: saw, Usage: modelstep.WithCost(summary.Usage, price), Call: string(encoded), Timing: summary.Timing, Reasoning: summary.Reasoning, UserTokens: int64(budget)}); err != nil {
						return err
					}
					turn.fileTracker().forgetReads(conversation.ID)
					lastInput, lastInputEstimate = 0, 0
					compactedBeforeStep = true
					continue
				}
				compactionFailed = true
				if err := turn.Repository.RecordCompactionFailure(ctx, conversation.ID, turn.id, textutil.HeadUnits(summary.Detail, 500)); err != nil {
					return err
				}
			}
		}

		request.Messages = turn.withImages(ctx, messages)
		answer, err := modelstep.Request(ctx, provider, model.Settings, request, options)
		if err != nil {
			var failure *modelstep.Failure
			if ctx.Err() == nil && errors.As(err, &failure) && model.Provider.HasStatefulSessions() && failure.ContextLimit && !recoveredContext {
				// The failed native session has been released. Compact if there is
				// enough history, then resume the same discussion with a fresh session.
				recoveredContext = true
				compactedBeforeStep = false
				lastInput = max(lastInput, threshold)
				lastInputEstimate = estimate
				continue
			}
			return err
		}
		compactedBeforeStep = false
		if answer.Usage != nil {
			if answer.Usage.ContextInput != nil {
				if *answer.Usage.ContextInput > 0 {
					lastInput = int(*answer.Usage.ContextInput)
					lastInputEstimate = estimate
				}
			} else if answer.Usage.Input > 0 {
				lastInput = int(answer.Usage.Input)
				lastInputEstimate = estimate
			}
		}
		if err := turn.Repository.RecordStep(ctx, conversation.ID, turn.id, core.Step{
			Text: answer.Text, Reasoning: answer.Reasoning, ToolCalls: answer.ToolCalls, SawSeq: saw, Usage: modelstep.WithCost(answer.Usage, price),
			Input: stepInput, Timing: answer.Timing, Cut: answer.Cut, Quiet: turn.quiet,
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
			if !turn.quiet && !remindedFeedback && slices.ContainsFunc(tools, func(tool tool) bool {
				return tool.spec.Name == "finish_feedback_round"
			}) {
				reminded, err := turn.Repository.RemindPendingArtifactFeedback(ctx, conversation.ID, turn.id)
				if err != nil {
					return err
				}
				if reminded {
					remindedFeedback, hintedUnsent = true, true
					continue
				}
			}
			if !turn.quiet && strings.TrimSpace(answer.Text) != "" && !hintedUnsent {
				hintedUnsent = true
				continue
			}
			turn.changes.record(ctx, turn.id)
			return turn.Repository.FinishTurn(ctx, conversation.ID, turn.id, "completed", nil)
		}
		batch, err := turn.execute(ctx, tools, answer.ToolCalls)
		if err != nil {
			return err
		}
		if turn.quiet && !answer.Cut && !slices.ContainsFunc(answer.ToolCalls, func(call sdk.ToolCall) bool {
			return !coordinatorChatTool(call.Name)
		}) {
			turn.changes.record(ctx, turn.id)
			return turn.Repository.FinishTurn(ctx, conversation.ID, turn.id, "completed", nil)
		}
		switch batch.effect {
		case repeatProgress:
			recent = nil
		case repeatWait:
		default:
			recent = append(recent, batch.hash)
			recent = recent[max(0, len(recent)-repeats*maxCycle):]
			for width := 1; width <= min(maxCycle, len(recent)/repeats); width++ {
				cycle := recent[len(recent)-width*repeats:]
				if slices.Equal(cycle[width:], cycle[:len(cycle)-width]) {
					return domainerr.New("error.repeating", map[string]any{"count": repeats})
				}
			}
		}
		// Safe-point handoff for stateless HTTP models: every step and tool result
		// is durably committed in SQLite, so suspending here lets the restarted
		// server immediately resume the next step without error or lost work.
		if !model.Provider.HasStatefulSessions() && turn.ShouldSuspend != nil && turn.ShouldSuspend() {
			turn.changes.record(ctx, turn.id)
			return turn.Repository.FinishTurn(ctx, conversation.ID, turn.id, "suspended", nil)
		}
	}
}

func (turn *turn) recordModelFailure(ctx context.Context, saw int64, input core.StepInput, purpose, toolCallID string) func(modelstep.AttemptFailure) error {
	return func(failure modelstep.AttemptFailure) error {
		redact := secrets.NewDiagnosticRedactor(secrets.ConfiguredValues(turn.Settings.Current()))
		failure.Detail = redact.Text(failure.Detail)
		if failure.AgentError != nil {
			info := *failure.AgentError
			info.Stderr, info.RPCData = redact.Text(info.Stderr), redact.Text(info.RPCData)
			failure.AgentError = &info
		}
		return turn.Repository.RecordModelFailure(ctx, turn.conversation.ID, turn.id, saw, input, purpose, toolCallID, failure)
	}
}

func effectiveContextSize(lastInput, lastInputEstimate, estimate int) int {
	if lastInput <= 0 {
		return estimate
	}
	return lastInput + max(0, estimate-lastInputEstimate)
}

func compactThreshold(model *settings.ResolvedModel, limits map[string]any) int {
	tokens := 128000.0
	if model.ContextTokens != nil {
		tokens = *model.ContextTokens
	}
	return max(1, transcript.CompactAt(tokens, asNumber(limits["compactShare"])))
}

type batchDigest struct {
	hash   [32]byte
	effect repeatEffect
}

type callOutcome struct {
	started     int64
	finished    int64
	content     string
	attachments []any
	effect      repeatEffect
	err         error
	isError     bool
	refused     bool
}

// callGroups partitions a step's tool calls into execution groups: adjacent
// read-only calls share a group and run concurrently, while every other call
// runs alone in order. Returned elements are indices into calls.
func callGroups(calls []sdk.ToolCall, byName map[string]tool) [][]int {
	groups := make([][]int, 0, len(calls))
	var reading []int
	for index, call := range calls {
		if entry, ok := byName[call.Name]; ok && entry.readOnly {
			reading = append(reading, index)
			continue
		}
		if len(reading) > 0 {
			groups = append(groups, reading)
			reading = nil
		}
		groups = append(groups, []int{index})
	}
	if len(reading) > 0 {
		groups = append(groups, reading)
	}
	return groups
}

func (turn *turn) runToolCall(ctx context.Context, call sdk.ToolCall, byName map[string]tool, refused bool) callOutcome {
	started := time.Now().UnixMilli()
	if refused {
		return callOutcome{
			started:  started,
			finished: started,
			content:  coordinatorLookRefusedText,
			isError:  true,
			refused:  true,
		}
	}
	var input map[string]any
	var content string
	var attachments []any
	var effect repeatEffect
	var err error
	if len(call.Arguments) > 2<<20 || json.Unmarshal([]byte(call.Arguments), &input) != nil || input == nil {
		err = errors.New("tool arguments must be a JSON object within 2 MiB")
	} else if turn.quiet && coordinatorChatTool(call.Name) {
		err = errors.New("that discussion owns the user-facing reply. Use message_thread if it needs coordination or a correction, route unrelated work with create_thread or message_thread to another thread before notifying the user, or end the turn silently")
	} else if entry, available := byName[call.Name]; !available {
		err = fmt.Errorf("tool %q is unavailable", textutil.HeadUnits(call.Name, 128))
	} else {
		if entry.withCallID != nil {
			content, err = entry.withCallID(ctx, call.ID, input)
		} else if entry.call != nil {
			content, attachments, err = entry.call(ctx, input)
		} else if entry.poll != nil {
			content, effect, err = entry.poll(ctx, input)
		} else {
			content, err = entry.run(ctx, input)
		}
	}
	return callOutcome{
		started:     started,
		finished:    time.Now().UnixMilli(),
		content:     content,
		attachments: attachments,
		effect:      effect,
		err:         err,
	}
}

func (turn *turn) execute(ctx context.Context, tools []tool, calls []sdk.ToolCall) (batchDigest, error) {
	byName := map[string]tool{}
	for _, entry := range tools {
		byName[entry.spec.Name] = entry
	}
	var lookDecisions []lookDecision
	if turn.conversation.Role == "coordinator" {
		if slices.ContainsFunc(calls, func(call sdk.ToolCall) bool { return coordinatorHandsOver(call.Name) }) {
			turn.looks.reset()
		}
		lookDecisions = make([]lookDecision, len(calls))
		for index, call := range calls {
			lookDecisions[index] = turn.looks.reserve(call.Name)
		}
	}
	_, hasTodo := byName["update_todo"]
	todoWatched := !turn.quiet && !turn.isReviewThread(ctx) && hasTodo
	wroteTodo := false

	hash := sha256.New()
	progressed := false
	allWaited := len(calls) > 0
	defer turn.clearPreviews()
	for _, group := range callGroups(calls, byName) {
		if err := ctx.Err(); err != nil {
			return batchDigest{}, err
		}
		outcomes := make([]callOutcome, len(group))
		if len(group) == 1 {
			index := group[0]
			call := calls[index]
			refused := turn.conversation.Role == "coordinator" && lookDecisions[index].refused
			if !refused && turn.draft != nil && replyTool(call.Name) && (*turn.draftCall != index || turn.draftTool != call.Name) {
				// A preview belongs to exactly one call in this response.
				turn.clearDraft()
			}
			outcomes[0] = turn.runToolCall(ctx, call, byName, refused)
		} else {
			var wg sync.WaitGroup
			for slot, index := range group {
				call := calls[index]
				refused := turn.conversation.Role == "coordinator" && lookDecisions[index].refused
				if refused {
					outcomes[slot] = turn.runToolCall(ctx, call, byName, true)
					continue
				}
				wg.Go(func() {
					outcomes[slot] = turn.runToolCall(ctx, call, byName, false)
				})
			}
			wg.Wait()
		}
		if err := ctx.Err(); err != nil {
			return batchDigest{}, err
		}
		for slot, index := range group {
			call := calls[index]
			outcome := outcomes[slot]
			if outcome.refused {
				allWaited = false
			} else {
				if outcome.effect == repeatProgress {
					progressed = true
				}
				if outcome.effect != repeatWait {
					allWaited = false
				}
				if outcome.err != nil && turn.draft != nil && *turn.draftCall == index {
					turn.clearDraft()
				}
				if draft := turn.artifactDrafts[index]; draft != nil {
					draft.Close()
					delete(turn.artifactDrafts, index)
				}
				if call.Name == "update_todo" && outcome.err == nil {
					wroteTodo = true
				}
				outcome.isError = outcome.err != nil
				if outcome.err != nil && outcome.content == "" {
					hint := ""
					if _, available := byName[fileSourceToolName]; available {
						hint = referenceReadHint(call.Name, outcome.err)
					}
					outcome.content = textutil.HeadUnits(toolErrorText(outcome.err), 2000-len(hint)) + hint
				}
				outcome.content = strings.Clone(textutil.HeadUnits(outcome.content, maxToolResultChars))
			}
			encoded, _ := json.Marshal([]any{call.Name, call.Arguments, outcome.content, outcome.isError, outcome.attachments})
			_, _ = hash.Write(encoded)
			if turn.conversation.Role == "coordinator" && lookDecisions[index].hint {
				outcome.content = appendHint(outcome.content, coordinatorLookHintText)
			}
			if index == len(calls)-1 {
				turn.todo.workSteps++
				if wroteTodo {
					turn.todo.sinceTodo = 0
					turn.todo.todoThisTurn = true
				} else {
					turn.todo.sinceTodo++
				}
				if todoWatched {
					hint, hintErr := turn.todoHint(ctx, &turn.todo)
					if hintErr != nil {
						return batchDigest{}, hintErr
					}
					outcome.content = appendHint(outcome.content, hint)
				}
			}
			if err := turn.Repository.RecordToolResult(ctx, turn.conversation.ID, turn.id, core.ToolResult{
				ToolCallID: call.ID, Name: call.Name, Content: outcome.content, IsError: outcome.isError, Files: outcome.attachments,
				Timing: map[string]int64{"startedAt": outcome.started, "finishedAt": outcome.finished},
			}); err != nil {
				return batchDigest{}, err
			}
		}
	}
	var result batchDigest
	copy(result.hash[:], hash.Sum(nil))
	if progressed {
		result.effect = repeatProgress
	} else if allWaited {
		result.effect = repeatWait
	}
	return result, nil
}

func namesOfTools(tools []tool) []sdk.Tool {
	specs := make([]sdk.Tool, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, tool.spec)
	}
	return specs
}
