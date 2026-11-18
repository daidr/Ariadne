package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
)

func TestThreadDeltaChangesOrderRemovalInvalidCursorAndForget(t *testing.T) {
	syncer := NewThreadSync()
	defer syncer.Close()
	a, b := core.ThreadView{ID: "a", Title: "A"}, core.ThreadView{ID: "b", Title: "B"}
	first := syncer.Delta("p", []core.ThreadView{a, b}, "")
	if first.Partial || len(first.Threads) != 2 {
		t.Fatal(first)
	}
	same := syncer.Delta("p", []core.ThreadView{a, b}, first.Cursor)
	if !same.Partial || len(same.Threads) != 0 || same.IDs != nil {
		t.Fatal(same)
	}
	a.Title = "A2"
	changed := syncer.Delta("p", []core.ThreadView{a, b}, first.Cursor)
	if !changed.Partial || len(changed.Threads) != 1 || changed.Threads[0].ID != "a" || changed.IDs != nil {
		t.Fatal(changed)
	}
	moved := syncer.Delta("p", []core.ThreadView{b, a}, changed.Cursor)
	if !moved.Partial || len(moved.Threads) != 0 || moved.IDs == nil || fmt.Sprint(*moved.IDs) != "[b a]" {
		t.Fatal(moved)
	}
	removed := syncer.Delta("p", []core.ThreadView{}, moved.Cursor)
	if !removed.Partial || removed.IDs == nil || len(*removed.IDs) != 0 {
		t.Fatal(removed)
	}
	for _, cursor := range []string{"", "other.0", syncer.epoch + ".999999999999", syncer.epoch + ".bad"} {
		if result := syncer.Delta("p", []core.ThreadView{a}, cursor); result.Partial {
			t.Fatal(result)
		}
	}
	syncer.Forget("p")
	if entries, weight := syncer.projects.Stats(); entries != 0 || weight != 0 {
		t.Fatal(entries, weight)
	}
	for i := 0; i < 150; i++ {
		syncer.Delta(fmt.Sprint(i), []core.ThreadView{a}, "")
	}
	if entries, _ := syncer.projects.Stats(); entries != 128 {
		t.Fatal(entries)
	}
	oversized := core.ThreadView{ID: strings.Repeat("a", 9*1024*1024)}
	if result := syncer.Delta("large", []core.ThreadView{oversized}, syncer.epoch+".0"); result.Partial {
		t.Fatal("uncached large result returned a partial snapshot")
	}
	if _, found := syncer.projects.Get("large"); found {
		t.Fatal("oversized metadata retained")
	}
	syncer.Close()
	if entries, weight := syncer.projects.Stats(); entries != 0 || weight != 0 {
		t.Fatal(entries, weight)
	}
}
