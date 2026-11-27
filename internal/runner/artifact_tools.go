package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/textedit"
	"github.com/google/uuid"
)

func (turn *turn) artifactTools() []tool {
	widgets := arrayOf(args{"type": "object", "properties": args{"id": field("string"), "type": field("string"), "title": field("string"), "props": field("object"), "w": field("integer"), "h": field("integer")}, "required": []string{"id"}})
	items := arrayOf(args{"type": "object", "properties": args{"id": field("string"), "kind": field("string"), "text": field("string"), "title": field("string"), "note": field("string"), "tone": field("string"), "fill": field("string"), "stroke": field("string"), "textColor": field("string"), "color": field("string"), "file": field("string"), "path": field("string"), "alt": field("string"), "html": field("string"), "viewportWidth": field("integer"), "viewportHeight": field("integer"), "preview": field("object"), "tweaks": arrayOf(field("object")), "x": field("number"), "y": field("number"), "width": field("number"), "height": field("number")}, "required": []string{"id"}})
	connectors := arrayOf(args{"type": "object", "properties": args{"id": field("string"), "from": field("string"), "to": field("string"), "label": field("string"), "route": field("string"), "dash": field("string"), "arrow": field("string"), "color": field("string")}})
	groups := arrayOf(args{"type": "object", "properties": args{"id": field("string"), "title": field("string"), "note": field("string"), "pick": field("string"), "round": field("integer"), "confirmed": field("boolean")}, "required": []string{"id", "title"}})
	options := arrayOf(args{"type": "object", "properties": args{"id": field("string"), "title": field("string"), "note": field("string"), "text": field("string"), "revises": field("string"), "group": field("string"), "recommended": field("boolean"), "images": arrayOf(field("object")), "baselineImages": arrayOf(field("object")), "prototype": field("object")}, "required": []string{"id", "title"}})
	return []tool{
		readOnlySpec("list_artifacts", "List the documents, whiteboards and dashboards of this project.", args{}, nil, turn.listArtifacts),
		readOnlySpec("read_artifact", "Read an artifact of this project. Omit version for the current content, comments and feedback.",
			args{"artifactId": field("string"), "version": field("integer")}, []string{"artifactId"}, turn.readArtifact),
		specification("create_document", "Create a Markdown document in this project. Documents do not render Mermaid; for flowcharts, architecture diagrams or relationship graphs, put a JSON object with items and connectors (supporting the same fill, stroke/\"none\" borderless, textColor, and color fields as standalone whiteboards) in an embedded whiteboard code fence (or use a standalone whiteboard) instead. Use ```ask JSON blocks (question, optional id/description, pick: one|many|text, options with id/label/note/recommended) for inline structured questions and ```ref JSON blocks (artifactId, optional groupId/optionId/label) to embed live artifact cards. Share the artifact in chat as [Title](artifact:<artifactId>). Use <pending>description</pending> on its own line for an unfinished section.",
			args{"title": field("string"), "content": field("string"), "nodeId": field("string")}, []string{"title", "content"}, turn.createDocument),
		specification("update_document", "Change a document with edits, or replace the whole Markdown using the baseVersion you read. An edit replaces a unique passage from read_artifact. Documents do not render Mermaid; use an embedded whiteboard code fence (a JSON object with items and connectors, supporting fill, stroke/\"none\" borderless, textColor, and color) for diagrams. Supports ```ask inline question blocks and ```ref live artifact cards. Pending browser edits are saved before applying the change.",
			args{"artifactId": field("string"), "baseVersion": field("integer"), "title": field("string"), "reason": field("string"), "content": field("string"),
				"edits": arrayOf(args{"type": "object", "properties": args{"old": field("string"), "new": field("string")}, "required": []string{"old", "new"}})}, []string{"artifactId", "reason"}, turn.updateDocument),
		specification("comment_artifact", "Start a comment on a visible document passage (quote) or a whiteboard item (on). The optional at point pins a comment inside an item, with x and y from 0 to 1. Reply to existing comments by commentThread.",
			args{"artifactId": field("string"), "body": field("string"), "commentThread": field("string"), "quote": field("string"), "on": field("string"),
				"at": args{"type": "object", "properties": args{"x": field("number"), "y": field("number")}, "required": []string{"x", "y"}}}, []string{"artifactId", "body"}, turn.commentArtifact),
		specification("resolve_artifact_comment", "Resolve a comment thread after addressing the feedback.",
			args{"artifactId": field("string"), "commentThread": field("string")}, []string{"artifactId", "commentThread"}, turn.resolveArtifactComment),
		readOnlySpec("list_dashboard_sources", "List dashboard data sources offered by installed plugins.",
			args{}, nil, func(context.Context, args) (string, error) { return reply(turn.Dashboards.Describe(), nil) }),
		specification("refresh_dashboard", "Fetch the latest data for a live dashboard.",
			args{"artifactId": field("string")}, []string{"artifactId"}, turn.refreshDashboard),
		specification("create_dashboard", "Create a dashboard with stat, text, table, chart, pivot or pending widgets. Supply data as named data sets or choose a live source from list_dashboard_sources.",
			args{"title": field("string"), "nodeId": field("string"), "widgets": widgets, "data": field("object"), "source": field("object")}, []string{"title", "widgets"}, turn.createDashboard),
		specification("update_dashboard", "Update dashboard widgets and data sets by ID. Existing widget positions are kept. Set source to null to disconnect a live dashboard.",
			args{"artifactId": field("string"), "baseVersion": field("integer"), "reason": field("string"), "title": field("string"), "widgets": widgets,
				"data": field("object"), "removeWidgets": arrayOf(field("string")), "removeData": arrayOf(field("string")), "source": args{"type": []string{"object", "null"}}},
			[]string{"artifactId", "baseVersion", "reason"}, turn.updateDashboard),
		specification("create_whiteboard", "Create a whiteboard of shapes, sticky notes, text, standalone images (kind:\"image\" with file or path), self-contained interactive HTML prototypes (kind:\"prototype\" with html/path/file, no external network requests, viewportWidth/viewportHeight, and optional declarative tweaks via <script type=\"application/json\" data-ariadne-tweaks> or tweaks array; each tweak uses key, label, kind: color|range|enum|boolean|text, default, and optional section, min/max/step/unit for range, options for enum, or swatches for color; inside the sandbox read live values via :root CSS variables --tweak-<key> / --tweak-<key>-raw, data-tweak-<key> attribute, or window/document ariadne:tweaks CustomEvent with event.detail.values), and connectors, or grouped decision options (groups with pick/round and options with recommended, images, baselineImages or a prototype) for the user to compare. Shapes and sticky notes support fill (or \"none\") and textColor; shapes also support stroke (or \"none\" for borderless); text/drawing items and connectors support color (palette names gray|red|orange|yellow|green|blue|purple|pink or CSS colors; pass \"\" to reset). Items without coordinates are placed automatically.",
			args{"title": field("string"), "nodeId": field("string"), "items": items, "connectors": connectors, "groups": groups, "options": options, "direction": field("string"), "pick": field("string")}, []string{"title"}, turn.createWhiteboard),
		specification("update_whiteboard", "Add, change or remove whiteboard items (including kind:\"image\" and kind:\"prototype\"), connectors, decision groups and options by ID. Omitted items and their positions are kept. Pass \"\" to clear fill, stroke, textColor, color, or tone back to default. Appending groups with a higher round marks earlier rounds as confirmed and places the new round below. Set relayout to arrange the board again.",
			args{"artifactId": field("string"), "baseVersion": field("integer"), "title": field("string"), "reason": field("string"), "items": items, "connectors": connectors,
				"removeItems": arrayOf(field("string")), "removeConnectors": arrayOf(field("string")), "groups": groups, "options": options, "direction": field("string"), "pick": field("string"), "relayout": field("boolean")},
			[]string{"artifactId", "reason"}, turn.updateWhiteboard),
		specification("finish_feedback_round", "Mark an artifact feedback round as handled after addressing its edits and comments. Give the user a short summary.",
			args{"artifactId": field("string"), "round": field("integer"), "summary": field("string")},
			[]string{"artifactId", "round", "summary"}, turn.finishFeedback),
	}
}

func (turn *turn) listArtifacts(ctx context.Context, _ args) (string, error) {
	artifacts, err := turn.Repository.ListArtifacts(ctx, turn.conversation.ProjectID)
	if err != nil {
		return "", err
	}
	result := make([]args, 0, len(artifacts))
	for _, artifact := range artifacts {
		item := args{"artifactId": artifact.ID, "kind": artifact.Kind, "title": artifact.Title, "version": artifact.Version,
			"nodeId": artifact.NodeID, "updatedBy": artifact.UpdatedBy}
		if artifact.OpenComments > 0 {
			item["openComments"] = artifact.OpenComments
		}
		result = append(result, item)
	}
	return reply(result, nil)
}

func (turn *turn) projectArtifact(ctx context.Context, id string) (core.ArtifactView, error) {
	artifact, err := turn.Repository.GetArtifact(ctx, id, nil)
	if err == nil && artifact.ProjectID != turn.conversation.ProjectID {
		return core.ArtifactView{}, domainerr.NotFound(id)
	}
	return artifact, err
}

func (turn *turn) readArtifact(ctx context.Context, input args) (string, error) {
	id := text(input, "artifactId")
	artifact, err := turn.projectArtifact(ctx, id)
	if err != nil {
		return "", err
	}
	newest := true
	if raw, exists := input["version"]; exists && raw != nil {
		version, err := artifactInteger(raw, "version")
		if err != nil {
			return "", err
		}
		if version != artifact.Version {
			artifact, err = turn.Repository.GetArtifact(ctx, id, &version)
			if err != nil {
				return "", err
			}
			newest = false
		}
	} else if turn.Rooms != nil {
		if err := turn.Rooms.Flush(ctx, id); err != nil {
			return "", err
		}
		artifact, err = turn.projectArtifact(ctx, id)
		if err != nil {
			return "", err
		}
	}
	result := args{"artifactId": id, "title": artifact.Title, "kind": artifact.Kind, "version": artifact.Version, "updatedBy": artifact.UpdatedBy}
	switch artifact.Kind {
	case "dashboard":
		result["content"] = json.RawMessage(artifact.Content)
	case "whiteboard":
		var board whiteboard
		if err := json.Unmarshal([]byte(artifact.Content), &board); err != nil {
			return "", err
		}
		result["items"], result["connectors"] = board.summary(true)
	default:
		result["content"] = documentView(artifact.Content)
	}
	if problems := artifactProblems(artifact.Kind, artifact.Content); len(problems) > 0 {
		result["problems"] = problems
	}
	if newest && artifact.Kind != "dashboard" {
		result["comments"], err = turn.Repository.ListArtifactComments(ctx, id)
		if err != nil {
			return "", err
		}
		result["feedback"], err = turn.Repository.ListArtifactFeedback(ctx, id)
	}
	return reply(result, err)
}

func (turn *turn) createDocument(ctx context.Context, input args) (string, error) {
	content, ok := input["content"].(string)
	if !ok {
		return "", errors.New("content must be Markdown text")
	}
	content, err := documentFromView(content, "", false)
	if err != nil {
		return "", err
	}
	if err := collab.ValidateDocument(content); err != nil {
		return "", err
	}
	return turn.createArtifact(ctx, "document", input, content)
}

func (turn *turn) createArtifact(ctx context.Context, kind string, input args, content string) (string, error) {
	artifact, err := turn.Repository.CreateArtifact(ctx, turn.conversation.ProjectID, core.NewArtifact{
		Kind: kind, Title: text(input, "title"), Content: content, Author: turn.conversation.ID, NodeID: optional(input, "nodeId"),
	})
	if err != nil {
		return "", err
	}
	result := args{"artifactId": artifact.ID, "version": artifact.Version}
	if problems := artifactProblems(kind, artifact.Content); len(problems) > 0 {
		result["problems"] = problems
	}
	if kind == "document" && strings.Contains(content, "<pending>") {
		result["next"] = fmt.Sprintf("Share the link [%s](artifact:%s) with the user before filling in the placeholders.", strings.NewReplacer("[", "", "]", "").Replace(artifact.Title), artifact.ID)
	}
	return reply(result, nil)
}

func (turn *turn) updateDocument(ctx context.Context, input args) (string, error) {
	content, rewrite := input["content"].(string)
	edits, hasEdits := input["edits"]
	hasEdits = hasEdits && edits != nil
	if rewrite == hasEdits {
		return "", errors.New("give either edits or content")
	}
	return turn.editArtifact(ctx, "document", input, rewrite, func(base string) (string, error) {
		view := content
		if !rewrite {
			var err error
			view, err = documentEdits(documentView(base), edits)
			if err != nil {
				return "", err
			}
		}
		return documentFromView(view, base, false)
	})
}

func documentEdits(base string, edits any) (string, error) {
	values, ok := edits.([]any)
	if !ok || len(values) == 0 || len(values) > 128 {
		return "", errors.New("edits must have between 1 and 128 replacements")
	}
	for index, raw := range values {
		change, _ := raw.(args)
		old, oldOK := change["old"].(string)
		newText, newOK := change["new"].(string)
		if !oldOK || old == "" || !newOK {
			return "", fmt.Errorf("edit %d requires nonempty old text and a new string", index+1)
		}
		next, _, err := textedit.Replace(base, old, newText, false, 8<<20)
		if err != nil {
			return "", fmt.Errorf("document edit %d: %w", index+1, err)
		}
		base = next
	}
	return base, nil
}
func (turn *turn) editArtifact(ctx context.Context, kind string, input args, versionRequired bool, edit func(string) (string, error)) (string, error) {
	id := text(input, "artifactId")
	artifact, err := turn.projectArtifact(ctx, id)
	if err != nil {
		return "", err
	}
	if artifact.Kind != kind {
		return "", fmt.Errorf("artifact %s is not a %s", id, kind)
	}
	reason := text(input, "reason")
	if reason == "" {
		return "", errors.New("reason is required")
	}
	var from *int64
	if versionRequired {
		version, err := artifactInteger(input["baseVersion"], "baseVersion")
		if err != nil {
			return "", err
		}
		from = &version
	}
	revision := core.ArtifactRevision{Author: turn.conversation.ID, Reason: reason, Title: optional(input, "title"), WriterName: turn.artifactWriter(ctx)}
	if turn.Rooms != nil {
		artifact, err = turn.Rooms.Edit(ctx, id, revision, from, edit)
	} else {
		if from != nil && *from != artifact.Version {
			return "", domainerr.New("error.artifactConflict", map[string]any{"version": artifact.Version})
		}
		revision.Content, err = edit(artifact.Content)
		if err == nil {
			revision.BaseVersion = &artifact.Version
			artifact, err = turn.Repository.ReviseArtifact(ctx, id, revision)
		}
	}
	if err != nil {
		return "", err
	}
	result := args{"artifactId": id, "version": artifact.Version}
	if problems := artifactProblems(kind, artifact.Content); len(problems) > 0 {
		result["problems"] = problems
	}
	return reply(result, nil)
}

func (turn *turn) artifactWriter(ctx context.Context) string {
	if turn.conversation.Role == "thread" {
		if thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID); err == nil && thread.Title != "" {
			return thread.Title
		}
	}
	if turn.Settings != nil && turn.Settings.Locale() == "en" {
		return "Coordinator"
	}
	return "协调器"
}

func (turn *turn) refreshDashboard(ctx context.Context, input args) (string, error) {
	id := text(input, "artifactId")
	artifact, err := turn.projectArtifact(ctx, id)
	if err != nil {
		return "", err
	}
	if artifact.Kind != "dashboard" || turn.Dashboards == nil {
		return "", errors.New("a live dashboard is required")
	}
	options := dashboard.RefreshOptions{Author: turn.conversation.ID, Reason: "Refreshed dashboard data"}
	if turn.Rooms != nil {
		options.Shared = turn.Rooms
	}
	artifact, err = dashboard.Refresh(ctx, turn.Repository, turn.Dashboards, id, options)
	return reply(args{"artifactId": id, "version": artifact.Version}, err)
}

func (turn *turn) commentArtifact(ctx context.Context, input args) (string, error) {
	id := text(input, "artifactId")
	artifact, err := turn.projectArtifact(ctx, id)
	if err != nil {
		return "", err
	}
	thread := text(input, "commentThread")
	action := args{"type": "reply", "threadId": thread, "commentId": "cmt_" + uuid.NewString(), "body": text(input, "body")}
	if thread == "" {
		thread = "cth_" + uuid.NewString()
		action["type"], action["threadId"] = "create", thread
		if artifact.Kind == "whiteboard" {
			return turn.commentOnBoard(ctx, artifact, input, action)
		}
		quote := strings.TrimSpace(text(input, "quote"))
		if turn.Rooms != nil && artifact.Kind == "document" {
			artifact.Content, err = turn.Rooms.Content(ctx, id)
			if err != nil {
				return "", err
			}
		}
		if artifact.Kind != "document" || quote == "" || !collab.ContainsDocumentQuote(artifact.Content, quote) {
			return "", errors.New("give a passage from the document or an existing comment thread")
		}
		action["quote"] = quote
	}
	_, err = turn.Repository.CommentOnArtifact(ctx, id, turn.conversation.ID, action)
	return reply(args{"commentThread": thread}, err)
}

func (turn *turn) commentOnBoard(ctx context.Context, artifact core.ArtifactView, input, action args) (string, error) {
	on := strings.TrimSpace(text(input, "on"))
	thread := text(action, "threadId")
	if on == "" {
		return "", errors.New("give the ID of an item or connector in on")
	}
	var pin args
	if input["at"] != nil {
		point := objectField(input, "at")
		x, xOK := point["x"].(float64)
		y, yOK := point["y"].(float64)
		if !xOK || !yOK || math.IsInf(x+y, 0) || math.IsNaN(x+y) {
			return "", errors.New("at requires numeric x and y fractions")
		}
		pin = args{"x": min(1, max(0, x)), "y": min(1, max(0, y))}
	}
	edit := func(base string) (string, error) {
		var board whiteboard
		if err := json.Unmarshal([]byte(base), &board); err != nil {
			return "", err
		}
		item, key := boardItems(board.Nodes)[on], "node"
		if item == nil {
			item, key = boardItems(board.Edges)[on], "edge"
		}
		if item == nil || (key == "edge" && pin != nil) {
			return "", errors.New("the item or connector is missing, or the point is on a connector")
		}
		action["anchor"], action["quote"] = args{key: on}, ""
		comments, _ := item["comments"].([]any)
		item["comments"] = append(comments, thread)
		if pin != nil {
			pins := objectField(item, "commentPins")
			pins[thread], item["commentPins"] = pin, pins
		}
		encoded, err := jsonutil.Encode(board)
		return string(encoded), err
	}
	revision := core.ArtifactRevision{Author: turn.conversation.ID, BoardComment: action}
	var err error
	if turn.Rooms != nil {
		_, err = turn.Rooms.Edit(ctx, artifact.ID, revision, nil, edit)
	} else {
		revision.Content, err = edit(artifact.Content)
		if err == nil {
			revision.BaseVersion = &artifact.Version
			_, err = turn.Repository.ReviseArtifact(ctx, artifact.ID, revision)
		}
	}
	return reply(args{"commentThread": thread}, err)
}

func (turn *turn) resolveArtifactComment(ctx context.Context, input args) (string, error) {
	id, thread := text(input, "artifactId"), text(input, "commentThread")
	if _, err := turn.projectArtifact(ctx, id); err != nil {
		return "", err
	}
	_, err := turn.Repository.CommentOnArtifact(ctx, id, turn.conversation.ID, args{"type": "resolve", "threadId": thread})
	return reply(args{"commentThread": thread, "resolved": true}, err)
}

func (turn *turn) finishFeedback(ctx context.Context, input args) (string, error) {
	id := text(input, "artifactId")
	if _, err := turn.projectArtifact(ctx, id); err != nil {
		return "", err
	}
	round, err := artifactInteger(input["round"], "round")
	if err != nil {
		return "", err
	}
	summary := text(input, "summary")
	if summary == "" {
		return "", errors.New("summary is required")
	}
	_, err = turn.Repository.FinishArtifactFeedback(ctx, id, round, turn.conversation.ID, summary)
	return reply(args{"artifactId": id, "round": round, "done": true}, err)
}

func artifactInteger(raw any, key string) (int64, error) {
	value, ok := raw.(float64)
	if !ok || value != math.Trunc(value) || value < 1 || value >= math.MaxInt64 {
		return 0, errors.New(key + " must be a positive integer")
	}
	return int64(value), nil
}
