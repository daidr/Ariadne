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
)

const systemdUnitName = "ariadne.service"

type linuxSystemdManager struct {
	home string
}

func NewManager(home string) Manager {
	if hasSystemdUser() {
		return &linuxSystemdManager{home: home}
	}
	return &detachedManager{home: home}
}

func hasSystemdUser() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	cmd := exec.Command("systemctl", "--user", "show-environment")
	return cmd.Run() == nil
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
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-enabled", systemdUnitName).Output(); err == nil {
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
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return ServiceInfo{}, fmt.Errorf("systemctl daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if opts.Autostart {
		if out, err := exec.CommandContext(ctx, "systemctl", "--user", "enable", systemdUnitName).CombinedOutput(); err != nil {
			return ServiceInfo{}, fmt.Errorf("systemctl enable: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	} else {
		_ = exec.CommandContext(ctx, "systemctl", "--user", "disable", systemdUnitName).Run()
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
	_ = exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", systemdUnitName).Run()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
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
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "start", systemdUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *linuxSystemdManager) Stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", systemdUnitName).CombinedOutput(); err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "not loaded") || strings.Contains(msg, "not found") {
			return nil
		}
		return fmt.Errorf("systemctl stop: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
