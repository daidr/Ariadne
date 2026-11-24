package collab

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
	"github.com/daidr/Ariadne/internal/core"
)

const draftInterval = 75 * time.Millisecond

// Draft is a live model edit in an open room. Its changes are broadcast without
// becoming a version, and its undo manager tracks no user edits.
type Draft struct {
	room      *room
	Base      string
	Version   int64
	last      string
	writer    string
	undo      *crdt.UndoManager
	shadow    *crdt.Doc
	pieces    int
	wroteAt   time.Time
	changed   bool
	locked    map[string]bool
	reserved  string
	cursor    map[string]any
	lockPulse *time.Timer
	paragraph *paragraphDraft
}

func (draft *Draft) ensureShadow() (*crdt.Doc, error) {
	if draft.shadow != nil {
		return draft.shadow, nil
	}
	doc, err := draft.room.cloneDoc()
	if err != nil {
		return nil, err
	}
	draft.shadow = doc
	return doc, nil
}

func (draft *Draft) dropShadow() {
	if draft.shadow != nil {
		draft.shadow.Destroy()
		draft.shadow = nil
	}
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
	if active.doc == nil || active.artifact.Kind != kind || active.draft != nil || len(active.peers) == 0 || active.exceedsHalfStateLimit() {
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

func (draft *Draft) syncParagraphLast() {
	if draft.paragraph != nil {
		draft.last = draft.paragraph.before + draft.paragraph.rendered + draft.paragraph.after
	}
}

func (draft *Draft) Write(next string) error {
	active := draft.room
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.draft != draft {
		return nil
	}
	draft.syncParagraphLast()
	if next == draft.last || len(next) > 256<<10 || draft.pieces >= 128 || time.Since(draft.wroteAt) < draftInterval {
		return nil
	}
	draft.paragraph = nil
	kind := active.artifact.Kind
	if kind == "document" {
		content, err := normalizeDocument(next)
		if err != nil {
			return err
		}
		next = content
	}
	trial, err := draft.ensureShadow()
	if err != nil {
		return err
	}
	base := draft.last
	var changed map[string]bool
	change, err := captureUpdates(trial, func() error {
		switch kind {
		case "document":
			var err error
			changed, err = draft.updateDocumentPreview(trial, next)
			return err
		case "whiteboard":
			return UpdateWhiteboard(trial, next, &base, draft)
		case "dashboard":
			return UpdateDashboard(trial, next, &base, draft)
		default:
			return nil
		}
	})
	if err != nil {
		if len(change) > 0 {
			draft.dropShadow()
		}
		return err
	}
	if len(change) > 0 {
		compacted, err := active.checkStateLimit(trial, len(change), 0)
		if err != nil {
			draft.dropShadow()
			return err
		}
		if kind == "document" {
			if err := draft.lockDocumentBlocks(changed); err != nil {
				draft.dropShadow()
				return err
			}
		}
		var applyErr error
		writerState := observeWriter(active, func() { applyErr = crdt.ApplyUpdate(active.doc, change, draft) }, draft.writer)
		if applyErr != nil {
			draft.dropShadow()
			return applyErr
		}
		active.recordApplied(change, compacted, 0)
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
	draft.syncParagraphLast()
	content, err := normalizeDocument(next)
	if err != nil {
		return err
	}
	trial, err := active.cloneDoc()
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
	draft.dropShadow()
	if draft.lockPulse != nil {
		draft.lockPulse.Stop()
	}
	change, err := captureUpdates(active.doc, func() error {
		for draft.undo.CanUndo() {
			if draft.undo.Undo() == nil {
				break
			}
		}
		return nil
	})
	draft.undo.Destroy()
	draft.undo = nil
	if draft.changed {
		if err != nil {
			active.owner.report(err)
		} else if len(change) > 0 {
			active.broadcast(protocol.EncodeUpdate(change), nil)
			compacted, limitErr := active.checkStateLimit(active.doc, len(change), 0)
			if limitErr != nil {
				active.owner.report(limitErr)
			} else {
				active.recordApplied(change, compacted, 0)
				active.checkpoint = true
			}
		} else {
			active.checkpoint = true
		}
	}
	if !active.dirty.IsZero() {
		active.changed()
	}
	return true
}
