package collab

import (
	"bytes"
	"context"
	"errors"
	"math"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
	"github.com/daidr/Ariadne/internal/core"
)

// Draft is a live model edit in an open room. Its changes are broadcast without
// becoming a version, and its undo manager tracks no user edits.
type Draft struct {
	room      *room
	Base      string
	Version   int64
	last      string
	writer    string
	undo      *crdt.UndoManager
	pieces    int
	wroteAt   time.Time
	changed   bool
	locked    map[string]bool
	reserved  string
	cursor    map[string]any
	lockPulse *time.Timer
}

func (rooms *Rooms) Draft(ctx context.Context, id string, kind, writer string) (*Draft, error) {
	rooms.mu.Lock()
	active := rooms.active[id]
	if active == nil {
		rooms.mu.Unlock()
		return nil, nil
	}
	active.mu.Lock()
	rooms.mu.Unlock()
	defer active.mu.Unlock()
	if active.doc == nil || active.artifact.Kind != kind || active.draft != nil || len(active.peers) == 0 || len(active.state) > maxRoomState/2 {
		return nil, nil
	}
	if !active.dirty.IsZero() {
		if _, err := active.save(ctx, core.ArtifactRevision{Author: "user"}); err != nil {
			return nil, err
		}
	}
	if kind != "dashboard" {
		if _, err := rooms.repo.SendPendingArtifactFeedback(ctx, id, nil); err != nil {
			return nil, err
		}
	}
	base := active.artifact.Content
	if kind == "document" {
		var err error
		base, err = normalizeDocument(base)
		if err != nil {
			return nil, err
		}
	}
	draft := &Draft{room: active, Base: active.artifact.Content, last: base, Version: active.artifact.Version, writer: writer}
	parts := []any{active.doc.GetMap("document")}
	switch kind {
	case "whiteboard":
		parts = []any{active.doc.GetMap("nodes"), active.doc.GetMap("edges")}
	case "dashboard":
		parts = []any{active.doc.GetMap("widgets"), active.doc.GetMap("snapshot")}
	}
	origins := crdt.NewSet()
	origins.Add(draft)
	draft.undo = crdt.NewUndoManager(parts, math.MaxInt, origins)
	active.draft = draft
	return draft, nil
}

func (draft *Draft) Write(next string) error {
	active := draft.room
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.draft != draft || next == draft.last || len(next) > 256<<10 || draft.pieces >= 128 || time.Since(draft.wroteAt) < 75*time.Millisecond {
		return nil
	}
	kind := active.artifact.Kind
	trial, err := Load(kind, active.state)
	if err != nil {
		return err
	}
	defer trial.Destroy()
	base := draft.last
	var changed map[string]bool
	switch kind {
	case "document":
		content, err := normalizeDocument(next)
		if err != nil {
			return err
		}
		changed, err = draft.updateDocumentPreview(trial, content)
		if err != nil {
			return err
		}
		next = content
	case "whiteboard":
		if err := UpdateWhiteboard(trial, next, &base, draft); err != nil {
			return err
		}
	case "dashboard":
		if err := UpdateDashboard(trial, next, &base, draft); err != nil {
			return err
		}
	}
	state, err := roomState(trial)
	if err != nil {
		return err
	}
	if !bytes.Equal(state, active.state) {
		change, err := crdt.EncodeStateAsUpdate(trial, crdt.EncodeStateVector(active.doc))
		if err != nil {
			return err
		}
		if kind == "document" {
			if err := draft.lockDocumentBlocks(changed); err != nil {
				return err
			}
		}
		writerState := observeWriter(active, func() { err = crdt.ApplyUpdate(active.doc, change, draft) }, draft.writer)
		if err != nil {
			return err
		}
		active.state = state
		active.broadcast(protocol.EncodeUpdate(change), nil)
		if kind == "document" {
			draft.cursor = writerState
			draft.publishDocumentWriter()
		} else {
			active.publishWriter(draft.writer, writerState)
		}
		draft.changed = true
	}
	draft.last, draft.wroteAt = next, time.Now()
	draft.pieces++
	return nil
}

func (draft *Draft) updateDocumentPreview(trial *crdt.Doc, content string) (map[string]bool, error) {
	current, err := ReadDocument(trial)
	if err != nil {
		return nil, err
	}
	merged, ok := core.MergeText(draft.last, current, content)
	if !ok {
		return nil, errors.New("the draft overlaps another edit")
	}
	return changedDocumentBlocks(trial, func() error { return UpdateDocument(trial, merged, draft) })
}

// Reserve the existing blocks a known replacement will change before any new
// text is visible. The probe only runs in a temporary copy of the document.
func (draft *Draft) ReserveDocument(next string) error {
	active := draft.room
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.draft != draft || active.artifact.Kind != "document" || next == draft.reserved || len(next) > 256<<10 {
		return nil
	}
	content, err := normalizeDocument(next)
	if err != nil {
		return err
	}
	trial, err := Load("document", active.state)
	if err != nil {
		return err
	}
	defer trial.Destroy()
	changed, err := draft.updateDocumentPreview(trial, content)
	if err != nil {
		return err
	}
	for id := range changed {
		if !active.doc.GetMap("document").Has(id) {
			delete(changed, id)
		}
	}
	if err := draft.lockDocumentBlocks(changed); err != nil {
		return err
	}
	draft.reserved = next
	return nil
}

func (draft *Draft) Close() {
	active := draft.room
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.draft == draft {
		active.cancelDraft()
	}
}

func (active *room) cancelDraft() {
	if active.rollbackDraft() {
		active.clearWriter()
	}
}

func (active *room) rollbackDraft() bool {
	draft := active.draft
	if draft == nil {
		return false
	}
	active.draft = nil
	if draft.lockPulse != nil {
		draft.lockPulse.Stop()
	}
	vector := crdt.EncodeStateVector(active.doc)
	for draft.undo.CanUndo() {
		if draft.undo.Undo() == nil {
			break
		}
	}
	draft.undo.Destroy()
	draft.undo = nil
	if draft.changed {
		state, err := roomState(active.doc)
		if err != nil {
			active.owner.report(err)
		} else {
			active.state = state
			active.checkpoint = true
		}
		if change, err := crdt.EncodeStateAsUpdate(active.doc, vector); err == nil {
			active.broadcast(protocol.EncodeUpdate(change), nil)
		} else {
			active.owner.report(err)
		}
	}
	if !active.dirty.IsZero() {
		active.changed()
	}
	return true
}
