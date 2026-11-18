// Package skills loads repository and user-authored instructions for a single
// conversation. Sets own their text and have no process-wide cache.
package skills

import (
	"bytes"
	"context"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const MaxSkills = 40
const MaxSkillBytes = 256 * 1024
const MaxSkillChars = 32000
const MaxSkillFiles = 60
const maxDepth = 3
const maxDirs = 30

var validName = regexp.MustCompile(`^[\p{L}\p{N}](?:[\p{L}\p{N}._-]{0,62}[\p{L}\p{N}])?$`)
var description = regexp.MustCompile(`(?i)^description:\s*`)
var blockValue = regexp.MustCompile(`^[>|][+-]?$`)
var linePrefix = regexp.MustCompile(`^\s*(?:#+|>+|[-*])\s*`)
var whitespace = regexp.MustCompile(`[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)

type Skill struct {
	Name   string `json:"name"`
	When   string `json:"when"`
	Source string `json:"source"`
}
type Content struct {
	Skill
	Text   string  `json:"text"`
	Folder *string `json:"folder"`
}
type loaded struct {
	Content
	files              sdk.SourceView
	root, plain, place string
	own                bool
}
type Set struct{ found []loaded }
type place struct {
	files                   sdk.SourceView
	dir, shown, source, key string
	own                     bool
}

func oneLine(value string) string {
	value = textutil.Trim(whitespace.ReplaceAllString(value, " "))
	if len([]rune(value)) > 100 || len(value) > 200 { // Count UTF-16 only when needed.
		prefix := textutil.HeadUnits(value, 200)
		if prefix != value {
			return prefix + "…"
		}
	}
	return value
}
func WhenToUse(value string) string {
	lines := strings.Split(strings.TrimPrefix(value, "\ufeff"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	first := 0
	if textutil.Trim(lines[0]) == "---" {
		end := 1
		for end < len(lines) && textutil.Trim(lines[end]) != "---" {
			end++
		}
		if end < len(lines) {
			for at := 1; at < end; at++ {
				if !description.MatchString(lines[at]) {
					continue
				}
				value := textutil.Trim(description.ReplaceAllString(lines[at], ""))
				if value == "" || blockValue.MatchString(value) {
					rest := []string{}
					for next := at + 1; next < end; next++ {
						line := lines[next]
						if textutil.Trim(line) != "" && !whitespace.MatchString(string([]rune(line)[0])) {
							break
						}
						rest = append(rest, line)
					}
					value = strings.Join(rest, " ")
				}
				value = textutil.Trim(value)
				if value != "" {
					if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'") {
						value = value[1:]
					}
					if strings.HasSuffix(value, `"`) || strings.HasSuffix(value, "'") {
						value = value[:len(value)-1]
					}
					return oneLine(value)
				}
				break
			}
			first = end + 1
		}
	}
	for _, line := range lines[first:] {
		if textutil.Trim(line) != "" {
			return oneLine(linePrefix.ReplaceAllString(line, ""))
		}
	}
	return ""
}
func Name(file string) string {
	if !strings.HasSuffix(strings.ToLower(file), ".md") {
		return ""
	}
	name := file[:len(file)-3]
	if !validName.MatchString(name) {
		return ""
	}
	return name
}
func decode(data []byte) string {
	value := strings.TrimPrefix(strings.Map(func(r rune) rune { return r }, string(data)), "\ufeff")
	cut := textutil.HeadUnits(value, MaxSkillChars)
	if cut != value {
		return cut + "\n[cut: the skill is longer]"
	}
	return value
}
func under(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}
func fromPlace(ctx context.Context, p place) []loaded {
	entries, err := p.files.List(ctx, p.dir)
	if err != nil {
		return nil
	}
	type candidate struct {
		path   string
		folder *string
	}
	candidates := map[string][]candidate{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil
		}
		if entry.Kind == "file" {
			if name := Name(entry.Name); name != "" {
				candidates[name] = append(candidates[name], candidate{path: under(p.dir, entry.Name)})
			}
		} else if entry.Kind == "directory" && validName.MatchString(entry.Name) {
			folder := under(p.dir, entry.Name)
			candidates[entry.Name] = append([]candidate{{path: folder + "/SKILL.md", folder: &folder}}, candidates[entry.Name]...)
		}
	}
	names := []string{}
	for name := range candidates {
		names = append(names, name)
	}
	compare := collate.New(language.AmericanEnglish).CompareString
	slices.SortStableFunc(names, compare)
	found := []loaded{}
	for _, name := range names {
		if ctx.Err() != nil || len(found) >= MaxSkills {
			break
		}
		for _, candidate := range candidates[name] {
			data, err := p.files.ReadBytes(ctx, candidate.path, MaxSkillBytes)
			if err != nil || len(data) > MaxSkillBytes {
				continue
			}
			value := decode(data)
			var folder *string
			root := ""
			if candidate.folder != nil {
				folder = new(under(p.shown, name))
				root = *candidate.folder
			}
			found = append(found, loaded{Content: Content{Skill: Skill{Name: name, When: WhenToUse(value), Source: p.source}, Text: value, Folder: folder}, files: p.files, root: root, plain: name, place: p.key, own: p.own})
			break
		}
	}
	return found
}
func Load(ctx context.Context, repo sdk.SourceView, localDirs []string) *Set {
	places := []place{}
	if repo != nil {
		places = append(places, place{repo, ".ariadne/skills", ".ariadne/skills", "repo", "repo", true}, place{repo, ".agents/skills", ".agents/skills", "repo", "repo-agents", false})
	}
	for i, dir := range localDirs {
		key := "user"
		if i == 1 {
			key = "user-agents"
		} else if i > 1 {
			key = "user-" + decimal(i+1)
		}
		places = append(places, place{directoryFiles{dir}, ".", dir, "local", key, i == 0})
	}
	found := []loaded{}
	counts := map[string]int{}
	for _, p := range places {
		for _, skill := range fromPlace(ctx, p) {
			counts[skill.plain]++
			found = append(found, skill)
		}
	}
	for i := range found {
		if counts[found[i].plain] > 1 {
			found[i].Name += "@" + found[i].place
		}
	}
	compare := collate.New(language.AmericanEnglish).CompareString
	slices.SortStableFunc(found, func(a, b loaded) int { return compare(a.plain, b.plain) })
	return &Set{found: found}
}
func (set *Set) List() []Skill {
	out := []Skill{}
	if set != nil {
		for _, skill := range set.found {
			out = append(out, skill.Skill)
		}
	}
	return out
}
func (set *Set) Named(name string) []string {
	out := []string{}
	if set != nil {
		for _, skill := range set.found {
			if skill.plain == name {
				out = append(out, skill.Name)
			}
		}
	}
	return out
}
func (set *Set) find(name string) *loaded {
	if set != nil {
		for i := range set.found {
			if set.found[i].Name == name {
				return &set.found[i]
			}
		}
	}
	return nil
}
func (set *Set) Get(name string) *Content {
	skill := set.find(name)
	if skill == nil {
		return nil
	}
	out := skill.Content
	if out.Folder != nil {
		out.Folder = new(*out.Folder)
	}
	return &out
}
func (set *Set) Review() *Content {
	if set != nil {
		for _, skill := range set.found {
			if skill.plain == "review" && skill.own {
				return set.Get(skill.Name)
			}
		}
	}
	return nil
}

type FileList struct {
	Paths []string `json:"paths"`
	More  bool     `json:"more"`
}

func (set *Set) Files(ctx context.Context, name string) FileList {
	out := FileList{Paths: []string{}}
	skill := set.find(name)
	if skill == nil || skill.Folder == nil {
		return out
	}
	listed := 0
	compare := collate.New(language.AmericanEnglish).CompareString
	var walk func(string, int)
	walk = func(dir string, depth int) {
		if ctx.Err() != nil {
			return
		}
		listed++
		if listed > maxDirs {
			out.More = true
			return
		}
		entries, err := skill.files.List(ctx, under(skill.root, dir))
		if err != nil {
			return
		}
		slices.SortStableFunc(entries, func(a, b sdk.DirectoryEntry) int { return compare(a.Name, b.Name) })
		for _, entry := range entries {
			if out.More && len(out.Paths) >= MaxSkillFiles {
				return
			}
			name := under(dir, entry.Name)
			if entry.Kind == "directory" {
				if depth < maxDepth {
					walk(name, depth+1)
				} else {
					out.More = true
				}
			} else if name != "SKILL.md" {
				if len(out.Paths) < MaxSkillFiles {
					out.Paths = append(out.Paths, name)
				} else {
					out.More = true
				}
			}
		}
	}
	walk(".", 1)
	return out
}

type ReadResult struct {
	Text   string `json:"text"`
	Binary bool   `json:"-"`
}

func inside(file string) string {
	if strings.HasPrefix(file, "/") || strings.HasPrefix(file, `\`) || len(file) >= 2 && ((file[0] >= 'a' && file[0] <= 'z') || (file[0] >= 'A' && file[0] <= 'Z')) && file[1] == ':' {
		return ""
	}
	file = strings.ReplaceAll(file, `\`, "/")
	for _, part := range strings.Split(file, "/") {
		if part == ".." {
			return ""
		}
	}
	file = path.Clean(file)
	if file == "." {
		return ""
	}
	return file
}
func (set *Set) Read(ctx context.Context, name, file string) *ReadResult {
	skill := set.find(name)
	file = inside(file)
	if skill == nil || skill.Folder == nil || file == "" {
		return nil
	}
	data, err := skill.files.ReadBytes(ctx, skill.root+"/"+file, MaxSkillBytes)
	if err != nil || len(data) > MaxSkillBytes {
		return nil
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return &ReadResult{Binary: true}
	}
	return &ReadResult{Text: decode(data)}
}
