package sdk

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceRequestPreservesAbsentAndNullBase(t *testing.T) {
	for _, input := range []string{`{"threadId":"one"}`, `{"threadId":"one","base":null}`, `{"threadId":"one","base":"main"}`} {
		var request WorkspaceRequest
		if err := json.Unmarshal([]byte(input), &request); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(request)
		if err != nil || string(encoded) != input {
			t.Fatal(input, string(encoded), err)
		}
	}
	var request WorkspaceRequest
	if json.Unmarshal([]byte(`{"threadId":"one","base":true}`), &request) == nil {
		t.Fatal("invalid base accepted")
	}
}
