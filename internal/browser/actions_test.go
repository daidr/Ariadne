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
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pages := New(ctx, t.TempDir(), nil)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err := pages.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	do := func(action Action) Result {
		t.Helper()
		result, err := pages.Do(ctx, "project/thread", action)
		if err != nil {
			t.Fatalf("%s: %v", action.Name, err)
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
	do(Action{Name: "type", Ref: ref(`\[(\d+)\] textbox Example input`), Text: "验证 👋"})
	do(Action{Name: "click", Ref: ref(`\[(\d+)\] button Apply`)})
	do(Action{Name: "wait", Text: "验证 👋", Millis: 1500})
	if console := do(Action{Name: "console", All: true}); !strings.Contains(console.Text, "example log") {
		t.Fatal("console event was not collected", console)
	}
	shot := do(Action{Name: "screenshot", FullPage: true})
	if shot.MIME != "image/png" || !bytes.HasPrefix(shot.Data, []byte{137, 80, 78, 71}) {
		t.Fatal("invalid screenshot")
	}
	pages.DropThread("thread")
	if _, err := pages.Do(ctx, "project/thread", Action{Name: "snapshot"}); err == nil {
		t.Fatal("previous page still responds after dropping its thread")
	}
}
