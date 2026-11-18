package collab

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"golang.org/x/net/html"
)

const maxDocumentBytes = 8 << 20
const maxDocumentDepth = 96

type documentBlock struct {
	id, kind string
	props    contentObject
	inline   []documentInline
	children []documentBlock
	table    *documentTable
}

type documentInline struct {
	text  string
	br    bool
	marks contentObject
}

var editorMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify, extension.TaskList),
	// HTML remains inside the parser. Only editor blocks and marks listed below are stored.
	goldmark.WithRendererOptions(goldhtml.WithUnsafe()),
)

func parseMarkdown(source string) ([]documentBlock, error) {
	if len(source) > maxDocumentBytes {
		return nil, errors.New("document is too large")
	}
	var rendered bytes.Buffer
	if err := editorMarkdown.Convert([]byte(source), &rendered); err != nil {
		return nil, err
	}
	page, err := html.Parse(&rendered)
	if err != nil {
		return nil, err
	}
	for node := range page.Descendants() {
		if node.Type == html.ElementNode && node.Data == "body" {
			return parseDocumentNodes(node.FirstChild, 0)
		}
	}
	return nil, nil
}

// ContainsDocumentQuote matches the text as shown in the editor.
func ContainsDocumentQuote(source, quote string) bool {
	sourceBlocks, err := parseMarkdown(source)
	if err != nil {
		return false
	}
	var visit func([]documentBlock, *strings.Builder)
	visit = func(blocks []documentBlock, value *strings.Builder) {
		for _, block := range blocks {
			for _, item := range block.inline {
				if item.br {
					value.WriteByte(' ')
				}
				value.WriteString(item.text)
			}
			value.WriteByte(' ')
			visit(block.children, value)
		}
	}
	var visible strings.Builder
	visit(sourceBlocks, &visible)
	return strings.Contains(strings.Join(strings.Fields(visible.String()), " "), strings.Join(strings.Fields(quote), " ")) ||
		strings.Contains(source, quote)
}

func parseDocumentNodes(at *html.Node, depth int) ([]documentBlock, error) {
	if depth > maxDocumentDepth {
		return nil, errors.New("document has too many nested blocks")
	}
	var result []documentBlock
	var loose []documentInline
	flush := func() {
		if content := trimInlines(loose); len(content) > 0 {
			result = append(result, documentBlock{kind: "paragraph", props: contentObject{"align": nil}, inline: content})
		}
		loose = nil
	}
	for ; at != nil; at = at.NextSibling {
		switch at.Type {
		case html.TextNode:
			loose = append(loose, readInlines(at, nil, 0)...)
		case html.ElementNode:
			blocks, recognized, err := parseDocumentBlock(at, depth)
			if err != nil {
				return nil, err
			}
			if recognized {
				flush()
				result = append(result, blocks...)
			} else {
				loose = append(loose, readInlines(at, nil, 0)...)
			}
		}
	}
	flush()
	return result, nil
}

var alertMarker = regexp.MustCompile(`(?i)^\s*\[!(NOTE|IMPORTANT|TIP|WARNING|CAUTION)\][ \t]*\n?`)
var alertTones = map[string]string{"NOTE": "info", "IMPORTANT": "info", "TIP": "tip", "WARNING": "warning", "CAUTION": "danger"}

func parseDocumentBlock(node *html.Node, depth int) ([]documentBlock, bool, error) {
	block := documentBlock{props: contentObject{}}
	content := node
	switch node.Data {
	case "script", "style", "iframe", "noscript":
		return nil, true, nil
	case "p":
		if child := onlyElement(node); child != nil && child.Data == "pending" {
			return parseDocumentBlock(child, depth)
		}
		block.kind = "paragraph"
		block.props["align"] = validAlign(styleValue(node, "text-align"))
	case "h1", "h2", "h3", "h4", "h5", "h6":
		block.kind = "heading"
		block.props["level"] = min(3, int(node.Data[1]-'0'))
		block.props["align"] = validAlign(styleValue(node, "text-align"))
	case "pending":
		block.kind = "pending"
	case "hr":
		block.kind, content = "divider", nil
	case "pre":
		return parseCodeBlock(node)
	case "table":
		table, err := parseDocumentTable(node)
		block.kind, block.table, content = "table", table, nil
		return []documentBlock{block}, true, err
	case "ul", "ol":
		blocks, err := parseList(node, depth)
		return blocks, true, err
	case "blockquote", "aside", "details":
		var err error
		block, err = parseContainer(node, depth)
		return []documentBlock{block}, true, err
	case "section", "article", "main", "div", "figure":
		switch attribute(node, "data-content-type") {
		case "columns", "column":
			block.kind = attribute(node, "data-content-type")
			if block.kind == "column" {
				width, err := strconv.ParseFloat(attribute(node, "data-width"), 64)
				block.props["width"] = nil
				if err == nil && width > 0 {
					block.props["width"] = min(width, 100)
				}
			}
			var err error
			block.children, err = parseDocumentNodes(node.FirstChild, depth+1)
			return []documentBlock{block}, true, err
		case "whiteboard":
			board, err := parseDocumentBoard(attribute(node, "data-prop-board"), attribute(node, "data-prop-height"))
			return []documentBlock{board}, true, err
		default:
			blocks, err := parseDocumentNodes(node.FirstChild, depth+1)
			return blocks, true, err
		}
	default:
		return nil, false, nil
	}
	if content != nil {
		block.inline = readChildrenInlines(content)
	}
	return []documentBlock{block}, true, nil
}

func onlyElement(node *html.Node) *html.Node {
	var result *html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.CommentNode || child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
			continue
		}
		if child.Type != html.ElementNode || result != nil {
			return nil
		}
		result = child
	}
	return result
}

func parseCodeBlock(node *html.Node) ([]documentBlock, bool, error) {
	lang := ""
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "code" {
			for _, class := range strings.Fields(attribute(child, "class")) {
				if strings.HasPrefix(class, "language-") {
					lang = strings.TrimPrefix(class, "language-")
					break
				}
			}
			break
		}
	}
	words := strings.TrimSuffix(nodeText(node), "\n")
	if lang == "dou-whiteboard" {
		var input struct {
			Height any             `json:"height"`
			Board  json.RawMessage `json:"board"`
		}
		if err := json.Unmarshal([]byte(words), &input); err == nil && len(input.Board) > 0 {
			board, err := parseDocumentBoard(string(input.Board), "")
			if err == nil {
				board.props["height"] = boardHeight(input.Height)
				return []documentBlock{board}, true, nil
			}
		}
	}
	var language any
	if lang != "" {
		language = lang
	}
	return []documentBlock{{kind: "codeBlock", props: contentObject{"language": language}, inline: []documentInline{{text: words}}}}, true, nil
}

func parseDocumentBoard(raw, height string) (documentBlock, error) {
	board, err := parseWhiteboard(raw)
	if err != nil {
		return documentBlock{}, err
	}
	size, _ := strconv.ParseFloat(height, 64)
	return documentBlock{kind: "whiteboard", props: contentObject{"height": boardHeight(size), "board": contentObject{
		"version": 1, "nodes": board.Nodes, "edges": board.Edges,
	}}}, nil
}

func boardHeight(value any) int {
	n := documentNumber(value)
	if n <= 0 {
		return 360
	}
	return min(max(int(n+0.5), 160), 1200)
}

func parseList(node *html.Node, depth int) ([]documentBlock, error) {
	var blocks []documentBlock
	for item := node.FirstChild; item != nil; item = item.NextSibling {
		if item.Type != html.ElementNode || item.Data != "li" {
			continue
		}
		content, kids, task, err := splitBlockContainer(item, depth)
		if err != nil {
			return nil, err
		}
		kind, props := "bulletItem", contentObject{}
		if node.Data == "ol" {
			kind = "numberedItem"
		}
		if task != nil {
			kind, props["checked"] = "checkItem", *task
		}
		blocks = append(blocks, documentBlock{kind: kind, props: props, inline: content, children: kids})
	}
	return blocks, nil
}

func parseContainer(node *html.Node, depth int) (documentBlock, error) {
	content, kids, _, err := splitBlockContainer(node, depth)
	if err != nil {
		return documentBlock{}, err
	}
	kind, props := "quote", contentObject{"align": validAlign(styleValue(node, "text-align"))}
	if node.Data == "details" {
		kind, props = "toggle", contentObject{}
	}
	tone := attribute(node, "data-callout")
	if node.Data == "blockquote" && len(content) > 0 {
		if match := alertMarker.FindStringSubmatch(content[0].text); match != nil {
			tone = alertTones[strings.ToUpper(match[1])]
			content[0].text = strings.TrimLeftFunc(strings.TrimPrefix(content[0].text, match[0]), unicode.IsSpace)
			content = trimInlines(content)
			if len(content) > 0 && content[0].br {
				content = trimInlines(content[1:])
			}
		}
	}
	if tone != "" || node.Data == "aside" {
		switch tone {
		case "tip", "warning", "danger":
		default:
			tone = "info"
		}
		kind, props = "callout", contentObject{"tone": tone}
	}
	return documentBlock{kind: kind, props: props, inline: content, children: kids}, nil
}

func splitBlockContainer(node *html.Node, depth int) ([]documentInline, []documentBlock, *bool, error) {
	if depth >= maxDocumentDepth {
		return nil, nil, nil, errors.New("document has too many nested blocks")
	}
	var content []documentInline
	var kids []documentBlock
	var task *bool
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
			continue
		}
		if child.Type == html.ElementNode && child.Data == "input" && attribute(child, "type") == "checkbox" {
			task = new(hasAttribute(child, "checked"))
			continue
		}
		if child.Type == html.ElementNode && (child.Data == "p" && len(content) == 0 && len(kids) == 0 || child.Data == "summary" && node.Data == "details") {
			for in := child.FirstChild; in != nil; in = in.NextSibling {
				if in.Type == html.ElementNode && in.Data == "input" && attribute(in, "type") == "checkbox" {
					task = new(hasAttribute(in, "checked"))
					continue
				}
				content = append(content, readInlines(in, nil, 0)...)
			}
			continue
		}
		if child.Type == html.ElementNode {
			blocks, matched, err := parseDocumentBlock(child, depth+1)
			if err != nil {
				return nil, nil, nil, err
			}
			if matched {
				kids = append(kids, blocks...)
				continue
			}
		}
		content = append(content, readInlines(child, nil, 0)...)
	}
	return trimInlines(content), kids, task, nil
}

func readChildrenInlines(node *html.Node) []documentInline {
	var result []documentInline
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result = append(result, readInlines(child, nil, 0)...)
	}
	return trimInlines(result)
}

func readInlines(node *html.Node, before contentObject, depth int) []documentInline {
	if depth >= maxDocumentDepth {
		return nil
	}
	if node.Type == html.TextNode {
		words := node.Data
		if before["code"] != true {
			words = inlineSpace.ReplaceAllString(words, " ")
		}
		if words == "" {
			return nil
		}
		return []documentInline{{text: words, marks: before}}
	}
	if node.Type != html.ElementNode {
		return nil
	}
	key := ""
	var value any = true
	switch node.Data {
	case "strong", "b":
		key = "bold"
	case "em", "i":
		key = "italic"
	case "s", "del", "strike":
		key = "strike"
	case "u":
		key = "underline"
	case "code":
		key = "code"
	case "a":
		if href := attribute(node, "href"); safeDocumentHref(href) {
			key, value = "link", contentObject{"href": href}
		}
	case "mark":
		color := attribute(node, "data-color")
		if color == "" {
			color = "yellow"
		}
		if validDocumentColor(color) {
			key, value = "highlight", contentObject{"value": color}
		}
	case "span":
		if color := attribute(node, "data-color"); validDocumentColor(color) {
			key, value = "textColor", contentObject{"value": color}
		}
	case "br":
		return []documentInline{{br: true}}
	case "img", "input", "script", "style", "iframe", "noscript":
		return nil
	}
	marks := before
	if key != "" {
		marks = make(contentObject, len(before)+1)
		for k, v := range before {
			marks[k] = v
		}
		marks[key] = value
	}
	var result []documentInline
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result = append(result, readInlines(child, marks, depth+1)...)
	}
	return result
}

var inlineSpace = regexp.MustCompile(`[\t\n\r\f ]+`)

func trimInlines(parts []documentInline) []documentInline {
	result := make([]documentInline, 0, len(parts))
	for _, part := range parts {
		if part.br {
			result = append(result, part)
			continue
		}
		if i := len(result) - 1; i < 0 || result[i].br || strings.HasSuffix(result[i].text, " ") {
			part.text = strings.TrimLeft(part.text, " ")
		}
		if part.text != "" {
			result = append(result, part)
		}
	}
	for len(result) > 0 && !result[len(result)-1].br {
		index := len(result) - 1
		result[index].text = strings.TrimRight(result[index].text, " ")
		if result[index].text != "" {
			break
		}
		result = result[:index]
	}
	return result
}

func nodeText(node *html.Node) string {
	var out strings.Builder
	for child := range node.Descendants() {
		if child.Type == html.TextNode {
			out.WriteString(child.Data)
		}
	}
	return out.String()
}

func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func hasAttribute(node *html.Node, key string) bool {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return true
		}
	}
	return false
}

func styleValue(node *html.Node, name string) string {
	for part := range strings.SplitSeq(attribute(node, "style"), ";") {
		if field, value, ok := strings.Cut(part, ":"); ok && strings.TrimSpace(field) == name {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	return attribute(node, "align")
}

func validAlign(s string) any {
	switch s {
	case "left", "center", "right", "justify":
		return s
	}
	return nil
}
