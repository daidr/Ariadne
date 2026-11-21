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
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
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

func TestRunnerReadCommandOutputRepetition(t *testing.T) {
	type callSpec struct {
		name string
		args string
	}
	for _, test := range []struct {
		name    string
		setup   func(t *testing.T, commands *background.Commands, threadID string) (handle string, onBatch func(batch int), cleanup func())
		batches func(handle string) [][]callSpec
		stopped bool
	}{
		{
			name: "running command polled with wait",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				finish := make(chan struct{})
				job, err := commands.Start(threadID, "go test ./...", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
					select {
					case <-finish:
						return sdk.ExecResult{ExitCode: 0}, nil
					case <-ctx.Done():
						return sdk.ExecResult{ExitCode: 130}, nil
					}
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				closed := false
				done := func() {
					if !closed {
						closed = true
						close(finish)
					}
				}
				return job.ID, func(batch int) {
					if batch == 5 {
						done()
						if _, err := commands.Wait(t.Context(), threadID, job.ID); err != nil {
							t.Fatal(err)
						}
					}
				}, done
			},
			batches: func(handle string) [][]callSpec {
				call := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q,"waitSeconds":0.01}`, handle)}
				return [][]callSpec{{call}, {call}, {call}, {call}, {call}, {call}}
			},
		},
		{
			name: "running command emits identical chunks",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				started := make(chan sdk.ExecOptions, 1)
				finish := make(chan struct{})
				job, err := commands.Start(threadID, "progress", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
					started <- options
					select {
					case <-finish:
						return sdk.ExecResult{ExitCode: 0}, nil
					case <-ctx.Done():
						return sdk.ExecResult{ExitCode: 130}, nil
					}
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				options := <-started
				closed := false
				done := func() {
					if !closed {
						closed = true
						close(finish)
					}
				}
				return job.ID, func(int) {
					options.OnOutput("tick\n", "stdout")
				}, done
			},
			batches: func(handle string) [][]callSpec {
				call := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q}`, handle)}
				return [][]callSpec{{call}, {call}, {call}, {call}, {call}}
			},
		},
		{
			name: "finished command paged with identical output",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				job, err := commands.Start(threadID, "dump", func(_ context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
					options.OnOutput(strings.Repeat("x", 150_000), "stdout")
					return sdk.ExecResult{ExitCode: 0}, nil
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := commands.Wait(t.Context(), threadID, job.ID); err != nil {
					t.Fatal(err)
				}
				return job.ID, nil, func() {}
			},
			batches: func(handle string) [][]callSpec {
				call := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q}`, handle)}
				return [][]callSpec{{call}, {call}, {call}, {call}, {call}}
			},
		},
		{
			name: "finished command repeated after drained",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				job, err := commands.Start(threadID, "done", func(context.Context, sdk.ExecOptions) (sdk.ExecResult, error) {
					return sdk.ExecResult{ExitCode: 0}, nil
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := commands.Wait(t.Context(), threadID, job.ID); err != nil {
					t.Fatal(err)
				}
				return job.ID, nil, func() {}
			},
			batches: func(handle string) [][]callSpec {
				call := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q,"waitSeconds":0.01}`, handle)}
				return [][]callSpec{{call}, {call}, {call}, {call}}
			},
			stopped: true,
		},
		{
			name: "running command busy spin without wait",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				finish := make(chan struct{})
				job, err := commands.Start(threadID, "sleep", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
					select {
					case <-finish:
						return sdk.ExecResult{ExitCode: 0}, nil
					case <-ctx.Done():
						return sdk.ExecResult{ExitCode: 130}, nil
					}
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return job.ID, nil, func() { close(finish) }
			},
			batches: func(handle string) [][]callSpec {
				call := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q}`, handle)}
				return [][]callSpec{{call}, {call}, {call}, {call}}
			},
			stopped: true,
		},
		{
			name: "unknown command handle repeated",
			setup: func(*testing.T, *background.Commands, string) (string, func(int), func()) {
				return "cmd-missing", nil, func() {}
			},
			batches: func(handle string) [][]callSpec {
				call := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q,"waitSeconds":0.01}`, handle)}
				return [][]callSpec{{call}, {call}, {call}, {call}}
			},
			stopped: true,
		},
		{
			name: "stuck tool interleaved with running command wait",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				finish := make(chan struct{})
				job, err := commands.Start(threadID, "sleep", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
					select {
					case <-finish:
						return sdk.ExecResult{ExitCode: 0}, nil
					case <-ctx.Done():
						return sdk.ExecResult{ExitCode: 130}, nil
					}
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return job.ID, nil, func() { close(finish) }
			},
			batches: func(handle string) [][]callSpec {
				wait := callSpec{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q,"waitSeconds":0.01}`, handle)}
				probe := callSpec{name: "probe", args: `{"key":"a"}`}
				return [][]callSpec{{probe}, {wait}, {probe}, {wait}, {probe}, {wait}, {probe}}
			},
			stopped: true,
		},
		{
			name: "mixed batch with stuck tool and running command wait",
			setup: func(t *testing.T, commands *background.Commands, threadID string) (string, func(int), func()) {
				finish := make(chan struct{})
				job, err := commands.Start(threadID, "sleep", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
					select {
					case <-finish:
						return sdk.ExecResult{ExitCode: 0}, nil
					case <-ctx.Done():
						return sdk.ExecResult{ExitCode: 130}, nil
					}
				}, background.StartOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return job.ID, nil, func() { close(finish) }
			},
			batches: func(handle string) [][]callSpec {
				batch := []callSpec{
					{name: "probe", args: `{"key":"a"}`},
					{name: "read_command_output", args: fmt.Sprintf(`{"handle":%q,"waitSeconds":0.01}`, handle)},
				}
				return [][]callSpec{batch, batch, batch, batch}
			},
			stopped: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			home, folder := t.TempDir(), t.TempDir()
			repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			sourceRegistry := sources.New()
			if _, err := sourceRegistry.Register(local.DirectoryProvider(filepath.Join(home, "local"), nil, true)); err != nil {
				t.Fatal(err)
			}
			work, err := workspaces.New(workspaces.Options{Repository: repo, Sources: sourceRegistry})
			if err != nil {
				t.Fatal(err)
			}
			defer work.Close(context.Background())
			project, err := repo.CreateProject(ctx, new("Test project"), core.ProjectStart{Source: args{"kind": "local-directory", "settings": args{"path": folder}}})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Poll command"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: "Run tests"}); err != nil {
				t.Fatal(err)
			}
			turnID, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
			if err != nil || turnID == "" {
				t.Fatal(turnID, err)
			}
			store, err := settings.Open(filepath.Join(home, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			handle, onBatch, cleanup := test.setup(t, work.Background, thread.ID)
			defer cleanup()
			batches := test.batches(handle)
			index := 0
			const name = "mock"
			info := sdk.ModelProviderInfo{Kind: name, Program: true}
			registry := models.New()
			if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
				return func(yield func(sdk.Part) bool) {
					if index < len(batches) {
						batchIndex := index
						index++
						if onBatch != nil {
							onBatch(batchIndex)
						}
						for callIndex, call := range batches[batchIndex] {
							if !yield(sdk.Part{Type: "tool-call", Name: call.name, ID: fmt.Sprintf("call_%d_%d", batchIndex, callIndex), Arguments: call.args}) {
								return
							}
						}
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
						return
					}
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}}); err != nil {
				t.Fatal(err)
			}
			probe := specification("probe", "Static probe.", args{"key": field("string")}, nil, func(context.Context, args) (string, error) {
				return "Unchanged", nil
			})
			active := &turn{
				Runner: &Runner{Repository: repo, Settings: store, Models: registry, Workspaces: work},
				id:     turnID, external: []tool{probe},
				conversation: core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"},
			}
			err = active.run(ctx, &settings.ResolvedModel{Provider: info, Model: name, Label: name})
			var domain *domainerr.Error
			if test.stopped {
				if !errors.As(err, &domain) || domain.Key != "error.repeating" || index != len(batches) {
					t.Fatalf("wanted replay protection after %d batches, got %d batches and %v", len(batches), index, err)
				}
			} else if err != nil || index != len(batches) {
				t.Fatalf("wanted %d successful batches, got %d and %v", len(batches), index, err)
			}
		})
	}
}
