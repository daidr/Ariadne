//go:build windows

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	windowsRunKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	windowsRunValueName = "Ariadne"
	legacyWindowsTask   = "Ariadne"

	codePageUTF8      = 65001
	mbErrInvalidChars = 0x00000008
)

var (
	kernel32DLL             = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleOutputCP  = kernel32DLL.NewProc("GetConsoleOutputCP")
	procGetOEMCP            = kernel32DLL.NewProc("GetOEMCP")
	procGetACP              = kernel32DLL.NewProc("GetACP")
	procMultiByteToWideChar = kernel32DLL.NewProc("MultiByteToWideChar")
)

type windowsAutostartStore interface {
	Exists() (bool, error)
	Set(command string) error
	Delete() error
}

type registryAutostartStore struct {
	root      registry.Key
	keyPath   string
	valueName string
}

func (s registryAutostartStore) Exists() (bool, error) {
	k, err := registry.OpenKey(s.root, s.keyPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()

	val, _, err := k.GetStringValue(s.valueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(val) != "", nil
}

func (s registryAutostartStore) Set(command string) error {
	k, _, err := registry.CreateKey(s.root, s.keyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(s.valueName, command)
}

func (s registryAutostartStore) Delete() error {
	k, err := registry.OpenKey(s.root, s.keyPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()

	err = k.DeleteValue(s.valueName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

type windowsManager struct {
	home              string
	autostart         windowsAutostartStore
	conhostPath       func() string
	cleanupLegacyTask func(ctx context.Context) string
	spawn             func(executable string, args []string, env []string) (int, error)
}

func NewManager(home string) Manager {
	return &windowsManager{
		home: home,
		autostart: registryAutostartStore{
			root:      registry.CURRENT_USER,
			keyPath:   windowsRunKeyPath,
			valueName: windowsRunValueName,
		},
		conhostPath:       defaultConhostPath,
		cleanupLegacyTask: cleanupLegacyWindowsTask,
		spawn:             SpawnDetached,
	}
}

func (m *windowsManager) ManagedBy() string       { return ManagedWindowsRun }
func (m *windowsManager) SupportsAutostart() bool { return true }

func defaultConhostPath() string {
	if sysDir, err := windows.GetSystemDirectory(); err == nil && sysDir != "" {
		candidate := filepath.Join(sysDir, "conhost.exe")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		candidate := filepath.Join(root, "System32", "conhost.exe")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func runSchtasks(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd.CombinedOutput()
}

// cleanupLegacyWindowsTask removes the legacy Task Scheduler entry created by older
// elevated Ariadne installs so users do not end up with duplicate logon triggers.
// Missing tasks or access-denied errors are never fatal.
func cleanupLegacyWindowsTask(ctx context.Context) string {
	taskCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if _, err := runSchtasks(taskCtx, "/Query", "/TN", legacyWindowsTask); err != nil {
		return ""
	}
	out, err := runSchtasks(taskCtx, "/Delete", "/F", "/TN", legacyWindowsTask)
	if err == nil {
		return ""
	}
	detail := decodeWindowsOutput(out)
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Sprintf("legacy scheduled task %q could not be removed (%s); you can delete it in Task Scheduler with administrator privileges if needed", legacyWindowsTask, detail)
}

func decodeWindowsCodePage(data []byte, codePage uint32, strict bool) (string, bool) {
	if len(data) == 0 || codePage == 0 {
		return "", false
	}
	var flags uint32
	if strict {
		flags = mbErrInvalidChars
	}
	n, _, _ := procMultiByteToWideChar.Call(
		uintptr(codePage),
		uintptr(flags),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		0,
		0,
	)
	if n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	written, _, _ := procMultiByteToWideChar.Call(
		uintptr(codePage),
		uintptr(flags),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&buf[0])),
		n,
	)
	if written == 0 {
		return "", false
	}
	return string(utf16.Decode(buf[:written])), true
}

// decodeWindowsOutput converts raw output bytes from Windows CLI utilities
// (which commonly write in the active console/OEM/ANSI code page, such as GBK CP936)
// into a trimmed UTF-8 string.
func decodeWindowsOutput(data []byte) string {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		raw := data[2:]
		if len(raw)%2 == 1 {
			raw = raw[:len(raw)-1]
		}
		u16 := make([]uint16, len(raw)/2)
		for i := range u16 {
			u16[i] = binary.LittleEndian.Uint16(raw[i*2:])
		}
		return strings.TrimSpace(string(utf16.Decode(u16)))
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ""
	}
	if utf8.Valid(trimmed) {
		return string(trimmed)
	}

	candidates := make([]uint32, 0, 3)
	addCP := func(cp uint32) {
		if cp == 0 || cp == codePageUTF8 {
			return
		}
		for _, existing := range candidates {
			if existing == cp {
				return
			}
		}
		candidates = append(candidates, cp)
	}

	if r, _, _ := procGetConsoleOutputCP.Call(); r != 0 {
		addCP(uint32(r))
	}
	if r, _, _ := procGetOEMCP.Call(); r != 0 {
		addCP(uint32(r))
	}
	if r, _, _ := procGetACP.Call(); r != 0 {
		addCP(uint32(r))
	}

	for _, cp := range candidates {
		if decoded, ok := decodeWindowsCodePage(trimmed, cp, true); ok {
			return strings.TrimSpace(decoded)
		}
	}
	for _, cp := range candidates {
		if decoded, ok := decodeWindowsCodePage(trimmed, cp, false); ok {
			return strings.TrimSpace(decoded)
		}
	}
	return strings.TrimSpace(string(trimmed))
}

func quoteWindowsCommandArg(arg string, forceQuote bool) string {
	escaped := syscall.EscapeArg(arg)
	if !forceQuote || strings.HasPrefix(escaped, `"`) {
		return escaped
	}
	// When wrapping an argument in double quotes, trailing backslashes must be
	// doubled so Windows CommandLineToArgvW does not treat \" as an escaped quote.
	trailingBackslashes := 0
	for i := len(escaped) - 1; i >= 0 && escaped[i] == '\\'; i-- {
		trailingBackslashes++
	}
	return `"` + escaped + strings.Repeat(`\`, trailingBackslashes) + `"`
}

// renderWindowsRunCommand builds the HKCU Run startup command.
// It uses `conhost.exe --headless` when available so Windows does not flash a
// console window at login, and invokes `ariadne service start` which spawns
// Ariadne detached and exits immediately (leaving no resident conhost process).
func renderWindowsRunCommand(opts InstallOptions, conhostPath string) string {
	parts := make([]string, 0, 7)
	if conhostPath != "" {
		parts = append(parts, quoteWindowsCommandArg(conhostPath, true), "--headless")
	}
	parts = append(parts, quoteWindowsCommandArg(opts.Executable, true), "service", "start")
	if opts.Home != "" {
		parts = append(parts, "--home", quoteWindowsCommandArg(opts.Home, true))
	}
	return strings.Join(parts, " ")
}

func (m *windowsManager) Info(_ context.Context) (ServiceInfo, error) {
	cfg, err := readServiceConfig(m.home)
	if err != nil {
		return ServiceInfo{}, err
	}
	autostart, err := m.autostart.Exists()
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("read Windows user startup entry (HKCU\\%s\\%s): %w", windowsRunKeyPath, windowsRunValueName, err)
	}
	info := ServiceInfo{
		Installed:         cfg != nil || autostart,
		ManagedBy:         ManagedWindowsRun,
		Autostart:         autostart,
		SupportsAutostart: true,
		ConfigPath:        serviceConfigPath(m.home),
		LogPath:           LogPath(m.home),
	}
	if cfg != nil && cfg.LogPath != "" {
		info.LogPath = cfg.LogPath
	}
	return info, nil
}

func (m *windowsManager) Install(ctx context.Context, opts InstallOptions) (ServiceInfo, error) {
	if opts.Executable == "" {
		return ServiceInfo{}, errors.New("executable path is required")
	}
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
		conhost := ""
		if m.conhostPath != nil {
			conhost = m.conhostPath()
		}
		cmdLine := renderWindowsRunCommand(opts, conhost)
		if err := m.autostart.Set(cmdLine); err != nil {
			return ServiceInfo{}, fmt.Errorf("register Windows user startup entry (HKCU\\%s\\%s): %w (retry with --no-autostart to install without login autostart)", windowsRunKeyPath, windowsRunValueName, err)
		}
	} else {
		if err := m.autostart.Delete(); err != nil {
			return ServiceInfo{}, fmt.Errorf("remove Windows user startup entry (HKCU\\%s\\%s): %w", windowsRunKeyPath, windowsRunValueName, err)
		}
	}

	var note string
	if m.cleanupLegacyTask != nil {
		note = m.cleanupLegacyTask(ctx)
	}

	info, err := m.Info(ctx)
	if err != nil {
		return ServiceInfo{}, err
	}
	if note != "" {
		info.Note = note
	}
	return info, nil
}

func (m *windowsManager) Uninstall(ctx context.Context) error {
	if err := m.autostart.Delete(); err != nil {
		return fmt.Errorf("remove Windows user startup entry (HKCU\\%s\\%s): %w", windowsRunKeyPath, windowsRunValueName, err)
	}
	if m.cleanupLegacyTask != nil {
		_ = m.cleanupLegacyTask(ctx)
	}
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
	args := BuildServeArgs(opts, ManagedWindowsRun)
	spawnFn := m.spawn
	if spawnFn == nil {
		spawnFn = SpawnDetached
	}
	_, err := spawnFn(opts.Executable, args, os.Environ())
	return err
}

func (m *windowsManager) Stop(_ context.Context) error {
	rt, err := LiveRuntime(m.home)
	if err != nil || rt == nil {
		return err
	}
	return TerminateProcess(rt.PID)
}
