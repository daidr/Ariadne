package textedit

import (
	"errors"
	"fmt"
	"strings"
)

// Replace applies an exact match, then tries matching whole lines with
// indentation and trailing whitespace ignored.
func Replace(content, old, replacement string, all bool, limit int) (string, int, error) {
	if old == "" {
		return "", 0, errors.New("old text is required")
	}
	count := strings.Count(content, old)
	if count > 0 {
		if count > 1 && !all {
			return "", 0, fmt.Errorf("old text occurs %d times. Include more surrounding text", count)
		}
		if int64(len(content))+int64(count)*int64(len(replacement)-len(old)) > int64(limit) {
			return "", 0, errors.New("the edited content is too large")
		}
		return strings.ReplaceAll(content, old, replacement), count, nil
	}
	snippet := strings.Split(strings.ReplaceAll(strings.TrimSpace(old), "\r\n", "\n"), "\n")
	for i := range snippet {
		snippet[i] = strings.TrimSpace(snippet[i])
	}
	if len(snippet) == 1 && snippet[0] == "" {
		return "", 0, errors.New("the old text was not found")
	}
	var lines []string
	starts := []int{0}
	for line := range strings.SplitSeq(content, "\n") {
		lines = append(lines, strings.TrimSpace(line))
		starts = append(starts, starts[len(starts)-1]+len(line)+1)
	}
	matches := matchesOf(lines, snippet, false)
	if len(matches) == 0 {
		return "", 0, errors.New("the old text was not found. Read the current lines and copy them into the edit")
	}
	if len(matches) > 1 && !all {
		return "", 0, fmt.Errorf("old text matches %d places after trimming indentation. Include more surrounding text", len(matches))
	}
	replacement = strings.TrimSuffix(strings.TrimSuffix(replacement, "\n"), "\r")
	var result strings.Builder
	from := 0
	for _, match := range matches {
		start, end := starts[match], starts[match+len(snippet)]-1
		if end > 0 && end <= len(content) && content[end-1] == '\r' {
			end--
		}
		if result.Len()+start-from+len(replacement) > limit {
			return "", 0, errors.New("the edited content is too large")
		}
		result.WriteString(content[from:start])
		result.WriteString(replacement)
		from = end
	}
	if result.Len()+len(content)-from > limit {
		return "", 0, errors.New("the edited content is too large")
	}
	result.WriteString(content[from:])
	return result.String(), len(matches), nil
}

func matchesOf(lines, pattern []string, first bool) []int {
	if len(pattern) == 0 {
		return nil
	}
	backtrack := make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); index++ {
		for matched > 0 && pattern[index] != pattern[matched] {
			matched = backtrack[matched-1]
		}
		if pattern[index] == pattern[matched] {
			matched++
		}
		backtrack[index] = matched
	}
	var result []int
	matched := 0
	for index, line := range lines {
		for matched > 0 && line != pattern[matched] {
			matched = backtrack[matched-1]
		}
		if line == pattern[matched] {
			matched++
			if matched == len(pattern) {
				result = append(result, index-matched+1)
				if first {
					return result
				}
				matched = 0
			}
		}
	}
	return result
}
