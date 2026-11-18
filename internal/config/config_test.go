package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

func TestReusesExistingDataAndToken(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "token"), []byte("existing-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "ariadne.sqlite"), []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(env(map[string]string{"ARIADNE_HOME": home, "ARIADNE_PORT": "7440", "ARIADNE_OPEN": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "existing-token" || got.FirstRun || !got.Open || got.Address() != "127.0.0.1:7440" {
		t.Fatalf("config: %+v", got)
	}
	data, err := os.ReadFile(got.DatabasePath)
	if err != nil || string(data) != "untouched" {
		t.Fatalf("changed database: %q, %v", data, err)
	}
}

func TestEnvironmentTokenStaysOutOfDisk(t *testing.T) {
	home := t.TempDir()
	got, err := Load(env(map[string]string{"ARIADNE_HOME": home, "ARIADNE_TOKEN": "from-env"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "from-env" || !got.FirstRun {
		t.Fatalf("config: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(home, "token")); !os.IsNotExist(err) {
		t.Fatalf("environment token was persisted: %v", err)
	}
}

func TestGeneratedTokenIsStable(t *testing.T) {
	home := t.TempDir()
	first, err := Load(env(map[string]string{"ARIADNE_HOME": home}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(env(map[string]string{"ARIADNE_HOME": home}))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Token) != 43 || first.Token != second.Token {
		t.Fatal("generated token is not stable")
	}
}

func TestInvalidPortAndEmptyTokenFailBeforeServing(t *testing.T) {
	for _, values := range []map[string]string{{"ARIADNE_PORT": "NaN"}, {"ARIADNE_PORT": "65536"}, {"ARIADNE_TOKEN": ""}} {
		values["ARIADNE_HOME"] = t.TempDir()
		if _, err := Load(env(values)); err == nil {
			t.Fatalf("accepted invalid config: %v", values)
		}
	}
}

func TestEmptyTokenFileIsRegenerated(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "token"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(env(map[string]string{"ARIADNE_HOME": home}))
	if err != nil || len(got.Token) != 43 {
		t.Fatalf("empty token recovery: %v", err)
	}
}
