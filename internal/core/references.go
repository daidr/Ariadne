package core

import (
	"context"
	"regexp"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type Source struct {
	Kind     string         `json:"kind"`
	Settings map[string]any `json:"settings"`
}
type Reference struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Settings    map[string]any `json:"settings"`
	AttentionID *string        `json:"attentionId,omitempty"`
}

var referenceParts = regexp.MustCompile(`[\\/:]+`)
var referenceLetters = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
var gitSuffix = regexp.MustCompile(`(?i)\.git$`)

func ReferenceName(where string, taken []string) string {
	parts := referenceParts.Split(projectsettings.Trim(where), -1)
	last := ""
	for _, part := range parts {
		if part != "" {
			last = part
		}
	}
	base := strings.Trim(referenceLetters.ReplaceAllString(gitSuffix.ReplaceAllString(last, ""), "-"), "-.")
	if len(base) > 40 {
		base = base[:40]
	}
	if base == "" {
		base = "reference"
	}
	seen := map[string]bool{}
	for _, name := range taken {
		seen[name] = true
	}
	if !seen[base] {
		return base
	}
	for n := 2; ; n++ {
		name := base + "-" + encoded(n)
		if !seen[name] {
			return name
		}
	}
}
func refs(raw any) []object {
	values, _ := listValue(raw)
	result := make([]object, len(values))
	for i, value := range values {
		result[i] = asObject(value)
	}
	return result
}
func refNames(references []object) []string {
	names := make([]string, len(references))
	for i, reference := range references {
		names[i] = reference.str("name")
	}
	return names
}
func asReference(raw object) Reference {
	var attentionID *string
	if value, has := raw["attentionId"].(string); has {
		attentionID = &value
	}
	return Reference{ID: raw.str("id"), Name: raw.str("name"), Kind: raw.str("kind"), Settings: raw.obj("settings"), AttentionID: attentionID}
}
func existingReference(references []object, source Source) object {
	for _, ref := range references {
		if ref.str("kind") == source.Kind && encoded(ref["settings"]) == encoded(source.Settings) {
			return ref
		}
	}
	return nil
}
func addProjectReference(ctx context.Context, tx *eventstore.Transaction, projectID string, source Source, where string, now int64) (Reference, error) {
	detail, err := projectDetail(ctx, tx.SQL(), projectID, now)
	if err != nil {
		return Reference{}, err
	}
	references := refs(detail.Project.References)
	if len(references) >= 8 {
		return Reference{}, domainerr.New("error.tooManyReferences", map[string]any{"max": 8})
	}
	if already := existingReference(references, source); already != nil {
		return Reference{}, domainerr.New("error.referenceExists", map[string]any{"name": already["name"]})
	}
	id, err := newID("ref")
	if err != nil {
		return Reference{}, err
	}
	reference := Reference{ID: id, Name: ReferenceName(where, refNames(references)), Kind: source.Kind, Settings: source.Settings}
	values := []any{}
	for _, ref := range references {
		values = append(values, ref)
	}
	values = append(values, reference)
	if err := appendChanges(tx, projectID, []change{{"project.references.set", object{"references": values}}}); err != nil {
		return Reference{}, err
	}
	return reference, nil
}
func (repo *Repository) AddProjectReference(ctx context.Context, projectID string, source Source, where string) (result Reference, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		result, err = addProjectReference(ctx, tx, projectID, source, where, repo.Reader.now())
		return err
	})
	return
}
func readThread(ctx context.Context, db queryer, id string, now int64) (ThreadView, error) {
	rows, err := records(ctx, db, threadSelect+" WHERE t.id = ?", id)
	if err != nil {
		return ThreadView{}, err
	}
	if len(rows) == 0 {
		return ThreadView{}, domainerr.NotFound(id)
	}
	return toThread(rows[0], now)
}
func (repo *Repository) AddThreadReference(ctx context.Context, id string, source Source, where, attentionID string) (result Reference, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		thread, err := readThread(ctx, tx.SQL(), id, repo.Reader.now())
		if err != nil {
			return err
		}
		references := refs(thread.References)
		if already := existingReference(references, source); already != nil {
			result = asReference(already)
			return nil
		}
		detail, err := projectDetail(ctx, tx.SQL(), thread.ProjectID, repo.Reader.now())
		if err != nil {
			return err
		}
		refID, err := newID("ref")
		if err != nil {
			return err
		}
		names := append(refNames(refs(detail.Project.References)), refNames(references)...)
		result = Reference{ID: refID, Name: ReferenceName(where, names), Kind: source.Kind, Settings: source.Settings, AttentionID: &attentionID}
		values := []any{}
		for _, ref := range references {
			values = append(values, ref)
		}
		values = append(values, result)
		return appendChanges(tx, thread.ProjectID, []change{{"thread.references.set", object{"threadId": id, "references": values}}})
	})
	return
}
func (repo *Repository) RemoveThreadReference(ctx context.Context, id, referenceID string) (ThreadView, error) {
	return repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		kept := []object{}
		var gone object
		for _, ref := range refs(current.References) {
			if ref.str("id") == referenceID {
				gone = ref
			} else {
				kept = append(kept, ref)
			}
		}
		if gone == nil {
			return nil, nil
		}
		return []change{{"thread.references.set", object{"threadId": id, "references": kept, "ended": object{"attentionId": gone["attentionId"], "name": gone["name"], "now": nil}}}}, nil
	})
}
func (repo *Repository) KeepThreadReference(ctx context.Context, id, referenceID string) (result Reference, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		thread, err := readThread(ctx, tx.SQL(), id, repo.Reader.now())
		if err != nil {
			return err
		}
		var kept object
		remaining := []object{}
		for _, ref := range refs(thread.References) {
			if ref.str("id") == referenceID {
				kept = ref
			} else {
				remaining = append(remaining, ref)
			}
		}
		if kept == nil {
			return domainerr.NotFound(referenceID)
		}
		detail, err := projectDetail(ctx, tx.SQL(), thread.ProjectID, repo.Reader.now())
		if err != nil {
			return err
		}
		references := refs(detail.Project.References)
		if len(references) >= 8 {
			return domainerr.New("error.tooManyReferences", map[string]any{"max": 8})
		}
		already := existingReference(references, Source{Kind: kept.str("kind"), Settings: kept.obj("settings")})
		changes := []change{}
		if already != nil {
			result = asReference(already)
		} else {
			reference := pick(kept, "id", "name", "kind", "settings")
			for _, ref := range references {
				if ref["name"] == kept["name"] {
					reference["name"] = ReferenceName(kept.str("name"), refNames(references))
					break
				}
			}
			result = asReference(reference)
			changes = append(changes, change{"project.references.set", object{"references": append(references, reference)}})
		}
		changes = append(changes, change{"thread.references.set", object{"threadId": id, "references": remaining, "ended": object{"attentionId": kept["attentionId"], "name": kept["name"], "now": result.Name}}})
		return appendChanges(tx, thread.ProjectID, changes)
	})
	return
}
func (repo *Repository) RemoveProjectReference(ctx context.Context, projectID, referenceID string) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		before := refs(current.References)
		after := []object{}
		for _, ref := range before {
			if ref.str("id") != referenceID {
				after = append(after, ref)
			}
		}
		if len(after) == len(before) {
			return nil, nil
		}
		return []change{{"project.references.set", object{"references": after}}}, nil
	})
}

type NewReference struct {
	Source Source `json:"source"`
	Where  string `json:"where"`
}
type ProjectStart struct {
	Source     any            `json:"source"`
	Message    string         `json:"message"`
	References []NewReference `json:"references"`
}

func firstWords(message string) string {
	line := ""
	for _, part := range strings.Split(message, "\n") {
		if clean := projectsettings.Trim(part); clean != "" {
			line = clean
			break
		}
	}
	line = spaces.ReplaceAllString(line, " ")
	letters := []rune(line)
	if len(letters) > 24 {
		return projectsettings.Trim(string(letters[:24])) + "…"
	}
	return line
}
func (repo *Repository) CreateProject(ctx context.Context, name *string, start ProjectStart) (result ProjectView, err error) {
	message := projectsettings.Trim(start.Message)
	clean := ""
	if name != nil {
		clean = projectsettings.Trim(*name)
	}
	unnamed := clean == ""
	if clean == "" {
		clean = firstWords(message)
	}
	if clean == "" {
		return ProjectView{}, badInput("name")
	}
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		projectID, err := newID("prj")
		if err != nil {
			return err
		}
		coordinatorID, err := newID("crd")
		if err != nil {
			return err
		}
		data := object{"name": clean, "coordinatorId": coordinatorID}
		if unnamed {
			data["unnamed"] = true
		}
		if err := appendChanges(tx, projectID, []change{{"project.created", data}}); err != nil {
			return err
		}
		if truthy(start.Source) {
			if err := appendChanges(tx, projectID, []change{{"project.source.set", object{"source": start.Source}}}); err != nil {
				return err
			}
		}
		for _, reference := range start.References {
			if _, err := addProjectReference(ctx, tx, projectID, reference.Source, reference.Where, repo.Reader.now()); err != nil {
				return err
			}
		}
		if message != "" {
			if _, err := postMessage(ctx, tx, projectID, MessageInput{ConversationID: coordinatorID, Author: "user", Content: message}); err != nil {
				return err
			}
		}
		detail, err := projectDetail(ctx, tx.SQL(), projectID, repo.Reader.now())
		if err != nil {
			return err
		}
		result = detail.Project
		return nil
	})
	return
}
