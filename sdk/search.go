package sdk

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/dlclark/regexp2/v2"
	"golang.org/x/text/encoding/unicode"
)

const MaxSearchedBytes = 1024 * 1024
const MaxWalked = 20_000
const MaxMatchLines = 300
const MaxFileMatches = 50

func Walk(ctx context.Context, source SourceView, path string) (FileListing, error) {
	ignored := map[string]bool{".git": true, "node_modules": true, ".ariadne": true}
	if data, err := source.ReadBytes(ctx, ".gitignore", MaxSearchedBytes); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "/"), "/")
			if name != "" && !strings.ContainsAny(name, "*?[!#/") {
				ignored[name] = true
			}
		}
	}
	root := strings.ReplaceAll(path, "\\", "/")
	if strings.HasPrefix(root, ".") {
		root = strings.TrimPrefix(strings.TrimPrefix(root, "."), "/")
	}
	root = strings.TrimSuffix(root, "/")
	files := []string{}
	dirs := []string{root}
	for next := 0; next < len(dirs); next++ {
		if err := ctx.Err(); err != nil {
			return FileListing{}, err
		}
		dir := dirs[next]
		at := dir
		if at == "" {
			at = "."
		}
		entries, err := source.List(ctx, at)
		if err != nil {
			if dir == root && root != "" {
				files = append(files, root)
			}
			continue
		}
		for _, entry := range entries {
			if ignored[entry.Name] {
				continue
			}
			if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\\x00") {
				continue
			}
			full := entry.Name
			if dir != "" {
				full = dir + "/" + full
			}
			if entry.Kind == "directory" {
				if len(dirs) >= MaxWalked {
					return FileListing{Files: files, Partial: true}, nil
				}
				dirs = append(dirs, full)
			} else {
				files = append(files, full)
			}
			if len(files) >= MaxWalked {
				return FileListing{Files: files, Partial: true}, nil
			}
		}
	}
	return FileListing{Files: files}, nil
}
func GlobToRegexp(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > 64<<10 {
		return nil, fmt.Errorf("file glob exceeds 64 KiB")
	}
	glob := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"), "./")
	if !strings.ContainsRune(glob, '/') {
		glob = "**/" + glob
	}
	var source strings.Builder
	source.WriteByte('^')
	braces := 0
	for i := 0; i < len(glob); i++ {
		ch := glob[i]
		switch {
		case ch == '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				if i+2 < len(glob) && glob[i+2] == '/' {
					source.WriteString("(?:[^/]*/)*")
					i += 2
				} else {
					source.WriteString(".*")
					i++
				}
			} else {
				source.WriteString("[^/]*")
			}
		case ch == '?':
			source.WriteString("[^/]")
		case ch == '{':
			braces++
			source.WriteString("(?:")
		case ch == '}' && braces > 0:
			braces--
			source.WriteByte(')')
		case ch == ',' && braces > 0:
			source.WriteByte('|')
		case ch == '[':
			close := strings.IndexByte(glob[i+1:], ']')
			if close < 0 {
				source.WriteString(`\[`)
			} else {
				end := i + 1 + close
				set := glob[i+1 : end]
				if strings.HasPrefix(set, "!") {
					set = "^" + set[1:]
				}
				source.WriteByte('[')
				source.WriteString(set)
				source.WriteByte(']')
				i = end
			}
		default:
			if strings.ContainsRune(".+^$()|\\}", rune(ch)) {
				source.WriteByte('\\')
			}
			source.WriteByte(ch)
		}
	}
	source.WriteString(strings.Repeat(")", braces))
	source.WriteByte('$')
	return regexp.Compile(source.String())
}
func SearchFilter(glob string) (func(string) bool, error) {
	if strings.TrimSpace(glob) == "" {
		return func(string) bool { return true }, nil
	}
	match, err := GlobToRegexp(glob)
	if err != nil {
		return nil, err
	}
	return func(line string) bool { file, _, _ := strings.Cut(line, ":"); return match.MatchString(file) }, nil
}
func CappedMatches(lines []string) []string {
	perFile := map[string]int{}
	kept := []string{}
	for _, line := range lines {
		file, _, _ := strings.Cut(line, ":")
		if perFile[file] >= MaxFileMatches {
			continue
		}
		perFile[file]++
		kept = append(kept, line)
		if len(kept) >= MaxMatchLines {
			break
		}
	}
	return kept
}
func SearchByReading(ctx context.Context, source SourceView, pattern, path string, options SearchOptions) (FileSearchResult, error) {
	empty := FileSearchResult{Lines: []string{}}
	if len(pattern) > 64<<10 {
		empty.Error = new("search pattern exceeds 64 KiB")
		return empty, nil
	}
	flags := regexp2.ECMAScript
	if options.IgnoreCase {
		flags |= regexp2.IgnoreCase
	}
	translated := legacyPattern(pattern)
	if strings.Contains(strings.ToLower(translated), `\ud`) {
		// regexp2 v2.13's unanchored first-character prefilter serializes
		// surrogate class endpoints through UTF-8. This always-true position
		// assertion bypasses that prefilter without consuming input or changing
		// captures, anchors, lookbehinds or the current match position.
		translated = `(?:^|(?<=[\s\S]))(?:` + translated + `)`
	}
	match, err := regexp2.Compile(translated, flags)
	if err != nil {
		empty.Error = new(err.Error())
		return empty, nil
	}
	// JavaScript backreferences and lookarounds, with a bound on a pathological line.
	match.MatchTimeout = 100 * time.Millisecond
	keep, err := SearchFilter(options.Glob)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	found, err := Walk(ctx, source, path)
	if err != nil {
		return empty, err
	}
	lines := []string{}
	for _, file := range found.Files {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if len(lines) >= MaxMatchLines {
			break
		}
		if !keep(file + ":") {
			continue
		}
		data, err := source.ReadBytes(ctx, file, MaxSearchedBytes)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		data, err = unicode.UTF8BOM.NewDecoder().Bytes(data)
		if err != nil {
			continue
		}
		inFile := 0
		for i, line := range strings.Split(string(data), "\n") {
			if err := ctx.Err(); err != nil {
				return empty, err
			}
			units := utf16.Encode([]rune(line))
			input := make([]rune, len(units))
			for i, unit := range units {
				input[i] = rune(unit)
			}
			matched, err := match.MatchRunes(input)
			if err != nil {
				empty.Error = new(fmt.Sprintf("search expression failed: %s", err))
				return empty, nil
			}
			if !matched {
				continue
			}
			lines = append(lines, file+":"+strconv.Itoa(i+1)+":"+strings.TrimSuffix(line, "\r"))
			inFile++
			if inFile >= MaxFileMatches || len(lines) >= MaxMatchLines {
				break
			}
		}
	}
	return FileSearchResult{Lines: lines}, nil
}

// JavaScript RegExp without /u consumes UTF-16 units. regexp2 accepts surrogate
// runes through MatchRunes; escape supplementary literals in the pattern too.
func legacyPattern(pattern string) string {
	result := make([]byte, 0, len(pattern))
	slashes := 0
	for _, r := range pattern {
		if r > 0xffff {
			if slashes%2 != 0 {
				result = result[:len(result)-1]
			}
			high, low := utf16.EncodeRune(r)
			result = fmt.Appendf(result, `\u%04X\u%04X`, high, low)
		} else {
			result = append(result, string(r)...)
		}
		if r == '\\' {
			slashes++
		} else {
			slashes = 0
		}
	}
	return string(result)
}
