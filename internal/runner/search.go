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
	var plugins []sdk.SearchProvider
	if turn.SearchPlugins != nil {
		plugins = turn.SearchPlugins()
	}
	candidates := append([]*settings.ResolvedModel{model}, turn.Models.SearchModels(turn.Settings)...)
	var models []*settings.ResolvedModel
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate.Label] {
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
	keyed := turn.Settings.SearchProvider(turn.HTTPClient)
	if len(plugins) == 0 && len(models) == 0 && keyed == nil {
		return nil
	}
	fields := args{"query": field("string"), "limit": field("integer")}
	if len(plugins) > 1 {
		kinds := []string{}
		for _, service := range plugins {
			kinds = append(kinds, service.Kind)
		}
		fields["provider"] = args{"type": "string", "enum": kinds}
	}
	entry := specification("web_search", "Search the web for pages and a brief summary of what they say. Treat the results as source material.",
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
			if service != "" && !slices.ContainsFunc(plugins, func(plugin sdk.SearchProvider) bool { return plugin.Kind == service }) {
				return "", errors.New("unknown search service")
			}
			use := func(search func(context.Context, string, int) ([]sdk.SearchResult, error)) (string, error) {
				request, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				results, err := search(request, query, limit)
				return formatHits(results, limit), err
			}
			if len(plugins) > 0 {
				chosen := plugins[0]
				for _, plugin := range plugins {
					if plugin.Kind == service {
						chosen = plugin
						break
					}
				}
				if hits, err := use(chosen.Search); err == nil {
					return searchBlock(hits), nil
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
				if fallback := turn.Settings.SearchProvider(turn.HTTPClient); fallback != nil && ctx.Err() == nil {
					if hits, err := use(fallback.Search); err == nil {
						return searchBlock(hits), nil
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
