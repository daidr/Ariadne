package settings

import (
	"math"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

type ResolvedModel struct {
	Provider        sdk.ModelProviderInfo `json:"provider"`
	Settings        Object                `json:"settings"`
	Model           string                `json:"model"`
	Label           string                `json:"label"`
	ContextTokens   *float64              `json:"contextTokens,omitempty"`
	Images          bool                  `json:"images"`
	Price           Object                `json:"price,omitempty"`
	Effort          string                `json:"effort,omitempty"`
	Request         Object                `json:"request,omitempty"`
	acceptedEfforts *[]string
}

func ValidEffort(value string) bool {
	return hasString(efforts, value)
}

func modelFor(config Object, role, given string) (Object, bool) {
	models := object(config["models"])
	items := list(models["models"])
	configured := object(models["roles"])
	find := func(role string) Object { return findByID(items, text(object(configured[role])["model"])) }
	switch role {
	case "triage":
		if own := find("triage"); own != nil {
			return own, false
		}
		if coordinator := find("coordinator"); coordinator != nil {
			return coordinator, false
		}
		return find("thread"), false
	case "research":
		return find("research"), false
	case "coordinator":
		if own := find("coordinator"); own != nil {
			return own, false
		}
		return find("thread"), false
	default:
		if given != "" {
			if own := findByID(items, given); own != nil {
				return own, true
			}
		}
		return find("thread"), false
	}
}
func effortFor(config Object, role string) string {
	models := object(config["models"])
	items := list(models["models"])
	configured := object(models["roles"])
	own := object(configured[role])
	if role == "triage" && findByID(items, text(own["model"])) == nil && !truthy(own["effort"]) {
		return text(object(configured["coordinator"])["effort"])
	}
	return text(own["effort"])
}
func roleUsesCoordinatorDefault(config Object, role string) bool {
	models := object(config["models"])
	items := list(models["models"])
	configured := object(models["roles"])
	hasModel := func(role string) bool { return findByID(items, text(object(configured[role])["model"])) != nil }
	switch role {
	case "triage":
		own := object(configured["triage"])
		if hasModel("triage") || truthy(own["effort"]) {
			return false
		}
		fallthrough
	case "coordinator":
		coord := object(configured["coordinator"])
		return !hasModel("coordinator") && !truthy(coord["effort"])
	default:
		return false
	}
}
func acceptedEfforts(chosen Object) *[]string {
	raw, exists := chosen["efforts"]
	if !exists || raw == nil {
		return nil
	}
	values := []string{}
	for _, item := range list(raw) {
		values = append(values, text(item))
	}
	return &values
}
func acceptsEffort(chosen Object, wanted string) bool {
	accepted := acceptedEfforts(chosen)
	if accepted == nil {
		return hasString([]string{"low", "medium", "high"}, wanted)
	}
	return hasString(*accepted, wanted)
}
func resolveEffort(config Object, role, givenEffort string, chosen Object, matchedGiven bool) string {
	wanted := effortFor(config, role)
	if role == "thread" && matchedGiven {
		wanted = givenEffort
	}
	if wanted == defaultEffort {
		return ""
	}
	if wanted == "" {
		if roleUsesCoordinatorDefault(config, role) && acceptsEffort(chosen, "low") {
			return "low"
		}
		return ""
	}
	return sdk.EffortWithin(wanted, acceptedEfforts(chosen))
}
func modelLabel(config, model Object) string {
	provider := findByID(list(object(config["models"])["providers"]), text(model["provider"]))
	name := text(provider["name"])
	if provider == nil {
		name = text(model["provider"])
	}
	return name + " / " + text(model["model"])
}
func resolveConfig(config Object, role, givenEffort string, chosen Object, matchedGiven bool, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
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
	accepted := acceptedEfforts(chosen)
	effort := resolveEffort(config, role, givenEffort, chosen, matchedGiven)
	request, _ := clone(object(provider["request"]))
	if truthy(chosen["maxTokens"]) {
		request["maxTokens"] = chosen["maxTokens"]
	}
	ownedSettings, _ := clone(effective(info, object(provider["settings"])))
	result := &ResolvedModel{Provider: info, Settings: ownedSettings, Model: text(chosen["model"]), Label: modelLabel(config, chosen), Images: chosen["images"] != false, Effort: effort, Request: request, acceptedEfforts: accepted}
	if size, ok := number(chosen["contextTokens"]); ok && size != 0 {
		result.ContextTokens = &size
	}
	if price, ok := chosen["price"].(map[string]any); ok {
		result.Price, _ = clone(price)
	}
	return result, nil
}
func resolveModel(config Object, role, given, givenEffort string, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	chosen, matchedGiven := modelFor(config, role, given)
	return resolveConfig(config, role, givenEffort, chosen, matchedGiven, kinds)
}
func (store *Store) ResolveModel(role, given, givenEffort string, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	if !hasString(roles, role) {
		return nil, bad("role")
	}
	return resolveModel(store.snapshot(), role, given, givenEffort, kinds)
}

func (store *Store) ResolveResearch(parent *ResolvedModel, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	config := store.snapshot()
	model, err := resolveModel(config, "research", "", "", kinds)
	if err != nil {
		return nil, err
	}
	if model != nil {
		return model, nil
	}
	if parent == nil {
		return nil, nil
	}
	wanted := effortFor(config, "research")
	if wanted == "" {
		return parent, nil
	}
	cloned := *parent
	cloned.Effort = sdk.EffortWithin(wanted, parent.acceptedEfforts)
	return &cloned, nil
}

func (store *Store) ConversationModel(role, given, givenEffort string) Object {
	config := store.snapshot()
	chosen, matchedGiven := modelFor(config, role, given)
	if chosen == nil {
		return nil
	}
	size, ok := number(chosen["contextTokens"])
	if !ok || size <= 0 {
		size = 128000
	}
	share, _ := number(object(config["limits"])["compactShare"])
	out := Object{"id": chosen["id"], "contextTokens": size, "compactAt": transcript.CompactAt(size, share)}
	if effort := resolveEffort(config, role, givenEffort, chosen, matchedGiven); effort != "" {
		out["effort"] = effort
	}
	return out
}
func (store *Store) ResolveSaved(model Object, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	return resolveConfig(store.snapshot(), "thread", "", model, false, kinds)
}
func (store *Store) ResolveCoordinator(triage bool, contextTokens float64, kinds []sdk.ModelProviderInfo) (*ResolvedModel, error) {
	config := store.snapshot()
	coordinator, err := resolveModel(config, "coordinator", "", "", kinds)
	if !triage || err != nil {
		return coordinator, err
	}
	quick, err := resolveModel(config, "triage", "", "", kinds)
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
func (store *Store) ModelChoices(kinds []sdk.ModelProviderInfo) []any {
	config := store.snapshot()
	models := object(config["models"])
	providers := list(models["providers"])
	thread, _ := modelFor(config, "thread", "")
	choices := []any{}
	for _, raw := range list(models["models"]) {
		model := object(raw)
		provider := findByID(providers, text(model["provider"]))
		if provider == nil {
			continue
		}
		if kinds != nil {
			if _, known := providerInfo(kinds, text(provider["kind"])); !known {
				continue
			}
		}
		choices = append(choices, Object{"id": model["id"], "label": modelLabel(config, model), "note": fallback(model["note"], ""), "threadDefault": model["id"] == thread["id"]})
	}
	return choices
}

// ModelUsageByKinds returns configured provider names and active role keys referencing any of kinds.
func (store *Store) ModelUsageByKinds(kinds []string) ([]string, []string) {
	if len(kinds) == 0 {
		return nil, nil
	}
	models := object(store.snapshot()["models"])
	providerIDs := map[string]bool{}
	var providerNames []string
	for _, raw := range list(models["providers"]) {
		provider := object(raw)
		kind := text(provider["kind"])
		if !slices.Contains(kinds, kind) {
			continue
		}
		if id := text(provider["id"]); id != "" {
			providerIDs[id] = true
		}
		name := text(provider["name"])
		if name == "" {
			name = kind
		}
		if !slices.Contains(providerNames, name) {
			providerNames = append(providerNames, name)
		}
	}
	if len(providerIDs) == 0 {
		return providerNames, nil
	}
	modelIDs := map[string]bool{}
	for _, raw := range list(models["models"]) {
		model := object(raw)
		if providerIDs[text(model["provider"])] {
			if id := text(model["id"]); id != "" {
				modelIDs[id] = true
			}
		}
	}
	roleMap := object(models["roles"])
	var usedRoles []string
	for _, role := range roles {
		entry := object(roleMap[role])
		if modelIDs[text(entry["model"])] {
			usedRoles = append(usedRoles, role)
		}
	}
	return providerNames, usedRoles
}

func (store *Store) ResolvedModels(role string, kinds []sdk.ModelProviderInfo) []*ResolvedModel {
	if !hasString(roles, role) {
		return []*ResolvedModel{}
	}
	config := store.snapshot()
	result := []*ResolvedModel{}
	for _, raw := range list(object(config["models"])["models"]) {
		model, err := resolveConfig(config, role, "", object(raw), false, kinds)
		if err == nil && model != nil {
			result = append(result, model)
		}
	}
	return result
}
