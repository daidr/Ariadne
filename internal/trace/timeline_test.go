package trace

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestTimelineShrinksPagesWithoutDroppingTurns(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "trace.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		for _, kind := range []string{"turn.started", "turn.finished"} {
			data, _ := json.Marshal(object{"conversationId": project.CoordinatorID, "turnId": string(rune('a' + i)), "outcome": "completed", "model": "fixture"})
			if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: kind, ProjectID: project.ID, Data: data}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var before *int64
	seen := map[string]bool{}
	for range 10 {
		page, err := Build(t.Context(), repo, project.CoordinatorID, Options{Before: before, MaxEvents: 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Turns) == 0 || len(page.Turns) > 2 {
			t.Fatal("budget did not shrink page", page)
		}
		for _, turn := range page.Turns {
			id := text(turn["turnId"])
			if seen[id] {
				t.Fatal("duplicate turn", id)
			}
			seen[id] = true
		}
		if !page.More {
			break
		}
		seq := int64(number(page.Turns[0]["seq"]))
		before = &seq
	}
	if len(seen) != 7 {
		t.Fatal("pagination lost turns", seen)
	}
	if _, err := Build(t.Context(), repo, project.CoordinatorID, Options{MaxEvents: 1}); err == nil {
		t.Fatal("single-turn budget ignored")
	}
}
