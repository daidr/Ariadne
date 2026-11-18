package browser

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type locator struct {
	home   string
	mu     sync.Mutex
	active *download
}

type download struct {
	done    chan struct{}
	stop    context.CancelFunc
	waiters int
	err     error
}

func installed() string {
	if name := os.Getenv("ARIADNE_BROWSER_EXECUTABLE"); name != "" {
		if executable, err := exec.LookPath(name); err == nil {
			return executable
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "chrome", "msedge"} {
		if executable, err := exec.LookPath(name); err == nil {
			return executable
		}
	}
	var locations []string
	switch runtime.GOOS {
	case "windows":
		for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if root := os.Getenv(name); root != "" {
				locations = append(locations,
					filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
					filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
					filepath.Join(root, "Chromium", "Application", "chrome.exe"))
			}
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			for _, name := range []string{"Google Chrome", "Microsoft Edge", "Chromium"} {
				locations = append(locations, filepath.Join(root, name+".app", "Contents", "MacOS", name))
			}
		}
	}
	for _, location := range locations {
		if regular(location) {
			return location
		}
	}
	return ""
}

func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func platformName() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "win64"
	case "linux/amd64":
		return "linux64"
	case "darwin/amd64":
		return "mac-x64"
	case "darwin/arm64":
		return "mac-arm64"
	}
	return ""
}

func (locator *locator) find(ctx context.Context) (string, error) {
	if bin := installed(); bin != "" {
		return bin, nil
	}
	platform := platformName()
	if platform == "" {
		return "", errors.New("install Chrome, Edge or Chromium to use the browser tool on this platform")
	}
	folder := "chrome-headless-shell-" + platform
	bin := "chrome-headless-shell"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	executable := filepath.Join(locator.home, folder, bin)
	if regular(executable) {
		return executable, nil
	}
	locator.mu.Lock()
	attempt := locator.active
	if attempt == nil {
		job, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		attempt = &download{done: make(chan struct{}), stop: cancel}
		locator.active = attempt
		go func() {
			defer cancel()
			err := downloadChrome(job, locator.home, platform, folder, bin)
			locator.mu.Lock()
			attempt.err = err
			if locator.active == attempt {
				locator.active = nil
			}
			close(attempt.done)
			locator.mu.Unlock()
		}()
	}
	attempt.waiters++
	locator.mu.Unlock()
	defer func() {
		locator.mu.Lock()
		attempt.waiters--
		if attempt.waiters == 0 {
			attempt.stop()
		}
		locator.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-attempt.done:
		return executable, attempt.err
	}
}

func downloadChrome(ctx context.Context, home, platform, folder, bin string) error {
	const manifest = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	validOrigin := func(address *url.URL) bool {
		return address.Scheme == "https" && address.User == nil && (address.Host == "googlechromelabs.github.io" || address.Host == "storage.googleapis.com")
	}
	client := http.Client{CheckRedirect: func(req *http.Request, previous []*http.Request) error {
		if len(previous) > 3 || !validOrigin(req.URL) {
			return errors.New("unexpected Chrome download redirect")
		}
		return nil
	}}
	get := func(address string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil || !validOrigin(req.URL) {
			return nil, errors.New("invalid Chrome download URL")
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("Chrome download HTTP %d", response.StatusCode)
		}
		return response.Body, nil
	}
	listing, err := get(manifest)
	if err != nil {
		return err
	}
	var data struct {
		Channels map[string]struct {
			Downloads map[string][]struct{ Platform, URL string }
		}
	}
	err = json.NewDecoder(io.LimitReader(listing, 2<<20)).Decode(&data)
	listing.Close()
	if err != nil {
		return err
	}
	address := ""
	for _, entry := range data.Channels["Stable"].Downloads["chrome-headless-shell"] {
		if entry.Platform == platform {
			address = entry.URL
			break
		}
	}
	if address == "" {
		return fmt.Errorf("Chrome does not list a download for %s", platform)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(home, "download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "browser.zip")
	body, err := get(address)
	if err != nil {
		return err
	}
	defer body.Close()
	output, err := os.Create(archive)
	if err != nil {
		return err
	}
	const maxDownload = 384 << 20
	size, copyErr := io.Copy(output, io.LimitReader(body, maxDownload+1))
	if err := errors.Join(copyErr, output.Close()); err != nil {
		return err
	}
	if size > maxDownload {
		return errors.New("Chrome archive is too large")
	}
	if err := unpack(ctx, stage, archive, folder); err != nil {
		return err
	}
	if !regular(filepath.Join(stage, folder, bin)) {
		return errors.New("Chrome archive has no executable")
	}
	if err := os.Rename(filepath.Join(stage, folder), filepath.Join(home, folder)); err != nil && !regular(filepath.Join(home, folder, bin)) {
		return err
	}
	return nil
}

func unpack(ctx context.Context, dest, source, folder string) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer archive.Close()
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	var total uint64
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || name != folder && !strings.HasPrefix(name, folder+"/") {
			return errors.New("invalid Chrome archive path")
		}
		mode := entry.Mode()
		if mode.IsDir() {
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() || entry.UncompressedSize64 > 1<<30-total {
			return errors.New("invalid Chrome archive entry")
		}
		total += entry.UncompressedSize64
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		if err := unpackFile(root, entry, name, mode.Perm()|0o600); err != nil {
			return err
		}
	}
	return nil
}

func unpackFile(root *os.Root, entry *zip.File, name string, mode fs.FileMode) error {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	count, err := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
	if err == nil && count != int64(entry.UncompressedSize64) {
		err = errors.New("Chrome archive entry has an inconsistent size")
	}
	return errors.Join(err, output.Close())
}
