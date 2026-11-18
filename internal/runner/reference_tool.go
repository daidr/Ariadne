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
)

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
	if turn.conversation.Role == "thread" {
		required = append(required, "reason", "keep")
		properties["keep"] = args{"type": "string", "enum": []string{"project", "thread"}}
	}
	entry := specification("add_reference", "Request read-only access to a source needed for the user's work. The user chooses whether to allow it. Never provide credentials. Kinds: "+strings.Join(descriptions, ", ")+".",
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
	keep := text(input, "keep")
	if isThread {
		thread, err := turn.editable(ctx)
		if err != nil {
			return "", err
		}
		if reason == "" || keep != "project" && keep != "thread" {
			return "", errors.New("a reason and keep of project or thread are required")
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
		detail = append(detail, fmt.Sprintf("%s = %v", key, settings[key]))
	}
	fingerprint, err := jsonutil.Encode([]any{kind, values})
	if err != nil {
		return "", err
	}
	purpose := fmt.Sprintf("reference:%x", sha256.Sum256(fingerprint))
	options := core.CallApprovalOptions{InChat: true}
	if isThread {
		options.No = new(2)
	}
	approval, err := turn.Repository.CallApproval(ctx, projectID, owner, purpose, func() args {
		question := "Allow read-only access to this reference?"
		projectLabel, threadLabel, no, yes := "For the project", "For this thread", "Decline", "Allow"
		projectNote, threadNote, declineNote := "All conversations in this project may read it.", "Only this thread may read it.", "The source stays unavailable."
		if turn.Settings.Locale() != "en" {
			question, projectLabel, threadLabel, no, yes = "允许读取这个引用来源吗？", "供整个项目使用", "仅供当前讨论串使用", "拒绝", "允许"
			projectNote, threadNote, declineNote = "项目中的所有会话都可以读取。", "当前讨论串可以读取。", "此来源保持不可读取。"
		}
		if isThread {
			recommend := 1
			if keep == "project" && !full {
				recommend = 0
			}
			return args{"type": "choice", "question": question + "\n" + strings.Join(detail, "\n") + "\n" + reason, "recommended": recommend,
				"options": []any{args{"label": projectLabel, "consequence": projectNote}, args{"label": threadLabel, "consequence": threadNote}, args{"label": no, "consequence": declineNote}}}
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
		if isThread && approval.Choice != nil && *approval.Choice == 1 {
			added, err = turn.Repository.AddThreadReference(ctx, owner, source, where, approval.AttentionID)
		} else {
			added, err = turn.Repository.AddProjectReference(ctx, projectID, source, where)
		}
		return reply(args{"source": added.Name}, err)
	default:
		return "Waiting for the user to choose access for this reference.", nil
	}
}
