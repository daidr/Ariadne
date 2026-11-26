package server

import (
	"context"
	"net/http"
	"slices"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/textutil"
)

type artifactService struct {
	api *API
}

func (api *API) registerArtifactRPC() {
	svc := &artifactService{api: api}
	api.mountConnect(
		ariadnev1.File_ariadne_v1_artifacts_proto.Services().ByName("ArtifactService"),
		func(opts ...connect.HandlerOption) (string, http.Handler) {
			return ariadnev1connect.NewArtifactServiceHandler(svc, opts...)
		},
	)
}

func (s *artifactService) ListArtifacts(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListArtifactsRequest],
) (*connect.Response[ariadnev1.ListArtifactsResponse], error) {
	page, err := s.api.options.Repository.ListArtifactsPage(
		ctx,
		req.Msg.ProjectId,
		int(req.Msg.PageSize),
		req.Msg.PageToken,
	)
	if err != nil {
		return nil, err
	}
	artifacts := make([]*ariadnev1.ArtifactSummary, 0, len(page.Artifacts))
	for _, item := range page.Artifacts {
		artifacts = append(artifacts, toProtoArtifactSummary(item))
	}
	return connect.NewResponse(&ariadnev1.ListArtifactsResponse{
		Artifacts:     artifacts,
		NextPageToken: page.NextPageToken,
		HasMore:       page.HasMore,
	}), nil
}

func (s *artifactService) ListGalleryArtifacts(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListGalleryArtifactsRequest],
) (*connect.Response[ariadnev1.ListGalleryArtifactsResponse], error) {
	page, err := s.api.options.Repository.ListGalleryArtifacts(ctx, core.GalleryArtifactsQuery{
		ProjectID:      req.Msg.ProjectId,
		Kind:           req.Msg.Kind,
		OnlyPrototypes: req.Msg.OnlyPrototypes,
		Status:         req.Msg.Status,
		Query:          req.Msg.Query,
		Sort:           req.Msg.Sort,
		PageSize:       int(req.Msg.PageSize),
		PageToken:      req.Msg.PageToken,
		IncludeCounts:  req.Msg.IncludeCounts,
	})
	if err != nil {
		return nil, err
	}
	items := make([]*ariadnev1.GalleryArtifactSummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toProtoGalleryArtifactSummary(item))
	}
	return connect.NewResponse(&ariadnev1.ListGalleryArtifactsResponse{
		Items:         items,
		NextPageToken: page.NextPageToken,
		HasMore:       page.HasMore,
		Counts:        toProtoGalleryCounts(page.Counts),
	}), nil
}

func (s *artifactService) CreateArtifact(
	ctx context.Context,
	req *connect.Request[ariadnev1.CreateArtifactRequest],
) (*connect.Response[ariadnev1.CreateArtifactResponse], error) {
	kind := req.Msg.Kind
	if kind != "document" && kind != "whiteboard" && kind != "dashboard" {
		return nil, badInput("kind")
	}
	repo := s.api.options.Repository
	artifact, err := repo.CreateArtifact(ctx, req.Msg.ProjectId, core.NewArtifact{
		Kind:    kind,
		Title:   req.Msg.Title,
		Author:  "user",
		Content: core.EmptyContent(kind),
	})
	if err != nil {
		return nil, err
	}
	detail, err := repo.GetArtifactDetail(ctx, artifact.ID, nil, true, 0, "")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CreateArtifactResponse{
		Artifact: toProtoArtifactView(detail.Artifact),
		Versions: toProtoArtifactVersions(detail.Versions),
		Comments: toProtoArtifactCommentThreads(detail.Comments),
	}), nil
}

func (s *artifactService) GetArtifact(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetArtifactRequest],
) (*connect.Response[ariadnev1.GetArtifactResponse], error) {
	var version *int64
	if req.Msg.Version != nil {
		v := *req.Msg.Version
		version = &v
	}
	detail, err := s.api.options.Repository.GetArtifactDetail(
		ctx,
		req.Msg.ArtifactId,
		version,
		req.Msg.IncludeVersions,
		int(req.Msg.VersionPageSize),
		req.Msg.VersionPageToken,
	)
		if err != nil {
			return nil, err
		}
		if req.Msg.Lightweight {
			detail.Artifact.Content = core.LightweightArtifactContent(detail.Artifact.Kind, detail.Artifact.Content)
		}
		return connect.NewResponse(&ariadnev1.GetArtifactResponse{
		Artifact:             toProtoArtifactView(detail.Artifact),
		Versions:             toProtoArtifactVersions(detail.Versions),
		Comments:             toProtoArtifactCommentThreads(detail.Comments),
		NextVersionPageToken: detail.NextVersionPageToken,
		HasMoreVersions:      detail.HasMoreVersions,
		AskBaseline:          toProtoDocumentAskBaseline(detail.AskBaseline),
	}), nil
}

func (s *artifactService) ListArtifactVersions(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListArtifactVersionsRequest],
) (*connect.Response[ariadnev1.ListArtifactVersionsResponse], error) {
	page, err := s.api.options.Repository.ListArtifactVersionsPage(
		ctx,
		req.Msg.ArtifactId,
		int(req.Msg.PageSize),
		req.Msg.PageToken,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.ListArtifactVersionsResponse{
		Versions:      toProtoArtifactVersions(page.Versions),
		NextPageToken: page.NextPageToken,
		HasMore:       page.HasMore,
	}), nil
}

func (s *artifactService) SaveArtifact(
	ctx context.Context,
	req *connect.Request[ariadnev1.SaveArtifactRequest],
) (*connect.Response[ariadnev1.SaveArtifactResponse], error) {
	msg := req.Msg
	id := msg.ArtifactId
	repo := s.api.options.Repository
	current, err := repo.GetArtifact(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	shared := collab.Enabled(current.Kind)
	if msg.Content == nil && !shared {
		return nil, badInput("content")
	}
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}
	baseVersion := msg.BaseVersion
	input := core.ArtifactRevision{
		Content:     content,
		Author:      "user",
		Reason:      msg.Reason,
		BaseVersion: &baseVersion,
	}
	if msg.Title != nil {
		title := *msg.Title
		input.Title = &title
	}
	if len(msg.Quotes) > 0 {
		quotes := make(map[string]string, len(msg.Quotes))
		for k, v := range msg.Quotes {
			quotes[k] = v
		}
		input.Quotes = quotes
	}
	var artifact core.ArtifactView
	if shared {
		var next *string
		if msg.Content != nil {
			value := *msg.Content
			next = &value
		}
		artifact, err = s.api.rooms.Revise(ctx, id, next, input)
	} else {
		artifact, err = repo.ReviseArtifact(ctx, id, input)
	}
	if err != nil {
		return nil, err
	}
	var versions []*ariadnev1.ArtifactVersionView
	if msg.IncludeVersions {
		page, err := repo.ListArtifactVersionsPage(ctx, artifact.ID, 0, "")
		if err != nil {
			return nil, err
		}
		versions = toProtoArtifactVersions(page.Versions)
	} else {
		versions = []*ariadnev1.ArtifactVersionView{}
	}
	comments, err := repo.ListArtifactComments(ctx, artifact.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveArtifactResponse{
		Artifact: toProtoArtifactView(artifact),
		Versions: versions,
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) RefreshDashboard(
	ctx context.Context,
	req *connect.Request[ariadnev1.RefreshDashboardRequest],
) (*connect.Response[ariadnev1.RefreshDashboardResponse], error) {
	reason := "刷新了数据"
	if s.api.options.Settings != nil && s.api.options.Settings.Locale() == "en" {
		reason = "Refreshed the data"
	}
	shared := s.api.options.SharedDashboard
	if shared == nil {
		shared = s.api.rooms
	}
	artifact, err := dashboard.Refresh(
		ctx,
		s.api.options.Repository,
		s.api.options.Dashboards,
		req.Msg.ArtifactId,
		dashboard.RefreshOptions{
			Author: "user",
			Reason: reason,
			Now:    s.api.options.Now,
			Shared: shared,
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RefreshDashboardResponse{
		Artifact: toProtoArtifactView(artifact),
	}), nil
}

func (s *artifactService) SendArtifactFeedback(
	ctx context.Context,
	req *connect.Request[ariadnev1.SendArtifactFeedbackRequest],
) (*connect.Response[ariadnev1.SendArtifactFeedbackResponse], error) {
	id := req.Msg.ArtifactId
	repo := s.api.options.Repository
	if _, err := repo.GetArtifact(ctx, id, nil); err != nil {
		return nil, err
	}
	if err := s.api.rooms.Flush(ctx, id); err != nil {
		return nil, err
	}
	images := req.Msg.Images
	if images == nil {
		images = []string{}
	}
	feedback, err := repo.SendPendingArtifactFeedback(ctx, id, images)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SendArtifactFeedbackResponse{
		Feedback: toProtoArtifactFeedbackView(feedback),
	}), nil
}

func (s *artifactService) WithdrawArtifactFeedback(
	ctx context.Context,
	req *connect.Request[ariadnev1.WithdrawArtifactFeedbackRequest],
) (*connect.Response[ariadnev1.WithdrawArtifactFeedbackResponse], error) {
	id := req.Msg.ArtifactId
	round := req.Msg.Round
	if id == "" || round <= 0 {
		return nil, badInput("round")
	}
	withdrawn, err := s.api.options.Repository.WithdrawArtifactFeedback(ctx, id, round)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.WithdrawArtifactFeedbackResponse{
		Withdrawn: withdrawn,
	}), nil
}

// ---- D-5: Dedicated comment lifecycle RPCs ----

func (s *artifactService) CreateArtifactComment(
	ctx context.Context,
	req *connect.Request[ariadnev1.CreateArtifactCommentRequest],
) (*connect.Response[ariadnev1.CreateArtifactCommentResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	if msg.CommentId == "" {
		return nil, badInput("commentId")
	}
	if msg.Body == "" {
		return nil, badInput("body")
	}
	action := object{
		"type":      "create",
		"threadId":  msg.ThreadId,
		"commentId": msg.CommentId,
		"body":      msg.Body,
	}
	if msg.Anchor != nil {
		anchor := object{}
		switch target := msg.Anchor.Target.(type) {
		case *ariadnev1.ArtifactCommentAnchor_Node:
			if target.Node == "" {
				return nil, badInput("anchor")
			}
			anchor["node"] = target.Node
		case *ariadnev1.ArtifactCommentAnchor_Edge:
			if target.Edge == "" {
				return nil, badInput("anchor")
			}
			anchor["edge"] = target.Edge
		default:
			return nil, badInput("anchor")
		}
		if msg.Anchor.Block != nil {
			if textutil.Trim(*msg.Anchor.Block) == "" {
				return nil, badInput("anchor.block")
			}
			anchor["block"] = *msg.Anchor.Block
		}
		if elem := msg.Anchor.Element; elem != nil {
			el := object{}
			if elem.Label != nil && textutil.Trim(*elem.Label) != "" {
				el["label"] = *elem.Label
			}
			if elem.Role != nil && textutil.Trim(*elem.Role) != "" {
				el["role"] = *elem.Role
			}
			if elem.Tag != nil && textutil.Trim(*elem.Tag) != "" {
				el["tag"] = *elem.Tag
			}
			if elem.Selector != nil && textutil.Trim(*elem.Selector) != "" {
				el["selector"] = *elem.Selector
			}
			if elem.Rect != nil {
				el["rect"] = object{
					"x": elem.Rect.X,
					"y": elem.Rect.Y,
					"w": elem.Rect.W,
					"h": elem.Rect.H,
				}
			}
			if len(el) > 0 {
				anchor["element"] = el
			}
		}
		action["anchor"] = anchor
		action["quote"] = msg.Quote
	} else {
		if msg.Quote == "" {
			return nil, badInput("quote")
		}
		action["quote"] = msg.Quote
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", action)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CreateArtifactCommentResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) ReplyArtifactComment(
	ctx context.Context,
	req *connect.Request[ariadnev1.ReplyArtifactCommentRequest],
) (*connect.Response[ariadnev1.ReplyArtifactCommentResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	if msg.CommentId == "" {
		return nil, badInput("commentId")
	}
	if msg.Body == "" {
		return nil, badInput("body")
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", object{
		"type":      "reply",
		"threadId":  msg.ThreadId,
		"commentId": msg.CommentId,
		"body":      msg.Body,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.ReplyArtifactCommentResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) EditArtifactComment(
	ctx context.Context,
	req *connect.Request[ariadnev1.EditArtifactCommentRequest],
) (*connect.Response[ariadnev1.EditArtifactCommentResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	if msg.CommentId == "" {
		return nil, badInput("commentId")
	}
	if msg.Body == "" {
		return nil, badInput("body")
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", object{
		"type":      "edit",
		"threadId":  msg.ThreadId,
		"commentId": msg.CommentId,
		"body":      msg.Body,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.EditArtifactCommentResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) DeleteArtifactComment(
	ctx context.Context,
	req *connect.Request[ariadnev1.DeleteArtifactCommentRequest],
) (*connect.Response[ariadnev1.DeleteArtifactCommentResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	if msg.CommentId == "" {
		return nil, badInput("commentId")
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", object{
		"type":      "delete",
		"threadId":  msg.ThreadId,
		"commentId": msg.CommentId,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.DeleteArtifactCommentResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) ResolveArtifactCommentThread(
	ctx context.Context,
	req *connect.Request[ariadnev1.ResolveArtifactCommentThreadRequest],
) (*connect.Response[ariadnev1.ResolveArtifactCommentThreadResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", object{
		"type":     "resolve",
		"threadId": msg.ThreadId,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.ResolveArtifactCommentThreadResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) ReopenArtifactCommentThread(
	ctx context.Context,
	req *connect.Request[ariadnev1.ReopenArtifactCommentThreadRequest],
) (*connect.Response[ariadnev1.ReopenArtifactCommentThreadResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", object{
		"type":     "reopen",
		"threadId": msg.ThreadId,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.ReopenArtifactCommentThreadResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

func (s *artifactService) RemoveArtifactCommentThread(
	ctx context.Context,
	req *connect.Request[ariadnev1.RemoveArtifactCommentThreadRequest],
) (*connect.Response[ariadnev1.RemoveArtifactCommentThreadResponse], error) {
	msg := req.Msg
	if msg.ThreadId == "" {
		return nil, badInput("threadId")
	}
	comments, err := s.api.options.Repository.CommentOnArtifact(ctx, msg.ArtifactId, "user", object{
		"type":     "remove",
		"threadId": msg.ThreadId,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RemoveArtifactCommentThreadResponse{
		Comments: toProtoArtifactCommentThreads(comments),
	}), nil
}

// ---- Artifact proto conversion helpers ----

func toProtoArtifactPreview(raw any) *ariadnev1.ArtifactPreview {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	switch anyString(m["kind"]) {
	case "document":
		return &ariadnev1.ArtifactPreview{
			Preview: &ariadnev1.ArtifactPreview_Document{
				Document: &ariadnev1.DocumentArtifactPreview{
					Excerpt: anyString(m["excerpt"]),
					Lead:    anyString(m["lead"]),
				},
			},
		}
	case "whiteboard":
		minimap := []*ariadnev1.WhiteboardMinimapBox{}
		for _, box := range asMapSlice(m["minimap"]) {
			minimap = append(minimap, &ariadnev1.WhiteboardMinimapBox{
				X:    anyFloat64(box["x"]),
				Y:    anyFloat64(box["y"]),
				W:    anyFloat64(box["w"]),
				H:    anyFloat64(box["h"]),
				Kind: anyString(box["kind"]),
			})
		}
		return &ariadnev1.ArtifactPreview{
			Preview: &ariadnev1.ArtifactPreview_Whiteboard{
				Whiteboard: &ariadnev1.WhiteboardArtifactPreview{
					Items:          int32(anyInt64(m["items"])),
					Connectors:     int32(anyInt64(m["connectors"])),
					Texts:          anyStringSlice(m["texts"]),
					PrototypeCount: int32(anyInt64(m["prototypeCount"])),
					OptionCount:    int32(anyInt64(m["optionCount"])),
					PreviewImage:   anyStringPtr(m["previewImage"]),
					Minimap:        minimap,
				},
			},
		}
	case "dashboard":
		var takenAt *int64
		if m["takenAt"] != nil {
			v := anyInt64(m["takenAt"])
			takenAt = &v
		}
		tiles := []*ariadnev1.ArtifactPreviewTile{}
		for _, t := range asMapSlice(m["tiles"]) {
			tiles = append(tiles, &ariadnev1.ArtifactPreviewTile{
				Type:  anyString(t["type"]),
				X:     int32(anyInt64(t["x"])),
				Y:     int32(anyInt64(t["y"])),
				W:     int32(anyInt64(t["w"])),
				H:     int32(anyInt64(t["h"])),
				Title: anyStringPtr(t["title"]),
			})
		}
		return &ariadnev1.ArtifactPreview{
			Preview: &ariadnev1.ArtifactPreview_Dashboard{
				Dashboard: &ariadnev1.DashboardArtifactPreview{
					Widgets: int32(anyInt64(m["widgets"])),
					TakenAt: takenAt,
					Titles:  anyStringSlice(m["titles"]),
					Tiles:   tiles,
					IsLive:  anyBool(m["isLive"]),
				},
			},
		}
	default:
		return nil
	}
}

func toProtoArtifactSummary(item core.ArtifactSummary) *ariadnev1.ArtifactSummary {
	return &ariadnev1.ArtifactSummary{
		Id:           item.ID,
		ProjectId:    item.ProjectID,
		Kind:         item.Kind,
		Title:        item.Title,
		NodeId:       anyStringPtr(item.NodeID),
		Version:      item.Version,
		CreatedBy:    item.CreatedBy,
		UpdatedBy:    item.UpdatedBy,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		Preview:      toProtoArtifactPreview(item.Preview),
		OpenComments: item.OpenComments,
		CommentedAt:  item.CommentedAt,
	}
}

func toProtoGalleryArtifactSummary(item core.GalleryArtifactSummary) *ariadnev1.GalleryArtifactSummary {
	var updatedByLabel *string
	if item.UpdatedByLabel != "" {
		v := item.UpdatedByLabel
		updatedByLabel = &v
	}
	var nodeTitle *string
	if item.NodeTitle != "" {
		v := item.NodeTitle
		nodeTitle = &v
	}
	return &ariadnev1.GalleryArtifactSummary{
		Artifact:             toProtoArtifactSummary(item.Artifact),
		ProjectName:          item.ProjectName,
		UpdatedByLabel:       updatedByLabel,
		UpdatedByCoordinator: item.UpdatedByCoordinator,
		HasPrototype:         item.HasPrototype,
		PendingActions:       int32(item.PendingActions),
		PendingFeedback:      int32(item.PendingFeedback),
		NodeTitle:            nodeTitle,
	}
}

func toProtoGalleryCounts(counts *core.GalleryCounts) *ariadnev1.GalleryCounts {
	if counts == nil {
		return nil
	}
	projects := make([]*ariadnev1.GalleryProjectCount, 0, len(counts.Projects))
	for _, p := range counts.Projects {
		projects = append(projects, &ariadnev1.GalleryProjectCount{
			ProjectId:   p.ProjectID,
			ProjectName: p.ProjectName,
			Count:       int32(p.Count),
		})
	}
	return &ariadnev1.GalleryCounts{
		Total:           int32(counts.Total),
		Documents:       int32(counts.Documents),
		Whiteboards:     int32(counts.Whiteboards),
		Dashboards:      int32(counts.Dashboards),
		Prototypes:      int32(counts.Prototypes),
		NeedsMe:         int32(counts.NeedsMe),
		FeedbackPending: int32(counts.FeedbackPending),
		Projects:        projects,
	}
}

func toProtoDocumentAskBaseline(item *core.DocumentAskBaseline) *ariadnev1.DocumentAskBaseline {
	if item == nil {
		return nil
	}
	asks := make([]*ariadnev1.DocumentAskAnswerBaseline, 0, len(item.Asks))
	for _, ask := range item.Asks {
		asks = append(asks, &ariadnev1.DocumentAskAnswerBaseline{
			Id:       ask.ID,
			Selected: slices.Clone(ask.Selected),
			Note:     ask.Note,
		})
	}
	return &ariadnev1.DocumentAskBaseline{
		Version: item.Version,
		Asks:    asks,
	}
}

func toProtoArtifactView(item core.ArtifactView) *ariadnev1.ArtifactView {
	return &ariadnev1.ArtifactView{
		Id:           item.ID,
		ProjectId:    item.ProjectID,
		Kind:         item.Kind,
		Title:        item.Title,
		NodeId:       anyStringPtr(item.NodeID),
		Version:      item.Version,
		CreatedBy:    item.CreatedBy,
		UpdatedBy:    item.UpdatedBy,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		Preview:      toProtoArtifactPreview(item.Preview),
		OpenComments: item.OpenComments,
		CommentedAt:  item.CommentedAt,
		Content:      item.Content,
	}
}

func toProtoArtifactVersions(input any) []*ariadnev1.ArtifactVersionView {
	list := asMapSlice(input)
	out := make([]*ariadnev1.ArtifactVersionView, 0, len(list))
	for _, row := range list {
		out = append(out, &ariadnev1.ArtifactVersionView{
			Version:   anyInt64(row["version"]),
			Title:     anyString(row["title"]),
			Author:    anyString(row["author"]),
			Reason:    anyString(row["reason"]),
			CreatedAt: anyInt64(row["createdAt"]),
		})
	}
	return out
}

func toProtoArtifactCommentAnchor(raw any) *ariadnev1.ArtifactCommentAnchor {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	anchor := &ariadnev1.ArtifactCommentAnchor{
		Block: anyStringPtr(m["block"]),
	}
	if node := anyString(m["node"]); node != "" {
		anchor.Target = &ariadnev1.ArtifactCommentAnchor_Node{Node: node}
	} else if edge := anyString(m["edge"]); edge != "" {
		anchor.Target = &ariadnev1.ArtifactCommentAnchor_Edge{Edge: edge}
	}
	if em := asMap(m["element"]); em != nil {
		elem := &ariadnev1.ArtifactCommentElement{
			Label:    anyStringPtr(em["label"]),
			Role:     anyStringPtr(em["role"]),
			Tag:      anyStringPtr(em["tag"]),
			Selector: anyStringPtr(em["selector"]),
		}
		if rm := asMap(em["rect"]); rm != nil {
			elem.Rect = &ariadnev1.ArtifactCommentElementRect{
				X: anyFloat64(rm["x"]),
				Y: anyFloat64(rm["y"]),
				W: anyFloat64(rm["w"]),
				H: anyFloat64(rm["h"]),
			}
		}
		anchor.Element = elem
	}
	return anchor
}

func toProtoArtifactCommentThreads(input any) []*ariadnev1.ArtifactCommentThreadView {
	list := asMapSlice(input)
	out := make([]*ariadnev1.ArtifactCommentThreadView, 0, len(list))
	for _, thread := range list {
		comments := []*ariadnev1.ArtifactCommentView{}
		for _, c := range asMapSlice(thread["comments"]) {
			var editedAt *int64
			if c["editedAt"] != nil {
				v := anyInt64(c["editedAt"])
				editedAt = &v
			}
			comments = append(comments, &ariadnev1.ArtifactCommentView{
				Id:        anyString(c["id"]),
				Author:    anyString(c["author"]),
				Body:      anyString(c["body"]),
				CreatedAt: anyInt64(c["createdAt"]),
				EditedAt:  editedAt,
			})
		}
		var resolved *ariadnev1.ArtifactCommentResolution
		if r := asMap(thread["resolved"]); r != nil {
			resolved = &ariadnev1.ArtifactCommentResolution{
				By: anyString(r["by"]),
				At: anyInt64(r["at"]),
			}
		}
		out = append(out, &ariadnev1.ArtifactCommentThreadView{
			Id:         anyString(thread["id"]),
			ArtifactId: anyString(thread["artifactId"]),
			Quote:      anyString(thread["quote"]),
			Anchor:     toProtoArtifactCommentAnchor(thread["anchor"]),
			Comments:   comments,
			Resolved:   resolved,
			CreatedAt:  anyInt64(thread["createdAt"]),
		})
	}
	return out
}

func toProtoArtifactFeedbackView(input any) *ariadnev1.ArtifactFeedbackView {
	raw := asMap(input)
	if raw == nil {
		return nil
	}
	items := []*ariadnev1.ArtifactFeedbackItem{}
	for _, m := range asMapSlice(raw["items"]) {
		if converted := toProtoArtifactFeedbackItem(m); converted != nil {
			items = append(items, converted)
		}
	}
	images := []*ariadnev1.ArtifactAttachment{}
	for _, m := range asMapSlice(raw["images"]) {
		images = append(images, &ariadnev1.ArtifactAttachment{
			FileId: anyString(m["fileId"]),
			Name:   anyString(m["name"]),
			Mime:   anyString(m["mime"]),
			Size:   anyInt64(m["size"]),
		})
	}
	var doneAt *int64
	if raw["doneAt"] != nil {
		v := anyInt64(raw["doneAt"])
		doneAt = &v
	}
	return &ariadnev1.ArtifactFeedbackView{
		ArtifactId: anyString(raw["artifactId"]),
		Round:      anyInt64(raw["round"]),
		Version:    anyInt64(raw["version"]),
		Notify:     anyString(raw["notify"]),
		Items:      items,
		Images:     images,
		DoneBy:     anyStringPtr(raw["doneBy"]),
		DoneAt:     doneAt,
		Summary:    anyStringPtr(raw["summary"]),
		CreatedAt:  anyInt64(raw["createdAt"]),
	}
}
