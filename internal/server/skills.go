package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/skills"
)

func (api *API) registerSkills() {
	api.add("GET", "/api/conversations/:conversationId/skills", func(r *http.Request) (any, int, error) {
		conversation, err := api.options.Repository.GetConversation(r.Context(), r.PathValue("conversationId"))
		if err != nil {
			return nil, 0, err
		}
		set, err := skills.Conversation(r.Context(), api.options.Repository, conversation, api.options.Workspaces, api.options.SkillsDirs)
		if err != nil {
			return nil, 0, err
		}
		return object{"skills": set.List()}, 200, nil
	})
}
