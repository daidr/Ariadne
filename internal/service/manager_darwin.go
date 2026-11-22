//go:build darwin

package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const launchdLabel = "me.daidr.ariadne"

type darwinManager struct {
	home string
}

func NewManager(home string) Manager {
	return &darwinManager{home: home}
}

func (m *darwinManager) ManagedBy() string       { return ManagedLaunchd }
func (m *darwinManager) SupportsAutostart() bool { return true }

func (m *darwinManager) plistPath() (string, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func (m *darwinManager) domainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func (m *darwinManager) serviceTarget() string {
	return fmt.Sprintf("%s/%s", m.domainTarget(), launchdLabel)
}

func (m *darwinManager) Info(_ context.Context) (ServiceInfo, error) {
	path, err := m.plistPath()
	if err != nil {
		return ServiceInfo{}, err
	}
	info := ServiceInfo{
		ManagedBy:         ManagedLaunchd,
		SupportsAutostart: true,
		ConfigPath:        path,
		LogPath:           LogPath(m.home),
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	info.Installed = true
	text := string(data)
	info.Autostart = strings.Contains(text, "<key>RunAtLoad</key>\n\t<true/>")
	return info, nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func renderLaunchdPlist(opts InstallOptions) string {
	logPath := opts.LogPath
	if logPath == "" {
		logPath = LogPath(opts.Home)
	}
	args := append([]string{opts.Executable}, BuildServeArgs(opts, ManagedLaunchd)...)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(launchdLabel) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range args {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if opts.Autostart {
		b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	} else {
		b.WriteString("\t<key>RunAtLoad</key>\n\t<false/>\n")
	}
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>1</integer>\n")
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		b.WriteString("\t\t<key>PATH</key>\n\t\t<string>" + xmlEscape(pathEnv) + "</string>\n")
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlEscape(logPath) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlEscape(logPath) + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func (m *darwinManager) Install(_ context.Context, opts InstallOptions) (ServiceInfo, error) {
	path, err := m.plistPath()
	if err != nil {
		return ServiceInfo{}, err
	}
	if opts.Home == "" {
		opts.Home = m.home
	}
	if opts.LogPath == "" {
		opts.LogPath = LogPath(opts.Home)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ServiceInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o700); err != nil {
		return ServiceInfo{}, err
	}
	content := renderLaunchdPlist(opts)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return ServiceInfo{}, err
	}
	return m.Info(context.Background())
}

func (m *darwinManager) Uninstall(ctx context.Context) error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	_ = m.Stop(ctx)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *darwinManager) Start(ctx context.Context, opts InstallOptions) error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := m.Install(ctx, opts); err != nil {
			return err
		}
	}
	// Always bootout any cached in-memory launchd definition before bootstrapping
	// so plist updates written by `service install` take effect immediately.
	_ = m.Stop(ctx)

	var lastOut string
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		out, bootErr := exec.CommandContext(ctx, "launchctl", "bootstrap", m.domainTarget(), path).CombinedOutput()
		if bootErr == nil || strings.Contains(strings.ToLower(string(out)), "already") {
			lastErr = nil
			break
		}
		lastOut = strings.TrimSpace(string(out))
		lastErr = bootErr
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("launchctl bootstrap: %w (%s)", lastErr, lastOut)
	}
	if out, err := exec.CommandContext(ctx, "launchctl", "kickstart", "-k", m.serviceTarget()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *darwinManager) Stop(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "launchctl", "bootout", m.serviceTarget())
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "could not find service") || strings.Contains(msg, "no such process") || strings.Contains(msg, "not loaded") {
			return nil
		}
		// Fallback for older macOS launchctl exit code 3 (ESRCH).
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 3 || exitErr.ExitCode() == 113) {
			return nil
		}
		return fmt.Errorf("launchctl bootout: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var _ = strconv.Itoa
