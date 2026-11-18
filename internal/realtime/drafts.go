package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

type ReplyDraft struct {
	MessageID string          `json:"messageId"`
	Text      string          `json:"text"`
	Blocks    json.RawMessage `json:"blocks,omitempty"`
	StartedAt int64           `json:"startedAt"`
	Posted    bool            `json:"posted,omitempty"`
}
type DraftUpdate struct {
	ProjectID      string      `json:"projectId"`
	ConversationID string      `json:"conversationId"`
	Draft          *ReplyDraft `json:"draft"`
}
type draftEntry struct {
	project, conversation, message, text string
	blocks                               json.RawMessage
	startedAt                            int64
	shown                                bool
	timer                                *time.Timer
}
type DraftHandle struct {
	MessageID string
	Write     func(string) error
	WriteUI   func(string, json.RawMessage) error
	Withdraw  func()
	Posted    func()
}
type ReplyDrafts struct {
	mu      sync.Mutex
	state   *State
	entries map[string]*draftEntry
	options LiveOptions
	bytes   int
	closed  bool
}

func NewDrafts(state *State, options LiveOptions) *ReplyDrafts {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Interval <= 0 {
		options.Interval = 100 * time.Millisecond
	}
	return &ReplyDrafts{state: state, entries: map[string]*draftEntry{}, options: options}
}
func (drafts *ReplyDrafts) emit(entry *draftEntry, drop, posted bool) error {
	update := DraftUpdate{ProjectID: entry.project, ConversationID: entry.conversation}
	if !drop {
		update.Draft = &ReplyDraft{MessageID: entry.message, Text: entry.text, Blocks: entry.blocks, StartedAt: entry.startedAt, Posted: posted}
	}
	data, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return drafts.state.Set(Update{Kind: "draft", ProjectID: entry.project, ConversationID: entry.conversation, Data: data}, !drop && !posted)
}
func (drafts *ReplyDrafts) drop(entry *draftEntry) {
	if drafts.entries[entry.conversation] != entry {
		return
	}
	delete(drafts.entries, entry.conversation)
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	drafts.bytes -= len(entry.text) + len(entry.blocks)
	entry.text, entry.blocks = "", nil
	if entry.shown {
		drafts.emit(entry, true, false)
	}
}
func (drafts *ReplyDrafts) Start(project, conversation, message string) (DraftHandle, error) {
	drafts.mu.Lock()
	defer drafts.mu.Unlock()
	if drafts.closed {
		return DraftHandle{}, errors.New("reply drafts are closed")
	}
	if project == "" || conversation == "" || message == "" || len(project)+len(conversation)+len(message) > 1024 {
		return DraftHandle{}, errors.New("invalid reply draft identity")
	}
	if _, exists := drafts.entries[conversation]; !exists && len(drafts.entries) >= 1024 {
		return DraftHandle{}, errors.New("too many reply drafts")
	}
	project, conversation, message = strings.Clone(project), strings.Clone(conversation), strings.Clone(message)
	if old := drafts.entries[conversation]; old != nil {
		drafts.drop(old)
	}
	entry := &draftEntry{project: project, conversation: conversation, message: message}
	drafts.entries[conversation] = entry
	write := func(text string, blocks json.RawMessage) error {
		drafts.mu.Lock()
		defer drafts.mu.Unlock()
		if drafts.closed || drafts.entries[conversation] != entry {
			return nil
		}
		if text == "" && len(blocks) == 0 || text == entry.text && bytes.Equal(blocks, entry.blocks) {
			return nil
		}
		size := len(text) + len(blocks)
		delta := size - len(entry.text) - len(entry.blocks)
		if size > 200*1024 || drafts.bytes+delta > 16*1024*1024 {
			return errors.New("reply draft preview exceeds size limit")
		}
		startedAt := entry.startedAt
		if !entry.shown {
			startedAt = drafts.options.Now().UnixMilli()
		}
		data, err := json.Marshal(DraftUpdate{ProjectID: project, ConversationID: conversation, Draft: &ReplyDraft{MessageID: message, Text: text, Blocks: blocks, StartedAt: startedAt}})
		if err != nil {
			return err
		}
		if len(data) > MaxPendingBytes-1024 {
			return errors.New("reply draft preview exceeds frame limit")
		}
		if !entry.shown {
			if err := drafts.state.Set(Update{Kind: "draft", ProjectID: project, ConversationID: conversation, Data: data}, true); err != nil {
				return err
			}
			entry.shown = true
			entry.text, entry.blocks, entry.startedAt = strings.Clone(text), bytes.Clone(blocks), startedAt
			drafts.bytes += delta
			return nil
		}
		entry.text, entry.blocks = strings.Clone(text), bytes.Clone(blocks)
		drafts.bytes += delta
		if entry.timer != nil {
			return nil
		}
		entry.timer = time.AfterFunc(drafts.options.Interval, func() {
			drafts.mu.Lock()
			defer drafts.mu.Unlock()
			if drafts.closed || drafts.entries[conversation] != entry {
				return
			}
			entry.timer = nil
			drafts.emit(entry, false, false)
		})
		return nil
	}
	withdraw := func() { drafts.mu.Lock(); defer drafts.mu.Unlock(); drafts.drop(entry) }
	posted := func() {
		drafts.mu.Lock()
		defer drafts.mu.Unlock()
		if drafts.entries[conversation] != entry {
			return
		}
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		if entry.shown {
			if err := drafts.emit(entry, false, true); err != nil {
				drafts.emit(entry, true, false)
			}
		}
		delete(drafts.entries, conversation)
		drafts.bytes -= len(entry.text) + len(entry.blocks)
		entry.text, entry.blocks = "", nil
	}
	return DraftHandle{
		MessageID: message, Write: func(text string) error { return write(text, nil) },
		WriteUI: write, Withdraw: withdraw, Posted: posted,
	}, nil
}
func (drafts *ReplyDrafts) Drop(conversation string) {
	drafts.mu.Lock()
	defer drafts.mu.Unlock()
	if entry := drafts.entries[conversation]; entry != nil {
		drafts.drop(entry)
	}
}
func (drafts *ReplyDrafts) DropProject(project string) {
	drafts.mu.Lock()
	defer drafts.mu.Unlock()
	for _, entry := range drafts.entries {
		if entry.project == project {
			drafts.drop(entry)
		}
	}
}
func (drafts *ReplyDrafts) Close() {
	drafts.mu.Lock()
	defer drafts.mu.Unlock()
	if drafts.closed {
		return
	}
	drafts.closed = true
	for _, entry := range drafts.entries {
		drafts.drop(entry)
	}
}
