package collab

import (
	"encoding/json"
	"html"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func documentNumber(value any) float64 {
	var n float64
	switch value := normalizeJSON(value).(type) {
	case float64:
		n = value
	case string:
		n, _ = strconv.ParseFloat(value, 64)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return n
}

var documentScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
var documentColor = regexp.MustCompile(`^(gray|grey|brown|orange|yellow|green|blue|purple|pink|red|default)$`)
var escapedMarkdown = regexp.MustCompile(`[\\\x60*_\[\]<>~|]|&(?:#?\w+;)`)
var initialMarkdown = regexp.MustCompile(`^(\s*)(#{1,6}(?:\s|$)|(?:[-+]|[-=]+)(?:\s|$)|\d+[.)](?:\s|$))`)

func safeDocumentHref(href string) bool {
	stripped := strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, href)
	scheme := strings.ToLower(documentScheme.FindString(stripped))
	return stripped != "" && (scheme == "" || scheme == "http:" || scheme == "https:" || scheme == "mailto:" || scheme == "tel:")
}

func validDocumentColor(color string) bool { return documentColor.MatchString(color) }

func escapeDocumentText(words string) string {
	words = escapedMarkdown.ReplaceAllStringFunc(words, func(match string) string { return "\\" + match })
	lines := strings.Split(words, "\n")
	for i, line := range lines {
		parts := initialMarkdown.FindStringSubmatchIndex(line)
		if len(parts) == 0 {
			continue
		}
		at := parts[4]
		for at < len(line) && line[at] >= '0' && line[at] <= '9' {
			at++
		}
		lines[i] = line[:at] + "\\" + line[at:]
	}
	return strings.Join(lines, "\n")
}

func longRun(words string, mark byte) int {
	longest, current := 0, 0
	for i := 0; i < len(words); i++ {
		if words[i] != mark {
			current = 0
			continue
		}
		current++
		longest = max(longest, current)
	}
	return longest
}

func codeSpan(words string) string {
	fence := strings.Repeat("`", longRun(words, '`')+1)
	if strings.HasPrefix(words, "`") || strings.HasSuffix(words, "`") ||
		strings.HasPrefix(words, " ") && strings.HasSuffix(words, " ") && strings.TrimSpace(words) != "" {
		return fence + " " + words + " " + fence
	}
	return fence + words + fence
}

type inlineFormat struct{ name, value string }

func inlineFormats(run documentInline, asHTML bool) []inlineFormat {
	result := []inlineFormat{}
	if href, _ := toObject(run.marks["link"])["href"].(string); safeDocumentHref(href) {
		result = append(result, inlineFormat{"link", href})
	}
	for _, name := range []string{"textColor", "highlight"} {
		color, _ := toObject(run.marks[name])["value"].(string)
		if validDocumentColor(color) {
			result = append(result, inlineFormat{name, color})
		}
	}
	for _, name := range []string{"bold", "italic", "underline", "strike", "code"} {
		if run.marks[name] == true && (name != "code" || asHTML) {
			result = append(result, inlineFormat{name, ""})
		}
	}
	return result
}

func inlineTags(key inlineFormat, asHTML, delimiterOK bool) (string, string) {
	switch key.name {
	case "link":
		if asHTML {
			return `<a href="` + html.EscapeString(key.value) + `">`, "</a>"
		}
		href := key.value
		if strings.ContainsAny(href, " ()<>\t\n") {
			href = "<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(href) + ">"
		}
		return "[", "](" + href + ")"
	case "textColor":
		return `<span data-color="` + key.value + `">`, "</span>"
	case "highlight":
		return `<mark data-color="` + key.value + `">`, "</mark>"
	}
	tag, delimiter := "code", "`"
	switch key.name {
	case "bold":
		tag, delimiter = "strong", "**"
	case "italic":
		tag, delimiter = "em", "*"
	case "strike":
		tag, delimiter = "s", "~~"
	case "underline":
		tag, delimiter = "u", ""
	}
	if !asHTML && delimiterOK && delimiter != "" {
		return delimiter, delimiter
	}
	return "<" + tag + ">", "</" + tag + ">"
}

func flanks(inner, outer rune) bool {
	if inner == 0 || unicode.IsSpace(inner) {
		return false
	}
	if !unicode.IsPunct(inner) && !unicode.IsSymbol(inner) {
		return true
	}
	return outer == 0 || unicode.IsSpace(outer) || unicode.IsPunct(outer) || unicode.IsSymbol(outer)
}

func renderInlines(parts []documentInline, asHTML bool) string {
	type entry struct {
		key   inlineFormat
		close string
	}
	stack := []entry{}
	var out strings.Builder
	previous := rune(0)
	for i, part := range parts {
		keys := inlineFormats(part, asHTML)
		current := 0
		for current < len(stack) && slices.Contains(keys, stack[current].key) {
			current++
		}
		for len(stack) > current {
			out.WriteString(stack[len(stack)-1].close)
			stack = stack[:len(stack)-1]
		}
		if part.br {
			if asHTML {
				out.WriteString("<br>")
			} else {
				out.WriteString("\\\n")
			}
			previous = '\n'
			continue
		}
		words := part.text
		if len(keys) > current {
			trimmed := strings.TrimLeftFunc(words, unicode.IsSpace)
			out.WriteString(words[:len(words)-len(trimmed)])
			if len(trimmed) < len(words) {
				previous = ' '
			}
			words = trimmed
		}
		for _, key := range keys {
			if slices.ContainsFunc(stack, func(opened entry) bool { return opened.key == key }) {
				continue
			}
			before, after := rune(0), rune(0)
			if words != "" {
				before, _ = utf8.DecodeRuneInString(words)
			}
			tail := words
			for j := i + 1; j < len(parts); j++ {
				if !slices.Contains(inlineFormats(parts[j], asHTML), key) {
					if parts[j].br {
						after = '\n'
					} else if parts[j].text != "" {
						after, _ = utf8.DecodeRuneInString(parts[j].text)
					}
					break
				}
				if strings.TrimSpace(parts[j].text) != "" {
					tail = parts[j].text
				}
			}
			end, _ := utf8.DecodeLastRuneInString(strings.TrimSpace(tail))
			open, close := inlineTags(key, asHTML, flanks(before, previous) && flanks(end, after))
			out.WriteString(open)
			stack = append(stack, entry{key: key, close: close})
		}
		trail := ""
		var next []inlineFormat
		if i+1 < len(parts) {
			next = inlineFormats(parts[i+1], asHTML)
		}
		closeAt := len(stack)
		for at, opened := range stack {
			if !slices.Contains(next, opened.key) {
				closeAt = at
				break
			}
		}
		if !asHTML && closeAt < len(stack) {
			trimmed := strings.TrimRightFunc(words, unicode.IsSpace)
			trail, words = words[len(trimmed):], trimmed
		}
		switch {
		case asHTML:
			out.WriteString(html.EscapeString(words))
		case words != "" && part.marks["code"] == true:
			out.WriteString(codeSpan(words))
		default:
			out.WriteString(escapeDocumentText(words))
		}
		if trail != "" {
			for len(stack) > closeAt {
				out.WriteString(stack[len(stack)-1].close)
				stack = stack[:len(stack)-1]
			}
			out.WriteString(trail)
		}
		if part.text != "" {
			previous, _ = utf8.DecodeLastRuneInString(part.text)
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		out.WriteString(stack[i].close)
	}
	return out.String()
}

func renderDocument(blocks []documentBlock) string {
	text := renderDocumentBlocks(blocks)
	if text != "" {
		return text + "\n"
	}
	return text
}

func renderDocumentBlocks(blocks []documentBlock) string {
	var output strings.Builder
	previous, lastWritten := "", ""
	index := 0
	for _, block := range blocks {
		if block.kind == previous {
			index++
		} else {
			index = 1
		}
		previous = block.kind
		next := renderDocumentBlock(block, index)
		if next == "" {
			continue
		}
		if output.Len() != 0 {
			group := listGroup(block.kind)
			if group != "" && group == listGroup(lastWritten) {
				output.WriteByte('\n')
			} else {
				output.WriteString("\n\n")
			}
		}
		output.WriteString(next)
		lastWritten = block.kind
	}
	return output.String()
}

func listGroup(kind string) string {
	switch kind {
	case "bulletItem":
		return "ul"
	case "checkItem":
		return "task-list"
	case "numberedItem":
		return "ol"
	default:
		return ""
	}
}

func combineMarkdown(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

func indentDocument(value, first, rest string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		prefix := rest
		if i == 0 {
			prefix = first
		}
		if i != 0 && lines[i] == "" {
			prefix = strings.TrimRight(prefix, " ")
		}
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func renderDocumentBlock(block documentBlock, index int) string {
	inline := renderInlines(block.inline, false)
	children := renderDocumentBlocks(block.children)
	switch block.kind {
	case "heading":
		level := min(3, max(1, int(documentNumber(block.props["level"]))))
		return combineMarkdown(strings.TrimRight(strings.Repeat("#", level)+" "+inline, " "), children)
	case "quote":
		return indentDocument(combineMarkdown(inline, children), "> ", "> ")
	case "callout":
		label := "NOTE"
		switch block.props["tone"] {
		case "tip":
			label = "TIP"
		case "warning":
			label = "WARNING"
		case "danger":
			label = "CAUTION"
		}
		head := "[!" + label + "]"
		if inline != "" {
			head += "\n" + inline
		}
		return indentDocument(combineMarkdown(head, children), "> ", "> ")
	case "numberedItem", "bulletItem", "checkItem":
		prefix := "- "
		switch block.kind {
		case "numberedItem":
			prefix = strconv.Itoa(index) + ". "
		case "checkItem":
			prefix = "- [ ] "
			if block.props["checked"] == true {
				prefix = "- [x] "
			}
		}
		body := inline
		if children != "" {
			body += "\n" + children
		}
		return indentDocument(body, prefix, strings.Repeat(" ", len(prefix)))
	case "divider":
		return combineMarkdown("---", children)
	case "codeBlock":
		var text strings.Builder
		for _, segment := range block.inline {
			if segment.br {
				text.WriteByte('\n')
			} else {
				text.WriteString(segment.text)
			}
		}
		lang, _ := block.props["language"].(string)
		if value := strings.Fields(lang); len(value) > 0 && !strings.ContainsRune(value[0], '`') {
			lang = value[0]
		} else {
			lang = ""
		}
		fence := strings.Repeat("`", max(3, longRun(text.String(), '`')+1))
		return combineMarkdown(fence+lang+"\n"+text.String()+"\n"+fence, children)
	case "whiteboard":
		data, _ := json.Marshal(struct {
			Height int `json:"height"`
			Board  any `json:"board"`
		}{boardHeight(block.props["height"]), block.props["board"]})
		return combineMarkdown("```dou-whiteboard\n"+string(data)+"\n```", children)
	case "pending":
		return combineMarkdown("<pending>"+renderInlines(block.inline, true)+"</pending>", children)
	case "ask":
		return combineMarkdown(renderAskBlock(block.props), children)
	case "ref":
		return combineMarkdown(renderRefBlock(block.props), children)
	case "toggle":
		summary := "<details>\n<summary>" + renderInlines(block.inline, true) + "</summary>\n\n"
		if children != "" {
			summary += children + "\n\n"
		}
		return summary + "</details>"
	case "table":
		return combineMarkdown(writeMarkdownTable(block.table), children)
	case "columns", "column":
		return children
	default:
		return combineMarkdown(inline, children)
	}
}
