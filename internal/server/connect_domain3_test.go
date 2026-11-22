package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func TestDomain3ConnectServices(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "domain3.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	seed := []eventstore.NewEvent{
		{ProjectID: "prj_a", Type: "project.created", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)},
		{ProjectID: "prj_a", Type: "thread.created", Data: json.RawMessage(`{"threadId":"thr_a","title":"讨论串 A"}`)},
		{ProjectID: "prj_a", Type: "thread.created", Data: json.RawMessage(`{"threadId":"thr_b","title":"讨论串 B"}`)},
		{ProjectID: "prj_a", Type: "message.posted", Data: json.RawMessage(`{"conversationId":"thr_a","messageId":"msg_agent","author":"agent","kind":"chat","content":"代理回复"}`)},
		{ProjectID: "prj_a", Type: "message.posted", Data: json.RawMessage(`{"conversationId":"crd_a","messageId":"msg_crd_1","author":"agent","kind":"chat","content":"协调器回复"}`)},
		{ProjectID: "prj_a", Type: "review.requested", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"rev_1","round":1,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)},
		{ProjectID: "prj_a", Type: "review.submitted", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"rev_1","round":1,"verdict":"changes_requested","summary":"第一轮","comments":[{"path":"a.go","line":10,"quote":"foo","body":"改一下","kind":"issue"}]}`)},
		{ProjectID: "prj_a", Type: "review.requested", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"rev_1","round":2,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)},
		{ProjectID: "prj_a", Type: "review.submitted", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"rev_1","round":2,"verdict":"approved","summary":"第二轮","comments":[]}`)},
		{ProjectID: "prj_a", Type: "review.requested", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"rev_1","round":3,"head":"cccccccccccccccccccccccccccccccccccccccc"}`)},
		{ProjectID: "prj_a", Type: "review.submitted", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"rev_1","round":3,"verdict":"approved","summary":"第三轮","comments":[]}`)},
	}
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}

	// Verify P-5 batch lookup returns both threads in one call.
	batch, err := repo.ListThreadsByIDs(t.Context(), []string{"thr_a", "thr_b", "thr_missing", "thr_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || batch["thr_a"].Title != "讨论串 A" || batch["thr_b"].Title != "讨论串 B" {
		t.Fatalf("unexpected batch lookup result: %+v", batch)
	}

	api, err := New(Options{Repository: repo, Token: "test-token", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	authOpt := connect.WithInterceptors(bearerClientInterceptor("test-token"))
	convClient := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix, authOpt)
	threadClient := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix, authOpt)

	// ConversationService: GetConversation + cursor validation
	badLimit := int32(0)
	if _, err := convClient.GetConversation(t.Context(), connect.NewRequest(&ariadnev1.GetConversationRequest{
		ConversationId: "crd_a",
		Limit:          &badLimit,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for limit=0, got %v", err)
	}
	badSince := int64(-1)
	if _, err := convClient.GetConversation(t.Context(), connect.NewRequest(&ariadnev1.GetConversationRequest{
		ConversationId: "crd_a",
		Since:          &badSince,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for since=-1, got %v", err)
	}
	if err := repo.MarkRead(t.Context(), "crd_a", "msg_crd_1"); err != nil {
		t.Fatal(err)
	}
	convResp, err := convClient.GetConversation(t.Context(), connect.NewRequest(&ariadnev1.GetConversationRequest{
		ConversationId: "crd_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if convResp.Msg.GetConversation().GetId() != "crd_a" || convResp.Msg.GetReadThrough() != "msg_crd_1" || convResp.Msg.GetLastSeq() == 0 {
		t.Fatalf("unexpected conversation snapshot: %+v", convResp.Msg)
	}

	// ConversationService: PostMessage with line comments (also sets up P-6 line-comments pagination)
	for _, body := range []string{"评论 1", "评论 2", "评论 3"} {
		postResp, err := convClient.PostMessage(t.Context(), connect.NewRequest(&ariadnev1.PostMessageRequest{
			ProjectId:      "prj_a",
			ConversationId: "thr_a",
			Content:        "",
			Comments: []*ariadnev1.LineComment{{
				Path:  "a.go",
				Line:  12,
				Quote: "line",
				Body:  body,
			}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if len(postResp.Msg.GetMessage().GetComments()) != 1 {
			t.Fatalf("expected 1 line comment, got %+v", postResp.Msg.GetMessage())
		}
	}

	// D-5: Split AddReaction / RemoveReaction RPCs
	added, err := convClient.AddReaction(t.Context(), connect.NewRequest(&ariadnev1.AddReactionRequest{
		MessageId: "msg_agent",
		Emoji:     " 👍 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(added.Msg.GetMessage().GetReactions()) != 1 || added.Msg.GetMessage().GetReactions()[0].GetEmoji() != "👍" {
		t.Fatalf("unexpected reactions after add: %+v", added.Msg.GetMessage().GetReactions())
	}
	removed, err := convClient.RemoveReaction(t.Context(), connect.NewRequest(&ariadnev1.RemoveReactionRequest{
		MessageId: "msg_agent",
		Emoji:     " 👍 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Msg.GetMessage().GetReactions()) != 0 {
		t.Fatalf("expected empty reactions after remove, got %+v", removed.Msg.GetMessage().GetReactions())
	}

	// Memory CRUD RPCs
	createdMem, err := convClient.CreateMemory(t.Context(), connect.NewRequest(&ariadnev1.CreateMemoryRequest{
		ProjectId: "prj_a",
		Text:      "常用规则",
	}))
	if err != nil {
		t.Fatal(err)
	}
	memID := createdMem.Msg.GetMemory().GetId()
	updatedMem, err := convClient.UpdateMemory(t.Context(), connect.NewRequest(&ariadnev1.UpdateMemoryRequest{
		ProjectId: "prj_a",
		MemoryId:  memID,
		Text:      "更新后的规则",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if updatedMem.Msg.GetMemory().GetText() != "更新后的规则" {
		t.Fatalf("unexpected updated memory: %+v", updatedMem.Msg.GetMemory())
	}
	listMem, err := convClient.ListMemories(t.Context(), connect.NewRequest(&ariadnev1.ListMemoriesRequest{ProjectId: "prj_a"}))
	if err != nil || len(listMem.Msg.GetMemories()) != 1 {
		t.Fatalf("unexpected memories list: %+v err=%v", listMem, err)
	}
	if _, err := convClient.DeleteMemory(t.Context(), connect.NewRequest(&ariadnev1.DeleteMemoryRequest{
		ProjectId: "prj_a",
		MemoryId:  memID,
	})); err != nil {
		t.Fatal(err)
	}

	// MarkRead, NoteReturn, StopTurn, RetryTurn
	if _, err := convClient.MarkRead(t.Context(), connect.NewRequest(&ariadnev1.MarkReadRequest{
		ConversationId: "thr_a",
		UpTo:           "msg_agent",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := convClient.NoteReturn(t.Context(), connect.NewRequest(&ariadnev1.NoteReturnRequest{
		ProjectId: "prj_a",
		Away:      600000,
	})); err != nil {
		t.Fatal(err)
	}
	stopResp, err := convClient.StopTurn(t.Context(), connect.NewRequest(&ariadnev1.StopTurnRequest{ConversationId: "crd_a"}))
	if err != nil || stopResp.Msg.GetStopped() {
		t.Fatalf("unexpected stop turn: %+v err=%v", stopResp, err)
	}
	if _, err := convClient.RetryTurn(t.Context(), connect.NewRequest(&ariadnev1.RetryTurnRequest{ConversationId: "crd_a"})); err != nil {
		t.Fatal(err)
	}

	// P-6: ListReviews pagination
	reviewLimit := int32(2)
	page1, err := threadClient.ListReviews(t.Context(), connect.NewRequest(&ariadnev1.ListReviewsRequest{
		ThreadId: "thr_a",
		Limit:    &reviewLimit,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Msg.GetRounds()) != 2 || !page1.Msg.GetHasMore() || page1.Msg.NextBeforeId == nil {
		t.Fatalf("unexpected reviews page 1: %+v", page1.Msg)
	}
	if page1.Msg.GetRounds()[0].GetRound() != 2 || page1.Msg.GetRounds()[1].GetRound() != 3 {
		t.Fatalf("expected rounds [2, 3], got [%d, %d]", page1.Msg.GetRounds()[0].GetRound(), page1.Msg.GetRounds()[1].GetRound())
	}
	page2, err := threadClient.ListReviews(t.Context(), connect.NewRequest(&ariadnev1.ListReviewsRequest{
		ThreadId: "thr_a",
		BeforeId: page1.Msg.NextBeforeId,
		Limit:    &reviewLimit,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Msg.GetRounds()) != 1 || page2.Msg.GetHasMore() || page2.Msg.GetRounds()[0].GetRound() != 1 {
		t.Fatalf("unexpected reviews page 2: %+v", page2.Msg)
	}

	// P-6: ListLineComments pagination
	commentLimit := int32(2)
	cPage1, err := threadClient.ListLineComments(t.Context(), connect.NewRequest(&ariadnev1.ListLineCommentsRequest{
		ThreadId: "thr_a",
		Limit:    &commentLimit,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cPage1.Msg.GetComments()) != 2 || !cPage1.Msg.GetHasMore() || cPage1.Msg.NextBeforeMessageId == nil {
		t.Fatalf("unexpected line comments page 1: %+v", cPage1.Msg)
	}
	if cPage1.Msg.GetComments()[0].GetBody() != "评论 2" || cPage1.Msg.GetComments()[1].GetBody() != "评论 3" {
		t.Fatalf("unexpected line comments page 1 bodies: %+v", cPage1.Msg.GetComments())
	}
	cPage2, err := threadClient.ListLineComments(t.Context(), connect.NewRequest(&ariadnev1.ListLineCommentsRequest{
		ThreadId:        "thr_a",
		BeforeMessageId: cPage1.Msg.NextBeforeMessageId,
		Limit:           &commentLimit,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cPage2.Msg.GetComments()) != 1 || cPage2.Msg.GetHasMore() || cPage2.Msg.GetComments()[0].GetBody() != "评论 1" {
		t.Fatalf("unexpected line comments page 2: %+v", cPage2.Msg)
	}

	// Thread lifecycle RPCs: ResumeThread on stopped thread and CompleteThread on ready thread
	if _, err := repo.StopThread(t.Context(), "thr_a", "暂停测试", "user"); err != nil {
		t.Fatal(err)
	}
	resumed, err := threadClient.ResumeThread(t.Context(), connect.NewRequest(&ariadnev1.ResumeThreadRequest{ThreadId: "thr_a"}))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Msg.GetThread().GetStopped() != nil {
		t.Fatalf("expected stopped cleared after resume, got %+v", resumed.Msg.GetThread().GetStopped())
	}
	if _, err := repo.ReportThread(t.Context(), "thr_b", core.ThreadReport{Kind: "ready", Headline: "ready headline", Summary: "ready summary"}, nil, core.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	completed, err := threadClient.CompleteThread(t.Context(), connect.NewRequest(&ariadnev1.CompleteThreadRequest{ThreadId: "thr_b"}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.GetThread().GetStage() != "done" {
		t.Fatalf("expected done stage, got %s", completed.Msg.GetThread().GetStage())
	}
}
