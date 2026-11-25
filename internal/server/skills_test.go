package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func TestConversationSkillsRPC(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte("# Review changes"), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), traceSeed()[:1]); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", SkillsDirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix)
	client := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor("test-token")))

	if _, err := unauth.ListSkills(t.Context(), connect.NewRequest(&ariadnev1.ListSkillsRequest{ConversationId: "coo_a"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", err)
	}

	resp, err := client.ListSkills(t.Context(), connect.NewRequest(&ariadnev1.ListSkillsRequest{ConversationId: "coo_a"}))
	if err != nil || len(resp.Msg.Skills) == 0 || !strings.Contains(resp.Msg.Skills[0].When, "Review changes") {
		t.Fatalf("skills result unexpected: resp=%+v err=%v", resp, err)
	}

	if _, err := client.ListSkills(t.Context(), connect.NewRequest(&ariadnev1.ListSkillsRequest{ConversationId: "missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for missing conversation, got %v", err)
	}
}
