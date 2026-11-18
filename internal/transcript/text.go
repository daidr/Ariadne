package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
)

const spaces = `\x{0009}-\x{000D}\x{0020}\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
const separator = `(?:[^A-Za-z0-9_\x{017F}\x{212A}]|_|\p{Cf})*`

const FinalResearchPrompt = "That is as far as this research goes: answer now with what you found, and say what is still open."

var outside = regexp.MustCompile(`(?i)[<＜﹤〈‹][` + spaces + `\p{Cf}]*(/?)[` + spaces + `\p{Cf}]*system` + separator + `update`)
var relayTag = regexp.MustCompile(`(?i)<(/?)(user_message|message_from_coordinator)`)
var blockTag = regexp.MustCompile(`<([a-z]+(?:_[a-z]+)*)_[0-9a-f]{12}[` + spaces + `>]`)
var attributeText = strings.NewReplacer(`"`, "_", "<", "_", ">", "_")

func decimal(value int) string     { return strconv.Itoa(value) }
func AsOutside(text string) string { return outside.ReplaceAllString(text, "‹${1}system update") }
func Defang(text string) string    { return relayTag.ReplaceAllString(AsOutside(text), "‹${1}${2}") }

type Block struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

func Fenced(name, body, attributes string) Block {
	sum := sha256.Sum256([]byte(body))
	tag := name + "_" + hex.EncodeToString(sum[:])[:12]
	words := strings.Split(name, "_")
	escaped := make([]string, len(words))
	for i, word := range words {
		escaped[i] = regexp.QuoteMeta(word)
	}
	pattern := regexp.MustCompile(`(?i)` + strings.Join(escaped, separator))
	inner := pattern.ReplaceAllString(body, strings.Join(words, " "))
	attribute := ""
	if attributes != "" {
		attribute = " " + attributes
	}
	return Block{Tag: tag, Text: "<" + tag + attribute + ">\n" + inner + "\n</" + tag + ">"}
}
func sectionPairs(entry Entry) [][2]string {
	out := [][2]string{}
	for _, raw := range arr(entry["sections"]) {
		pair := arr(raw)
		if len(pair) >= 2 {
			out = append(out, [2]string{text(pair[0]), text(pair[1])})
		}
	}
	return out
}
func CurrentSections(entries []Entry) [][2]string {
	base := LatestBase(entries)
	if base == nil {
		return nil
	}
	output := sectionPairs(base)
	indexes := map[string]int{}
	for i, pair := range output {
		indexes[pair[0]] = i
	}
	for _, entry := range InModelOrder(entries) {
		if entry["kind"] != "prompt" || yes(entry["base"]) || num(entry["seq"]) < num(base["seq"]) {
			continue
		}
		for _, pair := range sectionPairs(entry) {
			if index, ok := indexes[pair[0]]; ok {
				output[index] = pair
			} else {
				indexes[pair[0]] = len(output)
				output = append(output, pair)
			}
		}
	}
	return output
}
func RemovedTools(entries []Entry) map[string]bool {
	removed := map[string]bool{}
	base := LatestBase(entries)
	if base == nil {
		return removed
	}
	for _, entry := range entries {
		if entry["kind"] == "prompt" && !yes(entry["base"]) && num(entry["seq"]) > num(base["seq"]) {
			for _, name := range arr(entry["removedTools"]) {
				removed[text(name)] = true
			}
		}
	}
	return removed
}
func partOf(value string) string {
	trimmed := textutil.Trim(value)
	if trimmed == "" {
		return "nothing yet: it is new"
	}
	if match := blockTag.FindStringSubmatch(value); match != nil {
		return "the part with the block " + match[1] + "_…"
	}
	words := strings.FieldsFunc(trimmed, textutil.IsSpace)
	words = words[:min(8, len(words))]
	return "the part beginning " + strings.NewReplacer(`"`, "", "<", "", ">", "").Replace(strings.Join(words, " ")) + "…"
}
func PromptUpdate(entry Entry, before map[string]string) string {
	parts := []string{}
	for _, pair := range sectionPairs(entry) {
		start := `<section name="` + pair[0] + `" replaces="` + partOf(before[pair[0]]) + `">`
		if value := textutil.Trim(pair[1]); value != "" {
			parts = append(parts, start+"\n"+value+"\n</section>")
		} else {
			parts = append(parts, start+"(now empty: what it said no longer applies)</section>")
		}
	}
	values := append([]string{}, parts...)
	removed := []string{}
	for _, name := range arr(entry["removedTools"]) {
		removed = append(removed, text(name))
	}
	if len(removed) > 0 {
		values = append(values, "These tools are no longer offered; do not call them: "+strings.Join(removed, ", ")+".")
	}
	block := Fenced("system_update", strings.Join(values, "\n"), "")
	how := ""
	if len(parts) > 0 {
		how = " Each <section> inside <" + block.Tag + "> takes the place of the part of your instructions its \"replaces\" names; the rest stays as it was."
	}
	return "Ariadne updated your instructions; this is part of them, not a message." + how + "\n" + block.Text
}
