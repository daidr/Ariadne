// Package legacytest runs compatibility fixtures against the pinned Bun
// checkout outside the application source tree.
package legacytest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func Command(t *testing.T, bun, fixture string, args ...string) *exec.Cmd {
	t.Helper()
	return exec.CommandContext(t.Context(), bun, append([]string{Path(t, fixture)}, args...)...)
}

func Path(t *testing.T, fixture string) string {
	t.Helper()
	root := os.Getenv("ARIADNE_LEGACY_ROOT")
	if root == "" {
		current, _ := os.Getwd()
		for current != "" {
			if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
				root = filepath.Join(current, "dist", "legacy-oracle")
				break
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(absolute, filepath.FromSlash(fixture))
	if _, err := os.Stat(script); err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatalf("legacy oracle fixture %s is missing: %v", fixture, err)
		}
		t.Skip("legacy oracle is optional. Prepare it with go run ./tools/legacyoracle")
	}
	return script
}
