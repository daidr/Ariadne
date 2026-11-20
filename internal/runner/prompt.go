package runner

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

type prompt struct {
	system, tools string
	specs         []sdk.Tool
}

func joinSections(sections [][2]string) string {
	var text strings.Builder
	for _, section := range sections {
		text.WriteString(section[1])
	}
	return text.String()
}

func (turn *turn) sections(ctx context.Context, project core.ProjectView, config map[string]any) [][2]string {
	role := "You are an Ariadne discussion thread. Work on this thread's task. Report results or blockers to the coordinator."
	workflow := threadGuide
	if turn.conversation.Role == "coordinator" {
		role = "You are the Ariadne project coordinator. Talk with the user, decide what work is needed, and delegate implementation to discussion threads. The project source is read-only for you."
		workflow = coordinatorGuide
	} else if thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID); err == nil {
		role += "\nThread: " + transcript.Defang(thread.Title)
		if authorID, ok := thread.ReviewOf.(string); ok && authorID != "" {
			workflow = reviewerGuide
			role += "\nYou are reviewing another thread's work. Keep its workspace read-only."
			if author, err := turn.Repository.GetThread(ctx, authorID); err == nil {
				thread = author
				role += "\nAuthor: " + transcript.Defang(author.Title)
			}
		}
		if nodeID, ok := thread.NodeID.(string); ok && nodeID != "" {
			if brief, err := turn.Repository.PlanBrief(ctx, project.ID, nodeID); err == nil && brief != "" {
				role += "\n" + transcript.Fenced("plan node", brief, "").Text
			}
		}
	}
	locale, _ := config["locale"].(string)
	if locale == "" {
		locale = "zh-CN"
	}
	if project.Source == nil && turn.conversation.Role == "thread" {
		role += "\nThe project has no primary workspace. Read-only references may still be available."
	}
	if workflow == threadGuide {
		if source, ok := project.Source.(args); ok && (source["kind"] == "github" || source["kind"] == "gitlab") {
			if project.Settings.Delivery.Mode == "external" {
				workflow += "For external delivery, use push_branch only when the configured workflow calls for publishing your branch. Follow the issue and deployment requirements without merging into the default branch.\n\n"
			} else {
				workflow += "For a hosted repository, use open_pull_request after committing. Use push_branch for later commits, sync_branch for conflicts or rejected pushes, and repo_status or read_check_log for failed checks. Wait for approval when the project needs permission to publish. When the user explicitly asks you in this discussion to merge its PR, use repo_status followed by merge_pull_request when available.\n\n"
			}
		}
	}
	sections := [][2]string{
		{"role", role + "\n\n"},
		{"project", transcript.Fenced("project", project.Name, "").Text + "\n\n"},
		{"communication", conversationGuide},
		{"writing", writingGuide(locale)},
		{"workflow", workflow},
	}
	if turn.Workspaces != nil {
		owner := ""
		if turn.conversation.Role == "thread" {
			owner = turn.conversation.ID
		}
		if available, err := turn.Workspaces.References(ctx, project.ID, locale, owner); err == nil && len(available) != 0 {
			lines := make([]string, 0, len(available))
			for _, source := range available {
				access := "read-only"
				if source.Writable && strings.HasPrefix(workflow, threadGuide) {
					access = "writable"
				}
				if source.Primary {
					access += ", default"
				}
				if source.Own {
					access += ", this thread"
				}
				lines = append(lines, source.Name+" ("+access+"): "+source.Line)
			}
			sections = append(sections, [2]string{"file-sources", "To work in a named file source, pass its name as source to the file or command tool. Before editing another source, read its repository instructions with the same source parameter. Each Git repository has its own discussion worktree.\n" +
				transcript.Fenced("file sources", strings.Join(lines, "\n"), "").Text + "\n\n"})
		}
	}
	if value, _ := config["instructions"].(string); value != "" {
		sections = append(sections, [2]string{"user-instructions", transcript.Fenced("user instructions", value, "").Text + "\n\n"})
	}
	if value := project.Settings.Instructions; value != "" {
		sections = append(sections, [2]string{"project-instructions", transcript.Fenced("project instructions", value, "").Text + "\n\n"})
	}
	if memories, err := turn.Repository.ListMemories(ctx, project.ID); err == nil && len(memories) > 0 {
		lines := make([]string, 0, len(memories))
		for _, memory := range memories {
			lines = append(lines, "["+memory.ID+"] "+memory.Text)
		}
		sections = append(sections, [2]string{"project-memory", transcript.Fenced("project memory", strings.Join(lines, "\n"), "").Text + "\n\n"})
	}
	if project.Settings.Delivery.Mode == "external" {
		sections = append(sections, [2]string{"delivery", "This project uses external delivery. Follow the delivery requirements.\n" +
			transcript.Fenced("delivery requirements", project.Settings.Delivery.Instructions, "").Text + "\n\n"})
	}
	if turn.skills != nil {
		var available []string
		for _, skill := range turn.skills.List() {
			available = append(available, skill.Name+": "+skill.When)
		}
		if len(available) != 0 {
			sections = append(sections, [2]string{"skills", "Use read_skill when one of these skills applies.\n" + transcript.Fenced("skills", strings.Join(available, "\n"), "").Text + "\n\n"})
		}
	}
	if recorded, err := turn.Repository.GetInstructions(ctx, turn.conversation.ID); err == nil {
		if value, ok := recorded.(map[string]any); ok {
			if content, ok := value["content"].(string); ok && content != "" {
				sections = append(sections, [2]string{"repository-instructions", transcript.Fenced("repository instructions", content, "").Text + "\n\n"})
			}
		}
	}
	return sections
}

func (turn *turn) prompt(ctx context.Context, program bool, tools []tool, sections [][2]string, entries []transcript.Entry) (prompt, bool, error) {
	specs := namesOfTools(tools)
	data, err := json.Marshal(specs)
	if err != nil {
		return prompt{}, false, err
	}
	fresh := prompt{joinSections(sections), string(data), specs}
	if program {
		return fresh, false, nil
	}
	writeBase := func() (prompt, bool, error) {
		err := turn.Repository.UpdatePrompt(ctx, turn.conversation.ID, turn.id, true, sections, fresh.tools, nil)
		return fresh, true, err
	}
	base := transcript.LatestBase(entries)
	if base == nil {
		return writeBase()
	}
	size := 0
	for _, entry := range entries {
		if asNumber(entry["seq"]) <= asNumber(base["seq"]) {
			continue
		}
		switch entry["kind"] {
		case "summary", "cleared":
			return writeBase()
		case "prompt":
			for _, raw := range items(entry["sections"]) {
				pair := items(raw)
				if len(pair) >= 2 {
					text, _ := pair[1].(string)
					size += len(text)
				}
			}
		}
		if size > 40000 {
			return writeBase()
		}
	}
	hash, _ := base["tools"].(string)
	if hash == "" {
		return writeBase()
	}
	known, err := turn.Repository.GetPrompt(ctx, turn.conversation.ProjectID, hash)
	if err != nil {
		return prompt{}, false, err
	}
	var declared []sdk.Tool
	if known == nil || json.Unmarshal([]byte(*known), &declared) != nil {
		return writeBase()
	}
	removed := transcript.RemovedTools(entries)
	byName := map[string]string{}
	for _, spec := range declared {
		body, _ := json.Marshal(spec)
		byName[spec.Name] = string(body)
	}
	offered := map[string]bool{}
	for _, spec := range specs {
		body, _ := json.Marshal(spec)
		if removed[spec.Name] || byName[spec.Name] != string(body) {
			return writeBase()
		}
		offered[spec.Name] = true
	}
	var gone []string
	for _, spec := range declared {
		if !removed[spec.Name] && !offered[spec.Name] {
			gone = append(gone, spec.Name)
		}
	}
	before := transcript.CurrentSections(entries)
	current := map[string]string{}
	for _, pair := range before {
		current[pair[0]] = pair[1]
	}
	var changes [][2]string
	keep := map[string]bool{}
	for _, pair := range sections {
		keep[pair[0]] = true
		if current[pair[0]] != pair[1] {
			changes = append(changes, pair)
		}
	}
	for _, pair := range before {
		if pair[1] != "" && !keep[pair[0]] {
			changes = append(changes, [2]string{pair[0], ""})
		}
	}
	changed := len(changes) > 0 || len(gone) > 0
	if changed {
		if err := turn.Repository.UpdatePrompt(ctx, turn.conversation.ID, turn.id, false, changes, "", gone); err != nil {
			return prompt{}, false, err
		}
	}
	return prompt{joinSections(transcript.CurrentSections([]transcript.Entry{base})), *known, declared}, changed, nil
}

func items(value any) []any {
	out, _ := value.([]any)
	return out
}
