package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/notifications"
	"github.com/daidr/Ariadne/internal/plugins"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func TestNotificationSettingsPushWebhookNotifierAndPresenceConnect(t *testing.T) {
	t.Parallel()

	var hookSig, hookAuth string
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookSig = r.Header.Get(notifications.SignatureHeader)
		hookAuth = r.Header.Get("Authorization")
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hookSrv.Close()

	pushSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushSrv.Close()

	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{
		Path: filepath.Join(dir, "ariadne.sqlite"),
		Now:  func() time.Time { return time.UnixMilli(1800000000000) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_n", Data: json.RawMessage(`{"name":"Notify","coordinatorId":"crd_n"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	notifierRegistry := notifications.NewRegistry()
	host, err := plugins.New(plugins.Options{
		DataRoot:   filepath.Join(dir, "plugins"),
		Models:     models.New(),
		Sources:    sources.New(),
		Dashboards: dashboard.New(),
		Links:      links.New(links.Options{}),
		Notifiers:  notifierRegistry,
		Settings:   store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())

	var lastNotifierToken string
	if err := host.Load(t.Context(), sdk.Plugin{
		ID:       "mock-notifier-plugin",
		Name:     sdk.LocalizedText{"zh-CN": "模拟通知插件"},
		Provides: sdk.PluginProvides{Notifiers: []string{"mock-bot"}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			_, err := pc.RegisterNotifier(sdk.NotifierProvider{
				Kind:  "mock-bot",
				Title: sdk.LocalizedText{"zh-CN": "模拟机器人"},
				Settings: sdk.SettingsSchema{
					Type: "object",
					Properties: map[string]sdk.SettingField{
						"room":  {Type: "string", Title: sdk.LocalizedText{"zh-CN": "群组"}},
						"token": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "密钥"}, Secret: true},
					},
					Required: []string{"token"},
				},
				Send: func(_ context.Context, values map[string]any, _ sdk.NotificationPayload) error {
					lastNotifierToken, _ = values["token"].(string)
					return nil
				},
				Check: func(_ context.Context, values map[string]any) (sdk.NotifierCheckResult, error) {
					lastNotifierToken, _ = values["token"].(string)
					status := 200
					return sdk.NotifierCheckResult{
						OK:      lastNotifierToken == "notifier-secret-token",
						Status:  &status,
						Message: sdk.LocalizedText{"zh-CN": "检查通过"},
					}, nil
				},
			})
			return nil, err
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	notifSvc, err := notifications.NewService(notifications.Options{
		Repository:            repo,
		Settings:              store,
		Notifiers:             notifierRegistry,
		HTTPClient:            hookSrv.Client(),
		AllowInsecureEndpoint: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	api, err := New(Options{
		Repository:    repo,
		Token:         "test-token",
		Settings:      store,
		Notifiers:     notifierRegistry,
		Notifications: notifSvc,
		Plugins:       host,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	settingsClient := ariadnev1connect.NewSettingsServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	pluginsClient := ariadnev1connect.NewPluginServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	assertNoSecrets := func(msg proto.Message) {
		t.Helper()
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{"webhook-hmac-secret", "Bearer hook-header-secret", "notifier-secret-token"} {
			if strings.Contains(string(raw), leaked) {
				t.Fatalf("secret %q leaked in protobuf response", leaked)
			}
		}
	}

	// 1. GetNotificationSettings auto-generates VAPID public key.
	getRes, err := settingsClient.GetNotificationSettings(t.Context(), connect.NewRequest(&ariadnev1.GetNotificationSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetNotificationSettings: %v", err)
	}
	if getRes.Msg.Notifications.GetVapidPublicKey() == "" {
		t.Fatal("expected generated VAPID public key")
	}

	// 2. Save webhook + notifier secrets and verify no secret leaks + persistence across empty secret edits.
	secretStr := "webhook-hmac-secret"
	notifierValues, _ := structpb.NewStruct(map[string]any{
		"room":  "ops",
		"token": "notifier-secret-token",
	})
	saveRes, err := settingsClient.SaveNotificationSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveNotificationSettingsRequest{
		Enabled:        true,
		WebPushEnabled: true,
		Webhooks: []*ariadnev1.WebhookInput{{
			Id:      "wh_ops",
			Name:    "Ops Hook",
			Url:     hookSrv.URL + "/notify",
			Enabled: true,
			Secret:  &secretStr,
			Headers: []*ariadnev1.SecretPair{{Name: "Authorization", Value: "Bearer hook-header-secret"}},
		}},
		Notifiers: map[string]*ariadnev1.NotifierServiceInput{
			"mock-bot": {Enabled: true, Values: notifierValues},
		},
	}))
	if err != nil {
		t.Fatalf("SaveNotificationSettings: %v", err)
	}
	assertNoSecrets(saveRes.Msg)
	if len(saveRes.Msg.Notifications.Webhooks) != 1 || !saveRes.Msg.Notifications.Webhooks[0].SecretSet {
		t.Fatalf("unexpected saved webhooks: %+v", saveRes.Msg.Notifications.Webhooks)
	}
	if len(saveRes.Msg.Notifications.Notifiers) != 1 || !saveRes.Msg.Notifications.Notifiers[0].Ready {
		t.Fatalf("unexpected saved notifiers: %+v", saveRes.Msg.Notifications.Notifiers)
	}

	// Verify plugin usage reports active notifier.
	plugRes, err := pluginsClient.ListPlugins(t.Context(), connect.NewRequest(&ariadnev1.ListPluginsRequest{}))
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugRes.Msg.Plugins) != 1 || !slices.Equal(plugRes.Msg.Plugins[0].Usage.GetNotifiers(), []string{"mock-bot"}) {
		t.Fatalf("expected plugin usage to report mock-bot, got %+v", plugRes.Msg.Plugins)
	}

	// 3. Test webhook draft keeping existing secret & header without re-sending secret values.
	hookTest, err := settingsClient.TestNotification(t.Context(), connect.NewRequest(&ariadnev1.TestNotificationRequest{
		Channel: "webhook",
		WebhookDraft: &ariadnev1.WebhookInput{
			Id:      "wh_ops",
			Name:    "Ops Hook Draft",
			Url:     hookSrv.URL + "/notify",
			Enabled: true,
			Headers: []*ariadnev1.SecretPair{{Name: "Authorization", Value: ""}},
		},
	}))
	if err != nil || !hookTest.Msg.Ok || hookTest.Msg.GetStatus() != http.StatusAccepted {
		t.Fatalf("TestNotification(webhook): res=%+v err=%v", hookTest.Msg, err)
	}
	if !strings.HasPrefix(hookSig, "sha256=") || hookAuth != "Bearer hook-header-secret" {
		t.Fatalf("expected preserved HMAC sig and header, got sig=%q auth=%q", hookSig, hookAuth)
	}

	// 4. Test notifier using stored secret.
	notifierTest, err := settingsClient.TestNotification(t.Context(), connect.NewRequest(&ariadnev1.TestNotificationRequest{
		Channel:  "notifier",
		TargetId: proto.String("mock-bot"),
	}))
	if err != nil || !notifierTest.Msg.Ok || lastNotifierToken != "notifier-secret-token" {
		t.Fatalf("TestNotification(notifier): res=%+v err=%v token=%q", notifierTest.Msg, err, lastNotifierToken)
	}

	// 5. Subscribe, test Web Push, and delete subscription.
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authBytes := make([]byte, 16)
	_, _ = io.ReadFull(rand.Reader, authBytes)
	subRes, err := settingsClient.SubscribePush(t.Context(), connect.NewRequest(&ariadnev1.SubscribePushRequest{
		Endpoint:  pushSrv.URL + "/push/dev-1",
		P256Dh:    base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:      base64.RawURLEncoding.EncodeToString(authBytes),
		Label:     "Desktop Chrome",
		UserAgent: "Chrome/130",
	}))
	if err != nil || len(subRes.Msg.Subscriptions) != 1 {
		t.Fatalf("SubscribePush: res=%+v err=%v", subRes.Msg, err)
	}
	subID := subRes.Msg.Subscription.Id

	pushTest, err := settingsClient.TestNotification(t.Context(), connect.NewRequest(&ariadnev1.TestNotificationRequest{
		Channel:  "webpush",
		TargetId: proto.String(subID),
	}))
	if err != nil || !pushTest.Msg.Ok || pushTest.Msg.Delivered != 1 {
		t.Fatalf("TestNotification(webpush): res=%+v err=%v", pushTest.Msg, err)
	}

	delRes, err := settingsClient.DeletePushSubscription(t.Context(), connect.NewRequest(&ariadnev1.DeletePushSubscriptionRequest{Id: subID}))
	if err != nil || len(delRes.Msg.Subscriptions) != 0 {
		t.Fatalf("DeletePushSubscription: res=%+v err=%v", delRes.Msg, err)
	}

	// 6. Presence heartbeat RPC updates live tracker.
	_, err = settingsClient.ReportNotificationPresence(t.Context(), connect.NewRequest(&ariadnev1.ReportNotificationPresenceRequest{
		ClientId:  "tab_abc",
		Focused:   true,
		ProjectId: proto.String("prj_n"),
	}))
	if err != nil {
		t.Fatalf("ReportNotificationPresence: %v", err)
	}
	if !notifSvc.Presence().IsActive() {
		t.Fatal("expected PresenceTracker to become active after heartbeat RPC")
	}
}
