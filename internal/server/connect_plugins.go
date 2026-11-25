package server

import (
	"context"
	"net/http"
	"slices"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/plugins"
)

type pluginServiceHandler struct {
	api *API
}

func (api *API) registerPluginsRPC() {
	serviceDesc := ariadnev1.File_ariadne_v1_plugins_proto.Services().ByName("PluginService")
	handler := &pluginServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewPluginServiceHandler(handler, opts...)
	})
}

func (h *pluginServiceHandler) pluginRecordsProto(ctx context.Context) []*ariadnev1.PluginRecord {
	var records []plugins.Record
	if host := h.api.options.Plugins; host != nil {
		records = host.Records()
	}
	var projects []core.ProjectView
	if h.api.options.Repository != nil {
		for _, record := range records {
			if len(record.Provides.Contexts) > 0 {
				if listed, err := h.api.options.Repository.ListProjects(ctx); err == nil {
					projects = listed
				}
				break
			}
		}
	}
	activeSearch := ""
	var activeNotifiers []string
	if h.api.options.Settings != nil {
		activeSearch = h.api.options.Settings.ActiveSearchProvider()
		activeNotifiers = h.api.options.Settings.ActiveNotifierKinds()
	}
	out := make([]*ariadnev1.PluginRecord, 0, len(records))
	for _, record := range records {
		proto := toProtoPluginRecord(record)
		proto.Usage = h.pluginUsageProto(record.Provides, activeSearch, activeNotifiers, projects)
		out = append(out, proto)
	}
	return out
}

func (h *pluginServiceHandler) pluginUsageProto(
	provides plugins.Provides,
	activeSearch string,
	activeNotifiers []string,
	projects []core.ProjectView,
) *ariadnev1.PluginUsage {
	usage := &ariadnev1.PluginUsage{}
	if h.api.options.Settings != nil && len(provides.Models) > 0 {
		usage.ModelProviders, usage.ModelRoles = h.api.options.Settings.ModelUsageByKinds(provides.Models)
	}
	if activeSearch != "" && slices.Contains(provides.Searches, activeSearch) {
		usage.Searches = []string{activeSearch}
	}
	for _, kind := range provides.Notifiers {
		if slices.Contains(activeNotifiers, kind) {
			usage.Notifiers = append(usage.Notifiers, kind)
		}
	}
	if len(provides.Contexts) > 0 && len(projects) > 0 {
		for _, project := range projects {
			if projectUsesContextKind(project, provides.Contexts) {
				name := project.Name
				if name == "" {
					name = project.ID
				}
				usage.Projects = append(usage.Projects, name)
			}
		}
	}
	return usage
}

func projectUsesContextKind(project core.ProjectView, kinds []string) bool {
	for _, source := range project.WritableSources().Sources {
		if slices.Contains(kinds, source.Kind) {
			return true
		}
		if hostingKind, _ := source.Hosting["kind"].(string); hostingKind != "" && slices.Contains(kinds, hostingKind) {
			return true
		}
	}
	if refs, ok := project.References.([]any); ok {
		for _, raw := range refs {
			if ref, ok := raw.(map[string]any); ok {
				if kind, _ := ref["kind"].(string); kind != "" && slices.Contains(kinds, kind) {
					return true
				}
			}
		}
	}
	return false
}

func (h *pluginServiceHandler) ListPlugins(
	ctx context.Context,
	_ *connect.Request[ariadnev1.ListPluginsRequest],
) (*connect.Response[ariadnev1.ListPluginsResponse], error) {
	return connect.NewResponse(&ariadnev1.ListPluginsResponse{
		Plugins: h.pluginRecordsProto(ctx),
	}), nil
}

func (h *pluginServiceHandler) GetPluginConfig(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetPluginConfigRequest],
) (*connect.Response[ariadnev1.GetPluginConfigResponse], error) {
	id := req.Msg.GetPluginId()
	host := h.api.options.Plugins
	if host == nil {
		return nil, domainerr.NotFound(id)
	}
	view, err := host.Settings(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetPluginConfigResponse{
		Config: toProtoPluginConfig(view),
	}), nil
}

func (h *pluginServiceHandler) SavePluginConfig(
	ctx context.Context,
	req *connect.Request[ariadnev1.SavePluginConfigRequest],
) (*connect.Response[ariadnev1.SavePluginConfigResponse], error) {
	id := req.Msg.GetPluginId()
	host := h.api.options.Plugins
	if host == nil {
		return nil, domainerr.NotFound(id)
	}
	if req.Msg.Values == nil {
		return nil, badInput("values")
	}
	view, err := host.SaveSettings(ctx, id, structToMap(req.Msg.Values))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SavePluginConfigResponse{
		Config: toProtoPluginConfig(view),
	}), nil
}

func (h *pluginServiceHandler) SetPluginEnabled(
	ctx context.Context,
	req *connect.Request[ariadnev1.SetPluginEnabledRequest],
) (*connect.Response[ariadnev1.SetPluginEnabledResponse], error) {
	id := req.Msg.GetPluginId()
	host := h.api.options.Plugins
	if host == nil {
		return nil, domainerr.NotFound(id)
	}
	if err := host.SetBuiltinEnabled(ctx, h.api.options.Settings, id, req.Msg.GetEnabled()); err != nil {
		return nil, err
	}
	if h.api.options.Data != nil {
		h.api.options.Data.InvalidateUsage()
	}
	return connect.NewResponse(&ariadnev1.SetPluginEnabledResponse{
		Plugins: h.pluginRecordsProto(ctx),
	}), nil
}

func (h *pluginServiceHandler) AddPlugin(
	ctx context.Context,
	req *connect.Request[ariadnev1.AddPluginRequest],
) (*connect.Response[ariadnev1.AddPluginResponse], error) {
	host := h.api.options.Plugins
	if host == nil {
		return nil, badInput("plugins")
	}
	if err := host.AddPath(ctx, h.api.options.Settings, req.Msg.GetPath()); err != nil {
		return nil, err
	}
	if h.api.options.Data != nil {
		h.api.options.Data.InvalidateUsage()
	}
	return connect.NewResponse(&ariadnev1.AddPluginResponse{
		Plugins: h.pluginRecordsProto(ctx),
	}), nil
}

func (h *pluginServiceHandler) DeletePlugin(
	ctx context.Context,
	req *connect.Request[ariadnev1.DeletePluginRequest],
) (*connect.Response[ariadnev1.DeletePluginResponse], error) {
	host := h.api.options.Plugins
	if host == nil {
		return nil, badInput("plugins")
	}
	if err := host.RemovePath(ctx, h.api.options.Settings, req.Msg.GetPath()); err != nil {
		return nil, err
	}
	if h.api.options.Data != nil {
		h.api.options.Data.InvalidateUsage()
	}
	return connect.NewResponse(&ariadnev1.DeletePluginResponse{
		Plugins: h.pluginRecordsProto(ctx),
	}), nil
}

func toProtoPluginRecord(record plugins.Record) *ariadnev1.PluginRecord {
	return &ariadnev1.PluginRecord{
		Id:     record.ID,
		Name:   toProtoOptionalLocalizedText(record.Name),
		Source: record.Source,
		Path:   record.Path,
		Status: record.Status,
		Error:  record.Error,
		Provides: &ariadnev1.PluginProvides{
			Models:     slices.Clone(record.Provides.Models),
			Contexts:   slices.Clone(record.Provides.Contexts),
			Dashboards: slices.Clone(record.Provides.Dashboards),
			Searches:   slices.Clone(record.Provides.Searches),
			Links:      slices.Clone(record.Provides.Links),
			Notifiers:  slices.Clone(record.Provides.Notifiers),
		},
		Configurable: record.Configurable,
	}
}

func toProtoPluginConfig(view plugins.SettingsView) *ariadnev1.PluginConfig {
	out := &ariadnev1.PluginConfig{
		Name:       toProtoLocalizedText(view.Name),
		FieldOrder: slices.Clone(view.FieldOrder),
		Values:     mapToStruct(view.Values),
		SecretsSet: slices.Clone(view.SecretsSet),
		Ready:      view.Ready,
	}
	if view.Schema != nil {
		out.Schema = toProtoSettingsSchema(*view.Schema)
	}
	return out
}
