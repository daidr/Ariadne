package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/textedit"
	"github.com/daidr/Ariadne/sdk"
)

const maxEditBytes = 1 << 20

func (turn *turn) editorTools() []tool {
	edit := args{"old": field("string"), "new": field("string"), "all": field("boolean")}
	return []tool{
		specification("edit_file", "Replace a unique part of a workspace file. Give old and new, or edits applied in order. Use all to replace every occurrence.",
			args{"path": field("string"), "old": field("string"), "new": field("string"), "all": field("boolean"),
				"edits": arrayOf(args{"type": "object", "properties": edit, "required": []string{"old", "new"}})},
			[]string{"path"}, turn.editFile),
		specification("apply_patch", "Apply a patch to workspace files. Use *** Begin Patch, *** Add File: path, *** Update File: path, *** Move to: path or *** Delete File: path, then *** End Patch. Each hunk can start with @@ and uses space for context, - for removal, + for addition. Hunks are checked before writing.",
			args{"input": field("string")}, []string{"input"}, turn.applyPatch),
	}
}

func editPath(raw string) (string, error) {
	name, err := sdk.ViewPath(raw)
	if err != nil || name == "." || len(name) > 4096 || strings.ContainsAny(name, "\r\n\x00") {
		return "", errors.New("a relative file path within the workspace is required")
	}
	for _, part := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		if strings.EqualFold(part, ".git") {
			return "", errors.New("Git metadata cannot be edited here")
		}
	}
	return name, nil
}

func editableText(ctx context.Context, workspace sdk.Workspace, path string) (string, error) {
	data, err := workspace.ReadBytes(ctx, path, maxEditBytes)
	if err != nil {
		return "", err
	}
	if !files.IsText(data) {
		return "", fmt.Errorf("%s is not a UTF-8 text file", path)
	}
	return string(data), nil
}

func (turn *turn) editFile(ctx context.Context, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	path, err := editPath(text(input, "path"))
	if err != nil {
		return "", err
	}
	edits := []args{input}
	if input["edits"] != nil {
		edits, err = artifactObjects(input["edits"])
		if err != nil || len(edits) == 0 || len(edits) > 128 {
			return "", errors.New("edits must contain 1 to 128 replacements")
		}
	}
	workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
	if err != nil {
		return "", err
	}
	before, err := editableText(ctx, workspace, path)
	if err != nil {
		return "", err
	}
	edited, total := before, 0
	for i, edit := range edits {
		old, _ := edit["old"].(string)
		replacement, ok := edit["new"].(string)
		all, _ := edit["all"].(bool)
		if !ok {
			return "", fmt.Errorf("edit %d expects new text", i+1)
		}
		var count int
		edited, count, err = textedit.Replace(edited, old, replacement, all, maxEditBytes)
		if err != nil {
			return "", fmt.Errorf("edit %d in %s: %w", i+1, path, err)
		}
		total += count
	}
	if current, err := editableText(ctx, workspace, path); err != nil || current != before {
		return "", fmt.Errorf("%s changed while preparing the edit", path)
	}
	if err := workspace.WriteFile(ctx, path, edited); err != nil {
		return "", err
	}
	turn.instructionsDirty = true
	return fmt.Sprintf("Replaced %d occurrence(s) in %s.", total, path) + turn.checkAfterEdit(ctx, thread, workspace, []string{path}), nil
}

type fileChange struct {
	path          string
	before, after *string
}

func (turn *turn) applyPatch(ctx context.Context, input args) (string, error) {
	entries, err := textedit.ParsePatch(text(input, "input"))
	if err != nil {
		return "", err
	}
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
	if err != nil {
		return "", err
	}
	editor, ok := workspace.(sdk.WorkspaceFiles)
	if !ok {
		return "", errors.New("the workspace does not support file patches")
	}
	staged := map[string]*fileChange{}
	var ordered []*fileChange
	read := func(raw string) (*fileChange, error) {
		name, err := editPath(raw)
		if err != nil {
			return nil, err
		}
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if existing := staged[key]; existing != nil {
			return existing, nil
		}
		record := &fileChange{path: name}
		exists, err := editor.FileExists(ctx, name)
		if err != nil {
			return nil, err
		}
		if exists {
			value, err := editableText(ctx, workspace, name)
			if err != nil {
				return nil, err
			}
			record.before, record.after = &value, &value
		}
		staged[key] = record
		ordered = append(ordered, record)
		return record, nil
	}
	size := 0
	for _, entry := range entries {
		change, err := read(entry.Path)
		if err != nil {
			return "", err
		}
		switch entry.Action {
		case "Add":
			if change.after != nil {
				return "", fmt.Errorf("%s already exists. Use Update File to change it", change.path)
			}
			change.after = &entry.Text
		case "Delete":
			if change.after == nil {
				return "", fmt.Errorf("%s does not exist", change.path)
			}
			change.after = nil
		case "Update":
			if change.after == nil {
				return "", fmt.Errorf("%s does not exist", change.path)
			}
			content, err := entry.Apply(*change.after, maxEditBytes)
			if err != nil {
				return "", err
			}
			target := change
			if entry.Target != "" {
				target, err = read(entry.Target)
				if err != nil {
					return "", err
				}
				if target != change {
					if target.after != nil {
						return "", fmt.Errorf("the move destination %s already exists", target.path)
					}
					change.after = nil
				}
			}
			target.after = &content
		}
		if change.before != nil {
			size += len(*change.before)
		}
		if size > 16<<20 {
			return "", errors.New("the patch touches too much data")
		}
	}
	for _, change := range ordered {
		exists, err := editor.FileExists(ctx, change.path)
		if err != nil {
			return "", err
		}
		if exists != (change.before != nil) {
			return "", fmt.Errorf("%s changed while preparing the patch", change.path)
		}
		if exists {
			current, err := workspace.ReadBytes(ctx, change.path, maxEditBytes)
			if err != nil || !bytes.Equal(current, []byte(*change.before)) {
				return "", fmt.Errorf("%s changed while preparing the patch", change.path)
			}
		}
	}
	var written, done []string
	turn.instructionsDirty = true
	for _, change := range ordered {
		if change.after == nil || change.before != nil && *change.before == *change.after {
			continue
		}
		if err := workspace.WriteFile(ctx, change.path, *change.after); err != nil {
			return "Patch stopped while writing " + change.path + ". Changes already applied: " + strings.Join(done, ", "), err
		}
		written = append(written, change.path)
		done = append(done, "wrote "+change.path)
	}
	for _, change := range ordered {
		if change.after != nil || change.before == nil {
			continue
		}
		if err := editor.RemoveFile(ctx, change.path); err != nil {
			return "Patch stopped while removing " + change.path + ". Changes already applied: " + strings.Join(done, ", "), err
		}
		done = append(done, "deleted "+change.path)
	}
	return strings.Join(done, "\n") + turn.checkAfterEdit(ctx, thread, workspace, written), nil
}
