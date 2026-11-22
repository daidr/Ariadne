package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/rpc"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type acpSession struct {
	ID      string            `json:"sessionId"`
	Models  json.RawMessage   `json:"models"`
	Options []acpConfigOption `json:"configOptions"`
}

type acpConfigOption struct {
	ID       string            `json:"id"`
	Category string            `json:"category"`
	Type     string            `json:"type"`
	Current  json.RawMessage   `json:"currentValue"`
	Choices  []acpConfigChoice `json:"options"`
}

type acpConfigChoice struct {
	Value   string            `json:"value"`
	Name    string            `json:"name"`
	Choices []acpConfigChoice `json:"options"`
}

func (option acpConfigOption) values() []acpConfigChoice {
	var values []acpConfigChoice
	for _, choice := range option.Choices {
		if choice.Value != "" {
			values = append(values, choice)
		}
		for _, grouped := range choice.Choices {
			if grouped.Value != "" {
				values = append(values, grouped)
			}
		}
	}
	return values
}

func (session acpSession) option(category string) acpConfigOption {
	for _, option := range session.Options {
		if option.ID != "" && option.Type == "select" && (option.Category == category || option.Category == "" && option.ID == category) {
			return option
		}
	}
	return acpConfigOption{}
}

func (session acpSession) listedModels(ctx context.Context, peer *rpc.Peer, alwaysProbeThoughtLevels bool) []sdk.ListedModel {
	option := session.option("model")
	models := ParseModels(session.Models)
	current := agentruntime.TrimmedJSON(option.Current)
	choices := option.values()
	if len(choices) > 0 {
		// The older model field can omit models or drift from configOptions.
		models = []sdk.ListedModel{{ID: "default"}}
		for _, choice := range choices {
			if choice.Value == "default" {
				continue
			}
			model := sdk.ListedModel{ID: choice.Value, Title: choice.Name}
			models = append(models, model)
			if choice.Value == current {
				models[0].Title, models[0].Resolves = choice.Name, choice.Value
			}
		}
	}
	if session.Options != nil {
		thought := session.option("thought_level")
		levels := thought.efforts()
		probe := len(choices) > 0 && (thought.ID != "" || alwaysProbeThoughtLevels)
		// Keep catalog discovery responsive even if a model selection is slow.
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		for i := range models {
			switch {
			case i == 0 || models[i].ID == models[0].Resolves:
				models[i].Efforts = &levels
			case probe && probeCtx.Err() == nil:
				config, err := setACPConfig(probeCtx, peer, session.ID, option.ID, models[i].ID)
				if err == nil {
					available := config.option("thought_level").efforts()
					models[i].Efforts = &available
				}
			}
		}
	}
	return models
}

func (option acpConfigOption) efforts() []string {
	levels := []string{}
	values := option.effortValues()
	for _, level := range sdk.Efforts {
		if _, ok := values[level]; ok {
			levels = append(levels, level)
		}
	}
	return levels
}

func (option acpConfigOption) effortValues() map[string]string {
	values := map[string]string{}
	for _, choice := range option.values() {
		level := choice.Value
		if level == "extra" {
			level = "xhigh"
		}
		if slices.Contains(sdk.Efforts, level) {
			values[level] = choice.Value
		}
	}
	return values
}

func setACPConfig(ctx context.Context, peer *rpc.Peer, sessionID, id, value string) (acpSession, error) {
	var result acpSession
	params := map[string]any{"sessionId": sessionID, "configId": id, "value": value}
	err := peer.Request(ctx, "session/set_config_option", params, &result)
	if err == nil {
		for _, option := range result.Options {
			if option.ID == id && agentruntime.TrimmedJSON(option.Current) == value {
				return result, nil
			}
		}
		err = fmt.Errorf("the agent did not confirm the value")
	}
	return result, fmt.Errorf("set ACP option %q to %q: %w", id, value, err)
}

func (d *acpDriver) selectModel(ctx context.Context, session acpSession) (acpSession, error) {
	model := textutil.Trim(d.ctx.Model)
	if model == "" || model == "default" {
		return session, nil
	}
	if option := session.option("model"); option.ID != "" {
		if agentruntime.TrimmedJSON(option.Current) == model {
			return session, nil
		}
		for _, choice := range option.values() {
			if choice.Value == model {
				return setACPConfig(ctx, d.stdio.Peer, d.sessionID, option.ID, model)
			}
		}
	}
	if len(session.Models) != 0 {
		for _, listed := range ParseModels(session.Models)[1:] {
			if listed.ID != model {
				continue
			}
			err := d.stdio.Peer.Request(ctx, "session/set_model", map[string]any{"sessionId": d.sessionID, "modelId": model}, nil)
			if err != nil {
				return session, fmt.Errorf("set ACP model %q: %w", model, err)
			}
			return session, nil
		}
	}
	return session, fmt.Errorf("the ACP agent did not offer model %q", model)
}

func (d *acpDriver) Configure(ctx context.Context, effort string) error {
	d.mu.Lock()
	option, defaultLevel := d.thoughtOption, d.defaultLevel
	d.mu.Unlock()
	if option.ID == "" {
		return nil
	}
	levels := option.effortValues()
	value := defaultLevel
	if effort != "" {
		accepted := option.efforts()
		value = levels[sdk.EffortWithin(effort, &accepted)]
	}
	if value == "" || value == agentruntime.TrimmedJSON(option.Current) {
		return nil
	}
	result, err := setACPConfig(ctx, d.stdio.Peer, d.sessionID, option.ID, value)
	if err != nil {
		return d.diagnosticError("session/set_config_option", err)
	}
	d.mu.Lock()
	d.thoughtOption = result.option("thought_level")
	d.mu.Unlock()
	return nil
}
