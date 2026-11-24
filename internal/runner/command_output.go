package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/daidr/Ariadne/sdk"
	"github.com/dlclark/regexp2/v2"
	"golang.org/x/text/encoding/unicode"
)

const (
	toolOutputSourceName       = "tool-output"
	toolOutputDirName          = "tool-output"
	toolOutputMaxAge           = 7 * 24 * time.Hour
	toolOutputPruneInterval    = time.Hour
	maxSavedCommandOutputBytes = 4 << 20 // 4 MiB
)

var lastToolOutputPrune atomic.Int64

func safeToolOutputSegment(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func isSafeToolOutputFile(name string) bool {
	if len(name) < len("cmd-a.txt") || len(name) > 128 {
		return false
	}
	if !strings.HasPrefix(name, "cmd-") || !strings.HasSuffix(name, ".txt") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return !strings.Contains(name, "..")
}

func (turn *turn) toolOutputDir() (string, error) {
	if turn == nil || turn.Runner == nil || strings.TrimSpace(turn.Home) == "" {
		return "", errors.New("tool output storage is unavailable")
	}
	if !safeToolOutputSegment(turn.conversation.ID) {
		return "", errors.New("conversation ID cannot store tool output")
	}
	return filepath.Join(turn.Home, toolOutputDirName, turn.conversation.ID), nil
}

func (turn *turn) toolOutputView() (sdk.SourceView, error) {
	dir, err := turn.toolOutputDir()
	if err != nil {
		return nil, err
	}
	return &toolOutputView{dir: dir, convID: turn.conversation.ID}, nil
}

func (turn *turn) saveCommandOutput(result sdk.ExecResult) string {
	dir, err := turn.toolOutputDir()
	if err != nil {
		return ""
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	var full strings.Builder
	fmt.Fprintf(&full, "Exit code %d\n", result.ExitCode)
	if result.Stdout != "" {
		full.WriteString("--- stdout ---\n")
		full.WriteString(result.Stdout)
		if !strings.HasSuffix(result.Stdout, "\n") {
			full.WriteByte('\n')
		}
	}
	if result.Stderr != "" {
		full.WriteString("--- stderr ---\n")
		full.WriteString(result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			full.WriteByte('\n')
		}
	}
	payload := capSavedCommandOutput(full.String(), maxSavedCommandOutputBytes)
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return ""
	}
	name := fmt.Sprintf("cmd-%d-%s.txt", time.Now().UnixMilli(), hex.EncodeToString(suffix[:]))
	target := filepath.Join(dir, name)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ""
	}
	_, writeErr := io.WriteString(file, payload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(target)
		return ""
	}
	maybePruneToolOutputs(filepath.Join(turn.Home, toolOutputDirName), time.Now())
	return name
}

func capSavedCommandOutput(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const marker = "\n[… saved output capped at 4 MiB …]\n"
	if limit <= len(marker) {
		return trimUTF8Head(text, limit)
	}
	budget := limit - len(marker)
	head := trimUTF8Head(text, budget/2)
	tail := trimUTF8Tail(text, budget-len(head))
	return head + marker + tail
}

func trimUTF8Head(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	i := maxBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

func trimUTF8Tail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	i := len(s) - maxBytes
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

func maybePruneToolOutputs(root string, now time.Time) {
	nowUnix := now.UnixNano()
	prev := lastToolOutputPrune.Load()
	if prev != 0 && time.Duration(nowUnix-prev) < toolOutputPruneInterval {
		return
	}
	if !lastToolOutputPrune.CompareAndSwap(prev, nowUnix) {
		return
	}
	pruneToolOutputDir(root, now, toolOutputMaxAge)
}

func pruneToolOutputDir(root string, now time.Time, maxAge time.Duration) {
	convEntries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, conv := range convEntries {
		if conv.Type()&os.ModeSymlink != 0 || !conv.IsDir() || !safeToolOutputSegment(conv.Name()) {
			continue
		}
		convDir := filepath.Join(root, conv.Name())
		files, err := os.ReadDir(convDir)
		if err != nil {
			continue
		}
		remaining := 0
		for _, entry := range files {
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !isSafeToolOutputFile(entry.Name()) {
				remaining++
				continue
			}
			info, err := entry.Info()
			if err != nil {
				remaining++
				continue
			}
			if now.Sub(info.ModTime()) > maxAge {
				_ = os.Remove(filepath.Join(convDir, entry.Name()))
				continue
			}
			remaining++
		}
		if remaining == 0 {
			_ = os.Remove(convDir)
		}
	}
}

type toolOutputView struct {
	dir    string
	convID string
}

func (v *toolOutputView) Ref() string {
	return toolOutputSourceName + ":" + v.convID
}

func (v *toolOutputView) checkDir() error {
	info, err := os.Lstat(v.dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return &sdk.WorkspacePathError{Path: toolOutputSourceName, Reason: "is unavailable"}
	}
	return nil
}

func (v *toolOutputView) resolveFile(path string) (string, string, os.FileInfo, error) {
	cleaned := strings.TrimSpace(path)
	if !isSafeToolOutputFile(cleaned) {
		return "", "", nil, &sdk.WorkspacePathError{Path: path, Reason: "is outside saved tool outputs"}
	}
	if err := v.checkDir(); err != nil {
		if os.IsNotExist(err) {
			return "", "", nil, fmt.Errorf("saved tool output %q not found", cleaned)
		}
		return "", "", nil, err
	}
	full := filepath.Join(v.dir, cleaned)
	if filepath.Dir(full) != v.dir {
		return "", "", nil, &sdk.WorkspacePathError{Path: path, Reason: "is outside saved tool outputs"}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", nil, &sdk.WorkspacePathError{Path: path, Reason: "is not a regular tool output file"}
	}
	return full, cleaned, info, nil
}

func (v *toolOutputView) ReadBytes(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	full, name, info, err := v.resolveFile(path)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, &sdk.FileTooLargeError{Path: name, Size: info.Size(), Max: maxBytes}
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if maxBytes <= 0 {
		return io.ReadAll(file)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, &sdk.FileTooLargeError{Path: name, Size: int64(len(data)), Max: maxBytes}
	}
	return data, nil
}

func (v *toolOutputView) listNames() ([]string, error) {
	if err := v.checkDir(); err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !isSafeToolOutputFile(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}

func (v *toolOutputView) List(_ context.Context, path string) ([]sdk.DirectoryEntry, error) {
	cleaned := strings.TrimSpace(path)
	if cleaned != "" && cleaned != "." && cleaned != "./" {
		return nil, &sdk.WorkspacePathError{Path: path, Reason: "is outside saved tool outputs"}
	}
	names, err := v.listNames()
	if err != nil {
		return nil, err
	}
	entries := make([]sdk.DirectoryEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, sdk.DirectoryEntry{Name: name, Kind: "file"})
	}
	return entries, nil
}

func (v *toolOutputView) Files(_ context.Context, path string) (sdk.FileListing, error) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" || cleaned == "." || cleaned == "./" {
		names, err := v.listNames()
		if err != nil {
			return sdk.FileListing{}, err
		}
		return sdk.FileListing{Files: names}, nil
	}
	_, name, _, err := v.resolveFile(cleaned)
	if err != nil {
		return sdk.FileListing{}, err
	}
	return sdk.FileListing{Files: []string{name}}, nil
}

func (v *toolOutputView) Search(ctx context.Context, pattern, path string, options sdk.SearchOptions) (sdk.FileSearchResult, error) {
	empty := sdk.FileSearchResult{Lines: []string{}}
	if len(pattern) > 64<<10 {
		empty.Error = new("search pattern exceeds 64 KiB")
		return empty, nil
	}
	flags := regexp2.ECMAScript
	if options.IgnoreCase {
		flags |= regexp2.IgnoreCase
	}
	match, err := regexp2.Compile(pattern, flags)
	if err != nil {
		empty.Error = new(err.Error())
		return empty, nil
	}
	match.MatchTimeout = 100 * time.Millisecond
	keep, err := sdk.SearchFilter(options.Glob)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	listed, err := v.Files(ctx, path)
	if err != nil {
		return empty, err
	}
	lines := []string{}
	more := false
	for _, file := range listed.Files {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if len(lines) >= sdk.MaxMatchLines {
			more = true
			break
		}
		if !keep(file + ":") {
			continue
		}
		data, err := v.ReadBytes(ctx, file, maxSavedCommandOutputBytes)
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
			matched, err := match.MatchRunes(utf16.Decode(units))
			if err != nil {
				empty.Error = new(fmt.Sprintf("search expression failed: %s", err))
				return empty, nil
			}
			if !matched {
				continue
			}
			if inFile >= sdk.MaxFileMatches || len(lines) >= sdk.MaxMatchLines {
				more = true
				break
			}
			lines = append(lines, file+":"+strconv.Itoa(i+1)+":"+strings.TrimSuffix(line, "\r"))
			inFile++
		}
	}
	return sdk.FileSearchResult{Lines: lines, More: more}, nil
}
