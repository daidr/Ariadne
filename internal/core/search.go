package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/textutil"
)

type SearchProjection struct{}

func (SearchProjection) Setup(context.Context, *sql.Tx) error          { return nil }
func (SearchProjection) Forget(context.Context, *sql.Tx, string) error { return nil }
func (SearchProjection) Apply(ctx context.Context, tx *sql.Tx, e eventstore.Event) error {
	var d object
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	searchEvent(w, e, d)
	return w.err
}
func boardWords(raw any) (string, bool) {
	board := asObject(raw)
	nodes, ok := board["nodes"].([]any)
	if !ok {
		return "", false
	}
	words := []string{}
	for _, raw := range nodes {
		if raw == nil {
			return "", false
		}
		data := asObject(raw).obj("data")
		for _, key := range []string{"text", "label"} {
			if text, ok := data[key].(string); ok {
				words = append(words, text)
			}
		}
	}
	return strings.Join(words, "\n"), true
}
func searchText(kind, content string) string {
	if kind == "whiteboard" || kind == "dashboard" {
		var raw any
		if json.Unmarshal([]byte(content), &raw) != nil {
			return ""
		}
		value := asObject(raw)
		if kind == "whiteboard" {
			text, _ := boardWords(value)
			return text
		}
		widgets, ok := value.obj("dashboard")["widgets"].([]any)
		if !ok {
			return ""
		}
		words := []string{}
		for _, raw := range widgets {
			widget := asObject(raw)
			if widget == nil || widget.obj("props") == nil {
				return ""
			}
			props := widget.obj("props")
			for _, raw := range []any{widget["title"], props["text"], props["content"]} {
				if text, ok := raw.(string); ok {
					words = append(words, text)
				}
			}
		}
		return strings.Join(words, "\n")
	}
	return documentBoard.ReplaceAllStringFunc(content, func(fence string) string {
		parts := documentBoard.FindStringSubmatch(fence)
		var value object
		if json.Unmarshal([]byte(parts[1]), &value) != nil {
			return fence
		}
		text, ok := boardWords(value["board"])
		if !ok {
			return fence
		}
		return text
	})
}
func searchEvent(w *projectionWriter, e eventstore.Event, d object) {
	put := func(kind string, refID, conversationID any, title, body string) {
		row := w.one("INSERT INTO search_docs (project_id, kind, ref_id, conversation_id, at) VALUES (?, ?, ?, ?, ?) RETURNING id", e.ProjectID, kind, refID, conversationID, e.At)
		if row != nil {
			w.run("INSERT INTO search_text (rowid, title, body) VALUES (?, ?, ?)", row["id"], title, body)
		}
	}
	change := func(refID any, title *string, body string, at *int64) {
		row := w.one("SELECT id FROM search_docs WHERE ref_id = ?", refID)
		if row == nil {
			return
		}
		if at != nil {
			w.run("UPDATE search_docs SET at = ? WHERE id = ?", *at, row["id"])
		}
		if title == nil {
			w.run("UPDATE search_text SET body = ? WHERE rowid = ?", body, row["id"])
		} else {
			w.run("UPDATE search_text SET title = ?, body = ? WHERE rowid = ?", *title, body, row["id"])
		}
	}
	switch e.Type {
	case "message.posted":
		put("message", d["messageId"], d["conversationId"], "", d.str("content"))
	case "message.edited":
		change(d["messageId"], nil, d.str("content"), nil)
	case "thread.created":
		put("thread", d["threadId"], d["threadId"], d.str("title"), "")
	case "thread.renamed":
		if row := w.one("SELECT id FROM search_docs WHERE ref_id = ?", d["threadId"]); row != nil {
			w.run("UPDATE search_text SET title = ? WHERE rowid = ?", d["title"], row["id"])
		}
	case "thread.reported":
		change(d["threadId"], nil, d.str("headline"), &e.At)
	case "artifact.created":
		put("artifact", d["artifactId"], nil, d.str("title"), searchText(d.str("kind"), d.str("content")))
	case "artifact.revised":
		title := d.str("title")
		change(d["artifactId"], &title, searchText(artifactKind(w, d["artifactId"]), d.str("content")), &e.At)
	}
}
func searchTerms(query string) []string {
	parts := spaces.Split(projectsettings.Trim(query), -1)
	terms := []string{}
	for _, term := range parts {
		if term != "" && !slices.Contains(terms, term) {
			terms = append(terms, term)
			if len(terms) == 8 {
				break
			}
		}
	}
	return terms
}

func searchRows(ctx context.Context, db queryer, projectID, kind string, terms []string, limit int) ([]record, error) {
	if len(terms) == 0 {
		return []record{}, nil
	}
	long := []string{}
	short := []string{}
	for _, term := range terms {
		if len([]rune(term)) >= 3 {
			long = append(long, term)
		} else {
			short = append(short, term)
		}
	}
	where := []string{"d.project_id = ?", "d.kind = ?"}
	args := []any{projectID, kind}
	if len(long) > 0 {
		where = append(where, "search_text MATCH ?")
		quoted := []string{}
		for _, term := range long {
			quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		}
		args = append(args, strings.Join(quoted, " AND "))
	}
	for _, term := range short {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term) + "%"
		where = append(where, `(t.title LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	args = append(args, limit)
	return records(ctx, db, "SELECT d.kind, d.ref_id, d.conversation_id, d.at, t.title, t.body FROM search_text t JOIN search_docs d ON d.id = t.rowid WHERE "+strings.Join(where, " AND ")+" ORDER BY d.at DESC LIMIT ?", args...)
}

var plainHeading = regexp.MustCompile(`(?m)^` + jsSpace + `{0,3}(?:#{1,6}|>|[-+*]|[0-9]+\.)` + jsSpace + `+`)
var plainEmphasis = regexp.MustCompile("(\\*\\*|__|`+|~~)")

func snippet(text string, terms []string) string {
	flat := markdownLinks.ReplaceAllString(text, "${1}")
	flat = plainHeading.ReplaceAllString(flat, "")
	flat = plainEmphasis.ReplaceAllString(flat, "")
	flat = projectsettings.Trim(spaces.ReplaceAllString(flat, " "))
	lower := jsLower(flat)
	found := -1
	for _, term := range terms {
		at := strings.Index(lower, jsLower(term))
		if at >= 0 {
			at = utf16Length(lower[:at])
			if found < 0 || at < found {
				found = at
			}
		}
	}
	start := 0
	if found > 24 {
		start = found - 24
	}
	end := min(utf16Length(flat), start+120)
	result := utf16Slice(flat, start, end)
	if start > 0 {
		result = "…" + result
	}
	if end < utf16Length(flat) {
		result += "…"
	}
	return result
}
func jsLower(value string) string { return textutil.Lower(value) }

func uniqueColumnValues(rows []record, column string) []string {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := row.text(column)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func inPlaceholders(count int) string {
	return strings.Repeat("?,", count-1) + "?"
}

func lookupThreadTitles(ctx context.Context, db queryer, projectID string, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, projectID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := records(ctx, db, "SELECT id, title FROM threads WHERE project_id = ? AND id IN ("+inPlaceholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	titles := make(map[string]string, len(rows))
	for _, row := range rows {
		titles[row.text("id")] = row.text("title")
	}
	return titles, nil
}

func lookupArtifactKinds(ctx context.Context, db queryer, projectID string, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, projectID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := records(ctx, db, "SELECT id, kind FROM artifacts WHERE project_id = ? AND id IN ("+inPlaceholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	kinds := make(map[string]string, len(rows))
	for _, row := range rows {
		kinds[row.text("id")] = row.text("kind")
	}
	return kinds, nil
}

func (reader Reader) Search(ctx context.Context, projectID, query string) (result object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		projects, err := records(ctx, tx, "SELECT id FROM projects WHERE id = ?", projectID)
		if err != nil {
			return err
		}
		if len(projects) == 0 {
			return domainerr.NotFound(projectID)
		}
		terms := searchTerms(query)
		result = object{"terms": terms}
		for _, kind := range []string{"thread", "artifact", "message"} {
			limit := 5
			if kind == "message" {
				limit = 30
			}
			rows, err := searchRows(ctx, tx, projectID, kind, terms, limit)
			if err != nil {
				return err
			}
			var titles, kinds map[string]string
			switch kind {
			case "message":
				titles, err = lookupThreadTitles(ctx, tx, projectID, uniqueColumnValues(rows, "conversation_id"))
				if err != nil {
					return err
				}
			case "artifact":
				kinds, err = lookupArtifactKinds(ctx, tx, projectID, uniqueColumnValues(rows, "ref_id"))
				if err != nil {
					return err
				}
			}
			hits := make([]object, 0, len(rows))
			for _, row := range rows {
				hit := object{"kind": kind, "id": row["ref_id"], "snippet": snippet(row.text("body"), terms), "at": row["at"]}
				if kind == "message" {
					conversationID := row.text("conversation_id")
					hit["conversationId"] = conversationID
					hit["where"] = nil
					if title, has := titles[conversationID]; has {
						hit["where"] = title
					}
				} else {
					hit["title"] = row["title"]
					if kind == "artifact" {
						artifactKind := kinds[row.text("ref_id")]
						if artifactKind == "" {
							artifactKind = "document"
						}
						hit["artifactKind"] = artifactKind
					}
				}
				hits = append(hits, hit)
			}
			result[kind+"s"] = hits
		}
		return nil
	})
	return
}
