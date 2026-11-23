package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) readFileTool() tool {
	entry := specification("read_file", "Read numbered lines of a text file up to 1 MiB. Pass source to select a named file source. Use the returned offset for more lines. Give large logs, traces, source maps and generated bundles to a script for a targeted summary. Images are attached to the result.",
		args{"path": field("string"), "source": field("string"), "reference": field("string"), "offset": field("integer"), "limit": field("integer")},
		[]string{"path"}, nil)
	entry.call = turn.readFile
	return entry
}

func workspaceImageType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

func (turn *turn) readFile(ctx context.Context, input args) (string, []any, error) {
	path := text(input, "path")
	if path == "" {
		return "", nil, errors.New("path is required")
	}
	view, err := turn.source(ctx, input)
	if err != nil {
		return "", nil, err
	}
	image := workspaceImageType(path)
	maxBytes := int64(1 << 20)
	if image != "" {
		maxBytes = 5 << 20
	}
	data, err := view.ReadBytes(ctx, path, maxBytes)
	if err != nil {
		var tooLarge *sdk.FileTooLargeError
		if image == "" && errors.As(err, &tooLarge) {
			return "", nil, fmt.Errorf("%w. Offsets apply only within the text read limit. For a large trace, log, source map or bundle, have a workspace script read the file and print only the needed fields or a small summary", err)
		}
		return "", nil, err
	}
	if image != "" && http.DetectContentType(data) == image {
		store := files.Store{Events: turn.Repository.Store}
		saved, err := store.Save(ctx, turn.conversation.ProjectID, image, data, false)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Attached %s (%s).", path, image), []any{attachment(saved, path)}, nil
	}
	if bytes.ContainsRune(data, 0) {
		return "", nil, fmt.Errorf("%s is a binary file", path)
	}
	if len(data) > 1<<20 {
		return "", nil, errors.New("the text file exceeds the 1 MiB read limit")
	}
	lines := textLines(data)
	offset, limit := readFileRange(len(lines), input)
	if !inResearchContext(ctx) {
		sourceID := fileSourceIdentity(view, input)
		if turn.conversation.Role == "thread" {
			if _, writable := view.(sdk.Workspace); writable {
				turn.fileTracker().noteFile(turn.conversation.ID, sourceID, path, data)
			}
		}
		if turn.keepsResults() && turn.fileTracker().noteRead(turn.conversation.ID, sourceID, path, offset, limit, data) {
			return fmt.Sprintf("%s is unchanged since you read it with the same offset and limit; that result, earlier in this conversation, is still current.", path), nil, nil
		}
	}
	return formatNumberedRange(lines, offset, limit), nil, nil
}

func textLines(data []byte) []string {
	content := strings.TrimPrefix(strings.ToValidUTF8(string(data), "\ufffd"), "\ufeff")
	return strings.Split(content, "\n")
}

func readFileRange(lineCount int, input args) (int, int) {
	offset, limit := 1, 240
	if input["offset"] != nil {
		offset = int(max(1, min(asNumber(input["offset"]), float64(lineCount+1))))
	}
	if input["limit"] != nil {
		limit = int(max(1, min(asNumber(input["limit"]), 800)))
	}
	return offset, limit
}

func formatNumberedLines(data []byte, input args) string {
	content := strings.TrimPrefix(strings.ToValidUTF8(string(data), "�"), "\ufeff")
	lines := strings.Split(content, "\n")
	offset, limit := readFileRange(len(lines), input)
	return formatNumberedRange(lines, offset, limit)
}

func formatNumberedRange(lines []string, offset, limit int) string {
	var out strings.Builder
	end := offset - 1
	for ; end < min(len(lines), offset-1+limit) && out.Len() < 60000; end++ {
		if end != offset-1 {
			out.WriteByte('\n')
		}
		line := strings.TrimSuffix(lines[end], "\r")
		prefix := textutil.HeadUnits(line, 2000)
		fmt.Fprintf(&out, "%5d\t%s", end+1, prefix)
		if prefix != line {
			out.WriteString(" […]")
		}
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "\n[Lines %d–%d of %d. Continue at offset %d.]", offset, end, len(lines), end+1)
	}
	return out.String()
}

func attachment(saved files.Stored, name string) args {
	return args{"fileId": saved.ID, "name": name, "mime": saved.MIME, "size": saved.Size}
}

func (turn *turn) messageAttachments(ctx context.Context, ids []string) ([]any, error) {
	store := files.Store{Events: turn.Repository.Store}
	project := turn.conversation.ProjectID
	items := []any{}
	for _, value := range ids {
		id := strings.TrimPrefix(value, "file:")
		if strings.HasPrefix(id, "fil_") && !strings.ContainsAny(id, "/\\") {
			info, err := store.Info(ctx, project, id)
			if err != nil {
				return nil, err
			}
			if info == nil {
				return nil, errors.New("the attachment must belong to this project")
			}
			items = append(items, args{"fileId": id})
			continue
		}
		if turn.Workspaces == nil || turn.conversation.Role != "thread" {
			return nil, errors.New("give a project file ID")
		}
		path, err := editPath(value)
		if err != nil {
			return nil, err
		}
		view, err := turn.source(ctx, args{})
		if err != nil {
			return nil, err
		}
		data, err := view.ReadBytes(ctx, path, files.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		media := http.DetectContentType(data)
		if mediaType, _, err := mime.ParseMediaType(media); err == nil {
			media = mediaType
		}
		if media == "application/octet-stream" && strings.EqualFold(filepath.Ext(path), ".avif") &&
			len(data) >= 12 && string(data[4:12]) == "ftypavif" {
			media = "image/avif"
		}
		saved, err := store.Save(ctx, project, media, data, true)
		if err != nil {
			return nil, err
		}
		items = append(items, attachment(saved, filepath.Base(path)))
	}
	return items, nil
}

var imageLink = regexp.MustCompile(`(!?\[[^\]\n]*\]\(\s*)(<[^>\n]+>|[^\s()<>]+)((?:\s+"[^"\n]*")?\s*\))`)
var messageCode = regexp.MustCompile("(?s)```.*?(?:```|$)|`[^`\\n]*`")

func (turn *turn) keepMessageImages(ctx context.Context, content string) string {
	if turn.Workspaces == nil || turn.conversation.Role != "thread" || !strings.Contains(content, "](") {
		return content
	}
	store := files.Store{Events: turn.Repository.Store}
	code := messageCode.FindAllStringIndex(content, -1)
	pathIDs := map[string]string{}
	var updated strings.Builder
	last, codeIndex := 0, 0
	for _, match := range imageLink.FindAllStringSubmatchIndex(content, -1) {
		for codeIndex < len(code) && code[codeIndex][1] <= match[0] {
			codeIndex++
		}
		if codeIndex < len(code) && code[codeIndex][0] < match[1] {
			continue
		}
		given := strings.TrimSpace(strings.Trim(content[match[4]:match[5]], "<>"))
		decoded, err := url.PathUnescape(given)
		if err != nil {
			continue
		}
		if strings.ContainsRune(decoded, ':') || strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "#") || workspaceImageType(decoded) == "" {
			continue
		}
		if _, err := editPath(decoded); err != nil {
			continue
		}
		id, known := pathIDs[decoded]
		if !known && len(pathIDs) < 20 {
			pathIDs[decoded] = ""
			view, err := turn.source(ctx, args{})
			if err != nil {
				continue
			}
			data, err := view.ReadBytes(ctx, decoded, files.MaxImageBytes)
			if media := workspaceImageType(decoded); err == nil && http.DetectContentType(data) == media {
				if saved, err := store.Save(ctx, turn.conversation.ProjectID, media, data, false); err == nil {
					id, pathIDs[decoded] = saved.ID, saved.ID
				}
			}
		}
		if id != "" {
			updated.WriteString(content[last:match[4]])
			updated.WriteString("file:" + id)
			last = match[5]
		}
	}
	if last == 0 {
		return content
	}
	updated.WriteString(content[last:])
	return updated.String()
}

func (turn *turn) sendNativeMessage(ctx context.Context, input args, content string, attachments []any) (core.MessageView, error) {
	conversation := turn.conversation
	message := core.MessageInput{ConversationID: conversation.ID, Author: conversation.Role,
		Content: turn.keepMessageImages(ctx, content), Attachments: attachments, ReplyTo: optional(input, "replyTo")}
	if turn.draft != nil {
		message.MessageID = &turn.draft.MessageID
	}
	return turn.Repository.PostMessage(ctx, conversation.ProjectID, message)
}
