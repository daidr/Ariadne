package server

import (
	"context"
	"math"
	"net/http"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/skills"
	"github.com/daidr/Ariadne/internal/trace"
)

type conversationServiceHandler struct {
	api *API
}

func (api *API) registerConversationService() {
	serviceDesc := ariadnev1.File_ariadne_v1_conversations_proto.Services().ByName("ConversationService")
	handler := &conversationServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewConversationServiceHandler(handler, opts...)
	})
}

func (h *conversationServiceHandler) GetConversation(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetConversationRequest],
) (*connect.Response[ariadnev1.GetConversationResponse], error) {
	cursor := core.MessageCursor{}
	if req.Msg.Before != nil {
		v := *req.Msg.Before
		cursor.Before = &v
	}
	if req.Msg.From != nil {
		v := *req.Msg.From
		cursor.From = &v
	}
	if req.Msg.Limit != nil {
		if *req.Msg.Limit < 1 || *req.Msg.Limit > 500 {
			return nil, badInput("limit")
		}
		v := int(*req.Msg.Limit)
		cursor.Limit = &v
	}
	if req.Msg.Since != nil {
		if *req.Msg.Since < 0 {
			return nil, badInput("since")
		}
		v := *req.Msg.Since
		cursor.Since = &v
	}
	snapshot, err := h.api.options.Repository.GetConversationSnapshot(ctx, req.Msg.GetConversationId(), cursor)
	if err != nil {
		return nil, err
	}
	var model any
	if h.api.options.Settings != nil {
		fallback := snapshot.Conversation.Turn != nil && snapshot.Conversation.Turn.Fallback
		model = map[string]any(h.api.options.Settings.ConversationModel(snapshot.Conversation.Role, snapshot.ThreadModel, fallback))
	} else if h.api.options.ModelOf != nil {
		dummyReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
		if reqErr == nil {
			model, err = h.api.options.ModelOf(dummyReq, snapshot.Conversation)
			if err != nil {
				return nil, err
			}
		}
	}
	messages := make([]*ariadnev1.MessageView, 0, len(snapshot.Messages))
	for _, msg := range snapshot.Messages {
		messages = append(messages, toProtoMessageView(msg))
	}
	notices := make([]*ariadnev1.NoticeView, 0, len(snapshot.Notices))
	for _, notice := range snapshot.Notices {
		notices = append(notices, toProtoNoticeView(notice))
	}
	return connect.NewResponse(&ariadnev1.GetConversationResponse{
		Conversation: toProtoConversationView(snapshot.Conversation),
		Messages:     messages,
		Notices:      notices,
		ReadThrough:  protoOptNonEmptyStr(snapshot.ReadThrough),
		More:         snapshot.More,
		LastSeq:      snapshot.LastSeq,
		Todo:         toProtoTodoView(snapshot.Todo),
		Model:        toProtoConversationModel(model),
	}), nil
}

func (h *conversationServiceHandler) PostMessage(
	ctx context.Context,
	req *connect.Request[ariadnev1.PostMessageRequest],
) (*connect.Response[ariadnev1.PostMessageResponse], error) {
	conversationID := req.Msg.GetConversationId()
	if conversationID == "" {
		return nil, badInput("conversationId")
	}
	var attachments []any
	if len(req.Msg.Attachments) > 0 {
		attachments = make([]any, 0, len(req.Msg.Attachments))
		for _, a := range req.Msg.Attachments {
			attachments = append(attachments, object{
				"fileId": a.GetFileId(),
				"name":   a.GetName(),
			})
		}
	}
	var mentions []any
	if len(req.Msg.Mentions) > 0 {
		mentions = make([]any, 0, len(req.Msg.Mentions))
		for _, m := range req.Msg.Mentions {
			switch inner := m.Mention.(type) {
			case *ariadnev1.MentionInput_ThreadId:
				mentions = append(mentions, object{"kind": "thread", "id": inner.ThreadId})
			case *ariadnev1.MentionInput_ArtifactId:
				mentions = append(mentions, object{"kind": "artifact", "id": inner.ArtifactId})
			case *ariadnev1.MentionInput_FilePath:
				mentions = append(mentions, object{"kind": "file", "path": inner.FilePath})
			case *ariadnev1.MentionInput_SkillName:
				mentions = append(mentions, object{"kind": "skill", "name": inner.SkillName})
			default:
				return nil, badInput("mentions")
			}
		}
	}
	var comments []any
	if len(req.Msg.Comments) > 0 {
		comments = make([]any, 0, len(req.Msg.Comments))
		for _, c := range req.Msg.Comments {
			item := object{
				"path":  c.GetPath(),
				"line":  float64(c.GetLine()),
				"quote": c.GetQuote(),
				"body":  c.GetBody(),
			}
			if c.Side != nil {
				item["side"] = *c.Side
			}
			comments = append(comments, item)
		}
	}
	message, err := h.api.options.Repository.PostMessage(ctx, req.Msg.GetProjectId(), core.MessageInput{
		ConversationID: conversationID,
		Author:         "user",
		Content:        req.Msg.GetContent(),
		Attachments:    attachments,
		Mentions:       mentions,
		Comments:       comments,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.PostMessageResponse{
		Message: toProtoMessageView(message),
	}), nil
}

func (h *conversationServiceHandler) MarkRead(
	ctx context.Context,
	req *connect.Request[ariadnev1.MarkReadRequest],
) (*connect.Response[ariadnev1.MarkReadResponse], error) {
	upTo := req.Msg.GetUpTo()
	if upTo == "" {
		return nil, badInput("upTo")
	}
	if err := h.api.options.Repository.MarkRead(ctx, req.Msg.GetConversationId(), upTo); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.MarkReadResponse{}), nil
}

func (h *conversationServiceHandler) NoteReturn(
	ctx context.Context,
	req *connect.Request[ariadnev1.NoteReturnRequest],
) (*connect.Response[ariadnev1.NoteReturnResponse], error) {
	away := req.Msg.GetAway()
	if math.IsNaN(away) || math.IsInf(away, 0) {
		return nil, badInput("away")
	}
	if err := h.api.options.Repository.NoteReturn(ctx, req.Msg.GetProjectId(), away); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.NoteReturnResponse{}), nil
}

func (h *conversationServiceHandler) AnswerAttention(
	ctx context.Context,
	req *connect.Request[ariadnev1.AnswerAttentionRequest],
) (*connect.Response[ariadnev1.AnswerAttentionResponse], error) {
	answer := fromProtoBlockAnswer(req.Msg.GetAnswer())
	if err := h.api.options.Repository.AnswerAttention(ctx, req.Msg.GetAttentionId(), answer); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.AnswerAttentionResponse{}), nil
}

func (h *conversationServiceHandler) AddReaction(
	ctx context.Context,
	req *connect.Request[ariadnev1.AddReactionRequest],
) (*connect.Response[ariadnev1.AddReactionResponse], error) {
	emoji := req.Msg.GetEmoji()
	if emoji == "" {
		return nil, badInput("emoji")
	}
	message, err := h.api.options.Repository.AddReactionAsUser(ctx, req.Msg.GetMessageId(), emoji)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.AddReactionResponse{
		Message: toProtoMessageView(message),
	}), nil
}

func (h *conversationServiceHandler) RemoveReaction(
	ctx context.Context,
	req *connect.Request[ariadnev1.RemoveReactionRequest],
) (*connect.Response[ariadnev1.RemoveReactionResponse], error) {
	emoji := req.Msg.GetEmoji()
	if emoji == "" {
		return nil, badInput("emoji")
	}
	message, err := h.api.options.Repository.RemoveReactionAsUser(ctx, req.Msg.GetMessageId(), emoji)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RemoveReactionResponse{
		Message: toProtoMessageView(message),
	}), nil
}

func (h *conversationServiceHandler) StopTurn(
	ctx context.Context,
	req *connect.Request[ariadnev1.StopTurnRequest],
) (*connect.Response[ariadnev1.StopTurnResponse], error) {
	id := req.Msg.GetConversationId()
	if _, err := h.api.options.Repository.GetConversation(ctx, id); err != nil {
		return nil, err
	}
	stopped := h.api.options.Scheduler != nil && h.api.options.Scheduler.Cancel(id)
	return connect.NewResponse(&ariadnev1.StopTurnResponse{Stopped: stopped}), nil
}

func (h *conversationServiceHandler) RetryTurn(
	ctx context.Context,
	req *connect.Request[ariadnev1.RetryTurnRequest],
) (*connect.Response[ariadnev1.RetryTurnResponse], error) {
	if err := h.api.options.Repository.RequestTurn(ctx, req.Msg.GetConversationId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RetryTurnResponse{}), nil
}

func (h *conversationServiceHandler) WakeWait(
	ctx context.Context,
	req *connect.Request[ariadnev1.WakeWaitRequest],
) (*connect.Response[ariadnev1.WakeWaitResponse], error) {
	if _, err := h.api.options.Repository.EndWait(ctx, req.Msg.GetConversationId(), "now", "user", nil); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.WakeWaitResponse{}), nil
}

func (h *conversationServiceHandler) CancelWait(
	ctx context.Context,
	req *connect.Request[ariadnev1.CancelWaitRequest],
) (*connect.Response[ariadnev1.CancelWaitResponse], error) {
	if _, err := h.api.options.Repository.EndWait(ctx, req.Msg.GetConversationId(), "cancelled", "user", nil); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CancelWaitResponse{}), nil
}

func (h *conversationServiceHandler) ListSkills(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListSkillsRequest],
) (*connect.Response[ariadnev1.ListSkillsResponse], error) {
	conversation, err := h.api.options.Repository.GetConversation(ctx, req.Msg.GetConversationId())
	if err != nil {
		return nil, err
	}
	set, err := skills.Conversation(ctx, h.api.options.Repository, conversation, h.api.options.Workspaces, h.api.options.SkillsDirs)
	if err != nil {
		return nil, err
	}
	list := set.List()
	out := make([]*ariadnev1.SkillView, 0, len(list))
	for _, item := range list {
		out = append(out, toProtoSkillView(item))
	}
	return connect.NewResponse(&ariadnev1.ListSkillsResponse{Skills: out}), nil
}

func (h *conversationServiceHandler) GetTrace(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetTraceRequest],
) (*connect.Response[ariadnev1.GetTraceResponse], error) {
	options := trace.Options{}
	if req.Msg.Before != nil {
		v := *req.Msg.Before
		options.Before = &v
	}
	if req.Msg.From != nil {
		if *req.Msg.From < 1 {
			return nil, badInput("from")
		}
		v := *req.Msg.From
		options.From = &v
	}
	result, err := trace.Build(ctx, h.api.options.Repository, req.Msg.GetConversationId(), options)
	if err != nil {
		return nil, err
	}
	turns := make([]*ariadnev1.TraceTurn, 0, len(result.Turns))
	for _, turn := range result.Turns {
		turns = append(turns, toProtoTraceTurn(turn))
	}
	return connect.NewResponse(&ariadnev1.GetTraceResponse{
		Turns: turns,
		More:  result.More,
	}), nil
}

func (h *conversationServiceHandler) GetTraceRequestDetail(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetTraceRequestDetailRequest],
) (*connect.Response[ariadnev1.GetTraceRequestDetailResponse], error) {
	seq := req.Msg.GetSeq()
	if seq < 1 {
		return nil, badInput("seq")
	}
	detail, request, err := trace.RebuildRequest(ctx, h.api.options.Repository, req.Msg.GetConversationId(), seq)
	if err != nil {
		return nil, err
	}
	var body any
	if detail.Call != nil && request != nil && h.api.options.Models != nil {
		if provider, ok := h.api.options.Models.Get(detail.Call.Provider); ok && provider.Describe != nil {
			func() {
				defer func() {
					if recover() != nil {
						body = nil
					}
				}()
				body = provider.Describe(detail.Call.Settings, *request)
			}()
		}
	}
	return connect.NewResponse(&ariadnev1.GetTraceRequestDetailResponse{
		Request: toProtoTraceRequestDetail(detail, body),
	}), nil
}

func (h *conversationServiceHandler) UndoNotice(
	ctx context.Context,
	req *connect.Request[ariadnev1.UndoNoticeRequest],
) (*connect.Response[ariadnev1.UndoNoticeResponse], error) {
	repo := h.api.options.Repository
	project, id := req.Msg.GetProjectId(), req.Msg.GetNoticeId()
	notice, err := repo.EverywhereNotice(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if notice == nil {
		if err := repo.UndoMemoryNotice(ctx, project, id); err != nil {
			return nil, err
		}
		return connect.NewResponse(&ariadnev1.UndoNoticeResponse{}), nil
	}
	if h.api.options.Settings != nil && notice["undone"] != true {
		if notice["action"] == "saved" {
			_, err = h.api.options.Settings.RemoveInstruction(text(notice, "text"))
		} else {
			_, err = h.api.options.Settings.AddInstruction(text(notice, "text"))
		}
		if err != nil {
			return nil, err
		}
		if err := repo.SettleNotice(ctx, project, id); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&ariadnev1.UndoNoticeResponse{}), nil
}

func (h *conversationServiceHandler) ListMemories(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListMemoriesRequest],
) (*connect.Response[ariadnev1.ListMemoriesResponse], error) {
	memories, err := h.api.options.Repository.ListMemories(ctx, req.Msg.GetProjectId())
	if err != nil {
		return nil, err
	}
	out := make([]*ariadnev1.MemoryView, 0, len(memories))
	for _, m := range memories {
		out = append(out, toProtoMemoryView(m))
	}
	return connect.NewResponse(&ariadnev1.ListMemoriesResponse{Memories: out}), nil
}

func (h *conversationServiceHandler) CreateMemory(
	ctx context.Context,
	req *connect.Request[ariadnev1.CreateMemoryRequest],
) (*connect.Response[ariadnev1.CreateMemoryResponse], error) {
	value := req.Msg.GetText()
	if value == "" {
		return nil, badInput("text")
	}
	memory, err := h.api.options.Repository.SaveMemory(ctx, req.Msg.GetProjectId(), core.MemoryChange{
		Text:   value,
		Author: "user",
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CreateMemoryResponse{
		Memory: toProtoMemoryView(memory),
	}), nil
}

func (h *conversationServiceHandler) UpdateMemory(
	ctx context.Context,
	req *connect.Request[ariadnev1.UpdateMemoryRequest],
) (*connect.Response[ariadnev1.UpdateMemoryResponse], error) {
	value := req.Msg.GetText()
	if value == "" {
		return nil, badInput("text")
	}
	memoryID := req.Msg.GetMemoryId()
	memory, err := h.api.options.Repository.SaveMemory(ctx, req.Msg.GetProjectId(), core.MemoryChange{
		MemoryID: &memoryID,
		Text:     value,
		Author:   "user",
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.UpdateMemoryResponse{
		Memory: toProtoMemoryView(memory),
	}), nil
}

func (h *conversationServiceHandler) DeleteMemory(
	ctx context.Context,
	req *connect.Request[ariadnev1.DeleteMemoryRequest],
) (*connect.Response[ariadnev1.DeleteMemoryResponse], error) {
	if err := h.api.options.Repository.RemoveMemory(ctx, req.Msg.GetProjectId(), req.Msg.GetMemoryId(), "user", ""); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.DeleteMemoryResponse{}), nil
}
