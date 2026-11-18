package projectsettings

import (
	"encoding/json"
	"errors"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/daidr/Ariadne/internal/domainerr"
)

func TestSettingsMatchLegacyValidationAndDefaults(t *testing.T) {
	type check struct {
		Mode  string   `json:"mode"`
		Input any      `json:"input"`
		Tools []string `json:"tools,omitempty"`
	}
	cases := make([]check, 0)
	for _, raw := range []string{
		`{}`, `null`, `false`, `{"instructions":"  中文说明  "}`, `{"instructions":null}`,
		`{"delivery":{"mode":"external","instructions":"  提交到预发布分支  "}}`,
		`{"delivery":{"mode":"bad"}}`, `{"delivery":{"instructions":5}}`,
		`{"tools":["fetch","browser","fetch"]}`, `{"tools":["missing"]}`, `{"tools":null}`,
		`{"budget":{"thread":1000,"project":""}}`, `{"budget":{"thread":"1000"}}`, `{"budget":{"project":999}}`,
		`{"workspace":{"setup":"  bun install  ","check":" test ","shell":"pwsh","retention":"keep","branchPrefix":"ai/"}}`,
		`{"workspace":{"setup":"bun install","auto":{"setup":true,"check":false}}}`,
		`{"workspace":{"retention":"bad"}}`, `{"workspace":{"shell":"bad"}}`, `{"workspace":{"auto":{"setup":5}}}`,
		`{"review":{"enabled":false},"autopilot":{"enabled":true}}`, `{"autopilot":{"enabled":true,"method":"merge"}}`,
		`{"autopilot":{"method":"rebase"}}`,
		`{"routines":[{"id":"daily","prompt":"  检查  ","days":[6,1,1],"time":"09:30"}]}`,
		`{"routines":[{"id":"daily","prompt":"检查","days":[],"time":"23:59","enabled":false}]}`,
		`{"routines":[{"id":"daily","prompt":"检查","days":[7],"time":"09:30"}]}`,
		`{"routines":[{"id":"daily","prompt":"检查","days":[1],"time":"24:00"}]}`,
		`{"routines":[{"id":"daily","prompt":"检查","days":[1],"time":"09:30"},{"id":"daily","prompt":"检查","days":[1],"time":"09:30"}]}`,
	} {
		var input any
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, check{Mode: "parse", Input: input, Tools: []string{"fetch", "browser"}})
	}
	cases = append(cases, check{Mode: "parse", Input: map[string]any{"instructions": strings.Repeat("🧵", 8001)}})
	for _, raw := range []string{"", `{"workspace":{"setup":"install","check":"check"}}`, `{"delivery":{"mode":"external"}}`} {
		cases = append(cases, check{Mode: "stored", Input: raw})
	}
	for _, raw := range []any{"Fix My Export!", "中文", "KKFoo", "İFoo", "ΟΣ", nil, 5, "a" + strings.Repeat("-b", 40)} {
		cases = append(cases, check{Mode: "slug", Input: raw})
	}
	for _, raw := range []string{"", "ai/", "foo/bar/", "../bad", "a.lock", "a.lock/", "foo//bar", "-bad", ".bad"} {
		cases = append(cases, check{Mode: "prefix", Input: raw})
	}
	results := make([]map[string]any, 0, len(cases))
	for _, test := range cases {
		var value any
		var err error
		switch test.Mode {
		case "parse":
			value, err = Parse(test.Input, test.Tools)
		case "stored":
			value, err = Stored(test.Input.(string))
		case "slug":
			value = BranchSlug(test.Input)
		case "prefix":
			value = ValidBranchPrefix(test.Input.(string))
		}
		if err != nil {
			var localized *domainerr.Error
			if !errors.As(err, &localized) {
				t.Fatal(err)
			}
			results = append(results, map[string]any{"error": localized})
		} else {
			results = append(results, map[string]any{"value": value})
		}
	}
	if value, err := Parse(map[string]any{}, nil); err != nil || value.Workspace.Retention != "auto" || !value.Review.Enabled {
		t.Fatalf("defaults: %+v %v", value, err)
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Log("native validation ran; legacy oracle requires Bun")
		return
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(cases)
	cmd := legacytest.Command(t, bun, "packages/core/src/testing/go-settings-fixture.ts")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(string(input))
	legacyJSON, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("settings oracle: %v\n%s", err, legacyJSON)
	}
	nativeJSON, _ := json.Marshal(results)
	var native, legacy []any
	if err := json.Unmarshal(nativeJSON, &native); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(legacyJSON, &legacy); err != nil {
		t.Fatal(err)
	}
	for i := range native {
		if !reflect.DeepEqual(native[i], legacy[i]) {
			t.Errorf("case %d (%s) differs\nGo: %v\nTS: %v", i, cases[i].Mode, native[i], legacy[i])
		}
	}
}

func TestRoutineNextRunAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	routine := Routine{Enabled: true, Days: []int{0}, Time: "02:30"}
	after := time.Date(2026, 3, 8, 0, 0, 0, 0, location)
	next := NextRun(routine, after, location)
	if next == nil || next.Hour() != 3 || next.Minute() != 30 {
		t.Fatalf("spring gap: %v", next)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	fall := time.Date(2026, 10, 25, 0, 0, 0, 0, berlin)
	next = NextRun(routine, fall, berlin)
	if next == nil {
		t.Fatal("missing fall run")
	}
	_, offset := next.Zone()
	if offset != 2*60*60 {
		t.Fatalf("did not choose earlier overlap: %v", next)
	}
	if again := NextRun(routine, *next, berlin); again == nil || again.Day() != 1 || again.Month() != time.November {
		t.Fatalf("same minute repeated: %v", again)
	}
}
