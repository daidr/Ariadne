package collab

import (
	"encoding/json"
	"reflect"
	"testing"
)

func compareContent(t *testing.T, got, want string) {
	t.Helper()
	var a, b any
	if json.Unmarshal([]byte(got), &a) != nil || json.Unmarshal([]byte(want), &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("content differs\nGo: %s\nJS: %s", got, want)
	}
}

func TestMalformedStateAndSyncFrame(t *testing.T) {
	t.Parallel()
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
