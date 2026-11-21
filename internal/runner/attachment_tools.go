package runner

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/sdk"
)

const maxZipListedEntries = 500

func (turn *turn) readAttachmentTool() tool {
	entry := specification("read_attachment",
		"Inspect a project file attachment by fileId. Lists entries or reads a specific entry inside a ZIP archive, previews worksheets in an XLSX file, pages through a text attachment by offset/limit, or returns metadata for other binary files.",
		args{
			"fileId": field("string"),
			"entry":  args{"type": "string", "description": "Path of a file inside a ZIP archive to read. Omit to list entries."},
			"sheet":  args{"type": "integer", "description": "0-based worksheet index for an XLSX spreadsheet (default 0)."},
			"offset": field("integer"),
			"limit":  field("integer"),
		},
		[]string{"fileId"}, nil)
	entry.call = turn.readAttachment
	return entry
}

func (turn *turn) saveAttachmentTool() tool {
	return specification("save_attachment",
		"Save a project file attachment (by fileId) to a workspace path so commands or scripts can inspect, extract, or use it. Pass source to select a writable source. Remove temporary copies before committing if they do not belong in the repository.",
		args{
			"fileId": field("string"),
			"path":   field("string"),
			"source": field("string"),
		},
		[]string{"fileId", "path"}, turn.saveAttachment)
}

func cleanAttachmentID(raw string) (string, error) {
	id := strings.TrimPrefix(strings.TrimSpace(raw), "file:")
	if id == "" || strings.ContainsAny(id, "/\\") {
		return "", errors.New("fileId is required")
	}
	return id, nil
}

func normalizeZipEntry(raw string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if trimmed == "" {
		return ""
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+trimmed), "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func isZipArchive(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06"))
}

func formatAttachmentBytes(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func (turn *turn) readAttachment(ctx context.Context, input args) (string, []any, error) {
	id, err := cleanAttachmentID(text(input, "fileId"))
	if err != nil {
		return "", nil, err
	}
	store := files.Store{Events: turn.Repository.Store}
	info, err := store.Info(ctx, turn.conversation.ProjectID, id)
	if err != nil {
		return "", nil, err
	}
	if info == nil {
		return "", nil, errors.New("attachment not found in this project")
	}
	entryName := normalizeZipEntry(text(input, "entry"))

	if info.MIME == files.SpreadsheetMIME && entryName == "" {
		return turn.readSpreadsheetAttachment(ctx, store, id, info, input)
	}

	if files.IsImage(info.MIME) {
		if entryName != "" {
			return "", nil, errors.New("entry is only supported for ZIP archives")
		}
		if info.Size <= 5<<20 && info.MIME != "image/avif" {
			return fmt.Sprintf("Attached image %s (%s, %s).", id, info.MIME, formatAttachmentBytes(info.Size)),
				[]any{args{"fileId": id, "name": id, "mime": info.MIME, "size": info.Size}}, nil
		}
		return fmt.Sprintf("Attachment %s (%s, %s). Image cannot be shown inline to the model.", id, info.MIME, formatAttachmentBytes(info.Size)), nil, nil
	}

	content, err := store.Read(ctx, id)
	if err != nil {
		return "", nil, err
	}

	if info.MIME == "text/plain" {
		if entryName != "" {
			return "", nil, errors.New("entry is only supported for ZIP archives")
		}
		return formatNumberedLines(content.Data, input), nil, nil
	}

	if isZipArchive(content.Data) {
		reader, zipErr := zip.NewReader(bytes.NewReader(content.Data), int64(len(content.Data)))
		if zipErr == nil {
			if entryName == "" {
				return formatZipListing(id, info, reader), nil, nil
			}
			return turn.readZipEntry(ctx, store, id, reader, entryName, input)
		}
	}

	if entryName != "" {
		return "", nil, errors.New("entry is only supported for ZIP archives")
	}

	sum := sha256.Sum256(content.Data)
	return fmt.Sprintf("Attachment %s (%s, %s, sha256:%x). Binary file cannot be read inline as text. In a writable thread workspace, use save_attachment to copy it to a workspace path and inspect or process it with run_command.",
		id, info.MIME, formatAttachmentBytes(info.Size), sum[:6]), nil, nil
}

func (turn *turn) readSpreadsheetAttachment(ctx context.Context, store files.Store, id string, info *files.Info, input args) (string, []any, error) {
	if info.Size > files.MaxPreviewBytes {
		return fmt.Sprintf("Spreadsheet %s (%s) exceeds the %d MB inline preview limit. In a writable thread workspace, use save_attachment to copy it and inspect it with a script.",
			id, formatAttachmentBytes(info.Size), files.MaxPreviewBytes/1024/1024), nil, nil
	}
	index := 0
	if raw, given := input["sheet"]; given && raw != nil {
		value, err := artifactInteger(raw, "sheet")
		if err != nil {
			return "", nil, err
		}
		if value < 0 {
			return "", nil, errors.New("sheet must be 0 or greater")
		}
		index = int(value)
	}
	preview, err := store.PreviewSheet(ctx, id, index)
	if err != nil {
		return "", nil, err
	}
	var out strings.Builder
	sheetLabels := make([]string, len(preview.Sheets))
	for i, name := range preview.Sheets {
		sheetLabels[i] = fmt.Sprintf("%d: %s", i, name)
	}
	currentName := ""
	if index < len(preview.Sheets) {
		currentName = preview.Sheets[index]
	}
	fmt.Fprintf(&out, "Spreadsheet %s (%s). Sheets: [%s].\nShowing sheet %d (%q):", id, formatAttachmentBytes(info.Size), strings.Join(sheetLabels, ", "), index, currentName)
	if len(preview.Rows) == 0 {
		out.WriteString("\n(empty sheet)")
		return out.String(), nil, nil
	}
	shown := 0
	for i, row := range preview.Rows {
		rowNumber := i + 1
		if i < len(preview.RowNumbers) {
			rowNumber = preview.RowNumbers[i]
		}
		line := fmt.Sprintf("\n%5d\t%s", rowNumber, strings.Join(row, "\t"))
		if out.Len()+len(line) > 60000 {
			fmt.Fprintf(&out, "\n[Output truncated after %d rows; use save_attachment in a writable workspace to inspect the full workbook with a script.]", shown)
			return out.String(), nil, nil
		}
		out.WriteString(line)
		shown++
	}
	if preview.Truncated {
		out.WriteString("\n[Preview truncated; use save_attachment in a writable workspace to process the full workbook with a script.]")
	}
	return out.String(), nil, nil
}

func formatZipListing(id string, info *files.Info, reader *zip.Reader) string {
	var totalUncompressed uint64
	for _, f := range reader.File {
		totalUncompressed += f.UncompressedSize64
	}
	var out strings.Builder
	fmt.Fprintf(&out, "ZIP archive %s (%s, %d entries, %s uncompressed). Pass entry to read_attachment to inspect a file inside, or use save_attachment in a writable workspace to extract it.",
		id, formatAttachmentBytes(info.Size), len(reader.File), formatAttachmentBytes(int64(min(totalUncompressed, uint64(1<<62)))))
	listed := 0
	for _, f := range reader.File {
		if listed >= maxZipListedEntries {
			fmt.Fprintf(&out, "\n[... %d more entries omitted]", len(reader.File)-listed)
			break
		}
		var line string
		if f.FileInfo().IsDir() {
			line = fmt.Sprintf("\n- %s [dir]", f.Name)
		} else {
			line = fmt.Sprintf("\n- %s (%s)", f.Name, formatAttachmentBytes(int64(min(f.UncompressedSize64, uint64(1<<62)))))
		}
		if out.Len()+len(line) > 60000 {
			fmt.Fprintf(&out, "\n[... %d more entries omitted]", len(reader.File)-listed)
			break
		}
		out.WriteString(line)
		listed++
	}
	return out.String()
}

func (turn *turn) readZipEntry(ctx context.Context, store files.Store, id string, reader *zip.Reader, entryName string, input args) (string, []any, error) {
	var target *zip.File
	for _, f := range reader.File {
		if normalizeZipEntry(f.Name) == entryName {
			target = f
			break
		}
	}
	if target == nil {
		return "", nil, fmt.Errorf("entry %q not found in archive %s", entryName, id)
	}
	if target.FileInfo().IsDir() {
		return "", nil, fmt.Errorf("%q is a directory in archive %s", entryName, id)
	}
	image := workspaceImageType(entryName)
	maxBytes := int64(1 << 20)
	if image != "" {
		maxBytes = 5 << 20
	}
	rc, err := target.Open()
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > maxBytes {
		return "", nil, fmt.Errorf("entry %q exceeds the %d MiB inline read limit; use save_attachment in a writable workspace to extract and inspect it with a command or script", entryName, maxBytes>>20)
	}
	if image != "" && http.DetectContentType(data) == image {
		saved, err := store.Save(ctx, turn.conversation.ProjectID, image, data, false)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Attached %s from %s (%s).", entryName, id, image), []any{attachment(saved, entryName)}, nil
	}
	if bytes.ContainsRune(data, 0) || !utf8.Valid(data) {
		detected := http.DetectContentType(data)
		return fmt.Sprintf("Entry %q in %s is a binary file (%s, %s). Use save_attachment in a writable workspace to extract and inspect it.",
			entryName, id, detected, formatAttachmentBytes(int64(len(data)))), nil, nil
	}
	return formatNumberedLines(data, input), nil, nil
}

func (turn *turn) saveAttachment(ctx context.Context, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	id, err := cleanAttachmentID(text(input, "fileId"))
	if err != nil {
		return "", err
	}
	dest, err := editPath(text(input, "path"))
	if err != nil {
		return "", err
	}
	store := files.Store{Events: turn.Repository.Store}
	info, err := store.Info(ctx, thread.ProjectID, id)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", errors.New("attachment not found in this project")
	}
	workspace, err := turn.editWorkspace(ctx, thread, input)
	if err != nil {
		return "", err
	}
	content, err := store.Read(ctx, id)
	if err != nil {
		return "", err
	}
	turn.instructionsDirty = true
	if writer, ok := workspace.(sdk.WorkspaceBytes); ok {
		err = writer.WriteBytes(ctx, dest, content.Data)
	} else {
		err = workspace.WriteFile(ctx, dest, string(content.Data))
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved attachment %s (%s, %s) to %s.", id, info.MIME, formatAttachmentBytes(int64(len(content.Data))), dest), nil
}
