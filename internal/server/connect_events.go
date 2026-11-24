package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/update"
)

type eventServiceHandler struct {
	api *API
}

func (api *API) registerEventService() {
	serviceDesc := ariadnev1.File_ariadne_v1_events_proto.Services().ByName("EventService")
	handler := &eventServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewEventServiceHandler(handler, opts...)
	})
}

func (h *eventServiceHandler) StreamEvents(
	ctx context.Context,
	req *connect.Request[ariadnev1.StreamEventsRequest],
	stream *connect.ServerStream[ariadnev1.StreamEventsResponse],
) error {
	var after int64
	if req.Msg.AfterSeq != nil {
		if *req.Msg.AfterSeq < 0 {
			return badInput("afterSeq")
		}
		after = *req.Msg.AfterSeq
	} else {
		latest, err := h.api.options.Repository.Store.LastSeq(ctx)
		if err != nil {
			return err
		}
		after = latest
	}
	project := req.Msg.GetProjectId()

	guard := h.api.streamWriteGuard(ctx)
	send := func(msg *ariadnev1.StreamEventsResponse) error {
		return guard.Send(func() error {
			return stream.Send(msg)
		})
	}
	var drafts draftStreamTracker

	err := realtime.Stream(ctx, h.api.options.Repository.Store, h.api.hub, realtime.StreamOptions{
		AfterSeq:  after,
		ProjectID: project,
		Initial:   h.api.state.Snapshots(project),
	}, realtime.StreamSink{
		OnReady: func(lastSeq int64) error {
			return send(&ariadnev1.StreamEventsResponse{
				Payload: &ariadnev1.StreamEventsResponse_Ready{
					Ready: &ariadnev1.StreamReady{LastSeq: lastSeq},
				},
			})
		},
		OnHeartbeat: func() error {
			return send(&ariadnev1.StreamEventsResponse{
				Payload: &ariadnev1.StreamEventsResponse_Heartbeat{
					Heartbeat: &ariadnev1.StreamHeartbeat{
						ServerTimeMs: h.api.now().UnixMilli(),
					},
				},
			})
		},
		OnEvent: func(event eventstore.Event) error {
			return send(&ariadnev1.StreamEventsResponse{
				Payload: &ariadnev1.StreamEventsResponse_Event{
					Event: &ariadnev1.StoredEvent{
						Seq:       event.Seq,
						Id:        event.ID,
						ProjectId: event.ProjectID,
						Type:      event.Type,
						At:        event.At,
						RawJson:   []byte(event.Data),
					},
				},
			})
		},
		OnUpdate: func(item realtime.Update) error {
			msg, ok, commit := toProtoRealtimeUpdateWithDrafts(item, &drafts)
			if !ok {
				return nil
			}
			if err := send(msg); err != nil {
				return err
			}
			if commit != nil {
				commit()
			}
			return nil
		},
	})
	if err == nil {
		return nil
	}
	guard.ArmCloseDeadline()
	if errors.Is(err, realtime.ErrSlowConsumer) {
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	return err
}

type sentDraftSnapshot struct {
	messageID string
	text      string
	blocks    json.RawMessage
	startedAt int64
}

type draftStreamTracker struct {
	sent map[string]sentDraftSnapshot
}

func (t *draftStreamTracker) encode(payload realtime.DraftUpdate) (*ariadnev1.StreamEventsResponse, bool, func()) {
	convID := payload.ConversationID
	if payload.Draft == nil {
		return &ariadnev1.StreamEventsResponse{
				Payload: &ariadnev1.StreamEventsResponse_Draft{
					Draft: &ariadnev1.ReplyDraftUpdate{
						ProjectId:      payload.ProjectID,
						ConversationId: convID,
					},
				},
			}, true, func() {
				delete(t.sent, convID)
			}
	}
	draft := payload.Draft
	prev, hasPrev := t.sent[convID]
	canAppend := hasPrev && prev.messageID == draft.MessageID && prev.startedAt == draft.StartedAt && strings.HasPrefix(draft.Text, prev.text)
	if canAppend {
		delta := draft.Text[len(prev.text):]
		sameBlocks := bytes.Equal(draft.Blocks, prev.blocks)
		if delta == "" && sameBlocks && !draft.Posted {
			return nil, false, nil
		}
		outDraft := &ariadnev1.ReplyDraft{
			MessageId:  draft.MessageID,
			Text:       delta,
			StartedAt:  draft.StartedAt,
			Posted:     draft.Posted,
			Append:     true,
			KeepBlocks: sameBlocks,
		}
		if !sameBlocks {
			outDraft.BlocksJson = []byte(draft.Blocks)
		}
		commit := func() {
			if draft.Posted {
				delete(t.sent, convID)
				return
			}
			if t.sent == nil {
				t.sent = make(map[string]sentDraftSnapshot)
			}
			t.sent[convID] = sentDraftSnapshot{
				messageID: draft.MessageID,
				text:      draft.Text,
				blocks:    draft.Blocks,
				startedAt: draft.StartedAt,
			}
		}
		return &ariadnev1.StreamEventsResponse{
			Payload: &ariadnev1.StreamEventsResponse_Draft{
				Draft: &ariadnev1.ReplyDraftUpdate{
					ProjectId:      payload.ProjectID,
					ConversationId: convID,
					Draft:          outDraft,
				},
			},
		}, true, commit
	}
	outDraft := &ariadnev1.ReplyDraft{
		MessageId:  draft.MessageID,
		Text:       draft.Text,
		BlocksJson: []byte(draft.Blocks),
		StartedAt:  draft.StartedAt,
		Posted:     draft.Posted,
	}
	commit := func() {
		if draft.Posted {
			delete(t.sent, convID)
			return
		}
		if t.sent == nil {
			t.sent = make(map[string]sentDraftSnapshot)
		}
		t.sent[convID] = sentDraftSnapshot{
			messageID: draft.MessageID,
			text:      draft.Text,
			blocks:    draft.Blocks,
			startedAt: draft.StartedAt,
		}
	}
	return &ariadnev1.StreamEventsResponse{
		Payload: &ariadnev1.StreamEventsResponse_Draft{
			Draft: &ariadnev1.ReplyDraftUpdate{
				ProjectId:      payload.ProjectID,
				ConversationId: convID,
				Draft:          outDraft,
			},
		},
	}, true, commit
}

type backgroundSnapshotPayload struct {
	ProjectID      string               `json:"projectId"`
	ConversationID string               `json:"conversationId"`
	Commands       []background.Command `json:"commands"`
}

func toProtoRealtimeUpdate(item realtime.Update) (*ariadnev1.StreamEventsResponse, bool) {
	msg, ok, _ := toProtoRealtimeUpdateWithDrafts(item, nil)
	return msg, ok
}

func toProtoRealtimeUpdateWithDrafts(item realtime.Update, drafts *draftStreamTracker) (*ariadnev1.StreamEventsResponse, bool, func()) {
	switch item.Kind {
	case "live":
		var payload realtime.LiveUpdate
		switch v := item.Value.(type) {
		case *realtime.LiveUpdate:
			if v == nil {
				return nil, false, nil
			}
			payload = *v
		case realtime.LiveUpdate:
			payload = v
		default:
			if err := json.Unmarshal(item.Data, &payload); err != nil {
				return nil, false, nil
			}
		}
		if payload.ProjectID == "" {
			payload.ProjectID = item.ProjectID
		}
		if payload.ConversationID == "" {
			payload.ConversationID = item.ConversationID
		}
		out := &ariadnev1.LiveCommandUpdate{
			ProjectId:      payload.ProjectID,
			ConversationId: payload.ConversationID,
		}
		if payload.Command != nil {
			out.Command = &ariadnev1.LiveCommand{
				ProjectId:      payload.Command.ProjectID,
				ConversationId: payload.Command.ConversationID,
				Command:        payload.Command.Command,
				Cwd:            payload.Command.CWD,
				StartedAt:      payload.Command.StartedAt,
				Tail:           payload.Command.Tail,
			}
		}
		return &ariadnev1.StreamEventsResponse{
			Payload: &ariadnev1.StreamEventsResponse_Live{Live: out},
		}, true, nil

	case "background":
		var payload backgroundSnapshotPayload
		switch v := item.Value.(type) {
		case *background.Snapshot:
			if v == nil {
				return nil, false, nil
			}
			payload = backgroundSnapshotPayload{
				ProjectID:      v.ProjectID,
				ConversationID: v.ConversationID,
				Commands:       v.Commands,
			}
		case background.Snapshot:
			payload = backgroundSnapshotPayload{
				ProjectID:      v.ProjectID,
				ConversationID: v.ConversationID,
				Commands:       v.Commands,
			}
		default:
			if err := json.Unmarshal(item.Data, &payload); err != nil {
				return nil, false, nil
			}
		}
		if payload.ProjectID == "" {
			payload.ProjectID = item.ProjectID
		}
		if payload.ConversationID == "" {
			payload.ConversationID = item.ConversationID
		}
		cmds := make([]*ariadnev1.BackgroundCommandView, 0, len(payload.Commands))
		for _, cmd := range payload.Commands {
			cmds = append(cmds, toProtoBackgroundCommand(cmd))
		}
		return &ariadnev1.StreamEventsResponse{
			Payload: &ariadnev1.StreamEventsResponse_Background{
				Background: &ariadnev1.BackgroundCommandsUpdate{
					ProjectId:      payload.ProjectID,
					ConversationId: payload.ConversationID,
					Commands:       cmds,
				},
			},
		}, true, nil

	case "draft":
		var payload realtime.DraftUpdate
		switch v := item.Value.(type) {
		case *realtime.DraftUpdate:
			if v == nil {
				return nil, false, nil
			}
			payload = *v
		case realtime.DraftUpdate:
			payload = v
		default:
			if err := json.Unmarshal(item.Data, &payload); err != nil {
				return nil, false, nil
			}
		}
		if payload.ProjectID == "" {
			payload.ProjectID = item.ProjectID
		}
		if payload.ConversationID == "" {
			payload.ConversationID = item.ConversationID
		}
		if drafts != nil {
			return drafts.encode(payload)
		}
		out := &ariadnev1.ReplyDraftUpdate{
			ProjectId:      payload.ProjectID,
			ConversationId: payload.ConversationID,
		}
		if payload.Draft != nil {
			out.Draft = &ariadnev1.ReplyDraft{
				MessageId:  payload.Draft.MessageID,
				Text:       payload.Draft.Text,
				BlocksJson: []byte(payload.Draft.Blocks),
				StartedAt:  payload.Draft.StartedAt,
				Posted:     payload.Draft.Posted,
			}
		}
		return &ariadnev1.StreamEventsResponse{
			Payload: &ariadnev1.StreamEventsResponse_Draft{Draft: out},
		}, true, nil

	case "update":
		var view update.View
		switch v := item.Value.(type) {
		case update.View:
			view = v
		case *update.View:
			if v == nil {
				return nil, false, nil
			}
			view = *v
		default:
			if err := json.Unmarshal(item.Data, &view); err != nil {
				return nil, false, nil
			}
		}
		return &ariadnev1.StreamEventsResponse{
			Payload: &ariadnev1.StreamEventsResponse_UpdateStatus{
				UpdateStatus: toProtoUpdateStatus(view),
			},
		}, true, nil
	}
	return nil, false, nil
}
