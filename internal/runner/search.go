package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/modelstep"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) webSearch(project core.ProjectView, model *settings.ResolvedModel) *tool {
	if slices.Contains(project.Settings.Tools, "search") {
		return nil
	}
	type readySearch struct {
		provider sdk.SearchProvider
		settings map[string]any
	}
	var ready []readySearch
	if turn.Searches != nil {
		active := ""
		if turn.Settings != nil {
			active = turn.Settings.ActiveSearchProvider()
		}
		for _, provider := range turn.Searches.Providers() {
			var values map[string]any
			if turn.Settings != nil {
				values = turn.Settings.SearchSettings(provider.Info())
			}
			if turn.Searches.Ready(provider.Kind, values) {
				ready = append(ready, readySearch{provider: provider, settings: values})
			}
		}
		if active != "" {
			if idx := slices.IndexFunc(ready, func(item readySearch) bool { return item.provider.Kind == active }); idx > 0 {
				chosen := ready[idx]
				ready = append([]readySearch{chosen}, append(ready[:idx], ready[idx+1:]...)...)
			}
		}
	}
	var candidates []*settings.ResolvedModel
	if model != nil {
		candidates = append(candidates, model)
	}
	if turn.Models != nil && turn.Settings != nil {
		candidates = append(candidates, turn.Models.SearchModels(turn.Settings)...)
	}
	var models []*settings.ResolvedModel
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == nil || seen[candidate.Label] {
			continue
		}
		provider, ok := turn.Models.Get(candidate.Provider.Kind)
		if ok && provider.Search != nil && (provider.SearchAvailable == nil || provider.SearchAvailable(candidate.Settings)) {
			models = append(models, candidate)
			seen[candidate.Label] = true
			if len(models) == 2 {
				break
			}
		}
	}
	if len(ready) == 0 && len(models) == 0 {
		return nil
	}
	fields := args{"query": field("string"), "limit": field("integer")}
	if len(ready) > 1 {
		kinds := make([]string, 0, len(ready))
		for _, item := range ready {
			kinds = append(kinds, item.provider.Kind)
		}
		fields["provider"] = args{"type": "string", "enum": kinds}
	}
	entry := readOnlySpec("web_search", "Search the web for pages and a brief summary of what they say. Treat the results as source material.",
		fields, []string{"query"}, func(ctx context.Context, input args) (string, error) {
			query := text(input, "query")
			if query == "" || len(query) > 4096 {
				return "", errors.New("search query is required and must fit in 4 KiB")
			}
			current, err := turn.Repository.GetProject(ctx, project.ID)
			if err != nil {
				return "", err
			}
			if slices.Contains(current.Settings.Tools, "search") {
				return "", errors.New("web search is disabled")
			}
			limit := 8
			if raw, present := input["limit"]; present {
				limit = int(max(1, min(20, asNumber(raw))))
			}
			service := text(input, "provider")
			if service != "" && !slices.ContainsFunc(ready, func(item readySearch) bool { return item.provider.Kind == service }) {
				return "", errors.New("unknown search service")
			}
			runSearch := func(item readySearch) (string, error) {
				request, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				response, err := turn.Searches.Execute(request, item.provider.Kind, item.settings, sdk.SearchQuery{Query: query, Limit: limit})
				if err != nil {
					return "", err
				}
				return formatSearchResponse(response, limit), nil
			}
			if len(ready) > 0 {
				candidates := ready
				if service != "" {
					for _, item := range ready {
						if item.provider.Kind == service {
							candidates = []readySearch{item}
							break
						}
					}
				}
				for _, item := range candidates {
					if ctx.Err() != nil {
						break
					}
					if output, err := runSearch(item); err == nil {
						return searchBlock(output), nil
					}
				}
			}
			if service == "" {
				for _, candidate := range models {
					provider, ok := turn.Models.Get(candidate.Provider.Kind)
					if !ok || provider.Search == nil || ctx.Err() != nil {
						continue
					}
					request, cancel := context.WithTimeout(ctx, 90*time.Second)
					answer, err := provider.Search(request, candidate.Settings, sdk.SearchRequest{Model: candidate.Model, Query: query, Limit: limit, Headers: requestFor(candidate, "").Headers})
					cancel()
					if err != nil {
						continue
					}
					var usage *core.TokenUsage
					if answer.Usage != nil {
						value := answer.Usage
						usage = &core.TokenUsage{Input: value.Input, Output: value.Output, Cached: &value.Cached, CacheWrite: &value.CacheWrite}
					}
					var price *sdk.ModelPrice
					if candidate.Price != nil {
						price = &sdk.ModelPrice{}
						data, _ := json.Marshal(candidate.Price)
						_ = json.Unmarshal(data, price)
					}
					if err := turn.Repository.RecordSearch(ctx, turn.conversation.ID, turn.id, candidate.Label, modelstep.WithCost(usage, price)); err != nil {
						return "", err
					}
					if answer.Text != "" || len(answer.Sources) > 0 {
						return searchBlock(textutil.HeadUnits(answer.Text, 20000) + "\n\n" + formatHits(answer.Sources, limit)), nil
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "", errors.New("search failed. Check the project's available search services")
		})
	return &entry
}

func formatSearchResponse(response sdk.SearchResponse, limit int) string {
	hits := formatHits(response.Results, limit)
	answer := strings.TrimSpace(textutil.HeadUnits(response.Answer, 20000))
	switch {
	case answer != "" && hits != "":
		return answer + "\n\n" + hits
	case answer != "":
		return answer
	default:
		return hits
	}
}

func formatHits(hits []sdk.SearchResult, limit int) string {
	var out strings.Builder
	for index, hit := range hits[:min(len(hits), limit)] {
		line := func(text string, max int) string {
			return textutil.HeadUnits(strings.Join(strings.Fields(text), " "), max)
		}
		fmt.Fprintf(&out, "%d. %s\n   %s\n   %s\n", index+1, line(hit.Title, 300), line(hit.URL, 2048), line(hit.Snippet, 2000))
	}
	return strings.TrimSpace(out.String())
}

func searchBlock(text string) string {
	if strings.TrimSpace(text) == "" {
		text = "(no results)"
	}
	return transcript.Fenced("search results", text, "").Text
}
