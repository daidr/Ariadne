package core

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type ProjectPatch struct {
	Name            *string
	Settings        any
	HasSettings     bool
	Slots           *int
	HasSlots        bool
	MCP             []string
	HasMCP          bool
	AutopilotPaused *bool
}

func (repo *Repository) UpdateProject(ctx context.Context, projectID string, patch ProjectPatch, tools []string) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		changes := []change{}
		if patch.Name != nil {
			clean := projectsettings.Trim(*patch.Name)
			if clean == "" {
				return nil, badInput("name")
			}
			if clean != current.Name || current.Unnamed {
				changes = append(changes, change{"project.renamed", object{"name": clean}})
			}
		}
		autopilotEnabled := current.Settings.Autopilot.Enabled
		if patch.HasSettings {
			next, err := projectsettings.Parse(patch.Settings, tools)
			if err != nil {
				return nil, err
			}
			autopilotEnabled = next.Autopilot.Enabled
			if !reflect.DeepEqual(next, current.Settings) {
				changes = append(changes, change{"project.settings.set", object{"settings": next}})
			}
		}
		if patch.HasSlots {
			if patch.Slots != nil && (*patch.Slots < 1 || *patch.Slots > 16) {
				return nil, badInput("slots")
			}
			if encoded(current.Slots) != encoded(patch.Slots) {
				changes = append(changes, change{"project.slots.set", object{"slots": patch.Slots}})
			}
		}
		if patch.HasMCP {
			if patch.MCP == nil {
				return nil, badInput("servers")
			}
			unique := []string{}
			seen := map[string]bool{}
			for _, id := range patch.MCP {
				if id == "" {
					return nil, badInput("servers")
				}
				if !seen[id] {
					seen[id] = true
					unique = append(unique, id)
				}
			}
			if encoded(unique) != encoded(current.MCP) {
				changes = append(changes, change{"project.mcp.set", object{"servers": unique}})
			}
		}
		if patch.AutopilotPaused != nil {
			if !autopilotEnabled {
				return nil, domainerr.New("error.autopilotOff", nil)
			}
			if current.AutopilotPaused != *patch.AutopilotPaused {
				changes = append(changes, change{"project.autopilot.paused", object{"paused": *patch.AutopilotPaused}})
			}
		}
		return changes, nil
	})
}

type change struct {
	kind string
	data any
}

func appendChanges(tx *eventstore.Transaction, projectID string, changes []change) error {
	events := make([]eventstore.NewEvent, 0, len(changes))
	for _, change := range changes {
		data, err := json.Marshal(change.data)
		if err != nil {
			return err
		}
		events = append(events, eventstore.NewEvent{Type: change.kind, ProjectID: projectID, Data: data})
	}
	_, err := tx.Append(events)
	return err
}
func badInput(reason string) error {
	return domainerr.New("error.invalidBody", map[string]any{"reason": reason})
}
func (repo *Repository) mutateProject(ctx context.Context, projectID string, mutate func(ProjectView) ([]change, error)) (result ProjectView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		detail, err := projectDetail(ctx, tx.SQL(), projectID, repo.Reader.now())
		if err != nil {
			return err
		}
		changes, err := mutate(detail.Project)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			result = detail.Project
			return nil
		}
		if err := appendChanges(tx, projectID, changes); err != nil {
			return err
		}
		detail, err = projectDetail(ctx, tx.SQL(), projectID, repo.Reader.now())
		result = detail.Project
		return err
	})
	return
}
func (repo *Repository) RenameProject(ctx context.Context, projectID, name string) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(ProjectView) ([]change, error) {
		clean := projectsettings.Trim(name)
		if clean == "" {
			return nil, badInput("name")
		}
		return []change{{"project.renamed", object{"name": clean}}}, nil
	})
}
func (repo *Repository) SetProjectSlots(ctx context.Context, projectID string, slots *int) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		if slots != nil && (*slots < 1 || *slots > 16) {
			return nil, badInput("slots")
		}
		if encoded(current.Slots) == encoded(slots) {
			return nil, nil
		}
		return []change{{"project.slots.set", object{"slots": slots}}}, nil
	})
}
func (repo *Repository) SetProjectMCP(ctx context.Context, projectID string, servers []string) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		if servers == nil {
			return nil, badInput("servers")
		}
		unique := []string{}
		seen := map[string]bool{}
		for _, id := range servers {
			if id == "" {
				return nil, badInput("servers")
			}
			if !seen[id] {
				seen[id] = true
				unique = append(unique, id)
			}
		}
		if encoded(unique) == encoded(current.MCP) {
			return nil, nil
		}
		return []change{{"project.mcp.set", object{"servers": unique}}}, nil
	})
}
func (repo *Repository) SetProjectSettings(ctx context.Context, projectID string, input any, tools []string) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		next, err := projectsettings.Parse(input, tools)
		if err != nil {
			return nil, err
		}
		if reflect.DeepEqual(next, current.Settings) {
			return nil, nil
		}
		return []change{{"project.settings.set", object{"settings": next}}}, nil
	})
}
func (repo *Repository) SetProjectDelivery(ctx context.Context, projectID string, input map[string]any) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		delivery := map[string]any{"mode": current.Settings.Delivery.Mode, "instructions": current.Settings.Delivery.Instructions}
		if value, has := input["mode"]; has {
			if value != "merge" && value != "external" {
				return nil, badInput("delivery.mode")
			}
			delivery["mode"] = value
		}
		if value, has := input["instructions"]; has {
			if _, ok := value.(string); !ok {
				return nil, badInput("delivery.instructions")
			}
			delivery["instructions"] = value
		}
		parsed, err := projectsettings.Parse(map[string]any{"delivery": delivery}, nil)
		if err != nil {
			return nil, err
		}
		next := current.Settings
		next.Delivery = parsed.Delivery
		if reflect.DeepEqual(next, current.Settings) {
			return nil, nil
		}
		return []change{{"project.settings.set", object{"settings": next}}}, nil
	})
}

type RoutineChange struct {
	By     string `json:"by"`
	Action string `json:"action"`
	Prompt string `json:"prompt"`
}

func (repo *Repository) SetRoutines(ctx context.Context, projectID string, routines any, changed *RoutineChange) (ProjectView, error) {
	return repo.changeRoutines(ctx, projectID, func([]projectsettings.Routine) (any, *RoutineChange, error) {
		return routines, changed, nil
	})
}

func (repo *Repository) EditRoutine(ctx context.Context, projectID, by, id string, add *projectsettings.Routine) (ProjectView, error) {
	return repo.changeRoutines(ctx, projectID, func(current []projectsettings.Routine) (any, *RoutineChange, error) {
		if add != nil {
			return append(current, *add), &RoutineChange{By: by, Action: "added", Prompt: add.Prompt}, nil
		}
		for index, routine := range current {
			if routine.ID == id {
				next := append(current[:index:index], current[index+1:]...)
				return next, &RoutineChange{By: by, Action: "removed", Prompt: routine.Prompt}, nil
			}
		}
		return nil, nil, domainerr.NotFound(id)
	})
}

func (repo *Repository) changeRoutines(ctx context.Context, projectID string, transform func([]projectsettings.Routine) (any, *RoutineChange, error)) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		routines, changed, err := transform(current.Settings.Routines)
		if err != nil {
			return nil, err
		}
		parsed, err := projectsettings.ParseRoutines(routines)
		if err != nil {
			return nil, err
		}
		next := current.Settings
		next.Routines = parsed
		if reflect.DeepEqual(next, current.Settings) {
			return nil, nil
		}
		data := object{"settings": next}
		if changed != nil {
			data["routine"] = changed
		}
		return []change{{"project.settings.set", data}}, nil
	})
}
func (repo *Repository) RememberMergeMethod(ctx context.Context, projectID, method string) error {
	_, err := repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		if method != "squash" && method != "merge" {
			return nil, badInput("autopilot.method")
		}
		if current.Settings.Autopilot.Method == method {
			return nil, nil
		}
		next := current.Settings
		next.Autopilot.Method = method
		return []change{{"project.settings.set", object{"settings": next}}}, nil
	})
	return err
}
func (repo *Repository) SetAutopilotPaused(ctx context.Context, projectID string, paused bool) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		if !current.Settings.Autopilot.Enabled {
			return nil, domainerr.New("error.autopilotOff", nil)
		}
		if current.AutopilotPaused == paused {
			return nil, nil
		}
		return []change{{"project.autopilot.paused", object{"paused": paused}}}, nil
	})
}
func (repo *Repository) SetProjectSource(ctx context.Context, projectID string, source any) (ProjectView, error) {
	return repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		if encoded(current.Source) == encoded(source) {
			return nil, nil
		}
		return []change{{"project.source.set", object{"source": source}}}, nil
	})
}
