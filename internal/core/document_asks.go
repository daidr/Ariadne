package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// FallbackAskID computes a deterministic short ID for an inline ask block when
// the author omits id. It uses 32-bit FNV-1a over UTF-8 bytes so Go and JS
// produce identical IDs for all Unicode inputs.
func FallbackAskID(question string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(question))
	return fmt.Sprintf("ask-%08x", h.Sum32())
}

// NormalizeAskMap validates and normalizes raw ask block properties into a
// canonical map shared across collab Yjs syncing, markdown rendering, and
// feedback diffing.
func NormalizeAskMap(raw map[string]any) (map[string]any, error) {
	str := func(key string) string {
		value, _ := raw[key].(string)
		return strings.TrimSpace(value)
	}
	question := str("question")
	if question == "" {
		return nil, errors.New("ask block requires a question")
	}
	id := str("id")
	if id == "" {
		id = FallbackAskID(question)
	}
	options := []any{}
	seenOpt := map[string]bool{}
	if list, ok := raw["options"].([]any); ok {
		for i, item := range list {
			var optID, label, note string
			recommended := false
			switch value := item.(type) {
			case string:
				label = strings.TrimSpace(value)
			case map[string]any:
				optID, _ = value["id"].(string)
				optID = strings.TrimSpace(optID)
				label, _ = value["label"].(string)
				label = strings.TrimSpace(label)
				note, _ = value["note"].(string)
				note = strings.TrimSpace(note)
				recommended, _ = value["recommended"].(bool)
			}
			if label == "" {
				continue
			}
			if optID == "" {
				optID = fmt.Sprintf("opt-%d", i+1)
			}
			if seenOpt[optID] {
				continue
			}
			seenOpt[optID] = true
			entry := map[string]any{"id": optID, "label": label}
			if note != "" {
				entry["note"] = note
			}
			if recommended {
				entry["recommended"] = true
			}
			options = append(options, entry)
		}
	}
	pick := str("pick")
	switch pick {
	case "one", "many", "text":
	default:
		if len(options) > 0 {
			pick = "one"
		} else {
			pick = "text"
		}
	}
	if len(options) == 0 {
		pick = "text"
	}
	allowNote := true
	if value, ok := raw["allowNote"].(bool); ok {
		allowNote = value
	}
	selected := []any{}
	seenSel := map[string]bool{}
	if list, ok := raw["selected"].([]any); ok {
		for _, item := range list {
			s, _ := item.(string)
			s = strings.TrimSpace(s)
			if s == "" || seenSel[s] {
				continue
			}
			seenSel[s] = true
			selected = append(selected, s)
		}
	}
	note, _ := raw["note"].(string)
	confirmed, _ := raw["confirmed"].(bool)
	return map[string]any{
		"id":          id,
		"question":    question,
		"description": str("description"),
		"pick":        pick,
		"options":     options,
		"allowNote":   allowNote,
		"selected":    selected,
		"note":        note,
		"confirmed":   confirmed,
	}, nil
}

// MarshalCanonicalAsk serializes an ask block into the canonical JSON payload
// used in ```ask fences. When stripAnswers is true, user-answer fields
// (selected, note, confirmed) are omitted so plain-prose diffing ignores answer
// updates while still normalizing shorthand tags or omitted defaults.
func MarshalCanonicalAsk(raw map[string]any, stripAnswers bool) (string, error) {
	normalized, err := NormalizeAskMap(raw)
	if err != nil {
		return "", err
	}
	out := map[string]any{
		"id":       normalized["id"],
		"question": normalized["question"],
		"pick":     normalized["pick"],
	}
	if desc, _ := normalized["description"].(string); desc != "" {
		out["description"] = desc
	}
	if opts, _ := normalized["options"].([]any); len(opts) > 0 {
		out["options"] = opts
	}
	if allowNote, ok := normalized["allowNote"].(bool); ok && !allowNote {
		out["allowNote"] = false
	}
	if !stripAnswers {
		if selected, _ := normalized["selected"].([]any); len(selected) > 0 {
			out["selected"] = selected
		}
		if note, _ := normalized["note"].(string); note != "" {
			out["note"] = note
		}
		if confirmed, _ := normalized["confirmed"].(bool); confirmed {
			out["confirmed"] = true
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ParseAskTagNode parses an HTML <ask ...>...</ask> element into normalized ask
// properties. Both JSON body syntax (<ask>{...}</ask>) and attribute syntax
// (<ask question="..." options='[...]'>) are supported.
func ParseAskTagNode(node *html.Node) (map[string]any, bool, error) {
	body := strings.TrimSpace(htmlNodeText(node))
	if strings.HasPrefix(body, "{") {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(body), &parsed); err == nil {
			if normalized, err := NormalizeAskMap(parsed); err == nil {
				return normalized, true, nil
			}
		}
	}
	question := strings.TrimSpace(htmlAttr(node, "question"))
	if question == "" {
		question = body
	}
	if question == "" {
		return nil, false, nil
	}
	raw := map[string]any{
		"id":          htmlAttr(node, "id"),
		"question":    question,
		"description": htmlAttr(node, "description"),
		"pick":        htmlAttr(node, "pick"),
		"note":        htmlAttr(node, "note"),
	}
	if opts := strings.TrimSpace(htmlAttr(node, "options")); opts != "" {
		var parsedOpts []any
		if err := json.Unmarshal([]byte(opts), &parsedOpts); err == nil {
			raw["options"] = parsedOpts
		}
	}
	if sel := strings.TrimSpace(htmlAttr(node, "selected")); sel != "" {
		var parsedSel []any
		if err := json.Unmarshal([]byte(sel), &parsedSel); err == nil {
			raw["selected"] = parsedSel
		}
	}
	if allowNote := strings.TrimSpace(htmlAttr(node, "allownote")); allowNote != "" {
		raw["allowNote"] = allowNote != "false"
	} else if allowNote := strings.TrimSpace(htmlAttr(node, "allow-note")); allowNote != "" {
		raw["allowNote"] = allowNote != "false"
	}
	if confirmed := strings.TrimSpace(htmlAttr(node, "confirmed")); confirmed == "true" {
		raw["confirmed"] = true
	}
	normalized, err := NormalizeAskMap(raw)
	if err != nil {
		return nil, false, nil
	}
	return normalized, true, nil
}

func htmlAttr(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func htmlNodeText(node *html.Node) string {
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return out.String()
}

var (
	documentAskBlockPattern = regexp.MustCompile("(?m)(?:^```ask[ \t]*\\r?\\n(?s:.*?)\\r?\\n```[ \t]*$|<ask(?:\\s[^>]*)?>(?s:.*?)</ask>|<ask(?:\\s[^>]*)?/>)")
	documentAskFenceExtract = regexp.MustCompile("(?ms)^```ask[ \t]*\\r?\\n(.*?)\\r?\\n```[ \t]*$")
)

func parseAskMatch(match string) (map[string]any, bool) {
	if parts := documentAskFenceExtract.FindStringSubmatch(match); len(parts) >= 2 {
		var raw map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(parts[1])), &raw); err != nil {
			return nil, false
		}
		normalized, err := NormalizeAskMap(raw)
		if err != nil {
			return nil, false
		}
		return normalized, true
	}
	doc, err := html.Parse(strings.NewReader(match))
	if err != nil {
		return nil, false
	}
	for node := range doc.Descendants() {
		if node.Type == html.ElementNode && node.Data == "ask" {
			normalized, ok, err := ParseAskTagNode(node)
			if err != nil || !ok {
				return nil, false
			}
			return normalized, true
		}
	}
	return nil, false
}
