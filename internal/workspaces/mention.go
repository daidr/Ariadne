package workspaces

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const MentionFiles = 30
const maxMentionFiles = 20000
const maxMentionBytes = 2 << 20

var skippedMentionDirs = map[string]bool{"node_modules": true, ".git": true, "dist": true, "build": true, "target": true, ".venv": true, "__pycache__": true}

type mentionList struct{ files []string }

func (list *mentionList) Close() error { list.files = nil; return nil }

func MentionRank(path, query string) *int {
	rank := mentionRank(path, textutil.Lower(query))
	if rank < 0 {
		return nil
	}
	return &rank
}
func mentionRank(path, q string) int {
	if q == "" {
		return strings.Count(path, "/") + 1
	}
	lower := textutil.Lower(path)
	name := lower[strings.LastIndexByte(lower, '/')+1:]
	if strings.HasPrefix(name, q) {
		return 0
	}
	if strings.Contains(name, q) {
		return 1
	}
	if strings.Contains(lower, q) {
		return 2
	}
	return -1
}
func selectMentions(files []string, query string) []string {
	type candidate struct {
		path         string
		rank, length int
	}
	best := []candidate{}
	query = textutil.Lower(query)
	for _, file := range files {
		rank := mentionRank(file, query)
		if rank < 0 {
			continue
		}
		length := 0
		for _, r := range file {
			length += utf16.RuneLen(r)
		}
		item := candidate{file, rank, length}
		at := len(best)
		for at > 0 && (item.rank < best[at-1].rank || item.rank == best[at-1].rank && item.length < best[at-1].length) {
			at--
		}
		if at >= MentionFiles {
			continue
		}
		if len(best) < MentionFiles {
			best = append(best, candidate{})
		}
		copy(best[at+1:], best[at:len(best)-1])
		best[at] = item
	}
	out := make([]string, 0, len(best))
	for _, item := range best {
		out = append(out, item.path)
	}
	return out
}

// WalkMentionFiles bounds files, directories and retained path bytes, including
// trees containing only empty directories. Directory errors remain best-effort.
func WalkMentionFiles(ctx context.Context, view sdk.SourceView) ([]string, error) {
	files := []string{}
	queue := []string{""}
	bytes := 0
	directories := 1
	queueBytes := 0
	for at := 0; at < len(queue) && len(files) < maxMentionFiles; at++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := queue[at]
		queueBytes -= len(dir)
		queue[at] = ""
		where := dir
		if where == "" {
			where = "."
		}
		entries, err := view.List(ctx, where)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\") {
				continue
			}
			path := entry.Name
			if dir != "" {
				path = dir + "/" + entry.Name
			}
			if entry.Kind == "directory" {
				if !skippedMentionDirs[entry.Name] && directories < maxMentionFiles && queueBytes+len(path) <= maxMentionBytes {
					queue = append(queue, strings.Clone(path))
					queueBytes += len(path)
					directories++
				}
			} else {
				if bytes+len(path) > maxMentionBytes {
					return files, nil
				}
				files = append(files, strings.Clone(path))
				bytes += len(path)
				if len(files) >= maxMentionFiles {
					break
				}
			}
		}
	}
	return files, ctx.Err()
}
func (manager *Manager) mentionFilesOf(ctx context.Context, project, threadID string) ([]string, error) {
	var view sdk.SourceView
	if threadID != "" {
		thread, err := manager.options.Repository.GetThread(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if thread.Workspace != nil && !thread.Workspace.Removed && manager.Available(ctx, project) {
			workspace, err := manager.ForThread(ctx, project, threadID)
			if err != nil {
				return nil, err
			}
			if branch := workspace.Info().Branch; branch != nil && *branch != "" {
				result, err := workspace.ExecFile(ctx, []string{"git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"}, sdk.ExecOptions{Timeout: 15 * time.Second})
				if err == nil && result.ExitCode == 0 {
					out := []string{}
					seen := map[string]bool{}
					bytes := 0
					for value := range strings.SplitSeq(result.Stdout, "\x00") {
						if value == "" || seen[value] {
							continue
						}
						if len(out) >= maxMentionFiles || bytes+len(value) > maxMentionBytes {
							break
						}
						value = strings.Clone(value)
						out = append(out, value)
						seen[value] = true
						bytes += len(value)
					}
					return out, nil
				}
			}
			view = workspace
		}
	}
	if view == nil {
		var err error
		view, err = manager.View(ctx, project)
		if err != nil {
			return nil, err
		}
	}
	return WalkMentionFiles(ctx, view)
}
func (manager *Manager) MentionFiles(ctx context.Context, project, threadID, query string) ([]string, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return nil, err
	}
	key := config.key
	if threadID != "" {
		thread, err := manager.options.Repository.GetThread(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if thread.ProjectID != project {
			return nil, changedSource()
		}
		value, err := digest(thread.Workspace)
		if err != nil {
			return nil, err
		}
		key += "/" + value
	}
	// The generation changes even under constant access. Idle expiry alone would
	// otherwise keep a hot list forever and hide newly created files.
	key += "/" + strconv.FormatInt(manager.options.Now().UnixMilli()/30000, 10)
	lease, err := manager.mentions.Acquire(ctx, workspaceID(project, threadID), key, func(ctx context.Context) (*mentionList, error) {
		files, err := manager.mentionFilesOf(ctx, project, threadID)
		if err != nil {
			return nil, err
		}
		return &mentionList{files: files}, nil
	})
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	return selectMentions(lease.Value.files, query), nil
}
