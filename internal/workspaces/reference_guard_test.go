package workspaces

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestReferenceRefusalsMatchLegacy(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	own := []string{filepath.Join(home, "app-data")}
	if err := os.MkdirAll(filepath.Join(home, "work"), 0700); err != nil {
		t.Fatal(err)
	}
	local := []string{home, filepath.Dir(home), filepath.Join(home, ".ssh"), filepath.Join(home, ".ssh", "key"), filepath.Join(home, ".config"), filepath.Join(home, "work"), filepath.Join(home, "work", "missing"), own[0], filepath.Join(own[0], "db.sqlite"), "~/.npmrc"}
	remote := []string{"/", "/home", "/home/alice", "~", "~/work", "~/.ssh/key", "~/work/../.aws", "~/../work", "../../work", "/../../work", "/home/alice/.config", "/home/alice/.config/gh", "C:\\Users\\alice", "C:\\Users\\alice\\repo", "D:\\.ssh\\id", "/srv/my..dir", "~/.local/share", "/root/project", "/srv/.ssh-backup", " /srv/project\u3000"}
	ptr := func(value string) *string {
		if value == "" {
			return nil
		}
		return &value
	}
	locals, remotes := []*string{}, []*string{}
	for _, path := range local {
		locals = append(locals, ptr(LocalRefusal(path, own, home)))
	}
	for _, path := range remote {
		remotes = append(remotes, ptr(RemoteRefusal(path)))
	}
	if locals[0] == nil || locals[2] == nil || locals[5] != nil || remotes[4] != nil || remotes[5] == nil {
		t.Fatal("reference credential boundary failed")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("native guard checks passed; Bun unavailable")
	}
	data, _ := json.Marshal(map[string]any{"home": home, "own": own, "local": local, "remote": remote})
	command := legacytest.Command(t, bun, "packages/agent/src/testing/go-reference-guard-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(data))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy: %v %s", err, output)
	}
	native, _ := json.Marshal(map[string]any{"local": locals, "remote": remotes})
	var actual, want any
	json.Unmarshal(native, &actual)
	if err := json.Unmarshal(output, &want); err != nil {
		t.Fatal(err, string(output))
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("guard differs\nGo %s\nTS %s", native, output)
	}
}
func TestMissingDescendantOfSymlinkCannotBypassReferenceGuard(t *testing.T) {
	home := t.TempDir()
	secret := filepath.Join(home, ".ssh")
	if err := os.Mkdir(secret, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "shortcut")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if LocalRefusal(filepath.Join(link, "missing"), nil, home) == "" {
		t.Fatal("missing child bypassed credential guard")
	}
}
func TestNativeDirectorySchemaIsCheckedWithoutFieldOrder(t *testing.T) {
	provider := sdk.ContextProviderInfo{Remote: true, Settings: sdk.SettingsSchema{Properties: map[string]sdk.SettingField{"path": {Type: "string", Directory: true}}}}
	if ReferenceRefusal(provider, map[string]any{"path": "~/.ssh"}, nil) == "" {
		t.Fatal("native schema skipped guard")
	}
}
