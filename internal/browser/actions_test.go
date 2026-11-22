package browser

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrowserInteractsWithLocalPageAndCloses(t *testing.T) {
	if installed() == "" {
		t.Skip("Chrome, Edge or Chromium is required")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><input aria-label="Example input" value="Before"><button onclick="document.querySelector('p').textContent=document.querySelector('input').value;console.error('example log')">Apply</button><p>Waiting</p>`)
	}))
	defer site.Close()
	browserCtx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pages := New(browserCtx, t.TempDir(), nil)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := pages.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	do := func(action Action) Result {
		t.Helper()
		start := time.Now()
		stepCtx, stopStep := context.WithTimeout(browserCtx, 25*time.Second)
		defer stopStep()
		result, err := pages.Do(stepCtx, "project/thread", action)
		if err != nil {
			t.Fatalf("%s (after %s): %v", action.Name, time.Since(start).Round(time.Millisecond), err)
		}
		return result
	}
	do(Action{Name: "open", URL: site.URL, Width: 800, Height: 480})
	snapshot := do(Action{Name: "snapshot"}).Text
	ref := func(pattern string) int {
		t.Helper()
		matches := regexp.MustCompile(pattern).FindStringSubmatch(snapshot)
		if len(matches) < 2 {
			t.Fatal("snapshot missed target", snapshot)
		}
		id, _ := strconv.Atoi(matches[1])
		return id
	}
	buttonRef := ref(`\[(\d+)\] button Apply`)
	do(Action{Name: "type", Ref: ref(`\[(\d+)\] textbox Example input`), Text: "验证 👋"})
	do(Action{Name: "click", Ref: buttonRef})
	do(Action{Name: "wait", Text: "验证 👋", Millis: 5000})
	if console := do(Action{Name: "console", All: true}); !strings.Contains(console.Text, "example log") {
		t.Fatal("console event was not collected", console)
	}
	shot := do(Action{Name: "screenshot", FullPage: true})
	if shot.MIME != "image/png" || !bytes.HasPrefix(shot.Data, []byte{137, 80, 78, 71}) {
		t.Fatal("invalid screenshot")
	}
	elementShot := do(Action{Name: "screenshot", Ref: buttonRef})
	if elementShot.MIME != "image/png" || !bytes.HasPrefix(elementShot.Data, []byte{137, 80, 78, 71}) {
		t.Fatal("invalid element screenshot")
	}
	pages.DropThread("thread")
	if _, err := pages.Do(browserCtx, "project/thread", Action{Name: "snapshot"}); err == nil {
		t.Fatal("previous page still responds after dropping its thread")
	}
}

func TestBrowserFlagsEnableScreenshotSurfaceAndDisableGPUOnWindows(t *testing.T) {
	findFlag := func(flags []browserFlag, name string) (any, bool) {
		for _, flag := range flags {
			if flag.name == name {
				return flag.value, true
			}
		}
		return nil, false
	}

	for _, goos := range []string{"windows", "linux", "darwin"} {
		flags := browserFlags(goos)
		features, ok := findFlag(flags, "enable-features")
		if !ok {
			t.Fatalf("%s: missing enable-features flag", goos)
		}
		featureList, _ := features.(string)
		for _, want := range []string{"NetworkService", "NetworkServiceInProcess", "CDPScreenshotNewSurface"} {
			if !strings.Contains(featureList, want) {
				t.Fatalf("%s: enable-features=%q missing %q", goos, featureList, want)
			}
		}
		disableGPU, hasDisableGPU := findFlag(flags, "disable-gpu")
		if goos == "windows" {
			if !hasDisableGPU || disableGPU != true {
				t.Fatalf("windows: expected disable-gpu=true, got present=%v value=%v", hasDisableGPU, disableGPU)
			}
		} else if hasDisableGPU {
			t.Fatalf("%s: unexpected disable-gpu flag (%v)", goos, disableGPU)
		}
	}
}
