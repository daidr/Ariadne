// Package secrets removes configured credentials from text leaving an adapter.
package secrets

import (
	"slices"
	"strings"
)

func Redact(text string, values []string) string {
	sorted := append([]string(nil), values...)
	slices.SortStableFunc(sorted, func(a, b string) int { return len(b) - len(a) })
	for _, value := range sorted {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[secret]")
		}
	}
	return text
}
