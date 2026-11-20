package textutil

import (
	"strings"
	"unicode/utf8"
)

func ScreenText(text string) string {
	if strings.ContainsRune(text, '\x1b') {
		var escapes TerminalEscapes
		text = string(escapes.Strip([]byte(text)))
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "\r")
		lines[i] = ""
		for j := len(parts) - 1; j >= 0; j-- {
			if parts[j] != "" {
				lines[i] = parts[j]
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// HeadUnits copies a prefix measured in JavaScript's UTF-16 units.
func HeadUnits(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	used := 0
	for offset, r := range text {
		units := 1
		if r > 0xffff {
			units = 2
		}
		if used+units > limit {
			return text[:offset] + "\ufffd"
		}
		used += units
		if used == limit {
			_, size := utf8.DecodeRuneInString(text[offset:])
			return strings.Clone(text[:offset+size])
		}
	}
	return strings.Clone(text)
}

// TailUnits bounds retained terminal output in JavaScript's UTF-16 units.
func TailUnits(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	used := 0
	for offset := len(text); offset > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:offset])
		units := 1
		if r > 0xffff {
			units = 2
		}
		if used+units > limit {
			return "\ufffd" + text[offset:]
		}
		offset -= size
		used += units
		if used == limit {
			return strings.Clone(text[offset:])
		}
	}
	return strings.Clone(text)
}
