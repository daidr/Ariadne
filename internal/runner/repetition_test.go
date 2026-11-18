package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"iter"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestRunnerStopsOnlyRepeatedToolBatches(t *testing.T) {
	for _, test := range []struct {
		name    string
		steps   []string
		images  bool
		stopped bool
	}{
		{name: "same result", steps: []string{"a", "a", "a", "a"}, stopped: true},
		{name: "alternating loop", steps: []string{"a", "b", "a", "b", "a", "b", "a", "b"}, stopped: true},
		{name: "interleaved progress", steps: []string{"a", "b", "a", "c", "a", "d", "a"}},
		{name: "changing screenshots", steps: []string{"a", "a", "a", "a"}, images: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			home := t.TempDir()
			repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			project, err := repo.CreateProject(ctx, new("Test project"), core.ProjectStart{})
			if err != nil {
				t.Fatal(err)
			}
			id := project.CoordinatorID
			if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: id, Author: "user", Content: "Check progress"}); err != nil {
				t.Fatal(err)
			}
			turnID, err := repo.StartScheduledTurn(ctx, id, "mock")
			if err != nil || turnID == "" {
				t.Fatal(turnID, err)
			}
			store, err := settings.Open(filepath.Join(home, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			index := 0
			const name = "mock"
			info := sdk.ModelProviderInfo{Kind: name, Program: true}
			registry := models.New()
			if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
				return func(yield func(sdk.Part) bool) {
					if index < len(test.steps) {
						if !yield(sdk.Part{Type: "tool-call", Name: "probe", ID: fmt.Sprintf("call_%d", index), Arguments: fmt.Sprintf(`{"key":%q}`, test.steps[index])}) {
							return
						}
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
						return
					}
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}}); err != nil {
				t.Fatal(err)
			}
			probe := tool{spec: sdk.Tool{Name: "probe"}, call: func(ctx context.Context, _ args) (string, []any, error) {
				index++
				if test.images {
					frame := image.NewRGBA(image.Rect(0, 0, 1, 1))
					frame.SetRGBA(0, 0, color.RGBA{R: uint8(index), A: 255})
					var data bytes.Buffer
					if err := png.Encode(&data, frame); err != nil {
						t.Fatal(err)
					}
					stored, err := (files.Store{Events: repo.Store}).Save(ctx, project.ID, "image/png", data.Bytes(), false)
					if err != nil {
						t.Fatal(err)
					}
					return "Captured screenshot", []any{attachment(stored, "screenshot.png")}, nil
				}
				return "Unchanged", nil, nil
			}}
			turn := &turn{
				Runner: &Runner{Repository: repo, Settings: store, Models: registry},
				id:     turnID, external: []tool{probe},
				conversation: core.ConversationView{ID: id, ProjectID: project.ID, Role: "coordinator"},
			}
			err = turn.run(ctx, &settings.ResolvedModel{Provider: info, Model: name, Label: name, Images: test.images})
			var domain *domainerr.Error
			if test.stopped {
				if !errors.As(err, &domain) || domain.Key != "error.repeating" || index != len(test.steps) {
					t.Fatalf("wanted replay protection after %d batches, got %d batches and %v", len(test.steps), index, err)
				}
			} else if err != nil || index != len(test.steps) {
				t.Fatalf("wanted %d successful batches, got %d and %v", len(test.steps), index, err)
			}
		})
	}
}
