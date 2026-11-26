package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func TestArtifactGalleryRPCAndLightweightGet(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := core.Open(ctx, core.Options{
		Path: ":memory:",
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	prjA, err := repo.CreateProject(ctx, new("主工程"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	prjB, err := repo.CreateProject(ctx, new("组件库"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	largeHTML := "<div>" + strings.Repeat("prototype-body-", 200) + "</div>"
	boardJSON, err := json.Marshal(map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{
				"id": "proto-1", "type": "prototype", "x": 10, "y": 20, "width": 480, "height": 320,
				"data": map[string]any{
					"title":   "画廊原型",
					"note":    "含大体积 HTML",
					"html":    largeHTML,
					"preview": "/api/files/fil_proto_cover",
				},
			},
		},
		"edges": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	protoArt, err := repo.CreateArtifact(ctx, prjA.ID, core.NewArtifact{
		Kind:    "whiteboard",
		Title:   "交互画板",
		Author:  prjA.CoordinatorID,
		Content: string(boardJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateArtifact(ctx, prjB.ID, core.NewArtifact{
		Kind:    "document",
		Title:   "设计文档",
		Author:  "user",
		Content: "# 概览\n\n正文",
	}); err != nil {
		t.Fatal(err)
	}

	api, err := New(Options{Repository: repo, Token: "gallery-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewArtifactServiceClient(srv.Client(), srv.URL+rpcPrefix)
	if _, err := unauth.ListGalleryArtifacts(ctx, connect.NewRequest(&ariadnev1.ListGalleryArtifactsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", err)
	}

	client := ariadnev1connect.NewArtifactServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("gallery-token")),
	)

	res, err := client.ListGalleryArtifacts(ctx, connect.NewRequest(&ariadnev1.ListGalleryArtifactsRequest{
		IncludeCounts: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Items) != 2 || res.Msg.Counts == nil || res.Msg.Counts.Prototypes != 1 || res.Msg.Counts.Total != 2 {
		t.Fatalf("unexpected gallery response: items=%d counts=%+v", len(res.Msg.Items), res.Msg.Counts)
	}

	protoOnly, err := client.ListGalleryArtifacts(ctx, connect.NewRequest(&ariadnev1.ListGalleryArtifactsRequest{
		OnlyPrototypes: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(protoOnly.Msg.Items) != 1 || protoOnly.Msg.Items[0].Artifact.Id != protoArt.ID {
		t.Fatalf("only_prototypes = %+v", protoOnly.Msg.Items)
	}
	wbPreview := protoOnly.Msg.Items[0].Artifact.Preview.GetWhiteboard()
	if wbPreview == nil || wbPreview.PrototypeCount != 1 || wbPreview.GetPreviewImage() != "/api/files/fil_proto_cover" || len(wbPreview.Minimap) != 1 {
		t.Fatalf("unexpected whiteboard preview proto: %+v", wbPreview)
	}

	fullRes, err := client.GetArtifact(ctx, connect.NewRequest(&ariadnev1.GetArtifactRequest{
		ArtifactId: protoArt.ID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fullRes.Msg.Artifact.Content, "prototype-body-") {
		t.Fatalf("expected full GetArtifact to preserve prototype html")
	}

	lightRes, err := client.GetArtifact(ctx, connect.NewRequest(&ariadnev1.GetArtifactRequest{
		ArtifactId:  protoArt.ID,
		Lightweight: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lightRes.Msg.Artifact.Content, "prototype-body-") {
		t.Fatalf("expected lightweight GetArtifact to strip prototype html")
	}
	if !strings.Contains(lightRes.Msg.Artifact.Content, "/api/files/fil_proto_cover") || !strings.Contains(lightRes.Msg.Artifact.Content, "画廊原型") {
		t.Fatalf("expected lightweight GetArtifact to retain metadata: %s", lightRes.Msg.Artifact.Content)
	}
}
