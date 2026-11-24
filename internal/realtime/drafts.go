package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
	project, conversation, message string
	buf                            []byte
	escapedTextLen                 int
	blocks                         json.RawMessage
	startedAt                      int64
	shown                          bool
	timer                          *time.Timer
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

// jsonEscapedLen returns the byte length of s when encoded inside a JSON string
// literal by encoding/json (including HTML escaping of <, >, &).
func jsonEscapedLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			switch {
			case b == '\\' || b == '"' || b == '\n' || b == '\r' || b == '\t' || b == '\b' || b == '\f':
				n += 2
			case b < 0x20 || b == '<' || b == '>' || b == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			n += 3
		} else if r == '\u2028' || r == '\u2029' {
			n += 6
		} else {
			n += size
		}
		i += size
	}
	return n
}

func (drafts *ReplyDrafts) emit(entry *draftEntry, drop, posted bool) error {
	payload := &DraftUpdate{ProjectID: entry.project, ConversationID: entry.conversation}
	size := len(entry.project) + len(entry.conversation) + 32
	if !drop {
		text := string(entry.buf)
		payload.Draft = &ReplyDraft{
			MessageID: entry.message,
			Text:      text,
			Blocks:    entry.blocks,
			StartedAt: entry.startedAt,
			Posted:    posted,
		}
		size += len(entry.message) + len(text) + len(entry.blocks) + 64
	}
	return drafts.state.Set(Update{
		Kind:           "draft",
		ProjectID:      entry.project,
		ConversationID: entry.conversation,
		Value:          payload,
		Size:           size,
	}, !drop && !posted)
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
	drafts.bytes -= len(entry.buf) + len(entry.blocks)
	entry.buf, entry.blocks, entry.escapedTextLen = nil, nil, 0
	if entry.shown {
		_ = drafts.emit(entry, true, false)
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
		blocksUnchanged := bytes.Equal(blocks, entry.blocks)
		if text == "" && len(blocks) == 0 || (len(text) == len(entry.buf) && string(entry.buf) == text && blocksUnchanged) {
			return nil
		}
		size := len(text) + len(blocks)
		delta := size - len(entry.buf) - len(entry.blocks)
		if size > 200*1024 || drafts.bytes+delta > 16*1024*1024 {
			return errors.New("reply draft preview exceeds size limit")
		}
		if len(blocks) > 0 && !blocksUnchanged && !json.Valid(blocks) {
			return errors.New("invalid reply draft blocks JSON")
		}
		var nextEscapedLen int
		isAppend := len(text) >= len(entry.buf) && string(entry.buf) == text[:len(entry.buf)]
		if isAppend {
			nextEscapedLen = entry.escapedTextLen + jsonEscapedLen(text[len(entry.buf):])
		} else {
			nextEscapedLen = jsonEscapedLen(text)
		}
		frameSize := nextEscapedLen + len(blocks) + len(project) + len(conversation) + len(message) + 128
		if frameSize > MaxPendingBytes-1024 {
			return errors.New("reply draft preview exceeds frame limit")
		}
		if !entry.shown {
			startedAt := drafts.options.Now().UnixMilli()
			clonedText := strings.Clone(text)
			clonedBlocks := bytes.Clone(blocks)
			payload := &DraftUpdate{
				ProjectID:      project,
				ConversationID: conversation,
				Draft: &ReplyDraft{
					MessageID: message,
					Text:      clonedText,
					Blocks:    clonedBlocks,
					StartedAt: startedAt,
				},
			}
			updateSize := len(project) + len(conversation) + len(message) + len(clonedText) + len(clonedBlocks) + 96
			if err := drafts.state.Set(Update{
				Kind:           "draft",
				ProjectID:      project,
				ConversationID: conversation,
				Value:          payload,
				Size:           updateSize,
			}, true); err != nil {
				return err
			}
			entry.shown = true
			entry.buf = append(entry.buf[:0], clonedText...)
			entry.escapedTextLen = nextEscapedLen
			entry.blocks = clonedBlocks
			entry.startedAt = startedAt
			drafts.bytes += delta
			return nil
		}
		if isAppend {
			entry.buf = append(entry.buf, text[len(entry.buf):]...)
		} else {
			entry.buf = append(entry.buf[:0], text...)
		}
		entry.escapedTextLen = nextEscapedLen
		if !blocksUnchanged {
			entry.blocks = bytes.Clone(blocks)
		}
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
			_ = drafts.emit(entry, false, false)
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
				_ = drafts.emit(entry, true, false)
			}
		}
		delete(drafts.entries, conversation)
		drafts.bytes -= len(entry.buf) + len(entry.blocks)
		entry.buf, entry.blocks, entry.escapedTextLen = nil, nil, 0
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
