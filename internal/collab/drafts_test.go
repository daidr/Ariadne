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

func TestDocumentDraftReplacementTracksCursorAndRollsBack(t *testing.T) {
	t.Run("general", func(t *testing.T) { checkDocumentDraft(t, false) })
	t.Run("single paragraph", func(t *testing.T) { checkDocumentDraft(t, true) })
}

func checkDocumentDraft(t *testing.T, paragraph bool) {
	t.Helper()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Draft"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	const initial = "# 交付说明\n\n原来的交付说明。\n\n保留这条说明。\n"
	artifact, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{Kind: "document", Title: "Draft", Author: "user", Content: initial})
	if err != nil {
		t.Fatal(err)
	}
	rooms := NewRooms(repo, func(err error) { t.Error(err) })
	defer rooms.Close()
	active, err := rooms.lock(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	browser := &peer{sends: make(chan []byte, 16), cancel: func() {}, awareness: map[uint64]presence{}}
	active.peers[browser] = true
	active.mu.Unlock()
	draft, err := rooms.Draft(ctx, artifact.ID, "document", "Coordinator")
	if err != nil || draft == nil {
		t.Fatal(draft, err)
	}
	if paragraph {
		if handled, err := draft.WriteParagraph("原来的交付说明。", nil); err != nil || !handled {
			t.Fatal(handled, err)
		}
	} else {
		if err := draft.ReserveDocument(strings.Replace(initial, "原来的交付说明。", "<pending>…</pending>", 1)); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := readDocumentBlocks(active.doc.GetMap("document"))
	if err != nil || len(blocks) != 3 {
		t.Fatal(blocks, err)
	}
	if len(draft.locked) != 1 || !draft.locked[blocks[1].id] || !strings.Contains(active.writer.json, blocks[1].id) {
		t.Fatalf("wrong blocks locked before generation: %s", active.writer.json)
	}
	<-browser.sends // lock before any preview content
	if got, err := ReadDocument(active.doc); err != nil || !strings.Contains(got, "原来的交付说明。") {
		t.Fatalf("reservation changed the document: %q, %v", got, err)
	}
	browserEdit := func(old, replacement string) error {
		t.Helper()
		clientDoc, err := active.cloneDoc()
		if err != nil {
			t.Fatal(err)
		}
		defer clientDoc.Destroy()
		vector := crdt.EncodeStateVector(clientDoc)
		content, err := ReadDocument(clientDoc)
		if err != nil {
			t.Fatal(err)
		}
		if err := UpdateDocument(clientDoc, strings.Replace(content, old, replacement, 1), "client"); err != nil {
			t.Fatal(err)
		}
		update, err := crdt.EncodeStateAsUpdate(clientDoc, vector)
		if err != nil {
			t.Fatal(err)
		}
		return active.receive(browser, protocol.EncodeUpdate(update))
	}
	if err := browserEdit("原来的交付说明。", "过期的用户编辑。"); !errors.Is(err, errDocumentBlockLocked) {
		t.Fatalf("a late edit reached the locked block: %v", err)
	}
	if err := browserEdit("保留这条说明。", "用户修改了另一个块。"); err != nil {
		t.Fatalf("an unrelated edit was refused: %v", err)
	}
	if err := rooms.Flush(ctx, artifact.ID); err != nil || active.draft != draft {
		t.Fatalf("feedback on the unrelated edit ended the draft: %v", err)
	}
	afterUser := strings.Replace(initial, "保留这条说明。", "用户修改了另一个块。", 1)

	for _, fragment := range []string{"先核对预发布分支的构建结果", "先核对预发布分支的构建结果，再附上验收记录"} {
		draft.wroteAt = time.Time{}
		if paragraph {
			if handled, err := draft.WriteParagraph("原来的交付说明。", &fragment); err != nil || !handled {
				t.Fatal(handled, err)
			}
		} else if err := draft.Write(strings.Replace(initial, "原来的交付说明。", fragment, 1)); err != nil {
			t.Fatal(err)
		}
		content, err := ReadDocument(active.doc)
		if err != nil || !strings.Contains(content, fragment) {
			t.Fatalf("preview was not applied: %q, %v", content, err)
		}
		if !strings.Contains(active.writer.json, "Coordinator") {
			t.Fatalf("missing cursor for %q: %s", fragment, active.writer.json)
		}
		<-browser.sends // the content update
		// y-websocket forwards the awareness update it just applied.
		if err := active.receive(browser, <-browser.sends); err != nil {
			t.Fatalf("forwarded writer awareness disconnected the editor: %v", err)
		}
		if len(browser.awareness) != 0 {
			t.Fatal("server writer was assigned to a browser")
		}
	}
	previewState, err := crdt.EncodeStateAsUpdate(active.doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	preview := protocol.EncodeUpdate(previewState)
	draft.Close()
	content, err := ReadDocument(active.doc)
	if err != nil || strings.TrimSpace(content) != strings.TrimSpace(afterUser) {
		t.Fatalf("uncommitted replacement remained after close: %q, %v", content, err)
	}
	rooms.release(active, browser)
	reopened, err := rooms.lock(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	reopened.peers[browser] = true
	reopened.mu.Unlock()
	// The browser lost its connection before receiving the rollback and sends
	// its last state on reconnect. The old preview must still be deleted.
	if err := reopened.receive(browser, preview); err != nil {
		t.Fatal(err)
	}
	content, err = ReadDocument(reopened.doc)
	if err != nil || strings.TrimSpace(content) != strings.TrimSpace(afterUser) {
		t.Fatalf("reconnection restored an unfinished draft: %q, %v", content, err)
	}
	finalText := "核对构建结果，然后附上验收记录。"
	saved, err := rooms.Edit(ctx, artifact.ID, core.ArtifactRevision{Author: project.CoordinatorID, Reason: "交付说明"}, nil, func(base string) (string, error) {
		return strings.Replace(base, "原来的交付说明。", finalText, 1), nil
	})
	if err != nil || saved.Version != artifact.Version+2 || !strings.Contains(saved.Content, finalText) {
		t.Fatalf("preview affected the stored edit: %+v, %v", saved, err)
	}
}
