package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestProjectAndThreadViewsMatchLegacy(t *testing.T) {
	const at int64 = 1800000000000
	day := int64(24 * time.Hour / time.Millisecond)
	type step struct {
		SQL string `json:"sql"`
		Now int64  `json:"now"`
	}
	steps := []step{
		{`INSERT INTO projects (id,name,coordinator_id,created_at,updated_at) VALUES ('prj_a','测试项目','crd_a',1800000000000,1800000000000),('prj_b','另一个项目','crd_b',1800000000000,1800000000001);
		INSERT INTO conversations (id,project_id,role) VALUES ('crd_a','prj_a','coordinator'),('crd_b','prj_b','coordinator'),('thr_a','prj_a','thread'),('thr_b','prj_a','thread');
		INSERT INTO threads (id,project_id,title,created_at,updated_at) VALUES ('thr_a','prj_a','测试',1800000000000,1800000000000),('thr_b','prj_a','其他任务',1800000000000,1800000000000);`, at},
		{`UPDATE threads SET started=1 WHERE id='thr_a'`, at},
		{`UPDATE conversations SET pending_seq=1 WHERE id='thr_a'`, at},
		{`UPDATE conversations SET turn_id='turn_a',turn_started_at=1800000000500 WHERE id='thr_a'`, at + 1000},
		{`INSERT INTO attentions (id,project_id,thread_id,conversation_id,block,created_at) VALUES ('att_a','prj_a','thr_a','thr_a','{"type":"confirm","title":"确认"}',1800000000000),('att_b','prj_a',NULL,'crd_a','{"type":"question","title":"问题"}',1800000000000);
		INSERT INTO attention_waiters VALUES ('att_a','thr_a'),('att_a','thr_b');`, at},
		{`UPDATE attentions SET closed=1`, at},
		{`UPDATE conversations SET turn_id=NULL,turn_started_at=NULL,pending_seq=0,last_outcome='failed' WHERE id='thr_a'`, at},
		{`UPDATE conversations SET last_outcome='cancelled',last_turn_at=1800000000001 WHERE id='thr_a'`, at + 2},
		{`UPDATE conversations SET last_outcome='completed' WHERE id='thr_a'`, at + 2},
		{`UPDATE conversations SET wait_id='wait_a' WHERE id='thr_a'`, at + 2},
		{`UPDATE threads SET last_report='waiting' WHERE id='thr_a'`, at + 2},
		{`UPDATE threads SET last_report='ready',reported_at=1800000000000,last_headline='标题',last_summary='说明',reported_head='head_a' WHERE id='thr_a'`, at + 2},
		{`UPDATE threads SET reviewing=1,reviewer_id='thr_b',review_rounds=2 WHERE id='thr_a'; UPDATE threads SET review_of='thr_a' WHERE id='thr_b'`, at + 2},
		{`UPDATE threads SET reviewed_at=1800000000000 WHERE id='thr_a'`, at + 2},
		{`UPDATE threads SET reviewing=0 WHERE id='thr_a'`, at + 2},
		{`UPDATE threads SET last_report='done',reviewed_at=NULL WHERE id='thr_a'; UPDATE projects SET settings='{"delivery":{"mode":"external","instructions":"向集成平台提票"}}' WHERE id='prj_a'`, at + 7*day - 1},
		{`UPDATE threads SET last_report='done' WHERE id='thr_a'`, at + 7*day},
		{`UPDATE threads SET last_report='ready',merged='{"head":"head_a","at":1800000000000}',landed_at=1800000000000,workspace_label='workspace_a',workspace_branch='work/test',workspace_base='main',workspace_removed=1,workspace_parked=1 WHERE id='thr_a'`, at + day - 1},
		{`UPDATE threads SET last_report='ready' WHERE id='thr_a'`, at + day},
		{`UPDATE threads SET user_at=1800000000001 WHERE id='thr_a'`, at + day},
		{`UPDATE threads SET reported_at=1800000000002,reported_head='head_b',merged='{"head":"head_a","at":1800000000000}' WHERE id='thr_a'`, at + day},
		{`UPDATE threads SET pull_request='{"state":"merged","number":4}',changes='{"added":9,"removed":3,"files":1}' WHERE id='thr_a'`, at + day},
		{`INSERT INTO artifacts (id,project_id,kind,title,version,content,preview,created_by,updated_by,created_at,updated_at) VALUES ('art_a','prj_a','document','最新标题',1,'{}','{}','user','user',0,0),('art_b','prj_b','document','其他项目的产物',1,'{}','{}','user','user',0,0);
		UPDATE threads SET last_artifacts='["missing","art_b","art_a"]',reference_sources='[{"id":"ref_a"}]',reference_ended='[{"attentionId":"att_old","kept":false}]',model='model_a',branch_slug='test',auto=1,autopilot_held='held' WHERE id='thr_a';
		UPDATE conversations SET input_tokens=100,output_tokens=3,cached_tokens=20,cache_write_tokens=10,reasoning_tokens=2,cost_usd=0.5,cost_cny=0.2 WHERE id='thr_a';
		UPDATE conversations SET input_tokens=200,cost_usd=0.2 WHERE id='crd_a'`, at + day},
		{`INSERT INTO todos VALUES ('thr_a','prj_a','[{"label":"已完成","status":"done"},{"label":"正在做","status":"doing"},{"label":"还在做","status":"doing"},{"label":"待做","status":"pending"}]',1800000000010)`, at + 8*day},
		{`UPDATE threads SET aside_until=1800000000005,aside_reason='稍后',aside_by='user',aside_due=1 WHERE id='thr_b'`, at + 8*day},
		{`UPDATE threads SET stopped_reason='暂停',stopped_by='user',stopped_at=1800000000004 WHERE id='thr_a'`, at + 8*day},
		{`UPDATE threads SET stopped_reason=NULL WHERE id='thr_a'; INSERT INTO messages (id,project_id,conversation_id,author,kind,content,created_at) VALUES ('msg_a','prj_a','thr_a','user','text','新消息',1800691200000)`, at + 8*day},
		{`INSERT INTO events (id,type,project_id,data,at) VALUES ('evt_a','turn.step','prj_a','{}',1800691200100); INSERT INTO transcript (project_id,conversation_id,seq,kind) VALUES ('prj_a','thr_a',1,'step')`, at + 8*day + 100},
		{`UPDATE conversations SET turn_id='turn_b',turn_started_at=1800691200200 WHERE id='thr_a'`, at + 8*day + 200},
	}
	var expected []map[string]any
	oracle(t, "packages/core/src/testing/go-views-fixture.ts", steps, &expected)
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "views.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i, s := range steps {
		if _, err := store.DB().ExecContext(t.Context(), s.SQL); err != nil {
			t.Fatalf("step %d SQL: %v", i, err)
		}
		reader := Reader{DB: store.DB(), Now: func() time.Time { return time.UnixMilli(s.Now) }}
		projects, err := reader.ListProjects(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		detail, err := reader.GetProjectDetail(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		actual := object{"projects": projects, "detail": detail}
		if !reflect.DeepEqual(normalize(actual), normalize(expected[i])) {
			t.Fatalf("step %d differs\ngot %s\nwant%s", i, encoded(normalize(actual)), encoded(normalize(expected[i])))
		}
	}
	_, err = (Reader{DB: store.DB()}).GetThread(t.Context(), "missing")
	if err == nil {
		t.Fatal("missing thread did not fail")
	}
	_, err = (Reader{DB: store.DB()}).GetProject(t.Context(), "missing")
	if err == nil {
		t.Fatal("missing project did not fail")
	}
}

func TestStoredViewJSONErrorsSurface(t *testing.T) {
	_, err := toThread(record{"merged": "broken"}, 0)
	if err == nil {
		t.Fatal("bad persisted JSON silently discarded")
	}
	// A zero count is distinct from null when serialized to the existing UI.
	bytes, err := json.Marshal(usage(record{}))
	if err != nil || string(bytes) == "null" {
		t.Fatal(string(bytes), err)
	}
}
