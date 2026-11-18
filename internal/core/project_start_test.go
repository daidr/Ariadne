package core

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestProjectStartAndReferenceLifecyclesMatchLegacy(t *testing.T) {
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	source := func(path string) object { return object{"kind": "local", "settings": object{"path": path}} }
	add("create", object{"name": nil, "start": object{"message": "\n  请实现一个需要验证的功能，并在完成后告诉我结果。  \n第二行", "source": object{"kind": "local", "settings": object{"path": "D:/main"}}, "references": []object{{"source": source("D:/initial"), "where": "D:/initial"}}}})
	add("create", object{"name": " ", "start": object{}})
	add("create", object{"name": "必须回滚", "start": object{"references": []object{{"source": source("D:/duplicate"), "where": "D:/a"}, {"source": source("D:/duplicate"), "where": "D:/b"}}}})
	add("thread", object{"title": "  测试讨论串  ", "branch": "Fix Export!", "auto": true})
	add("project-ref", object{"source": source("D:/ref_a"), "where": "owner/ref.git", "tag": "project_a"})
	add("project-ref", object{"source": source("D:/ref_b"), "where": "D:/ref", "tag": "project_b"})
	add("project-ref", object{"source": source("D:/ref_a"), "where": "other"})
	add("thread-ref", object{"source": source("D:/thread_a"), "where": "D:/ref", "attentionId": "att_read_a", "tag": "thread_a"})
	add("thread-ref", object{"source": source("D:/thread_a"), "where": "other", "attentionId": "att_read_b"})
	add("project-ref", object{"source": source("D:/ref_c"), "where": "D:/ref-3", "tag": "project_c"})
	add("keep", object{"ref": "thread_a", "tag": "kept_a"})
	add("thread-ref", object{"source": source("D:/ref_a"), "where": "D:/ref", "attentionId": "att_read_c", "tag": "shared"})
	add("keep", object{"ref": "shared", "tag": "kept_existing"})
	add("thread-ref", object{"source": source("D:/transient"), "where": ".git", "attentionId": "att_read_d", "tag": "transient"})
	add("remove-thread", object{"ref": "missing"})
	add("remove-thread", object{"ref": "transient"})
	add("keep", object{"ref": "missing"})
	add("remove-project", object{"ref": "missing"})
	add("remove-project", object{"ref": "project_b"})
	for i := 0; i < 6; i++ {
		add("project-ref", object{"source": source("D:/extra_" + encoded(i)), "where": "D:/extra", "tag": "extra_" + encoded(i)})
	}
	add("thread-ref", object{"source": source("D:/ref_a"), "where": "D:/ref", "attentionId": "att_read_e", "tag": "over_limit"})
	add("keep", object{"ref": "over_limit"})
	startRefs := []object{}
	for i := 0; i < 9; i++ {
		startRefs = append(startRefs, object{"source": source("D:/start_" + encoded(i)), "where": "D:/start"})
	}
	add("create", object{"name": "第九项失败时整体回滚", "start": object{"references": startRefs}})
	add("create", object{"name": " 显式命名 ", "start": object{"message": "  新项目首条消息  "}})
	var expected []any
	oracle(t, "packages/core/src/testing/go-project-start-fixture.ts", commands, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "project-start.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, thread := "", ""
	references := map[string]string{}
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		reference := a.str("ref")
		if value, has := references[reference]; has {
			reference = value
		}
		var value any
		var err error
		var taggedID string
		switch command.str("op") {
		case "create":
			var name *string
			if text, ok := a["name"].(string); ok {
				name = &text
			}
			var start ProjectStart
			decodeInput(t, a["start"], &start)
			var created ProjectView
			created, err = repo.CreateProject(t.Context(), name, start)
			if err == nil {
				project = created.ID
				value = created
			}
		case "thread":
			var input NewThread
			decodeInput(t, a, &input)
			var created ThreadView
			created, err = repo.CreateThread(t.Context(), project, input)
			if err == nil {
				thread = created.ID
				value = created
				taggedID = created.ID
			}
		case "project-ref":
			var src Source
			decodeInput(t, a["source"], &src)
			var ref Reference
			ref, err = repo.AddProjectReference(t.Context(), project, src, a.str("where"))
			value = ref
			taggedID = ref.ID
		case "thread-ref":
			var src Source
			decodeInput(t, a["source"], &src)
			var ref Reference
			ref, err = repo.AddThreadReference(t.Context(), thread, src, a.str("where"), a.str("attentionId"))
			value = ref
			taggedID = ref.ID
		case "keep":
			var ref Reference
			ref, err = repo.KeepThreadReference(t.Context(), thread, reference)
			value = ref
			taggedID = ref.ID
		case "remove-thread":
			value, err = repo.RemoveThreadReference(t.Context(), thread, reference)
		case "remove-project":
			value, err = repo.RemoveProjectReference(t.Context(), project, reference)
		default:
			t.Fatal(command)
		}
		if err == nil && a.str("tag") != "" {
			references[a.str("tag")] = taggedID
		}
		var localized any
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatal(err)
			}
			localized = domain
			value = nil
		}
		projects, err := repo.ListProjects(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		details := []ProjectDetail{}
		reads := [][]object{}
		for _, p := range projects {
			detail, err := repo.GetProjectDetail(t.Context(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
			ids := []string{p.CoordinatorID}
			for _, thread := range detail.Threads {
				ids = append(ids, thread.ID)
			}
			for _, id := range ids {
				transcript, err := repo.ListTranscript(t.Context(), id, TranscriptOptions{})
				if err != nil {
					t.Fatal(err)
				}
				reads = append(reads, transcript)
			}
		}
		stored, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		events := []object{}
		for _, e := range stored {
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": e.Data, "at": e.At})
		}
		actual := object{"value": value, "error": localized, "details": details, "reads": reads, "events": events}
		if !reflect.DeepEqual(normalizedIDs(actual), normalizedIDs(expected[i])) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(normalizedIDs(expected[i])))
		}
	}
}
