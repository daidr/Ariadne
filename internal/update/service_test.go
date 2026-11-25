package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/settings"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (transport releaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func response(code int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: code, Header: make(http.Header), ContentLength: int64(len(body)),
		Body: io.NopCloser(bytes.NewReader(body)),
	}
}

func encodedRelease(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "--version":
			fmt.Println("1.0.0")
			os.Exit(0)
		case "self-check":
			os.Exit(0)
		case "serve":
			fmt.Println("ready")
			_, _ = io.Copy(os.Stdout, os.Stdin)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func copyInstalledTestBinary(t *testing.T, folder, name string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(folder, name+filepath.Ext(targetAsset(runtime.GOOS, runtime.GOARCH)))
	if err := os.WriteFile(dst, data, 0700); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestChannelsKeepTheCurrentVersionWhenThereIsNoStableRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := settings.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == releaseAPI+"/latest" {
			return response(http.StatusNotFound, nil), nil
		}
		if req.URL.String() != releaseAPI+"?per_page=30&page=1" {
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return response(http.StatusOK, encodedRelease([]githubRelease{
			{Tag: "v0.1.0-alpha.9", Prerelease: true},
			{Tag: "v0.1.0-alpha.10", Prerelease: true, Body: "New features"},
			{Tag: "v9.0.0", Draft: true},
			{Tag: "unversioned"},
		})), nil
	})}
	service := New(t.Context(), store, Options{Version: "0.1.0-alpha.9", Client: client})
	defer service.Close(context.Background())
	if initial := service.View(); initial.Preferences.Channel != "preview" || initial.Preferences.Automatic {
		t.Fatalf("unexpected initial preferences: %+v", initial)
	}
	view := service.Check(t.Context())
	if view.State != "available" || view.Release == nil || view.Release.Version != "0.1.0-alpha.10" || view.Release.Notes != "New features" {
		t.Fatalf("unexpected preview release: %+v", view)
	}
	if _, err := service.Save(settings.UpdatePreferences{Channel: "stable"}); err != nil {
		t.Fatal(err)
	}
	view = service.Check(t.Context())
	if view.State != "no-release" || view.Release != nil || view.Version != "0.1.0-alpha.9" {
		t.Fatalf("stable channel should keep the installed preview: %+v", view)
	}
	reopened, err := settings.Open(path)
	if err != nil || reopened.UpdatePreferences("preview").Channel != "stable" {
		t.Fatalf("channel did not persist: %v", err)
	}
}

func TestStableChannelDoesNotDowngrade(t *testing.T) {
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUpdatePreferences(settings.UpdatePreferences{Channel: "stable"}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, encodedRelease(githubRelease{Tag: "v1.2.0"})), nil
	})}
	service := New(t.Context(), store, Options{Version: "1.3.0-rc.1", Client: client})
	defer service.Close(context.Background())
	if view := service.Check(t.Context()); view.State != "current" {
		t.Fatalf("older release must not replace a newer preview: %+v", view)
	}
}

func buildUpdateFixture(t *testing.T, folder, name, version string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go compiler required for the running executable test")
	}
	path := filepath.Join(folder, name+filepath.Ext(targetAsset(runtime.GOOS, runtime.GOARCH)))
	source := filepath.Join(folder, "main.go")
	const program = `package main
import ("fmt"; "io"; "os")
var version string
func main() {
	switch os.Args[1] {
	case "--version": fmt.Println(version)
	case "self-check":
	case "serve":
		fmt.Println("ready")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	default: os.Exit(2)
	}
}`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "build", "-ldflags", "-X main.version="+version, "-o", path, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	return path
}

func TestAutomaticUpdateReplacesRunningProgram(t *testing.T) {
	t.Parallel()
	platform := targetAsset(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		t.Skip("platform has no published updater asset")
	}
	dir := t.TempDir()
	old := copyInstalledTestBinary(t, dir, "installed")
	newer := buildUpdateFixture(t, dir, "release", "1.1.0")
	payload, err := os.ReadFile(newer)
	if err != nil {
		t.Fatal(err)
	}

	original := exec.CommandContext(t.Context(), old, "serve")
	prepareCommand(original)
	out, err := original.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := original.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = original.Process.Kill()
		_ = original.Wait()
	}()
	reader := bufio.NewReader(out)
	if line, err := reader.ReadString('\n'); strings.TrimSpace(line) != "ready" || err != nil {
		t.Fatalf("original process did not start: %q, %v", line, err)
	}

	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUpdatePreferences(settings.UpdatePreferences{Channel: "stable", Automatic: true}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case releaseAPI + "/latest":
			return response(http.StatusOK, encodedRelease(githubRelease{
				Tag: "v1.1.0", Assets: []asset{{Name: platform, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), Size: int64(len(payload))}},
			})), nil
		case releaseWeb + "/download/v1.1.0/" + platform:
			return response(http.StatusOK, payload), nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
	})}
	service := New(t.Context(), store, Options{Version: "1.0.0", Executable: old, Client: client, OnError: func(err error) { t.Log(err) }})
	defer service.Close(context.Background())
	waitForUpdate(t, service, func(view View) bool { return view.State == "ready" || view.Failure != "" })
	if view := service.View(); view.Failure != "" {
		t.Fatalf("automatic update failed: %+v", view)
	}
	if err := verifyProgram(t.Context(), old, "1.1.0"); err != nil {
		t.Fatalf("next launch did not load the new program: %v", err)
	}
	if _, err := io.WriteString(in, "still running\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := reader.ReadString('\n'); strings.TrimSpace(line) != "still running" || err != nil {
		t.Fatalf("updating interrupted the old process: %q, %v", line, err)
	}
}

func waitForUpdate(t *testing.T, service *Service, match func(View) bool) View {
	t.Helper()
	deadline, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		view := service.View()
		if match(view) {
			return view
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for update: %+v", view)
		case <-ticker.C:
		}
	}
}

func TestInstallReportsProgressAndTruncatedDownload(t *testing.T) {
	t.Parallel()
	name := targetAsset(runtime.GOOS, runtime.GOARCH)
	if name == "" {
		t.Skip("platform has no published updater asset")
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, "program")
	const original = "existing program"
	if err := os.WriteFile(executable, []byte(original), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	body, writer := io.Pipe()
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case releaseAPI + "/latest":
			return response(http.StatusOK, encodedRelease(githubRelease{
				Tag: "v1.1.0", Assets: []asset{{Name: name, Digest: fmt.Sprintf("sha256:%064x", 1), Size: 8}},
			})), nil
		case releaseWeb + "/download/v1.1.0/" + name:
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1, Body: body}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
	})}
	service := New(t.Context(), store, Options{Version: "1.0.0", Executable: executable, Client: client})
	defer func() {
		body.Close()
		writer.Close()
		service.Close(context.Background())
	}()
	if view := service.Check(t.Context()); view.State != "available" {
		t.Fatalf("update unavailable: %+v", view)
	}
	if view, err := service.Install("1.1.0"); err != nil || view.State != "downloading" || view.DownloadPercent != 0 {
		t.Fatalf("could not start download: %+v, %v", view, err)
	}
	go func() { _, _ = io.WriteString(writer, "half") }()
	waitForUpdate(t, service, func(view View) bool {
		return view.State == "downloading" && view.DownloadPercent == 50
	})
	_ = writer.Close()
	failed := waitForUpdate(t, service, func(view View) bool { return view.Failure != "" })
	if failed.State != "available" || failed.Failure != "download" || failed.DownloadPercent != 0 {
		t.Fatalf("truncated download should leave the update available to retry: %+v", failed)
	}
	if data, err := os.ReadFile(executable); err != nil || string(data) != original {
		t.Fatalf("truncated download changed the installed program: %q, %v", data, err)
	}
}

func TestHTTPDownloadFailure(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusServiceUnavailable, nil), nil
	})}
	release := Release{Version: "2.0.0", asset: asset{Name: "program", Size: 8}}
	err := download(t.Context(), client, io.Discard, release, nil)
	if !errors.Is(err, errDownload) || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("expected a classified HTTP error, got %v", err)
	}
}

func TestInvalidDownloadLeavesTheExecutableAlone(t *testing.T) {
	t.Parallel()
	executable := filepath.Join(t.TempDir(), "program")
	const original = "existing program"
	if err := os.WriteFile(executable, []byte(original), 0700); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, []byte("tampered")), nil
	})}
	release := Release{Version: "2.0.0", asset: asset{Name: "program", Digest: fmt.Sprintf("sha256:%064x", 0), Size: 8}}
	if err := install(t.Context(), client, executable, stat, release, nil); !errors.Is(err, errVerification) {
		t.Fatalf("expected verification error, got %v", err)
	}
	if data, err := os.ReadFile(executable); err != nil || string(data) != original {
		t.Fatalf("installation changed after verification failed: %q, %v", data, err)
	}
	if entries, err := os.ReadDir(filepath.Dir(executable)); err != nil || len(entries) != 1 {
		t.Fatalf("staging file leaked: %v, %v", entries, err)
	}
}

func TestOnChangeEmitsMonotonicFinalStateOnFastFailure(t *testing.T) {
	t.Parallel()
	name := targetAsset(runtime.GOOS, runtime.GOARCH)
	if name == "" {
		t.Skip("platform has no published updater asset")
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, "program")
	if err := os.WriteFile(executable, []byte("existing"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case releaseAPI + "/latest":
			return response(http.StatusOK, encodedRelease(githubRelease{
				Tag: "v1.1.0", Assets: []asset{{Name: name, Digest: fmt.Sprintf("sha256:%064x", 1), Size: 8}},
			})), nil
		case releaseWeb + "/download/v1.1.0/" + name:
			return response(http.StatusServiceUnavailable, nil), nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
	})}

	var emittedMu sync.Mutex
	var emitted []View
	service := New(t.Context(), store, Options{
		Version:    "1.0.0",
		Executable: executable,
		Client:     client,
		OnChange: func(view View) {
			emittedMu.Lock()
			emitted = append(emitted, view)
			emittedMu.Unlock()
		},
	})
	defer service.Close(context.Background())

	if view := service.Check(t.Context()); view.State != "available" {
		t.Fatalf("update unavailable: %+v", view)
	}

	for attempt := range 15 {
		emittedMu.Lock()
		emitted = nil
		emittedMu.Unlock()

		if _, err := service.Install("1.1.0"); err != nil {
			t.Fatalf("attempt %d install failed to start: %v", attempt, err)
		}
		final := waitForUpdate(t, service, func(view View) bool {
			return view.State == "available" && view.Failure == "download"
		})
		service.jobs.Wait()

		emittedMu.Lock()
		got := append([]View(nil), emitted...)
		emittedMu.Unlock()

		if len(got) == 0 {
			t.Fatalf("attempt %d emitted no OnChange views", attempt)
		}
		last := got[len(got)-1]
		if last.State != final.State || last.Failure != final.Failure || last.DownloadPercent != final.DownloadPercent {
			t.Fatalf("attempt %d stale OnChange overwrote final state: last=%+v want=%+v sequence=%+v", attempt, last, final, got)
		}
	}
}
