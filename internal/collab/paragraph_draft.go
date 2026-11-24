package collab

import (
	"strings"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
)

// Keep short, single-block replacements on the existing Y.Text. Other edits
// use the general Markdown projection and merge in Draft.Write.
const maxPreviewParagraph = 4096

type paragraphDraft struct {
	id, anchor     string
	before, after  string
	prefix, suffix string
	rendered       string
}

func (draft *Draft) findParagraph(anchor string) (*paragraphDraft, error) {
	if draft.changed || anchor == "" || strings.ContainsAny(anchor, "\r\n") ||
		strings.Count(draft.Base, anchor) != 1 || strings.Count(draft.last, anchor) != 1 {
		return nil, nil
	}
	blocks, err := readDocumentBlocks(draft.room.doc.GetMap("document"))
	if err != nil {
		return nil, err
	}
	for _, block := range blocks {
		if block.kind != "paragraph" || len(block.children) != 0 {
			continue
		}
		line := renderDocumentBlock(block, 1)
		if len(line) > maxPreviewParagraph || strings.ContainsAny(line, "\r\n") || !strings.Contains(line, anchor) {
			continue
		}
		before, after, ok := strings.Cut(draft.last, line)
		if !ok || before != "" && !strings.HasSuffix(before, "\n\n") ||
			after != "" && !strings.HasPrefix(after, "\n\n") && after != "\n" {
			continue
		}
		prefix, suffix, _ := strings.Cut(line, anchor)
		return &paragraphDraft{id: block.id, anchor: anchor, before: before, after: after, prefix: prefix, suffix: suffix, rendered: line}, nil
	}
	return nil, nil
}

// A nil replacement reserves the paragraph as soon as its complete anchor arrives.
func (draft *Draft) WriteParagraph(anchor string, replacement *string) (bool, error) {
	active := draft.room
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.draft != draft {
		return true, nil
	}
	if active.artifact.Kind != "document" || active.exceedsHalfStateLimit() {
		return false, nil
	}
	if draft.paragraph == nil {
		paragraph, err := draft.findParagraph(anchor)
		if err != nil || paragraph == nil {
			return false, err
		}
		if err := draft.lockDocumentBlocks(map[string]bool{paragraph.id: true}); err != nil {
			return true, err
		}
		draft.paragraph = paragraph
	}
	paragraph := draft.paragraph
	if paragraph.anchor != anchor {
		return false, nil
	}
	if replacement == nil || draft.pieces >= 128 || time.Since(draft.wroteAt) < draftInterval {
		return true, nil
	}
	raw := paragraph.prefix + *replacement + paragraph.suffix
	if len(raw) > maxPreviewParagraph || strings.ContainsAny(raw, "\r\n") {
		return false, nil
	}
	blocks, err := parseMarkdown(raw)
	if err != nil || len(blocks) != 1 || blocks[0].kind != "paragraph" || len(blocks[0].children) != 0 {
		return false, err
	}
	rendered := renderDocumentBlock(blocks[0], 1)
	if rendered == paragraph.rendered {
		return true, nil
	}
	root := active.doc.GetMap("document")
	node, _ := root.Get(paragraph.id).(*crdt.YMap)
	if node == nil {
		return false, nil
	}
	text, _ := node.Get("text").(*crdt.YText)
	if text == nil {
		return false, nil
	}
	var cursor map[string]any
	change, err := captureUpdates(active.doc, func() error {
		cursor = observeWriter(active, func() {
			active.doc.Transact(func(*crdt.Transaction) {
				writeDocumentText(text, blocks[0].inline)
			}, draft)
		}, draft.writer)
		return nil
	})
	draft.changed = true
	if err == nil {
		var compacted []byte
		compacted, err = active.checkStateLimit(active.doc, len(change), 0)
		if err == nil {
			if len(change) > 0 {
				active.recordApplied(change, compacted, 0)
				if draft.shadow != nil {
					if syncErr := crdt.ApplyUpdate(draft.shadow, change, "sync"); syncErr != nil {
						draft.dropShadow()
					}
				}
				active.broadcast(protocol.EncodeUpdate(change), nil)
			}
			draft.cursor = cursor
			draft.publishDocumentWriter()
			paragraph.rendered = rendered
			draft.pieces++
			draft.wroteAt = time.Now()
			return true, nil
		}
	}
	// Roll back the draft if we cannot encode the resulting shared state.
	active.cancelDraft()
	// Peers never received the failed delta and need all of the rollback's
	// dependencies to integrate its new clocks.
	if state, stateErr := active.compactState(); stateErr == nil {
		active.broadcast(protocol.EncodeUpdate(state), nil)
	}
	return true, err
}
