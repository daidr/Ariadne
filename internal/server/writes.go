package server

import (
	"io"
	"math"
	"net/http"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
)

func (api *API) registerWrites() {
	repo := api.options.Repository
	api.add("PUT", "/api/projects/:projectId", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		name, err := required(body, "name")
		if err != nil {
			return nil, 0, err
		}
		project, err := repo.RenameProject(r.Context(), r.PathValue("projectId"), name)
		return object{"project": project}, 200, err
	})
	api.add("PUT", "/api/projects/:projectId/slots", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		var slots *int
		if raw, present := body["slots"]; !present {
			return nil, 0, badInput("slots")
		} else if raw != nil {
			value, ok := raw.(float64)
			if !ok || value != math.Trunc(value) || value < 1 || value > 16 {
				return nil, 0, badInput("slots")
			}
			n := int(value)
			slots = &n
		}
		project, err := repo.SetProjectSlots(r.Context(), r.PathValue("projectId"), slots)
		return object{"project": project}, 200, err
	})
	api.add("PUT", "/api/projects/:projectId/autopilot", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		paused, ok := body["paused"].(bool)
		if !ok {
			return nil, 0, badInput("paused")
		}
		project, err := repo.SetAutopilotPaused(r.Context(), r.PathValue("projectId"), paused)
		return object{"project": project}, 200, err
	})
	api.add("POST", "/api/projects/:projectId/messages", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		arrays := map[string][]any{}
		for _, key := range []string{"attachments", "mentions", "comments"} {
			var given []any
			if body[key] != nil {
				var ok bool
				given, ok = body[key].([]any)
				if !ok {
					return nil, 0, badInput(key)
				}
			}
			arrays[key] = given
		}
		conversationID, err := required(body, "conversationId")
		if err != nil {
			return nil, 0, err
		}
		message, err := repo.PostMessage(r.Context(), r.PathValue("projectId"), core.MessageInput{ConversationID: conversationID, Author: "user", Content: text(body, "content"), Attachments: arrays["attachments"], Mentions: arrays["mentions"], Comments: arrays["comments"]})
		return object{"message": message}, 201, err
	})
	api.add("POST", "/api/projects/:projectId/files", func(r *http.Request) (any, int, error) {
		forMessage := r.URL.Query().Get("for") == "message"
		limit := files.MaxImageBytes
		if forMessage {
			limit = files.MaxFileBytes
		}
		maxMB := limit / 1024 / 1024
		if r.ContentLength > int64(limit) {
			return nil, 0, domainerr.New("error.fileTooLarge", object{"max": maxMB})
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
		if err != nil {
			return nil, 0, err
		}
		if len(data) > limit {
			return nil, 0, domainerr.New("error.fileTooLarge", object{"max": maxMB})
		}
		file, err := api.files.Save(r.Context(), r.PathValue("projectId"), r.Header.Get("Content-Type"), data, forMessage)
		return object{"file": file}, 201, err
	})
	for _, method := range []string{"POST", "PUT"} {
		pattern := "/api/projects/:projectId/memory"
		if method == "PUT" {
			pattern += "/:memoryId"
		}
		api.add(method, pattern, func(r *http.Request) (any, int, error) {
			body, err := readJSON(r)
			if err != nil {
				return nil, 0, err
			}
			value, err := required(body, "text")
			if err != nil {
				return nil, 0, err
			}
			input := core.MemoryChange{Text: value, Author: "user"}
			status := 201
			if r.Method == "PUT" {
				id := r.PathValue("memoryId")
				input.MemoryID = &id
				status = 200
			}
			memory, err := repo.SaveMemory(r.Context(), r.PathValue("projectId"), input)
			return object{"memory": memory}, status, err
		})
	}
	api.add("DELETE", "/api/projects/:projectId/memory/:memoryId", func(r *http.Request) (any, int, error) {
		return nil, 204, repo.RemoveMemory(r.Context(), r.PathValue("projectId"), r.PathValue("memoryId"), "user", "")
	})
	api.add("POST", "/api/projects/:projectId/return", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		away, ok := body["away"].(float64)
		if !ok {
			return nil, 0, badInput("away")
		}
		return nil, 204, repo.NoteReturn(r.Context(), r.PathValue("projectId"), away)
	})
	api.add("POST", "/api/threads/:threadId/stop", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		reason, ok := body["reason"].(string)
		if !ok {
			return nil, 0, badInput("reason")
		}
		thread, err := repo.StopThread(r.Context(), r.PathValue("threadId"), reason, "user")
		return object{"thread": thread}, 200, err
	})
	api.add("POST", "/api/threads/:threadId/resume", func(r *http.Request) (any, int, error) {
		thread, err := repo.ResumeThread(r.Context(), r.PathValue("threadId"), "user")
		return object{"thread": thread}, 200, err
	})
	api.add("POST", "/api/threads/:threadId/complete", func(r *http.Request) (any, int, error) {
		thread, err := repo.CompleteThread(r.Context(), r.PathValue("threadId"))
		return object{"thread": thread}, 200, err
	})
	api.add("DELETE", "/api/threads/:threadId/aside", func(r *http.Request) (any, int, error) {
		id := r.PathValue("threadId")
		if _, err := repo.EndAside(r.Context(), id, "user"); err != nil {
			return nil, 0, err
		}
		thread, err := repo.GetThread(r.Context(), id)
		return object{"thread": thread}, 200, err
	})
	api.add("DELETE", "/api/threads/:threadId/references/:referenceId", func(r *http.Request) (any, int, error) {
		thread, err := repo.RemoveThreadReference(r.Context(), r.PathValue("threadId"), r.PathValue("referenceId"))
		if err == nil && api.options.Workspaces != nil {
			err = api.options.Workspaces.InvalidateReference(r.Context(), thread.ProjectID, r.PathValue("referenceId"))
		}
		return object{"thread": thread}, 200, err
	})
	api.add("POST", "/api/threads/:threadId/references/:referenceId/keep", func(r *http.Request) (any, int, error) {
		id := r.PathValue("threadId")
		if _, err := repo.KeepThreadReference(r.Context(), id, r.PathValue("referenceId")); err != nil {
			return nil, 0, err
		}
		thread, err := repo.GetThread(r.Context(), id)
		return object{"thread": thread}, 200, err
	})
	for _, method := range []string{"POST", "DELETE"} {
		pattern := "/api/conversations/:conversationId/wait"
		if method == "POST" {
			pattern += "/now"
		}
		api.add(method, pattern, func(r *http.Request) (any, int, error) {
			how := "cancelled"
			if r.Method == "POST" {
				how = "now"
			}
			_, err := repo.EndWait(r.Context(), r.PathValue("conversationId"), how, "user", nil)
			return nil, 204, err
		})
	}
	api.add("POST", "/api/conversations/:conversationId/read", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		upTo, err := required(body, "upTo")
		if err != nil {
			return nil, 0, err
		}
		return nil, 204, repo.MarkRead(r.Context(), r.PathValue("conversationId"), upTo)
	})
	api.add("POST", "/api/attentions/:attentionId/answer", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		return nil, 204, repo.AnswerAttention(r.Context(), r.PathValue("attentionId"), body["answer"])
	})
	api.add("POST", "/api/messages/:messageId/reactions", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		emoji, err := required(body, "emoji")
		if err != nil {
			return nil, 0, err
		}
		remove := false
		if raw, present := body["remove"]; present {
			var ok bool
			remove, ok = raw.(bool)
			if !ok {
				return nil, 0, badInput("remove")
			}
		}
		message, err := repo.ReactAsUser(r.Context(), r.PathValue("messageId"), emoji, remove)
		return object{"message": message}, 200, err
	})
	api.add("POST", "/api/conversations/:conversationId/stop", func(r *http.Request) (any, int, error) {
		id := r.PathValue("conversationId")
		if _, err := repo.GetConversation(r.Context(), id); err != nil {
			return nil, 0, err
		}
		stopped := api.options.Scheduler != nil && api.options.Scheduler.Cancel(id)
		return object{"stopped": stopped}, 200, nil
	})
	api.add("POST", "/api/conversations/:conversationId/retry", func(r *http.Request) (any, int, error) {
		return nil, 204, repo.RequestTurn(r.Context(), r.PathValue("conversationId"))
	})
}
