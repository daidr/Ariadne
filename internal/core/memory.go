package core

import (
	"context"
	"database/sql"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type MemoryView struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Text      string `json:"text"`
	Author    string `json:"author"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func listMemories(ctx context.Context, db queryer, projectID string) ([]MemoryView, error) {
	project, err := records(ctx, db, "SELECT id FROM projects WHERE id = ?", projectID)
	if err != nil {
		return nil, err
	}
	if len(project) == 0 {
		return nil, domainerr.NotFound(projectID)
	}
	rows, err := records(ctx, db, "SELECT * FROM memories WHERE project_id = ? ORDER BY created_at, id", projectID)
	if err != nil {
		return nil, err
	}
	result := []MemoryView{}
	for _, row := range rows {
		result = append(result, MemoryView{ID: row.text("id"), ProjectID: row.text("project_id"), Text: row.text("text"), Author: row.text("author"), CreatedAt: row.integer("created_at"), UpdatedAt: row.integer("updated_at")})
	}
	return result, nil
}
func (reader Reader) ListMemories(ctx context.Context, projectID string) (result []MemoryView, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error { result, err = listMemories(ctx, tx, projectID); return err })
	return
}

type MemoryChange struct {
	MemoryID *string
	Text     string
	Author   string
	Undoes   string
}

func saveMemory(ctx context.Context, tx *eventstore.Transaction, projectID string, input MemoryChange) (MemoryView, error) {
	text := projectsettings.Trim(spaces.ReplaceAllString(strings.ReplaceAll(input.Text, "\u0085", " "), " "))
	if text == "" {
		return MemoryView{}, badInput("text")
	}
	if utf16Length(text) > 500 {
		return MemoryView{}, badInput("text longer than 500")
	}
	existing, err := listMemories(ctx, tx.SQL(), projectID)
	if err != nil {
		return MemoryView{}, err
	}
	var same *MemoryView
	for i := range existing {
		if input.MemoryID != nil && existing[i].ID == *input.MemoryID {
			same = &existing[i]
			break
		}
	}
	if input.MemoryID != nil && *input.MemoryID != "" && same == nil {
		return MemoryView{}, domainerr.NotFound(*input.MemoryID)
	}
	if (input.MemoryID == nil || *input.MemoryID == "") && len(existing) >= 50 {
		return MemoryView{}, domainerr.New("error.memoryFull", map[string]any{"max": 50})
	}
	id := ""
	if input.MemoryID != nil {
		id = *input.MemoryID
	} else {
		id, err = newID("mem")
		if err != nil {
			return MemoryView{}, err
		}
	}
	if same != nil && same.Text == text {
		return *same, nil
	}
	data := object{"memoryId": id, "text": text, "author": input.Author}
	if input.Undoes != "" {
		data["undoes"] = input.Undoes
	}
	if err := appendChanges(tx, projectID, []change{{"memory.saved", data}}); err != nil {
		return MemoryView{}, err
	}
	after, err := listMemories(ctx, tx.SQL(), projectID)
	if err != nil {
		return MemoryView{}, err
	}
	for _, view := range after {
		if view.ID == id {
			return view, nil
		}
	}
	return MemoryView{}, domainerr.NotFound(id)
}
func (repo *Repository) SaveMemory(ctx context.Context, projectID string, input MemoryChange) (result MemoryView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		result, err = saveMemory(ctx, tx, projectID, input)
		return err
	})
	return
}
func removeMemory(ctx context.Context, tx *eventstore.Transaction, projectID, id, author, undoes string) error {
	existing, err := listMemories(ctx, tx.SQL(), projectID)
	if err != nil {
		return err
	}
	found := false
	for _, memory := range existing {
		found = found || memory.ID == id
	}
	if !found {
		return domainerr.NotFound(id)
	}
	data := object{"memoryId": id, "author": author}
	if undoes != "" {
		data["undoes"] = undoes
	}
	return appendChanges(tx, projectID, []change{{"memory.removed", data}})
}
func (repo *Repository) RemoveMemory(ctx context.Context, projectID, id, author, undoes string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error { return removeMemory(ctx, tx, projectID, id, author, undoes) })
}
func assertConversationIn(ctx context.Context, db queryer, projectID, id string) error {
	projects, err := records(ctx, db, "SELECT coordinator_id FROM projects WHERE id = ?", projectID)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return domainerr.NotFound(projectID)
	}
	if projects[0].text("coordinator_id") == id {
		return nil
	}
	threads, err := records(ctx, db, "SELECT project_id FROM threads WHERE id = ?", id)
	if err != nil {
		return err
	}
	if len(threads) == 0 || threads[0].text("project_id") != projectID {
		return domainerr.NotFound(id)
	}
	return nil
}
func (repo *Repository) NoteEverywhere(ctx context.Context, projectID, id, action, text string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		if err := assertConversationIn(ctx, tx.SQL(), projectID, id); err != nil {
			return err
		}
		return appendChanges(tx, projectID, []change{{"memory.everywhere", object{"conversationId": id, "action": action, "text": text}}})
	})
}
func (reader Reader) EverywhereNotice(ctx context.Context, projectID, id string) (object, error) {
	rows, err := records(ctx, reader.DB, "SELECT kind, payload, undone FROM notices WHERE id = ? AND project_id = ?", id, projectID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, domainerr.NotFound(id)
	}
	if rows[0].text("kind") != "memory-everywhere" {
		return nil, nil
	}
	payload, err := rows[0].json("payload", nil)
	if err != nil {
		return nil, err
	}
	view := except(asObject(payload), "")
	view["undone"] = rows[0].flag("undone")
	return view, nil
}
func markUndone(ctx context.Context, tx *eventstore.Transaction, projectID, id string) error {
	rows, err := records(ctx, tx.SQL(), "SELECT undone FROM notices WHERE id = ?", id)
	if err != nil {
		return err
	}
	if len(rows) > 0 && rows[0].flag("undone") {
		return nil
	}
	return appendChanges(tx, projectID, []change{{"notice.dismissed", object{"noticeId": id}}})
}
func (repo *Repository) SettleNotice(ctx context.Context, projectID, id string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error { return markUndone(ctx, tx, projectID, id) })
}
func (repo *Repository) UndoMemoryNotice(ctx context.Context, projectID, id string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), "SELECT conversation_id, kind, payload, undone FROM notices WHERE id = ? AND project_id = ?", id, projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 || rows[0].text("kind") != "memory" {
			return domainerr.NotFound(id)
		}
		if rows[0].flag("undone") {
			return nil
		}
		payload, err := rows[0].json("payload", nil)
		if err != nil {
			return err
		}
		change := asObject(payload)
		memories, err := listMemories(ctx, tx.SQL(), projectID)
		if err != nil {
			return err
		}
		exists := false
		for _, memory := range memories {
			exists = exists || memory.ID == change.str("memoryId")
		}
		if change.str("action") == "saved" && change["previous"] == nil {
			if exists {
				return removeMemory(ctx, tx, projectID, change.str("memoryId"), "user", id)
			}
			return markUndone(ctx, tx, projectID, id)
		}
		input := MemoryChange{Text: change.str("previous"), Author: "user", Undoes: id}
		if exists {
			memoryID := change.str("memoryId")
			input.MemoryID = &memoryID
		}
		if _, err := saveMemory(ctx, tx, projectID, input); err != nil {
			return err
		}
		return markUndone(ctx, tx, projectID, id)
	})
}

type TodoItem struct {
	Label  string  `json:"label"`
	Status string  `json:"status"`
	Detail *string `json:"detail,omitempty"`
}

func (repo *Repository) UpdateTodo(ctx context.Context, projectID, id string, items []TodoItem) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		if err := assertConversationIn(ctx, tx.SQL(), projectID, id); err != nil {
			return err
		}
		if items == nil {
			items = []TodoItem{}
		}
		before, err := readTodo(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		data := object{"conversationId": id, "items": items}
		if len(items) > 0 {
			if sameTodoSteps(array(before, "items"), items) {
				position := before.obj("position")
				if position == nil {
					position = object{"at": before["updatedAt"]}
				}
				data["position"] = position
			} else {
				rows, err := records(ctx, tx.SQL(), "SELECT id FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 1", id)
				if err != nil {
					return err
				}
				position := object{"afterMessageId": nil}
				if len(rows) > 0 {
					position["afterMessageId"] = rows[0]["id"]
				}
				data["position"] = position
			}
		}
		if err := appendChanges(tx, projectID, []change{{"todo.updated", data}}); err != nil {
			return err
		}
		result, err = readTodo(ctx, tx.SQL(), id)
		return err
	})
	return
}

func sameTodoSteps(before []any, items []TodoItem) bool {
	if len(before) != len(items) {
		return false
	}
	for i, item := range items {
		if asObject(before[i]).str("label") != item.Label {
			return false
		}
	}
	return true
}
