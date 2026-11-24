package collab

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
	"github.com/daidr/Ariadne/internal/core"
)

func largeMarkdownDocument(paragraphs int) string {
	var b strings.Builder
	b.WriteString("# 大文档协同性能基准\n\n")
	b.WriteString("锚点段落：用于草稿预览与增量编辑测试。\n\n")
	filler := strings.Repeat("这是一段用于模拟真实长文档内容的中文与 English 混合文本，包含常规标点与结构说明。", 20)
	for i := range paragraphs {
		fmt.Fprintf(&b, "第 %04d 段：%s\n\n", i, filler)
	}
	return b.String()
}

func largeDashboardContent(rows, widgets int) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"dashboard":{"version":1,"widgets":[`)
	for i := range widgets {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"w%03d","type":"table","title":"Widget %d","props":{"source":"metrics"},"place":{"lg":{"x":%d,"y":%d,"w":4,"h":3}}}`,
			i, i, (i%3)*4, (i/3)*3)
	}
	b.WriteString(`]},"data":{"metrics":[`)
	for i := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"row-%05d","service":"api-gateway-%02d","region":"ap-east-1","p50":%.2f,"p95":%.2f,"p99":%.2f,"status":"healthy","note":"payload-padding-for-realistic-dashboard-snapshot"}`,
			i, i%16, float64(i%100)+0.25, float64(i%200)+12.5, float64(i%400)+45.75)
	}
	b.WriteString(`]},"source":{"kind":"bench","settings":{"query":"metrics"}}}`)
	return b.String()
}

func largeWhiteboardContent(nodes int) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"nodes":[`)
	for i := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"n%04d","type":"sticky","x":%d,"y":%d,"width":220,"height":140,"data":{"title":"Node %d","text":"这是一张用于基准测试的白板便签卡片，包含标题和说明文字。"}}`,
			i, (i%20)*260, (i/20)*180, i)
	}
	b.WriteString(`],"edges":[`)
	for i := 1; i < nodes; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"e%04d","source":{"node":"n%04d","port":"right"},"target":{"node":"n%04d","port":"left"}}`, i, i-1, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func openBenchRoom(b *testing.B, kind, content string) (*Rooms, *room, *peer, *peer) {
	b.Helper()
	ctx := b.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(b.TempDir(), "bench.sqlite")})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Bench"), core.ProjectStart{})
	if err != nil {
		b.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{
		Kind:    kind,
		Title:   "Bench",
		Author:  "user",
		Content: content,
	})
	if err != nil {
		b.Fatal(err)
	}
	rooms := NewRooms(repo, func(err error) { b.Error(err) })
	b.Cleanup(rooms.Close)
	active, err := rooms.lock(ctx, artifact.ID)
	if err != nil {
		b.Fatal(err)
	}
	sender := &peer{sends: make(chan []byte, 64), cancel: func() {}, awareness: map[uint64]presence{}}
	receiver := &peer{sends: make(chan []byte, 64), cancel: func() {}, awareness: map[uint64]presence{}}
	active.peers[sender] = true
	active.peers[receiver] = true
	active.mu.Unlock()
	return rooms, active, sender, receiver
}

func drainPeer(p *peer) {
	for {
		select {
		case frame := <-p.sends:
			p.queued.Add(-int64(len(frame)))
		default:
			return
		}
	}
}

func buildDocumentKeystrokeFrames(b *testing.B, active *room, blockIndex, count int) [][]byte {
	b.Helper()
	state, err := crdt.EncodeStateAsUpdate(active.doc, nil)
	if err != nil {
		b.Fatal(err)
	}
	clientDoc, err := Load("document", state)
	if err != nil {
		b.Fatal(err)
	}
	defer clientDoc.Destroy()
	blocks, err := readDocumentBlocks(clientDoc.GetMap("document"))
	if err != nil || len(blocks) <= blockIndex {
		b.Fatal(len(blocks), err)
	}
	block := clientDoc.GetMap("document").Get(blocks[blockIndex].id).(*crdt.YMap)
	text := block.Get("text").(*crdt.YText)
	frames := make([][]byte, 0, count)
	var latest []byte
	h := clientDoc.OnUpdate(func(u []byte, _ any) {
		latest = append([]byte(nil), u...)
	})
	defer clientDoc.OffUpdate(h)
	for i := range count {
		latest = nil
		clientDoc.Transact(func(*crdt.Transaction) {
			if i%2 == 0 {
				text.Insert(text.GetLength(), "a", crdt.Object{})
			} else {
				text.Delete(text.GetLength()-1, 1)
			}
		}, "client")
		frames = append(frames, protocol.EncodeUpdate(latest))
	}
	return frames
}

func buildDashboardDragFrames(b *testing.B, active *room, count int) [][]byte {
	b.Helper()
	state, err := crdt.EncodeStateAsUpdate(active.doc, nil)
	if err != nil {
		b.Fatal(err)
	}
	clientDoc, err := Load("dashboard", state)
	if err != nil {
		b.Fatal(err)
	}
	defer clientDoc.Destroy()
	widget := clientDoc.GetMap("widgets").Get("w000").(*crdt.YMap)
	place := widget.Get("place").(*crdt.YMap)
	frames := make([][]byte, 0, count)
	var latest []byte
	h := clientDoc.OnUpdate(func(u []byte, _ any) {
		latest = append([]byte(nil), u...)
	})
	defer clientDoc.OffUpdate(h)
	for i := range count {
		latest = nil
		clientDoc.Transact(func(*crdt.Transaction) {
			place.Set("lg", sharedJSON(contentObject{"x": i % 12, "y": (i / 12) % 20, "w": 4, "h": 3}))
		}, "client")
		frames = append(frames, protocol.EncodeUpdate(latest))
	}
	return frames
}

func buildWhiteboardDragFrames(b *testing.B, active *room, count int) [][]byte {
	b.Helper()
	state, err := crdt.EncodeStateAsUpdate(active.doc, nil)
	if err != nil {
		b.Fatal(err)
	}
	clientDoc, err := Load("whiteboard", state)
	if err != nil {
		b.Fatal(err)
	}
	defer clientDoc.Destroy()
	node := clientDoc.GetMap("nodes").Get("n0000").(*crdt.YMap)
	frames := make([][]byte, 0, count)
	var latest []byte
	h := clientDoc.OnUpdate(func(u []byte, _ any) {
		latest = append([]byte(nil), u...)
	})
	defer clientDoc.OffUpdate(h)
	for i := range count {
		latest = nil
		clientDoc.Transact(func(*crdt.Transaction) {
			node.Set("x", i%1000)
			node.Set("y", (i*3)%1000)
		}, "client")
		frames = append(frames, protocol.EncodeUpdate(latest))
	}
	return frames
}

func BenchmarkRoomReceiveLargeDocument(b *testing.B) {
	_, active, sender, receiver := openBenchRoom(b, "document", largeMarkdownDocument(400))
	frames := buildDocumentKeystrokeFrames(b, active, 2, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := active.receive(sender, frames[i]); err != nil {
			b.Fatal(err)
		}
		drainPeer(receiver)
	}
	b.StopTimer()
}

func BenchmarkRoomReceiveLargeDocumentDuringDraft(b *testing.B) {
	rooms, active, sender, receiver := openBenchRoom(b, "document", largeMarkdownDocument(400))
	draft, err := rooms.Draft(b.Context(), active.artifact.ID, "document", "BenchWriter")
	if err != nil || draft == nil {
		b.Fatal(draft, err)
	}
	defer draft.Close()
	if handled, err := draft.WriteParagraph("锚点段落：用于草稿预览与增量编辑测试。", nil); err != nil || !handled {
		b.Fatal(handled, err)
	}
	drainPeer(sender)
	drainPeer(receiver)
	// Edit an unlocked block (index 2) while block 1 is locked by the draft.
	frames := buildDocumentKeystrokeFrames(b, active, 2, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := active.receive(sender, frames[i]); err != nil {
			b.Fatal(err)
		}
		drainPeer(receiver)
	}
	b.StopTimer()
}

func BenchmarkRoomReceiveLargeDashboard(b *testing.B) {
	_, active, sender, receiver := openBenchRoom(b, "dashboard", largeDashboardContent(2500, 24))
	frames := buildDashboardDragFrames(b, active, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := active.receive(sender, frames[i]); err != nil {
			b.Fatal(err)
		}
		drainPeer(receiver)
	}
	b.StopTimer()
}

func BenchmarkRoomReceiveLargeDashboardDuringDraft(b *testing.B) {
	rooms, active, sender, receiver := openBenchRoom(b, "dashboard", largeDashboardContent(2500, 24))
	draft, err := rooms.Draft(b.Context(), active.artifact.ID, "dashboard", "BenchWriter")
	if err != nil || draft == nil {
		b.Fatal(draft, err)
	}
	defer draft.Close()
	frames := buildDashboardDragFrames(b, active, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := active.receive(sender, frames[i]); err != nil {
			b.Fatal(err)
		}
		drainPeer(receiver)
	}
	b.StopTimer()
}

func BenchmarkRoomReceiveLargeWhiteboard(b *testing.B) {
	_, active, sender, receiver := openBenchRoom(b, "whiteboard", largeWhiteboardContent(500))
	frames := buildWhiteboardDragFrames(b, active, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := active.receive(sender, frames[i]); err != nil {
			b.Fatal(err)
		}
		drainPeer(receiver)
	}
	b.StopTimer()
}

func BenchmarkDraftWriteParagraphLargeDocument(b *testing.B) {
	rooms, active, sender, receiver := openBenchRoom(b, "document", largeMarkdownDocument(400))
	draft, err := rooms.Draft(b.Context(), active.artifact.ID, "document", "BenchWriter")
	if err != nil || draft == nil {
		b.Fatal(draft, err)
	}
	defer draft.Close()
	const anchor = "锚点段落：用于草稿预览与增量编辑测试。"
	if handled, err := draft.WriteParagraph(anchor, nil); err != nil || !handled {
		b.Fatal(handled, err)
	}
	drainPeer(sender)
	drainPeer(receiver)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		draft.wroteAt = time.Time{}
		draft.pieces = 0
		text := fmt.Sprintf("流式更新片段 #%d，用于验证单段草稿增量编码。", i)
		if handled, err := draft.WriteParagraph(anchor, &text); err != nil || !handled {
			b.Fatal(handled, err)
		}
		drainPeer(sender)
		drainPeer(receiver)
	}
	b.StopTimer()
}

func BenchmarkDraftWriteLargeDocument(b *testing.B) {
	initial := largeMarkdownDocument(60)
	rooms, active, sender, receiver := openBenchRoom(b, "document", initial)
	draft, err := rooms.Draft(b.Context(), active.artifact.ID, "document", "BenchWriter")
	if err != nil || draft == nil {
		b.Fatal(draft, err)
	}
	defer draft.Close()
	const anchor = "锚点段落：用于草稿预览与增量编辑测试。"
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		draft.wroteAt = time.Time{}
		draft.pieces = 0
		next := strings.Replace(initial, anchor, fmt.Sprintf("草稿全文预览更新 #%d。", i), 1)
		if err := draft.Write(next); err != nil {
			b.Fatal(err)
		}
		drainPeer(sender)
		drainPeer(receiver)
	}
	b.StopTimer()
}
