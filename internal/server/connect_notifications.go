package server

import (
	"context"
	"slices"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/notifications"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func (h *settingsServiceHandler) notifierRegistry() *notifications.Registry {
	if h.api.options.Notifiers != nil {
		return h.api.options.Notifiers
	}
	if h.api.options.Notifications != nil {
		return h.api.options.Notifications.Notifiers()
	}
	if h.api.options.Plugins != nil {
		return h.api.options.Plugins.Notifiers()
	}
	return nil
}

func (h *settingsServiceHandler) notifierKinds() []sdk.NotifierProviderInfo {
	if registry := h.notifierRegistry(); registry != nil {
		return registry.Kinds()
	}
	return []sdk.NotifierProviderInfo{}
}

func (h *settingsServiceHandler) notificationSettingsView(ctx context.Context) (*ariadnev1.NotificationSettings, error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	if h.api.options.Notifications != nil {
		if _, err := h.api.options.Notifications.EnsureVAPID(); err != nil {
			return nil, err
		}
	} else {
		if _, err := h.api.options.Settings.EnsureVAPID(notifications.GenerateVAPIDKeys); err != nil {
			return nil, err
		}
	}
	var ready func(string, settings.Object) bool
	if registry := h.notifierRegistry(); registry != nil {
		ready = registry.Ready
	}
	view := h.api.options.Settings.PublicNotifications(h.notifierKinds(), ready)
	var subs []notifications.PushSubscription
	if h.api.options.Notifications != nil {
		list, err := h.api.options.Notifications.Subscriptions().List(ctx)
		if err != nil {
			return nil, err
		}
		subs = list
	}
	return toProtoNotificationSettings(view, subs), nil
}

func (h *settingsServiceHandler) GetNotificationSettings(
	ctx context.Context,
	_ *connect.Request[ariadnev1.GetNotificationSettingsRequest],
) (*connect.Response[ariadnev1.GetNotificationSettingsResponse], error) {
	view, err := h.notificationSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetNotificationSettingsResponse{
		Notifications: view,
	}), nil
}

func (h *settingsServiceHandler) SaveNotificationSettings(
	ctx context.Context,
	req *connect.Request[ariadnev1.SaveNotificationSettingsRequest],
) (*connect.Response[ariadnev1.SaveNotificationSettingsResponse], error) {
	if h.api.options.Settings == nil {
		return nil, domainerr.NotFound("settings")
	}
	webhooks := make([]any, 0, len(req.Msg.GetWebhooks()))
	for _, hook := range req.Msg.GetWebhooks() {
		webhooks = append(webhooks, webhookInputToObject(hook))
	}
	notifiers := map[string]any{}
	for kind, item := range req.Msg.GetNotifiers() {
		notifiers[kind] = notifierInputToObject(item)
	}
	input := settings.Object{
		"enabled":        req.Msg.GetEnabled(),
		"webPushEnabled": req.Msg.GetWebPushEnabled(),
		"webhooks":       webhooks,
		"notifiers":      notifiers,
	}
	var validate func(string, settings.Object) error
	if registry := h.notifierRegistry(); registry != nil {
		validate = registry.Validate
	}
	if err := h.api.options.Settings.UpdateNotifications(input, h.notifierKinds(), validate); err != nil {
		return nil, err
	}
	view, err := h.notificationSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SaveNotificationSettingsResponse{
		Notifications: view,
	}), nil
}

func (h *settingsServiceHandler) SubscribePush(
	ctx context.Context,
	req *connect.Request[ariadnev1.SubscribePushRequest],
) (*connect.Response[ariadnev1.SubscribePushResponse], error) {
	if h.api.options.Notifications == nil {
		return nil, domainerr.NotFound("notifications")
	}
	sub, err := h.api.options.Notifications.Subscriptions().Upsert(ctx, notifications.SubscribeInput{
		Endpoint:  req.Msg.GetEndpoint(),
		P256DH:    req.Msg.GetP256Dh(),
		Auth:      req.Msg.GetAuth(),
		Label:     req.Msg.GetLabel(),
		UserAgent: req.Msg.GetUserAgent(),
	})
	if err != nil {
		return nil, err
	}
	all, err := h.api.options.Notifications.Subscriptions().List(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SubscribePushResponse{
		Subscription:  toProtoPushSubscription(sub),
		Subscriptions: toProtoPushSubscriptions(all),
	}), nil
}

func (h *settingsServiceHandler) UnsubscribePush(
	ctx context.Context,
	req *connect.Request[ariadnev1.UnsubscribePushRequest],
) (*connect.Response[ariadnev1.UnsubscribePushResponse], error) {
	if h.api.options.Notifications == nil {
		return nil, domainerr.NotFound("notifications")
	}
	if err := h.api.options.Notifications.Subscriptions().DeleteByEndpoint(ctx, req.Msg.GetEndpoint()); err != nil {
		return nil, err
	}
	all, err := h.api.options.Notifications.Subscriptions().List(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.UnsubscribePushResponse{
		Subscriptions: toProtoPushSubscriptions(all),
	}), nil
}

func (h *settingsServiceHandler) DeletePushSubscription(
	ctx context.Context,
	req *connect.Request[ariadnev1.DeletePushSubscriptionRequest],
) (*connect.Response[ariadnev1.DeletePushSubscriptionResponse], error) {
	if h.api.options.Notifications == nil {
		return nil, domainerr.NotFound("notifications")
	}
	if err := h.api.options.Notifications.Subscriptions().DeleteByID(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	all, err := h.api.options.Notifications.Subscriptions().List(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.DeletePushSubscriptionResponse{
		Subscriptions: toProtoPushSubscriptions(all),
	}), nil
}

func (h *settingsServiceHandler) TestNotification(
	ctx context.Context,
	req *connect.Request[ariadnev1.TestNotificationRequest],
) (*connect.Response[ariadnev1.TestNotificationResponse], error) {
	if h.api.options.Notifications == nil {
		return nil, domainerr.NotFound("notifications")
	}
	testReq := notifications.TestRequest{
		Channel:  req.Msg.GetChannel(),
		TargetID: req.Msg.GetTargetId(),
	}
	if req.Msg.WebhookDraft != nil {
		testReq.WebhookDraft = webhookInputToObject(req.Msg.WebhookDraft)
	}
	if req.Msg.NotifierDraft != nil {
		testReq.NotifierDraft = notifierInputToObject(req.Msg.NotifierDraft)
	}
	result, err := h.api.options.Notifications.Test(ctx, testReq)
	if err != nil {
		return nil, err
	}
	resp := &ariadnev1.TestNotificationResponse{
		Ok:        result.OK,
		Message:   toProtoOptionalLocalizedText(result.Message),
		Delivered: int32(result.Delivered),
		Failed:    int32(result.Failed),
	}
	if result.Status != nil {
		status := int32(*result.Status)
		resp.Status = &status
	}
	return connect.NewResponse(resp), nil
}

func (h *settingsServiceHandler) ReportNotificationPresence(
	_ context.Context,
	req *connect.Request[ariadnev1.ReportNotificationPresenceRequest],
) (*connect.Response[ariadnev1.ReportNotificationPresenceResponse], error) {
	if h.api.options.Notifications != nil {
		h.api.options.Notifications.Presence().Report(
			req.Msg.GetClientId(),
			req.Msg.GetFocused(),
			req.Msg.GetProjectId(),
			req.Msg.GetThreadId(),
		)
	}
	return connect.NewResponse(&ariadnev1.ReportNotificationPresenceResponse{Ok: true}), nil
}

func webhookInputToObject(hook *ariadnev1.WebhookInput) settings.Object {
	if hook == nil {
		return settings.Object{}
	}
	out := settings.Object{
		"id":          hook.GetId(),
		"name":        hook.GetName(),
		"url":         hook.GetUrl(),
		"enabled":     hook.GetEnabled(),
		"clearSecret": hook.GetClearSecret(),
		"headers":     secretPairsToAny(hook.GetHeaders()),
	}
	if hook.Secret != nil {
		out["secret"] = *hook.Secret
	}
	return out
}

func notifierInputToObject(item *ariadnev1.NotifierServiceInput) settings.Object {
	if item == nil {
		return settings.Object{}
	}
	return settings.Object{
		"enabled": item.GetEnabled(),
		"values":  structToMap(item.Values),
	}
}

func toProtoPushSubscription(item notifications.PushSubscription) *ariadnev1.PushSubscriptionView {
	return &ariadnev1.PushSubscriptionView{
		Id:         item.ID,
		Endpoint:   item.Endpoint,
		Label:      item.Label,
		UserAgent:  item.UserAgent,
		CreatedAt:  item.CreatedAt,
		LastUsedAt: item.LastUsedAt,
		LastError:  item.LastError,
	}
}

func toProtoPushSubscriptions(items []notifications.PushSubscription) []*ariadnev1.PushSubscriptionView {
	out := make([]*ariadnev1.PushSubscriptionView, 0, len(items))
	for _, item := range items {
		out = append(out, toProtoPushSubscription(item))
	}
	return out
}

func toProtoNotificationSettings(view settings.PublicNotificationView, subs []notifications.PushSubscription) *ariadnev1.NotificationSettings {
	webhooks := make([]*ariadnev1.WebhookView, 0, len(view.Webhooks))
	for _, hook := range view.Webhooks {
		webhooks = append(webhooks, &ariadnev1.WebhookView{
			Id:        hook.ID,
			Name:      hook.Name,
			Url:       hook.URL,
			Enabled:   hook.Enabled,
			SecretSet: hook.SecretSet,
			Headers:   slices.Clone(hook.Headers),
		})
	}
	notifiers := make([]*ariadnev1.NotifierServiceView, 0, len(view.Notifiers))
	for _, item := range view.Notifiers {
		notifiers = append(notifiers, &ariadnev1.NotifierServiceView{
			Kind:        item.Info.Kind,
			Title:       toProtoLocalizedText(item.Info.Title),
			Description: toProtoOptionalLocalizedText(item.Info.Description),
			Settings:    toProtoSettingsSchema(item.Info.Settings),
			Capabilities: &ariadnev1.NotifierCapabilities{
				SupportsCheck: item.Info.Capabilities.SupportsCheck,
			},
			Enabled:    item.Enabled,
			Values:     mapToStruct(item.Values),
			SecretsSet: slices.Clone(item.SecretsSet),
			Ready:      item.Ready,
		})
	}
	return &ariadnev1.NotificationSettings{
		Enabled:        view.Enabled,
		WebPushEnabled: view.WebPushEnabled,
		VapidPublicKey: view.VAPIDPublicKey,
		Subscriptions:  toProtoPushSubscriptions(subs),
		Webhooks:       webhooks,
		Notifiers:      notifiers,
	}
}
