package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type InstallOptions struct {
	Executable string
	Home       string
	Host       string
	Port       int
	Autostart  bool
	LogPath    string
}

type ServiceInfo struct {
	Installed         bool   `json:"installed"`
	ManagedBy         string `json:"managedBy"`
	Autostart         bool   `json:"autostart"`
	SupportsAutostart bool   `json:"supportsAutostart"`
	ConfigPath        string `json:"configPath,omitempty"`
	LogPath           string `json:"logPath,omitempty"`
	Note              string `json:"note,omitempty"`
}

// Manager abstracts user-level OS service registration and lifecycle.
type Manager interface {
	ManagedBy() string
	SupportsAutostart() bool
	Info(ctx context.Context) (ServiceInfo, error)
	Install(ctx context.Context, opts InstallOptions) (ServiceInfo, error)
	Uninstall(ctx context.Context) error
	Start(ctx context.Context, opts InstallOptions) error
	Stop(ctx context.Context) error
}

// BuildServeArgs builds the standard argument list for managed background Ariadne instances.
func BuildServeArgs(opts InstallOptions, managedBy string) []string {
	args := []string{"serve", "--no-open", "--managed-by", managedBy}
	if opts.Home != "" {
		args = append(args, "--home", opts.Home)
	}
	if opts.Host != "" && opts.Host != "127.0.0.1" {
		args = append(args, "--host", opts.Host)
	}
	if opts.Port > 0 && opts.Port != 7428 {
		args = append(args, "--port", strconv.Itoa(opts.Port))
	}
	logPath := opts.LogPath
	if logPath == "" && opts.Home != "" {
		logPath = LogPath(opts.Home)
	}
	if logPath != "" {
		args = append(args, "--log-file", logPath)
	}
	return args
}

// storedServiceConfig persists install options for detached / Windows managers.
type storedServiceConfig struct {
	Executable string `json:"executable"`
	Home       string `json:"home"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Autostart  bool   `json:"autostart"`
	LogPath    string `json:"logPath,omitempty"`
}

func serviceConfigPath(home string) string {
	return filepath.Join(home, "service.json")
}

func writeServiceConfig(home string, cfg storedServiceConfig) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(serviceConfigPath(home), data, 0o600)
}

func readServiceConfig(home string) (*storedServiceConfig, error) {
	data, err := os.ReadFile(serviceConfigPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg storedServiceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// detachedManager is a fallback for environments without an active OS user service
// manager (such as headless Linux containers without systemd --user or headless macOS
// SSH sessions without a GUI launchd domain).
type detachedManager struct {
	home string
	note string
}

func (m *detachedManager) ManagedBy() string       { return ManagedDetached }
func (m *detachedManager) SupportsAutostart() bool { return false }

func (m *detachedManager) Info(_ context.Context) (ServiceInfo, error) {
	cfg, err := readServiceConfig(m.home)
	if err != nil {
		return ServiceInfo{}, err
	}
	note := m.note
	if note == "" {
		note = "user service manager unavailable; running in detached background mode without autostart"
	}
	info := ServiceInfo{
		Installed:         cfg != nil,
		ManagedBy:         ManagedDetached,
		Autostart:         false,
		SupportsAutostart: false,
		ConfigPath:        serviceConfigPath(m.home),
		LogPath:           LogPath(m.home),
		Note:              note,
	}
	if cfg != nil && cfg.LogPath != "" {
		info.LogPath = cfg.LogPath
	}
	return info, nil
}

func (m *detachedManager) Install(_ context.Context, opts InstallOptions) (ServiceInfo, error) {
	if opts.Home == "" {
		opts.Home = m.home
	}
	logPath := opts.LogPath
	if logPath == "" {
		logPath = LogPath(opts.Home)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return ServiceInfo{}, err
	}
	if err := writeServiceConfig(m.home, storedServiceConfig{
		Executable: opts.Executable,
		Home:       opts.Home,
		Host:       opts.Host,
		Port:       opts.Port,
		Autostart:  false,
		LogPath:    logPath,
	}); err != nil {
		return ServiceInfo{}, err
	}
	return m.Info(context.Background())
}

func (m *detachedManager) Uninstall(_ context.Context) error {
	err := os.Remove(serviceConfigPath(m.home))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (m *detachedManager) Start(_ context.Context, opts InstallOptions) error {
	cfg, _ := readServiceConfig(m.home)
	if cfg == nil {
		if _, err := m.Install(context.Background(), opts); err != nil {
			return err
		}
		cfg, _ = readServiceConfig(m.home)
	}
	if opts.Executable == "" && cfg != nil {
		opts.Executable = cfg.Executable
	}
	if opts.Executable == "" {
		return errors.New("executable path is required to start Ariadne service")
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
	args := BuildServeArgs(opts, ManagedDetached)
	_, err := SpawnDetached(opts.Executable, args, os.Environ())
	return err
}

func (m *detachedManager) Stop(_ context.Context) error {
	rt, err := LiveRuntime(m.home)
	if err != nil || rt == nil {
		return err
	}
	return TerminateProcess(rt.PID)
}

func quoteSystemdArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\$") {
		return arg
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`)
	return `"` + replacer.Replace(arg) + `"`
}
