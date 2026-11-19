package runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

const referenceToolName = "add_reference"

func referenceReadHint(toolName string, err error) string {
	switch toolName {
	case "read_file", "list_dir", "search", "find_files":
		var pathErr *sdk.WorkspacePathError
		if errors.As(err, &pathErr) && pathErr.Reason == "" {
			return "\n\nFor a target inside the workspace or an existing reference, use a path relative to that source. If the task requires reading outside those sources, use add_reference to request read-only access to the specific file or directory. Use kind \"local-directory\" and settings.path for a local path. After approval, use the returned source name and a path relative to that source."
		}
	}
	return ""
}

func (turn *turn) referenceTool() *tool {
	var kinds, descriptions []string
	for _, info := range turn.Sources.Kinds() {
		var fields []string
		for _, key := range slices.Sorted(maps.Keys(info.Settings.Properties)) {
			if !info.Settings.Properties[key].Secret {
				fields = append(fields, key)
			}
		}
		kinds = append(kinds, info.Kind)
		descriptions = append(descriptions, info.Kind+" ("+strings.Join(fields, ", ")+")")
	}
	if len(kinds) == 0 {
		return nil
	}
	required := []string{"kind", "settings"}
	properties := args{"kind": args{"type": "string", "enum": kinds}, "settings": field("object"), "reason": field("string")}
	scope := "The approval is for this project."
	if turn.conversation.Role == "thread" {
		required = append(required, "reason")
		scope = "The user chooses whether to allow it for this discussion or for the project."
	}
	entry := specification(referenceToolName, "Request read-only access to a source needed for the user's work. "+scope+" Never provide credentials. Kinds: "+strings.Join(descriptions, ", ")+".",
		properties, required, turn.addReference)
	return &entry
}

func (turn *turn) addReference(ctx context.Context, input args) (string, error) {
	kind := text(input, "kind")
	provider, ok := turn.Sources.Get(kind)
	if !ok {
		return "", errors.New("unknown source kind")
	}
	given, ok := input["settings"].(args)
	if !ok {
		return "", errors.New("settings must be an object")
	}
	settings := args{}
	for key, value := range given {
		field, known := provider.Info.Settings.Properties[key]
		if !known || field.Secret {
			return "", fmt.Errorf("reference setting %q cannot be supplied here", key)
		}
		if s, ok := value.(string); ok {
			value = strings.TrimSpace(s)
		}
		if value != nil && value != "" {
			settings[key] = value
		}
	}
	for _, key := range provider.Info.Settings.Required {
		if !provider.Info.Settings.Properties[key].Secret && settings[key] == nil {
			return "", fmt.Errorf("reference setting %q is required", key)
		}
	}
	encoded, err := jsonutil.Encode(settings)
	if err != nil || len(encoded) > 5000 {
		return "", errors.New("reference settings are too large")
	}
	if refusal := workspaces.ReferenceRefusal(provider.Info, settings, []string{turn.Home}); refusal != "" {
		return "", errors.New(refusal)
	}
	projectID, owner := turn.conversation.ProjectID, turn.conversation.ID
	project, err := turn.Repository.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	projectRefs, _ := project.References.([]any)
	references := projectRefs
	isThread := turn.conversation.Role == "thread"
	reason := textutil.HeadUnits(strings.Join(strings.Fields(text(input, "reason")), " "), 200)
	if isThread {
		thread, err := turn.editable(ctx)
		if err != nil {
			return "", err
		}
		if reason == "" {
			return "", errors.New("a reason is required")
		}
		own, _ := thread.References.([]any)
		references = append(slices.Clone(own), projectRefs...)
	}
	for _, raw := range references {
		known, _ := raw.(args)
		normalized, _ := jsonutil.Encode(known["settings"])
		if known["kind"] == kind && string(normalized) == string(encoded) {
			return fmt.Sprintf("Read this reference using source %q.", known["name"]), nil
		}
	}
	full := len(projectRefs) >= 8
	if full && !isThread {
		return "", errors.New("the project already has eight references")
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	where, err := turn.Workspaces.CheckReference(check, projectID, sources.Part{Kind: kind, Settings: settings}, nil)
	cancel()
	if err != nil {
		return "", err
	}
	values := make([][2]any, 0, len(settings))
	detail := []string{where}
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		values = append(values, [2]any{key, settings[key]})
		if settings[key] != where {
			detail = append(detail, fmt.Sprintf("%s = %v", key, settings[key]))
		}
	}
	fingerprint, err := jsonutil.Encode([]any{kind, values})
	if err != nil {
		return "", err
	}
	purpose := fmt.Sprintf("reference:%x", sha256.Sum256(fingerprint))
	options := core.CallApprovalOptions{InChat: true, AllowProjectReference: isThread && !full}
	if isThread {
		// Existing approvals may still have the old three choices.
		options.No = new(2)
	}
	approval, err := turn.Repository.CallApproval(ctx, projectID, owner, purpose, func() args {
		projectQuestion := "Allow this project to read this source?"
		threadQuestion := "Allow this discussion to read this source?"
		scopeQuestion := "Allow reading this source?"
		no, yes := "Decline", "Allow"
		if turn.Settings.Locale() != "en" {
			projectQuestion, threadQuestion, no, yes = "允许整个项目读取这个来源？", "允许当前讨论串读取这个来源？", "拒绝", "允许"
			scopeQuestion = "允许读取这个来源？"
		}
		question := projectQuestion
		if isThread {
			question = threadQuestion
		}
		if options.AllowProjectReference {
			question = scopeQuestion
		}
		return args{"type": "confirm", "title": question, "detail": strings.Join(detail, "\n"), "description": reason, "approveText": yes, "denyText": no}
	}, options)
	if err != nil {
		return "", err
	}
	switch approval.State {
	case "declined":
		return "", errors.New("the user declined this reference")
	case "approved":
		if err := turn.Repository.UseApproval(ctx, projectID, owner, approval.AttentionID, "reference"); err != nil {
			return "", err
		}
		source := core.Source{Kind: kind, Settings: settings}
		var added core.Reference
		if isThread && approval.Scope != "project" && (approval.Choice == nil || *approval.Choice == 1) {
			added, err = turn.Repository.AddThreadReference(ctx, owner, source, where, approval.AttentionID)
		} else {
			added, err = turn.Repository.AddProjectReference(ctx, projectID, source, where)
		}
		return reply(args{"source": added.Name}, err)
	default:
		return "Waiting for the user to approve read-only access to this reference.", nil
	}
}
