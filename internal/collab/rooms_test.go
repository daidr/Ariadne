package collab

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
	"github.com/daidr/Ariadne/internal/core"
)

func openTestRoom(t *testing.T, kind, content string) (*core.Repository, *Rooms, *room, *peer, *peer) {
	t.Helper()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "collab.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("CollabTest"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{
		Kind:    kind,
		Title:   "Test",
		Author:  "user",
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	rooms := NewRooms(repo, func(err error) { t.Error(err) })
	t.Cleanup(rooms.Close)
	active, err := rooms.lock(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	left := &peer{sends: make(chan []byte, 64), cancel: func() {}, awareness: map[uint64]presence{}}
	right := &peer{sends: make(chan []byte, 64), cancel: func() {}, awareness: map[uint64]presence{}}
	active.peers[left] = true
	active.peers[right] = true
	active.mu.Unlock()
	return repo, rooms, active, left, right
}

func makeUpdateFrame(t *testing.T, doc *crdt.Doc, mutate func()) []byte {
	t.Helper()
	delta, err := captureUpdates(doc, func() error {
		mutate()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.EncodeUpdate(delta)
}

func TestRoomIncrementalUpdatesCompactAndPersist(t *testing.T) {
	repo, rooms, active, left, right := openTestRoom(t, "document", "# 标题\n\n第一段内容。\n")
	clientDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer clientDoc.Destroy()
	observerDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer observerDoc.Destroy()

	blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
	if err != nil || len(blocks) < 2 {
		t.Fatal(blocks, err)
	}
	text := clientDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)

	// Insert edit -> incremental delta appended, broadcast to observer.
	insertFrame := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			text.Insert(text.GetLength(), "新增后缀", crdt.Object{})
		}, "client")
	})
	if err := active.receive(left, insertFrame); err != nil {
		t.Fatal(err)
	}
	if len(active.pending) != 1 {
		t.Fatalf("expected 1 uncompacted delta, got %d", len(active.pending))
	}
	if _, err := HandleSync(observerDoc, <-right.sends, "sync"); err != nil {
		t.Fatal(err)
	}

	// Duplicate frame -> no broadcast, no extra delta.
	if err := active.receive(left, insertFrame); err != nil {
		t.Fatal(err)
	}
	select {
	case unexpected := <-right.sends:
		t.Fatalf("duplicate update triggered unexpected broadcast (%d bytes)", len(unexpected))
	default:
	}
	if len(active.pending) != 1 {
		t.Fatalf("duplicate update changed pending count: %d", len(active.pending))
	}

	// Delete-only edit -> must still be captured, queued, and broadcast.
	deleteFrame := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			text.Delete(text.GetLength()-2, 2)
		}, "client")
	})
	if err := active.receive(left, deleteFrame); err != nil {
		t.Fatal(err)
	}
	if len(active.pending) != 2 {
		t.Fatalf("expected 2 uncompacted deltas after delete-only frame, got %d", len(active.pending))
	}
	if _, err := HandleSync(observerDoc, <-right.sends, "sync"); err != nil {
		t.Fatal(err)
	}

	gotObserver, err := ReadDocument(observerDoc)
	if err != nil {
		t.Fatal(err)
	}
	gotRoom, err := ReadDocument(active.doc)
	if err != nil {
		t.Fatal(err)
	}
	if gotObserver != gotRoom || !strings.Contains(gotRoom, "第一段内容。新增") || strings.Contains(gotRoom, "后缀") {
		t.Fatalf("observer/room mismatch: observer=%q room=%q", gotObserver, gotRoom)
	}

	// Flush compacts pending deltas and persists full SharedState.
	if err := rooms.Flush(t.Context(), active.artifact.ID); err != nil {
		t.Fatal(err)
	}
	if len(active.pending) != 0 || active.pendingLen != 0 {
		t.Fatalf("expected pending deltas compacted on flush, got count=%d len=%d", len(active.pending), active.pendingLen)
	}
	stored, err := repo.GetArtifact(t.Context(), active.artifact.ID, nil)
	if err != nil || !strings.Contains(stored.Content, "第一段内容。新增") {
		t.Fatalf("persisted content mismatch: %+v, %v", stored, err)
	}
}

func TestRoomRestorePreservesUncompactedDeltas(t *testing.T) {
	_, _, active, left, right := openTestRoom(t, "document", "# 标题\n\n初始正文。\n")
	clientDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer clientDoc.Destroy()

	blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
	if err != nil || len(blocks) < 2 {
		t.Fatal(blocks, err)
	}
	text := clientDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)

	for _, suffix := range []string{"第一笔", "第二笔"} {
		frame := makeUpdateFrame(t, clientDoc, func() {
			clientDoc.Transact(func(*crdt.Transaction) {
				text.Insert(text.GetLength(), suffix, crdt.Object{})
			}, "client")
		})
		if err := active.receive(left, frame); err != nil {
			t.Fatal(err)
		}
		<-right.sends
	}
	if len(active.pending) != 2 {
		t.Fatalf("expected 2 pending deltas before bad frame, got %d", len(active.pending))
	}

	// Malformed update frame triggers restore(); prior uncompacted deltas must survive.
	badFrame := protocol.EncodeUpdate([]byte{255, 255, 255})
	if err := active.receive(left, badFrame); err == nil {
		t.Fatal("expected malformed sync frame to fail")
	}
	got, err := ReadDocument(active.doc)
	if err != nil || !strings.Contains(got, "初始正文。第一笔第二笔") {
		t.Fatalf("restore lost uncompacted edits: %q, %v", got, err)
	}

	// Subsequent valid edit still works cleanly.
	frame3 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			text.Insert(text.GetLength(), "第三笔", crdt.Object{})
		}, "client")
	})
	if err := active.receive(left, frame3); err != nil {
		t.Fatal(err)
	}
	got, err = ReadDocument(active.doc)
	if err != nil || !strings.Contains(got, "初始正文。第一笔第二笔第三笔") {
		t.Fatalf("post-restore edit failed: %q, %v", got, err)
	}
}

func TestDashboardRejectsSnapshotEditsAndPreservesWidgets(t *testing.T) {
	initial := `{"version":1,"dashboard":{"version":1,"widgets":[{"id":"w1","type":"stat","props":{"source":"m"},"place":{"lg":{"x":0,"y":0,"w":4,"h":3}}}]},"data":{"m":[{"v":1}]},"source":{"kind":"test","settings":{}}}`
	_, rooms, active, left, right := openTestRoom(t, "dashboard", initial)

	clientDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer clientDoc.Destroy()

	// 1. Valid widget move without draft.
	widgetMove := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			place := clientDoc.GetMap("widgets").Get("w1").(*crdt.YMap).Get("place").(*crdt.YMap)
			place.Set("lg", sharedJSON(contentObject{"x": 6, "y": 2, "w": 4, "h": 3}))
		}, "client")
	})
	if err := active.receive(left, widgetMove); err != nil {
		t.Fatalf("valid widget move rejected: %v", err)
	}
	<-right.sends
	if len(active.pending) != 1 {
		t.Fatalf("expected incremental pending delta on dashboard, got %d", len(active.pending))
	}

	// 2. Duplicate SyncStep2 carrying server's existing snapshot must be accepted silently.
	fullState, err := crdt.EncodeStateAsUpdate(active.doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := active.receive(left, protocol.EncodeUpdate(fullState)); err != nil {
		t.Fatalf("duplicate state update with existing snapshot was rejected: %v", err)
	}

	// 3. Client mutating snapshot (set key) must be rejected and preserve prior widget move.
	badSetDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer badSetDoc.Destroy()
	badSetFrame := makeUpdateFrame(t, badSetDoc, func() {
		badSetDoc.Transact(func(*crdt.Transaction) {
			badSetDoc.GetMap("snapshot").Set("data", sharedJSON(contentObject{"m": []any{contentObject{"v": 999}}}))
		}, "evil")
	})
	if err := active.receive(left, badSetFrame); err == nil || !strings.Contains(err.Error(), "dashboard data comes from the server") {
		t.Fatalf("expected snapshot mutation rejection, got %v", err)
	}
	board, err := ReadBoard(active.doc, "dashboard")
	if err != nil || !strings.Contains(board, `"x":6`) || strings.Contains(board, `999`) {
		t.Fatalf("snapshot rejection corrupted dashboard state: %s, %v", board, err)
	}

	// 4. Client deleting key in snapshot must also be rejected.
	badDelDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer badDelDoc.Destroy()
	badDelFrame := makeUpdateFrame(t, badDelDoc, func() {
		badDelDoc.Transact(func(*crdt.Transaction) {
			badDelDoc.GetMap("snapshot").Delete("source")
		}, "evil")
	})
	if err := active.receive(left, badDelFrame); err == nil || !strings.Contains(err.Error(), "dashboard data comes from the server") {
		t.Fatalf("expected snapshot delete rejection, got %v", err)
	}

	// 5. During an active draft: shadow doc is reused across valid widget drags,
	// and a forbidden snapshot edit is rejected WITHOUT cancelling the draft.
	draft, err := rooms.Draft(t.Context(), active.artifact.ID, "dashboard", "Writer")
	if err != nil || draft == nil {
		t.Fatal(draft, err)
	}
	defer draft.Close()

	move2 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			place := clientDoc.GetMap("widgets").Get("w1").(*crdt.YMap).Get("place").(*crdt.YMap)
			place.Set("lg", sharedJSON(contentObject{"x": 8, "y": 3, "w": 4, "h": 3}))
		}, "client")
	})
	if err := active.receive(left, move2); err != nil {
		t.Fatal(err)
	}
	<-right.sends
	shadowFirst := draft.shadow
	if shadowFirst == nil {
		t.Fatal("expected draft shadow doc to be initialized lazily")
	}

	move3 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			place := clientDoc.GetMap("widgets").Get("w1").(*crdt.YMap).Get("place").(*crdt.YMap)
			place.Set("lg", sharedJSON(contentObject{"x": 9, "y": 4, "w": 4, "h": 3}))
		}, "client")
	})
	if err := active.receive(left, move3); err != nil {
		t.Fatal(err)
	}
	<-right.sends
	if draft.shadow != shadowFirst {
		t.Fatal("draft shadow doc was rebuilt instead of reused on consecutive valid frames")
	}

	if err := active.receive(left, badSetFrame); err == nil || !strings.Contains(err.Error(), "dashboard data comes from the server") {
		t.Fatalf("expected snapshot mutation rejection during draft, got %v", err)
	}
	if active.draft != draft {
		t.Fatal("rejected snapshot edit cancelled active draft")
	}
	if draft.shadow != nil {
		t.Fatal("expected dirty shadow doc to be dropped after rejection")
	}

	move4 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			place := clientDoc.GetMap("widgets").Get("w1").(*crdt.YMap).Get("place").(*crdt.YMap)
			place.Set("lg", sharedJSON(contentObject{"x": 10, "y": 5, "w": 4, "h": 3}))
		}, "client")
	})
	if err := active.receive(left, move4); err != nil {
		t.Fatal(err)
	}
	<-right.sends
	board, err = ReadBoard(active.doc, "dashboard")
	if err != nil || !strings.Contains(board, `"x":10`) {
		t.Fatalf("widget edit after rejected snapshot edit failed: %s, %v", board, err)
	}
}

func TestDocumentDraftShadowReuseLockEnforcementAndFallback(t *testing.T) {
	initial := "# 标题\n\n锁定段落。\n\n自由段落。\n"
	_, rooms, active, left, right := openTestRoom(t, "document", initial)

	draft, err := rooms.Draft(t.Context(), active.artifact.ID, "document", "AI")
	if err != nil || draft == nil {
		t.Fatal(draft, err)
	}
	if handled, err := draft.WriteParagraph("锁定段落。", nil); err != nil || !handled {
		t.Fatal(handled, err)
	}
	<-left.sends
	<-right.sends

	clientDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer clientDoc.Destroy()
	blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
	if err != nil || len(blocks) != 3 {
		t.Fatal(blocks, err)
	}
	lockedText := clientDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)
	freeText := clientDoc.GetMap("document").Get(blocks[2].id).(*crdt.YMap).Get("text").(*crdt.YText)

	// Two consecutive edits to the unlocked block reuse the same shadow doc.
	free1 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			freeText.Insert(freeText.GetLength(), "A", crdt.Object{})
		}, "client")
	})
	if err := active.receive(left, free1); err != nil {
		t.Fatal(err)
	}
	<-right.sends
	shadowFirst := draft.shadow
	if shadowFirst == nil {
		t.Fatal("expected draft shadow doc to be initialized")
	}

	free2 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			freeText.Insert(freeText.GetLength(), "B", crdt.Object{})
		}, "client")
	})
	if err := active.receive(left, free2); err != nil {
		t.Fatal(err)
	}
	<-right.sends
	if draft.shadow != shadowFirst {
		t.Fatal("expected draft shadow doc to be reused for consecutive unlocked edits")
	}

	// WriteParagraph updates active.doc and keeps draft.shadow synchronized.
	draft.wroteAt = time.Time{}
	chunk := "流式草稿第一句。"
	if handled, err := draft.WriteParagraph("锁定段落。", &chunk); err != nil || !handled {
		t.Fatal(handled, err)
	}
	<-left.sends
	<-left.sends
	<-right.sends
	<-right.sends

	// Editing the locked block must fail with errDocumentBlockLocked without ending the draft.
	staleDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer staleDoc.Destroy()
	staleLocked := staleDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)
	badLockFrame := makeUpdateFrame(t, staleDoc, func() {
		staleDoc.Transact(func(*crdt.Transaction) {
			staleLocked.Insert(staleLocked.GetLength(), "非法修改", crdt.Object{})
		}, "stale")
	})
	if err := active.receive(left, badLockFrame); !errors.Is(err, errDocumentBlockLocked) {
		t.Fatalf("expected errDocumentBlockLocked, got %v", err)
	}
	if active.draft != draft {
		t.Fatal("locked block violation cancelled draft")
	}
	_ = lockedText

	// Fallback from WriteParagraph to general Draft.Write must see the synced paragraph last text.
	draft.wroteAt = time.Time{}
	fallbackNext := "# 标题\n\n多段替换一。\n\n多段替换二。\n\n自由段落。\n"
	if err := draft.Write(fallbackNext); err != nil {
		t.Fatalf("fallback Draft.Write after WriteParagraph failed: %v", err)
	}
	got, err := ReadDocument(active.doc)
	if err != nil || !strings.Contains(got, "多段替换二。") || !strings.Contains(got, "自由段落。AB") {
		t.Fatalf("fallback Draft.Write lost preview or concurrent user edit: %q, %v", got, err)
	}

	// Closing the draft rolls back AI preview while preserving user's unlocked edits.
	draft.Close()
	got, err = ReadDocument(active.doc)
	if err != nil || !strings.Contains(got, "锁定段落。") || !strings.Contains(got, "自由段落。AB") || strings.Contains(got, "多段替换") {
		t.Fatalf("draft rollback mismatch: %q, %v", got, err)
	}
}

func TestRoomOutOfOrderUpdatesRetryAndBroadcast(t *testing.T) {
	_, _, active, left, right := openTestRoom(t, "document", "# 标题\n\n正文：\n")
	clientDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer clientDoc.Destroy()
	observerDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer observerDoc.Destroy()

	blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
	if err != nil || len(blocks) < 2 {
		t.Fatal(blocks, err)
	}
	text := clientDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)

	frame1 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			text.Insert(text.GetLength(), "先到", crdt.Object{})
		}, "client")
	})
	frame2 := makeUpdateFrame(t, clientDoc, func() {
		clientDoc.Transact(func(*crdt.Transaction) {
			text.Insert(text.GetLength(), "后到", crdt.Object{})
		}, "client")
	})

	// Deliver frame2 before frame1 (out-of-order).
	if err := active.receive(left, frame2); err != nil {
		t.Fatal(err)
	}
	// Now deliver frame1 -> both frame1 and pending frame2 integrate and broadcast.
	if err := active.receive(left, frame1); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case msg := <-right.sends:
			if _, err := HandleSync(observerDoc, msg, "sync"); err != nil {
				t.Fatal(err)
			}
		default:
			goto done
		}
	}
done:
	gotRoom, err := ReadDocument(active.doc)
	if err != nil {
		t.Fatal(err)
	}
	gotObserver, err := ReadDocument(observerDoc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoom != gotObserver || !strings.Contains(gotRoom, "正文：先到后到") {
		t.Fatalf("out-of-order convergence failed: room=%q observer=%q", gotRoom, gotObserver)
	}
}

func TestRoomPendingCompactsAtThreshold(t *testing.T) {
	_, _, active, left, right := openTestRoom(t, "document", "# 标题\n\n开头。\n")
	clientDoc, err := active.cloneDoc()
	if err != nil {
		t.Fatal(err)
	}
	defer clientDoc.Destroy()
	blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
	if err != nil || len(blocks) < 2 {
		t.Fatal(blocks, err)
	}
	text := clientDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)

	chunk := strings.Repeat("x", 200<<10) // 200 KiB per frame; 3 frames > 512 KiB maxPendingBytes
	for i := range 3 {
		frame := makeUpdateFrame(t, clientDoc, func() {
			clientDoc.Transact(func(*crdt.Transaction) {
				text.Insert(text.GetLength(), chunk, crdt.Object{})
			}, "client")
		})
		if err := active.receive(left, frame); err != nil {
			t.Fatalf("frame %d failed: %v", i, err)
		}
		<-right.sends
	}
	if len(active.pending) != 0 || active.pendingLen != 0 {
		t.Fatalf("expected auto-compaction past maxPendingBytes, got pending=%d len=%d", len(active.pending), active.pendingLen)
	}
	if len(active.state) < 512<<10 {
		t.Fatalf("expected compacted state to include appended chunks, got %d bytes", len(active.state))
	}
}

func TestRoomRejectsOversizedStateUpdate(t *testing.T) {
	_, rooms, active, left, _ := openTestRoom(t, "document", "# 标题\n\n短文本。\n")

	makeOversizedFrame := func() []byte {
		clientDoc, err := active.cloneDoc()
		if err != nil {
			t.Fatal(err)
		}
		defer clientDoc.Destroy()
		blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
		if err != nil || len(blocks) < 2 {
			t.Fatal(blocks, err)
		}
		text := clientDoc.GetMap("document").Get(blocks[1].id).(*crdt.YMap).Get("text").(*crdt.YText)
		return makeUpdateFrame(t, clientDoc, func() {
			clientDoc.Transact(func(*crdt.Transaction) {
				text.Insert(text.GetLength(), strings.Repeat("A", maxRoomState+1024), crdt.Object{})
			}, "client")
		})
	}

	// 1. Rejected when no draft is active, restoring clean document state.
	oversized := makeOversizedFrame()
	if err := active.receive(left, oversized); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized rejection, got %v", err)
	}
	got, err := ReadDocument(active.doc)
	if err != nil || strings.Contains(got, "AAAA") {
		t.Fatalf("oversized update leaked into room doc: len=%d, %v", len(got), err)
	}

	// 2. Rejected during an active draft without cancelling the draft.
	draft, err := rooms.Draft(t.Context(), active.artifact.ID, "document", "AI")
	if err != nil || draft == nil {
		t.Fatal(draft, err)
	}
	defer draft.Close()
	if err := active.receive(left, oversized); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized rejection during draft, got %v", err)
	}
	if active.draft != draft {
		t.Fatal("oversized update cancelled active draft")
	}
}
