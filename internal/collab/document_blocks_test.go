package collab

import (
	"strings"
	"testing"
)

func TestDocumentAskAndRefBlocksRoundTripAndStableIDs(t *testing.T) {
	t.Parallel()
	doc, err := Load("document", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Destroy()

	initial := strings.Join([]string{
		"# 架构评审",
		`<ask id="ask-auth" question="登录态采用哪种方案？" options='[{"id":"jwt","label":"JWT","recommended":true},{"id":"session","label":"Session"}]'></ask>`,
		`<ref artifact="artifact:art_board_1" group="grp-nav" label="导航对比"></ref>`,
	}, "\n\n")

	if err := UpdateDocument(doc, initial, "agent"); err != nil {
		t.Fatalf("UpdateDocument initial: %v", err)
	}
	keysBefore := orderedKeys(doc.GetMap("document"), "order")
	if len(keysBefore) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(keysBefore))
	}

	rendered, err := ReadDocument(doc)
	if err != nil {
		t.Fatalf("ReadDocument initial: %v", err)
	}
	for _, want := range []string{
		"```ask\n",
		`"id":"ask-auth"`,
		`"question":"登录态采用哪种方案？"`,
		"```ref\n",
		`"artifactId":"art_board_1"`,
		`"groupId":"grp-nav"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered document to contain %q, got:\n%s", want, rendered)
		}
	}

	// Update ask block with user answer + confirmed=true and ensure Yjs block ID is preserved.
	updated := strings.Join([]string{
		"# 架构评审",
		"```ask\n{\"id\":\"ask-auth\",\"question\":\"登录态采用哪种方案？\",\"pick\":\"one\",\"options\":[{\"id\":\"jwt\",\"label\":\"JWT\",\"recommended\":true},{\"id\":\"session\",\"label\":\"Session\"}],\"selected\":[\"jwt\"],\"note\":\"优先无状态\",\"confirmed\":true}\n```",
		"```ref\n{\"artifactId\":\"art_board_1\",\"groupId\":\"grp-nav\",\"label\":\"导航对比（更新）\"}\n```",
	}, "\n\n")

	if err := UpdateDocument(doc, updated, "agent"); err != nil {
		t.Fatalf("UpdateDocument updated: %v", err)
	}
	keysAfter := orderedKeys(doc.GetMap("document"), "order")
	if strings.Join(keysAfter, ",") != strings.Join(keysBefore, ",") {
		t.Fatalf("expected stable block IDs across ask/ref updates: before=%v after=%v", keysBefore, keysAfter)
	}

	rendered2, err := ReadDocument(doc)
	if err != nil {
		t.Fatalf("ReadDocument updated: %v", err)
	}
	for _, want := range []string{
		`"selected":["jwt"]`,
		`"note":"优先无状态"`,
		`"confirmed":true`,
		`"label":"导航对比（更新）"`,
	} {
		if !strings.Contains(rendered2, want) {
			t.Fatalf("expected updated document to contain %q, got:\n%s", want, rendered2)
		}
	}
}
