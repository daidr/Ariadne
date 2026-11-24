package collab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

func parseAskBlock(raw string) (documentBlock, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return documentBlock{}, err
	}
	return normalizeAskProps(parsed)
}

func parseAskTag(node *html.Node) (documentBlock, bool, error) {
	body := strings.TrimSpace(nodeText(node))
	if strings.HasPrefix(body, "{") {
		if block, err := parseAskBlock(body); err == nil {
			return block, true, nil
		}
	}
	question := strings.TrimSpace(attribute(node, "question"))
	if question == "" {
		question = body
	}
	if question == "" {
		return documentBlock{}, false, nil
	}
	raw := map[string]any{
		"id":          attribute(node, "id"),
		"question":    question,
		"description": attribute(node, "description"),
		"pick":        attribute(node, "pick"),
	}
	if opts := strings.TrimSpace(attribute(node, "options")); opts != "" {
		var parsedOpts []any
		if err := json.Unmarshal([]byte(opts), &parsedOpts); err == nil {
			raw["options"] = parsedOpts
		}
	}
	block, err := normalizeAskProps(raw)
	if err != nil {
		return documentBlock{}, false, nil
	}
	return block, true, nil
}

func normalizeAskProps(raw map[string]any) (documentBlock, error) {
	str := func(key string) string {
		value, _ := raw[key].(string)
		return strings.TrimSpace(value)
	}
	question := str("question")
	if question == "" {
		return documentBlock{}, errors.New("ask block requires a question")
	}
	id := str("id")
	if id == "" {
		sum := sha256.Sum256([]byte(question))
		id = "ask-" + hex.EncodeToString(sum[:4])
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
			entry := contentObject{"id": optID, "label": label}
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
	props := contentObject{
		"id":          id,
		"question":    question,
		"description": str("description"),
		"pick":        pick,
		"options":     options,
		"allowNote":   allowNote,
		"selected":    selected,
		"note":        note,
		"confirmed":   confirmed,
	}
	return documentBlock{kind: "ask", props: props}, nil
}

func renderAskBlock(props contentObject) string {
	normalized, err := normalizeAskProps(props)
	if err != nil {
		return ""
	}
	out := map[string]any{
		"id":       normalized.props["id"],
		"question": normalized.props["question"],
		"pick":     normalized.props["pick"],
	}
	if desc, _ := normalized.props["description"].(string); desc != "" {
		out["description"] = desc
	}
	if opts, _ := normalized.props["options"].([]any); len(opts) > 0 {
		out["options"] = opts
	}
	if allowNote, ok := normalized.props["allowNote"].(bool); ok && !allowNote {
		out["allowNote"] = false
	}
	if selected, _ := normalized.props["selected"].([]any); len(selected) > 0 {
		out["selected"] = selected
	}
	if note, _ := normalized.props["note"].(string); note != "" {
		out["note"] = note
	}
	if confirmed, _ := normalized.props["confirmed"].(bool); confirmed {
		out["confirmed"] = true
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return "```ask\n" + string(encoded) + "\n```"
}

func parseRefBlock(raw string) (documentBlock, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return documentBlock{}, err
	}
	return normalizeRefProps(parsed)
}

func parseRefTag(node *html.Node) (documentBlock, bool, error) {
	body := strings.TrimSpace(nodeText(node))
	if strings.HasPrefix(body, "{") {
		if block, err := parseRefBlock(body); err == nil {
			return block, true, nil
		}
	}
	artifactID := attribute(node, "artifact")
	if artifactID == "" {
		artifactID = attribute(node, "artifact-id")
	}
	if artifactID == "" {
		artifactID = attribute(node, "artifactid")
	}
	groupID := attribute(node, "group")
	if groupID == "" {
		groupID = attribute(node, "group-id")
	}
	if groupID == "" {
		groupID = attribute(node, "groupid")
	}
	optionID := attribute(node, "option")
	if optionID == "" {
		optionID = attribute(node, "option-id")
	}
	if optionID == "" {
		optionID = attribute(node, "optionid")
	}
	label := attribute(node, "label")
	if label == "" {
		label = body
	}
	block, err := normalizeRefProps(map[string]any{
		"artifactId": artifactID,
		"groupId":    groupID,
		"optionId":   optionID,
		"label":      label,
	})
	if err != nil {
		return documentBlock{}, false, nil
	}
	return block, true, nil
}

func normalizeRefProps(raw map[string]any) (documentBlock, error) {
	str := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := raw[key].(string); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed
				}
			}
		}
		return ""
	}
	artifactID := strings.TrimPrefix(str("artifactId", "artifact"), "artifact:")
	if artifactID == "" {
		return documentBlock{}, errors.New("ref block requires artifactId")
	}
	props := contentObject{
		"artifactId": artifactID,
		"groupId":    str("groupId", "group"),
		"optionId":   str("optionId", "option"),
		"label":      str("label"),
	}
	return documentBlock{kind: "ref", props: props}, nil
}

func renderRefBlock(props contentObject) string {
	normalized, err := normalizeRefProps(props)
	if err != nil {
		return ""
	}
	out := map[string]any{
		"artifactId": normalized.props["artifactId"],
	}
	for _, key := range []string{"groupId", "optionId", "label"} {
		if value, _ := normalized.props[key].(string); value != "" {
			out[key] = value
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return "```ref\n" + string(encoded) + "\n```"
}
