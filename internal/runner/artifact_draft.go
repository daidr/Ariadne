package runner

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/daidr/Ariadne/internal/collab"
)

func (turn *turn) clearPreviews() {
	turn.clearDraft()
	for _, draft := range turn.artifactDrafts {
		if draft != nil {
			draft.Close()
		}
	}
	turn.artifactDrafts = nil
}

func (turn *turn) previewArtifact(ctx context.Context, call *int, name, body string) {
	if turn.Rooms == nil || call == nil || len(body) > 256<<10 {
		return
	}
	var kind string
	switch name {
	case "update_document":
		kind = "document"
	case "update_whiteboard":
		kind = "whiteboard"
	case "update_dashboard":
		kind = "dashboard"
	default:
		return
	}
	draft, known := turn.artifactDrafts[*call]
	if known && draft == nil || !known && len(turn.artifactDrafts) >= 8 {
		return
	}
	input, partial := growingArtifactArguments(body)
	id := text(input, "artifactId")
	if id == "" {
		return
	}
	switch kind {
	case "document":
		if input["content"] == nil && input["edits"] == nil {
			return
		}
	case "whiteboard":
		if input["items"] == nil && input["connectors"] == nil {
			return
		}
	case "dashboard":
		if input["widgets"] == nil {
			return
		}
	}
	if !known {
		if turn.conversation.Role == "thread" {
			thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID)
			if err != nil || thread.ReviewOf != nil || thread.Stopped != nil {
				return
			}
		}
		if turn.artifactDrafts == nil {
			turn.artifactDrafts = map[int]*collab.Draft{}
		}
		turn.artifactDrafts[*call] = nil
		artifact, err := turn.projectArtifact(ctx, id)
		if err != nil || artifact.Kind != kind {
			return
		}
		draft, err = turn.Rooms.Draft(ctx, id, kind, turn.artifactWriter(ctx))
		if err != nil || draft == nil {
			return
		}
		turn.artifactDrafts[*call] = draft
	}
	if raw := input["baseVersion"]; raw != nil {
		version, err := artifactInteger(raw, "baseVersion")
		if err != nil || version != draft.Version {
			draft.Close()
			turn.artifactDrafts[*call] = nil
			return
		}
	}
	var next string
	var err error
	switch kind {
	case "document":
		if content, ok := input["content"].(string); ok {
			next = content
			if partial {
				next = previewRewrite(documentView(draft.Base), next)
			}
		} else {
			next, err = documentEdits(documentView(draft.Base), input["edits"])
		}
		if err == nil {
			next, err = documentFromView(next, draft.Base, true)
		}
	case "whiteboard":
		var change func(string) (string, error)
		change, err = turn.whiteboardChange(ctx, input)
		if err == nil {
			next, err = change(draft.Base)
		}
	case "dashboard":
		var change func(string) (string, error)
		change, err = turn.dashboardChange(ctx, input)
		if err == nil {
			next, err = change(draft.Base)
		}
	}
	if err == nil {
		_ = draft.Write(next)
	}
}

// Read only complete IDs and complete array entries. A growing Markdown string
// can be shown immediately. Removals, source fetches and image imports wait for
// the validated tool call.
func growingArtifactArguments(body string) (args, bool) {
	out := args{}
	decoder := json.NewDecoder(strings.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return out, false
	}
	for decoder.More() {
		raw, err := decoder.Token()
		if err != nil {
			return out, false
		}
		key, _ := raw.(string)
		switch key {
		case "edits", "items", "connectors", "widgets":
			if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
				return out, false
			}
			var entries []any
			for decoder.More() && len(entries) < 128 {
				var value args
				if err := decoder.Decode(&value); err != nil {
					if len(entries) > 0 {
						out[key] = entries
					}
					return out, false
				}
				entries = append(entries, value)
			}
			if len(entries) > 0 {
				out[key] = entries
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				return out, false
			}
		default:
			var value any
			if err := decoder.Decode(&value); err != nil {
				if key == "content" {
					out[key] = partialString(body, "content")
					return out, true
				}
				return out, false
			}
			switch key {
			case "artifactId", "baseVersion", "content", "direction", "data":
				out[key] = value
			}
		}
	}
	return out, false
}

func previewRewrite(base, growing string) string {
	before, next := strings.Split(base, "\n"), strings.Split(growing, "\n")
	if len(before) > 2000 || len(next) > 2000 {
		return growing
	}
	// Retain the old part until the draft passes a line it also contains.
	passed := 0
	for _, line := range next[:len(next)-1] {
		line = strings.TrimSpace(line)
		if len(line) < 3 {
			continue
		}
		for i := passed; i < len(before); i++ {
			if strings.TrimSpace(before[i]) == line {
				passed = i + 1
				break
			}
		}
	}
	if passed < len(before) && strings.HasPrefix(strings.TrimSpace(before[passed]), strings.TrimSpace(next[len(next)-1])) {
		next = next[:len(next)-1]
	}
	return strings.Join(append(next, before[passed:]...), "\n")
}
