package realtime

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/textutil"
)

const LiveTail = 8000

type LiveCommand struct {
	ProjectID      string `json:"projectId"`
	ConversationID string `json:"conversationId"`
	Command        string `json:"command"`
	CWD            string `json:"cwd"`
	StartedAt      int64  `json:"startedAt"`
	Tail           string `json:"tail"`
}
type LiveUpdate struct {
	ProjectID      string       `json:"projectId"`
	ConversationID string       `json:"conversationId"`
	Command        *LiveCommand `json:"command"`
}
type LiveOptions struct {
	Now      func() time.Time
	Interval time.Duration
}
type liveEntry struct {
	value    LiveCommand
	raw      string
	timer    *time.Timer
	sequence uint64
}
type LiveCommands struct {
	mu      sync.Mutex
	state   *State
	entries map[string]*liveEntry
	options LiveOptions
	closed  bool
	next    uint64
}
type LiveHandle struct {
	Output func(string)
	End    func()
}

func NewLive(state *State, options LiveOptions) *LiveCommands {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Interval <= 0 {
		options.Interval = 250 * time.Millisecond
	}
	return &LiveCommands{state: state, entries: map[string]*liveEntry{}, options: options}
}
func (live *LiveCommands) emit(entry *liveEntry, ended bool) error {
	update := LiveUpdate{ProjectID: entry.value.ProjectID, ConversationID: entry.value.ConversationID}
	if !ended {
		copy := entry.value
		update.Command = &copy
	}
	data, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return live.state.Set(Update{Kind: "live", ProjectID: update.ProjectID, ConversationID: update.ConversationID, Data: data}, !ended)
}
func (live *LiveCommands) Start(input LiveCommand) (LiveHandle, error) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.closed {
		return LiveHandle{}, errors.New("live commands are closed")
	}
	if len(input.Command) > 32*1024 || len(input.CWD) > 4096 {
		return LiveHandle{}, errors.New("live command metadata exceeds size limit")
	}
	if len(input.ProjectID) > 512 || len(input.ConversationID) > 512 {
		return LiveHandle{}, errors.New("live command identity exceeds size limit")
	}
	key := strings.Clone(input.ConversationID)
	if _, exists := live.entries[input.ConversationID]; !exists && len(live.entries) >= 1024 {
		return LiveHandle{}, errors.New("too many live commands")
	}
	entry := &liveEntry{value: LiveCommand{ProjectID: strings.Clone(input.ProjectID), ConversationID: key, Command: strings.Clone(input.Command), CWD: strings.Clone(input.CWD)}}
	if entry.value.CWD == "" {
		entry.value.CWD = "."
	}
	if old := live.entries[key]; old != nil {
		entry.sequence = old.sequence
	} else {
		live.next++
		entry.sequence = live.next
	}
	entry.value.StartedAt = live.options.Now().UnixMilli()
	entry.value.Tail = ""
	if err := live.emit(entry, false); err != nil {
		return LiveHandle{}, err
	}
	if old := live.entries[input.ConversationID]; old != nil {
		if old.timer != nil {
			old.timer.Stop()
		}
		old.raw = ""
		old.value.Command = ""
		old.value.CWD = ""
		old.value.Tail = ""
	}
	live.entries[key] = entry
	output := func(text string) {
		live.mu.Lock()
		defer live.mu.Unlock()
		if live.closed || live.entries[key] != entry || text == "" {
			return
		}
		entry.raw = textutil.TailUnits(entry.raw+text, LiveTail*2)
		if entry.timer != nil {
			return
		}
		entry.timer = time.AfterFunc(live.options.Interval, func() {
			live.mu.Lock()
			defer live.mu.Unlock()
			if live.closed || live.entries[key] != entry {
				return
			}
			entry.timer = nil
			entry.value.Tail = textutil.TailUnits(textutil.ScreenText(entry.raw), LiveTail)
			live.emit(entry, false)
		})
	}
	end := func() {
		live.mu.Lock()
		defer live.mu.Unlock()
		if live.entries[key] != entry {
			return
		}
		delete(live.entries, key)
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		entry.raw = ""
		live.emit(entry, true)
		entry.value.Command = ""
		entry.value.CWD = ""
		entry.value.Tail = ""
	}
	return LiveHandle{Output: output, End: end}, nil
}
func (live *LiveCommands) List(project string) []LiveCommand {
	live.mu.Lock()
	defer live.mu.Unlock()
	out := []LiveCommand{}
	ordered := []*liveEntry{}
	for _, entry := range live.entries {
		if project == "" || entry.value.ProjectID == project {
			ordered = append(ordered, entry)
		}
	}
	slices.SortFunc(ordered, func(a, b *liveEntry) int {
		if a.sequence < b.sequence {
			return -1
		}
		if a.sequence > b.sequence {
			return 1
		}
		return 0
	})
	for _, entry := range ordered {
		out = append(out, entry.value)
	}
	return out
}
func (live *LiveCommands) DropProject(project string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	for key, entry := range live.entries {
		if entry.value.ProjectID == project {
			delete(live.entries, key)
			if entry.timer != nil {
				entry.timer.Stop()
				entry.timer = nil
			}
			entry.raw = ""
			live.emit(entry, true)
			entry.value.Command = ""
			entry.value.CWD = ""
			entry.value.Tail = ""
		}
	}
}
func (live *LiveCommands) Close() {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.closed {
		return
	}
	live.closed = true
	for key, entry := range live.entries {
		delete(live.entries, key)
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		entry.raw = ""
		live.emit(entry, true)
		entry.value.Command = ""
		entry.value.CWD = ""
		entry.value.Tail = ""
	}
}
