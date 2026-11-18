package modelcatalog

import (
	"regexp"
	"sort"

	"github.com/daidr/Ariadne/sdk"
)

func Entries(listed []sdk.ListedModel) []sdk.ListedModel {
	result := []sdk.ListedModel{}
	seen := map[string]bool{}
	for _, model := range listed {
		if model.ID != "" && !seen[model.ID] {
			seen[model.ID] = true
			result = append(result, model)
		}
	}
	return result
}
func InOrder(listed []string, info map[string]Info) []string {
	known, other := []string{}, []string{}
	for _, name := range listed {
		facts, exists := info[name]
		if exists && (facts.Tools == nil || *facts.Tools) {
			known = append(known, name)
		} else {
			other = append(other, name)
		}
	}
	sort.SliceStable(known, func(i, j int) bool { return info[known[i]].Released > info[known[j]].Released })
	return append(known, other...)
}

var notFirst = regexp.MustCompile(`(?i)embed|tts|whisper|audio|realtime|image|dall-e|sora|moderation|transcribe|search|babbage|davinci|instruct|mini|nano|lite|haiku|flash|small|guard|codex|vision|rerank|ocr|preview`)

func SuggestModel(listed []string, info map[string]Info) string {
	general := []string{}
	known := []string{}
	for _, name := range listed {
		if notFirst.MatchString(name) {
			continue
		}
		general = append(general, name)
		if facts, exists := info[name]; exists && (facts.Tools == nil || *facts.Tools) && facts.Released != "" {
			known = append(known, name)
		}
	}
	sort.SliceStable(known, func(i, j int) bool { return info[known[i]].Released > info[known[j]].Released })
	if len(known) > 0 {
		return known[0]
	}
	if len(general) > 0 {
		return general[0]
	}
	if len(listed) > 0 {
		return listed[0]
	}
	return ""
}
