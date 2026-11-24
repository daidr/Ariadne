package transcript

import (
	"strings"
	"testing"
)

func TestDescribeFeedbackTweak(t *testing.T) {
	rendered := DescribeNote(map[string]any{
		"type":       "artifact-feedback",
		"artifactId": "art_proto",
		"title":      "交互原型板",
		"kind":       "whiteboard",
		"round":      float64(1),
		"items": []any{
			map[string]any{
				"kind": "tweak",
				"tweaks": []any{
					map[string]any{
						"nodeId":      "opt-a-prototype",
						"title":       "工作台头部",
						"optionId":    "opt-a",
						"optionTitle": "方案 A",
						"changes": []any{
							map[string]any{"key": "accent", "label": "主色", "value": "#355ecb", "previous": "#2563eb"},
							map[string]any{"key": "spacing", "label": "间距", "value": "12", "previous": "16", "unit": "px"},
						},
					},
				},
			},
		},
	}, func(string) string { return "" })

	for _, want := range []string{
		`Adjusted prototype "工作台头部" (opt-a-prototype) in option "方案 A" (opt-a)`,
		`主色 (accent) = #355ecb (was #2563eb)`,
		`间距 (spacing) = 12px (was 16px)`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered transcript to contain %q, got:\n%s", want, rendered)
		}
	}
}
