package settings

import (
	"math"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

type ResolvedModel struct {
	Provider      sdk.ModelProviderInfo `json:"provider"`
	Settings      Object                `json:"settings"`
	Model         string                `json:"model"`
	Label         string                `json:"label"`
	ContextTokens *float64              `json:"contextTokens,omitempty"`
	Images        bool                  `json:"images"`
	Price         Object                `json:"price,omitempty"`
	Effort        string                `json:"effort,omitempty"`
	Request       Object                `json:"request,omitempty"`
	Fallback      *ResolvedModel        `json:"fallback,omitempty"`
}

func modelFor(config Object, role, given string) Object {
	models := object(config["models"])
	items := list(models["models"])
	configured := object(models["roles"])
	find := func(role string) Object { return findByID(items, text(object(configured[role])["model"])) }
	switch role {
	case "triage":
		if own := find("triage"); own != nil {
			return own
		}
		if coordinator := find("coordinator"); coordinator != nil {
			return coordinator
		}
		return find("thread")
	case "research":
		return find("research")
	case "coordinator":
		if own := find("coordinator"); own != nil {
			return own
		}
		return find("thread")
	default:
		if given != "" {
			if own := findByID(items, given); own != nil {
				return own
			}
		}
		return find("thread")
	}
}
func fallbackFor(config Object, role string) Object {
	models := object(config["models"])
	configured := object(models["roles"])
	own := object(configured[role])
	fallback := findByID(list(models["models"]), text(own["fallback"]))
	if fallback != nil || role == "research" || role == "thread" || truthy(own["model"]) {
		return fallback
	}
	if role == "triage" {
		return fallbackFor(config, "coordinator")
	}
	return fallbackFor(config, "thread")
}
func effortFor(config Object, role string) string {
	configured := object(object(config["models"])["roles"])
	own := object(configured[role])
	if role == "triage" && !truthy(own["model"]) && !truthy(own["effort"]) {
		return text(object(configured["coordinator"])["effort"])
	}
	return text(own["effort"])
}
func modelLabel(config, model Object) string {
	provider := findByID(list(object(config["models"])["providers"]), text(model["provider"]))
	name := text(provider["name"])
	if provider == nil {
		name = text(model["provider"])
	}
	return name + " / " + text(model["model"])
}
func resolveConfig(config Object, role string, chosen Object, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	if chosen == nil {
		return nil, nil
	}
	provider := findByID(list(object(config["models"])["providers"]), text(chosen["provider"]))
	if provider == nil {
		return nil, nil
	}
	info, exists := providerInfo(kinds, text(provider["kind"]))
	if !exists {
		return nil, domainerr.New("error.modelUnknown", Object{"provider": provider["kind"]})
	}
	var accepted *[]string
	if raw, exists := chosen["efforts"]; exists {
		values := []string{}
		for _, raw := range list(raw) {
			values = append(values, text(raw))
		}
		accepted = &values
	}
	effort := ""
	if wanted := effortFor(config, role); wanted != "" {
		effort = sdk.EffortWithin(wanted, accepted)
	}
	request, _ := clone(object(provider["request"]))
	if truthy(chosen["maxTokens"]) {
		request["maxTokens"] = chosen["maxTokens"]
	}
	ownedSettings, _ := clone(effective(info, object(provider["settings"])))
	result := &ResolvedModel{Provider: info, Settings: ownedSettings, Model: text(chosen["model"]), Label: modelLabel(config, chosen), Images: chosen["images"] != false, Effort: effort, Request: request}
	if size, ok := number(chosen["contextTokens"]); ok && size != 0 {
		result.ContextTokens = &size
	}
	if price, ok := chosen["price"].(map[string]any); ok {
		result.Price, _ = clone(price)
	}
	return result, nil
}
func resolveModel(config Object, role, given string, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	chosen := modelFor(config, role, given)
	resolved, err := resolveConfig(config, role, chosen, kinds)
	if err != nil || resolved == nil {
		return resolved, err
	}
	if backup := fallbackFor(config, role); backup != nil && backup["id"] != chosen["id"] {
		resolved.Fallback, err = resolveConfig(config, role, backup, kinds)
	}
	return resolved, err
}
func (store *Store) ResolveModel(role, given string, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	if !hasString(roles, role) {
		return nil, bad("role")
	}
	return resolveModel(store.snapshot(), role, given, kinds)
}

func (store *Store) ConversationModel(role, given string, fallback bool) Object {
	config := store.snapshot()
	chosen := modelFor(config, role, given)
	if chosen == nil {
		return nil
	}
	if fallback {
		if backup := fallbackFor(config, role); backup != nil {
			chosen = backup
		}
	}
	size, ok := number(chosen["contextTokens"])
	if !ok || size <= 0 {
		size = 128000
	}
	share, _ := number(object(config["limits"])["compactShare"])
	return Object{"id": chosen["id"], "contextTokens": size, "compactAt": transcript.CompactAt(size, share)}
}
func (store *Store) ResolveSaved(model Object, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	return resolveConfig(store.snapshot(), "thread", model, kinds)
}
func (store *Store) ResolveCoordinator(triage bool, contextTokens float64, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	config := store.snapshot()
	coordinator, err := resolveModel(config, "coordinator", "", kinds)
	if !triage || err != nil {
		return coordinator, err
	}
	quick, err := resolveModel(config, "triage", "", kinds)
	if err != nil {
		return nil, err
	}
	if quick == nil {
		return coordinator, nil
	}
	size := float64(128000)
	if quick.ContextTokens != nil {
		size = *quick.ContextTokens
	}
	share, _ := number(object(config["limits"])["compactShare"])
	room := math.Floor(min(size, 400000)*share) * .8
	if contextTokens < room {
		return quick, nil
	}
	return coordinator, nil
}
func (store *Store) ModelChoices() []any {
	config := store.snapshot()
	models := object(config["models"])
	thread := modelFor(config, "thread", "")
	choices := []any{}
	for _, raw := range list(models["models"]) {
		model := object(raw)
		if findByID(list(models["providers"]), text(model["provider"])) == nil {
			continue
		}
		choices = append(choices, Object{"id": model["id"], "label": modelLabel(config, model), "note": fallback(model["note"], ""), "threadDefault": model["id"] == thread["id"]})
	}
	return choices
}

func (store *Store) ResolvedModels(role string, kinds []sdk.ModelProviderInfo) []*ResolvedModel {
	if !hasString(roles, role) {
		return []*ResolvedModel{}
	}
	config := store.snapshot()
	result := []*ResolvedModel{}
	for _, raw := range list(object(config["models"])["models"]) {
		model, err := resolveConfig(config, role, object(raw), kinds)
		if err == nil && model != nil {
			result = append(result, model)
		}
	}
	return result
}
