package server

import (
	"context"
	"net/http"
	"slices"

	"connectrpc.com/connect"

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

func (h *pluginServiceHandler) pluginRecordsProto() []*ariadnev1.PluginRecord {
	var records []plugins.Record
	if host := h.api.options.Plugins; host != nil {
		records = host.Records()
	}
	out := make([]*ariadnev1.PluginRecord, 0, len(records))
	for _, record := range records {
		out = append(out, toProtoPluginRecord(record))
	}
	return out
}

func (h *pluginServiceHandler) ListPlugins(
	_ context.Context,
	_ *connect.Request[ariadnev1.ListPluginsRequest],
) (*connect.Response[ariadnev1.ListPluginsResponse], error) {
	return connect.NewResponse(&ariadnev1.ListPluginsResponse{
		Plugins: h.pluginRecordsProto(),
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
		Plugins: h.pluginRecordsProto(),
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
		Plugins: h.pluginRecordsProto(),
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
		Plugins: h.pluginRecordsProto(),
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
