// Package service manages Ariadne's runtime metadata, safe drain/restart lifecycle,
// local control client, and cross-platform user-level background services.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// ExitCodeRestart signals supervising managers (launchd KeepAlive.SuccessfulExit=false
// and systemd Restart=on-failure) to immediately relaunch Ariadne using the binary on disk.
const ExitCodeRestart = 75

const (
	ManagedLaunchd     = "launchd"
	ManagedSystemd     = "systemd"
	ManagedWindowsRun  = "windows-run"
	ManagedWindowsTask = "windows-task"
	ManagedDetached    = "detached"
	ManagedDirect      = "direct"
)

// RuntimeInfo describes a running Ariadne backend instance for discovery by the CLI.
type RuntimeInfo struct {
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	StartedAt  int64  `json:"startedAt"`
	ManagedBy  string `json:"managedBy,omitempty"`
	Executable string `json:"executable,omitempty"`
}

func (info RuntimeInfo) Address() string {
	host := info.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(info.Port))
}

func loopbackHost(host string) string {
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return host
	}
}

func (info RuntimeInfo) BaseURL() string {
	return "http://" + net.JoinHostPort(loopbackHost(info.Host), strconv.Itoa(info.Port))
}

// DashboardURL constructs the authenticated browser URL for an Ariadne server.
func DashboardURL(host string, port int, token string) string {
	return "http://" + net.JoinHostPort(loopbackHost(host), strconv.Itoa(port)) + "/#" + url.Values{"token": {token}}.Encode()
}

func RuntimePath(home string) string {
	return filepath.Join(home, "runtime.json")
}

func LogPath(home string) string {
	return filepath.Join(home, "logs", "ariadne.log")
}

// WriteRuntime atomically writes runtime.json and returns a cleanup function that
// removes the file only if it still belongs to info.PID.
func WriteRuntime(home string, info RuntimeInfo) (func(), error) {
	if home == "" {
		return nil, errors.New("home directory is required")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	path := RuntimePath(home)
	tmp, err := os.CreateTemp(home, ".runtime-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	cleanup := func() {
		current, err := ReadRuntime(home)
		if err == nil && current != nil && current.PID == info.PID {
			_ = os.Remove(path)
		}
	}
	return cleanup, nil
}

// ReadRuntime reads runtime.json if present. Returns (nil, nil) if it does not exist.
func ReadRuntime(home string) (*RuntimeInfo, error) {
	data, err := os.ReadFile(RuntimePath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var info RuntimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse runtime.json: %w", err)
	}
	if info.PID <= 0 || info.Port <= 0 {
		return nil, nil
	}
	return &info, nil
}

// LiveRuntime returns the recorded RuntimeInfo if the process is currently alive.
// Stale runtime files whose process has exited are ignored and cleaned up lazily.
func LiveRuntime(home string) (*RuntimeInfo, error) {
	info, err := ReadRuntime(home)
	if err != nil || info == nil {
		return nil, err
	}
	if !ProcessAlive(info.PID) {
		return nil, nil
	}
	return info, nil
}
