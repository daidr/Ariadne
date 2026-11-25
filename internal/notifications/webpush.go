package notifications

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/internal/settings"
)

const (
	defaultVAPIDSubject = settings.DefaultVAPIDSubject
	webPushRecordSize   = 4096
	webPushTTLSeconds   = 86400
	maxPlaintextBytes   = 3800
)

// ErrSubscriptionGone signals that the push service rejected the subscription
// with HTTP 404 or 410 Gone and it should be removed.
var ErrSubscriptionGone = errors.New("push subscription is gone")

// GenerateVAPIDKeys creates a fresh P-256 VAPID keypair using only the Go standard library.
func GenerateVAPIDKeys() (settings.VAPIDConfig, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return settings.VAPIDConfig{}, err
	}
	return settings.VAPIDConfig{
		PublicKey:  base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		Subject:    defaultVAPIDSubject,
	}, nil
}

// encryptWebPushPayload implements RFC 8291 (Message Encryption for Web Push)
// using the RFC 8188 aes128gcm content coding.
func encryptWebPushPayload(plaintext, uaPublic, authSecret []byte) ([]byte, error) {
	if len(uaPublic) != 65 || uaPublic[0] != 0x04 {
		return nil, errors.New("invalid subscriber p256dh public key")
	}
	if len(authSecret) != 16 {
		return nil, errors.New("invalid subscriber auth secret")
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	return encryptWebPushPayloadDeterministic(plaintext, uaPublic, authSecret, asPriv, salt, webPushRecordSize)
}

func encryptWebPushPayloadDeterministic(
	plaintext, uaPublic, authSecret []byte,
	asPriv *ecdh.PrivateKey,
	salt []byte,
	recordSize uint32,
) ([]byte, error) {
	if len(plaintext) > maxPlaintextBytes {
		return nil, fmt.Errorf("web push payload too large (%d bytes)", len(plaintext))
	}
	if len(salt) != 16 {
		return nil, errors.New("salt must be 16 bytes")
	}
	uaPubKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("parse subscriber public key: %w", err)
	}
	sharedSecret, err := asPriv.ECDH(uaPubKey)
	if err != nil {
		return nil, fmt.Errorf("ecdh agreement: %w", err)
	}
	asPublic := asPriv.PublicKey().Bytes()

	// RFC 8291 Section 3.3:
	// PRK_key = HMAC-SHA-256(auth_secret, ecdh_secret)
	// key_info = "WebPush: info" || 0x00 || ua_public || as_public
	// IKM = HKDF-Expand(PRK_key, key_info, 32)
	keyInfo := make([]byte, 0, len("WebPush: info\x00")+len(uaPublic)+len(asPublic))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPublic...)
	keyInfo = append(keyInfo, asPublic...)

	ikm, err := hkdf.Key(sha256.New, sharedSecret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, fmt.Errorf("derive ikm: %w", err)
	}

	// RFC 8291 Section 3.4 / RFC 8188 Section 2.2-2.3:
	// PRK = HKDF-Extract(salt, IKM)
	// CEK = HKDF-Expand(PRK, "Content-Encoding: aes128gcm\x00", 16)
	// NONCE = HKDF-Expand(PRK, "Content-Encoding: nonce\x00", 12)
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("derive prk: %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("derive cek: %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("derive nonce: %w", err)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// RFC 8188 Section 2: final record ends with delimiter byte 0x02.
	padded := make([]byte, len(plaintext)+1)
	copy(padded, plaintext)
	padded[len(plaintext)] = 0x02

	ciphertext := aead.Seal(nil, nonce, padded, nil)

	// RFC 8188 Section 2.1 header: salt (16) || rs (4) || idlen (1) || keyid (65)
	headerLen := 16 + 4 + 1 + len(asPublic)
	out := make([]byte, headerLen+len(ciphertext))
	copy(out[0:16], salt)
	binary.BigEndian.PutUint32(out[16:20], recordSize)
	out[20] = byte(len(asPublic))
	copy(out[21:21+len(asPublic)], asPublic)
	copy(out[headerLen:], ciphertext)
	return out, nil
}

// decryptWebPushPayload reverses RFC 8291 aes128gcm encryption (used in tests).
func decryptWebPushPayload(body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(body) < 16+4+1 {
		return nil, errors.New("aes128gcm body too short")
	}
	salt := body[0:16]
	idLen := int(body[20])
	headerLen := 21 + idLen
	if len(body) <= headerLen {
		return nil, errors.New("aes128gcm body missing ciphertext")
	}
	asPublic := body[21:headerLen]
	ciphertext := body[headerLen:]

	asPubKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	sharedSecret, err := uaPriv.ECDH(asPubKey)
	if err != nil {
		return nil, err
	}
	uaPublic := uaPriv.PublicKey().Bytes()

	keyInfo := make([]byte, 0, len("WebPush: info\x00")+len(uaPublic)+len(asPublic))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPublic...)
	keyInfo = append(keyInfo, asPublic...)

	ikm, err := hkdf.Key(sha256.New, sharedSecret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	padded, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	// Strip trailing zero padding and final delimiter 0x02.
	end := len(padded)
	for end > 0 && padded[end-1] == 0x00 {
		end--
	}
	if end == 0 || padded[end-1] != 0x02 {
		return nil, errors.New("invalid aes128gcm record delimiter")
	}
	return padded[:end-1], nil
}

func vapidECDSAPrivateKey(vapid settings.VAPIDConfig) (*ecdsa.PrivateKey, []byte, error) {
	rawPriv, err := decodeBase64URL(vapid.PrivateKey)
	if err != nil || len(rawPriv) != 32 {
		return nil, nil, errors.New("invalid VAPID private key")
	}
	ecdhPriv, err := ecdh.P256().NewPrivateKey(rawPriv)
	if err != nil {
		return nil, nil, fmt.Errorf("parse VAPID private key: %w", err)
	}
	pubBytes := ecdhPriv.PublicKey().Bytes()
	if len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return nil, nil, errors.New("invalid derived VAPID public key")
	}
	privKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(pubBytes[1:33]),
			Y:     new(big.Int).SetBytes(pubBytes[33:65]),
		},
		D: new(big.Int).SetBytes(rawPriv),
	}
	return privKey, pubBytes, nil
}

// buildVAPIDAuthHeader constructs the RFC 8292 Authorization header ("vapid t=..., k=...").
func buildVAPIDAuthHeader(endpoint string, vapid settings.VAPIDConfig, now time.Time) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid push endpoint URL")
	}
	audience := parsed.Scheme + "://" + parsed.Host
	subject := settings.NormalizeVAPIDSubject(vapid.Subject)
	privKey, pubBytes, err := vapidECDSAPrivateKey(vapid)
	if err != nil {
		return "", err
	}

	headerJSON := []byte(`{"typ":"JWT","alg":"ES256"}`)
	claimsJSON, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{
		Aud: audience,
		Exp: now.Add(12 * time.Hour).Unix(),
		Sub: subject,
	})
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, privKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign VAPID JWT: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	pubEncoded := base64.RawURLEncoding.EncodeToString(pubBytes)
	return "vapid t=" + jwt + ", k=" + pubEncoded, nil
}

// SendWebPush encrypts payload with RFC 8291 aes128gcm, signs the request with RFC 8292 VAPID,
// and POSTs it to the subscription endpoint. Returns ErrSubscriptionGone on HTTP 404/410.
func SendWebPush(
	ctx context.Context,
	client *http.Client,
	sub PushSubscription,
	vapid settings.VAPIDConfig,
	payload []byte,
	now time.Time,
) (int, error) {
	httpClient := noRedirectClient(client, 15*time.Second)
	uaPublic, err := decodeBase64URL(sub.P256DH)
	if err != nil {
		return 0, fmt.Errorf("decode p256dh: %w", err)
	}
	authSecret, err := decodeBase64URL(sub.Auth)
	if err != nil {
		return 0, fmt.Errorf("decode auth: %w", err)
	}
	encrypted, err := encryptWebPushPayload(payload, uaPublic, authSecret)
	if err != nil {
		return 0, err
	}
	authHeader, err := buildVAPIDAuthHeader(sub.Endpoint, vapid, now)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(encrypted))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(webPushTTLSeconds))
	req.Header.Set("Urgency", "high")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return resp.StatusCode, fmt.Errorf("%w (HTTP %d)", ErrSubscriptionGone, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(secrets.Redact(string(bodyBytes), []string{vapid.PrivateKey, sub.Auth, authHeader}))
		if detail != "" {
			return resp.StatusCode, fmt.Errorf("push endpoint HTTP %d: %s", resp.StatusCode, detail)
		}
		return resp.StatusCode, fmt.Errorf("push endpoint HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}
