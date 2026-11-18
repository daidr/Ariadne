package server

import (
	"math"
	"net/http"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/textutil"
)

func (api *API) artifactResponse(r *http.Request, artifact core.ArtifactView) (any, int, error) {
	repo := api.options.Repository
	versions, err := repo.ListArtifactVersions(r.Context(), artifact.ID)
	if err != nil {
		return nil, 0, err
	}
	comments, err := repo.ListArtifactComments(r.Context(), artifact.ID)
	return object{"artifact": artifact, "versions": versions, "comments": comments}, 200, err
}
func (api *API) registerArtifacts() {
	repo := api.options.Repository
	api.addRaw("GET", "/api/artifacts/:artifactId/live", func(w http.ResponseWriter, r *http.Request) error {
		return api.rooms.Serve(w, r, r.PathValue("artifactId"))
	})
	api.add("POST", "/api/artifacts/:artifactId/refresh", func(r *http.Request) (any, int, error) {
		reason := "刷新了数据"
		if api.options.Settings != nil && api.options.Settings.Locale() == "en" {
			reason = "Refreshed the data"
		}
		shared := api.options.SharedDashboard
		if shared == nil {
			shared = api.rooms
		}
		artifact, err := dashboard.Refresh(r.Context(), repo, api.options.Dashboards, r.PathValue("artifactId"), dashboard.RefreshOptions{Author: "user", Reason: reason, Now: api.options.Now, Shared: shared})
		return object{"artifact": artifact}, 200, err
	})
	api.add("POST", "/api/projects/:projectId/artifacts", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		kind := text(body, "kind")
		if kind != "document" && kind != "whiteboard" && kind != "dashboard" {
			return nil, 0, badInput("kind")
		}
		title, ok := body["title"].(string)
		if !ok {
			return nil, 0, badInput("title")
		}
		artifact, err := repo.CreateArtifact(r.Context(), r.PathValue("projectId"), core.NewArtifact{Kind: kind, Title: title, Author: "user", Content: core.EmptyContent(kind)})
		if err != nil {
			return nil, 0, err
		}
		return api.artifactResponse(r, artifact)
	})
	api.add("GET", "/api/artifacts/:artifactId", func(r *http.Request) (any, int, error) {
		var version *int64
		if r.URL.Query().Has("version") {
			raw := r.URL.Query().Get("version")
			value, err := queryNumber(raw)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value >= math.MaxInt64 {
				return nil, 0, badInput("version")
			}
			n := int64(value)
			version = &n
		}
		artifact, err := repo.GetArtifact(r.Context(), r.PathValue("artifactId"), version)
		if err != nil {
			return nil, 0, err
		}
		return api.artifactResponse(r, artifact)
	})
	api.add("PUT", "/api/artifacts/:artifactId", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		id := r.PathValue("artifactId")
		current, err := repo.GetArtifact(r.Context(), id, nil)
		if err != nil {
			return nil, 0, err
		}
		shared := collab.Enabled(current.Kind)
		raw, hasContent := body["content"]
		content, ok := raw.(string)
		if !ok && !(shared && hasContent && raw == nil) {
			return nil, 0, badInput("content")
		}
		base, ok := body["baseVersion"].(float64)
		if !ok || base != math.Trunc(base) || base >= math.MaxInt64 || base < math.MinInt64 {
			return nil, 0, badInput("baseVersion")
		}
		n := int64(base)
		input := core.ArtifactRevision{Content: content, Author: "user", Reason: text(body, "reason"), BaseVersion: &n}
		if title, ok := body["title"].(string); ok {
			input.Title = &title
		}
		if raw, ok := body["quotes"].(map[string]any); ok {
			quotes := map[string]string{}
			valid := true
			for key, value := range raw {
				quote, ok := value.(string)
				if !ok {
					valid = false
					break
				}
				quotes[key] = quote
			}
			if valid {
				input.Quotes = quotes
			}
		}
		var artifact core.ArtifactView
		if shared {
			var next *string
			if _, given := body["content"].(string); given {
				next = &content
			}
			artifact, err = api.rooms.Revise(r.Context(), id, next, input)
		} else {
			artifact, err = repo.ReviseArtifact(r.Context(), id, input)
		}
		if err != nil {
			return nil, 0, err
		}
		return api.artifactResponse(r, artifact)
	})
	api.add("POST", "/api/artifacts/:artifactId/comments", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		action, err := commentAction(body)
		if err != nil {
			return nil, 0, err
		}
		comments, err := repo.CommentOnArtifact(r.Context(), r.PathValue("artifactId"), "user", action)
		return object{"comments": comments}, 200, err
	})
	api.add("POST", "/api/artifacts/:artifactId/feedback", func(r *http.Request) (any, int, error) {
		id := r.PathValue("artifactId")
		if _, err := repo.GetArtifact(r.Context(), id, nil); err != nil {
			return nil, 0, err
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		images := []string{}
		if body["images"] != nil {
			raw, ok := body["images"].([]any)
			if !ok {
				return nil, 0, badInput("images")
			}
			for _, item := range raw {
				id, ok := item.(string)
				if !ok {
					return nil, 0, badInput("images")
				}
				images = append(images, id)
			}
		}
		if err := api.rooms.Flush(r.Context(), id); err != nil {
			return nil, 0, err
		}
		feedback, err := repo.SendPendingArtifactFeedback(r.Context(), id, images)
		return object{"feedback": feedback}, 200, err
	})
}
func commentAction(body object) (object, error) {
	need := func(key string) (string, error) {
		value, ok := body[key].(string)
		if !ok || value == "" {
			return "", badInput(key)
		}
		return value, nil
	}
	threadID, err := need("threadId")
	if err != nil {
		return nil, err
	}
	kind := text(body, "type")
	action := object{"type": kind, "threadId": threadID}
	switch kind {
	case "create":
		if raw, present := body["anchor"]; present {
			anchor, _ := raw.(map[string]any)
			node, edge := text(anchor, "node"), text(anchor, "edge")
			if (node == "") == (edge == "") {
				return nil, badInput("anchor")
			}
			if raw, present := anchor["block"]; present {
				block, ok := raw.(string)
				if !ok || textutil.Trim(block) == "" {
					return nil, badInput("anchor.block")
				}
			}
			quote := ""
			if raw, present := body["quote"]; present {
				var ok bool
				quote, ok = raw.(string)
				if !ok {
					return nil, badInput("quote")
				}
			}
			clean := object{}
			if node != "" {
				clean["node"] = node
			} else {
				clean["edge"] = edge
			}
			if block, ok := anchor["block"].(string); ok {
				clean["block"] = block
			}
			action["anchor"], action["quote"] = clean, quote
		} else {
			quote, err := need("quote")
			if err != nil {
				return nil, err
			}
			action["quote"] = quote
		}
		fallthrough
	case "reply", "edit":
		id, err := need("commentId")
		if err != nil {
			return nil, err
		}
		body, err := need("body")
		if err != nil {
			return nil, err
		}
		action["commentId"], action["body"] = id, body
	case "delete":
		id, err := need("commentId")
		if err != nil {
			return nil, err
		}
		action["commentId"] = id
	case "resolve", "reopen", "remove":
	default:
		return nil, badInput("type")
	}
	return action, nil
}
