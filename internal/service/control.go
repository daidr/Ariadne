package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DrainModeSafePoint = "safe-point"
	DrainModeTurn      = "turn"
	DrainModeWhenIdle  = "when-idle"
	DrainModeNow       = "now"

	ActionStop    = "stop"
	ActionRestart = "restart"

	RestartPhaseNone        = ""
	RestartPhaseWaitingIdle = "waiting_idle"
	RestartPhaseDraining    = "draining"
	RestartPhaseRestarting  = "restarting"
)

type RestartState struct {
	CanRestart       bool
	Phase            string
	ActiveTurns      int
	ActiveBackground int
}

func (s RestartState) Restarting() bool {
	return s.Phase != RestartPhaseNone
}

type ActiveTurn struct {
	ConversationID string `json:"conversationId"`
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName,omitempty"`
	Role           string `json:"role"`
	Title          string `json:"title,omitempty"`
	Model          string `json:"model,omitempty"`
	Stateful       bool   `json:"stateful"`
	StartedAt      int64  `json:"startedAt"`
}

type ActiveBackground struct {
	ID        string `json:"id"`
	ThreadID  string `json:"threadId"`
	Command   string `json:"command"`
	StartedAt int64  `json:"startedAt"`
}

type StatusSnapshot struct {
	PID              int                `json:"pid"`
	Version          string             `json:"version"`
	Host             string             `json:"host"`
	Port             int                `json:"port"`
	StartedAt        int64              `json:"startedAt"`
	ManagedBy        string             `json:"managedBy"`
	Executable       string             `json:"executable,omitempty"`
	Draining         bool               `json:"draining"`
	DrainMode        string             `json:"drainMode,omitempty"`
	DrainAction      string             `json:"drainAction,omitempty"`
	RestartPhase     string             `json:"restartPhase,omitempty"`
	WaitBackground   bool               `json:"waitBackground,omitempty"`
	AutoShutdown     bool               `json:"autoShutdown,omitempty"`
	ActiveTurns      []ActiveTurn       `json:"activeTurns"`
	ActiveBackground []ActiveBackground `json:"activeBackground"`
}

func (s StatusSnapshot) ReadyToStop(waitBackground bool) bool {
	if len(s.ActiveTurns) > 0 {
		return false
	}
	if waitBackground && len(s.ActiveBackground) > 0 {
		return false
	}
	return true
}

type DrainRequest struct {
	Mode            string `json:"mode"`
	Action          string `json:"action"`
	WaitBackground  bool   `json:"waitBackground,omitempty"`
	AutoShutdown    bool   `json:"autoShutdown,omitempty"`
	QuietWindowMs   int64  `json:"quietWindowMs,omitempty"`
	LeaseMs         int64  `json:"leaseMs,omitempty"`
	ImmediateIfIdle bool   `json:"immediateIfIdle,omitempty"`
}

type ShutdownRequest struct {
	Restart bool `json:"restart,omitempty"`
	Force   bool `json:"force,omitempty"`
}

// Client speaks to a running local Ariadne backend over its authenticated control API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("service control %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (*StatusSnapshot, error) {
	var snap StatusSnapshot
	if err := c.doJSON(ctx, http.MethodGet, "/api/service/status", nil, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) BeginDrain(ctx context.Context, req DrainRequest) (*StatusSnapshot, error) {
	var snap StatusSnapshot
	if err := c.doJSON(ctx, http.MethodPost, "/api/service/drain", req, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) CancelDrain(ctx context.Context) (*StatusSnapshot, error) {
	var snap StatusSnapshot
	if err := c.doJSON(ctx, http.MethodPost, "/api/service/cancel-drain", nil, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) Shutdown(ctx context.Context, req ShutdownRequest) error {
	err := c.doJSON(ctx, http.MethodPost, "/api/service/shutdown", req, nil)
	if err != nil && errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// WaitReady polls Status until the backend responds and (if avoidPID > 0) has a different PID.
func (c *Client) WaitReady(ctx context.Context, avoidPID int) (*StatusSnapshot, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap, err := c.Status(ctx)
		if err == nil && snap != nil && snap.PID > 0 && (avoidPID == 0 || snap.PID != avoidPID) {
			return snap, nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return nil, fmt.Errorf("timed out waiting for Ariadne to become ready: %w", err)
			}
			return nil, errors.New("timed out waiting for Ariadne to become ready")
		case <-ticker.C:
		}
	}
}
