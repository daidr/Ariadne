package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
	"google.golang.org/protobuf/proto"
)

func testClock(start time.Time) (*time.Time, func() time.Time) {
	current := start
	return &current, func() time.Time { return current }
}

func makeTestSubscriptionKeys(t *testing.T) (*ecdh.PrivateKey, string, []byte, string) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, auth); err != nil {
		t.Fatal(err)
	}
	return uaPriv,
		base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		auth,
		base64.RawURLEncoding.EncodeToString(auth)
}

// Independent RFC 5869 HKDF-SHA256 to cross-check crypto/hkdf usage in production code.
func manualHKDFExtract(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

func manualHKDFExpand(prk, info []byte, length int) []byte {
	mac := hmac.New(sha256.New, prk)
	mac.Write(info)
	mac.Write([]byte{0x01})
	return mac.Sum(nil)[:length]
}

func verifyVAPIDHeader(t *testing.T, authHeader, expectedAud, expectedPublicKey string) {
	t.Helper()
	if !strings.HasPrefix(authHeader, "vapid ") {
		t.Fatalf("unexpected Authorization header: %q", authHeader)
	}
	parts := strings.Split(strings.TrimPrefix(authHeader, "vapid "), ",")
	kv := map[string]string{}
	for _, part := range parts {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			kv[k] = v
		}
	}
	if kv["k"] != expectedPublicKey {
		t.Fatalf("vapid k = %q, want %q", kv["k"], expectedPublicKey)
	}
	tokenParts := strings.Split(kv["t"], ".")
	if len(tokenParts) != 3 {
		t.Fatalf("invalid JWT format: %q", kv["t"])
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(tokenParts[0])
	if err != nil {
		t.Fatalf("decode JWT header: %v", err)
	}
	var hdr map[string]string
	if err := json.Unmarshal(headerBytes, &hdr); err != nil || hdr["alg"] != "ES256" || hdr["typ"] != "JWT" {
		t.Fatalf("unexpected JWT header: %s (%v)", headerBytes, err)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(tokenParts[1])
	if err != nil {
		t.Fatalf("decode JWT claims: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("decode claims JSON: %v", err)
	}
	if claims.Aud != expectedAud || claims.Sub == "" || claims.Exp <= 0 {
		t.Fatalf("unexpected VAPID claims: %+v (want aud=%q)", claims, expectedAud)
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(tokenParts[2])
	if err != nil || len(sigBytes) != 64 {
		t.Fatalf("invalid ES256 signature bytes: len=%d err=%v", len(sigBytes), err)
	}
	pubBytes, err := base64.RawURLEncoding.DecodeString(expectedPublicKey)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		t.Fatalf("invalid VAPID public key")
	}
	pubKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(pubBytes[1:33]),
		Y:     new(big.Int).SetBytes(pubBytes[33:65]),
	}
	digest := sha256.Sum256([]byte(tokenParts[0] + "." + tokenParts[1]))
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	if !ecdsa.Verify(pubKey, digest[:], r, s) {
		t.Fatal("VAPID ES256 signature verification failed")
	}
}

func TestWebPushRFC8291AndRFC8292(t *testing.T) {
	vapid, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	uaPriv, p256dh, authBytes, authEncoded := makeTestSubscriptionKeys(t)

	// Cross-check RFC 8291 encryption with an independent manual HKDF-SHA256 decryption.
	plaintext := []byte(`{"title":"等你回答 · Ariadne","body":"确认发布？","url":"/projects/prj_1?tab=thread:thr_1","needsMe":2}`)
	encrypted, err := encryptWebPushPayload(plaintext, uaPriv.PublicKey().Bytes(), authBytes)
	if err != nil {
		t.Fatalf("encryptWebPushPayload: %v", err)
	}
	if rs := binary.BigEndian.Uint32(encrypted[16:20]); rs != webPushRecordSize {
		t.Fatalf("record size = %d, want %d", rs, webPushRecordSize)
	}
	if encrypted[20] != 65 {
		t.Fatalf("idlen = %d, want 65", encrypted[20])
	}

	// Independent decryption path
	salt := encrypted[0:16]
	asPubBytes := encrypted[21:86]
	ciphertext := encrypted[86:]
	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPubBytes...)
	prkKey := manualHKDFExtract(authBytes, shared)
	ikm := manualHKDFExpand(prkKey, keyInfo, 32)
	prk := manualHKDFExtract(salt, ikm)
	cek := manualHKDFExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := manualHKDFExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	padded, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("independent AES-128-GCM decrypt failed: %v", err)
	}
	if padded[len(padded)-1] != 0x02 {
		t.Fatalf("missing 0x02 delimiter: %x", padded[len(padded)-1])
	}
	if string(padded[:len(padded)-1]) != string(plaintext) {
		t.Fatalf("decrypted = %q, want %q", padded[:len(padded)-1], plaintext)
	}

	// Also verify round-trip over HTTP with VAPID verification and 410 Gone handling.
	var received []byte
	var gotAuth, gotEncoding, gotTTL, gotUrgency string
	var status atomic.Int32
	status.Store(http.StatusCreated)
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEncoding = r.Header.Get("Content-Encoding")
		gotTTL = r.Header.Get("TTL")
		gotUrgency = r.Header.Get("Urgency")
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(int(status.Load()))
	}))
	defer pushServer.Close()

	sub := PushSubscription{
		ID:       "psb_test",
		Endpoint: pushServer.URL + "/push/sub-1",
		P256DH:   p256dh,
		Auth:     authEncoded,
	}
	now := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	code, err := SendWebPush(t.Context(), pushServer.Client(), sub, vapid, plaintext, now)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("SendWebPush: code=%d err=%v", code, err)
	}
	if gotEncoding != "aes128gcm" || gotTTL != "86400" || gotUrgency != "high" {
		t.Fatalf("unexpected headers: encoding=%q ttl=%q urgency=%q", gotEncoding, gotTTL, gotUrgency)
	}
	verifyVAPIDHeader(t, gotAuth, pushServer.URL, vapid.PublicKey)
	decrypted, err := decryptWebPushPayload(received, uaPriv, authBytes)
	if err != nil || string(decrypted) != string(plaintext) {
		t.Fatalf("HTTP body decrypt = %q, err=%v", decrypted, err)
	}

	status.Store(http.StatusGone)
	code, err = SendWebPush(t.Context(), pushServer.Client(), sub, vapid, plaintext, now)
	if code != http.StatusGone || !errors.Is(err, ErrSubscriptionGone) {
		t.Fatalf("410 Gone: code=%d err=%v, want ErrSubscriptionGone", code, err)
	}
}

func TestWebhookDeliveryAndHMACSignature(t *testing.T) {
	var gotBody []byte
	var gotSig, gotEvent, gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(SignatureHeader)
		gotEvent = r.Header.Get(EventHeader)
		gotCustom = r.Header.Get("X-Custom-Token")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook := settings.WebhookConfig{
		ID:      "wh_1",
		Name:    "Test Hook",
		URL:     server.URL + "/hook",
		Enabled: true,
		Secret:  "top-secret-key",
		Headers: map[string]string{"X-Custom-Token": "header-secret"},
	}
	payload := sdk.NotificationPayload{
		Event:       "attention.opened",
		Title:       "等你回答 · Ariadne · 切片 B",
		Body:        "请确认发布策略",
		URL:         "/projects/prj_1?tab=thread:thr_1",
		ProjectID:   "prj_1",
		ProjectName: "Ariadne",
		ThreadID:    "thr_1",
		ThreadTitle: "切片 B",
		Tag:         "ariadne:thr_1",
		NeedsMe:     3,
		At:          1800000000000,
	}
	code, err := SendWebhook(t.Context(), server.Client(), hook, payload)
	if err != nil || code != http.StatusOK {
		t.Fatalf("SendWebhook: code=%d err=%v", code, err)
	}
	if gotEvent != "attention.opened" || gotCustom != "header-secret" {
		t.Fatalf("unexpected headers: event=%q custom=%q", gotEvent, gotCustom)
	}
	wantSig := SignWebhookPayload("top-secret-key", gotBody)
	if gotSig != wantSig || !strings.HasPrefix(gotSig, "sha256=") {
		t.Fatalf("signature = %q, want %q", gotSig, wantSig)
	}
	var decoded sdk.NotificationPayload
	if err := json.Unmarshal(gotBody, &decoded); err != nil || decoded.NeedsMe != 3 || decoded.Title != payload.Title {
		t.Fatalf("decoded payload = %+v, err=%v", decoded, err)
	}

	// Ensure errors redact secret values echoed back by remote server.
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad token header-secret and top-secret-key"))
	}))
	defer failServer.Close()
	hook.URL = failServer.URL
	_, err = SendWebhook(t.Context(), failServer.Client(), hook, payload)
	if err == nil || strings.Contains(err.Error(), "header-secret") || strings.Contains(err.Error(), "top-secret-key") {
		t.Fatalf("expected redacted error, got: %v", err)
	}
}

func TestPresenceTrackerSuppressionGraceAndTTL(t *testing.T) {
	current, now := testClock(time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC))
	tracker := NewPresenceTracker(now)

	if tracker.IsActive() {
		t.Fatal("fresh tracker should not be active")
	}
	tracker.Report("tab_1", true, "prj_1", "thr_1")
	if !tracker.IsActive() {
		t.Fatal("focused client should mark presence active")
	}

	// Blur enters leave grace window.
	*current = current.Add(5 * time.Second)
	tracker.Report("tab_1", false, "prj_1", "thr_1")
	if !tracker.IsActive() {
		t.Fatal("client within leave grace should still suppress")
	}

	// After leave grace expires, offline pushes are allowed.
	*current = current.Add(DefaultLeaveGrace + time.Second)
	if tracker.IsActive() {
		t.Fatal("client past leave grace should no longer suppress")
	}

	// Focused client that disappears without blur expires after heartbeat TTL.
	tracker.Report("tab_2", true, "prj_1", "")
	if !tracker.IsActive() {
		t.Fatal("tab_2 should be active")
	}
	*current = current.Add(DefaultHeartbeatTTL + time.Second)
	if tracker.IsActive() {
		t.Fatal("stale client past heartbeat TTL should expire")
	}
}

func TestServiceEventRulesNoReplayAutoCleanupAndPresence(t *testing.T) {
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Seed project, thread, and a historical attention BEFORE Service.Start.
	_, err = repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_1", Data: json.RawMessage(`{"name":"Ariadne","coordinatorId":"crd_1"}`)},
		{Type: "thread.created", ProjectID: "prj_1", Data: json.RawMessage(`{"threadId":"thr_1","title":"通知底座"}`)},
		{Type: "attention.opened", ProjectID: "prj_1", Data: json.RawMessage(`{"attentionId":"att_old","threadId":"thr_1","block":{"type":"confirm","title":"历史问题"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu             sync.Mutex
		webhookEvents  []sdk.NotificationPayload
		pushPayloads   []sdk.NotificationPayload
		notifierEvents []sdk.NotificationPayload
	)
	var pushGone atomic.Bool

	uaPriv, p256dh, authBytes, authEncoded := makeTestSubscriptionKeys(t)
	pushSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pushGone.Load() {
			w.WriteHeader(http.StatusGone)
			return
		}
		body, _ := io.ReadAll(r.Body)
		plain, err := decryptWebPushPayload(body, uaPriv, authBytes)
		if err != nil {
			t.Errorf("decrypt push body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload sdk.NotificationPayload
		if err := json.Unmarshal(plain, &payload); err != nil {
			t.Errorf("unmarshal push payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		pushPayloads = append(pushPayloads, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushSrv.Close()

	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload sdk.NotificationPayload
		if err := json.Unmarshal(body, &payload); err == nil {
			mu.Lock()
			webhookEvents = append(webhookEvents, payload)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hookSrv.Close()

	registry := NewRegistry()
	if _, err := registry.Register(sdk.NotifierProvider{
		Kind:  "mock-im",
		Title: sdk.LocalizedText{"zh-CN": "模拟机器人"},
		Settings: sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"token": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "令牌"}, Secret: true},
			},
			Required: []string{"token"},
		},
		Send: func(_ context.Context, values map[string]any, payload sdk.NotificationPayload) error {
			if values["token"] != "bot-secret" {
				return errors.New("missing token")
			}
			mu.Lock()
			notifierEvents = append(notifierEvents, payload)
			mu.Unlock()
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateNotifications(settings.Object{
		"enabled":        true,
		"webPushEnabled": true,
		"webhooks": []any{
			settings.Object{
				"id":      "wh_test",
				"name":    "Hook",
				"url":     hookSrv.URL,
				"enabled": true,
				"secret":  "wh-secret",
			},
		},
		"notifiers": map[string]any{
			"mock-im": map[string]any{
				"enabled": true,
				"values":  map[string]any{"token": "bot-secret"},
			},
		},
	}, registry.Kinds(), registry.Validate); err != nil {
		t.Fatal(err)
	}

	current, now := testClock(time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC))
	service, err := NewService(Options{
		Repository:            repo,
		Settings:              store,
		Notifiers:             registry,
		HTTPClient:            hookSrv.Client(),
		Now:                   now,
		AllowInsecureEndpoint: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.Subscriptions().Upsert(t.Context(), SubscribeInput{
		Endpoint:  pushSrv.URL + "/sub/1",
		P256DH:    p256dh,
		Auth:      authEncoded,
		Label:     "Test Browser",
		UserAgent: "TestUA",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = service.Stop(stopCtx)
	}()

	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", desc)
	}

	// Historical attention must NOT be replayed.
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	if len(webhookEvents) != 0 || len(pushPayloads) != 0 || len(notifierEvents) != 0 {
		mu.Unlock()
		t.Fatalf("historical events should not be replayed: hooks=%d push=%d notifiers=%d", len(webhookEvents), len(pushPayloads), len(notifierEvents))
	}
	mu.Unlock()

	// 1. Live attention.opened fans out to all 3 channels.
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "attention.opened", ProjectID: "prj_1", Data: json.RawMessage(`{"attentionId":"att_live","threadId":"thr_1","block":{"type":"choice","question":"选择部署方案"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor("all 3 channels receive attention.opened", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(webhookEvents) == 1 && len(pushPayloads) == 1 && len(notifierEvents) == 1
	})
	mu.Lock()
	got := pushPayloads[0]
	mu.Unlock()
	if got.Event != "attention.opened" || got.Body != "选择部署方案" || got.Tag != "ariadne:thr_1" || got.URL != "/projects/prj_1?pane=side&tab=thread%3Athr_1" {
		t.Fatalf("unexpected push payload: %+v", got)
	}

	// 2. thread.aside_due triggers when thread is actually due.
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "thread.set_aside", ProjectID: "prj_1", Data: json.RawMessage(`{"threadId":"thr_1","until":1800000000000,"reason":"等发版窗口","by":"user"}`)},
		{Type: "thread.aside_due", ProjectID: "prj_1", Data: json.RawMessage(`{"threadId":"thr_1"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor("thread.aside_due delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(webhookEvents) == 2 && len(pushPayloads) == 2 && len(notifierEvents) == 2
	})
	mu.Lock()
	if webhookEvents[1].Event != "thread.aside_due" || webhookEvents[1].Body != "等发版窗口" {
		mu.Unlock()
		t.Fatalf("unexpected aside_due payload: %+v", webhookEvents[1])
	}
	mu.Unlock()

	// 3. Foreground presence suppresses live pushes, while TestNotification bypasses suppression.
	service.Presence().Report("browser_1", true, "prj_1", "thr_1")
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "thread.reported", ProjectID: "prj_1", Data: json.RawMessage(`{"threadId":"thr_1","report":"ready","headline":"完成","summary":"已检查","artifacts":[]}`)},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(webhookEvents) != 2 || len(pushPayloads) != 2 {
		mu.Unlock()
		t.Fatalf("foreground presence should suppress live notifications: hooks=%d push=%d", len(webhookEvents), len(pushPayloads))
	}
	mu.Unlock()

	testRes, err := service.Test(t.Context(), TestRequest{Channel: "webpush", TargetID: sub.ID})
	if err != nil || !testRes.OK || testRes.Delivered != 1 {
		t.Fatalf("Test webpush while active: res=%+v err=%v", testRes, err)
	}

	// Expire presence and verify 410 Gone cleans up subscription automatically.
	*current = current.Add(DefaultHeartbeatTTL + time.Second)
	pushGone.Store(true)
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "turn.finished", ProjectID: "prj_1", Data: json.RawMessage(`{"conversationId":"thr_1","turnId":"trn_1","outcome":"failed","error":{"message":"boom"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor("410 Gone auto cleanup removes subscription", func() bool {
		subs, err := service.Subscriptions().List(t.Context())
		return err == nil && len(subs) == 0
	})
}

func TestEvaluateEventSecondaryStateVerification(t *testing.T) {
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	// Create project + thread + attention, then immediately close the attention.
	events, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_v", Data: json.RawMessage(`{"name":"Verify","coordinatorId":"crd_v"}`)},
		{Type: "thread.created", ProjectID: "prj_v", Data: json.RawMessage(`{"threadId":"thr_v","title":"验证"}`)},
		{Type: "attention.opened", ProjectID: "prj_v", Data: json.RawMessage(`{"attentionId":"att_closed","threadId":"thr_v","block":{"type":"confirm","title":"确认"}}`)},
		{Type: "attention.answered", ProjectID: "prj_v", Data: json.RawMessage(`{"attentionId":"att_closed","answer":true}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := evaluateEvent(t.Context(), repo, events[2], "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		t.Fatalf("closed attention should be skipped by secondary state check, got %+v", payload)
	}

	// Body over 200 runes is truncated cleanly.
	longQuestion := strings.Repeat("测", 250)
	openEvs, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "attention.opened", ProjectID: "prj_v", Data: json.RawMessage(`{"attentionId":"att_open","threadId":"thr_v","block":{"type":"confirm","title":"` + longQuestion + `"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err = evaluateEvent(t.Context(), repo, openEvs[0], "zh-CN")
	if err != nil || payload == nil {
		t.Fatalf("open attention should produce payload, err=%v", err)
	}
	if len([]rune(payload.Body)) != maxBodyRunes {
		t.Fatalf("body runes = %d, want %d", len([]rune(payload.Body)), maxBodyRunes)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestVAPIDSubjectDefaultAndLegacyLocalhostFallback(t *testing.T) {
	t.Parallel()

	vapid, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	if vapid.Subject != settings.DefaultVAPIDSubject || !strings.HasPrefix(vapid.Subject, "https://") {
		t.Fatalf("unexpected default VAPID subject: %q", vapid.Subject)
	}

	cases := []struct {
		in   string
		want string
	}{
		{"", settings.DefaultVAPIDSubject},
		{"mailto:ariadne@localhost", settings.DefaultVAPIDSubject},
		{"mailto:ops@sub.localhost", settings.DefaultVAPIDSubject},
		{"https://localhost/contact", settings.DefaultVAPIDSubject},
		{"http://example.com/contact", settings.DefaultVAPIDSubject},
		{"mailto:ops@example.com", "mailto:ops@example.com"},
		{"https://ariadne.example.com/push", "https://ariadne.example.com/push"},
	}
	for _, tc := range cases {
		if got := settings.NormalizeVAPIDSubject(tc.in); got != tc.want {
			t.Fatalf("NormalizeVAPIDSubject(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Runtime JWT generation must normalize legacy @localhost subject.
	legacy := vapid
	legacy.Subject = "mailto:ariadne@localhost"
	authHeader, err := buildVAPIDAuthHeader("https://web.push.apple.com/Qdemo", legacy, time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	verifyVAPIDHeader(t, authHeader, "https://web.push.apple.com", vapid.PublicKey)
	token := strings.TrimPrefix(strings.Split(strings.TrimPrefix(authHeader, "vapid "), ",")[0], "t=")
	claimsRaw, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Sub != settings.DefaultVAPIDSubject {
		t.Fatalf("JWT sub = %q, want %q", claims.Sub, settings.DefaultVAPIDSubject)
	}

	// EnsureVAPID must repair persisted legacy @localhost subject without rotating keys.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	legacyJSON := `{"notifications":{"vapid":{"publicKey":"` + vapid.PublicKey + `","privateKey":"` + vapid.PrivateKey + `","subject":"mailto:ariadne@localhost"}}}`
	if err := os.WriteFile(cfgPath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := store.EnsureVAPID(func() (settings.VAPIDConfig, error) {
		t.Fatal("EnsureVAPID should not regenerate keys when repairing subject")
		return settings.VAPIDConfig{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.PublicKey != vapid.PublicKey || repaired.PrivateKey != vapid.PrivateKey {
		t.Fatal("EnsureVAPID unexpectedly rotated existing VAPID keys")
	}
	if repaired.Subject != settings.DefaultVAPIDSubject {
		t.Fatalf("repaired subject = %q, want %q", repaired.Subject, settings.DefaultVAPIDSubject)
	}
	if store.Notifications().VAPID.Subject != settings.DefaultVAPIDSubject {
		t.Fatalf("persisted subject = %q, want %q", store.Notifications().VAPID.Subject, settings.DefaultVAPIDSubject)
	}
}

func TestPushSubscriptionsUTF8SafeTruncationAndProtoMarshal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	_, p256dh, _, auth := makeTestSubscriptionKeys(t)
	subs := NewSubscriptionStore(repo.Store.DB(), nil)

	// Multi-byte UTF-8 strings longer than byte limits + embedded invalid UTF-8 bytes.
	longLabel := strings.Repeat("中文设备", 40) + string([]byte{0xff, 0xfe})
	longUA := strings.Repeat("浏览器代理", 150) + string([]byte{0xc3, 0x28})
	sub, err := subs.Upsert(t.Context(), SubscribeInput{
		Endpoint:  "https://push.example.com/sub/utf8",
		P256DH:    p256dh,
		Auth:      auth,
		Label:     longLabel,
		UserAgent: longUA,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !utf8.ValidString(sub.Label) || utf8.RuneCountInString(sub.Label) != 120 {
		t.Fatalf("label invalid UTF-8 or wrong rune count: valid=%v runes=%d", utf8.ValidString(sub.Label), utf8.RuneCountInString(sub.Label))
	}
	if !utf8.ValidString(sub.UserAgent) || utf8.RuneCountInString(sub.UserAgent) != 512 {
		t.Fatalf("userAgent invalid UTF-8 or wrong rune count: valid=%v runes=%d", utf8.ValidString(sub.UserAgent), utf8.RuneCountInString(sub.UserAgent))
	}

	longErr := strings.Repeat("推送失败错误", 120) + string([]byte{0xff, 0xfe})
	if err := subs.MarkError(t.Context(), sub.ID, longErr); err != nil {
		t.Fatalf("MarkError: %v", err)
	}
	listed, err := subs.List(t.Context())
	if err != nil || len(listed) != 1 || listed[0].LastError == nil {
		t.Fatalf("List: %+v err=%v", listed, err)
	}
	gotErr := *listed[0].LastError
	if !utf8.ValidString(gotErr) || utf8.RuneCountInString(gotErr) != 500 {
		t.Fatalf("lastError invalid UTF-8 or wrong rune count: valid=%v runes=%d", utf8.ValidString(gotErr), utf8.RuneCountInString(gotErr))
	}

	// Ensure proto3 serialization succeeds cleanly.
	msg := &ariadnev1.PushSubscriptionView{
		Id:        listed[0].ID,
		Endpoint:  listed[0].Endpoint,
		Label:     listed[0].Label,
		UserAgent: listed[0].UserAgent,
		CreatedAt: listed[0].CreatedAt,
		LastError: listed[0].LastError,
	}
	if _, err := proto.Marshal(msg); err != nil {
		t.Fatalf("proto.Marshal(PushSubscriptionView) failed: %v", err)
	}
}

func TestNotificationErrorAndCheckRedaction(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	notifierSecret := "super-secret-notifier-token-999"
	registry := NewRegistry()
	if _, err := registry.Register(sdk.NotifierProvider{
		Kind:  "secret-bot",
		Title: sdk.LocalizedText{"zh-CN": "密钥机器人"},
		Settings: sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"token": {Type: "string", Secret: true},
			},
			Required: []string{"token"},
		},
		Check: func(_ context.Context, values map[string]any) (sdk.NotifierCheckResult, error) {
			tok, _ := values["token"].(string)
			return sdk.NotifierCheckResult{
				OK: false,
				Message: sdk.LocalizedText{
					"zh-CN": "校验失败：" + tok,
					"en":    "check failed: " + tok,
				},
			}, nil
		},
		Send: func(_ context.Context, values map[string]any, _ sdk.NotificationPayload) error {
			tok, _ := values["token"].(string)
			return errors.New("upstream rejected token " + tok)
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, p256dh, _, auth := makeTestSubscriptionKeys(t)
	var vapidPriv string
	var reported []string
	var mu sync.Mutex

	failingClient := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed with vapid=" + vapidPriv + " auth=" + auth)
		}),
	}

	service, err := NewService(Options{
		Repository:            repo,
		Settings:              store,
		Notifiers:             registry,
		HTTPClient:            failingClient,
		AllowInsecureEndpoint: true,
		OnError: func(err error) {
			mu.Lock()
			reported = append(reported, err.Error())
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	vapid, err := service.EnsureVAPID()
	if err != nil {
		t.Fatal(err)
	}
	vapidPriv = vapid.PrivateKey

	sub, err := service.Subscriptions().Upsert(t.Context(), SubscribeInput{
		Endpoint: "https://push.example.com/sub/redact",
		P256DH:   p256dh,
		Auth:     auth,
		Label:    "Test Device",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateNotifications(settings.Object{
		"enabled":        true,
		"webPushEnabled": true,
		"webhooks":       []any{},
		"notifiers": settings.Object{
			"secret-bot": settings.Object{
				"enabled": true,
				"values":  settings.Object{"token": notifierSecret},
			},
		},
	}, registry.Kinds(), registry.Validate); err != nil {
		t.Fatal(err)
	}

	// 1. Notifier Check returning NotifierCheckResult{OK: false, Message: ...} with err == nil must be redacted.
	checkRes, err := service.Test(t.Context(), TestRequest{Channel: "notifier", TargetID: "secret-bot"})
	if err != nil {
		t.Fatalf("Test notifier: %v", err)
	}
	if checkRes.OK {
		t.Fatal("expected checkRes.OK == false")
	}
	for lang, msg := range checkRes.Message {
		if strings.Contains(msg, notifierSecret) || !strings.Contains(msg, "[secret]") {
			t.Fatalf("notifier Check message[%s] leaked secret: %q", lang, msg)
		}
	}

	// 2. Test WebPush failure must redact secrets in both returned message and persisted last_error.
	pushTestRes, err := service.Test(t.Context(), TestRequest{Channel: "webpush", TargetID: sub.ID})
	if err != nil {
		t.Fatalf("Test webpush: %v", err)
	}
	for lang, msg := range pushTestRes.Message {
		if strings.Contains(msg, vapidPriv) || strings.Contains(msg, auth) {
			t.Fatalf("webpush Test message[%s] leaked secret: %q", lang, msg)
		}
	}
	stored, ok, err := service.Subscriptions().GetByID(t.Context(), sub.ID)
	if err != nil || !ok || stored.LastError == nil {
		t.Fatalf("GetByID after Test: ok=%v err=%v stored=%+v", ok, err, stored)
	}
	if strings.Contains(*stored.LastError, vapidPriv) || strings.Contains(*stored.LastError, auth) || !strings.Contains(*stored.LastError, "[secret]") {
		t.Fatalf("last_error after Test leaked secret: %q", *stored.LastError)
	}

	// 3. Dispatch must redact secrets in both OnError (notifier + webpush) and push_subscriptions.last_error.
	service.Dispatch(t.Context(), sdk.NotificationPayload{Event: "turn.finished", Title: "Test"})
	stored, ok, err = service.Subscriptions().GetByID(t.Context(), sub.ID)
	if err != nil || !ok || stored.LastError == nil {
		t.Fatalf("GetByID after Dispatch: ok=%v err=%v", ok, err)
	}
	if strings.Contains(*stored.LastError, vapidPriv) || strings.Contains(*stored.LastError, auth) {
		t.Fatalf("last_error after Dispatch leaked secret: %q", *stored.LastError)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) < 2 {
		t.Fatalf("expected errors reported for webpush and notifier, got %v", reported)
	}
	for _, msg := range reported {
		for _, secretVal := range []string{notifierSecret, vapidPriv, auth} {
			if strings.Contains(msg, secretVal) {
				t.Fatalf("OnError leaked secret %q in %q", secretVal, msg)
			}
		}
		if !strings.Contains(msg, "[secret]") {
			t.Fatalf("expected [secret] marker in reported error %q", msg)
		}
	}
}

func TestOutboundNotificationsDoNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var targetHits atomic.Int32
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/steal", http.StatusFound)
	}))
	defer redirectSrv.Close()

	callerClient := &http.Client{Timeout: 5 * time.Second}
	status, err := SendWebhook(t.Context(), callerClient, settings.WebhookConfig{
		ID:      "wh_redir",
		URL:     redirectSrv.URL + "/hook",
		Enabled: true,
		Secret:  "top-secret-hmac",
		Headers: map[string]string{"X-API-Key": "secret-api-key"},
	}, sdk.NotificationPayload{Event: "notification.test", Title: "Test"})
	if err == nil || status != http.StatusFound {
		t.Fatalf("SendWebhook redirect: status=%d err=%v, want 302 with error", status, err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("webhook redirect target was hit %d times, want 0", targetHits.Load())
	}
	if callerClient.CheckRedirect != nil {
		t.Fatal("SendWebhook must not mutate caller-provided http.Client")
	}

	vapid, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, p256dh, _, auth := makeTestSubscriptionKeys(t)
	status, err = SendWebPush(t.Context(), callerClient, PushSubscription{
		Endpoint: redirectSrv.URL + "/push",
		P256DH:   p256dh,
		Auth:     auth,
	}, vapid, []byte(`{"title":"test"}`), time.Unix(1800000000, 0))
	if err == nil || status != http.StatusFound {
		t.Fatalf("SendWebPush redirect: status=%d err=%v, want 302 with error", status, err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("webpush redirect target was hit %d times, want 0", targetHits.Load())
	}
	if callerClient.CheckRedirect != nil {
		t.Fatal("SendWebPush must not mutate caller-provided http.Client")
	}
}
