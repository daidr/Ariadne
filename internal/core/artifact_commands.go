package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/textutil"
)

const maxCommentQuote = 1000
const maxCommentBody = 4000

var inlineDataURL = regexp.MustCompile(`(?s)^data:([\w/+.-]+);base64,(.*)$`)

// Node's base64 decoder accepts whitespace, URL-safe characters, and missing
// padding. Preserve those existing inputs when moving an image into files.
func inlineBase64(input string) []byte {
	var clean strings.Builder
	for _, char := range input {
		if char == '=' {
			break
		}
		if char == '-' {
			char = '+'
		}
		if char == '_' {
			char = '/'
		}
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '+' || char == '/' {
			clean.WriteRune(char)
		}
	}
	value := clean.String()
	if len(value)%4 == 1 {
		value = value[:len(value)-1]
	}
	data, _ := base64.RawStdEncoding.DecodeString(value)
	return data
}
func (repo *Repository) storeArtifactImages(ctx context.Context, tx *eventstore.Transaction, projectID, kind, content string) (string, error) {
	if kind != "whiteboard" || utf16Length(content) > maxArtifactJSON*8 || !strings.Contains(content, `"data:`) {
		return content, nil
	}
	var board object
	if json.Unmarshal([]byte(content), &board) != nil || board == nil {
		return content, nil
	}
	nodes, ok := board["nodes"].([]any)
	if !ok {
		return content, nil
	}
	store := files.Store{Events: repo.Store, Now: func() time.Time { return time.UnixMilli(repo.Reader.now()) }}
	changed := false
	for _, raw := range nodes {
		node := asObject(raw)
		if node.str("type") != "image" {
			continue
		}
		data := node.obj("data")
		match := inlineDataURL.FindStringSubmatch(data.str("src"))
		if match == nil {
			continue
		}
		file, err := store.SaveTx(ctx, tx, projectID, match[1], inlineBase64(match[2]), false)
		if err != nil {
			return "", err
		}
		data["src"] = file.URL
		changed = true
	}
	if !changed {
		return content, nil
	}
	return encoded(board), nil
}

type NewArtifact struct {
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	NodeID  *string `json:"nodeId"`
	Author  string  `json:"author"`
	Content string  `json:"content"`
}

func (repo *Repository) CreateArtifact(ctx context.Context, projectID string, input NewArtifact) (result ArtifactView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		project := w.one("SELECT id FROM projects WHERE id = ?", projectID)
		if w.err != nil {
			return w.err
		}
		if project == nil {
			return domainerr.NotFound(projectID)
		}
		title := textutil.Trim(input.Title)
		if title == "" {
			return badInput("title")
		}
		content, err := repo.storeArtifactImages(ctx, tx, projectID, input.Kind, input.Content)
		if err != nil {
			return err
		}
		if err := checkArtifactContent(input.Kind, content); err != nil {
			return err
		}
		if input.NodeID != nil && *input.NodeID != "" {
			node := w.one("SELECT id FROM plan_nodes WHERE project_id = ? AND id = ?", projectID, *input.NodeID)
			if w.err != nil {
				return w.err
			}
			if node == nil {
				return domainerr.NotFound(*input.NodeID)
			}
		}
		id, err := newID("art")
		if err != nil {
			return err
		}
		data := object{"artifactId": id, "kind": input.Kind, "title": title, "nodeId": input.NodeID, "author": input.Author, "content": content}
		if err := appendChanges(tx, projectID, []change{{"artifact.created", data}}); err != nil {
			return err
		}
		result, err = getArtifact(ctx, tx.SQL(), id, nil)
		return err
	})
	return
}
func artifactListener(w *projectionWriter, artifactID string) any {
	row := w.one("SELECT project_id, last_agent FROM artifacts WHERE id = ?", artifactID)
	if row == nil {
		return nil
	}
	coordinator := w.one("SELECT coordinator_id FROM projects WHERE id = ?", row.text("project_id")).text("coordinator_id")
	lastAgent := row.text("last_agent")
	if lastAgent == "" || lastAgent == coordinator {
		return coordinator
	}
	thread := w.one("SELECT stopped_at FROM threads WHERE id = ?", lastAgent)
	if thread != nil && !thread.present("stopped_at") {
		return lastAgent
	}
	return coordinator
}

type ArtifactRevision struct {
	Title        *string           `json:"title"`
	Content      string            `json:"content"`
	Author       string            `json:"author"`
	Reason       string            `json:"reason"`
	BaseVersion  *int64            `json:"baseVersion"`
	Quotes       map[string]string `json:"quotes"`
	Shared       bool              `json:"shared"`
	SharedState  []byte            `json:"-"`
	BoardComment map[string]any    `json:"-"`
	WriterName   string            `json:"-"`
}

func (repo *Repository) ReviseArtifact(ctx context.Context, id string, input ArtifactRevision) (result ArtifactView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := getArtifact(ctx, tx.SQL(), id, nil)
		if err != nil {
			return err
		}
		if input.BaseVersion != nil && *input.BaseVersion != current.Version {
			return domainerr.New("error.artifactConflict", map[string]any{"version": current.Version})
		}
		if input.SharedState != nil && (!input.Shared || input.BaseVersion == nil || len(input.SharedState) > 32<<20) {
			return badInput("sharedState")
		}
		saveState := func(version int64) error {
			if input.SharedState == nil {
				return nil
			}
			return writeSharedState(ctx, tx, current.ProjectID, id, SharedState{Version: version, State: input.SharedState}, repo.Reader.now())
		}
		title := current.Title
		if input.Title != nil {
			if clean := textutil.Trim(*input.Title); clean != "" {
				title = clean
			}
		}
		content, err := repo.storeArtifactImages(ctx, tx, current.ProjectID, current.Kind, input.Content)
		if err != nil {
			return err
		}
		if err := checkArtifactContent(current.Kind, content); err != nil {
			return err
		}
		if input.SharedState != nil && content != input.Content {
			return badInput("sharedState contains unmaterialized images")
		}
		comments, err := artifactComments(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		quotes := object{}
		for _, thread := range comments {
			if given, ok := input.Quotes[thread.str("id")]; ok {
				quote := utf16Slice(given, 0, maxCommentQuote)
				if quote != "" && quote != thread.str("quote") {
					quotes[thread.str("id")] = quote
				}
			}
		}
		if title == current.Title && content == current.Content {
			result = current
			return saveState(current.Version)
		}
		data := object{"artifactId": id, "version": current.Version + 1, "title": title, "content": content, "author": input.Author, "reason": input.Reason}
		if input.Author == "user" && !input.Shared {
			w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
			data["notify"] = artifactListener(w, id)
			if w.err != nil {
				return w.err
			}
			data["diff"] = nil
			if current.Kind == "document" {
				data["diff"] = LineDiff(current.Content, content, 4000)
			}
		}
		if len(quotes) > 0 {
			data["quotes"] = quotes
		}
		changes := []change{{"artifact.revised", data}}
		if input.BoardComment != nil {
			action := object(input.BoardComment)
			if current.Kind != "whiteboard" || action.str("type") != "create" {
				return badInput("anchor")
			}
			w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
			if w.one("SELECT id FROM artifact_comments WHERE id = ?", action.str("threadId")) != nil {
				return badInput("threadId")
			}
			if w.err != nil {
				return w.err
			}
			checked, err := checkedComment(current.Kind, input.Author, action, nil)
			if err != nil {
				return err
			}
			changes = append(changes, change{"artifact.commented", object{"artifactId": id, "by": input.Author, "action": checked, "notify": nil}})
		}
		if err := appendChanges(tx, current.ProjectID, changes); err != nil {
			return err
		}
		if err := saveState(current.Version + 1); err != nil {
			return err
		}
		result, err = getArtifact(ctx, tx.SQL(), id, nil)
		return err
	})
	return
}
