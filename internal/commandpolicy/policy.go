// Package commandpolicy applies the user's command rules to compound commands,
// wrappers and nested shell text, without executing or expanding that text.
package commandpolicy

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/textutil"
)

type Rule struct {
	Pattern string `json:"pattern"`
	Action  string `json:"action"`
}
type Verdict struct {
	Action  string  `json:"action"`
	Pattern *string `json:"pattern"`
}

func Defaults() []Rule {
	rules := []Rule{}
	for _, pattern := range []string{"git push*", "npm publish*", "pnpm publish*", "yarn publish*", "bun publish*", "cargo publish*", "gh pr merge*", "gh release create*"} {
		rules = append(rules, Rule{Pattern: pattern, Action: "ask"})
	}
	return rules
}
func DecodeRules(raw any) []Rule {
	items, _ := raw.([]any)
	out := []Rule{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pattern, _ := item["pattern"].(string)
		action, _ := item["action"].(string)
		if pattern != "" && (action == "allow" || action == "ask" || action == "deny") {
			out = append(out, Rule{Pattern: pattern, Action: action})
		}
	}
	return out
}
func set(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

var wrappers = set("command", "exec", "nohup", "time", "builtin", "nice")
var shells = set("bash", "sh", "zsh", "dash", "ksh", "fish", "pwsh", "powershell")
var textPrograms = set("echo", "printf", "grep", "egrep", "fgrep", "rg", "ag", "ack", "findstr", "select-string", "sls", "write-output", "write-host")
var gitText = set("commit", "tag", "log", "grep", "notes", "stash")
var textRunners = set("eval", "source", ".", "xargs", "iex", "invoke-expression")
var gitValued = set("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path")
var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
var shellCommand = regexp.MustCompile(`(?i)^-[a-z]*c[a-z]*$|^-com`)

// Parsing and matching have separate budgets. A command that cannot be fully
// examined is held for approval; it never falls through to an implicit allow.
type budget struct {
	bytes, forms, matches int
	exhausted             bool
}

func (cost *budget) retain(text string) bool {
	if cost.exhausted {
		return false
	}
	cost.bytes += len(text)
	cost.forms++
	if cost.bytes > 4<<20 || cost.forms > 8192 {
		cost.exhausted = true
		return false
	}
	return true
}

type simpleCommand struct {
	raw   string
	words []string
}

func simpleCommands(line string, depth int, cost *budget) []simpleCommand {
	if !cost.retain(line) {
		return nil
	}
	out := []simpleCommand{}
	var raw, word strings.Builder
	words := []string{}
	started, target := false, false
	endWord := func() {
		if !started {
			return
		}
		if target {
			target = false
		} else {
			words = append(words, word.String())
		}
		word.Reset()
		started = false
	}
	endCommand := func() {
		endWord()
		text := textutil.Trim(raw.String())
		if text != "" || len(words) > 0 {
			if cost.retain(text) {
				out = append(out, simpleCommand{raw: strings.Clone(text), words: words})
			}
		}
		raw.Reset()
		words = []string{}
		target = false
	}
	inner := func(text string) {
		if depth < 4 {
			out = append(out, simpleCommands(text, depth+1, cost)...)
		}
	}
	closing := func(at int) int {
		open := 0
		for j := at; j < len(line); j++ {
			if line[j] == '(' {
				open++
			} else if line[j] == ')' {
				open--
				if open == 0 {
					return j
				}
			}
		}
		return len(line)
	}
	for i := 0; i < len(line) && !cost.exhausted; i++ {
		char := line[i]
		if char == '\'' {
			stop := len(line)
			if at := strings.IndexByte(line[i+1:], '\''); at >= 0 {
				stop = i + 1 + at
			}
			word.WriteString(line[i+1 : stop])
			started = true
			raw.WriteString(line[i:min(stop+1, len(line))])
			i = stop
			continue
		}
		if char == '"' {
			var value strings.Builder
			j := i + 1
			for ; j < len(line) && line[j] != '"'; j++ {
				if line[j] == '\\' && j+1 < len(line) {
					j++
					value.WriteByte(line[j])
				} else if line[j] == '$' && j+1 < len(line) && line[j+1] == '(' {
					close := closing(j + 1)
					inner(line[j+2 : close])
					value.WriteString(line[j:min(close+1, len(line))])
					j = close
				} else {
					value.WriteByte(line[j])
				}
			}
			word.WriteString(value.String())
			started = true
			raw.WriteString(line[i:min(j+1, len(line))])
			i = j
			continue
		}
		if char == '\\' && i+1 < len(line) {
			if line[i+1] != '\n' {
				word.WriteByte(line[i+1])
				started = true
			}
			raw.WriteString(line[i : i+2])
			i++
			continue
		}
		if char == '$' && i+1 < len(line) && line[i+1] == '(' {
			close := closing(i + 1)
			inner(line[i+2 : close])
			word.WriteString(line[i:min(close+1, len(line))])
			started = true
			raw.WriteString(line[i:min(close+1, len(line))])
			i = close
			continue
		}
		if char == '`' {
			stop := len(line)
			if at := strings.IndexByte(line[i+1:], '`'); at >= 0 {
				stop = i + 1 + at
			}
			inner(line[i+1 : stop])
			word.WriteString(line[i:min(stop+1, len(line))])
			started = true
			raw.WriteString(line[i:min(stop+1, len(line))])
			i = stop
			continue
		}
		if char == '>' || char == '<' || char == '&' && i+1 < len(line) && line[i+1] == '>' {
			fd := started && word.Len() > 0
			for _, r := range word.String() {
				if r < '0' || r > '9' {
					fd = false
					break
				}
			}
			if fd {
				word.Reset()
				started = false
			} else {
				endWord()
			}
			j := i + 1
			for j < len(line) && (line[j] == '>' || line[j] == '<' || line[j] == '|') {
				j++
			}
			if j+1 < len(line) && line[j] == '&' && (line[j+1] >= '0' && line[j+1] <= '9' || line[j+1] == '-') {
				j += 2
				for j < len(line) && line[j] >= '0' && line[j] <= '9' {
					j++
				}
			} else {
				target = true
			}
			raw.WriteString(line[i:j])
			i = j - 1
			continue
		}
		if char == ';' || char == '\n' || char == '|' || char == '&' || char == '(' || char == ')' {
			endCommand()
			if (char == '&' || char == '|') && i+1 < len(line) && (line[i+1] == char || line[i+1] == '&') {
				i++
			}
			continue
		}
		if char == ' ' || char == '\t' {
			endWord()
			raw.WriteByte(char)
			continue
		}
		if char == '#' && !started {
			tail := raw.String()
			eligible := len(tail) == 0
			if len(tail) > 0 {
				last := []rune(tail)[len([]rune(tail))-1]
				eligible = textutil.IsSpace(last) || strings.ContainsRune(";&|()", last)
			}
			if eligible {
				for i+1 < len(line) && line[i+1] != '\n' {
					i++
				}
				continue
			}
		}
		word.WriteByte(char)
		started = true
		raw.WriteByte(char)
	}
	endCommand()
	return out
}
func programOf(word string) string {
	if at := strings.LastIndexAny(word, "/\\"); at >= 0 {
		word = word[at+1:]
	}
	if strings.HasSuffix(strings.ToLower(word), ".exe") {
		word = word[:len(word)-4]
	}
	return word
}
func gitForm(words []string) string {
	rest := words[1:]
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		option := rest[0]
		rest = rest[1:]
		if gitValued[option] && len(rest) > 0 {
			rest = rest[1:]
		}
	}
	return strings.Join(append([]string{"git"}, rest...), " ")
}
func units(text string) int {
	count := 0
	for _, r := range text {
		count++
		if r > 0xffff {
			count++
		}
	}
	return count
}

type looseForm struct {
	text   string
	offset int
}
type rulePart struct {
	forms, nested        []string
	loose                []looseForm
	text, quoted, runner bool
}

func decodeEncoded(value string) string {
	var input strings.Builder
	for _, r := range value {
		if r == '=' {
			break
		}
		if r == '-' {
			r = '+'
		}
		if r == '_' {
			r = '/'
		}
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' {
			input.WriteRune(r)
		}
	}
	data, _ := base64.RawStdEncoding.DecodeString(input.String())
	words := make([]uint16, len(data)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(words))
}
func makePart(command simpleCommand, cost *budget) rulePart {
	part := rulePart{forms: []string{command.raw}, nested: []string{}, loose: []looseForm{}}
	words := command.words
	for len(words) > 0 {
		first := words[0]
		if assignment.MatchString(first) {
			words = words[1:]
		} else if first == "env" {
			words = words[1:]
			for len(words) > 0 && words[0] != "" && (strings.HasPrefix(words[0], "-") || assignment.MatchString(words[0])) {
				words = words[1:]
			}
		} else if first == "sudo" || first == "doas" {
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
		} else if wrappers[first] {
			words = words[1:]
		} else {
			break
		}
	}
	for _, word := range command.words {
		if strings.ContainsFunc(word, textutil.IsSpace) {
			part.nested = append(part.nested, word)
		}
	}
	if len(words) == 0 {
		return part
	}
	program := programOf(words[0])
	words = append([]string{program}, words[1:]...)
	form := strings.Join(words, " ")
	if !cost.retain(form) {
		return part
	}
	part.forms = append(part.forms, form)
	if program == "git" {
		form := gitForm(words)
		if cost.retain(form) {
			part.forms = append(part.forms, form)
		}
	}
	offset := 0
	for k := 1; k < len(words) && !cost.exhausted; k++ {
		offset += units(words[k-1]) + 1
		rest := append([]string{programOf(words[k])}, words[k+1:]...)
		text := strings.Join(rest, " ")
		if !cost.retain(text) {
			break
		}
		part.loose = append(part.loose, looseForm{text: text, offset: offset})
		if rest[0] == "git" {
			text := gitForm(rest)
			if cost.retain(text) {
				part.loose = append(part.loose, looseForm{text: text, offset: offset})
			}
		}
	}
	name := strings.ToLower(program)
	part.text = textPrograms[name]
	if name == "git" {
		form := strings.Split(gitForm(words), " ")
		part.text = len(form) > 1 && gitText[form[1]]
	}
	for i, word := range words {
		runs := strings.ToLower(programOf(word))
		if i > 0 && runs == "." {
			continue
		}
		if shells[runs] || textRunners[runs] || runs == "cmd" {
			part.runner = true
			break
		}
	}
	if shells[name] {
		for i := 1; i < len(words); i++ {
			if shellCommand.MatchString(words[i]) {
				if i+1 < len(words) {
					part.nested = append(part.nested, strings.Join(words[i+1:], " "))
				}
				break
			}
		}
		for i := 1; i < len(words); i++ {
			prefix := strings.ToLower(words[i])
			if strings.HasPrefix(prefix, "-") && len(prefix) > 1 && strings.HasPrefix("encodedcommand", prefix[1:]) {
				if i+1 < len(words) && words[i+1] != "" {
					part.nested = append(part.nested, decodeEncoded(words[i+1]))
				}
				break
			}
		}
	} else if name == "cmd" {
		for i, word := range words {
			option := strings.ToLower(word)
			if option == "/c" || option == "/k" {
				if i > 0 && i+1 < len(words) {
					part.nested = append(part.nested, strings.Join(words[i+1:], " "))
				}
				break
			}
		}
	}
	return part
}
func ruleParts(command string, depth int, quoted bool, cost *budget) []rulePart {
	lines := []string{command}
	if depth == 0 && strings.Contains(command, "`") {
		lines = append(lines, strings.ReplaceAll(command, "`", ""))
	}
	out := []rulePart{}
	for _, line := range lines {
		for _, simple := range simpleCommands(line, 0, cost) {
			if cost.exhausted {
				return out
			}
			part := makePart(simple, cost)
			part.quoted = quoted
			out = append(out, part)
			if depth < 4 {
				for _, inner := range part.nested {
					out = append(out, ruleParts(inner, depth+1, quoted || part.text, cost)...)
					if cost.exhausted {
						return out
					}
				}
			}
		}
	}
	return out
}
func glob(pattern, text string) bool {
	pieces := strings.Split(textutil.Trim(pattern), "*")
	if len(pieces) == 1 {
		return text == pieces[0]
	}
	if !strings.HasPrefix(text, pieces[0]) {
		return false
	}
	text = text[len(pieces[0]):]
	for _, piece := range pieces[1 : len(pieces)-1] {
		if at := strings.Index(text, piece); at >= 0 {
			text = text[at+len(piece):]
		} else {
			return false
		}
	}
	return strings.HasSuffix(text, pieces[len(pieces)-1])
}
func spelled(pattern string) int {
	prefix, _, _ := strings.Cut(textutil.Trim(pattern), "*")
	return units(prefix)
}
func Parts(command string) ([]string, error) {
	cost := &budget{}
	parts := simpleCommands(command, 0, cost)
	if cost.exhausted || len(command) > 32<<10 {
		return nil, errors.New("command is too complex to examine")
	}
	output := []string{}
	for _, part := range parts {
		output = append(output, part.raw)
	}
	return output, nil
}
func Decide(command string, rules []Rule) Verdict {
	if len(command) > 32<<10 || len(rules) > 200 {
		return Verdict{Action: "ask"}
	}
	cost := &budget{}
	parts := append([]rulePart{{forms: []string{textutil.Trim(command)}}}, ruleParts(command, 0, false, cost)...)
	if cost.exhausted {
		return Verdict{Action: "ask"}
	}
	runsText := false
	for _, part := range parts {
		if part.runner {
			runsText = true
			break
		}
	}
	match := func(pattern, text string) bool {
		cost.matches += len(pattern) + len(text)
		if cost.matches > 64<<20 {
			cost.exhausted = true
			return false
		}
		return glob(pattern, text)
	}
	matching := func(action string, texts []string) *Rule {
		for i := range rules {
			rule := &rules[i]
			if rule.Action != action {
				continue
			}
			for _, text := range texts {
				if match(rule.Pattern, text) {
					return rule
				}
			}
		}
		return nil
	}
	verdicts := []Verdict{}
	for _, part := range parts {
		every := append([]string(nil), part.forms...)
		for _, loose := range part.loose {
			every = append(every, loose.text)
		}
		if rule := matching("deny", every); rule != nil {
			pattern := strings.Clone(rule.Pattern)
			return Verdict{Action: "deny", Pattern: &pattern}
		}
		mentioned := (part.text || part.quoted) && !runsText
		var allow *Rule
		reach := -1
		for i := range rules {
			rule := &rules[i]
			if rule.Action != "allow" {
				continue
			}
			for _, text := range part.forms {
				if match(rule.Pattern, text) && spelled(rule.Pattern) > reach {
					allow = rule
					reach = spelled(rule.Pattern)
				}
			}
		}
		if allow != nil {
			beyond := []string{}
			if !mentioned {
				for _, loose := range part.loose {
					if loose.offset >= reach {
						beyond = append(beyond, loose.text)
					}
				}
			}
			if ask := matching("ask", beyond); ask != nil {
				verdicts = append(verdicts, Verdict{Action: "ask", Pattern: new(strings.Clone(ask.Pattern))})
			} else {
				verdicts = append(verdicts, Verdict{Action: "allow", Pattern: new(strings.Clone(allow.Pattern))})
			}
		} else {
			asked := every
			if mentioned {
				asked = nil
				if !part.quoted {
					asked = part.forms
				}
			}
			if rule := matching("ask", asked); rule != nil {
				verdicts = append(verdicts, Verdict{Action: "ask", Pattern: new(strings.Clone(rule.Pattern))})
			}
		}
		if cost.exhausted {
			return Verdict{Action: "ask"}
		}
	}
	for _, action := range []string{"ask", "allow"} {
		for _, verdict := range verdicts {
			if verdict.Action == action {
				return verdict
			}
		}
	}
	return Verdict{Action: "allow"}
}
