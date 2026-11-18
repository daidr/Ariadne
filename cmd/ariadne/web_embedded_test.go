//go:build ariadne_web

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/config"
)

func TestEmbeddedWebServesFromAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ARIADNE_WEB", "")
	t.Setenv("PATH", dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := config.Config{Home: dir, DatabasePath: filepath.Join(dir, "ariadne.sqlite"), Token: "embedded-test-token"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, listener) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("native server did not shut down")
		}
	}()
	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	get := func(path string) (string, int, error) {
		response, err := client.Get(base + path)
		if err != nil {
			return "", 0, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return string(data), response.StatusCode, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, status, err := get("/api/health"); err == nil && status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	page, status, err := get("/")
	if err != nil || status != 200 {
		t.Fatal(status, err)
	}
	asset := regexp.MustCompile(`/assets/[^" ]+\.js`).FindString(page)
	if asset == "" {
		t.Fatal("embedded frontend did not reference its bundled JavaScript")
	}
	if body, status, err := get(asset); err != nil || status != 200 || len(body) < 1000 || strings.Contains(body[:min(100, len(body))], "<!doctype") {
		t.Fatal("embedded frontend asset missing", status, err)
	}
	if _, status, err := get("/api/projects"); err != nil || status != 401 {
		t.Fatal("embedded UI server changed API auth", status, err)
	}
	if _, err := os.Stat("ariadne.sqlite"); err != nil {
		t.Fatal("native database was not opened in its isolated home", err)
	}
}
