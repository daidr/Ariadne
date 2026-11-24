package runner

import (
	"fmt"
	"strings"
	"testing"
)

func TestReadFileRangeAndFormatting(t *testing.T) {
	t.Run("default reads 600 lines in one call with end-of-file marker", func(t *testing.T) {
		lines := make([]string, 600)
		for i := range lines {
			lines[i] = fmt.Sprintf("line-%d", i+1)
		}
		offset, limit := readFileRange(len(lines), args{})
		if offset != 1 || limit != readFileDefaultLimit {
			t.Fatalf("readFileRange = (%d, %d), want (1, %d)", offset, limit, readFileDefaultLimit)
		}
		out := formatNumberedRange(lines, offset, limit)
		if !strings.Contains(out, "  600\tline-600") {
			t.Fatalf("expected line 600 in output, got tail: %q", out[max(0, len(out)-120):])
		}
		if !strings.Contains(out, "[End of file — total 600 lines.]") {
			t.Fatalf("missing end-of-file marker: %q", out[max(0, len(out)-120):])
		}
	})

	t.Run("default stops at 2000 lines and hints continuation", func(t *testing.T) {
		lines := make([]string, 2500)
		for i := range lines {
			lines[i] = "x"
		}
		offset, limit := readFileRange(len(lines), args{})
		out := formatNumberedRange(lines, offset, limit)
		if !strings.Contains(out, " 2000\tx") || strings.Contains(out, " 2001\tx") {
			t.Fatalf("expected lines 1..2000 only")
		}
		if !strings.Contains(out, "[Lines 1–2000 of 2500. Continue at offset 2001.]") {
			t.Fatalf("unexpected continuation footer: %q", out[max(0, len(out)-120):])
		}
	})

	t.Run("explicit limit above 800 is respected", func(t *testing.T) {
		lines := make([]string, 1500)
		for i := range lines {
			lines[i] = "ok"
		}
		offset, limit := readFileRange(len(lines), args{"limit": float64(1500)})
		if limit != 1500 {
			t.Fatalf("limit = %d, want 1500", limit)
		}
		out := formatNumberedRange(lines, offset, limit)
		if !strings.Contains(out, " 1500\tok") || !strings.Contains(out, "[End of file — total 1500 lines.]") {
			t.Fatalf("expected all 1500 lines, got tail: %q", out[max(0, len(out)-120):])
		}
	})

	t.Run("50 KiB byte cap stops cleanly and notes cap", func(t *testing.T) {
		lines := make([]string, 500)
		payload := strings.Repeat("a", 250)
		for i := range lines {
			lines[i] = payload
		}
		out := formatNumberedRange(lines, 1, readFileDefaultLimit)
		idx := strings.LastIndex(out, "\n[Lines ")
		if idx < 0 {
			t.Fatalf("expected capped footer, got tail: %q", out[max(0, len(out)-160):])
		}
		body := out[:idx]
		footer := out[idx+1:]
		if len(body) > readFileMaxBytes {
			t.Fatalf("body length = %d, exceeds 50 KiB (%d)", len(body), readFileMaxBytes)
		}
		if !strings.Contains(footer, "(output capped at 50 KiB)") || !strings.Contains(footer, "Continue at offset ") {
			t.Fatalf("unexpected byte-cap footer: %q", footer)
		}
	})

	t.Run("offset past end of file reports total lines", func(t *testing.T) {
		lines := []string{"a", "b", "c"}
		offset, limit := readFileRange(len(lines), args{"offset": float64(50)})
		out := formatNumberedRange(lines, offset, limit)
		want := "[No lines returned: offset is past end of file — total 3 lines.]"
		if out != want {
			t.Fatalf("got %q, want %q", out, want)
		}
	})

	t.Run("tool description documents default limit and byte cap", func(t *testing.T) {
		spec := (&turn{}).readFileTool().spec
		if !strings.Contains(spec.Description, "2000 lines") || !strings.Contains(spec.Description, "50 KiB") {
			t.Fatalf("read_file description missing limits: %q", spec.Description)
		}
	})
}
