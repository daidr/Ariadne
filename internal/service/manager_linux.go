//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const systemdUnitName = "ariadne.service"

type linuxSystemdManager struct {
	home string
}

func NewManager(home string) Manager {
	if hasSystemdUser() {
		return &linuxSystemdManager{home: home}
	}
	return &detachedManager{
		home: home,
		note: "systemd user session unavailable; running in detached background mode without autostart",
	}
}

func systemdCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		runtimeDir := fmt.Sprintf("/run/user/%d", os.Getuid())
		if _, err := os.Stat(filepath.Join(runtimeDir, "bus")); err == nil {
			cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeDir)
		}
	}
	return cmd
}

func hasSystemdUser() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return systemdCommand(ctx, "show-environment").Run() == nil
}

func (m *linuxSystemdManager) ManagedBy() string       { return ManagedSystemd }
func (m *linuxSystemdManager) SupportsAutostart() bool { return true }

func (m *linuxSystemdManager) unitPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configHome = filepath.Join(userHome, ".config")
	}
	return filepath.Join(configHome, "systemd", "user", systemdUnitName), nil
}

func (m *linuxSystemdManager) Info(ctx context.Context) (ServiceInfo, error) {
	path, err := m.unitPath()
	if err != nil {
		return ServiceInfo{}, err
	}
	info := ServiceInfo{
		ManagedBy:         ManagedSystemd,
		SupportsAutostart: true,
		ConfigPath:        path,
		LogPath:           LogPath(m.home),
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return info, nil
	} else if err != nil {
		return info, err
	}
	info.Installed = true
	if out, err := systemdCommand(ctx, "is-enabled", systemdUnitName).Output(); err == nil {
		info.Autostart = strings.TrimSpace(string(out)) == "enabled"
	}
	return info, nil
}

func renderSystemdUnit(opts InstallOptions) string {
	args := append([]string{opts.Executable}, BuildServeArgs(opts, ManagedSystemd)...)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteSystemdArg(arg)
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Ariadne AI Collaboration Server\n")
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + strings.Join(quoted, " ") + "\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=1\n")
	b.WriteString("SuccessExitStatus=0\n")
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		b.WriteString("Environment=" + quoteSystemdArg("PATH="+pathEnv) + "\n")
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

func (m *linuxSystemdManager) Install(ctx context.Context, opts InstallOptions) (ServiceInfo, error) {
	path, err := m.unitPath()
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
	if err := os.WriteFile(path, []byte(renderSystemdUnit(opts)), 0o644); err != nil {
		return ServiceInfo{}, err
	}
	if out, err := systemdCommand(ctx, "daemon-reload").CombinedOutput(); err != nil {
		return ServiceInfo{}, fmt.Errorf("systemctl daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if opts.Autostart {
		if out, err := systemdCommand(ctx, "enable", systemdUnitName).CombinedOutput(); err != nil {
			return ServiceInfo{}, fmt.Errorf("systemctl enable: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	} else {
		_ = systemdCommand(ctx, "disable", systemdUnitName).Run()
	}
	info, err := m.Info(ctx)
	if err == nil && info.Autostart {
		info.Note = "For remote servers, run `loginctl enable-linger $USER` so Ariadne keeps running after SSH logout."
	}
	return info, err
}

func (m *linuxSystemdManager) Uninstall(ctx context.Context) error {
	path, err := m.unitPath()
	if err != nil {
		return err
	}
	_ = systemdCommand(ctx, "disable", "--now", systemdUnitName).Run()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = systemdCommand(ctx, "daemon-reload").Run()
	return nil
}

func (m *linuxSystemdManager) Start(ctx context.Context, opts InstallOptions) error {
	path, err := m.unitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := m.Install(ctx, opts); err != nil {
			return err
		}
	}
	if out, err := systemdCommand(ctx, "start", systemdUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *linuxSystemdManager) Stop(ctx context.Context) error {
	if out, err := systemdCommand(ctx, "stop", systemdUnitName).CombinedOutput(); err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "not loaded") || strings.Contains(msg, "not found") {
			return nil
		}
		return fmt.Errorf("systemctl stop: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
