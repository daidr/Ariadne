package collab

import (
	"strings"
	"testing"

	"github.com/antst/go-yjs/crdt"
)

func TestDocumentUpdateRetainsBlockIDsAndUnchangedFormatting(t *testing.T) {
	t.Parallel()
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
