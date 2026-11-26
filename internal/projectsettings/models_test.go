package projectsettings

import (
	"testing"
)

func TestParseAndStoredProjectModels(t *testing.T) {
	t.Parallel()

	parsed, err := Parse(map[string]any{
		"models": map[string]any{
			"thread":      map[string]any{"model": "m-opus", "effort": "high"},
			"coordinator": map[string]any{"model": "m-sonnet", "effort": nil},
			"research":    map[string]any{"model": "m-haiku", "effort": "low"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Parse valid models: %v", err)
	}
	if parsed.Models.Thread != (ModelRole{Model: "m-opus", Effort: "high"}) {
		t.Fatalf("thread = %+v", parsed.Models.Thread)
	}
	if parsed.Models.Coordinator != (ModelRole{Model: "m-sonnet", Effort: ""}) {
		t.Fatalf("coordinator = %+v", parsed.Models.Coordinator)
	}
	if parsed.Models.Research != (ModelRole{Model: "m-haiku", Effort: "low"}) {
		t.Fatalf("research = %+v", parsed.Models.Research)
	}

	// Legacy stored settings without "models" decode with empty overrides.
	legacy, err := Stored(`{"instructions":"hello"}`)
	if err != nil {
		t.Fatalf("Stored legacy: %v", err)
	}
	if legacy.Models != (Models{}) {
		t.Fatalf("legacy models = %+v", legacy.Models)
	}

	// Reject effort without model.
	if _, err := Parse(map[string]any{
		"models": map[string]any{
			"thread": map[string]any{"model": nil, "effort": "high"},
		},
	}, nil); err == nil {
		t.Fatal("expected error for effort without model")
	}

	// Reject invalid effort value.
	if _, err := Parse(map[string]any{
		"models": map[string]any{
			"coordinator": map[string]any{"model": "m-1", "effort": "turbo"},
		},
	}, nil); err == nil {
		t.Fatal("expected error for invalid effort")
	}

	// Reject whitespace in model id.
	if _, err := Parse(map[string]any{
		"models": map[string]any{
			"research": map[string]any{"model": "bad id"},
		},
	}, nil); err == nil {
		t.Fatal("expected error for whitespace in model id")
	}
}
