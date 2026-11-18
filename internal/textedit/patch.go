package textedit

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type PatchFile struct {
	Action, Path, Target, Text string
	hunks                      []hunk
}

type lineChange struct {
	mark byte
	text string
}

type hunk struct {
	anchor string
	atEnd  bool
	lines  []lineChange
}

func header(line string) (action, path string) {
	for _, action := range []string{"Add", "Update", "Delete"} {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "*** "+action+" File: "); ok {
			return action, strings.TrimSpace(name)
		}
	}
	return "", ""
}

func ParsePatch(raw string) ([]PatchFile, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("the patch is too large")
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	trimmed := func(marker string) func(string) bool {
		return func(line string) bool { return strings.TrimSpace(line) == marker }
	}
	if at := slices.IndexFunc(lines, trimmed("*** Begin Patch")); at >= 0 {
		lines = lines[at+1:]
	}
	if at := slices.IndexFunc(lines, trimmed("*** End Patch")); at >= 0 {
		lines = lines[:at]
	}
	var files []PatchFile
	for at := 0; at < len(lines); {
		if strings.TrimSpace(lines[at]) == "" {
			at++
			continue
		}
		action, path := header(lines[at])
		if path == "" {
			return nil, fmt.Errorf("expected a patch file header at line %d", at+1)
		}
		at++
		end := at
		for ; end < len(lines); end++ {
			if op, _ := header(lines[end]); op != "" {
				break
			}
		}
		edit := PatchFile{Action: action, Path: path}
		body := lines[at:end]
		var err error
		switch action {
		case "Add":
			added := make([]string, 0, len(body))
			for _, line := range body {
				if words, ok := strings.CutPrefix(line, "+"); ok {
					added = append(added, words)
				} else if strings.TrimSpace(line) != "" {
					return nil, fmt.Errorf("added lines of %s must start with +", path)
				}
			}
			for len(added) > 0 && added[len(added)-1] == "" {
				added = added[:len(added)-1]
			}
			if len(added) > 0 {
				edit.Text = strings.Join(added, "\n") + "\n"
			}
		case "Update":
			if len(body) > 0 {
				if target, found := strings.CutPrefix(strings.TrimSpace(body[0]), "*** Move to: "); found {
					edit.Target = strings.TrimSpace(target)
					body = body[1:]
				}
			}
			edit.hunks, err = parseHunks(body)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if edit.Target == "" && len(edit.hunks) == 0 {
				return nil, fmt.Errorf("%s: update has no changes", path)
			}
		case "Delete":
			for _, line := range body {
				if strings.TrimSpace(line) != "" {
					return nil, fmt.Errorf("%s: a deletion has no body", path)
				}
			}
		}
		files = append(files, edit)
		if len(files) > 100 {
			return nil, errors.New("too many file operations in a patch")
		}
		at = end
	}
	if len(files) == 0 {
		return nil, errors.New("the patch changes no files")
	}
	return files, nil
}

func parseHunks(body []string) ([]hunk, error) {
	hunks := []hunk{{}}
	for i, line := range body {
		if anchor, ok := strings.CutPrefix(line, "@@"); ok {
			hunks = append(hunks, hunk{anchor: strings.TrimSpace(anchor)})
			continue
		}
		last := &hunks[len(hunks)-1]
		if strings.TrimSpace(line) == "*** End of File" {
			last.atEnd = true
		} else if line == "" {
			last.lines = append(last.lines, lineChange{' ', ""})
		} else if strings.ContainsRune(" +-", rune(line[0])) {
			last.lines = append(last.lines, lineChange{line[0], line[1:]})
		} else {
			return nil, fmt.Errorf("hunk line %d must begin with a space, - or +", i+1)
		}
	}
	for i := range hunks {
		lines := hunks[i].lines
		for len(lines) > 0 && lines[len(lines)-1] == (lineChange{' ', ""}) {
			lines = lines[:len(lines)-1]
		}
		hunks[i].lines = lines
	}
	hunks = slices.DeleteFunc(hunks, func(value hunk) bool { return len(value.lines) == 0 })
	if len(hunks) > 128 {
		return nil, errors.New("too many hunks in one file")
	}
	return hunks, nil
}

var typographic = strings.NewReplacer("‘", "'", "’", "'", "‚", "'", "‛", "'", "“", `"`, "”", `"`, "„", `"`, "‟", `"`, "‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-")

func seek(lines, want []string, from int, end bool) int {
	for _, normalize := range []func(string) string{
		func(s string) string { return s }, func(s string) string { return strings.TrimRight(s, " \t\r") }, strings.TrimSpace,
		func(s string) string { return typographic.Replace(strings.TrimSpace(s)) },
	} {
		pattern := make([]string, len(want))
		for i, line := range want {
			pattern[i] = normalize(line)
		}
		start := len(lines) - len(want)
		if start < from {
			return -1
		}
		if end && slices.EqualFunc(lines[start:], pattern, func(a, b string) bool { return normalize(a) == b }) {
			return start
		}
		search := make([]string, len(lines)-from)
		for i, line := range lines[from:] {
			search[i] = normalize(line)
		}
		if matches := matchesOf(search, pattern, true); len(matches) > 0 {
			return from + matches[0]
		}
	}
	return -1
}

func (file PatchFile) Apply(content string, limit int) (string, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	terminated := content == "" || strings.HasSuffix(content, "\n")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if terminated {
		lines = lines[:len(lines)-1]
	}
	cursor := 0
	for index, hunk := range file.hunks {
		if hunk.anchor != "" {
			at := seek(lines, []string{hunk.anchor}, cursor, false)
			if at < 0 {
				return "", fmt.Errorf("%s hunk %d could not find its @@ line", file.Path, index+1)
			}
			cursor = at + 1
		}
		var old, newLines []string
		for _, line := range hunk.lines {
			if line.mark != '+' {
				old = append(old, line.text)
			}
			if line.mark != '-' {
				newLines = append(newLines, line.text)
			}
		}
		at := len(lines)
		if len(old) > 0 {
			at = seek(lines, old, cursor, hunk.atEnd)
			if at < 0 {
				return "", fmt.Errorf("%s hunk %d could not find these lines:\n%s", file.Path, index+1, strings.Join(old[:min(len(old), 6)], "\n"))
			}
		} else if hunk.anchor != "" && !hunk.atEnd {
			at = cursor
		}
		lines = slices.Replace(lines, at, at+len(old), newLines...)
		cursor = at + len(newLines)
	}
	output := strings.Join(lines, newline)
	if len(lines) > 0 && terminated {
		output += newline
	}
	if len(output) > limit {
		return "", errors.New("the patched file is too large")
	}
	return output, nil
}
