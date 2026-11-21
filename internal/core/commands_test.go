package core

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestCreateThreadValidatesProjectAndNodeAtomically(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "new-thread.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.CreateThread(t.Context(), "missing", NewThread{Title: "任务"}); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	node := "missing"
	if _, err := repo.CreateThread(t.Context(), "prj_a", NewThread{Title: "任务", NodeID: &node}); err == nil {
		t.Fatal("missing node accepted")
	}
	branch := "Fix Export!"
	thread, err := repo.CreateThread(t.Context(), "prj_a", NewThread{Title: "  自动任务  ", Branch: &branch, Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	if thread.Title != "自动任务" || thread.BranchSlug != "fix-export" || !thread.Auto || thread.Stage != "queued" {
		t.Fatalf("new thread: %+v", thread)
	}
	var notices, indexed, conversation int
	for _, test := range []struct {
		sql    string
		target *int
	}{{"SELECT count(*) FROM notices WHERE kind='thread-auto-started'", &notices}, {"SELECT count(*) FROM search_docs WHERE ref_id=?", &indexed}, {"SELECT count(*) FROM conversations WHERE id=?", &conversation}} {
		args := []any{}
		if strings.Contains(test.sql, "?") {
			args = append(args, thread.ID)
		}
		if err := repo.Store.DB().QueryRowContext(t.Context(), test.sql, args...).Scan(test.target); err != nil {
			t.Fatal(err)
		}
	}
	if notices != 1 || indexed != 1 || conversation != 1 {
		t.Fatal(notices, indexed, conversation)
	}
}
