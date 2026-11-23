//go:build windows

package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func parseWindowsCommandLine(t *testing.T, cmdLine string) []string {
	t.Helper()
	ptr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	var argc int32
	argv, err := windows.CommandLineToArgv(ptr, &argc)
	if err != nil {
		t.Fatalf("CommandLineToArgv(%q): %v", cmdLine, err)
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(argv))) }()
	// windows.CommandLineToArgv returns *[8192]*uint16 allocated via LocalAlloc.
	args := make([]string, 0, argc)
	for i := int32(0); i < argc; i++ {
		args = append(args, windows.UTF16ToString(argv[i][:]))
	}
	return args
}

func TestRenderWindowsRunCommandQuoting(t *testing.T) {
	cases := []struct {
		name        string
		opts        InstallOptions
		conhostPath string
		wantArgs    []string
	}{
		{
			name: "with conhost and spaces",
			opts: InstallOptions{
				Executable: `C:\Users\Test User\AppData\Local\Ariadne\bin\ariadne.exe`,
				Home:       `C:\Users\Test User\.ariadne`,
			},
			conhostPath: `C:\WINDOWS\system32\conhost.exe`,
			wantArgs: []string{
				`C:\WINDOWS\system32\conhost.exe`,
				`--headless`,
				`C:\Users\Test User\AppData\Local\Ariadne\bin\ariadne.exe`,
				`service`,
				`start`,
				`--home`,
				`C:\Users\Test User\.ariadne`,
			},
		},
		{
			name: "with trailing backslash in home",
			opts: InstallOptions{
				Executable: `C:\Ariadne\ariadne.exe`,
				Home:       `C:\AriadneData\`,
			},
			conhostPath: `C:\WINDOWS\system32\conhost.exe`,
			wantArgs: []string{
				`C:\WINDOWS\system32\conhost.exe`,
				`--headless`,
				`C:\Ariadne\ariadne.exe`,
				`service`,
				`start`,
				`--home`,
				`C:\AriadneData\`,
			},
		},
		{
			name: "without conhost fallback",
			opts: InstallOptions{
				Executable: `C:\Program Files\Ariadne\ariadne.exe`,
				Home:       `C:\Users\daidr\.ariadne`,
			},
			conhostPath: "",
			wantArgs: []string{
				`C:\Program Files\Ariadne\ariadne.exe`,
				`service`,
				`start`,
				`--home`,
				`C:\Users\daidr\.ariadne`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmdLine := renderWindowsRunCommand(tc.opts, tc.conhostPath)
			got := parseWindowsCommandLine(t, cmdLine)
			if !reflect.DeepEqual(got, tc.wantArgs) {
				t.Fatalf("parsed args mismatch for %q:\n got: %#v\nwant: %#v", cmdLine, got, tc.wantArgs)
			}
		})
	}
}

func TestWindowsManagerLifecycleWithRealHKCURegistry(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home dir")
	testKeyPath := fmt.Sprintf(`Software\AriadneUnitTest\%d-%d`, os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, testKeyPath)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\AriadneUnitTest`)
	})

	store := registryAutostartStore{
		root:      registry.CURRENT_USER,
		keyPath:   testKeyPath,
		valueName: windowsRunValueName,
	}

	var spawnedExe string
	var spawnedArgs []string
	mgr := &windowsManager{
		home:        home,
		autostart:   store,
		conhostPath: func() string { return `C:\WINDOWS\system32\conhost.exe` },
		cleanupLegacyTask: func(context.Context) string {
			return ""
		},
		spawn: func(executable string, args []string, _ []string) (int, error) {
			spawnedExe = executable
			spawnedArgs = append([]string(nil), args...)
			return 4321, nil
		},
	}

	ctx := context.Background()

	// Initially not installed.
	info, err := mgr.Info(ctx)
	if err != nil {
		t.Fatalf("Info before install: %v", err)
	}
	if info.Installed || info.Autostart || info.ManagedBy != ManagedWindowsRun || !info.SupportsAutostart {
		t.Fatalf("unexpected initial info: %+v", info)
	}

	// Install with autostart enabled (must work without administrator privileges).
	exe := filepath.Join(t.TempDir(), "bin dir", "ariadne.exe")
	info, err = mgr.Install(ctx, InstallOptions{
		Executable: exe,
		Home:       home,
		Host:       "0.0.0.0",
		Port:       9123,
		Autostart:  true,
	})
	if err != nil {
		t.Fatalf("Install(autostart=true): %v", err)
	}
	if !info.Installed || !info.Autostart || info.ManagedBy != ManagedWindowsRun {
		t.Fatalf("unexpected info after install: %+v", info)
	}

	// Verify the HKCU registry value was written and parses cleanly.
	k, err := registry.OpenKey(registry.CURRENT_USER, testKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("OpenKey(%s): %v", testKeyPath, err)
	}
	rawCmd, _, err := k.GetStringValue(windowsRunValueName)
	_ = k.Close()
	if err != nil {
		t.Fatalf("GetStringValue: %v", err)
	}
	parsed := parseWindowsCommandLine(t, rawCmd)
	wantParsed := []string{`C:\WINDOWS\system32\conhost.exe`, `--headless`, exe, `service`, `start`, `--home`, home}
	if !reflect.DeepEqual(parsed, wantParsed) {
		t.Fatalf("registry command mismatch:\n got: %#v\nwant: %#v", parsed, wantParsed)
	}

	// Start should load stored host/port/logPath and launch with ManagedWindowsRun.
	if err := mgr.Start(ctx, InstallOptions{Home: home}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if spawnedExe != exe {
		t.Fatalf("spawned exe mismatch: got %q, want %q", spawnedExe, exe)
	}
	wantSpawnArgs := BuildServeArgs(InstallOptions{
		Executable: exe,
		Home:       home,
		Host:       "0.0.0.0",
		Port:       9123,
		LogPath:    LogPath(home),
	}, ManagedWindowsRun)
	if !reflect.DeepEqual(spawnedArgs, wantSpawnArgs) {
		t.Fatalf("spawned args mismatch:\n got: %#v\nwant: %#v", spawnedArgs, wantSpawnArgs)
	}

	// Re-install with autostart disabled should remove the HKCU value while keeping service.json.
	info, err = mgr.Install(ctx, InstallOptions{
		Executable: exe,
		Home:       home,
		Autostart:  false,
	})
	if err != nil {
		t.Fatalf("Install(autostart=false): %v", err)
	}
	if !info.Installed || info.Autostart {
		t.Fatalf("expected installed=true autostart=false, got %+v", info)
	}
	exists, err := store.Exists()
	if err != nil || exists {
		t.Fatalf("expected HKCU value deleted, exists=%v err=%v", exists, err)
	}

	// Uninstall removes both registry value and service.json.
	if err := mgr.Uninstall(ctx); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	info, err = mgr.Info(ctx)
	if err != nil {
		t.Fatalf("Info after uninstall: %v", err)
	}
	if info.Installed || info.Autostart {
		t.Fatalf("expected uninstalled state, got %+v", info)
	}
}

type stubAutostartStore struct {
	exists    bool
	setErr    error
	deleteErr error
	lastCmd   string
}

func (s *stubAutostartStore) Exists() (bool, error) { return s.exists, nil }
func (s *stubAutostartStore) Set(command string) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.lastCmd = command
	s.exists = true
	return nil
}
func (s *stubAutostartStore) Delete() error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.exists = false
	return nil
}

func TestWindowsManagerErrorAndLegacyNote(t *testing.T) {
	home := t.TempDir()

	t.Run("autostart write failure surfaces actionable hint", func(t *testing.T) {
		mgr := &windowsManager{
			home:        home,
			autostart:   &stubAutostartStore{setErr: errors.New("access denied by policy")},
			conhostPath: func() string { return "" },
		}
		_, err := mgr.Install(context.Background(), InstallOptions{
			Executable: `C:\Ariadne\ariadne.exe`,
			Home:       home,
			Autostart:  true,
		})
		if err == nil {
			t.Fatal("expected error when autostart Set fails")
		}
		if !strings.Contains(err.Error(), "--no-autostart") || !strings.Contains(err.Error(), windowsRunKeyPath) {
			t.Fatalf("expected actionable error message, got: %v", err)
		}
	})

	t.Run("legacy task cleanup note is non-fatal", func(t *testing.T) {
		store := &stubAutostartStore{}
		mgr := &windowsManager{
			home:        home,
			autostart:   store,
			conhostPath: func() string { return "" },
			cleanupLegacyTask: func(context.Context) string {
				return `legacy scheduled task "Ariadne" could not be removed`
			},
		}
		info, err := mgr.Install(context.Background(), InstallOptions{
			Executable: `C:\Ariadne\ariadne.exe`,
			Home:       home,
			Autostart:  true,
		})
		if err != nil {
			t.Fatalf("Install should succeed even with legacy task note: %v", err)
		}
		if !strings.Contains(info.Note, "legacy scheduled task") {
			t.Fatalf("expected legacy task note, got %+v", info)
		}
	})
}

func TestDecodeWindowsOutput(t *testing.T) {
	t.Run("valid utf8 passes through", func(t *testing.T) {
		if got := decodeWindowsOutput([]byte("  错误: 拒绝访问。\r\n")); got != "错误: 拒绝访问。" {
			t.Fatalf("got %q, want %q", got, "错误: 拒绝访问。")
		}
	})

	t.Run("utf16le with bom decodes cleanly", func(t *testing.T) {
		runes := utf16.Encode([]rune("错误: 拒绝访问。\r\n"))
		raw := make([]byte, 2+len(runes)*2)
		raw[0], raw[1] = 0xFF, 0xFE
		for i, u := range runes {
			binary.LittleEndian.PutUint16(raw[2+i*2:], u)
		}
		if got := decodeWindowsOutput(raw); got != "错误: 拒绝访问。" {
			t.Fatalf("got %q, want %q", got, "错误: 拒绝访问。")
		}
	})

	t.Run("gbk cp936 decodes cleanly", func(t *testing.T) {
		// GBK bytes for "错误: 拒绝访问。"
		gbk := []byte{0xB4, 0xED, 0xCE, 0xF3, 0x3A, 0x20, 0xBE, 0xDC, 0xBE, 0xF8, 0xB7, 0xC3, 0xCE, 0xCA, 0xA1, 0xA3}
		got, ok := decodeWindowsCodePage(gbk, 936, true)
		if !ok {
			t.Fatal("decodeWindowsCodePage(cp936) returned ok=false")
		}
		if got != "错误: 拒绝访问。" {
			t.Fatalf("got %q, want %q", got, "错误: 拒绝访问。")
		}
	})
}
