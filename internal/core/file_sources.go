package core

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/sources"
)

// The original primary keeps its existing settings and secret keys on upgrade.
const LegacyPrimarySource = sources.LegacySourceID

type FileSource struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Settings map[string]any `json:"settings"`
	Hosting  map[string]any `json:"hosting,omitempty"`
}

type FileSourceSet struct {
	PrimaryID string       `json:"primaryId"`
	Sources   []FileSource `json:"sources"`
}

type ThreadFileSource struct{ ThreadID, SourceID string }

func (reader Reader) ThreadFileSources(ctx context.Context, project string) ([]ThreadFileSource, error) {
	rows, err := records(ctx, reader.DB, `SELECT t.id AS thread_id, json_extract(r.value, '$.id') AS source_id
	 FROM threads t, json_each(t.reference_sources) r WHERE t.project_id = ? AND json_extract(r.value, '$.writable') = 1`, project)
	if err != nil {
		return nil, err
	}
	result := make([]ThreadFileSource, 0, len(rows))
	for _, row := range rows {
		result = append(result, ThreadFileSource{ThreadID: row.text("thread_id"), SourceID: row.text("source_id")})
	}
	return result, nil
}

func (set FileSourceSet) Get(id string) (FileSource, bool) {
	for _, source := range set.Sources {
		if source.ID == id {
			return source, true
		}
	}
	return FileSource{}, false
}

func (source FileSource) definition() object {
	value := object{"kind": source.Kind, "settings": source.Settings}
	if source.Hosting != nil {
		value["hosting"] = source.Hosting
	}
	return value
}

func (project ProjectView) WritableSources() FileSourceSet {
	if project.FileSources != nil {
		return *project.FileSources
	}
	before := asObject(project.Source)
	if before == nil || before.str("kind") == "" {
		return FileSourceSet{Sources: []FileSource{}}
	}
	return FileSourceSet{PrimaryID: LegacyPrimarySource, Sources: []FileSource{{
		ID: LegacyPrimarySource, Name: LegacyPrimarySource, Kind: before.str("kind"),
		Settings: before.obj("settings"), Hosting: before.obj("hosting"),
	}}}
}

var fileSourceID = regexp.MustCompile(`^(primary|(src|ref)_[a-zA-Z0-9_-]{1,60})$`)

func ValidFileSourceID(id string) bool { return fileSourceID.MatchString(id) }
func NewFileSourceID() (string, error) { return newID("src") }

func fileSourcesChanged(before FileSourceSet, after FileSourceSet) (change, error) {
	if len(after.Sources) > 9 {
		return change{}, badInput("sources")
	}
	ids, names, definitions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, source := range after.Sources {
		key := encoded(source.definition())
		if !ValidFileSourceID(source.ID) || source.Name == "" || source.Kind == "" ||
			ids[source.ID] || names[source.Name] || definitions[key] {
			return change{}, badInput("sources")
		}
		ids[source.ID], names[source.Name], definitions[key] = true, true, true
	}
	primary, exists := after.Get(after.PrimaryID)
	if !exists && (len(after.Sources) != 0 || after.PrimaryID != "") {
		return change{}, badInput("primaryId")
	}
	var stored any
	if exists {
		value := primary.definition()
		value["fileSources"] = after
		stored = value
	}
	previous, _ := before.Get(before.PrimaryID)
	changed := before.PrimaryID != after.PrimaryID || encoded(previous.definition()) != encoded(primary.definition())
	return change{"project.file_sources.set", object{"source": stored, "primaryChanged": changed}}, nil
}

func readFileSources(raw any) (*FileSourceSet, any, error) {
	value := asObject(raw)
	if value["fileSources"] == nil {
		return nil, raw, nil
	}
	var set FileSourceSet
	if err := json.Unmarshal([]byte(encoded(value["fileSources"])), &set); err != nil {
		return nil, nil, err
	}
	delete(value, "fileSources")
	return &set, raw, nil
}

func (repo *Repository) SetProjectFileSources(ctx context.Context, projectID string, before, after FileSourceSet) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(project ProjectView) ([]change, error) {
		current := project.WritableSources()
		if encoded(current) != encoded(before) {
			return nil, domainerr.New("error.invalidBody", map[string]any{"reason": "sources changed; reload the project"})
		}
		if encoded(current) == encoded(after) {
			return nil, nil
		}
		event, err := fileSourcesChanged(current, after)
		if err != nil {
			return nil, err
		}
		changes := []change{event}
		references := refs(project.References)
		remaining := make([]object, 0, len(references))
		for _, reference := range references {
			if _, promoted := after.Get(reference.str("id")); !promoted {
				remaining = append(remaining, reference)
			}
		}
		if len(references) != len(remaining) {
			changes = append(changes, change{"project.references.set", object{"references": remaining}})
		}
		return changes, nil
	})
}

func (repo *Repository) AddProjectFileSource(ctx context.Context, projectID string, source FileSource, where string) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(project ProjectView) ([]change, error) {
		before := project.WritableSources()
		for _, known := range before.Sources {
			if encoded(known.definition()) == encoded(source.definition()) {
				return nil, domainerr.New("error.referenceExists", map[string]any{"name": known.Name})
			}
		}
		names := refNames(refs(project.References))
		for _, known := range before.Sources {
			names = append(names, known.Name)
		}
		source.Name = ReferenceName(where, names)
		after := FileSourceSet{PrimaryID: before.PrimaryID, Sources: append(slices.Clone(before.Sources), source)}
		if after.PrimaryID == "" {
			after.PrimaryID = source.ID
		}
		event, err := fileSourcesChanged(before, after)
		return []change{event}, err
	})
}
