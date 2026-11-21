package textutil

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var chatMarkdownParser = goldmark.DefaultParser()

// ChatMarkdown repairs double-escaped paragraph breaks in generated prose.
// Markdown code and link targets can intentionally contain these bytes.
func ChatMarkdown(content string) string {
	const escapedBreak = "\\n\\n"
	if !strings.Contains(content, escapedBreak) {
		return content
	}
	source := []byte(content)
	doc := chatMarkdownParser.Parse(text.NewReader(source))
	var result strings.Builder
	written := 0
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindCodeSpan, ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindHTMLBlock, ast.KindLink, ast.KindImage:
			return ast.WalkSkipChildren, nil
		}
		plain, ok := node.(*ast.Text)
		if !ok {
			return ast.WalkContinue, nil
		}
		start, stop := plain.Segment.Start, plain.Segment.Stop
		for search := start; search < stop; {
			offset := strings.Index(content[search:stop], escapedBreak)
			if offset < 0 {
				break
			}
			at := search + offset
			search = at + len(escapedBreak)
			for strings.HasPrefix(content[search:stop], `\n`) {
				search += 2
			}
			if at > 0 && content[at-1] == '\\' {
				continue
			}
			result.WriteString(content[written:at])
			result.WriteString(strings.Repeat("\n", (search-at)/2))
			written = search
		}
		return ast.WalkContinue, nil
	})
	if written == 0 {
		return content
	}
	result.WriteString(content[written:])
	return result.String()
}
