package agentruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointBoundsExpiryRemovalAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpoints(dir, "codex", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	store.now = func() time.Time { return now }
	value := Checkpoint{Config: "config", System: "system", FPS: []string{"u", "a"}, Driver: json.RawMessage(`{"threadId":"native-thread"}`)}
	for _, key := range []string{"one", "two", "three"} {
		if err := store.Write(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.files) != 2 || store.Read("one") != nil || store.Read("two") == nil || store.Read("three") == nil {
		t.Fatal("checkpoint count limit did not evict the oldest")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(store.files) != 0 {
		t.Fatal("closed store retained metadata")
	}
	store, err = NewCheckpoints(dir, "codex", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if restored := store.Read("three"); restored == nil || restored.Config != "config" || string(restored.Driver) != string(value.Driver) {
		t.Fatal(restored)
	}
	old := time.Now().Add(-checkpointTTL - time.Hour)
	path := filepath.Join(dir, "checkpoints", "codex", checkpointPath("two"))
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if store.Read("two") != nil {
		t.Fatal("expired checkpoint restored")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired file retained", err)
	}
	large := value
	large.Driver = json.RawMessage(`{"blob":"` + strings.Repeat("x", maxCheckpointBytes) + `"}`)
	if err := store.Write("three", large); err != nil {
		t.Fatal(err)
	}
	if store.Read("three") != nil || len(store.files) != 0 {
		t.Fatal("oversized checkpoint retained")
	}
	if err := store.Write("../outside", value); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("../outside"); err != nil {
		t.Fatal(err)
	}
	if len(store.files) != 0 {
		t.Fatal("removed checkpoint retained")
	}
	if _, err := NewCheckpoints(dir, "../escape", 2); err == nil {
		t.Fatal("unsafe provider kind accepted")
	}
}
func TestInvalidCheckpointAndFailedWriteLeaveNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpoints(dir, "codex", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	name := checkpointPath("invalid")
	for _, content := range []string{`{}`, `{"config":"x","system":"s","fps":[],"driver":false}`, `{"config":"x","system":"s","fps":null,"driver":{}}`, `{"config":null,"system":"s","fps":[],"driver":{}}`} {
		if err := os.WriteFile(filepath.Join(dir, "checkpoints", "codex", name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if store.Read("invalid") != nil {
			t.Fatal("invalid checkpoint restored", content)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "checkpoints", "codex", checkpointPath("directory")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("directory", Checkpoint{Config: "x", System: "s", FPS: []string{}, Driver: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("checkpoint overwrote a directory")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "checkpoints", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatal("failed write retained temporary file", entry.Name())
		}
	}
}
