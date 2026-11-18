package core

import (
	"slices"
	"strings"
)

type diffLine struct {
	mark byte
	text string
}

func changedLines(a, b []string) (start, end int, lines []diffLine, ok bool) {
	for start < len(a) && start < len(b) && a[start] == b[start] {
		start++
	}
	endA, endB := len(a), len(b)
	for endA > start && endB > start && a[endA-1] == b[endB-1] {
		endA--
		endB--
	}
	midA, midB := a[start:endA], b[start:endB]
	n, m := len(midA), len(midB)
	if n > 4000 || m > 4000 || n*m > 4_000_000 {
		return 0, 0, nil, false
	}
	table := make([]uint32, (n+1)*(m+1))
	at := func(i, j int) int { return i*(m+1) + j }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if midA[i] == midB[j] {
				table[at(i, j)] = table[at(i+1, j+1)] + 1
			} else {
				table[at(i, j)] = max(table[at(i+1, j)], table[at(i, j+1)])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && midA[i] == midB[j] {
			lines = append(lines, diffLine{' ', midA[i]})
			i++
			j++
		} else if i < n && (j == m || table[at(i+1, j)] >= table[at(i, j+1)]) {
			lines = append(lines, diffLine{'-', midA[i]})
			i++
		} else {
			lines = append(lines, diffLine{'+', midB[j]})
			j++
		}
	}
	return start, endA, lines, true
}

// LineDiff keeps one line of context around each change and caps the LCS table.
// A nil result means no change or an input too large to compare.
func LineDiff(before, after string, maxChars int) *string {
	if before == after {
		return nil
	}
	a := strings.Split(before, "\n")
	start, endA, lines, ok := changedLines(a, strings.Split(after, "\n"))
	if !ok {
		return nil
	}
	if start > 0 {
		lines = slices.Insert(lines, 0, diffLine{' ', a[start-1]})
	}
	if endA < len(a) {
		lines = append(lines, diffLine{' ', a[endA]})
	}
	out := []string{}
	if start > 1 {
		out = append(out, "…")
	}
	length := 0
	for index, item := range lines {
		keep := item.mark != ' ' || index > 0 && lines[index-1].mark != ' ' || index+1 < len(lines) && lines[index+1].mark != ' '
		if !keep {
			if len(out) == 0 || out[len(out)-1] != "…" {
				out = append(out, "…")
			}
			continue
		}
		text := string(item.mark) + item.text
		if length+utf16Length(text) > maxChars {
			out = append(out, "…")
			result := strings.Join(out, "\n")
			return &result
		}
		length += utf16Length(text) + 1
		out = append(out, text)
	}
	if endA+1 < len(a) && (len(out) == 0 || out[len(out)-1] != "…") {
		out = append(out, "…")
	}
	result := strings.Join(out, "\n")
	return &result
}

type lineEdit struct {
	start, end int
	lines      []string
}

func lineEdits(base, after []string) ([]lineEdit, bool) {
	offset, _, diff, ok := changedLines(base, after)
	var edits []lineEdit
	inEdit := false
	for _, line := range diff {
		if line.mark == ' ' {
			inEdit = false
			offset++
			continue
		}
		if !inEdit {
			edits = append(edits, lineEdit{start: offset, end: offset})
			inEdit = true
		}
		edit := &edits[len(edits)-1]
		if line.mark == '-' {
			edit.end++
			offset++
		} else {
			edit.lines = append(edit.lines, line.text)
		}
	}
	return edits, ok
}

// MergeText applies nonoverlapping edits and reports a conflict when the same
// lines were changed differently or comparing the versions exceeds the budget.
func MergeText(base, current, proposed string) (string, bool) {
	switch {
	case base == current:
		return proposed, true
	case base == proposed || current == proposed:
		return current, true
	}
	original := strings.Split(base, "\n")
	left, okLeft := lineEdits(original, strings.Split(current, "\n"))
	right, okRight := lineEdits(original, strings.Split(proposed, "\n"))
	if !okLeft || !okRight {
		return "", false
	}
	edits := append(left, right...)
	slices.SortStableFunc(edits, func(a, b lineEdit) int {
		if a.start == b.start {
			return a.end - b.end
		}
		return a.start - b.start
	})
	var result []string
	from := 0
	for index, edit := range edits {
		if index > 0 {
			prior := edits[index-1]
			if prior.start == edit.start && prior.end == edit.end && slices.Equal(prior.lines, edit.lines) {
				continue
			}
			if edit.start < from || edit.start == prior.start && prior.end == edit.end {
				return "", false
			}
		}
		result = append(result, original[from:edit.start]...)
		result = append(result, edit.lines...)
		from = edit.end
	}
	result = append(result, original[from:]...)
	return strings.Join(result, "\n"), true
}
