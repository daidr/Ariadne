package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/jsonutil"
)

type Shared interface {
	Content(context.Context, string) (string, error)
	Write(context.Context, string, string, string, string) (core.ArtifactView, error)
}
type RefreshOptions struct {
	Author, Reason string
	Now            func() time.Time
	Shared         Shared
}

func Refresh(ctx context.Context, repo *core.Repository, sources *Registry, id string, options RefreshOptions) (core.ArtifactView, error) {
	current, err := repo.GetArtifact(ctx, id, nil)
	if err != nil {
		return current, err
	}
	if current.Kind != "dashboard" {
		return core.ArtifactView{}, domainerr.New("error.invalidBody", map[string]any{"reason": "kind"})
	}
	content := map[string]any{}
	if err := json.Unmarshal([]byte(current.Content), &content); err != nil {
		return core.ArtifactView{}, err
	}
	source, _ := content["source"].(map[string]any)
	if source == nil {
		return core.ArtifactView{}, domainerr.New("error.dashboardStatic", nil)
	}
	kind, _ := source["kind"].(string)
	provider := sources.Get(kind)
	if provider == nil {
		return core.ArtifactView{}, domainerr.New("error.dashboardSourceUnknown", map[string]any{"kind": kind})
	}
	settings, err := CheckSettings(provider.Info, source["settings"])
	if err != nil {
		return core.ArtifactView{}, domainerr.New("error.dashboardFetch", map[string]any{"detail": err.Error()})
	}
	fetched, err := provider.Fetch(ctx, settings, current.ProjectID)
	if err != nil {
		return core.ArtifactView{}, domainerr.New("error.dashboardFetch", map[string]any{"detail": err.Error()})
	}
	if options.Shared != nil {
		value, err := options.Shared.Content(ctx, id)
		if err != nil {
			return core.ArtifactView{}, err
		}
		content = map[string]any{}
		if err := json.Unmarshal([]byte(value), &content); err != nil {
			return core.ArtifactView{}, err
		}
		original, err := jsonutil.Encode(source)
		if err != nil {
			return core.ArtifactView{}, err
		}
		active, err := jsonutil.Encode(content["source"])
		if err != nil {
			return core.ArtifactView{}, err
		}
		if !bytes.Equal(original, active) {
			return core.ArtifactView{}, domainerr.New("error.artifactConflict", map[string]any{"version": current.Version})
		}
	}
	data, _ := content["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	for key, value := range fetched {
		data[key] = value
	}
	content["data"] = data
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	content["takenAt"] = now().UnixMilli()
	encoded, err := jsonutil.Encode(content)
	if err != nil {
		return core.ArtifactView{}, err
	}
	if options.Shared != nil {
		return options.Shared.Write(ctx, id, options.Author, string(encoded), options.Reason)
	}
	return repo.ReviseArtifact(ctx, id, core.ArtifactRevision{Content: string(encoded), Author: options.Author, Reason: options.Reason, BaseVersion: &current.Version})
}
