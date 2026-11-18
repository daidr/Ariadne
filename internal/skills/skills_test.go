package skills

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	name = filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestSkillsMatchLegacy(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	write(t, first, "review.md", "fallback file")
	write(t, first, "review/SKILL.md", "---\ndescription: >\n  Review all changes\n  and test them\nother: value\n---\n# Review\n")
	write(t, first, "review/notes.txt", "read these notes")
	write(t, first, "review/nested/deep.txt", "nested notes")
	write(t, first, "review/binary.dat", "\x00binary")
	write(t, first, "folder-without-skill.md", "# Single file")
	write(t, first, "folder-without-skill/notes.txt", "folder does not win")
	write(t, first, "ignored!.md", "not valid")
	write(t, first, "too-large.md", strings.Repeat("x", MaxSkillBytes+1))
	write(t, first, "long.md", strings.Repeat("词😀", 11000))
	write(t, second, "review.md", "shared review")
	descriptions := []string{"", "\ufeff# first\nsecond", "---\ndescription: 'Quoted description'\n---\nBody", "---\ndescription:\n  first line\n\n  second line\nname: other\n---", "---\ndescription: |+\n  preserved words\n---", "---\nname: nothing\n---\n> quoted body", "---\nunterminated\n# body", strings.Repeat("😀中 ", 80)}
	names := []string{"a.md", ".hidden.md", "review.MD", "中文.md", "a-.md", "a_b-1.md", strings.Repeat("a", 64) + ".md", strings.Repeat("a", 65) + ".md", "with space.md", "😀.md"}
	reads := []map[string]string{{"name": "review@user", "path": "notes.txt"}, {"name": "review@user", "path": "nested/deep.txt"}, {"name": "review@user", "path": "binary.dat"}, {"name": "review@user", "path": "../review.md"}, {"name": "review@user", "path": "C:\\secret"}, {"name": "review@user-agents", "path": "missing"}}
	set := Load(t.Context(), nil, []string{first, second})
	if len(set.List()) != 4 || set.Review() == nil || set.Review().Name != "review@user" {
		t.Fatal("skill precedence changed", set.List())
	}
	if value := set.Read(t.Context(), "review@user", "../review.md"); value != nil {
		t.Fatal("path escaped skill")
	}
	if got := set.Read(t.Context(), "review@user", "binary.dat"); got == nil || !got.Binary {
		t.Fatal("binary file treated as text")
	}
	native := map[string]any{"list": set.List(), "review": set.Review(), "named": set.Named("review")}
	when := []string{}
	for _, value := range descriptions {
		when = append(when, WhenToUse(value))
	}
	native["descriptions"] = when
	named := []any{}
	for _, value := range names {
		if name := Name(value); name != "" {
			named = append(named, name)
		} else {
			named = append(named, nil)
		}
	}
	native["names"] = named
	contents := []*Content{}
	files := []FileList{}
	for _, skill := range set.List() {
		contents = append(contents, set.Get(skill.Name))
		files = append(files, set.Files(t.Context(), skill.Name))
	}
	native["contents"] = contents
	native["files"] = files
	results := []any{}
	for _, read := range reads {
		result := set.Read(t.Context(), read["name"], read["path"])
		if result == nil {
			results = append(results, nil)
		} else if result.Binary {
			results = append(results, "binary")
		} else {
			results = append(results, map[string]any{"text": result.Text})
		}
	}
	native["reads"] = results
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("native checks passed; Bun unavailable")
	}
	input, _ := json.Marshal(map[string]any{"dirs": []string{first, second}, "descriptions": descriptions, "names": names, "reads": reads})
	command := legacytest.Command(t, bun, "packages/agent/src/testing/go-skills-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	data, _ := json.Marshal(native)
	var actual, want map[string]any
	if json.Unmarshal(data, &actual) != nil || json.Unmarshal(output, &want) != nil {
		t.Fatal("invalid JSON", string(output))
	}
	for key, value := range actual {
		if !reflect.DeepEqual(value, want[key]) {
			got, _ := json.Marshal(value)
			expected, _ := json.Marshal(want[key])
			t.Errorf("%s\nGo %s\nTS %s", key, got, expected)
		}
	}
}
func TestSkillListingLimitsAndDirectoryBoundaries(t *testing.T) {
	dir := t.TempDir()
	for i := range MaxSkills + 5 {
		write(t, dir, "skill-"+decimal(i)+".md", "# Skill")
	}
	set := Load(t.Context(), nil, []string{dir})
	if len(set.List()) != MaxSkills {
		t.Fatal("skill count unbounded")
	}
	write(t, dir, "review/SKILL.md", "review")
	for i := range MaxSkillFiles + 10 {
		write(t, dir, "review/file-"+decimal(i)+".txt", "note")
	}
	set = Load(t.Context(), nil, []string{dir})
	listing := set.Files(t.Context(), "review")
	if len(listing.Paths) != MaxSkillFiles || !listing.More {
		t.Fatal("file listing unbounded", listing)
	}
	for _, name := range []string{"/etc/passwd", `\\host\share`, "../other", "sub/../../other", "D:secret", "."} {
		if set.Read(t.Context(), "review", name) != nil {
			t.Fatal("invalid relative path accepted", name)
		}
	}
	outside := t.TempDir()
	write(t, outside, "private.txt", "private")
	if err := os.Symlink(filepath.Join(outside, "private.txt"), filepath.Join(dir, "review", "escape.txt")); err != nil {
		t.Log("symlink privileges unavailable", err)
		return
	}
	if set.Read(t.Context(), "review", "escape.txt") != nil {
		t.Fatal("linked file escaped skill folder")
	}
	if err := os.Symlink(filepath.Join(dir, "review"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	set = Load(t.Context(), nil, []string{dir})
	if set.Get("linked") == nil {
		t.Fatal("top-level linked skill not offered")
	}
}
