package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIRoutingAndHelp(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		contains string
	}{
		{name: "no args prints help", args: nil, contains: "Usage:"},
		{name: "root help flag", args: []string{"--help"}, contains: "service"},
		{name: "help serve", args: []string{"help", "serve"}, contains: "--log-file"},
		{name: "help service", args: []string{"help", "service"}, contains: "restart"},
		{name: "help upgrade", args: []string{"help", "upgrade"}, contains: "--when-idle"},
		{name: "help dashboard", args: []string{"help", "dashboard"}, contains: "--open"},
		{name: "service help flag", args: []string{"service", "--help"}, contains: "uninstall"},
		{name: "version flag", args: []string{"--version"}, contains: version},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if err := runWithIO(context.Background(), tc.args, &out, &errBuf); err != nil {
				t.Fatalf("runWithIO(%v): %v (stderr=%s)", tc.args, err, errBuf.String())
			}
			if !strings.Contains(out.String(), tc.contains) {
				t.Fatalf("output for %v missing %q:\n%s", tc.args, tc.contains, out.String())
			}
		})
	}

	t.Run("version json", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		if err := runWithIO(context.Background(), []string{"version", "--json"}, &out, &errBuf); err != nil {
			t.Fatalf("version --json: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
			t.Fatalf("invalid json: %v (%s)", err, out.String())
		}
		if payload["version"] != version {
			t.Fatalf("unexpected version field: %+v", payload)
		}
	})

	t.Run("unknown command errors", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		if err := runWithIO(context.Background(), []string{"does-not-exist"}, &out, &errBuf); err == nil {
			t.Fatal("expected error for unknown command")
		}
	})
}

func TestConfigOverridesAndServiceStatusJSON(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfigWithOverrides(dir, "0.0.0.0", 8123, false, true, true)
	if err != nil {
		t.Fatalf("loadConfigWithOverrides: %v", err)
	}
	if cfg.Home != dir || cfg.Host != "0.0.0.0" || cfg.Port != 8123 || cfg.Open {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	var out, errBuf bytes.Buffer
	if err := runWithIO(context.Background(), []string{"service", "status", "--home", dir, "--json"}, &out, &errBuf); err != nil {
		t.Fatalf("service status --json: %v (stderr=%s)", err, errBuf.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid status json: %v (%s)", err, out.String())
	}
	if running, ok := payload["running"].(bool); !ok || running {
		t.Fatalf("expected running=false for empty temp home, got %+v", payload)
	}
}
