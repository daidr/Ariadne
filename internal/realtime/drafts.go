package realtime

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

type ReplyDraft struct {
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
	StartedAt int64  `json:"startedAt"`
	Posted    bool   `json:"posted,omitempty"`
}
type DraftUpdate struct {
	ProjectID      string      `json:"projectId"`
	ConversationID string      `json:"conversationId"`
	Draft          *ReplyDraft `json:"draft"`
}
type draftEntry struct {
	project, conversation, message, text string
	startedAt                            int64
	shown                                bool
	timer                                *time.Timer
}
type DraftHandle struct {
	MessageID string
	Write     func(string) error
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
		update.Draft = &ReplyDraft{MessageID: entry.message, Text: entry.text, StartedAt: entry.startedAt, Posted: posted}
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
	drafts.bytes -= len(entry.text)
	entry.text = ""
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
	write := func(text string) error {
		drafts.mu.Lock()
		defer drafts.mu.Unlock()
		if drafts.closed || drafts.entries[conversation] != entry {
			return nil
		}
		if text == "" || text == entry.text {
			return nil
		}
		if len(text) > 200*1024 || drafts.bytes-len(entry.text)+len(text) > 16*1024*1024 {
			return errors.New("reply draft preview exceeds size limit")
		}
		oldText, oldAt := entry.text, entry.startedAt
		if entry.text == "" {
			entry.startedAt = drafts.options.Now().UnixMilli()
		}
		entry.text = text
		data, _ := json.Marshal(DraftUpdate{ProjectID: project, ConversationID: conversation, Draft: &ReplyDraft{MessageID: message, Text: text, StartedAt: entry.startedAt}})
		if len(data) > MaxPendingBytes-1024 {
			entry.text, entry.startedAt = oldText, oldAt
			return errors.New("reply draft preview exceeds frame limit")
		}
		entry.text = strings.Clone(text)
		if !entry.shown {
			if err := drafts.emit(entry, false, false); err != nil {
				entry.text, entry.startedAt = oldText, oldAt
				return err
			}
			entry.shown = true
		}
		drafts.bytes += len(text) - len(oldText)
		if oldText == "" || entry.timer != nil {
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
		drafts.bytes -= len(entry.text)
		entry.text = ""
	}
	return DraftHandle{MessageID: message, Write: write, Withdraw: withdraw, Posted: posted}, nil
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
