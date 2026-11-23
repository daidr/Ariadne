package server

import (
	"context"
	"maps"
	"net/http"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/modelcatalog"
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

type settingsServiceHandler struct {
	api *API
}

func (api *API) registerSettingsRPC() {
	serviceDesc := ariadnev1.File_ariadne_v1_settings_proto.Services().ByName("SettingsService")
	handler := &settingsServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewSettingsServiceHandler(handler, opts...)
	})
}

func (h *settingsServiceHandler) GetConcurrency(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetConcurrencyRequest],
) (*connect.Response[ariadnev1.GetConcurrencyResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	return connect.NewResponse(&ariadnev1.GetConcurrencyResponse{
		Concurrency: toProtoConcurrencySettings(h.api.options.Settings.Concurrency()),
	}), nil
}

func (h *settingsServiceHandler) SaveConcurrency(
	_ context.Context,
	req *connect.Request[ariadnev1.SaveConcurrencyRequest],
) (*connect.Response[ariadnev1.SaveConcurrencyResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	value, err := h.api.options.Settings.UpdateConcurrency(object{
		"slots":    float64(req.Msg.GetSlots()),
		"reserved": float64(req.Msg.GetReserved()),
	})
	if err != nil {
		return nil, err
	}
	if h.api.options.Scheduler != nil {
		if err := h.api.options.Scheduler.SetSlots(int(asInt32(value["slots"])), int(asInt32(value["reserved"]))); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&ariadnev1.SaveConcurrencyResponse{
		Concurrency: toProtoConcurrencySettings(value),
	}), nil
}

func (h *settingsServiceHandler) GetInputSettings(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetInputSettingsRequest],
) (*connect.Response[ariadnev1.GetInputSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	return connect.NewResponse(&ariadnev1.GetInputSettingsResponse{
		Input: toProtoInputSettings(h.api.options.Settings.Input()),
	}), nil
}

func (h *settingsServiceHandler) SaveInputSettings(
	_ context.Context,
	req *connect.Request[ariadnev1.SaveInputSettingsRequest],
) (*connect.Response[ariadnev1.SaveInputSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	snippets := make([]any, 0, len(req.Msg.GetSnippets()))
	for _, item := range req.Msg.GetSnippets() {
		snippets = append(snippets, object{
			"id":   item.GetId(),
			"name": item.GetName(),
			"text": item.GetText(),
		})
	}
	body := object{
		"submitOn": req.Msg.GetSubmitOn(),
		"snippets": snippets,
	}
	if len(req.Msg.GetKeys()) > 0 {
		body["keys"] = stringMapToObject(req.Msg.GetKeys())
	}
	value, err := h.api.options.Settings.UpdateInput(body)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveInputSettingsResponse{
		Input: toProtoInputSettings(value),
	}), nil
}

func (h *settingsServiceHandler) GetWorkSettings(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetWorkSettingsRequest],
) (*connect.Response[ariadnev1.GetWorkSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, badInput("settings")
	}
	return connect.NewResponse(&ariadnev1.GetWorkSettingsResponse{
		Work: toProtoWorkSettings(h.api.options.Settings.Work()),
	}), nil
}

func (h *settingsServiceHandler) SaveWorkSettings(
	_ context.Context,
	req *connect.Request[ariadnev1.SaveWorkSettingsRequest],
) (*connect.Response[ariadnev1.SaveWorkSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, badInput("settings")
	}
	body := object{}
	if req.Msg.Instructions != nil {
		body["instructions"] = req.Msg.GetInstructions()
	}
	if req.Msg.Rules != nil {
		rules := make([]any, 0, len(req.Msg.Rules.GetItems()))
		for _, item := range req.Msg.Rules.GetItems() {
			rules = append(rules, object{
				"pattern": item.GetPattern(),
				"action":  item.GetAction(),
			})
		}
		body["rules"] = rules
	}
	if req.Msg.Limits != nil {
		limits := object{}
		if req.Msg.Limits.CommandTimeout != nil {
			limits["commandTimeout"] = float64(req.Msg.Limits.GetCommandTimeout())
		}
		if req.Msg.Limits.CommandTimeoutMax != nil {
			limits["commandTimeoutMax"] = float64(req.Msg.Limits.GetCommandTimeoutMax())
		}
		if req.Msg.Limits.CommandOutput != nil {
			limits["commandOutput"] = float64(req.Msg.Limits.GetCommandOutput())
		}
		if req.Msg.Limits.CompactShare != nil {
			limits["compactShare"] = req.Msg.Limits.GetCompactShare()
		}
		if req.Msg.Limits.ModelRetries != nil {
			limits["modelRetries"] = float64(req.Msg.Limits.GetModelRetries())
		}
		if req.Msg.Limits.LoopRepeats != nil {
			limits["loopRepeats"] = float64(req.Msg.Limits.GetLoopRepeats())
		}
		body["limits"] = limits
	}
	value, err := h.api.options.Settings.UpdateWork(body)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveWorkSettingsResponse{
		Work: toProtoWorkSettings(value),
	}), nil
}

func (h *settingsServiceHandler) searchRegistry() *searches.Registry {
	if h.api.options.Searches != nil {
		return h.api.options.Searches
	}
	if h.api.options.Plugins != nil {
		return h.api.options.Plugins.Searches()
	}
	return nil
}

func (h *settingsServiceHandler) searchKinds() []sdk.SearchProviderInfo {
	if registry := h.searchRegistry(); registry != nil {
		return registry.Kinds()
	}
	return []sdk.SearchProviderInfo{}
}

func (h *settingsServiceHandler) searchSettingsView() *ariadnev1.SearchSettings {
	var models []string
	if h.api.options.SearchAvailable != nil {
		models, _ = h.api.options.SearchAvailable()
	} else if h.api.options.Models != nil {
		for _, model := range h.api.options.Models.SearchModels(h.api.options.Settings) {
			models = append(models, model.Label)
		}
	}
	var ready func(string, map[string]any) bool
	if registry := h.searchRegistry(); registry != nil {
		ready = registry.Ready
	}
	return toProtoSearchSettings(h.api.options.Settings.PublicSearch(h.searchKinds(), ready, models))
}

func (h *settingsServiceHandler) GetSearchSettings(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetSearchSettingsRequest],
) (*connect.Response[ariadnev1.GetSearchSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	return connect.NewResponse(&ariadnev1.GetSearchSettingsResponse{
		Search: h.searchSettingsView(),
	}), nil
}

func (h *settingsServiceHandler) SaveSearchSettings(
	_ context.Context,
	req *connect.Request[ariadnev1.SaveSearchSettingsRequest],
) (*connect.Response[ariadnev1.SaveSearchSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	body := searchInputToObject(req.Msg.Provider, req.Msg.GetServices())
	var validate func(string, map[string]any) error
	if registry := h.searchRegistry(); registry != nil {
		validate = registry.Validate
	}
	if _, err := h.api.options.Settings.UpdateSearch(body, h.searchKinds(), validate); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveSearchSettingsResponse{
		Search: h.searchSettingsView(),
	}), nil
}

func (h *settingsServiceHandler) CheckSearchSettings(
	ctx context.Context,
	req *connect.Request[ariadnev1.CheckSearchSettingsRequest],
) (*connect.Response[ariadnev1.CheckSearchSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	body := searchInputToObject(req.Msg.Provider, req.Msg.GetServices())
	registry := h.searchRegistry()
	if registry == nil {
		return nil, badInput("provider")
	}
	kinds := registry.Kinds()
	draft, err := h.api.options.Settings.SearchDraft(body, kinds, registry.Validate)
	if err != nil {
		return nil, err
	}
	providerKind, _ := draft["provider"].(string)
	if providerKind == "" && len(kinds) > 0 {
		providerKind = kinds[0].Kind
	}
	provider, ok := registry.Get(providerKind)
	if !ok {
		return nil, badInput("provider")
	}
	values := settings.EffectiveSearchSettings(draft, provider.Info())
	checkCtx, cancel := context.WithTimeout(ctx, searchCheckTimeout)
	defer cancel()
	result, err := registry.Check(checkCtx, providerKind, values)
	if err != nil {
		return nil, err
	}
	resp := &ariadnev1.CheckSearchSettingsResponse{
		Ok:      result.OK,
		Message: toProtoOptionalLocalizedText(result.Message),
	}
	if result.Status != nil {
		status := int32(*result.Status)
		resp.Status = &status
	}
	return connect.NewResponse(resp), nil
}

func (h *settingsServiceHandler) GetModelSettings(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetModelSettingsRequest],
) (*connect.Response[ariadnev1.GetModelSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	return connect.NewResponse(&ariadnev1.GetModelSettingsResponse{
		Settings: toProtoModelSettings(h.api.options.Settings.PublicModels(h.api.modelKinds())),
	}), nil
}

func (h *settingsServiceHandler) SaveModelSettings(
	ctx context.Context,
	req *connect.Request[ariadnev1.SaveModelSettingsRequest],
) (*connect.Response[ariadnev1.SaveModelSettingsResponse], error) {
	providers := make([]any, 0, len(req.Msg.GetProviders()))
	for _, p := range req.Msg.GetProviders() {
		entry := object{
			"id":        p.GetId(),
			"kind":      p.GetKind(),
			"name":      p.GetName(),
			"settings":  structToMap(p.GetSettings()),
			"headers":   secretPairsToAny(p.GetHeaders()),
			"extraBody": structToMap(p.GetExtraBody()),
		}
		if p.TimeoutSeconds != nil {
			entry["timeoutSeconds"] = float64(p.GetTimeoutSeconds())
		}
		providers = append(providers, entry)
	}
	models := make([]any, 0, len(req.Msg.GetModels()))
	for _, m := range req.Msg.GetModels() {
		entry := object{
			"id":       m.GetId(),
			"provider": m.GetProvider(),
			"model":    m.GetModel(),
		}
		if m.ContextTokens != nil {
			entry["contextTokens"] = float64(m.GetContextTokens())
		}
		if m.Note != nil {
			entry["note"] = m.GetNote()
		}
		if m.Images != nil {
			entry["images"] = m.GetImages()
		}
		if m.MaxTokens != nil {
			entry["maxTokens"] = float64(m.GetMaxTokens())
		}
		if m.Price != nil {
			entry["price"] = modelPriceToObject(m.Price)
		}
		if m.Efforts != nil {
			efforts := make([]any, 0, len(m.Efforts.GetValues()))
			for _, value := range m.Efforts.GetValues() {
				efforts = append(efforts, value)
			}
			entry["efforts"] = efforts
		}
		models = append(models, entry)
	}
	body := object{
		"providers": providers,
		"models":    models,
		"roles":     modelRolesToObject(req.Msg.GetRoles()),
		"showCost":  req.Msg.GetShowCost(),
	}
	view, problems, err := h.api.saveModelSettings(ctx, body)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveModelSettingsResponse{
		Settings: toProtoModelSettings(view),
		Problems: toProtoModelProblems(problems),
	}), nil
}

func (h *settingsServiceHandler) ListProviderModels(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListProviderModelsRequest],
) (*connect.Response[ariadnev1.ListProviderModelsResponse], error) {
	raw := object{}
	if provider := req.Msg.GetProvider(); provider != nil {
		raw["id"] = provider.GetId()
		raw["kind"] = provider.GetKind()
		raw["settings"] = structToMap(provider.GetSettings())
	}
	listed, hasList, err := h.api.listProviderModels(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !hasList {
		return connect.NewResponse(&ariadnev1.ListProviderModelsResponse{}), nil
	}
	items := make([]*ariadnev1.ListedModel, 0, len(listed))
	for _, item := range listed {
		entry := &ariadnev1.ListedModel{Id: item.ID}
		if item.Title != "" {
			title := item.Title
			entry.Title = &title
		}
		if item.Info != nil {
			entry.Info = toProtoModelInfo(*item.Info)
		}
		items = append(items, entry)
	}
	return connect.NewResponse(&ariadnev1.ListProviderModelsResponse{
		Models: &ariadnev1.ListedModelList{Items: items},
	}), nil
}

func (h *settingsServiceHandler) GetModelInfo(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetModelInfoRequest],
) (*connect.Response[ariadnev1.GetModelInfoResponse], error) {
	facts, err := h.api.lookupModelInfo(ctx, req.Msg.GetModel(), req.Msg.GetHint())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetModelInfoResponse{
		Info: toProtoModelInfo(facts),
	}), nil
}

func (h *settingsServiceHandler) ListFoundSetups(
	_ context.Context,
	_ *connect.Request[ariadnev1.ListFoundSetupsRequest],
) (*connect.Response[ariadnev1.ListFoundSetupsResponse], error) {
	setups := h.api.foundSetups()
	found := make([]*ariadnev1.FoundSetup, 0, len(setups))
	for _, setup := range setups {
		item := &ariadnev1.FoundSetup{
			Id:    setup.ID,
			Kind:  setup.Kind,
			Title: toProtoLocalizedText(setup.Title),
		}
		if env, ok := setup.From["env"]; ok && env != "" {
			item.Source = &ariadnev1.FoundSetup_Env{Env: env}
		} else if program, ok := setup.From["program"]; ok && program != "" {
			item.Source = &ariadnev1.FoundSetup_Program{Program: program}
		}
		found = append(found, item)
	}
	return connect.NewResponse(&ariadnev1.ListFoundSetupsResponse{Found: found}), nil
}

func (h *settingsServiceHandler) UseFoundSetup(
	ctx context.Context,
	req *connect.Request[ariadnev1.UseFoundSetupRequest],
) (*connect.Response[ariadnev1.UseFoundSetupResponse], error) {
	view, problems, err := h.api.useFoundSetup(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.UseFoundSetupResponse{
		Settings: toProtoModelSettings(view),
		Problems: toProtoModelProblems(problems),
	}), nil
}

func (h *settingsServiceHandler) mcpSettingsProto() ([]*ariadnev1.McpServerView, []*ariadnev1.McpServerStatus, error) {
	var rawServers []any
	if h.api.options.Settings != nil {
		rawServers = h.api.options.Settings.PublicMCP()
	}
	var rawStatus []mcp.ServerStatus
	if h.api.options.MCP != nil {
		var err error
		rawStatus, err = h.api.options.MCP.Status()
		if err != nil {
			return nil, nil, err
		}
	}
	return toProtoMcpServerViews(rawServers), toProtoMcpServerStatuses(rawStatus), nil
}

func (h *settingsServiceHandler) GetMcpSettings(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetMcpSettingsRequest],
) (*connect.Response[ariadnev1.GetMcpSettingsResponse], error) {
	servers, status, err := h.mcpSettingsProto()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetMcpSettingsResponse{
		Servers: servers,
		Status:  status,
	}), nil
}

func (h *settingsServiceHandler) SaveMcpSettings(
	ctx context.Context,
	req *connect.Request[ariadnev1.SaveMcpSettingsRequest],
) (*connect.Response[ariadnev1.SaveMcpSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, badInput("settings")
	}
	items := make([]any, 0, len(req.Msg.GetServers()))
	for _, s := range req.Msg.GetServers() {
		args := make([]any, 0, len(s.GetArgs()))
		for _, arg := range s.GetArgs() {
			args = append(args, arg)
		}
		entry := object{
			"id":        s.GetId(),
			"name":      s.GetName(),
			"transport": s.GetTransport(),
			"args":      args,
			"env":       secretPairsToAny(s.GetEnv()),
			"headers":   secretPairsToAny(s.GetHeaders()),
			"tools":     stringMapToObject(s.GetTools()),
		}
		if s.Command != nil {
			entry["command"] = s.GetCommand()
		}
		if s.Cwd != nil {
			entry["cwd"] = s.GetCwd()
		}
		if s.Url != nil {
			entry["url"] = s.GetUrl()
		}
		items = append(items, entry)
	}
	if err := h.api.options.Settings.UpdateMCP(object{"servers": items}); err != nil {
		return nil, err
	}
	if h.api.options.MCP != nil {
		if err := h.api.options.MCP.Reconfigure(ctx); err != nil {
			return nil, err
		}
	}
	servers, status, err := h.mcpSettingsProto()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveMcpSettingsResponse{
		Servers: servers,
		Status:  status,
	}), nil
}

func (h *settingsServiceHandler) CheckMcpServer(
	ctx context.Context,
	req *connect.Request[ariadnev1.CheckMcpServerRequest],
) (*connect.Response[ariadnev1.CheckMcpServerResponse], error) {
	id := req.Msg.GetServerId()
	if h.api.options.MCP == nil {
		return nil, domainerr.NotFound(id)
	}
	checkCtx, cancel := context.WithTimeout(ctx, mcpProbeTimeout)
	defer cancel()
	status, err := h.api.options.MCP.Check(checkCtx, id)
	if err != nil {
		return nil, err
	}
	if status == nil {
		return nil, domainerr.NotFound(id)
	}
	return connect.NewResponse(&ariadnev1.CheckMcpServerResponse{
		Status: toProtoMcpServerStatus(*status),
	}), nil
}

// --- Shared conversion helpers ---

func toProtoLocalizedText(value sdk.LocalizedText) *ariadnev1.LocalizedText {
	if value == nil {
		return &ariadnev1.LocalizedText{Values: map[string]string{}}
	}
	return &ariadnev1.LocalizedText{Values: maps.Clone(value)}
}

func toProtoOptionalLocalizedText(value sdk.LocalizedText) *ariadnev1.LocalizedText {
	if len(value) == 0 {
		return nil
	}
	return &ariadnev1.LocalizedText{Values: maps.Clone(value)}
}

func anyToProtoLocalizedText(raw any) *ariadnev1.LocalizedText {
	switch value := raw.(type) {
	case sdk.LocalizedText:
		return toProtoLocalizedText(value)
	case map[string]string:
		return &ariadnev1.LocalizedText{Values: maps.Clone(value)}
	case map[string]any:
		out := make(map[string]string, len(value))
		for k, v := range value {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return &ariadnev1.LocalizedText{Values: out}
	default:
		return &ariadnev1.LocalizedText{Values: map[string]string{}}
	}
}

func toProtoSettingField(field sdk.SettingField) *ariadnev1.SettingField {
	out := &ariadnev1.SettingField{
		Type:          field.Type,
		Title:         toProtoLocalizedText(field.Title),
		Description:   toProtoOptionalLocalizedText(field.Description),
		Secret:        field.Secret,
		Directory:     field.Directory,
		Options:       slices.Clone(field.Options),
		NetworkOrigin: field.NetworkOrigin,
	}
	if field.Default != nil {
		if val, err := structpb.NewValue(field.Default); err == nil {
			out.DefaultValue = val
		}
	}
	return out
}

func toProtoSettingsSchema(schema sdk.SettingsSchema) *ariadnev1.SettingsSchema {
	props := make(map[string]*ariadnev1.SettingField, len(schema.Properties))
	for key, field := range schema.Properties {
		props[key] = toProtoSettingField(field)
	}
	seen := map[string]bool{}
	order := make([]string, 0, len(schema.Properties))
	for _, key := range schema.FieldOrder {
		if _, exists := schema.Properties[key]; exists && !seen[key] {
			order = append(order, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0, len(schema.Properties)-len(order))
	for key := range schema.Properties {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	slices.Sort(rest)
	order = append(order, rest...)
	return &ariadnev1.SettingsSchema{
		Type:       schema.Type,
		Properties: props,
		Required:   slices.Clone(schema.Required),
		FieldOrder: order,
	}
}

func mapToStruct(values map[string]any) *structpb.Struct {
	if len(values) == 0 {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	out, err := structpb.NewStruct(values)
	if err != nil {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	return out
}

func structToMap(value *structpb.Struct) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	out := value.AsMap()
	if out == nil {
		return map[string]any{}
	}
	return out
}

func stringMapToObject(values map[string]string) object {
	out := make(object, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func secretPairsToAny(pairs []*ariadnev1.SecretPair) []any {
	out := make([]any, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, object{
			"name":  pair.GetName(),
			"value": pair.GetValue(),
		})
	}
	return out
}

func asInt32(raw any) int32 {
	switch v := raw.(type) {
	case float64:
		return int32(v)
	case int:
		return int32(v)
	case int32:
		return v
	case int64:
		return int32(v)
	default:
		return 0
	}
}

func asOptionalInt32(raw any) (int32, bool) {
	switch v := raw.(type) {
	case float64:
		return int32(v), true
	case int:
		return int32(v), true
	case int32:
		return v, true
	case int64:
		return int32(v), true
	default:
		return 0, false
	}
}

func asOptionalInt64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
}

func asFloat64(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func asStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return slices.Clone(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}

func toProtoConcurrencySettings(raw map[string]any) *ariadnev1.ConcurrencySettings {
	return &ariadnev1.ConcurrencySettings{
		Slots:    asInt32(raw["slots"]),
		Reserved: asInt32(raw["reserved"]),
	}
}

func toProtoInputSettings(raw map[string]any) *ariadnev1.InputSettings {
	submitOn, _ := raw["submitOn"].(string)
	snippetsRaw, _ := raw["snippets"].([]any)
	snippets := make([]*ariadnev1.Snippet, 0, len(snippetsRaw))
	for _, item := range snippetsRaw {
		obj, _ := item.(map[string]any)
		snippets = append(snippets, &ariadnev1.Snippet{
			Id:   text(obj, "id"),
			Name: text(obj, "name"),
			Text: text(obj, "text"),
		})
	}
	keysRaw, _ := raw["keys"].(map[string]any)
	keys := make(map[string]string, len(keysRaw))
	for k, v := range keysRaw {
		if s, ok := v.(string); ok {
			keys[k] = s
		}
	}
	return &ariadnev1.InputSettings{
		SubmitOn: submitOn,
		Snippets: snippets,
		Keys:     keys,
	}
}

func toProtoWorkLimits(raw map[string]any) *ariadnev1.WorkLimits {
	compactShare, _ := asFloat64(raw["compactShare"])
	return &ariadnev1.WorkLimits{
		CommandTimeout:    asInt32(raw["commandTimeout"]),
		CommandTimeoutMax: asInt32(raw["commandTimeoutMax"]),
		CommandOutput:     asInt32(raw["commandOutput"]),
		CompactShare:      compactShare,
		ModelRetries:      asInt32(raw["modelRetries"]),
		LoopRepeats:       asInt32(raw["loopRepeats"]),
	}
}

func toProtoWorkSettings(raw map[string]any) *ariadnev1.WorkSettings {
	instructions, _ := raw["instructions"].(string)
	rulesRaw, _ := raw["rules"].([]any)
	rules := make([]*ariadnev1.CommandRule, 0, len(rulesRaw))
	for _, item := range rulesRaw {
		obj, _ := item.(map[string]any)
		rules = append(rules, &ariadnev1.CommandRule{
			Pattern: text(obj, "pattern"),
			Action:  text(obj, "action"),
		})
	}
	limitsRaw, _ := raw["limits"].(map[string]any)
	defaultsRaw, _ := raw["defaults"].(map[string]any)
	return &ariadnev1.WorkSettings{
		Instructions: instructions,
		Rules:        rules,
		Limits:       toProtoWorkLimits(limitsRaw),
		Defaults:     toProtoWorkLimits(defaultsRaw),
	}
}

func searchInputToObject(provider *string, services map[string]*structpb.Struct) object {
	body := object{}
	if provider != nil {
		body["provider"] = *provider
	}
	if len(services) > 0 {
		svcMap := object{}
		for kind, svc := range services {
			svcMap[kind] = structToMap(svc)
		}
		body["services"] = svcMap
	}
	return body
}

func toProtoSearchCapabilities(raw any) *ariadnev1.SearchCapabilities {
	switch caps := raw.(type) {
	case sdk.SearchCapabilities:
		if caps.MaxQueryRunes == 0 && caps.MaxResults == 0 && !caps.ReturnsAnswer && !caps.SupportsCheck {
			return nil
		}
		return &ariadnev1.SearchCapabilities{
			MaxQueryRunes: int32(caps.MaxQueryRunes),
			MaxResults:    int32(caps.MaxResults),
			ReturnsAnswer: caps.ReturnsAnswer,
			SupportsCheck: caps.SupportsCheck,
		}
	case *sdk.SearchCapabilities:
		if caps == nil {
			return nil
		}
		return toProtoSearchCapabilities(*caps)
	default:
		return nil
	}
}

func toProtoSearchSettings(raw map[string]any) *ariadnev1.SearchSettings {
	provider, _ := raw["provider"].(string)
	ready, _ := raw["ready"].(bool)
	keySet, _ := raw["keySet"].(bool)
	needed, _ := raw["needed"].(bool)
	models := asStringSlice(raw["models"])
	pluginsRaw, _ := raw["plugins"].([]any)
	plugins := make([]*ariadnev1.LocalizedText, 0, len(pluginsRaw))
	for _, item := range pluginsRaw {
		plugins = append(plugins, anyToProtoLocalizedText(item))
	}
	servicesRaw, _ := raw["services"].([]any)
	services := make([]*ariadnev1.SearchServiceView, 0, len(servicesRaw))
	for _, item := range servicesRaw {
		obj, _ := item.(map[string]any)
		var schema *ariadnev1.SettingsSchema
		if rawSchema, ok := obj["settings"].(sdk.SettingsSchema); ok {
			schema = toProtoSettingsSchema(rawSchema)
		} else {
			schema = toProtoSettingsSchema(sdk.SettingsSchema{Type: "object"})
		}
		var description *ariadnev1.LocalizedText
		if rawDesc, exists := obj["description"]; exists && rawDesc != nil {
			desc := anyToProtoLocalizedText(rawDesc)
			if len(desc.GetValues()) > 0 {
				description = desc
			}
		}
		values, _ := obj["values"].(map[string]any)
		svcReady, _ := obj["ready"].(bool)
		services = append(services, &ariadnev1.SearchServiceView{
			Kind:         text(obj, "kind"),
			Title:        anyToProtoLocalizedText(obj["title"]),
			Description:  description,
			Settings:     schema,
			Capabilities: toProtoSearchCapabilities(obj["capabilities"]),
			Values:       mapToStruct(values),
			SecretsSet:   asStringSlice(obj["secretsSet"]),
			Ready:        svcReady,
		})
	}
	out := &ariadnev1.SearchSettings{
		Provider: provider,
		Ready:    ready,
		KeySet:   keySet,
		Needed:   needed,
		Models:   models,
		Plugins:  plugins,
		Services: services,
	}
	if disabledProvider, _ := raw["disabledProvider"].(string); disabledProvider != "" {
		out.DisabledProvider = &disabledProvider
	}
	return out
}

func toProtoModelPriceFromSDK(price *sdk.ModelPrice) *ariadnev1.ModelPrice {
	if price == nil {
		return nil
	}
	out := &ariadnev1.ModelPrice{
		Currency: price.Currency,
		Input:    price.Input,
		Output:   price.Output,
	}
	if price.CacheRead != nil {
		val := *price.CacheRead
		out.CacheRead = &val
	}
	if price.CacheWrite != nil {
		val := *price.CacheWrite
		out.CacheWrite = &val
	}
	return out
}

func toProtoModelPriceFromMap(raw any) *ariadnev1.ModelPrice {
	obj, ok := raw.(map[string]any)
	if !ok || obj == nil {
		return nil
	}
	input, _ := asFloat64(obj["input"])
	output, _ := asFloat64(obj["output"])
	out := &ariadnev1.ModelPrice{
		Currency: text(obj, "currency"),
		Input:    input,
		Output:   output,
	}
	if val, ok := asFloat64(obj["cacheRead"]); ok {
		out.CacheRead = &val
	}
	if val, ok := asFloat64(obj["cacheWrite"]); ok {
		out.CacheWrite = &val
	}
	return out
}

func modelPriceToObject(price *ariadnev1.ModelPrice) object {
	out := object{
		"currency": price.GetCurrency(),
		"input":    price.GetInput(),
		"output":   price.GetOutput(),
	}
	if price.CacheRead != nil {
		out["cacheRead"] = price.GetCacheRead()
	}
	if price.CacheWrite != nil {
		out["cacheWrite"] = price.GetCacheWrite()
	}
	return out
}

func toProtoRoleConfig(raw any) *ariadnev1.RoleConfig {
	obj, _ := raw.(map[string]any)
	out := &ariadnev1.RoleConfig{}
	if value, ok := obj["model"].(string); ok {
		out.Model = &value
	}
	if value, ok := obj["effort"].(string); ok {
		out.Effort = &value
	}
	return out
}

func roleConfigToObject(role *ariadnev1.RoleConfig) object {
	out := object{"model": nil, "effort": nil}
	if role == nil {
		return out
	}
	if role.Model != nil {
		out["model"] = role.GetModel()
	}
	if role.Effort != nil {
		out["effort"] = role.GetEffort()
	}
	return out
}

func modelRolesToObject(roles *ariadnev1.ModelRoles) object {
	if roles == nil {
		return object{
			"thread":      roleConfigToObject(nil),
			"coordinator": roleConfigToObject(nil),
			"triage":      roleConfigToObject(nil),
			"research":    roleConfigToObject(nil),
		}
	}
	return object{
		"thread":      roleConfigToObject(roles.GetThread()),
		"coordinator": roleConfigToObject(roles.GetCoordinator()),
		"triage":      roleConfigToObject(roles.GetTriage()),
		"research":    roleConfigToObject(roles.GetResearch()),
	}
}

func toProtoModelSettings(raw map[string]any) *ariadnev1.ModelSettings {
	kindsRaw, _ := raw["kinds"].([]sdk.ModelProviderInfo)
	kinds := make([]*ariadnev1.ModelKind, 0, len(kindsRaw))
	for _, kind := range kindsRaw {
		kinds = append(kinds, &ariadnev1.ModelKind{
			Kind:     kind.Kind,
			Title:    toProtoLocalizedText(kind.Title),
			Settings: toProtoSettingsSchema(kind.Settings),
			Program:  kind.Program,
		})
	}
	providersRaw, _ := raw["providers"].([]any)
	providers := make([]*ariadnev1.ModelProviderView, 0, len(providersRaw))
	for _, item := range providersRaw {
		obj, _ := item.(map[string]any)
		settingsMap, _ := obj["settings"].(map[string]any)
		extraMap, _ := obj["extraBody"].(map[string]any)
		entry := &ariadnev1.ModelProviderView{
			Id:         text(obj, "id"),
			Kind:       text(obj, "kind"),
			Name:       text(obj, "name"),
			Settings:   mapToStruct(settingsMap),
			SecretsSet: asStringSlice(obj["secretsSet"]),
			Headers:    asStringSlice(obj["headers"]),
			ExtraBody:  mapToStruct(extraMap),
		}
		if timeout, ok := asOptionalInt32(obj["timeoutSeconds"]); ok {
			entry.TimeoutSeconds = &timeout
		}
		providers = append(providers, entry)
	}
	modelsRaw, _ := raw["models"].([]any)
	models := make([]*ariadnev1.ModelEntry, 0, len(modelsRaw))
	for _, item := range modelsRaw {
		obj, _ := item.(map[string]any)
		entry := &ariadnev1.ModelEntry{
			Id:       text(obj, "id"),
			Provider: text(obj, "provider"),
			Model:    text(obj, "model"),
			Price:    toProtoModelPriceFromMap(obj["price"]),
		}
		if tokens, ok := asOptionalInt64(obj["contextTokens"]); ok {
			entry.ContextTokens = &tokens
		}
		if note, ok := obj["note"].(string); ok && note != "" {
			entry.Note = &note
		}
		if images, ok := obj["images"].(bool); ok {
			entry.Images = &images
		}
		if maxTokens, ok := asOptionalInt64(obj["maxTokens"]); ok {
			entry.MaxTokens = &maxTokens
		}
		if rawEfforts, exists := obj["efforts"]; exists && rawEfforts != nil {
			entry.Efforts = &ariadnev1.StringList{Values: asStringSlice(rawEfforts)}
		}
		models = append(models, entry)
	}
	rolesRaw, _ := raw["roles"].(map[string]any)
	roles := &ariadnev1.ModelRoles{
		Thread:      toProtoRoleConfig(rolesRaw["thread"]),
		Coordinator: toProtoRoleConfig(rolesRaw["coordinator"]),
		Triage:      toProtoRoleConfig(rolesRaw["triage"]),
		Research:    toProtoRoleConfig(rolesRaw["research"]),
	}
	showCost, _ := raw["showCost"].(bool)
	return &ariadnev1.ModelSettings{
		Kinds:     kinds,
		Providers: providers,
		Models:    models,
		Roles:     roles,
		ShowCost:  showCost,
	}
}

func toProtoModelProblems(problems []modelProblem) []*ariadnev1.ModelProblem {
	out := make([]*ariadnev1.ModelProblem, 0, len(problems))
	for _, item := range problems {
		var problemProto *ariadnev1.LocalizedError
		if item.Problem != nil {
			problemProto = &ariadnev1.LocalizedError{
				Key:    item.Problem.Key,
				Params: encodeErrorParams(item.Problem.Params),
			}
		}
		out = append(out, &ariadnev1.ModelProblem{
			Model:    item.Model,
			Provider: item.Provider,
			Problem:  problemProto,
		})
	}
	return out
}

func toProtoModelInfo(info modelcatalog.Info) *ariadnev1.ModelInfo {
	out := &ariadnev1.ModelInfo{
		Price: toProtoModelPriceFromSDK(info.Price),
	}
	if info.ContextTokens != nil {
		val := int64(*info.ContextTokens)
		out.ContextTokens = &val
	}
	if info.MaxTokens != nil {
		val := int64(*info.MaxTokens)
		out.MaxTokens = &val
	}
	if info.Images != nil {
		val := *info.Images
		out.Images = &val
	}
	if info.Tools != nil {
		val := *info.Tools
		out.Tools = &val
	}
	if info.Released != "" {
		val := info.Released
		out.Released = &val
	}
	if info.Efforts != nil {
		out.Efforts = &ariadnev1.StringList{Values: slices.Clone(*info.Efforts)}
	}
	return out
}

func toProtoMcpServerViews(raw []any) []*ariadnev1.McpServerView {
	out := make([]*ariadnev1.McpServerView, 0, len(raw))
	for _, item := range raw {
		obj, _ := item.(map[string]any)
		view := &ariadnev1.McpServerView{
			Id:        text(obj, "id"),
			Name:      text(obj, "name"),
			Transport: text(obj, "transport"),
			Args:      asStringSlice(obj["args"]),
			Env:       asStringSlice(obj["env"]),
			Headers:   asStringSlice(obj["headers"]),
			Tools:     map[string]string{},
		}
		if command, ok := obj["command"].(string); ok && command != "" {
			view.Command = &command
		}
		if cwd, ok := obj["cwd"].(string); ok && cwd != "" {
			view.Cwd = &cwd
		}
		if address, ok := obj["url"].(string); ok && address != "" {
			view.Url = &address
		}
		if toolsRaw, ok := obj["tools"].(map[string]any); ok {
			for k, v := range toolsRaw {
				if s, ok := v.(string); ok {
					view.Tools[k] = s
				}
			}
		}
		out = append(out, view)
	}
	return out
}

func toProtoMcpServerStatus(status mcp.ServerStatus) *ariadnev1.McpServerStatus {
	out := &ariadnev1.McpServerStatus{
		Id:    status.ID,
		State: status.State,
		Error: status.Error,
		Log:   status.Log,
	}
	if status.Info != nil {
		out.Info = &ariadnev1.McpServerInfo{
			Name:            status.Info.Name,
			Version:         status.Info.Version,
			ProtocolVersion: status.Info.ProtocolVersion,
		}
	}
	if status.Tools != nil {
		items := make([]*ariadnev1.McpToolInfo, 0, len(*status.Tools))
		for _, tool := range *status.Tools {
			items = append(items, &ariadnev1.McpToolInfo{
				Name:        tool.Name,
				Title:       tool.Title,
				Description: tool.Description,
			})
		}
		out.Tools = &ariadnev1.McpToolInfoList{Items: items}
	}
	return out
}

func toProtoMcpServerStatuses(raw []mcp.ServerStatus) []*ariadnev1.McpServerStatus {
	out := make([]*ariadnev1.McpServerStatus, 0, len(raw))
	for _, item := range raw {
		out = append(out, toProtoMcpServerStatus(item))
	}
	return out
}
