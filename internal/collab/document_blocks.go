package collab

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
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
	normalized, ok, err := core.ParseAskTagNode(node)
	if err != nil || !ok {
		return documentBlock{}, false, err
	}
	return documentBlock{kind: "ask", props: normalized}, true, nil
}

func normalizeAskProps(raw map[string]any) (documentBlock, error) {
	normalized, err := core.NormalizeAskMap(raw)
	if err != nil {
		return documentBlock{}, err
	}
	return documentBlock{kind: "ask", props: normalized}, nil
}

func renderAskBlock(props contentObject) string {
	encoded, err := core.MarshalCanonicalAsk(props, false)
	if err != nil {
		return ""
	}
	return "```ask\n" + encoded + "\n```"
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
