package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

const (
	SignatureHeader = "X-Ariadne-Signature-256"
	EventHeader     = "X-Ariadne-Event"
)

// SignWebhookPayload computes the "sha256=<hex>" HMAC-SHA256 signature header value.
func SignWebhookPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SendWebhook posts a JSON notification payload to a generic Webhook endpoint,
// signing the body with X-Ariadne-Signature-256 when a secret is configured.
func SendWebhook(
	ctx context.Context,
	client *http.Client,
	hook settings.WebhookConfig,
	payload sdk.NotificationPayload,
) (int, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Ariadne/notifications")
	for name, value := range hook.Headers {
		if strings.EqualFold(name, SignatureHeader) || strings.EqualFold(name, EventHeader) {
			continue
		}
		req.Header.Set(name, value)
	}
	req.Header.Set(EventHeader, payload.Event)
	if hook.Secret != "" {
		req.Header.Set(SignatureHeader, SignWebhookPayload(hook.Secret, body))
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		redactList := make([]string, 0, 1+len(hook.Headers))
		if hook.Secret != "" {
			redactList = append(redactList, hook.Secret)
		}
		for _, value := range hook.Headers {
			if value != "" {
				redactList = append(redactList, value)
			}
		}
		detail := strings.TrimSpace(secrets.Redact(string(respBody), redactList))
		if detail != "" {
			return resp.StatusCode, fmt.Errorf("webhook HTTP %d: %s", resp.StatusCode, detail)
		}
		return resp.StatusCode, fmt.Errorf("webhook HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}
