package core

import (
	"path/filepath"
	"testing"
)

func TestActiveIssueWritesAreReleasedAndRestartIsUncertain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "issues.sqlite")
	repo, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	project, err := repo.CreateProject(ctx, nil, ProjectStart{Message: "处理工单"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "处理工单"})
	if err != nil {
		t.Fatal(err)
	}
	card := func() map[string]any { return object{"type": "confirm", "title": "关闭工单？"} }
	ask := func(purpose string) IssueWrite {
		t.Helper()
		result, err := repo.IssueApproval(ctx, project.ID, thread.ID, purpose, card)
		if err != nil || result.State != "asked" {
			t.Fatalf("%+v %v", result, err)
		}
		cards, err := records(ctx, repo.Store.DB(), "SELECT id FROM attentions WHERE project_id = ? AND purpose = ? AND closed = 0", project.ID, purpose)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if len(cards) > 0 {
			id = cards[0].text("id")
		}
		if id == "" {
			t.Fatal("missing question")
		}
		if err := repo.AnswerAttention(ctx, id, object{"type": "confirm", "approved": true}); err != nil {
			t.Fatal(err)
		}
		write := IssueWrite{ThreadID: thread.ID, AttentionID: id, Purpose: purpose, Tracker: "github", Number: 1, Action: "close", Text: "已修复"}
		if err := repo.StartIssueWrite(ctx, project.ID, write); err != nil {
			t.Fatal(err)
		}
		return write
	}
	write := ask("first")
	if len(repo.issueWrites) != 1 {
		t.Fatal(repo.issueWrites)
	}
	if err := repo.FinishIssueWrite(ctx, project.ID, write, nil); err != nil {
		t.Fatal(err)
	}
	if len(repo.issueWrites) != 0 {
		t.Fatal("finished write retained")
	}
	write = ask("second")
	if err := repo.MarkIssueWriteUnsure(ctx, project.ID, write.AttentionID, thread.ID); err != nil {
		t.Fatal(err)
	}
	if len(repo.issueWrites) != 0 {
		t.Fatal("uncertain write retained")
	}
	write = ask("restart")
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	if len(repo.issueWrites) != 0 {
		t.Fatal("closed repository retained writes")
	}
	repo, err = Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.IssueApproval(ctx, project.ID, thread.ID, "restart", card)
	if err != nil || result.State != "unsure" || result.AttentionID != write.AttentionID {
		t.Fatalf("%+v %v", result, err)
	}
	ask("delete")
	if err := repo.DeleteProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	if len(repo.issueWrites) != 0 {
		t.Fatal("deleted project retained writes")
	}
}
