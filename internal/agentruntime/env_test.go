package agentruntime

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

func TestEnvSettingAndMergeEnv(t *testing.T) {
	t.Parallel()

	env := EnvSetting(map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "https://proxy.example",
			"ARIADNE_SECRET":     "blocked",
			"ariadne_token":      "blocked",
			"EMPTY_VALUE":        "",
			"NON_STRING":         123,
		},
	})
	if len(env) != 2 || env["ANTHROPIC_BASE_URL"] != "https://proxy.example" || env["EMPTY_VALUE"] != "" {
		t.Fatalf("EnvSetting = %#v", env)
	}
	if _, exists := env["ARIADNE_SECRET"]; exists {
		t.Fatalf("ARIADNE_SECRET must be filtered out: %#v", env)
	}

	merged := MergeEnv(
		map[string]string{"MCP_TOOL_TIMEOUT": "86400000", "MAX_MCP_OUTPUT_TOKENS": "200000"},
		map[string]string{"MCP_TOOL_TIMEOUT": "1000", "CUSTOM": "yes"},
	)
	if merged["MCP_TOOL_TIMEOUT"] != "1000" || merged["MAX_MCP_OUTPUT_TOKENS"] != "200000" || merged["CUSTOM"] != "yes" {
		t.Fatalf("MergeEnv = %#v", merged)
	}
}

func TestAgentEnvStripsAriadneExtra(t *testing.T) {
	t.Setenv("ARIADNE_TEST_HOST", "secret-host")
	env := process.AgentEnv(map[string]string{
		"ARIADNE_INJECTED": "secret-extra",
		"CUSTOM_TOKEN":     "ok",
	})
	if _, exists := env["ARIADNE_TEST_HOST"]; exists {
		t.Fatalf("host ARIADNE_* leaked: %#v", env)
	}
	if _, exists := env["ARIADNE_INJECTED"]; exists {
		t.Fatalf("extra ARIADNE_* leaked: %#v", env)
	}
	if env["CUSTOM_TOKEN"] != "ok" {
		t.Fatalf("CUSTOM_TOKEN missing: %#v", env)
	}
}

func TestCheckCommandWithCustomPathEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	name := "ariadne-fake-agent-bin"
	fileName := name
	content := "#!/bin/sh\nexit 0\n"
	mode := os.FileMode(0755)
	if runtime.GOOS == "windows" {
		fileName = name + ".cmd"
		content = "@echo off\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), mode); err != nil {
		t.Fatal(err)
	}

	problem, err := CheckCommandWithEnv(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if problem == nil {
		t.Fatalf("expected missing command problem without custom PATH")
	}

	pathValue := dir + string(os.PathListSeparator) + os.Getenv("PATH")
	problem, err = CheckCommandWithEnv(name, map[string]string{"PATH": pathValue})
	if err != nil {
		t.Fatal(err)
	}
	if problem != nil {
		t.Fatalf("expected command to resolve via custom PATH, got %v", problem)
	}
}

func TestSessionsConfigHashChangesWithEnv(t *testing.T) {
	t.Parallel()

	sessions, err := NewSessions(Kind{
		Name: "fake-env",
		Create: func(ctx DriverContext) (Driver, error) {
			return nil, nil
		},
	}, SessionOptions{DataDir: t.TempDir(), Quiet: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sessions.Close(ctx)
	})

	req := sdk.Request{Model: "default"}
	h1, err := sessions.config(map[string]any{"command": "claude", "env": map[string]any{"FOO": "1"}}, req)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := sessions.config(map[string]any{"command": "claude", "env": map[string]any{"FOO": "2"}}, req)
	if err != nil {
		t.Fatal(err)
	}
	h3, err := sessions.config(map[string]any{"command": "claude"}, req)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 || h1 == h3 || h2 == h3 {
		t.Fatalf("expected distinct session config hashes across env changes: h1=%s h2=%s h3=%s", h1, h2, h3)
	}
}
