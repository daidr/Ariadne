package collab

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
	"github.com/coder/websocket"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
)

const (
	maxRooms         = 16
	maxPeers         = 16
	maxAwareness     = 8 // identities owned by one connection
	maxQueuedBytes   = 16 << 20
	maxPendingDeltas = 4096
	saveDelay        = 1500 * time.Millisecond
)

var (
	maxRoomState    = 12 << 20
	maxPendingBytes = 512 << 10
)

type Rooms struct {
	repo       *core.Repository
	onError    func(error)
	onUserEdit atomic.Pointer[func()]
	mu         sync.Mutex
	active     map[string]*room
	closed     bool
}

type room struct {
	owner       *Rooms
	mu          sync.Mutex
	artifact    core.ArtifactView
	doc         *crdt.Doc
	state       []byte   // last compacted full CRDT state snapshot
	pending     [][]byte // incremental V1 updates applied since state
	pendingLen  int      // sum of len(u) in pending
	peers       map[*peer]bool
	timer       *time.Timer
	due         time.Time
	dirty       time.Time
	draft       *Draft
	checkpoint  bool // the rollback must survive a peer reconnect
	writerID    uint64
	writer      presence
	writerTimer *time.Timer
}

type peer struct {
	cancel    context.CancelFunc
	sends     chan []byte
	queued    atomic.Int64
	awareness map[uint64]presence
}

func NewRooms(repo *core.Repository, onError func(error)) *Rooms {
	return &Rooms{repo: repo, onError: onError, active: make(map[string]*room)}
}

// SetOnUserEdit registers a lightweight callback invoked whenever a connected
// browser peer edits a collaborative document, whiteboard, or dashboard.
func (rooms *Rooms) SetOnUserEdit(fn func()) {
	if fn == nil {
		rooms.onUserEdit.Store(nil)
		return
	}
	rooms.onUserEdit.Store(&fn)
}

func (rooms *Rooms) notifyUserEdit() {
	if fn := rooms.onUserEdit.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
}

func Enabled(kind string) bool {
	return kind == "document" || kind == "whiteboard" || kind == "dashboard"
}

func updateContent(doc *crdt.Doc, kind, content string) error {
	switch kind {
	case "document":
		return UpdateDocument(doc, content, "server")
	case "whiteboard":
		return UpdateWhiteboard(doc, content, nil, "server")
	case "dashboard":
		return UpdateDashboard(doc, content, nil, "server")
	default:
		return errors.New("unsupported collaboration kind")
	}
}

func readContent(doc *crdt.Doc, kind string) (string, error) {
	if kind == "document" {
		return ReadDocument(doc)
	}
	return ReadBoard(doc, kind)
}

func (rooms *Rooms) load(ctx context.Context, id string) (*room, error) {
	artifact, err := rooms.repo.GetArtifact(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if !Enabled(artifact.Kind) {
		return nil, domainerr.NotFound(id)
	}
	stored, err := rooms.repo.ReadSharedState(ctx, id)
	if err != nil {
		return nil, err
	}
	var state []byte
	if stored != nil && stored.Version == artifact.Version {
		state = stored.State
	}
	if len(state) > maxRoomState {
		return nil, domainerr.New("error.invalidBody", map[string]any{"reason": "collaboration state is too large"})
	}
	doc, err := Load(artifact.Kind, state)
	if err != nil {
		return nil, err
	}
	if len(state) == 0 {
		if err := updateContent(doc, artifact.Kind, artifact.Content); err != nil {
			doc.Destroy()
			return nil, err
		}
		state, err = roomState(doc)
		if err != nil {
			doc.Destroy()
			return nil, err
		}
		if err := rooms.repo.CheckpointSharedState(ctx, id, core.SharedState{Version: artifact.Version, State: state}); err != nil {
			doc.Destroy()
			return nil, err
		}
	}
	return &room{owner: rooms, artifact: artifact, doc: doc, state: state, peers: make(map[*peer]bool)}, nil
}

func roomState(doc *crdt.Doc) ([]byte, error) {
	state, err := crdt.EncodeStateAsUpdate(doc, nil)
	if err == nil && len(state) > maxRoomState {
		return nil, errors.New("collaboration state is too large")
	}
	return state, err
}

func captureUpdates(doc *crdt.Doc, run func() error) ([]byte, error) {
	var updates [][]byte
	handler := doc.OnUpdate(func(update []byte, _ any) {
		if len(update) > 0 {
			updates = append(updates, update)
		}
	})
	defer doc.OffUpdate(handler)
	if err := run(); err != nil {
		if len(updates) > 0 {
			return updates[0], err
		}
		return nil, err
	}
	switch len(updates) {
	case 0:
		return nil, nil
	case 1:
		return updates[0], nil
	default:
		return crdt.MergeUpdates(updates)
	}
}

func observeSnapshotChange(doc *crdt.Doc, run func() error) (bool, error) {
	changed := false
	snapshot := doc.GetMap("snapshot")
	listener := func(payload, _ any) {
		if events, ok := payload.([]crdt.IEventType); ok && len(events) > 0 {
			changed = true
		}
	}
	snapshot.ObserveDeep(listener)
	defer snapshot.UnobserveDeep(listener)
	err := run()
	return changed, err
}

// pendingSyncPayload returns the V1 update to log for an accepted client sync
// frame so restoreDoc reproduces both integrated changes and any unintegrated
// pendingStructs/pendingDeleteSet buffered inside doc.
func pendingSyncPayload(doc *crdt.Doc, update, body []byte) []byte {
	if len(body) <= 2 {
		return update
	}
	if bytes.Equal(update, body) {
		return update
	}
	if rem, err := crdt.DiffUpdate(body, crdt.EncodeStateVector(doc)); err == nil && len(rem) <= 2 {
		return update
	}
	return bytes.Clone(body)
}

func (active *room) estimatedStateSize() int {
	return len(active.state) + active.pendingLen
}

func (active *room) exceedsHalfStateLimit() bool {
	if active.estimatedStateSize() <= maxRoomState/2 {
		return false
	}
	state, err := active.compactState()
	return err != nil || len(state) > maxRoomState/2
}

func (active *room) setCompactedState(state []byte) {
	active.state = state
	active.pending = nil
	active.pendingLen = 0
}

func (active *room) compactState() ([]byte, error) {
	if len(active.pending) == 0 && len(active.state) > 0 {
		return active.state, nil
	}
	state, err := roomState(active.doc)
	if err != nil {
		return nil, err
	}
	active.setCompactedState(state)
	return state, nil
}

// checkStateLimit enforces maxRoomState and compacts lazily once the
// uncompacted delta log crosses a bounded threshold. It returns a non-nil
// snapshot only when compaction ran.
func (active *room) checkStateLimit(doc *crdt.Doc, deltaLen int) ([]byte, error) {
	if deltaLen <= 0 {
		return nil, nil
	}
	nextPendingLen := active.pendingLen + deltaLen
	nextPendingCount := len(active.pending) + 1
	if len(active.state)+nextPendingLen > maxRoomState ||
		nextPendingLen >= maxPendingBytes || nextPendingCount >= maxPendingDeltas {
		return roomState(doc)
	}
	return nil, nil
}

func (active *room) recordApplied(entry, compacted []byte) {
	if compacted != nil {
		active.setCompactedState(compacted)
		return
	}
	if len(entry) > 0 {
		active.pending = append(active.pending, entry)
		active.pendingLen += len(entry)
	}
}

func (active *room) restoreDoc() (*crdt.Doc, error) {
	doc, err := Load(active.artifact.Kind, active.state)
	if err != nil {
		return nil, err
	}
	for _, delta := range active.pending {
		if err := crdt.ApplyUpdate(doc, delta, "load"); err != nil {
			doc.Destroy()
			return nil, err
		}
	}
	return doc, nil
}

func (active *room) cloneDoc() (*crdt.Doc, error) {
	return active.restoreDoc()
}

func (rooms *Rooms) lock(ctx context.Context, id string) (*room, error) {
	rooms.mu.Lock()
	defer rooms.mu.Unlock()
	if rooms.closed {
		return nil, errors.New("collaboration is closed")
	}
	active := rooms.active[id]
	if active == nil {
		if len(rooms.active) >= maxRooms {
			return nil, domainerr.New("error.invalidBody", map[string]any{"reason": "too many collaboration rooms"})
		}
		var err error
		active, err = rooms.load(ctx, id)
		if err != nil {
			return nil, err
		}
		rooms.active[id] = active
	}
	active.mu.Lock()
	if active.doc == nil {
		active.mu.Unlock()
		return nil, errors.New("collaboration state is unavailable")
	}
	return active, nil
}

func (rooms *Rooms) Serve(w http.ResponseWriter, r *http.Request, id string) error {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	active, err := rooms.lock(ctx, id)
	if err != nil {
		return err
	}
	if len(active.peers) >= maxPeers {
		active.mu.Unlock()
		return domainerr.New("error.invalidBody", map[string]any{"reason": "too many collaborators"})
	}
	client := &peer{cancel: cancel, sends: make(chan []byte, 24), awareness: make(map[uint64]presence)}
	active.peers[client] = true
	client.queue(protocol.EncodeSyncStep1(active.doc))
	client.queue(active.presenceFrame())
	active.mu.Unlock()
	defer rooms.release(active, client)
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept already wrote the HTTP error.
		return nil
	}
	defer socket.CloseNow()
	socket.SetReadLimit(int64(maxRoomState) + 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-client.sends:
				timeout, stop := context.WithTimeout(ctx, 5*time.Second)
				err := socket.Write(timeout, websocket.MessageBinary, frame)
				stop()
				client.queued.Add(-int64(len(frame)))
				if err != nil {
					return
				}
			case <-ping.C:
				timeout, stop := context.WithTimeout(ctx, 10*time.Second)
				err := socket.Ping(timeout)
				stop()
				if err != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	for {
		kind, body, err := socket.Read(ctx)
		if err != nil {
			return nil
		}
		if kind != websocket.MessageBinary {
			return nil
		}
		if err := active.receive(client, body); err != nil {
			if errors.Is(err, errDocumentBlockLocked) {
				_ = socket.Close(4409, errDocumentBlockLocked.Error())
			}
			return nil
		}
	}
}

func (client *peer) queue(frame []byte) {
	if len(frame) == 0 {
		return
	}
	size := int64(len(frame))
	if client.queued.Add(size) > maxQueuedBytes {
		client.queued.Add(-size)
		client.cancel()
		return
	}
	select {
	case client.sends <- frame:
	default:
		client.queued.Add(-size)
		client.cancel()
	}
}

func (active *room) receive(client *peer, frame []byte) error {
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.doc == nil {
		return errors.New("collaboration state is unavailable")
	}
	if !active.peers[client] {
		return errors.New("room is closed")
	}
	info, err := protocol.InspectMessage(frame)
	if err != nil {
		return err
	}
	switch info.Type {
	case protocol.MessageSync:
		if info.SyncType == protocol.SyncMessageStep1 {
			reply, err := HandleSync(active.doc, frame, client)
			client.queue(reply)
			return err
		}
		if info.SyncType != protocol.SyncMessageStep2 && info.SyncType != protocol.SyncMessageUpdate {
			return nil
		}
		isDashboard := active.artifact.Kind == "dashboard"
		if active.draft != nil {
			trial, err := active.draft.ensureShadow()
			if err != nil {
				return err
			}
			var snapshotChanged bool
			var lockedChanged bool
			update, err := captureUpdates(trial, func() error {
				apply := func() error {
					_, err := HandleSync(trial, frame, client)
					return err
				}
				if isDashboard {
					inner := apply
					apply = func() error {
						var err error
						snapshotChanged, err = observeSnapshotChange(trial, inner)
						return err
					}
				}
				if locks := active.draft.locked; len(locks) != 0 {
					changed, err := collectChangedTypes(trial, apply)
					if err == nil && documentLocksChanged(active.doc, trial, locks, changed) {
						lockedChanged = true
					}
					return err
				}
				return apply()
			})
			if err == nil && lockedChanged {
				err = errDocumentBlockLocked
			}
			if err == nil && snapshotChanged {
				err = errors.New("dashboard data comes from the server")
			}
			var entry []byte
			var compacted []byte
			if err == nil {
				entry = pendingSyncPayload(trial, update, info.Body)
				compacted, err = active.checkStateLimit(trial, max(len(update), len(entry)))
			}
			if err != nil {
				active.draft.dropShadow()
				return err
			}
			actualUpdate, err := captureUpdates(active.doc, func() error {
				_, err := HandleSync(active.doc, frame, client)
				return err
			})
			if err != nil {
				active.restore()
				return err
			}
			if !bytes.Equal(actualUpdate, update) {
				if isDashboard || len(active.draft.locked) != 0 {
					active.restore()
					return errors.New("collaboration state diverged during draft")
				}
				active.draft.dropShadow()
				update = actualUpdate
				entry = pendingSyncPayload(active.doc, update, info.Body)
				compacted = nil
				if deltaLen := max(len(update), len(entry)); deltaLen > 0 {
					compacted, err = active.checkStateLimit(active.doc, deltaLen)
					if err != nil {
						active.restore()
						return err
					}
				}
			}
			active.recordApplied(entry, compacted)
			if len(update) > 0 {
				active.broadcast(protocol.EncodeUpdate(update), client)
				active.changed()
			}
			return nil
		}
		var snapshotChanged bool
		update, err := captureUpdates(active.doc, func() error {
			apply := func() error {
				_, err := HandleSync(active.doc, frame, client)
				return err
			}
			if isDashboard {
				var err error
				snapshotChanged, err = observeSnapshotChange(active.doc, apply)
				return err
			}
			return apply()
		})
		if err != nil {
			active.restore()
			return err
		}
		if snapshotChanged {
			active.restore()
			return errors.New("dashboard data comes from the server")
		}
		entry := pendingSyncPayload(active.doc, update, info.Body)
		compacted, err := active.checkStateLimit(active.doc, max(len(update), len(entry)))
		if err != nil {
			active.restore()
			return err
		}
		active.recordApplied(entry, compacted)
		if len(update) > 0 {
			active.broadcast(protocol.EncodeUpdate(update), client)
			active.changed()
		}
	case protocol.MessageAwareness:
		return active.acceptPresence(client, info.Body)
	case 3: // y-websocket query awareness
		client.queue(active.presenceFrame())
	}
	return nil
}

func (active *room) restore() {
	if active.draft != nil {
		active.cancelDraft()
	}
	active.doc.Destroy()
	doc, err := active.restoreDoc()
	active.doc = doc
	if err != nil {
		for client := range active.peers {
			client.cancel()
		}
		active.owner.report(err)
	}
}

func (active *room) broadcast(frame []byte, except *peer) {
	for client := range active.peers {
		if client != except {
			client.queue(frame)
		}
	}
}

func (active *room) changed() {
	active.owner.notifyUserEdit()
	now := time.Now()
	if active.dirty.IsZero() {
		active.dirty = now
	}
	active.due = now.Add(saveDelay)
	if latest := active.dirty.Add(5 * time.Second); active.due.After(latest) {
		active.due = latest
	}
	if active.timer == nil {
		active.timer = time.AfterFunc(time.Until(active.due), active.autosave)
	} else {
		active.timer.Reset(time.Until(active.due))
	}
}

func (active *room) autosave() {
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.doc == nil || active.dirty.IsZero() || active.draft != nil {
		return
	}
	if wait := time.Until(active.due); wait > 0 {
		active.timer.Reset(wait)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := active.save(ctx, core.ArtifactRevision{Author: "user"})
	active.owner.report(err)
}

func (active *room) save(ctx context.Context, input core.ArtifactRevision) (core.ArtifactView, error) {
	if active.draft != nil {
		return core.ArtifactView{}, errors.New("artifact has an unfinished draft")
	}
	state, err := active.compactState()
	if err != nil {
		return core.ArtifactView{}, err
	}
	content, err := readContent(active.doc, active.artifact.Kind)
	if err != nil {
		return core.ArtifactView{}, err
	}
	if active.artifact.Kind == "document" {
		if input.Content != "" && strings.TrimSpace(input.Content) == strings.TrimSpace(content) {
			content = input.Content
		} else if strings.TrimSpace(active.artifact.Content) == strings.TrimSpace(content) {
			content = active.artifact.Content
		}
		if len(content) > maxDocumentBytes {
			return core.ArtifactView{}, domainerr.New("error.invalidBody", map[string]any{"reason": "document is too large"})
		}
	} else {
		var saved, edited any
		if json.Unmarshal([]byte(active.artifact.Content), &saved) == nil && json.Unmarshal([]byte(content), &edited) == nil && equalJSON(saved, edited) {
			content = active.artifact.Content
		}
	}
	input.Content, input.BaseVersion, input.Shared, input.SharedState = content, &active.artifact.Version, true, state
	result, err := active.owner.repo.ReviseArtifact(ctx, active.artifact.ID, input)
	if err != nil {
		return core.ArtifactView{}, err
	}
	active.artifact = result
	active.dirty = time.Time{}
	active.checkpoint = false
	if active.timer != nil {
		active.timer.Stop()
	}
	return result, nil
}

func (rooms *Rooms) Flush(ctx context.Context, id string) error {
	rooms.mu.Lock()
	active := rooms.active[id]
	if active == nil {
		rooms.mu.Unlock()
		return nil
	}
	active.mu.Lock()
	rooms.mu.Unlock()
	defer active.mu.Unlock()
	if active.doc == nil {
		return errors.New("collaboration state is unavailable")
	}
	if active.dirty.IsZero() {
		return nil
	}
	// Feedback from an unlocked block must not cancel a document preview.
	// Finalizing or rolling back the draft makes the pending user edits saveable.
	if active.draft != nil && active.artifact.Kind == "document" {
		return nil
	}
	active.cancelDraft()
	_, err := active.save(ctx, core.ArtifactRevision{Author: "user"})
	return err
}

// Revise saves pending user work before applying a restore, a rename, or a
// layout/data update from the server. All readers of the room see one operation.
func (rooms *Rooms) Revise(ctx context.Context, id string, content *string, input core.ArtifactRevision) (core.ArtifactView, error) {
	var change func(*room) error
	if content != nil {
		input.Content = *content
		change = func(active *room) error {
			if err := core.ValidateArtifactContent(active.artifact.Kind, *content); err != nil {
				return err
			}
			return updateContent(active.doc, active.artifact.Kind, *content)
		}
	}
	return rooms.revise(ctx, id, input, change)
}

func (rooms *Rooms) Content(ctx context.Context, id string) (string, error) {
	active, err := rooms.lock(ctx, id)
	if err != nil {
		return "", err
	}
	defer rooms.release(active, nil)
	defer active.mu.Unlock()
	return readContent(active.doc, active.artifact.Kind)
}

// Edit computes an agent's change with the room locked. Pending browser work
// becomes its own version first, and a rejected change leaves that work intact.
func (rooms *Rooms) Edit(ctx context.Context, id string, input core.ArtifactRevision, from *int64, edit func(string) (string, error)) (core.ArtifactView, error) {
	return rooms.revise(ctx, id, input, func(active *room) error {
		current := active.artifact.Content
		base := current
		if from != nil && *from != active.artifact.Version {
			read, err := rooms.repo.GetArtifact(ctx, id, from)
			if err != nil {
				return err
			}
			base = read.Content
		}
		content, err := edit(base)
		if err != nil {
			return err
		}
		if err := core.ValidateArtifactContent(active.artifact.Kind, content); err != nil {
			return err
		}
		if active.artifact.Kind != "dashboard" {
			if _, err := rooms.repo.SendPendingArtifactFeedback(ctx, id, nil); err != nil {
				return err
			}
		}
		switch active.artifact.Kind {
		case "document":
			if base != current {
				base, err = normalizeDocument(base)
				if err != nil {
					return err
				}
				current, err = normalizeDocument(current)
				if err != nil {
					return err
				}
				content, err = normalizeDocument(content)
				if err != nil {
					return err
				}
				var merged bool
				content, merged = core.MergeText(base, current, content)
				if !merged {
					return domainerr.New("error.artifactConflict", map[string]any{"version": active.artifact.Version})
				}
			}
			return UpdateDocument(active.doc, content, input.Author)
		case "whiteboard":
			return UpdateWhiteboard(active.doc, content, &base, input.Author)
		default:
			return UpdateDashboard(active.doc, content, &base, input.Author)
		}
	})
}

// Write is the dashboard data refresh entry point. Data returned by the
// provider can change without writing any of the user's widget placements.
func (rooms *Rooms) Write(ctx context.Context, id, author, content, reason string) (core.ArtifactView, error) {
	_, snapshot, err := parseDashboard(content)
	if err != nil {
		return core.ArtifactView{}, err
	}
	return rooms.revise(ctx, id, core.ArtifactRevision{Author: author, Reason: reason}, func(active *room) error {
		data := active.doc.GetMap("snapshot")
		if !equalJSON(data.Get("source"), snapshot["source"]) {
			return domainerr.New("error.artifactConflict", map[string]any{"version": active.artifact.Version})
		}
		active.doc.Transact(func(*crdt.Transaction) { writeSnapshot(data, snapshot) }, "dashboard-refresh")
		return nil
	})
}

func (rooms *Rooms) revise(ctx context.Context, id string, input core.ArtifactRevision, change func(*room) error) (core.ArtifactView, error) {
	active, err := rooms.lock(ctx, id)
	if err != nil {
		return core.ArtifactView{}, err
	}
	defer rooms.release(active, nil)
	defer active.mu.Unlock()
	if input.BaseVersion != nil && *input.BaseVersion != active.artifact.Version {
		return core.ArtifactView{}, domainerr.New("error.artifactConflict", map[string]any{"version": active.artifact.Version})
	}
	hadDraft := active.rollbackDraft()
	defer func() {
		if hadDraft {
			active.clearWriter()
		}
	}()
	if !active.dirty.IsZero() {
		if _, err := active.save(ctx, core.ArtifactRevision{Author: "user"}); err != nil {
			return core.ArtifactView{}, err
		}
	}
	before, err := active.compactState()
	if err != nil {
		return core.ArtifactView{}, err
	}
	var update []byte
	var writerState map[string]any
	if change != nil {
		update, err = captureUpdates(active.doc, func() error {
			var changeErr error
			writerState = observeWriter(active, func() { changeErr = change(active) }, input.WriterName)
			return changeErr
		})
		if err != nil {
			if len(update) > 0 {
				active.restore()
			}
			return core.ArtifactView{}, err
		}
		if len(update) > 0 {
			state, err := roomState(active.doc)
			if err != nil {
				active.restore()
				return core.ArtifactView{}, err
			}
			active.setCompactedState(state)
		}
	}
	result, err := active.save(ctx, input)
	if err != nil {
		if len(update) > 0 {
			active.setCompactedState(before)
			active.restore()
		}
		return result, err
	}
	if len(update) > 0 {
		active.broadcast(protocol.EncodeUpdate(update), nil)
		active.publishWriter(input.WriterName, writerState)
		if writerState != nil {
			hadDraft = false
		}
	}
	return result, nil
}

func (rooms *Rooms) release(active *room, client *peer) {
	rooms.mu.Lock()
	defer rooms.mu.Unlock()
	active.mu.Lock()
	defer active.mu.Unlock()
	if rooms.active[active.artifact.ID] != active {
		return
	}
	if client != nil {
		delete(active.peers, client)
		active.broadcast(disconnectedPresence(client), nil)
	}
	if len(active.peers) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rooms.retire(ctx, active, true)
	}
}

func (rooms *Rooms) retire(ctx context.Context, active *room, save bool) {
	if active.doc != nil {
		active.cancelDraft()
	}
	if save && active.doc != nil && !active.dirty.IsZero() {
		if _, err := active.save(ctx, core.ArtifactRevision{Author: "user"}); err != nil {
			rooms.report(err)
			// A temporary invalid board can still be recovered by its editor.
			if state, stateErr := active.compactState(); stateErr != nil {
				rooms.report(stateErr)
			} else if saveErr := rooms.repo.CheckpointSharedState(ctx, active.artifact.ID,
				core.SharedState{Version: active.artifact.Version, State: state}); saveErr != nil {
				rooms.report(saveErr)
			}
		}
	} else if save && active.doc != nil && active.checkpoint {
		// A reconnecting peer can still have the draft's updates. Keep their
		// deletion history even when the document content never changed.
		if state, err := active.compactState(); err != nil {
			rooms.report(err)
		} else {
			rooms.report(rooms.repo.CheckpointSharedState(ctx, active.artifact.ID,
				core.SharedState{Version: active.artifact.Version, State: state}))
		}
	}
	if active.timer != nil {
		active.timer.Stop()
	}
	active.clearWriter()
	for client := range active.peers {
		client.cancel()
	}
	clear(active.peers)
	if active.doc != nil {
		active.doc.Destroy()
	}
	active.doc, active.state, active.pending, active.pendingLen = nil, nil, nil, 0
	delete(rooms.active, active.artifact.ID)
}

func (rooms *Rooms) forget(project string, save bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, active := range rooms.active {
		active.mu.Lock()
		if project == "" || active.artifact.ProjectID == project {
			rooms.retire(ctx, active, save)
		}
		active.mu.Unlock()
	}
}

func (rooms *Rooms) DropProject(project string) {
	rooms.mu.Lock()
	defer rooms.mu.Unlock()
	rooms.forget(project, false)
}

func (rooms *Rooms) Close() {
	rooms.mu.Lock()
	defer rooms.mu.Unlock()
	rooms.closed = true
	rooms.forget("", true)
}

func (rooms *Rooms) report(err error) {
	if err != nil && rooms.onError != nil {
		rooms.onError(err)
	}
}

type presence struct {
	clock uint64
	json  string
}

func presenceMessage(states map[uint64]presence) []byte {
	if len(states) == 0 {
		return nil
	}
	out := binary.AppendUvarint(nil, uint64(len(states)))
	for id, item := range states {
		out = binary.AppendUvarint(out, id)
		out = binary.AppendUvarint(out, item.clock)
		out = binary.AppendUvarint(out, uint64(len(item.json)))
		out = append(out, item.json...)
	}
	return protocol.EncodeAwarenessUpdateMessage(out)
}

func (active *room) presenceFrame() []byte {
	states := map[uint64]presence{}
	if active.writerID != 0 && active.writer.json != "null" {
		states[active.writerID] = active.writer
	}
	for client := range active.peers {
		for id, item := range client.awareness {
			if item.json != "null" {
				states[id] = item
			}
		}
	}
	return presenceMessage(states)
}

func disconnectedPresence(client *peer) []byte {
	for id, item := range client.awareness {
		item.json = "null"
		client.awareness[id] = item
	}
	return presenceMessage(client.awareness)
}

func (active *room) acceptPresence(client *peer, body []byte) error {
	count, err := popVaruint(&body)
	// A y-websocket client also forwards changes it received from the room,
	// including the initial snapshot of other participants.
	const maxForwarded = maxPeers*maxAwareness + 1
	if err != nil || count > maxForwarded || len(body) > maxForwarded*((8<<10)+24)+(8<<10) {
		return errors.New("invalid awareness frame")
	}
	next, err := parsePresence(count, body)
	if err != nil {
		return err
	}
	known := len(client.awareness)
	for id, item := range next {
		if id >= 1<<52 {
			delete(next, id)
			continue
		}
		foreign := false
		for other := range active.peers {
			if _, exists := other.awareness[id]; exists && other != client {
				foreign = true
				break
			}
		}
		if foreign {
			delete(next, id)
			continue
		}
		prior, present := client.awareness[id]
		if !present {
			if item.json == "null" {
				delete(next, id)
				continue
			}
			known++
		}
		if present && (item.clock < prior.clock || item.clock == prior.clock && (prior.json == "null" || item.json != "null")) {
			delete(next, id)
		}
	}
	if known > maxAwareness {
		return errors.New("too many awareness identities")
	}
	for id, item := range next {
		client.awareness[id] = item
	}
	active.broadcast(presenceMessage(next), nil)
	return nil
}

func parsePresence(count uint64, body []byte) (map[uint64]presence, error) {
	next := make(map[uint64]presence, int(count))
	for range count {
		id, err := popVaruint(&body)
		if err != nil {
			return nil, err
		}
		clock, err := popVaruint(&body)
		if err != nil {
			return nil, err
		}
		size, err := popVaruint(&body)
		maxSize := uint64(8 << 10)
		if id >= 1<<52 {
			maxSize = 16 << 10
		}
		if err != nil || size == 0 || size > uint64(len(body)) || size > maxSize {
			return nil, errors.New("invalid awareness size")
		}
		data := bytes.TrimSpace(body[:size])
		if len(data) == 0 || !(bytes.Equal(data, []byte("null")) || data[0] == '{' && json.Valid(data)) {
			return nil, errors.New("invalid awareness JSON")
		}
		next[id] = presence{clock: clock, json: string(data)}
		body = body[size:]
	}
	if len(body) != 0 {
		return nil, errors.New("unexpected awareness payload")
	}
	return next, nil
}

func popVaruint(body *[]byte) (uint64, error) {
	value, length := binary.Uvarint(*body)
	if length <= 0 || value > 1<<53-1 {
		return 0, errors.New("invalid awareness integer")
	}
	*body = (*body)[length:]
	return value, nil
}
