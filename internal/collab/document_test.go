package collab

import (
	"encoding/base64"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/antst/go-yjs/crdt"
)

func compareContent(t *testing.T, got, want string) {
	t.Helper()
	var a, b any
	if json.Unmarshal([]byte(got), &a) != nil || json.Unmarshal([]byte(want), &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("content differs\nGo: %s\nJS: %s", got, want)
	}
}

func TestRealArtifactStatesRoundTripBetweenYjsAndGo(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("Yjs compatibility oracle needs Bun for tests only")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		State    string          `json:"state"`
		Snapshot json.RawMessage `json:"snapshot"`
		Content  string          `json:"content"`
		Sync     string          `json:"sync"`
	}
	legacytest.Path(t, "packages/agent/src/testing/go-collaboration-fixture.ts")
	oracle := func(t *testing.T, kind, state, reply string) fixture {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"kind": kind, "state": state, "reply": reply})
		cmd := legacytest.Command(t, bun, "packages/agent/src/testing/go-collaboration-fixture.ts")
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(string(input))
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Yjs oracle: %v\n%s", err, data)
		}
		var result fixture
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("oracle JSON: %v\n%s", err, data)
		}
		return result
	}
	for _, kind := range []string{"document", "whiteboard", "dashboard"} {
		t.Run(kind, func(t *testing.T) {
			before := oracle(t, kind, "", "")
			state, err := base64.StdEncoding.DecodeString(before.State)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := Load(kind, state)
			if err != nil {
				t.Fatal(err)
			}
			defer doc.Destroy()
			snapshot, err := Snapshot(doc, kind)
			if err != nil {
				t.Fatal(err)
			}
			var native, legacy any
			if err := json.Unmarshal(snapshot, &native); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(before.Snapshot, &legacy); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(native, legacy) {
				t.Fatalf("decoded artifact differs\nGo: %s\nJS: %s", snapshot, before.Snapshot)
			}
			if kind != "document" {
				got, err := ReadBoard(doc, kind)
				if err != nil {
					t.Fatal(err)
				}
				compareContent(t, got, before.Content)
				rebuilt, err := Load(kind, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer rebuilt.Destroy()
				switch kind {
				case "whiteboard":
					err = UpdateWhiteboard(rebuilt, before.Content, nil, "go")
				case "dashboard":
					err = UpdateDashboard(rebuilt, before.Content, nil, "go")
				}
				if err != nil {
					t.Fatal(err)
				}
				data, err := crdt.EncodeStateAsUpdate(rebuilt, nil)
				if err != nil {
					t.Fatal(err)
				}
				fromGo := oracle(t, kind, base64.StdEncoding.EncodeToString(data), "")
				compareContent(t, fromGo.Content, before.Content)
			}
			encoded, err := crdt.EncodeStateAsUpdate(doc, nil)
			if err != nil {
				t.Fatal(err)
			}
			after := oracle(t, kind, base64.StdEncoding.EncodeToString(encoded), "")
			if after.Content != before.Content {
				t.Fatalf("re-encoded content changed\nbefore: %s\nafter: %s", before.Content, after.Content)
			}
			frame, err := base64.StdEncoding.DecodeString(before.Sync)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "document":
				changed := false
				for _, item := range doc.GetMap("document").Entries() {
					block, ok := item.(*crdt.YMap)
					if !ok || block.Get("type") != "paragraph" {
						continue
					}
					text, ok := block.Get("text").(*crdt.YText)
					if !ok {
						continue
					}
					text.Insert(0, "Go 修改：", crdt.Object{})
					changed = true
					break
				}
				if !changed {
					t.Fatal("document fixture had no editable paragraph")
				}
			case "whiteboard":
				doc.GetMap("nodes").Get("n1").(*crdt.YMap).Set("x", 120)
			case "dashboard":
				doc.GetMap("widgets").Get("total").(*crdt.YMap).Set("title", "Go 标题")
			}
			reply, err := HandleSync(doc, frame, "test")
			if err != nil {
				t.Fatalf("JS sync frame: %v", err)
			}
			edited := oracle(t, kind, before.State, base64.StdEncoding.EncodeToString(reply))
			nativeJSON, err := Snapshot(doc, kind)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(nativeJSON, &native); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(edited.Snapshot, &legacy); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(native, legacy) {
				t.Fatalf("native edit did not synchronize\nGo: %s\nJS: %s", nativeJSON, edited.Snapshot)
			}
		})
	}
}

func TestMalformedStateAndSyncFrame(t *testing.T) {
	if doc, err := Load("unknown", nil); err == nil {
		doc.Destroy()
		t.Fatal("accepted unknown artifact kind")
	}
	if doc, err := Load("document", []byte{255, 255}); err == nil {
		doc.Destroy()
		t.Fatal("accepted truncated Yjs state")
	}
	doc, err := Load("document", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Destroy()
	doc.GetMap("document").Set("test", "value")
	if _, err := HandleSync(doc, []byte{0, 1, 255}, nil); err == nil {
		t.Fatal("accepted malformed sync frame")
	}
	if doc.GetMap("document").Get("test") != "value" {
		t.Fatal("malformed frame lost existing state")
	}
}
