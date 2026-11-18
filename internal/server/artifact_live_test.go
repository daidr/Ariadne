package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
	"github.com/coder/websocket"
	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
)

func TestArtifactCollaborationAuthenticatesSynchronizesAndPersists(t *testing.T) {
	t.Run("whiteboard", func(t *testing.T) {
		testArtifactCollaboration(t, "whiteboard", `{"version":1,"nodes":[{"id":"note","type":"sticky","x":0,"y":5,"data":{"text":"initial"}}],"edges":[]}`, `"x":42`, func(d *crdt.Doc) error {
			node, ok := d.GetMap("nodes").Get("note").(*crdt.YMap)
			if !ok {
				return errors.New("seeded whiteboard item did not reach the browser")
			}
			node.Set("x", 42)
			return nil
		})
	})
	t.Run("document", func(t *testing.T) {
		testArtifactCollaboration(t, "document", "# 文档\n\n初稿。\n", "修改 👋", func(d *crdt.Doc) error {
			current, err := collab.ReadDocument(d)
			if err != nil {
				return err
			}
			return collab.UpdateDocument(d, strings.Replace(current, "初稿。", "浏览器修改 👋。", 1), "editor")
		})
	})
}

func testArtifactCollaboration(t *testing.T, artifactKind, initial, want string, edit func(*crdt.Doc) error) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "shared.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{Kind: artifactKind, Title: "Options", Author: "user", Content: initial})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "live-token", OnError: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/artifacts/" + artifact.ID + "/live"
	deny := func(url string, headers http.Header, status int) {
		t.Helper()
		socket, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if socket != nil {
			_ = socket.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != status {
			t.Fatalf("upgrade response = %v, error = %v, want %d", response, err, status)
		}
		if response.Body != nil {
			response.Body.Close()
		}
	}
	deny(address, nil, 401)
	deny(address+"?token=live-token", http.Header{"Origin": {"https://other.example"}}, 403)
	read := func(conn *websocket.Conn) []byte {
		t.Helper()
		kind, data, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatal(kind, err)
		}
		return data
	}
	write := func(conn *websocket.Conn, data []byte) {
		t.Helper()
		if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
			t.Fatal(err)
		}
	}
	readSync := func(conn *websocket.Conn) []byte {
		t.Helper()
		for {
			frame := read(conn)
			kind, err := protocol.InspectMessage(frame)
			if err != nil {
				t.Fatal(err)
			}
			if kind.Type == protocol.MessageSync {
				return frame
			}
		}
	}
	connect := func() (*websocket.Conn, *crdt.Doc) {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, address+"?token=live-token", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		d, err := collab.Load(artifactKind, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Destroy)
		if info, err := protocol.InspectMessage(read(conn)); err != nil || info.SyncType != protocol.SyncMessageStep1 {
			t.Fatal("missing server sync step 1", info, err)
		}
		write(conn, protocol.EncodeSyncStep1(d))
		if _, err := collab.HandleSync(d, readSync(conn), "test"); err != nil {
			t.Fatal(err)
		}
		return conn, d
	}
	left, leftDoc := connect()
	right, rightDoc := connect()
	if artifactKind == "document" {
		draft, err := api.rooms.Draft(ctx, artifact.ID, artifactKind, "Test writer")
		if err != nil || draft == nil {
			t.Fatal("could not open a live draft", err)
		}
		if err := draft.Write(initial + "\n模型正在写这段。\n"); err != nil {
			t.Fatal(err)
		}
		for _, reader := range []struct {
			conn *websocket.Conn
			doc  *crdt.Doc
		}{{left, leftDoc}, {right, rightDoc}} {
			lock, err := protocol.InspectMessage(read(reader.conn))
			if err != nil || lock.Type != protocol.MessageAwareness || !strings.Contains(string(lock.Body), `"documentLocks"`) {
				t.Fatal("the draft lock did not arrive before its content", lock, err)
			}
			if _, err := collab.HandleSync(reader.doc, readSync(reader.conn), "test"); err != nil {
				t.Fatal(err)
			}
			content, err := collab.ReadDocument(reader.doc)
			if err != nil || !strings.Contains(content, "模型正在写这段") {
				t.Fatal("the draft did not reach another browser", content, err)
			}
			awareness, err := protocol.InspectMessage(read(reader.conn))
			if err != nil || awareness.Type != protocol.MessageAwareness ||
				!strings.Contains(string(awareness.Body), `"name":"Test writer"`) ||
				!strings.Contains(string(awareness.Body), `"position":{"assoc":0,"type":{"client":`) {
				t.Fatal("the draft cursor did not reach the browser", awareness, err)
			}
		}
		stored, err := repo.GetArtifact(ctx, artifact.ID, nil)
		if err != nil || stored.Content != initial || stored.Version != artifact.Version {
			t.Fatal("unfinished draft was saved", stored, err)
		}
		// A peer with a stale editor may send a change before seeing the lock.
		stale, staleDoc := connect()
		before, err := collab.ReadDocument(staleDoc)
		if err != nil || !strings.Contains(before, "模型正在写这段。") {
			t.Fatal("stale peer did not receive the current document", before, err)
		}
		vector := crdt.EncodeStateVector(staleDoc)
		if err := collab.UpdateDocument(staleDoc, strings.Replace(before, "模型正在写这段。", "冲突修改。", 1), "stale"); err != nil {
			t.Fatal(err)
		}
		update, err := crdt.EncodeStateAsUpdate(staleDoc, vector)
		if err != nil {
			t.Fatal(err)
		}
		write(stale, protocol.EncodeUpdate(update))
		if _, _, err := stale.Read(ctx); websocket.CloseStatus(err) != 4409 {
			t.Fatal("the stale peer was not asked to reload the locked document", err)
		}
		defer draft.Close()
	}
	vector := crdt.EncodeStateVector(leftDoc)
	if err := edit(leftDoc); err != nil {
		t.Fatal(err)
	}
	update, err := crdt.EncodeStateAsUpdate(leftDoc, vector)
	if err != nil {
		t.Fatal(err)
	}
	write(left, protocol.EncodeUpdate(update))
	if _, err := collab.HandleSync(rightDoc, readSync(right), "test"); err != nil {
		t.Fatal(err)
	}
	readContent := func(doc *crdt.Doc) (string, error) {
		if artifactKind == "document" {
			return collab.ReadDocument(doc)
		}
		return collab.ReadBoard(doc, artifactKind)
	}
	content, err := readContent(rightDoc)
	if err != nil || !strings.Contains(content, want) {
		t.Fatal("another browser did not receive the edit", content, err)
	}
	if artifactKind == "document" {
		saved, err := api.rooms.Edit(ctx, artifact.ID, core.ArtifactRevision{Author: project.CoordinatorID, Reason: "Rename heading"}, &artifact.Version, func(base string) (string, error) {
			return strings.Replace(base, "# 文档", "# 新标题", 1), nil
		})
		if err != nil || !strings.Contains(saved.Content, "# 新标题") || !strings.Contains(saved.Content, want) || strings.Contains(saved.Content, "模型正在写这段") {
			t.Fatal("the agent's edit did not preserve the pending browser edit", saved, err)
		}
		for range 2 {
			if _, err := collab.HandleSync(rightDoc, readSync(right), "test"); err != nil {
				t.Fatal(err)
			}
		}
		content, err = collab.ReadDocument(rightDoc)
		if err != nil || !strings.Contains(content, "# 新标题") || !strings.Contains(content, want) || strings.Contains(content, "模型正在写这段") {
			t.Fatal("the agent's edit was not synchronized", content, err)
		}
		if _, err := api.rooms.Edit(ctx, artifact.ID, core.ArtifactRevision{Author: project.CoordinatorID}, &artifact.Version, func(base string) (string, error) {
			return strings.Replace(base, "初稿。", "覆盖这段。", 1), nil
		}); err == nil {
			t.Fatal("an overlapping stale revision was accepted")
		}
	}
	_ = left.Close(websocket.StatusNormalClosure, "")
	_ = right.Close(websocket.StatusNormalClosure, "")
	for {
		if err := ctx.Err(); err != nil {
			t.Fatal("updated board did not persist", err)
		}
		latest, err := repo.GetArtifact(ctx, artifact.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if latest.Version > artifact.Version {
			if !strings.Contains(latest.Content, want) {
				t.Fatal("saved version lost the edit", latest)
			}
			state, err := repo.ReadSharedState(ctx, artifact.ID)
			if err != nil || state == nil || state.Version != latest.Version {
				t.Fatal("native save did not persist matching Yjs state", state, err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	reopened, reloaded := connect()
	content, err = readContent(reloaded)
	if err != nil || !strings.Contains(content, want) {
		t.Fatal("reconnection lost the saved edit", content, err)
	}
	_ = reopened.Close(websocket.StatusNormalClosure, "")
}
