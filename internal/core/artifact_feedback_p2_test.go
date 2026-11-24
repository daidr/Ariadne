package core

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrototypeNodeValidationAndTweakFeedback(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1931000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "test.sqlite"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	// Reject external src or missing html/src.
	badExternal := `{"version":1,"nodes":[{"id":"p1","type":"prototype","x":0,"y":0,"width":480,"height":320,"data":{"src":"https://example.com/a.html"}}],"edges":[]}`
	if err := ValidateArtifactContent("whiteboard", badExternal); err == nil {
		t.Fatal("expected external prototype src to fail validation")
	}
	badEmpty := `{"version":1,"nodes":[{"id":"p1","type":"prototype","x":0,"y":0,"width":480,"height":320,"data":{"title":"Empty"}}],"edges":[]}`
	if err := ValidateArtifactContent("whiteboard", badEmpty); err == nil {
		t.Fatal("expected empty prototype node to fail validation")
	}

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	boardV1, _ := json.Marshal(map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{
				"id": "opt-a", "type": "option", "x": 0, "y": 0, "width": 512, "height": 420,
				"data": map[string]any{"title": "方案 A"},
			},
			map[string]any{
				"id": "opt-a-prototype", "type": "prototype", "parent": "opt-a", "x": 16, "y": 40, "width": 480, "height": 336,
				"data": map[string]any{
					"title":          "工作台头部",
					"html":           "<!doctype html><html><body><button id=\"cta\">立即开始</button></body></html>",
					"viewportWidth":  1280,
					"viewportHeight": 800,
					"tweaks": []any{
						map[string]any{"key": "accent", "label": "主色", "kind": "color", "default": "#2563eb"},
						map[string]any{"key": "spacing", "label": "间距", "kind": "range", "min": 8, "max": 24, "step": 2, "unit": "px", "default": 16},
						map[string]any{"key": "dense", "label": "紧凑模式", "kind": "boolean", "default": false},
					},
				},
			},
		},
		"edges": []any{},
	})

	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "whiteboard",
		Title:   "交互原型板",
		Author:  project.CoordinatorID,
		Content: string(boardV1),
	})
	if err != nil {
		t.Fatalf("CreateArtifact failed: %v", err)
	}

	// User tweaks accent + spacing and pins a semantic element comment on the CTA button.
	boardV2, _ := json.Marshal(map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{
				"id": "opt-a", "type": "option", "x": 0, "y": 0, "width": 512, "height": 420,
				"data": map[string]any{"title": "方案 A"},
			},
			map[string]any{
				"id": "opt-a-prototype", "type": "prototype", "parent": "opt-a", "x": 16, "y": 40, "width": 480, "height": 336,
				"commentPins": map[string]any{
					"thr-btn": map[string]any{"x": 0.75, "y": 0.25},
				},
				"data": map[string]any{
					"title":          "工作台头部",
					"html":           "<!doctype html><html><body><button id=\"cta\">立即开始</button></body></html>",
					"viewportWidth":  1280,
					"viewportHeight": 800,
					"tweaks": []any{
						map[string]any{"key": "accent", "label": "主色", "kind": "color", "default": "#2563eb"},
						map[string]any{"key": "spacing", "label": "间距", "kind": "range", "min": 8, "max": 24, "step": 2, "unit": "px", "default": 16},
						map[string]any{"key": "dense", "label": "紧凑模式", "kind": "boolean", "default": false},
					},
					"tweakValues": map[string]any{
						"accent":  "#355ecb",
						"spacing": 12,
					},
				},
			},
		},
		"edges": []any{},
	})

	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     string(boardV2),
		BaseVersion: new(int64(1)),
		Author:      "user",
		Reason:      "tweak prototype params",
	}); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Second)
	if _, err := repo.CommentOnArtifact(ctx, artifact.ID, "user", object{
		"type":      "create",
		"threadId":  "thr-btn",
		"commentId": "cmt-1",
		"body":      "这个按钮字号再大一点",
		"anchor": object{
			"node": "opt-a-prototype",
			"element": object{
				"label":    "立即开始",
				"role":     "button",
				"tag":      "button",
				"selector": "body > button#cta",
				"rect":     object{"x": 0.65, "y": 0.2, "w": 0.2, "h": 0.08},
			},
		},
	}); err != nil {
		t.Fatalf("CommentOnArtifact failed: %v", err)
	}

	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatalf("SendPendingArtifactFeedback failed: %v", err)
	}
	items := feedbackItemsOf(t, fb)

	var foundTweak, foundComment object
	for _, it := range items {
		switch it.str("kind") {
		case "tweak":
			foundTweak = it
		case "comment":
			foundComment = it
		}
	}
	if foundTweak == nil {
		t.Fatalf("expected tweak feedback item, got %+v", items)
	}
	tweaksList, _ := foundTweak["tweaks"].([]any)
	if len(tweaksList) != 1 {
		t.Fatalf("expected 1 tweak entry, got %+v", tweaksList)
	}
	entry := object(tweaksList[0].(map[string]any))
	if entry.str("nodeId") != "opt-a-prototype" || entry.str("optionId") != "opt-a" || entry.str("optionTitle") != "方案 A" {
		t.Fatalf("unexpected tweak entry metadata: %+v", entry)
	}
	changes, _ := entry["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("expected 2 changed tweak values (accent + spacing), got %+v", changes)
	}

	if foundComment == nil {
		t.Fatalf("expected semantic comment item, got %+v", items)
	}
	where := foundComment.str("where")
	for _, want := range []string{`button "立即开始"`, `(body > button#cta)`, `prototype "工作台头部"`, `in option "方案 A"`} {
		if !strings.Contains(where, want) {
			t.Fatalf("expected comment where to contain %q, got %q", want, where)
		}
	}
}

func TestValidatePrototypeSrcRejectsPathTraversal(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{name: "valid file id", src: "/api/files/fil_01a20c-abc_123", wantErr: false},
		{name: "path traversal", src: "/api/files/../../api/settings", wantErr: true},
		{name: "extra segment", src: "/api/files/fil_abc/raw", wantErr: true},
		{name: "missing fil prefix", src: "/api/files/secret", wantErr: true},
		{name: "external url", src: "https://example.com/p.html", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			board := object{
				"version": 1,
				"nodes": []any{
					object{
						"id":     "p1",
						"type":   "prototype",
						"x":      0,
						"y":      0,
						"width":  480,
						"height": 336,
						"data":   object{"title": "Test", "src": tc.src},
					},
				},
				"edges": []any{},
			}
			encoded, _ := json.Marshal(board)
			err := ValidateArtifactContent("whiteboard", string(encoded))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for src=%q, got nil", tc.src)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected valid src=%q, got error %v", tc.src, err)
			}
		})
	}
}
