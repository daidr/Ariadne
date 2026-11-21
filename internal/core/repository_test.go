package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/schema"
)

type repositorySnapshot struct {
	Tables       map[string]any `json:"tables"`
	Projects     any            `json:"projects"`
	Details      any            `json:"details"`
	Events       any            `json:"events"`
	Now          int64          `json:"now"`
	Files        any            `json:"files"`
	Shared       any            `json:"shared"`
	Reads        any            `json:"reads"`
	Pending      any            `json:"pending"`
	Running      any            `json:"running"`
	ProjectReads any            `json:"projectReads"`
}

func nativeSnapshot(t *testing.T, repo *Repository) repositorySnapshot {
	t.Helper()
	result := repositorySnapshot{Tables: map[string]any{}}
	for _, table := range projectionTables {
		fields := "*"
		if table == "search_text" {
			fields = "rowid, *"
		}
		rows, err := records(t.Context(), repo.Store.DB(), "SELECT "+fields+" FROM "+table+" ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		result.Tables[table] = rows
	}
	projects, err := repo.ListProjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result.Projects = projects
	details := []ProjectDetail{}
	for _, project := range projects {
		detail, err := repo.GetProjectDetail(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	result.Details = details
	events, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{Limit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	result.Events = events
	result.Now = repo.Reader.now()
	files, err := records(t.Context(), repo.Store.DB(), "SELECT id, project_id, mime, sha256, hex(data) AS bytes FROM files ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	result.Files = files
	shared, err := records(t.Context(), repo.Store.DB(), "SELECT artifact_id, version, hex(state) AS bytes FROM artifact_shared ORDER BY artifact_id")
	if err != nil {
		t.Fatal(err)
	}
	result.Shared = shared
	ids, err := records(t.Context(), repo.Store.DB(), "SELECT id FROM conversations ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	editSeq := int64(0)
	for _, event := range events {
		if event.Type == "message.edited" {
			editSeq = event.Seq
			break
		}
	}
	reads := []object{}
	for _, row := range ids {
		id := row.text("id")
		conversation, err := repo.GetConversation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		messages, err := repo.ListMessages(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		one := 1
		beforeEdit := editSeq - 1
		cursors := []MessageCursor{{}, {Limit: &one}, {Since: &beforeEdit}}
		if len(messages) > 0 {
			cursors = append(cursors, MessageCursor{From: &messages[0].ID}, MessageCursor{Before: &messages[len(messages)-1].ID, Limit: &one})
		}
		pages := []MessagePage{}
		for _, cursor := range cursors {
			page, err := repo.PageMessages(t.Context(), id, cursor)
			if err != nil {
				t.Fatal(err)
			}
			pages = append(pages, page)
		}
		read := object{"conversation": conversation, "messages": messages, "pages": pages}
		for _, test := range []struct {
			key     string
			options TranscriptOptions
		}{{"transcript", TranscriptOptions{}}, {"beforeEdit", TranscriptOptions{UpTo: &beforeEdit}}, {"afterEdit", TranscriptOptions{UpTo: &editSeq}}, {"recent", TranscriptOptions{After: &editSeq}}} {
			entries, err := repo.ListTranscript(t.Context(), id, test.options)
			if err != nil {
				t.Fatal(err)
			}
			read[test.key] = entries
		}
		changed, err := repo.MessagesChangedSince(t.Context(), id, beforeEdit)
		if err != nil {
			t.Fatal(err)
		}
		read["changed"] = changed
		todo, err := repo.GetTodo(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		read["todo"] = todo
		notices, err := repo.ListNotices(t.Context(), id, NoticeRange{})
		if err != nil {
			t.Fatal(err)
		}
		read["notices"] = notices
		instructions, err := repo.GetInstructions(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		read["instructions"] = instructions
		reads = append(reads, read)
	}
	result.Reads = reads
	pending, err := repo.ListPendingConversations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result.Pending = pending
	running, err := repo.ListRunningConversations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result.Running = running
	projectReads := []object{}
	for _, project := range projects {
		read := object{}
		plan, err := repo.GetPlan(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		read["plan"] = plan
		artifacts, err := repo.ListArtifacts(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		read["artifacts"] = artifacts
		searches := []object{}
		for _, query := range []string{"说明", "原文", "用户", ""} {
			search, err := repo.Search(t.Context(), project.ID, query)
			if err != nil {
				t.Fatal(err)
			}
			searches = append(searches, search)
		}
		read["search"] = searches
		needs, err := repo.ListNeedsMe(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		read["needs"] = needs
		attentions, err := repo.ListOpenAttentions(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		read["attentions"] = attentions
		mayAsk, err := repo.MayAskGate(t.Context(), project.ID, "m")
		if err != nil {
			t.Fatal(err)
		}
		stale, err := repo.StaleGates(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		read["gates"] = object{"mayAsk": mayAsk, "stale": stale}
		threads, err := repo.ListThreads(t.Context(), project.ID)
		if err != nil {
			t.Fatal(err)
		}
		reviewReads := []object{}
		for _, thread := range threads {
			reviews, err := repo.ListReviews(t.Context(), thread.ID)
			if err != nil {
				t.Fatal(err)
			}
			reviewReads = append(reviewReads, object{"id": thread.ID, "reviews": reviews})
		}
		read["threads"] = reviewReads
		artifactReads := []object{}
		for _, artifact := range artifacts {
			current, err := repo.GetArtifact(t.Context(), artifact.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			one := int64(1)
			old, err := repo.GetArtifact(t.Context(), artifact.ID, &one)
			if err != nil {
				t.Fatal(err)
			}
			versions, err := repo.ListArtifactVersions(t.Context(), artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			comments, err := repo.ListArtifactComments(t.Context(), artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			feedback, err := repo.ListArtifactFeedback(t.Context(), artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifactReads = append(artifactReads, object{"current": current, "old": old, "versions": versions, "comments": comments, "feedback": feedback})
		}
		read["artifactReads"] = artifactReads
		projectReads = append(projectReads, read)
	}
	result.ProjectReads = projectReads
	return result
}
func TestCompleteProjectionsReplayRealLegacyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "项目完整重放.sqlite")
	var expected repositorySnapshot
	oracle(t, "packages/agent/src/testing/go-repository-fixture.ts", object{"mode": "create", "path": path}, &expected)
	opts := Options{Path: path, Now: func() time.Time { return time.UnixMilli(expected.Now) }}
	compare := func(label string, actual repositorySnapshot) {
		t.Helper()
		got := normalizeLegacyExpected(actual).(map[string]any)
		want := normalizeLegacyExpected(expected).(map[string]any)
		gotTables := got["tables"].(map[string]any)
		wantTables := want["tables"].(map[string]any)
		gotTables["threads"] = stripNewThreadColumns(gotTables["threads"])
		wantTables["threads"] = stripNewThreadColumns(wantTables["threads"])
		if !reflect.DeepEqual(got, want) {
			for _, table := range projectionTables {
				if !reflect.DeepEqual(gotTables[table], wantTables[table]) {
					t.Fatalf("%s table %s differs\ngot %s\nwant%s", label, table, encoded(gotTables[table]), encoded(wantTables[table]))
				}
			}
			t.Fatalf("%s read views or original bytes differ\ngot %s\nwant%s", label, encoded(got), encoded(want))
		}
	}
	repo, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	compare("existing", nativeSnapshot(t, repo))
	if err := repo.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	compare("native replay", nativeSnapshot(t, repo))
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	var roundTrip repositorySnapshot
	oracle(t, "packages/agent/src/testing/go-repository-fixture.ts", object{"mode": "read", "path": path}, &roundTrip)
	compare("legacy reads native replay", roundTrip)
	// Older projection versions rebuild from the intact log, including repairing stale rows.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 66; UPDATE projects SET name = 'stale'; CREATE TABLE plugin_fixture (value TEXT); INSERT INTO plugin_fixture VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	repo, err = Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	compare("upgrade replay", nativeSnapshot(t, repo))
	var plugin string
	if err := repo.Store.DB().QueryRowContext(t.Context(), "SELECT value FROM plugin_fixture").Scan(&plugin); err != nil || plugin != "keep" {
		t.Fatal(plugin, err)
	}
}

func TestRepositoryDeletionAndTurnStartAreSerialized(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "race.sqlite")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var deleteErr, turnErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; deleteErr = repo.DeleteProject(t.Context(), "prj_a") }()
		go func() {
			defer wg.Done()
			<-start
			_, turnErr = repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "turn.started", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"crd_a","turnId":"turn_a","model":"model_a"}`)}})
		}()
		close(start)
		wg.Wait()
		var domain *domainerr.Error
		if deleteErr == nil {
			if !errors.As(turnErr, &domain) || domain.Key != "error.notFound" {
				t.Fatalf("deleted project accepted turn: %v", turnErr)
			}
		} else {
			if !errors.As(deleteErr, &domain) || domain.Key != "error.projectBusy" || turnErr != nil {
				t.Fatalf("busy deletion: %v / %v", deleteErr, turnErr)
			}
		}
		_ = repo.Close()
	}
}

func TestFailedUpgradeRollsBackSchemaAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade-failure.sqlite")
	repo, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"必须保留","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), `PRAGMA user_version = 66; INSERT INTO events (id,type,project_id,data,at) VALUES ('evt_bad','artifact.created','prj_a','{}',1)`); err != nil {
		t.Fatal(err)
	}
	_ = repo.Close()
	if reopened, err := Open(t.Context(), Options{Path: path}); err == nil {
		_ = reopened.Close()
		t.Fatal("invalid event upgraded without error")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var name string
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 66 {
		t.Fatal(version, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT name FROM projects WHERE id = 'prj_a'").Scan(&name); err != nil || name != "必须保留" {
		t.Fatal(name, err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = "+encoded(schema.Version+1)); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), Options{Path: path}); err == nil {
		_ = reopened.Close()
		t.Fatal("newer database version was downgraded")
	}
}
