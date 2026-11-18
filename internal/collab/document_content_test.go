package collab

import (
	"encoding/base64"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antst/go-yjs/crdt"
)

const exampleDocument = "# 概览\n\n**加粗** *斜体* ~~删除~~ `code` [链接](https://example.test)\n\n" +
	"> [!TIP]\n> 先验证\n>\n> - 内层\n\n1. 一\n2. 二\n   - 三\n\n" +
	"- [ ] 待处理\n- [x] 已完成\n\n---\n\n```go\npackage main\n```\n\n" +
	"| 左 | 右 |\n| --- | --: |\n| 例 | **🧵** |\n\n" +
	"<details>\n<summary>更多</summary>\n\n细节<br>第二行\n\n</details>\n\n" +
	"<pending>等待结果</pending>\n\n```dou-whiteboard\n" +
	`{"height":240,"board":{"version":1,"nodes":[{"id":"n","type":"sticky","x":1,"y":2,"data":{"text":"视图"}}],"edges":[]}}` +
	"\n```\n"

func TestDocumentMarkdownRoundTripsWithExistingEditor(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("Bun is required for the editor comparison")
	}
	type fixture struct {
		State   string `json:"state"`
		Content string `json:"content"`
	}
	oracle := func(input map[string]any) fixture {
		t.Helper()
		input["kind"] = "document"
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		cmd := legacytest.Command(t, bun, "packages/agent/src/testing/go-collaboration-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(string(body))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("editor conversion: %v: %s", err, out)
		}
		var result fixture
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal(err, string(out))
		}
		return result
	}
	editor := oracle(map[string]any{"content": exampleDocument})
	state, err := base64.StdEncoding.DecodeString(editor.State)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := Load("document", state)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Destroy()
	read, err := ReadDocument(previous)
	if err != nil {
		t.Fatal(err)
	}
	want, err := normalizeDocument(editor.Content)
	if err != nil {
		t.Fatal(err)
	}
	if read != want {
		t.Fatalf("old editor state rendered differently\nGo:\n%s\nExpected:\n%s", read, want)
	}
	newest, err := Load("document", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer newest.Destroy()
	if err := UpdateDocument(newest, exampleDocument, "go"); err != nil {
		t.Fatal(err)
	}
	seeded, err := crdt.EncodeStateAsUpdate(newest, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := oracle(map[string]any{"state": base64.StdEncoding.EncodeToString(seeded)})
	got, err := normalizeDocument(result.Content)
	if err != nil || got != want {
		t.Fatalf("new Go state rendered differently in the editor\nGo → editor:\n%s\nExpected:\n%s\nError: %v", got, want, err)
	}
}

func TestDocumentUpdateRetainsBlockIDsAndUnchangedFormatting(t *testing.T) {
	doc, err := Load("document", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Destroy()
	before := "# 章节\n\n前文🧵 **要保留** 后文\n\n尾部"
	if err := UpdateDocument(doc, before, "user"); err != nil {
		t.Fatal(err)
	}
	root := doc.GetMap("document")
	keys := orderedKeys(root, "order")
	block := root.Get(keys[1]).(*crdt.YMap)
	text := block.Get("text").(*crdt.YText)
	text.Format(4, 4, crdt.MakeObject("comment:keep", true))
	after := "# 章节\n\n新的前文🧵 **要保留** 后文\n\n尾部\n"
	if err := UpdateDocument(doc, after, "agent"); err != nil {
		t.Fatal(err)
	}
	if next := orderedKeys(root, "order"); strings.Join(next, ",") != strings.Join(keys, ",") || block.Get("text") != text {
		t.Fatal("an edit replaced a block or its shared text", keys, next)
	}
	got, err := ReadDocument(doc)
	if err != nil || got != after {
		t.Fatalf("document text: %q, %v", got, err)
	}
	found := false
	for _, op := range text.ToDelta(nil, nil, nil) {
		comment, _ := op.Attributes.Get("comment:keep")
		found = found || comment == true
	}
	if !found {
		t.Fatal("the unchanged span lost its comment")
	}
}
