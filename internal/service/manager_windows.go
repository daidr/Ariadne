//go:build windows

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
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const windowsTaskName = "Ariadne"

type windowsManager struct {
	home string
}

func NewManager(home string) Manager {
	return &windowsManager{home: home}
}

func (m *windowsManager) ManagedBy() string       { return ManagedWindowsTask }
func (m *windowsManager) SupportsAutostart() bool { return true }

func runSchtasks(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd.CombinedOutput()
}

func (m *windowsManager) taskExists(ctx context.Context) bool {
	_, err := runSchtasks(ctx, "/Query", "/TN", windowsTaskName)
	return err == nil
}

func (m *windowsManager) Info(ctx context.Context) (ServiceInfo, error) {
	cfg, err := readServiceConfig(m.home)
	if err != nil {
		return ServiceInfo{}, err
	}
	taskRegistered := m.taskExists(ctx)
	info := ServiceInfo{
		Installed:         cfg != nil || taskRegistered,
		ManagedBy:         ManagedWindowsTask,
		Autostart:         taskRegistered,
		SupportsAutostart: true,
		ConfigPath:        serviceConfigPath(m.home),
		LogPath:           LogPath(m.home),
	}
	if cfg != nil && cfg.LogPath != "" {
		info.LogPath = cfg.LogPath
	}
	return info, nil
}

func xmlEscapeWindows(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func quoteWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\"") {
			quoted[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		} else {
			quoted[i] = arg
		}
	}
	return strings.Join(quoted, " ")
}

func renderWindowsTaskXML(opts InstallOptions) string {
	args := BuildServeArgs(opts, ManagedWindowsTask)
	workDir := filepath.Dir(opts.Executable)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Ariadne AI Collaboration Server</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, xmlEscapeWindows(opts.Executable), xmlEscapeWindows(quoteWindowsArgs(args)), xmlEscapeWindows(workDir))
}

func encodeUTF16LEWithBOM(s string) []byte {
	runes := []rune(s)
	out := make([]byte, 2+len(runes)*2)
	out[0], out[1] = 0xFF, 0xFE
	pos := 2
	for _, r := range runes {
		if r > 0xFFFF {
			r = '?'
		}
		out[pos] = byte(r)
		out[pos+1] = byte(r >> 8)
		pos += 2
	}
	return out
}

func (m *windowsManager) Install(ctx context.Context, opts InstallOptions) (ServiceInfo, error) {
	if opts.Home == "" {
		opts.Home = m.home
	}
	if opts.LogPath == "" {
		opts.LogPath = LogPath(opts.Home)
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o700); err != nil {
		return ServiceInfo{}, err
	}
	if err := writeServiceConfig(m.home, storedServiceConfig{
		Executable: opts.Executable,
		Home:       opts.Home,
		Host:       opts.Host,
		Port:       opts.Port,
		Autostart:  opts.Autostart,
		LogPath:    opts.LogPath,
	}); err != nil {
		return ServiceInfo{}, err
	}
	if opts.Autostart {
		tmp, err := os.CreateTemp(m.home, ".ariadne-task-*.xml")
		if err != nil {
			return ServiceInfo{}, err
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := tmp.Write(encodeUTF16LEWithBOM(renderWindowsTaskXML(opts))); err != nil {
			_ = tmp.Close()
			return ServiceInfo{}, err
		}
		if err := tmp.Close(); err != nil {
			return ServiceInfo{}, err
		}
		if out, err := runSchtasks(ctx, "/Create", "/F", "/TN", windowsTaskName, "/XML", tmpPath); err != nil {
			return ServiceInfo{}, fmt.Errorf("register Windows logon task: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	} else {
		_, _ = runSchtasks(ctx, "/Delete", "/F", "/TN", windowsTaskName)
	}
	return m.Info(ctx)
}

func (m *windowsManager) Uninstall(ctx context.Context) error {
	_, _ = runSchtasks(ctx, "/Delete", "/F", "/TN", windowsTaskName)
	if err := os.Remove(serviceConfigPath(m.home)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *windowsManager) Start(ctx context.Context, opts InstallOptions) error {
	cfg, _ := readServiceConfig(m.home)
	if cfg == nil {
		if _, err := m.Install(ctx, opts); err != nil {
			return err
		}
		cfg, _ = readServiceConfig(m.home)
	}
	if opts.Executable == "" && cfg != nil {
		opts.Executable = cfg.Executable
	}
	if opts.Home == "" {
		opts.Home = m.home
	}
	if opts.Port == 0 && cfg != nil {
		opts.Port = cfg.Port
	}
	if opts.Host == "" && cfg != nil {
		opts.Host = cfg.Host
	}
	if opts.LogPath == "" {
		if cfg != nil && cfg.LogPath != "" {
			opts.LogPath = cfg.LogPath
		} else {
			opts.LogPath = LogPath(m.home)
		}
	}
	args := BuildServeArgs(opts, ManagedWindowsTask)
	_, err := SpawnDetached(opts.Executable, args, os.Environ())
	return err
}

func (m *windowsManager) Stop(_ context.Context) error {
	rt, err := LiveRuntime(m.home)
	if err != nil || rt == nil {
		return err
	}
	return TerminateProcess(rt.PID)
}
