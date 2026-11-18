package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
)

func (api *API) registerConversationConfig() {
	repo := api.options.Repository
	api.add("GET", "/api/projects/:projectId/mention-files", func(r *http.Request) (any, int, error) {
		project := r.PathValue("projectId")
		if _, err := repo.GetProject(r.Context(), project); err != nil {
			return nil, 0, err
		}
		manager := api.options.Workspaces
		if manager == nil || !manager.Available(r.Context(), project) {
			return object{"files": []string{}}, 200, nil
		}
		thread := ""
		if id := r.URL.Query().Get("conversationId"); id != "" {
			conversation, err := repo.GetConversation(r.Context(), id)
			if err != nil {
				return nil, 0, err
			}
			if conversation.ProjectID != project {
				return nil, 0, badInput("conversationId")
			}
			if conversation.Role == "thread" {
				thread = conversation.ID
			}
		}
		files, err := manager.MentionFiles(r.Context(), project, thread, textutil.HeadUnits(r.URL.Query().Get("q"), 200))
		if err != nil {
			files = []string{}
		}
		return object{"files": files}, 200, nil
	})
	api.add("PUT", "/api/threads/:threadId/model", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		raw, present := body["model"]
		if !present {
			return nil, 0, badInput("model")
		}
		var model *string
		if raw != nil {
			value, ok := raw.(string)
			if !ok {
				return nil, 0, badInput("model")
			}
			found := false
			if api.options.Settings != nil {
				for _, raw := range api.options.Settings.ModelChoices() {
					choice, _ := raw.(map[string]any)
					if choice["id"] == value {
						found = true
						break
					}
				}
			}
			if !found {
				return nil, 0, badInput("model")
			}
			model = &value
		}
		thread, err := repo.SetThreadModel(r.Context(), r.PathValue("threadId"), model, "user")
		return object{"thread": thread}, 200, err
	})
	api.add("POST", "/api/projects/:projectId/notices/:noticeId/undo", func(r *http.Request) (any, int, error) {
		project, id := r.PathValue("projectId"), r.PathValue("noticeId")
		notice, err := repo.EverywhereNotice(r.Context(), project, id)
		if err != nil {
			return nil, 0, err
		}
		if notice == nil {
			return nil, 204, repo.UndoMemoryNotice(r.Context(), project, id)
		}
		if api.options.Settings != nil && notice["undone"] != true {
			if notice["action"] == "saved" {
				_, err = api.options.Settings.RemoveInstruction(text(notice, "text"))
			} else {
				_, err = api.options.Settings.AddInstruction(text(notice, "text"))
			}
			if err != nil {
				return nil, 0, err
			}
			if err := repo.SettleNotice(r.Context(), project, id); err != nil {
				return nil, 0, err
			}
		}
		return nil, 204, nil
	})
	api.add("GET", "/api/projects/:projectId/references/status", func(r *http.Request) (any, int, error) {
		project, err := repo.GetProject(r.Context(), r.PathValue("projectId"))
		if err != nil {
			return nil, 0, err
		}
		failed := []any{}
		manager := api.options.Workspaces
		if manager == nil {
			return object{"failed": failed}, 200, nil
		}
		references, _ := project.References.([]any)
		failures := make([]*domainerr.Error, len(references))
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		jobs := make(chan int)
		var group sync.WaitGroup
		for range min(16, len(references)) {
			group.Go(func() {
				for index := range jobs {
					if ctx.Err() != nil {
						continue
					}
					ref, _ := references[index].(map[string]any)
					view, err := manager.ReferenceView(ctx, project.ID, text(ref, "name"), "")
					if err == nil {
						_, err = view.List(ctx, ".")
					}
					if err == nil || ctx.Err() != nil {
						continue
					}
					var known *domainerr.Error
					if !errors.As(err, &known) {
						known = domainerr.New("error.workspace", object{"detail": err.Error()})
					}
					failures[index] = known
				}
			})
		}
		for index := range references {
			select {
			case jobs <- index:
			case <-ctx.Done():
			}
		}
		close(jobs)
		group.Wait()
		if err := r.Context().Err(); err != nil {
			return nil, 0, err
		}
		for index, failure := range failures {
			if failure != nil {
				ref, _ := references[index].(map[string]any)
				failed = append(failed, object{"id": ref["id"], "error": failure})
			}
		}
		return object{"failed": failed}, 200, nil
	})
}
