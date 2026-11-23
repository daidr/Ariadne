package server

import (
	"encoding/json"
	"fmt"
	"reflect"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/skills"
	"github.com/daidr/Ariadne/internal/trace"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

var mapStringAnyType = reflect.TypeFor[map[string]any]()

func protoMap(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Map || rv.IsNil() {
		return nil
	}
	if rv.Type().ConvertibleTo(mapStringAnyType) {
		return rv.Convert(mapStringAnyType).Interface().(map[string]any)
	}
	if rv.Type().Key().Kind() == reflect.String {
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = iter.Value().Interface()
		}
		return out
	}
	return nil
}

func protoSlice(raw any) []any {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		return v
	case []map[string]any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out
	}
	return nil
}

func protoStr(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case *string:
		if v != nil {
			return *v
		}
	}
	return ""
}

func protoOptStr(raw any) *string {
	switch v := raw.(type) {
	case string:
		s := v
		return &s
	case *string:
		if v == nil {
			return nil
		}
		s := *v
		return &s
	}
	return nil
}

func protoOptNonEmptyStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func protoBool(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case *bool:
		if v != nil {
			return *v
		}
	}
	return false
}

func protoOptBool(raw any) *bool {
	switch v := raw.(type) {
	case bool:
		b := v
		return &b
	case *bool:
		if v == nil {
			return nil
		}
		b := *v
		return &b
	}
	return nil
}

func protoFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case *float64:
		if v != nil {
			return *v, true
		}
	case float32:
		return float64(v), true
	case *float32:
		if v != nil {
			return float64(*v), true
		}
	case int:
		return float64(v), true
	case *int:
		if v != nil {
			return float64(*v), true
		}
	case int32:
		return float64(v), true
	case *int32:
		if v != nil {
			return float64(*v), true
		}
	case int64:
		return float64(v), true
	case *int64:
		if v != nil {
			return float64(*v), true
		}
	case uint:
		return float64(v), true
	case *uint:
		if v != nil {
			return float64(*v), true
		}
	case uint32:
		return float64(v), true
	case *uint32:
		if v != nil {
			return float64(*v), true
		}
	case uint64:
		return float64(v), true
	case *uint64:
		if v != nil {
			return float64(*v), true
		}
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func protoInt64(raw any) int64 {
	if v, ok := raw.(int64); ok {
		return v
	}
	if v, ok := raw.(*int64); ok && v != nil {
		return *v
	}
	if f, ok := protoFloat(raw); ok {
		return int64(f)
	}
	return 0
}

func protoOptInt64(raw any) *int64 {
	if raw == nil {
		return nil
	}
	if v, ok := raw.(int64); ok {
		return &v
	}
	if v, ok := raw.(*int64); ok {
		if v == nil {
			return nil
		}
		n := *v
		return &n
	}
	if f, ok := protoFloat(raw); ok {
		n := int64(f)
		return &n
	}
	return nil
}

func protoInt32(raw any) int32 {
	if f, ok := protoFloat(raw); ok {
		return int32(f)
	}
	return 0
}

func protoOptInt32(raw any) *int32 {
	if raw == nil {
		return nil
	}
	if f, ok := protoFloat(raw); ok {
		n := int32(f)
		return &n
	}
	return nil
}

func protoOptFloat64(raw any) *float64 {
	if raw == nil {
		return nil
	}
	if f, ok := protoFloat(raw); ok {
		return &f
	}
	return nil
}

func protoStringSlice(raw any) []string {
	if s, ok := raw.([]string); ok {
		return s
	}
	items := protoSlice(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if str, ok := item.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func protoStringMap(raw any) map[string]string {
	if m, ok := raw.(map[string]string); ok {
		return m
	}
	m := protoMap(raw)
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func toStructPB(raw any) *structpb.Struct {
	if raw == nil {
		return nil
	}
	if m := protoMap(raw); m != nil {
		if st, err := structpb.NewStruct(m); err == nil {
			return st
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	st, err := structpb.NewStruct(decoded)
	if err != nil {
		return nil
	}
	return st
}

func toValuePB(raw any) *structpb.Value {
	if raw == nil {
		return nil
	}
	if v, err := structpb.NewValue(raw); err == nil {
		return v
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	v, err := structpb.NewValue(decoded)
	if err != nil {
		return nil
	}
	return v
}

func toProtoLocalizedError(raw any) *ariadnev1.LocalizedError {
	if raw == nil {
		return nil
	}
	if derr, ok := raw.(*domainerr.Error); ok {
		if derr == nil {
			return nil
		}
		return &ariadnev1.LocalizedError{
			Key:    derr.Key,
			Params: encodeErrorParams(derr.Params),
		}
	}
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	key := protoStr(m["key"])
	if key == "" {
		return nil
	}
	return &ariadnev1.LocalizedError{
		Key:    key,
		Params: encodeErrorParams(protoMap(m["params"])),
	}
}

func toProtoTokenUsageTotals(raw map[string]any) *ariadnev1.TokenUsageTotals {
	totals := &ariadnev1.TokenUsageTotals{}
	if raw == nil {
		return totals
	}
	totals.Input = protoInt64(raw["input"])
	totals.Output = protoInt64(raw["output"])
	totals.Cached = protoInt64(raw["cached"])
	totals.CacheWrite = protoInt64(raw["cacheWrite"])
	totals.Reasoning = protoInt64(raw["reasoning"])
	if cost := protoMap(raw["cost"]); len(cost) > 0 {
		totals.Cost = make(map[string]float64, len(cost))
		for k, v := range cost {
			if f, ok := protoFloat(v); ok {
				totals.Cost[k] = f
			}
		}
	}
	return totals
}

func toProtoThreadView(v core.ThreadView) *ariadnev1.ThreadView {
	out := &ariadnev1.ThreadView{
		Id:            v.ID,
		ProjectId:     v.ProjectID,
		Title:         v.Title,
		NodeId:        protoOptStr(v.NodeID),
		Stage:         v.Stage,
		LastReport:    protoOptStr(v.LastReport),
		LastSummary:   protoOptStr(v.LastSummary),
		LastHeadline:  protoOptStr(v.LastHeadline),
		Archived:      v.Archived,
		Model:         protoOptStr(v.Model),
		Effort:        protoOptStr(v.Effort),
		BranchSlug:    protoOptStr(v.BranchSlug),
		Usage:         toProtoTokenUsageTotals(v.Usage),
		Doing:         protoOptStr(v.Doing),
		CreatedAt:     v.CreatedAt,
		UpdatedAt:     v.UpdatedAt,
		ActiveAt:      v.ActiveAt,
		ReviewOf:      protoOptStr(v.ReviewOf),
		Auto:          v.Auto,
		AutopilotHeld: protoOptStr(v.AutopilotHeld),
	}
	for _, item := range protoSlice(v.LastArtifacts) {
		m := protoMap(item)
		if m == nil {
			continue
		}
		out.LastArtifacts = append(out.LastArtifacts, &ariadnev1.ThreadArtifactRef{
			Id:    protoStr(m["id"]),
			Kind:  protoStr(m["kind"]),
			Title: protoStr(m["title"]),
		})
	}
	if v.Workspace != nil {
		out.Workspace = &ariadnev1.ThreadWorkspace{
			SourceId: protoOptNonEmptyStr(v.Workspace.SourceID),
			Label:    v.Workspace.Label,
			Branch:   protoOptStr(v.Workspace.Branch),
			Base:     protoOptStr(v.Workspace.Base),
			Removed:  v.Workspace.Removed,
			Parked:   v.Workspace.Parked,
		}
	}
	if m := protoMap(v.Merged); m != nil {
		out.Merged = &ariadnev1.ThreadBranchMerge{
			Branch: protoStr(m["branch"]),
			Base:   protoStr(m["base"]),
			Method: protoStr(m["method"]),
			Sha:    protoStr(m["sha"]),
			Head:   protoStr(m["head"]),
			Auto:   protoBool(m["auto"]),
			At:     protoInt64(m["at"]),
		}
	}
	if m := protoMap(v.Changes); m != nil {
		out.Changes = &ariadnev1.ThreadChangeTotals{
			Files:   protoInt32(m["files"]),
			Added:   protoInt32(m["added"]),
			Removed: protoInt32(m["removed"]),
		}
	}
	if m := protoMap(v.PullRequest); m != nil {
		pr := &ariadnev1.ThreadPullRequest{
			Number:     protoInt32(m["number"]),
			Reference:  protoOptStr(m["reference"]),
			Url:        protoStr(m["url"]),
			Title:      protoStr(m["title"]),
			State:      protoStr(m["state"]),
			Conflicted: protoBool(m["conflicted"]),
			HeadSha:    protoStr(m["headSha"]),
		}
		if checks := protoMap(m["checks"]); checks != nil {
			pr.Checks = &ariadnev1.PullRequestChecksSummary{
				State:  protoStr(checks["state"]),
				Failed: protoStringSlice(checks["failed"]),
			}
		}
		out.PullRequest = pr
	}
	if v.Aside != nil {
		out.Aside = &ariadnev1.ThreadAside{
			Until:  v.Aside.Until,
			Reason: v.Aside.Reason,
			By:     v.Aside.By,
			Due:    v.Aside.Due,
		}
	}
	if v.Stopped != nil {
		out.Stopped = &ariadnev1.ThreadStopped{
			Reason: v.Stopped.Reason,
			By:     v.Stopped.By,
			At:     v.Stopped.At,
		}
	}
	for _, item := range protoSlice(v.References) {
		m := protoMap(item)
		if m == nil {
			continue
		}
		out.References = append(out.References, &ariadnev1.ThreadReference{
			Id:          protoStr(m["id"]),
			Name:        protoStr(m["name"]),
			Kind:        protoStr(m["kind"]),
			Settings:    toStructPB(m["settings"]),
			Writable:    protoOptBool(m["writable"]),
			AttentionId: protoStr(m["attentionId"]),
		})
	}
	for _, item := range protoSlice(v.EndedReferences) {
		m := protoMap(item)
		if m == nil {
			continue
		}
		out.EndedReferences = append(out.EndedReferences, &ariadnev1.ThreadEndedReference{
			AttentionId: protoStr(m["attentionId"]),
			Kept:        protoBool(m["kept"]),
		})
	}
	if v.Progress != nil {
		out.Progress = &ariadnev1.TodoProgress{
			Done:  int32(v.Progress.Done),
			Total: int32(v.Progress.Total),
		}
	}
	if v.Review != nil {
		out.Review = &ariadnev1.ThreadReviewStatus{
			ReviewerId: v.Review.ReviewerID,
			Rounds:     int32(v.Review.Rounds),
			Reviewing:  v.Review.Reviewing,
		}
	}
	return out
}

func toProtoBackgroundCommand(cmd background.Command) *ariadnev1.BackgroundCommandView {
	out := &ariadnev1.BackgroundCommandView{
		Id:        cmd.ID,
		Command:   cmd.Command,
		StartedAt: cmd.StartedAt,
		Url:       cmd.URL,
	}
	if cmd.Result != nil {
		out.Result = &ariadnev1.BackgroundCommandResult{
			ExitCode: int32(cmd.Result.ExitCode),
			EndedAt:  cmd.Result.EndedAt,
			Stopped:  cmd.Result.Stopped,
		}
	}
	return out
}

func toProtoReviewRound(raw map[string]any) *ariadnev1.ReviewRoundView {
	out := &ariadnev1.ReviewRoundView{
		ThreadId:    protoStr(raw["threadId"]),
		ReviewerId:  protoStr(raw["reviewerId"]),
		Round:       protoInt32(raw["round"]),
		Head:        protoOptStr(raw["head"]),
		RequestedAt: protoInt64(raw["requestedAt"]),
		Verdict:     protoOptStr(raw["verdict"]),
		Abandoned:   protoBool(raw["abandoned"]),
		Summary:     protoOptStr(raw["summary"]),
		SubmittedAt: protoOptInt64(raw["submittedAt"]),
	}
	for _, item := range protoSlice(raw["comments"]) {
		m := protoMap(item)
		if m == nil {
			continue
		}
		out.Comments = append(out.Comments, &ariadnev1.ReviewComment{
			Path:  protoStr(m["path"]),
			Line:  protoInt32(m["line"]),
			Quote: protoStr(m["quote"]),
			Body:  protoStr(m["body"]),
			Side:  protoOptStr(m["side"]),
			Kind:  protoStr(m["kind"]),
		})
	}
	if raw["replies"] != nil {
		list := &ariadnev1.ReviewReplyList{}
		for _, item := range protoSlice(raw["replies"]) {
			m := protoMap(item)
			if m == nil {
				continue
			}
			list.Items = append(list.Items, &ariadnev1.ReviewReply{
				Comment: protoInt32(m["comment"]),
				Outcome: protoStr(m["outcome"]),
				Note:    protoStr(m["note"]),
			})
		}
		out.Replies = list
	}
	return out
}

func toProtoSentLineComment(raw map[string]any) *ariadnev1.SentLineComment {
	return &ariadnev1.SentLineComment{
		Id:        protoStr(raw["id"]),
		MessageId: protoStr(raw["messageId"]),
		SentAt:    protoInt64(raw["sentAt"]),
		Path:      protoStr(raw["path"]),
		Line:      protoInt32(raw["line"]),
		Quote:     protoStr(raw["quote"]),
		Body:      protoStr(raw["body"]),
		Side:      protoOptStr(raw["side"]),
	}
}

func toProtoConversationView(v core.ConversationView) *ariadnev1.ConversationView {
	out := &ariadnev1.ConversationView{
		Id:            v.ID,
		ProjectId:     v.ProjectID,
		Role:          v.Role,
		PendingSeq:    v.PendingSeq,
		LastOutcome:   protoOptStr(v.LastOutcome),
		LastError:     toProtoLocalizedError(v.LastError),
		Usage:         toProtoTokenUsageTotals(v.Usage),
		ContextTokens: v.ContextTokens,
		Compactions:   int32(v.Compactions),
	}
	if v.Turn != nil {
		out.Turn = &ariadnev1.ConversationTurn{
			Id:        v.Turn.ID,
			StartedAt: v.Turn.StartedAt,
			Model:     protoOptStr(v.Turn.Model),
			Activity:  protoOptStr(v.Turn.Activity),
		}
	}
	if v.Wait != nil {
		out.Wait = &ariadnev1.ConversationWait{
			Id:    v.Wait.ID,
			Until: v.Wait.Until,
			Note:  v.Wait.Note,
		}
	}
	return out
}

func toProtoConversationModel(raw any) *ariadnev1.ConversationModelInfo {
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	id := protoStr(m["id"])
	if id == "" {
		return nil
	}
	return &ariadnev1.ConversationModelInfo{
		Id:            id,
		ContextTokens: protoInt64(m["contextTokens"]),
		CompactAt:     protoInt64(m["compactAt"]),
		Effort:        protoOptNonEmptyStr(protoStr(m["effort"])),
	}
}

func toProtoTodoView(raw map[string]any) *ariadnev1.TodoView {
	if raw == nil {
		return &ariadnev1.TodoView{}
	}
	out := &ariadnev1.TodoView{
		ConversationId: protoStr(raw["conversationId"]),
		UpdatedAt:      protoInt64(raw["updatedAt"]),
	}
	for _, item := range protoSlice(raw["items"]) {
		m := protoMap(item)
		if m == nil {
			continue
		}
		out.Items = append(out.Items, &ariadnev1.TodoItem{
			Label:  protoStr(m["label"]),
			Status: protoStr(m["status"]),
			Detail: protoOptStr(m["detail"]),
		})
	}
	if pos := protoMap(raw["position"]); pos != nil {
		tp := &ariadnev1.TodoPosition{
			At: protoInt64(pos["at"]),
		}
		if after := protoStr(pos["afterMessageId"]); after != "" {
			tp.Anchor = &ariadnev1.TodoPosition_AfterMessageId{AfterMessageId: after}
		} else {
			tp.Anchor = &ariadnev1.TodoPosition_BeforeFirst{BeforeFirst: true}
		}
		out.Position = tp
	}
	return out
}

func toProtoFormFieldValue(raw any) *ariadnev1.FormFieldValue {
	if raw == nil {
		return &ariadnev1.FormFieldValue{
			Value: &ariadnev1.FormFieldValue_NullValue{NullValue: true},
		}
	}
	switch v := raw.(type) {
	case string:
		return &ariadnev1.FormFieldValue{
			Value: &ariadnev1.FormFieldValue_StringValue{StringValue: v},
		}
	case bool:
		return &ariadnev1.FormFieldValue{
			Value: &ariadnev1.FormFieldValue_BoolValue{BoolValue: v},
		}
	case []string:
		return &ariadnev1.FormFieldValue{
			Value: &ariadnev1.FormFieldValue_StringListValue{
				StringListValue: &ariadnev1.StringListValue{Values: v},
			},
		}
	case []any:
		return &ariadnev1.FormFieldValue{
			Value: &ariadnev1.FormFieldValue_StringListValue{
				StringListValue: &ariadnev1.StringListValue{Values: protoStringSlice(v)},
			},
		}
	default:
		if f, ok := protoFloat(raw); ok {
			return &ariadnev1.FormFieldValue{
				Value: &ariadnev1.FormFieldValue_NumberValue{NumberValue: f},
			}
		}
		return &ariadnev1.FormFieldValue{
			Value: &ariadnev1.FormFieldValue_StringValue{StringValue: fmt.Sprint(raw)},
		}
	}
}

func fromProtoFormFieldValue(v *ariadnev1.FormFieldValue) any {
	if v == nil || v.Value == nil {
		return nil
	}
	switch inner := v.Value.(type) {
	case *ariadnev1.FormFieldValue_StringValue:
		return inner.StringValue
	case *ariadnev1.FormFieldValue_NumberValue:
		return inner.NumberValue
	case *ariadnev1.FormFieldValue_BoolValue:
		return inner.BoolValue
	case *ariadnev1.FormFieldValue_StringListValue:
		if inner.StringListValue == nil {
			return []any{}
		}
		out := make([]any, len(inner.StringListValue.Values))
		for i, s := range inner.StringListValue.Values {
			out[i] = s
		}
		return out
	case *ariadnev1.FormFieldValue_NullValue:
		return nil
	default:
		return nil
	}
}

func toProtoAskBlock(raw any) *ariadnev1.AskBlock {
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	switch protoStr(m["type"]) {
	case "choice":
		block := &ariadnev1.ChoiceBlock{
			Question:    protoStr(m["question"]),
			Recommended: protoOptInt32(m["recommended"]),
		}
		for _, item := range protoSlice(m["options"]) {
			o := protoMap(item)
			if o == nil {
				continue
			}
			block.Options = append(block.Options, &ariadnev1.ChoiceOption{
				Label:       protoStr(o["label"]),
				Consequence: protoStr(o["consequence"]),
			})
		}
		return &ariadnev1.AskBlock{Block: &ariadnev1.AskBlock_Choice{Choice: block}}
	case "confirm":
		return &ariadnev1.AskBlock{
			Block: &ariadnev1.AskBlock_Confirm{
				Confirm: &ariadnev1.ConfirmBlock{
					Title:                 protoStr(m["title"]),
					Description:           protoOptStr(m["description"]),
					Detail:                protoOptStr(m["detail"]),
					Tone:                  protoStr(m["tone"]),
					ApproveText:           protoOptStr(m["approveText"]),
					DenyText:              protoOptStr(m["denyText"]),
					AllowProjectReference: protoBool(m["allowProjectReference"]),
				},
			},
		}
	case "form":
		block := &ariadnev1.FormBlock{
			Title:       protoStr(m["title"]),
			Description: protoOptStr(m["description"]),
			SubmitText:  protoOptStr(m["submitText"]),
		}
		for _, item := range protoSlice(m["fields"]) {
			f := protoMap(item)
			if f == nil {
				continue
			}
			field := &ariadnev1.FormField{
				Key:          protoStr(f["key"]),
				Label:        protoStr(f["label"]),
				Kind:         protoStr(f["kind"]),
				Required:     protoBool(f["required"]),
				Placeholder:  protoOptStr(f["placeholder"]),
				DefaultValue: toProtoFormFieldValue(f["default"]),
				Min:          protoOptFloat64(f["min"]),
				Max:          protoOptFloat64(f["max"]),
			}
			for _, opt := range protoSlice(f["options"]) {
				om := protoMap(opt)
				if om == nil {
					continue
				}
				field.Options = append(field.Options, &ariadnev1.FormSelectOption{
					Label: protoStr(om["label"]),
					Value: protoStr(om["value"]),
				})
			}
			block.Fields = append(block.Fields, field)
		}
		return &ariadnev1.AskBlock{Block: &ariadnev1.AskBlock_Form{Form: block}}
	}
	return nil
}

func toProtoBlockAnswer(raw any) *ariadnev1.BlockAnswer {
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	switch protoStr(m["type"]) {
	case "choice":
		return &ariadnev1.BlockAnswer{
			Answer: &ariadnev1.BlockAnswer_Choice{
				Choice: &ariadnev1.ChoiceAnswer{Choice: protoInt32(m["choice"])},
			},
		}
	case "confirm":
		return &ariadnev1.BlockAnswer{
			Answer: &ariadnev1.BlockAnswer_Confirm{
				Confirm: &ariadnev1.ConfirmAnswer{
					Approved: protoBool(m["approved"]),
					Scope:    protoOptStr(m["scope"]),
				},
			},
		}
	case "form":
		values := map[string]*ariadnev1.FormFieldValue{}
		for k, v := range protoMap(m["values"]) {
			values[k] = toProtoFormFieldValue(v)
		}
		return &ariadnev1.BlockAnswer{
			Answer: &ariadnev1.BlockAnswer_Form{
				Form: &ariadnev1.FormAnswer{Values: values},
			},
		}
	}
	return nil
}

func fromProtoBlockAnswer(a *ariadnev1.BlockAnswer) any {
	if a == nil || a.Answer == nil {
		return nil
	}
	switch inner := a.Answer.(type) {
	case *ariadnev1.BlockAnswer_Choice:
		if inner.Choice == nil {
			return nil
		}
		return object{"type": "choice", "choice": float64(inner.Choice.Choice)}
	case *ariadnev1.BlockAnswer_Confirm:
		if inner.Confirm == nil {
			return nil
		}
		res := object{"type": "confirm", "approved": inner.Confirm.Approved}
		if inner.Confirm.Scope != nil {
			res["scope"] = *inner.Confirm.Scope
		}
		return res
	case *ariadnev1.BlockAnswer_Form:
		if inner.Form == nil {
			return nil
		}
		values := object{}
		for k, v := range inner.Form.Values {
			values[k] = fromProtoFormFieldValue(v)
		}
		return object{"type": "form", "values": values}
	default:
		return nil
	}
}

func toProtoShowBlock(raw any) *ariadnev1.ShowBlock {
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	switch protoStr(m["type"]) {
	case "table":
		block := &ariadnev1.TableBlock{
			Title: protoOptStr(m["title"]),
		}
		for _, item := range protoSlice(m["columns"]) {
			c := protoMap(item)
			if c == nil {
				continue
			}
			block.Columns = append(block.Columns, &ariadnev1.TableColumn{
				Key:   protoStr(c["key"]),
				Title: protoStr(c["title"]),
				Align: protoStr(c["align"]),
			})
		}
		for _, item := range protoSlice(m["rows"]) {
			r := protoMap(item)
			if r == nil {
				continue
			}
			cells := make(map[string]*ariadnev1.TableCellValue, len(r))
			for k, v := range r {
				if v == nil {
					cells[k] = &ariadnev1.TableCellValue{Value: &ariadnev1.TableCellValue_NullValue{NullValue: true}}
				} else if s, ok := v.(string); ok {
					cells[k] = &ariadnev1.TableCellValue{Value: &ariadnev1.TableCellValue_StringValue{StringValue: s}}
				} else if b, ok := v.(bool); ok {
					cells[k] = &ariadnev1.TableCellValue{Value: &ariadnev1.TableCellValue_BoolValue{BoolValue: b}}
				} else if f, ok := protoFloat(v); ok {
					cells[k] = &ariadnev1.TableCellValue{Value: &ariadnev1.TableCellValue_NumberValue{NumberValue: f}}
				} else {
					cells[k] = &ariadnev1.TableCellValue{Value: &ariadnev1.TableCellValue_StringValue{StringValue: fmt.Sprint(v)}}
				}
			}
			block.Rows = append(block.Rows, &ariadnev1.TableRow{Cells: cells})
		}
		return &ariadnev1.ShowBlock{Block: &ariadnev1.ShowBlock_Table{Table: block}}
	case "chart":
		block := &ariadnev1.ChartBlock{
			Title:      protoOptStr(m["title"]),
			Kind:       protoStr(m["kind"]),
			X:          protoStr(m["x"]),
			Y:          protoStringSlice(m["y"]),
			Labels:     protoStringMap(m["labels"]),
			Stacked:    protoBool(m["stacked"]),
			Horizontal: protoBool(m["horizontal"]),
		}
		for _, item := range protoSlice(m["rows"]) {
			r := protoMap(item)
			if r == nil {
				continue
			}
			cells := make(map[string]*ariadnev1.ChartCellValue, len(r))
			for k, v := range r {
				if v == nil {
					cells[k] = &ariadnev1.ChartCellValue{Value: &ariadnev1.ChartCellValue_NullValue{NullValue: true}}
				} else if s, ok := v.(string); ok {
					cells[k] = &ariadnev1.ChartCellValue{Value: &ariadnev1.ChartCellValue_StringValue{StringValue: s}}
				} else if f, ok := protoFloat(v); ok {
					cells[k] = &ariadnev1.ChartCellValue{Value: &ariadnev1.ChartCellValue_NumberValue{NumberValue: f}}
				} else {
					cells[k] = &ariadnev1.ChartCellValue{Value: &ariadnev1.ChartCellValue_StringValue{StringValue: fmt.Sprint(v)}}
				}
			}
			block.Rows = append(block.Rows, &ariadnev1.ChartRow{Cells: cells})
		}
		return &ariadnev1.ShowBlock{Block: &ariadnev1.ShowBlock_Chart{Chart: block}}
	case "steps":
		block := &ariadnev1.StepsBlock{
			Title: protoOptStr(m["title"]),
		}
		for _, item := range protoSlice(m["items"]) {
			s := protoMap(item)
			if s == nil {
				continue
			}
			block.Items = append(block.Items, &ariadnev1.StepItem{
				Title:       protoStr(s["title"]),
				Description: protoOptStr(s["description"]),
				Status:      protoStr(s["status"]),
			})
		}
		return &ariadnev1.ShowBlock{Block: &ariadnev1.ShowBlock_Steps{Steps: block}}
	case "thread":
		return &ariadnev1.ShowBlock{
			Block: &ariadnev1.ShowBlock_Thread{
				Thread: &ariadnev1.ThreadBlock{ThreadId: protoStr(m["threadId"])},
			},
		}
	case "connector":
		return &ariadnev1.ShowBlock{
			Block: &ariadnev1.ShowBlock_Connector{
				Connector: &ariadnev1.ConnectorBlock{
					Name:      protoStr(m["name"]),
					Why:       protoOptStr(m["why"]),
					Transport: protoStr(m["transport"]),
					Command:   protoOptStr(m["command"]),
					Args:      protoStringSlice(m["args"]),
					Url:       protoOptStr(m["url"]),
					Secrets:   protoStringSlice(m["secrets"]),
				},
			},
		}
	}
	return nil
}

func toProtoAttachments(raw any) []*ariadnev1.MessageAttachment {
	items := protoSlice(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]*ariadnev1.MessageAttachment, 0, len(items))
	for _, item := range items {
		m := protoMap(item)
		if m == nil {
			continue
		}
		out = append(out, &ariadnev1.MessageAttachment{
			FileId: protoStr(m["fileId"]),
			Name:   protoStr(m["name"]),
			Mime:   protoStr(m["mime"]),
			Size:   protoInt64(m["size"]),
		})
	}
	return out
}

func toProtoMentions(raw any) []*ariadnev1.MessageMention {
	items := protoSlice(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]*ariadnev1.MessageMention, 0, len(items))
	for _, item := range items {
		m := protoMap(item)
		if m == nil {
			continue
		}
		switch protoStr(m["kind"]) {
		case "thread":
			out = append(out, &ariadnev1.MessageMention{
				Mention: &ariadnev1.MessageMention_Thread{
					Thread: &ariadnev1.ThreadMention{Id: protoStr(m["id"]), Title: protoStr(m["title"])},
				},
			})
		case "artifact":
			out = append(out, &ariadnev1.MessageMention{
				Mention: &ariadnev1.MessageMention_Artifact{
					Artifact: &ariadnev1.ArtifactMention{Id: protoStr(m["id"]), Title: protoStr(m["title"])},
				},
			})
		case "file":
			out = append(out, &ariadnev1.MessageMention{
				Mention: &ariadnev1.MessageMention_File{
					File: &ariadnev1.FileMention{Path: protoStr(m["path"])},
				},
			})
		case "skill":
			out = append(out, &ariadnev1.MessageMention{
				Mention: &ariadnev1.MessageMention_Skill{
					Skill: &ariadnev1.SkillMention{Name: protoStr(m["name"])},
				},
			})
		}
	}
	return out
}

func toProtoMessageView(v core.MessageView) *ariadnev1.MessageView {
	out := &ariadnev1.MessageView{
		Id:              v.ID,
		ConversationId:  v.ConversationID,
		Author:          v.Author,
		Kind:            v.Kind,
		Content:         v.Content,
		Attachments:     toProtoAttachments(v.Attachments),
		Mentions:        toProtoMentions(v.Mentions),
		CreatedAt:       v.CreatedAt,
		EditedAt:        protoOptInt64(v.EditedAt),
		EditReason:      protoOptStr(v.EditReason),
		OriginalContent: protoOptStr(v.OriginalContent),
		EditedSinceRead: v.EditedSinceRead,
	}
	for _, raw := range protoSlice(v.Blocks) {
		if b := toProtoShowBlock(raw); b != nil {
			out.Blocks = append(out.Blocks, b)
		}
	}
	if ask := protoMap(v.Ask); ask != nil {
		out.Ask = &ariadnev1.MessageAsk{
			AttentionId: protoStr(ask["attentionId"]),
			Block:       toProtoAskBlock(ask["block"]),
			Answer:      toProtoBlockAnswer(ask["answer"]),
			Withdrawn:   protoOptStr(ask["withdrawn"]),
		}
	}
	for _, raw := range protoSlice(v.Comments) {
		m := protoMap(raw)
		if m == nil {
			continue
		}
		out.Comments = append(out.Comments, &ariadnev1.LineComment{
			Path:  protoStr(m["path"]),
			Line:  protoInt32(m["line"]),
			Quote: protoStr(m["quote"]),
			Body:  protoStr(m["body"]),
			Side:  protoOptStr(m["side"]),
		})
	}
	for _, raw := range protoSlice(v.Cites) {
		m := protoMap(raw)
		if m == nil {
			continue
		}
		out.Cites = append(out.Cites, &ariadnev1.CitedMessage{
			MessageId:      protoStr(m["messageId"]),
			ConversationId: protoStr(m["conversationId"]),
			Content:        protoStr(m["content"]),
			At:             protoInt64(m["at"]),
			Attachments:    toProtoAttachments(m["attachments"]),
			Mentions:       toProtoMentions(m["mentions"]),
		})
	}
	for _, raw := range protoSlice(v.RelayedTo) {
		m := protoMap(raw)
		if m == nil {
			continue
		}
		out.RelayedTo = append(out.RelayedTo, &ariadnev1.MessageRelay{
			ThreadId:  protoStr(m["threadId"]),
			MessageId: protoStr(m["messageId"]),
		})
	}
	if reply := protoMap(v.ReplyTo); reply != nil {
		out.ReplyTo = &ariadnev1.QuotedMessage{
			MessageId: protoStr(reply["messageId"]),
			Author:    protoStr(reply["author"]),
			Text:      protoStr(reply["text"]),
		}
	}
	for _, raw := range protoSlice(v.Reactions) {
		m := protoMap(raw)
		if m == nil {
			continue
		}
		out.Reactions = append(out.Reactions, &ariadnev1.MessageReaction{
			Emoji: protoStr(m["emoji"]),
			By:    protoStr(m["by"]),
		})
	}
	return out
}

func toProtoArtifactFeedbackItem(raw any) *ariadnev1.ArtifactFeedbackItem {
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	switch protoStr(m["kind"]) {
	case "edit":
		return &ariadnev1.ArtifactFeedbackItem{
			Item: &ariadnev1.ArtifactFeedbackItem_Edit{
				Edit: &ariadnev1.ArtifactFeedbackEditItem{Diff: protoStr(m["diff"])},
			},
		}
	case "choice":
		choice := &ariadnev1.ArtifactFeedbackChoiceItem{}
		for _, item := range protoSlice(m["chosen"]) {
			cm := protoMap(item)
			if cm == nil {
				continue
			}
			choice.Chosen = append(choice.Chosen, &ariadnev1.ArtifactFeedbackChoiceOption{
				Id:         protoStr(cm["id"]),
				Title:      protoStr(cm["title"]),
				Group:      protoOptStr(cm["group"]),
				GroupTitle: protoOptStr(cm["groupTitle"]),
			})
		}
		return &ariadnev1.ArtifactFeedbackItem{Item: &ariadnev1.ArtifactFeedbackItem_Choice{Choice: choice}}
	case "comment":
		return &ariadnev1.ArtifactFeedbackItem{
			Item: &ariadnev1.ArtifactFeedbackItem_Comment{
				Comment: &ariadnev1.ArtifactFeedbackCommentItem{
					ThreadId: protoStr(m["threadId"]),
					Action:   protoStr(m["action"]),
					Body:     protoStr(m["body"]),
					Where:    protoStr(m["where"]),
				},
			},
		}
	case "mark":
		return &ariadnev1.ArtifactFeedbackItem{
			Item: &ariadnev1.ArtifactFeedbackItem_Mark{
				Mark: &ariadnev1.ArtifactFeedbackMarkItem{
					NodeId: protoStr(m["nodeId"]),
					Type:   protoStr(m["type"]),
					Text:   protoStr(m["text"]),
					Where:  protoStr(m["where"]),
				},
			},
		}
	case "board":
		return &ariadnev1.ArtifactFeedbackItem{
			Item: &ariadnev1.ArtifactFeedbackItem_Board{
				Board: &ariadnev1.ArtifactFeedbackBoardItem{
					Changes: protoStringSlice(m["changes"]),
				},
			},
		}
	}
	return nil
}

func toProtoNoticeView(raw map[string]any) *ariadnev1.NoticeView {
	out := &ariadnev1.NoticeView{
		Id:             protoStr(raw["id"]),
		ConversationId: protoStr(raw["conversationId"]),
		CreatedAt:      protoInt64(raw["createdAt"]),
	}
	switch protoStr(raw["kind"]) {
	case "approval":
		out.Payload = &ariadnev1.NoticeView_Approval{
			Approval: &ariadnev1.ApprovalNotice{
				Block:     toProtoAskBlock(raw["block"]),
				Answer:    toProtoBlockAnswer(raw["answer"]),
				Withdrawn: protoOptStr(raw["withdrawn"]),
			},
		}
	case "memory":
		out.Payload = &ariadnev1.NoticeView_Memory{
			Memory: &ariadnev1.MemoryNotice{
				Action:   protoStr(raw["action"]),
				MemoryId: protoStr(raw["memoryId"]),
				Text:     protoStr(raw["text"]),
				Previous: protoOptStr(raw["previous"]),
				Undone:   protoBool(raw["undone"]),
			},
		}
	case "memory-everywhere":
		out.Payload = &ariadnev1.NoticeView_MemoryEverywhere{
			MemoryEverywhere: &ariadnev1.MemoryEverywhereNotice{
				Action: protoStr(raw["action"]),
				Text:   protoStr(raw["text"]),
				Undone: protoBool(raw["undone"]),
			},
		}
	case "compaction-failed":
		out.Payload = &ariadnev1.NoticeView_CompactionFailed{
			CompactionFailed: &ariadnev1.CompactionFailedNotice{
				Detail: protoStr(raw["detail"]),
			},
		}
	case "routine-change":
		out.Payload = &ariadnev1.NoticeView_RoutineChange{
			RoutineChange: &ariadnev1.RoutineChangeNotice{
				Action: protoStr(raw["action"]),
				Prompt: protoStr(raw["prompt"]),
			},
		}
	case "recap":
		out.Payload = &ariadnev1.NoticeView_Recap{
			Recap: &ariadnev1.RecapNotice{
				Since:   protoInt64(raw["since"]),
				Done:    protoStringSlice(raw["done"]),
				Merged:  protoStringSlice(raw["merged"]),
				Waiting: protoStringSlice(raw["waiting"]),
				Asks:    protoStringSlice(raw["asks"]),
			},
		}
	case "model-retry":
		out.Payload = &ariadnev1.NoticeView_ModelRetry{
			ModelRetry: &ariadnev1.ModelRetryNotice{
				Attempt: protoInt32(raw["attempt"]),
				DelayMs: protoInt64(raw["delayMs"]),
				Detail:  protoStr(raw["detail"]),
			},
		}
	case "model-fallback":
		out.Payload = &ariadnev1.NoticeView_ModelFallback{
			ModelFallback: &ariadnev1.ModelFallbackNotice{
				From:   protoStr(raw["from"]),
				To:     protoStr(raw["to"]),
				Detail: protoStr(raw["detail"]),
			},
		}
	case "woken":
		out.Payload = &ariadnev1.NoticeView_Woken{
			Woken: &ariadnev1.WokenNotice{
				How:  protoStr(raw["how"]),
				Note: protoStr(raw["note"]),
			},
		}
	case "routine":
		out.Payload = &ariadnev1.NoticeView_Routine{
			Routine: &ariadnev1.RoutineNotice{
				RoutineId: protoStr(raw["routineId"]),
				Prompt:    protoStr(raw["prompt"]),
				DueAt:     protoInt64(raw["dueAt"]),
			},
		}
	case "issue-linked":
		out.Payload = &ariadnev1.NoticeView_IssueLinked{
			IssueLinked: &ariadnev1.IssueLinkedNotice{
				NodeId:    protoStr(raw["nodeId"]),
				NodeTitle: protoStr(raw["nodeTitle"]),
				Tracker:   protoStr(raw["tracker"]),
				Number:    protoInt32(raw["number"]),
				Linked:    protoBool(raw["linked"]),
			},
		}
	case "issue-written":
		out.Payload = &ariadnev1.NoticeView_IssueWritten{
			IssueWritten: &ariadnev1.IssueWrittenNotice{
				Tracker: protoStr(raw["tracker"]),
				Number:  protoInt32(raw["number"]),
				Action:  protoStr(raw["action"]),
				Text:    protoStr(raw["text"]),
				Url:     protoOptStr(raw["url"]),
			},
		}
	case "merged":
		out.Payload = &ariadnev1.NoticeView_Merged{
			Merged: &ariadnev1.MergedNotice{
				Base:        protoStr(raw["base"]),
				Method:      protoStr(raw["method"]),
				Sha:         protoStr(raw["sha"]),
				Auto:        protoBool(raw["auto"]),
				PullRequest: protoOptInt32(raw["pullRequest"]),
				Undone:      protoBool(raw["undone"]),
			},
		}
	case "autopilot-held":
		out.Payload = &ariadnev1.NoticeView_AutopilotHeld{
			AutopilotHeld: &ariadnev1.AutopilotHeldNotice{
				Reason: protoStr(raw["reason"]),
				Detail: protoStr(raw["detail"]),
			},
		}
	case "artifact-edited":
		out.Payload = &ariadnev1.NoticeView_ArtifactEdited{
			ArtifactEdited: &ariadnev1.ArtifactEditedNotice{
				ArtifactId: protoStr(raw["artifactId"]),
				Title:      protoStr(raw["title"]),
				Version:    protoInt32(raw["version"]),
			},
		}
	case "artifact-feedback":
		fb := &ariadnev1.ArtifactFeedbackNotice{
			ArtifactId: protoStr(raw["artifactId"]),
			Title:      protoStr(raw["title"]),
			Round:      protoInt32(raw["round"]),
			Done:       protoBool(raw["done"]),
			Summary:    protoOptStr(raw["summary"]),
			Withdrawn:  protoBool(raw["withdrawn"]),
		}
		for _, item := range protoSlice(raw["items"]) {
			if converted := toProtoArtifactFeedbackItem(item); converted != nil {
				fb.Items = append(fb.Items, converted)
			}
		}
		out.Payload = &ariadnev1.NoticeView_ArtifactFeedback{ArtifactFeedback: fb}
	case "artifact-comment":
		out.Payload = &ariadnev1.NoticeView_ArtifactComment{
			ArtifactComment: &ariadnev1.ArtifactCommentNotice{
				ArtifactId: protoStr(raw["artifactId"]),
				Title:      protoStr(raw["title"]),
				By:         protoStr(raw["by"]),
				Action:     protoStr(raw["action"]),
				Body:       protoStr(raw["body"]),
			},
		}
	case "merge-conflict":
		out.Payload = &ariadnev1.NoticeView_MergeConflict{
			MergeConflict: &ariadnev1.MergeConflictNotice{
				Base:        protoStr(raw["base"]),
				Files:       protoStringSlice(raw["files"]),
				PullRequest: protoOptInt32(raw["pullRequest"]),
			},
		}
	case "worktree-removed":
		out.Payload = &ariadnev1.NoticeView_WorktreeRemoved{
			WorktreeRemoved: &ariadnev1.WorktreeRemovedNotice{
				Reason: protoStr(raw["reason"]),
			},
		}
	case "plan-changed":
		out.Payload = &ariadnev1.NoticeView_PlanChanged{
			PlanChanged: &ariadnev1.PlanChangedNotice{
				RevisionId: protoStr(raw["revisionId"]),
				Reason:     protoStr(raw["reason"]),
				Added:      protoInt32(raw["added"]),
				Revert:     protoBool(raw["revert"]),
			},
		}
	case "thread-auto-started":
		out.Payload = &ariadnev1.NoticeView_ThreadAutoStarted{
			ThreadAutoStarted: &ariadnev1.ThreadAutoStartedNotice{
				ThreadId: protoStr(raw["threadId"]),
				Title:    protoStr(raw["title"]),
			},
		}
	case "review":
		out.Payload = &ariadnev1.NoticeView_Review{
			Review: &ariadnev1.ReviewNotice{
				ThreadId:   protoStr(raw["threadId"]),
				ReviewerId: protoStr(raw["reviewerId"]),
				Round:      protoInt32(raw["round"]),
				State:      protoStr(raw["state"]),
				Count:      protoInt32(raw["count"]),
			},
		}
	case "turn-changes":
		tc := &ariadnev1.TurnChangesNotice{
			TurnId:    protoStr(raw["turnId"]),
			From:      protoStr(raw["from"]),
			To:        protoStr(raw["to"]),
			StartedAt: protoOptInt64(raw["startedAt"]),
		}
		for _, item := range protoSlice(raw["files"]) {
			fm := protoMap(item)
			if fm == nil {
				continue
			}
			tc.Files = append(tc.Files, &ariadnev1.TurnFile{
				Path:         protoStr(fm["path"]),
				PreviousPath: protoOptStr(fm["previousPath"]),
				Status:       protoStr(fm["status"]),
				Added:        protoOptInt32(fm["added"]),
				Removed:      protoOptInt32(fm["removed"]),
			})
		}
		if total := protoMap(raw["total"]); total != nil {
			tc.Total = &ariadnev1.ThreadChangeTotals{
				Files:   protoInt32(total["files"]),
				Added:   protoInt32(total["added"]),
				Removed: protoInt32(total["removed"]),
			}
		}
		out.Payload = &ariadnev1.NoticeView_TurnChanges{TurnChanges: tc}
	}
	return out
}

func toProtoMemoryView(m core.MemoryView) *ariadnev1.MemoryView {
	return &ariadnev1.MemoryView{
		Id:        m.ID,
		ProjectId: m.ProjectID,
		Text:      m.Text,
		Author:    m.Author,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func toProtoSkillView(s skills.Skill) *ariadnev1.SkillView {
	return &ariadnev1.SkillView{
		Name:   s.Name,
		When:   s.When,
		Source: s.Source,
	}
}

func toProtoTraceTokenUsage(raw any) *ariadnev1.TraceTokenUsage {
	m := protoMap(raw)
	if m == nil {
		return nil
	}
	out := &ariadnev1.TraceTokenUsage{
		ContextInput: protoOptInt64(m["contextInput"]),
		Input:        protoInt64(m["input"]),
		Output:       protoInt64(m["output"]),
		Cached:       protoOptInt64(m["cached"]),
		CacheWrite:   protoOptInt64(m["cacheWrite"]),
		Reasoning:    protoOptInt64(m["reasoning"]),
	}
	if cost := protoMap(m["cost"]); cost != nil {
		if amount, ok := protoFloat(cost["amount"]); ok {
			out.Cost = &ariadnev1.TraceCost{
				Amount:   amount,
				Currency: protoStr(cost["currency"]),
			}
		}
	}
	return out
}

func toProtoTraceRequest(raw map[string]any) *ariadnev1.TraceRequest {
	req := &ariadnev1.TraceRequest{
		Seq:           protoInt64(raw["seq"]),
		Number:        protoInt32(raw["number"]),
		Purpose:       protoStr(raw["purpose"]),
		Provider:      protoOptStr(raw["provider"]),
		Model:         protoOptStr(raw["model"]),
		Effort:        protoOptStr(raw["effort"]),
		StartedAt:     protoInt64(raw["startedAt"]),
		FirstOutputAt: protoOptInt64(raw["firstOutputAt"]),
		FinishedAt:    protoInt64(raw["finishedAt"]),
		Usage:         toProtoTraceTokenUsage(raw["usage"]),
		Finish:        protoStr(raw["finish"]),
		Text:          protoStr(raw["text"]),
	}
	for _, item := range protoSlice(raw["reasoning"]) {
		rm := protoMap(item)
		if rm == nil {
			continue
		}
		req.Reasoning = append(req.Reasoning, &ariadnev1.TraceReasoning{
			Text:     protoStr(rm["text"]),
			Redacted: protoBool(rm["redacted"]),
		})
	}
	for _, item := range protoSlice(raw["toolCalls"]) {
		tm := protoMap(item)
		if tm == nil {
			continue
		}
		tc := &ariadnev1.TraceToolCall{
			Id:        protoStr(tm["id"]),
			Name:      protoStr(tm["name"]),
			Arguments: protoStr(tm["arguments"]),
			StartedAt: protoInt64(tm["startedAt"]),
		}
		if res := protoMap(tm["result"]); res != nil {
			tc.Result = &ariadnev1.TraceToolCallResult{
				Content:    protoStr(res["content"]),
				IsError:    protoBool(res["isError"]),
				FinishedAt: protoInt64(res["finishedAt"]),
				Files:      toProtoAttachments(res["files"]),
			}
		}
		for _, rItem := range protoSlice(tm["research"]) {
			if rm := protoMap(rItem); rm != nil {
				tc.Research = append(tc.Research, toProtoTraceRequest(rm))
			}
		}
		req.ToolCalls = append(req.ToolCalls, tc)
	}
	return req
}

func toProtoTraceTurn(raw map[string]any) *ariadnev1.TraceTurn {
	out := &ariadnev1.TraceTurn{
		TurnId:     protoStr(raw["turnId"]),
		Seq:        protoInt64(raw["seq"]),
		Model:      protoStr(raw["model"]),
		StartedAt:  protoInt64(raw["startedAt"]),
		FinishedAt: protoOptInt64(raw["finishedAt"]),
		Outcome:    protoOptStr(raw["outcome"]),
		Error:      toProtoLocalizedError(raw["error"]),
		Usage:      toProtoTraceTokenUsage(raw["usage"]),
	}
	for _, item := range protoSlice(raw["items"]) {
		m := protoMap(item)
		if m == nil {
			continue
		}
		switch protoStr(m["kind"]) {
		case "request":
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_Request{Request: toProtoTraceRequest(m)},
			})
		case "request-failed":
			fail := &ariadnev1.TraceRequestFailure{
				Seq:        protoInt64(m["seq"]),
				Purpose:    protoStr(m["purpose"]),
				Provider:   protoOptStr(m["provider"]),
				Model:      protoOptStr(m["model"]),
				Attempt:    protoInt32(m["attempt"]),
				StartedAt:  protoInt64(m["startedAt"]),
				FinishedAt: protoInt64(m["finishedAt"]),
				Detail:     protoStr(m["detail"]),
				Retryable:  protoBool(m["retryable"]),
				WillRetry:  protoBool(m["willRetry"]),
			}
			if ae := protoMap(m["agentError"]); ae != nil {
				fail.AgentError = &ariadnev1.TraceAgentError{
					Operation:       protoStr(ae["operation"]),
					Executable:      protoOptStr(ae["executable"]),
					Name:            protoOptStr(ae["name"]),
					Version:         protoOptStr(ae["version"]),
					ProtocolVersion: protoOptInt32(ae["protocolVersion"]),
					SessionMethod:   protoOptStr(ae["sessionMethod"]),
					RpcCode:         protoOptInt32(ae["rpcCode"]),
					RpcData:         protoOptStr(ae["rpcData"]),
					StopReason:      protoOptStr(ae["stopReason"]),
					Stderr:          protoOptStr(ae["stderr"]),
				}
			}
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_RequestFailed{RequestFailed: fail},
			})
		case "retry":
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_Retry{
					Retry: &ariadnev1.TraceRetry{
						At:      protoInt64(m["at"]),
						Attempt: protoInt32(m["attempt"]),
						DelayMs: protoInt64(m["delayMs"]),
						Detail:  protoStr(m["detail"]),
					},
				},
			})
		case "fallback":
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_Fallback{
					Fallback: &ariadnev1.TraceFallback{
						At:     protoInt64(m["at"]),
						From:   protoStr(m["from"]),
						To:     protoStr(m["to"]),
						Detail: protoStr(m["detail"]),
					},
				},
			})
		case "compaction-failed":
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_CompactionFailed{
					CompactionFailed: &ariadnev1.TraceCompactionFailed{
						At:     protoInt64(m["at"]),
						Detail: protoStr(m["detail"]),
					},
				},
			})
		case "results-cleared":
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_ResultsCleared{
					ResultsCleared: &ariadnev1.TraceResultsCleared{At: protoInt64(m["at"])},
				},
			})
		case "instructions":
			out.Items = append(out.Items, &ariadnev1.TraceItem{
				Item: &ariadnev1.TraceItem_Instructions{
					Instructions: &ariadnev1.TraceInstructions{
						At:          protoInt64(m["at"]),
						Path:        protoStr(m["path"]),
						Changed:     protoBool(m["changed"]),
						Removed:     protoBool(m["removed"]),
						Uncommitted: protoBool(m["uncommitted"]),
					},
				},
			})
		}
	}
	return out
}

func toProtoTranscriptAttachments(files []transcript.Attachment) []*ariadnev1.MessageAttachment {
	if len(files) == 0 {
		return nil
	}
	out := make([]*ariadnev1.MessageAttachment, 0, len(files))
	for _, f := range files {
		out = append(out, &ariadnev1.MessageAttachment{
			FileId: f.FileID,
			Name:   f.Name,
			Mime:   f.MIME,
			Size:   f.Size,
		})
	}
	return out
}

func toProtoTraceRequestDetail(detail trace.RequestDetail, body any) *ariadnev1.TraceRequestDetail {
	out := &ariadnev1.TraceRequestDetail{
		Effort:        detail.Effort,
		Body:          toValuePB(body),
		NewFrom:       int32(detail.NewFrom),
		SystemChanged: detail.SystemChanged,
	}
	for _, msg := range detail.Messages {
		switch msg.Role {
		case "system":
			out.Messages = append(out.Messages, &ariadnev1.TraceMessage{
				Message: &ariadnev1.TraceMessage_System{
					System: &ariadnev1.TraceSystemMessage{Content: msg.Content},
				},
			})
		case "user":
			out.Messages = append(out.Messages, &ariadnev1.TraceMessage{
				Message: &ariadnev1.TraceMessage_User{
					User: &ariadnev1.TraceUserMessage{
						Content: msg.Content,
						Images:  toProtoTranscriptAttachments(msg.Images),
					},
				},
			})
		case "assistant":
			am := &ariadnev1.TraceAssistantMessage{Content: msg.Content}
			for _, tc := range msg.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, &ariadnev1.TraceToolCallRecord{
					Id:        tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
			}
			for _, r := range msg.Reasoning {
				rr := &ariadnev1.TraceReasoningRecord{
					Text:      r.Text,
					Signature: protoOptNonEmptyStr(r.Signature),
				}
				if r.Redacted {
					redacted := true
					rr.Redacted = &redacted
				}
				am.Reasoning = append(am.Reasoning, rr)
			}
			out.Messages = append(out.Messages, &ariadnev1.TraceMessage{
				Message: &ariadnev1.TraceMessage_Assistant{Assistant: am},
			})
		case "tool":
			tm := &ariadnev1.TraceToolMessage{
				ToolCallId: msg.ToolCallID,
				Content:    msg.Content,
				Images:     toProtoTranscriptAttachments(msg.Images),
			}
			if msg.IsError {
				isErr := true
				tm.IsError = &isErr
			}
			out.Messages = append(out.Messages, &ariadnev1.TraceMessage{
				Message: &ariadnev1.TraceMessage_Tool{Tool: tm},
			})
		}
	}
	for _, tool := range detail.Tools {
		out.Tools = append(out.Tools, &ariadnev1.TraceToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  toStructPB(tool.Parameters),
		})
	}
	if detail.Call != nil {
		out.Call = &ariadnev1.TraceRequestCall{
			Provider: detail.Call.Provider,
			Model:    detail.Call.Model,
			Settings: toStructPB(detail.Call.Settings),
		}
	}
	return out
}

func toProtoBranchChanges(changes *sdk.BranchChanges) *ariadnev1.BranchChanges {
	if changes == nil {
		return nil
	}
	out := &ariadnev1.BranchChanges{
		Branch:      changes.Branch,
		Base:        changes.Base,
		From:        changes.From,
		Head:        changes.Head,
		BaseHead:    protoOptNonEmptyStr(changes.BaseHead),
		Uncommitted: changes.Uncommitted,
	}
	for _, c := range changes.Commits {
		out.Commits = append(out.Commits, &ariadnev1.BranchCommit{
			Sha:     c.SHA,
			Subject: c.Subject,
		})
	}
	for _, f := range changes.Files {
		out.Files = append(out.Files, &ariadnev1.ChangedFile{
			Path:         f.Path,
			PreviousPath: f.PreviousPath,
			Status:       f.Status,
			Added:        f.Added,
			Removed:      f.Removed,
		})
	}
	return out
}

func toProtoMergeOutcome(outcome sdk.MergeOutcome) *ariadnev1.MergeOutcome {
	out := &ariadnev1.MergeOutcome{
		Kind:  outcome.Kind,
		Sha:   protoOptNonEmptyStr(outcome.SHA),
		Head:  protoOptNonEmptyStr(outcome.Head),
		Files: outcome.Files,
	}
	return out
}

func toProtoProjectWorktrees(project workspaces.ProjectWorktrees) *ariadnev1.ProjectWorktrees {
	out := &ariadnev1.ProjectWorktrees{
		Id:     project.ID,
		Name:   project.Name,
		Shared: project.Shared,
		Error:  toProtoLocalizedError(project.Error),
	}
	for _, wt := range project.Worktrees {
		item := &ariadnev1.ListedWorktree{
			ThreadId: wt.ThreadID,
			Path:     wt.Path,
			Bytes:    wt.Bytes,
			Orphaned: wt.Orphaned,
			SourceId: protoOptNonEmptyStr(wt.SourceID),
		}
		if wt.Thread != nil {
			item.Thread = toProtoThreadView(*wt.Thread)
		}
		out.Worktrees = append(out.Worktrees, item)
	}
	return out
}
