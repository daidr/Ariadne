package server

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/service"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/update"
)

type updateServiceHandler struct {
	api *API
}

func (api *API) registerUpdatesRPC() {
	serviceDesc := ariadnev1.File_ariadne_v1_updates_proto.Services().ByName("UpdateService")
	handler := &updateServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewUpdateServiceHandler(handler, opts...)
	})
}

func (h *updateServiceHandler) service() (*update.Service, error) {
	if h.api.options.Updates == nil {
		return nil, domainerr.NotFound("updates")
	}
	return h.api.options.Updates, nil
}

func (h *updateServiceHandler) GetUpdateStatus(
	_ context.Context,
	_ *connect.Request[ariadnev1.GetUpdateStatusRequest],
) (*connect.Response[ariadnev1.GetUpdateStatusResponse], error) {
	service, err := h.service()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetUpdateStatusResponse{
		Status: toProtoUpdateStatus(service.View()),
	}), nil
}

func (h *updateServiceHandler) SaveUpdatePreferences(
	_ context.Context,
	req *connect.Request[ariadnev1.SaveUpdatePreferencesRequest],
) (*connect.Response[ariadnev1.SaveUpdatePreferencesResponse], error) {
	service, err := h.service()
	if err != nil {
		return nil, err
	}
	value := settings.UpdatePreferences{
		Channel:   req.Msg.GetChannel(),
		Automatic: req.Msg.GetAutomatic(),
	}
	if !value.Valid() {
		return nil, badInput("updates")
	}
	view, err := service.Save(value)
	if err != nil {
		return nil, updateDomainError(err)
	}
	return connect.NewResponse(&ariadnev1.SaveUpdatePreferencesResponse{
		Status: toProtoUpdateStatus(view),
	}), nil
}

func (h *updateServiceHandler) CheckForUpdate(
	ctx context.Context,
	_ *connect.Request[ariadnev1.CheckForUpdateRequest],
) (*connect.Response[ariadnev1.CheckForUpdateResponse], error) {
	service, err := h.service()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CheckForUpdateResponse{
		Status: toProtoUpdateStatus(service.Check(ctx)),
	}), nil
}

func (h *updateServiceHandler) InstallUpdate(
	_ context.Context,
	req *connect.Request[ariadnev1.InstallUpdateRequest],
) (*connect.Response[ariadnev1.InstallUpdateResponse], error) {
	service, err := h.service()
	if err != nil {
		return nil, err
	}
	version := textutil.Trim(req.Msg.GetVersion())
	if version == "" {
		return nil, badInput("version")
	}
	view, err := service.Install(version)
	if err != nil {
		return nil, updateDomainError(err)
	}
	return connect.NewResponse(&ariadnev1.InstallUpdateResponse{
		Status: toProtoUpdateStatus(view),
	}), nil
}

func (h *updateServiceHandler) RestartToApplyUpdate(
	ctx context.Context,
	_ *connect.Request[ariadnev1.RestartToApplyUpdateRequest],
) (*connect.Response[ariadnev1.RestartToApplyUpdateResponse], error) {
	updates, err := h.service()
	if err != nil {
		return nil, err
	}
	ctrl := h.api.options.ServiceControl
	if ctrl == nil || !ctrl.CanRestart() {
		return nil, domainerr.New("updates.error.cannotRestart", nil)
	}
	view := updates.View()
	if view.State != "ready" {
		return nil, updateDomainError(update.ErrUnavailable)
	}
	if _, err := ctrl.BeginDrain(ctx, service.DrainRequest{
		Mode:         service.DrainModeSafePoint,
		Action:       service.ActionRestart,
		AutoShutdown: true,
	}); err != nil {
		if errors.Is(err, service.ErrCannotRestart) {
			return nil, domainerr.New("updates.error.cannotRestart", nil)
		}
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RestartToApplyUpdateResponse{
		Status: toProtoUpdateStatus(updates.View()),
	}), nil
}

func (h *updateServiceHandler) CancelRestart(
	ctx context.Context,
	_ *connect.Request[ariadnev1.CancelRestartRequest],
) (*connect.Response[ariadnev1.CancelRestartResponse], error) {
	updates, err := h.service()
	if err != nil {
		return nil, err
	}
	if ctrl := h.api.options.ServiceControl; ctrl != nil {
		ctrl.CancelDrain(ctx)
	}
	return connect.NewResponse(&ariadnev1.CancelRestartResponse{
		Status: toProtoUpdateStatus(updates.View()),
	}), nil
}

func toProtoUpdateStatus(view update.View) *ariadnev1.UpdateStatus {
	out := &ariadnev1.UpdateStatus{
		Version: view.Version,
		Preferences: &ariadnev1.UpdatePreferences{
			Channel:   view.Preferences.Channel,
			Automatic: view.Preferences.Automatic,
		},
		CanInstall:      view.CanInstall,
		State:           view.State,
		DownloadPercent: int32(view.DownloadPercent),
		CanRestart:      view.CanRestart,
		Restarting:      view.Restarting,
		ActiveTurns:     int32(view.ActiveTurns),
	}
	if view.Release != nil {
		out.Release = &ariadnev1.UpdateRelease{
			Version:     view.Release.Version,
			Notes:       view.Release.Notes,
			Url:         view.Release.URL,
			Prerelease:  view.Release.Prerelease,
			Installable: view.Release.Installable,
		}
	}
	if view.Failure != "" {
		failure := view.Failure
		out.Failure = &failure
	}
	return out
}
