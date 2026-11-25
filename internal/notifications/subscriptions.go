package notifications

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/google/uuid"
)

const maxSubscriptions = 64

type PushSubscription struct {
	ID         string
	Endpoint   string
	P256DH     string
	Auth       string
	Label      string
	UserAgent  string
	CreatedAt  int64
	LastUsedAt *int64
	LastError  *string
}

type SubscribeInput struct {
	Endpoint  string
	P256DH    string
	Auth      string
	Label     string
	UserAgent string
}

type SubscriptionStore struct {
	db                    *sql.DB
	now                   func() time.Time
	allowInsecureEndpoint bool
}

func NewSubscriptionStore(db *sql.DB, now func() time.Time) *SubscriptionStore {
	if now == nil {
		now = time.Now
	}
	return &SubscriptionStore{db: db, now: now}
}

func decodeBase64URL(raw string) ([]byte, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "=")
	if trimmed == "" {
		return nil, errors.New("empty key")
	}
	return base64.RawURLEncoding.DecodeString(trimmed)
}

func sanitizeUTF8Truncate(raw string, maxRunes int) string {
	cleaned := textutil.Trim(strings.ReplaceAll(strings.ToValidUTF8(raw, ""), "\x00", ""))
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) <= maxRunes {
		return cleaned
	}
	return string(runes[:maxRunes])
}

func sanitizePushSubscription(item PushSubscription) PushSubscription {
	item.Endpoint = strings.ToValidUTF8(item.Endpoint, "")
	item.Label = strings.ToValidUTF8(item.Label, "")
	item.UserAgent = strings.ToValidUTF8(item.UserAgent, "")
	if item.LastError != nil {
		cleaned := strings.ToValidUTF8(*item.LastError, "")
		item.LastError = &cleaned
	}
	return item
}

func validatePushSubscriptionInput(input SubscribeInput, allowInsecure bool) (SubscribeInput, error) {
	endpoint := textutil.Trim(input.Endpoint)
	parsed, err := url.Parse(endpoint)
	validScheme := parsed != nil && (parsed.Scheme == "https" || (allowInsecure && parsed.Scheme == "http"))
	if err != nil || !utf8.ValidString(endpoint) || len(endpoint) > 2048 || strings.ContainsRune(endpoint, 0) ||
		!validScheme || parsed.Host == "" || parsed.User != nil {
		return SubscribeInput{}, domainerr.New("error.invalidInput", map[string]any{"field": "endpoint"})
	}
	p256dhRaw := strings.TrimRight(textutil.Trim(input.P256DH), "=")
	pubBytes, err := decodeBase64URL(p256dhRaw)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return SubscribeInput{}, domainerr.New("error.invalidInput", map[string]any{"field": "p256dh"})
	}
	authRaw := strings.TrimRight(textutil.Trim(input.Auth), "=")
	authBytes, err := decodeBase64URL(authRaw)
	if err != nil || len(authBytes) != 16 {
		return SubscribeInput{}, domainerr.New("error.invalidInput", map[string]any{"field": "auth"})
	}
	label := sanitizeUTF8Truncate(input.Label, 120)
	ua := sanitizeUTF8Truncate(input.UserAgent, 512)
	return SubscribeInput{
		Endpoint:  endpoint,
		P256DH:    p256dhRaw,
		Auth:      authRaw,
		Label:     label,
		UserAgent: ua,
	}, nil
}

func (store *SubscriptionStore) List(ctx context.Context) ([]PushSubscription, error) {
	if store == nil || store.db == nil {
		return []PushSubscription{}, nil
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, endpoint, p256dh, auth, label, user_agent, created_at, last_used_at, last_error
		FROM push_subscriptions ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSubscription{}
	for rows.Next() {
		var item PushSubscription
		var lastUsed sql.NullInt64
		var lastErr sql.NullString
		if err := rows.Scan(&item.ID, &item.Endpoint, &item.P256DH, &item.Auth, &item.Label, &item.UserAgent, &item.CreatedAt, &lastUsed, &lastErr); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			v := lastUsed.Int64
			item.LastUsedAt = &v
		}
		if lastErr.Valid && lastErr.String != "" {
			v := lastErr.String
			item.LastError = &v
		}
		out = append(out, sanitizePushSubscription(item))
	}
	return out, rows.Err()
}

func (store *SubscriptionStore) GetByID(ctx context.Context, id string) (PushSubscription, bool, error) {
	if store == nil || store.db == nil {
		return PushSubscription{}, false, nil
	}
	var item PushSubscription
	var lastUsed sql.NullInt64
	var lastErr sql.NullString
	err := store.db.QueryRowContext(ctx, `SELECT id, endpoint, p256dh, auth, label, user_agent, created_at, last_used_at, last_error
		FROM push_subscriptions WHERE id = ?`, id).Scan(
		&item.ID, &item.Endpoint, &item.P256DH, &item.Auth, &item.Label, &item.UserAgent, &item.CreatedAt, &lastUsed, &lastErr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, false, nil
	}
	if err != nil {
		return PushSubscription{}, false, err
	}
	if lastUsed.Valid {
		v := lastUsed.Int64
		item.LastUsedAt = &v
	}
	if lastErr.Valid && lastErr.String != "" {
		v := lastErr.String
		item.LastError = &v
	}
	return sanitizePushSubscription(item), true, nil
}

func (store *SubscriptionStore) Upsert(ctx context.Context, raw SubscribeInput) (PushSubscription, error) {
	if store == nil || store.db == nil {
		return PushSubscription{}, errors.New("push subscription store is unavailable")
	}
	input, err := validatePushSubscriptionInput(raw, store.allowInsecureEndpoint)
	if err != nil {
		return PushSubscription{}, err
	}
	nowMs := store.now().UnixMilli()
	var existingID string
	var createdAt int64
	err = store.db.QueryRowContext(ctx, `SELECT id, created_at FROM push_subscriptions WHERE endpoint = ?`, input.Endpoint).Scan(&existingID, &createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, err
	}
	if existingID != "" {
		_, err = store.db.ExecContext(ctx, `UPDATE push_subscriptions
			SET p256dh = ?, auth = ?, label = ?, user_agent = ?, last_error = NULL
			WHERE id = ?`, input.P256DH, input.Auth, input.Label, input.UserAgent, existingID)
		if err != nil {
			return PushSubscription{}, err
		}
		item, _, err := store.GetByID(ctx, existingID)
		return item, err
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions`).Scan(&count); err != nil {
		return PushSubscription{}, err
	}
	if count >= maxSubscriptions {
		return PushSubscription{}, domainerr.New("error.invalidInput", map[string]any{"field": "subscriptions"})
	}
	id := "psb_" + uuid.NewString()
	_, err = store.db.ExecContext(ctx, `INSERT INTO push_subscriptions
		(id, endpoint, p256dh, auth, label, user_agent, created_at, last_used_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		id, input.Endpoint, input.P256DH, input.Auth, input.Label, input.UserAgent, nowMs)
	if err != nil {
		return PushSubscription{}, err
	}
	return PushSubscription{
		ID:        id,
		Endpoint:  input.Endpoint,
		P256DH:    input.P256DH,
		Auth:      input.Auth,
		Label:     input.Label,
		UserAgent: input.UserAgent,
		CreatedAt: nowMs,
	}, nil
}

func (store *SubscriptionStore) DeleteByEndpoint(ctx context.Context, endpoint string) error {
	if store == nil || store.db == nil {
		return nil
	}
	endpoint = textutil.Trim(endpoint)
	if endpoint == "" {
		return domainerr.New("error.invalidInput", map[string]any{"field": "endpoint"})
	}
	_, err := store.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

func (store *SubscriptionStore) DeleteByID(ctx context.Context, id string) error {
	if store == nil || store.db == nil {
		return nil
	}
	id = textutil.Trim(id)
	if id == "" {
		return domainerr.New("error.invalidInput", map[string]any{"field": "id"})
	}
	_, err := store.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, id)
	return err
}

func (store *SubscriptionStore) MarkUsed(ctx context.Context, id string, atMs int64) error {
	if store == nil || store.db == nil {
		return nil
	}
	_, err := store.db.ExecContext(ctx, `UPDATE push_subscriptions SET last_used_at = ?, last_error = NULL WHERE id = ?`, atMs, id)
	return err
}

func (store *SubscriptionStore) MarkError(ctx context.Context, id string, message string) error {
	if store == nil || store.db == nil {
		return nil
	}
	message = sanitizeUTF8Truncate(message, 500)
	_, err := store.db.ExecContext(ctx, `UPDATE push_subscriptions SET last_error = ? WHERE id = ?`, message, id)
	return err
}
