package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

const (
	deliveryTimeout = 15 * time.Second
	eventBatchLimit = 100
)

type Options struct {
	Repository            *core.Repository
	Settings              *settings.Store
	Notifiers             *Registry
	HTTPClient            *http.Client
	Now                   func() time.Time
	OnError               func(error)
	AllowInsecureEndpoint bool
}

type TestRequest struct {
	Channel       string
	TargetID      string
	WebhookDraft  settings.Object
	NotifierDraft settings.Object
}

type TestResult struct {
	OK        bool
	Status    *int
	Message   sdk.LocalizedText
	Delivered int
	Failed    int
}

type Service struct {
	options       Options
	subscriptions *SubscriptionStore
	presence      *PresenceTracker
	client        *http.Client
	notifiers     *Registry
	now           func() time.Time

	mu     sync.Mutex
	life   context.Context
	cancel context.CancelFunc
	done   chan struct{}
	cursor int64
}

func NewService(options Options) (*Service, error) {
	if options.Repository == nil || options.Settings == nil {
		return nil, errors.New("notifications service requires repository and settings")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: deliveryTimeout}
	}
	notifiers := options.Notifiers
	if notifiers == nil {
		notifiers = NewRegistry()
	}
	subs := NewSubscriptionStore(options.Repository.Store.DB(), now)
	subs.allowInsecureEndpoint = options.AllowInsecureEndpoint
	return &Service{
		options:       options,
		subscriptions: subs,
		presence:      NewPresenceTracker(now),
		client:        client,
		notifiers:     notifiers,
		now:           now,
	}, nil
}

func (service *Service) Subscriptions() *SubscriptionStore { return service.subscriptions }
func (service *Service) Presence() *PresenceTracker      { return service.presence }
func (service *Service) Notifiers() *Registry            { return service.notifiers }

func (service *Service) EnsureVAPID() (settings.VAPIDConfig, error) {
	return service.options.Settings.EnsureVAPID(GenerateVAPIDKeys)
}

func (service *Service) reportError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if service.options.OnError != nil {
		func() { defer func() { recover() }(); service.options.OnError(err) }()
	}
}

func (service *Service) Start(ctx context.Context) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.cancel != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := service.EnsureVAPID(); err != nil {
		return fmt.Errorf("ensure VAPID keys: %w", err)
	}
	// Start from the current event tail so historical events are never replayed as pushes.
	var tail int64
	if err := service.options.Repository.Store.DB().QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM events`).Scan(&tail); err != nil {
		return err
	}
	service.cursor = tail
	watch, unwatch := service.options.Repository.Store.Watch()
	life, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	service.life = life
	service.cancel = cancel
	service.done = done
	go func() {
		defer close(done)
		defer unwatch()
		for {
			if err := service.drain(life); err != nil {
				service.reportError(err)
			}
			select {
			case <-life.Done():
				return
			case _, ok := <-watch:
				if !ok {
					return
				}
			}
		}
	}()
	return nil
}

func (service *Service) Stop(ctx context.Context) error {
	service.mu.Lock()
	cancel, done := service.cancel, service.done
	if cancel != nil {
		cancel()
	}
	service.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	service.mu.Lock()
	if service.done == done {
		service.life = nil
		service.cancel = nil
		service.done = nil
	}
	service.mu.Unlock()
	return nil
}

func (service *Service) drain(ctx context.Context) error {
	db := service.options.Repository.Store.DB()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		service.mu.Lock()
		since := service.cursor
		service.mu.Unlock()

		rows, err := db.QueryContext(ctx, `SELECT seq, id, type, project_id, data, at
			FROM events WHERE seq > ? ORDER BY seq ASC LIMIT ?`, since, eventBatchLimit)
		if err != nil {
			return err
		}
		batch := make([]eventstore.Event, 0, eventBatchLimit)
		for rows.Next() {
			var ev eventstore.Event
			var data string
			if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Type, &ev.ProjectID, &data, &ev.At); err != nil {
				rows.Close()
				return err
			}
			ev.Data = json.RawMessage(data)
			batch = append(batch, ev)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batch) == 0 {
			return nil
		}
		for _, ev := range batch {
			service.mu.Lock()
			if ev.Seq > service.cursor {
				service.cursor = ev.Seq
			}
			service.mu.Unlock()
			service.handleEvent(ctx, ev)
		}
		if len(batch) < eventBatchLimit {
			return nil
		}
	}
}

func (service *Service) handleEvent(ctx context.Context, ev eventstore.Event) {
	cfg := service.options.Settings.Notifications()
	if !cfg.Enabled {
		return
	}
	// Suppress offline pushes while any client is actively focused (or within leave grace)
	// so iOS/macOS WebKit never sees silent push drops and the user is not double-notified.
	if service.presence.IsActive() {
		return
	}
	locale := service.options.Settings.Locale()
	payload, err := evaluateEvent(ctx, service.options.Repository, ev, locale)
	if err != nil {
		service.reportError(err)
		return
	}
	if payload == nil {
		return
	}
	service.Dispatch(ctx, *payload)
}

// Dispatch fans out a notification payload across all enabled channels with error isolation.
func (service *Service) Dispatch(ctx context.Context, payload sdk.NotificationPayload) {
	cfg := service.options.Settings.Notifications()
	var wg sync.WaitGroup

	if cfg.WebPushEnabled && cfg.VAPID.PublicKey != "" && cfg.VAPID.PrivateKey != "" {
		subs, err := service.subscriptions.List(ctx)
		if err != nil {
			service.reportError(err)
		} else if len(subs) > 0 {
			rawPayload, err := json.Marshal(payload)
			if err != nil {
				service.reportError(err)
			} else {
				for _, sub := range subs {
					wg.Add(1)
					go func(sub PushSubscription) {
						defer wg.Done()
						sendCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
						defer cancel()
						_, err := SendWebPush(sendCtx, service.client, sub, cfg.VAPID, rawPayload, service.now())
						if err != nil {
							if errors.Is(err, ErrSubscriptionGone) {
								if delErr := service.subscriptions.DeleteByID(ctx, sub.ID); delErr != nil {
									service.reportError(delErr)
								}
								return
							}
							_ = service.subscriptions.MarkError(ctx, sub.ID, err.Error())
							service.reportError(fmt.Errorf("web push %s: %w", sub.ID, err))
							return
						}
						_ = service.subscriptions.MarkUsed(ctx, sub.ID, service.now().UnixMilli())
					}(sub)
				}
			}
		}
	}

	for _, hook := range cfg.Webhooks {
		if !hook.Enabled || hook.URL == "" {
			continue
		}
		wg.Add(1)
		go func(hook settings.WebhookConfig) {
			defer wg.Done()
			sendCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
			defer cancel()
			if _, err := SendWebhook(sendCtx, service.client, hook, payload); err != nil {
				service.reportError(fmt.Errorf("webhook %s: %w", hook.ID, err))
			}
		}(hook)
	}

	for _, info := range service.notifiers.Kinds() {
		enabled, values := service.options.Settings.EffectiveNotifierSettings(info)
		if !enabled || !service.notifiers.Ready(info.Kind, values) {
			continue
		}
		wg.Add(1)
		go func(kind string, values map[string]any) {
			defer wg.Done()
			sendCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
			defer cancel()
			if err := service.notifiers.Send(sendCtx, kind, values, payload); err != nil {
				service.reportError(fmt.Errorf("notifier %s: %w", kind, err))
			}
		}(info.Kind, values)
	}

	wg.Wait()
}

func localizedResultMessage(zh, en string) sdk.LocalizedText {
	return sdk.LocalizedText{"zh-CN": zh, "en": en}
}

func (service *Service) testPayload(ctx context.Context) sdk.NotificationPayload {
	locale := service.options.Settings.Locale()
	return sdk.NotificationPayload{
		Event:   "notification.test",
		Title:   localizedAction(locale, "testTitle"),
		Body:    localizedAction(locale, "testBody"),
		URL:     "/",
		Tag:     "ariadne:test",
		NeedsMe: totalNeedsMe(ctx, service.options.Repository),
		At:      service.now().UnixMilli(),
	}
}

// Test sends an immediate test notification over the requested channel, bypassing
// foreground presence suppression so the user always gets feedback in Settings.
func (service *Service) Test(ctx context.Context, req TestRequest) (TestResult, error) {
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	sendCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	switch channel {
	case "webpush":
		vapid, err := service.EnsureVAPID()
		if err != nil {
			return TestResult{}, err
		}
		var targets []PushSubscription
		if targetID := strings.TrimSpace(req.TargetID); targetID != "" {
			item, ok, err := service.subscriptions.GetByID(ctx, targetID)
			if err != nil {
				return TestResult{}, err
			}
			if !ok {
				return TestResult{}, domainerr.NotFound(targetID)
			}
			targets = []PushSubscription{item}
		} else {
			subs, err := service.subscriptions.List(ctx)
			if err != nil {
				return TestResult{}, err
			}
			targets = subs
		}
		if len(targets) == 0 {
			return TestResult{
				OK:      false,
				Message: localizedResultMessage("当前还没有已订阅的推送设备。", "No push subscriptions are registered yet."),
			}, nil
		}
		rawPayload, err := json.Marshal(service.testPayload(ctx))
		if err != nil {
			return TestResult{}, err
		}
		var delivered, failed int
		var lastStatus *int
		var lastErr string
		for _, sub := range targets {
			status, err := SendWebPush(sendCtx, service.client, sub, vapid, rawPayload, service.now())
			if status > 0 {
				s := status
				lastStatus = &s
			}
			if err != nil {
				failed++
				lastErr = secrets.Redact(err.Error(), []string{vapid.PrivateKey, sub.Auth})
				if errors.Is(err, ErrSubscriptionGone) {
					_ = service.subscriptions.DeleteByID(ctx, sub.ID)
				} else {
					_ = service.subscriptions.MarkError(ctx, sub.ID, err.Error())
				}
				continue
			}
			delivered++
			_ = service.subscriptions.MarkUsed(ctx, sub.ID, service.now().UnixMilli())
		}
		if delivered > 0 && failed == 0 {
			return TestResult{
				OK:        true,
				Status:    lastStatus,
				Delivered: delivered,
				Failed:    failed,
				Message: localizedResultMessage(
					fmt.Sprintf("已向 %d 台设备发送测试推送。", delivered),
					fmt.Sprintf("Sent test push to %d device(s).", delivered),
				),
			}, nil
		}
		if delivered > 0 {
			return TestResult{
				OK:        true,
				Status:    lastStatus,
				Delivered: delivered,
				Failed:    failed,
				Message: localizedResultMessage(
					fmt.Sprintf("成功 %d 台，失败 %d 台：%s", delivered, failed, lastErr),
					fmt.Sprintf("%d delivered, %d failed: %s", delivered, failed, lastErr),
				),
			}, nil
		}
		return TestResult{
			OK:        false,
			Status:    lastStatus,
			Delivered: delivered,
			Failed:    failed,
			Message: localizedResultMessage(
				fmt.Sprintf("推送发送失败：%s", lastErr),
				fmt.Sprintf("Push delivery failed: %s", lastErr),
			),
		}, nil

	case "webhook":
		var hook settings.WebhookConfig
		if req.WebhookDraft != nil {
			resolved, err := service.options.Settings.WebhookDraft(req.WebhookDraft)
			if err != nil {
				return TestResult{}, err
			}
			hook = resolved
		} else {
			targetID := strings.TrimSpace(req.TargetID)
			found := false
			for _, candidate := range service.options.Settings.Notifications().Webhooks {
				if candidate.ID == targetID {
					hook = candidate
					found = true
					break
				}
			}
			if !found {
				return TestResult{}, domainerr.NotFound(targetID)
			}
		}
		status, err := SendWebhook(sendCtx, service.client, hook, service.testPayload(ctx))
		var statusPtr *int
		if status > 0 {
			s := status
			statusPtr = &s
		}
		if err != nil {
			redacted := secrets.Redact(err.Error(), webhookSecretValues(hook))
			return TestResult{
				OK:      false,
				Status:  statusPtr,
				Failed:  1,
				Message: localizedResultMessage(redacted, redacted),
			}, nil
		}
		return TestResult{
			OK:        true,
			Status:    statusPtr,
			Delivered: 1,
			Message: localizedResultMessage(
				fmt.Sprintf("Webhook 返回 HTTP %d。", status),
				fmt.Sprintf("Webhook returned HTTP %d.", status),
			),
		}, nil

	case "notifier":
		kind := strings.TrimSpace(req.TargetID)
		provider, ok := service.notifiers.Get(kind)
		if !ok {
			return TestResult{}, domainerr.NotFound(kind)
		}
		var values map[string]any
		if req.NotifierDraft != nil {
			resolved, err := service.options.Settings.NotifierDraft(kind, req.NotifierDraft, service.notifiers.Kinds(), service.notifiers.Validate)
			if err != nil {
				return TestResult{}, err
			}
			values = resolved
		} else {
			_, resolved := service.options.Settings.EffectiveNotifierSettings(provider.Info())
			values = resolved
		}
		if provider.Check != nil {
			result, err := service.notifiers.Check(sendCtx, kind, values)
			if err != nil {
				if errors.Is(sendCtx.Err(), context.Canceled) {
					return TestResult{}, err
				}
				redacted := secrets.Redact(err.Error(), notifierSecretValues(provider.Settings, values))
				return TestResult{
					OK:      false,
					Failed:  1,
					Message: localizedResultMessage(redacted, redacted),
				}, nil
			}
			out := TestResult{
				OK:      result.OK,
				Status:  result.Status,
				Message: result.Message,
			}
			if result.OK {
				out.Delivered = 1
			} else {
				out.Failed = 1
			}
			return out, nil
		}
		if err := service.notifiers.Send(sendCtx, kind, values, service.testPayload(ctx)); err != nil {
			if errors.Is(sendCtx.Err(), context.Canceled) {
				return TestResult{}, err
			}
			redacted := secrets.Redact(err.Error(), notifierSecretValues(provider.Settings, values))
			return TestResult{
				OK:      false,
				Failed:  1,
				Message: localizedResultMessage(redacted, redacted),
			}, nil
		}
		return TestResult{
			OK:        true,
			Delivered: 1,
			Message:   localizedResultMessage("已发送测试通知。", "Test notification sent."),
		}, nil

	default:
		return TestResult{}, domainerr.New("error.invalidInput", map[string]any{"field": "channel"})
	}
}

func webhookSecretValues(hook settings.WebhookConfig) []string {
	out := make([]string, 0, 1+len(hook.Headers))
	if hook.Secret != "" {
		out = append(out, hook.Secret)
	}
	for _, value := range hook.Headers {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func notifierSecretValues(schema sdk.SettingsSchema, values map[string]any) []string {
	out := []string{}
	for key, field := range schema.Properties {
		if !field.Secret {
			continue
		}
		if value, ok := values[key].(string); ok && value != "" {
			out = append(out, value)
		}
	}
	return out
}
