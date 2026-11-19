package update

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	releaseAPI   = "https://api.github.com/repos/daidr/Ariadne/releases"
	releaseWeb   = "https://github.com/daidr/Ariadne/releases"
	maxAssetSize = 128 << 20
)

type Release struct {
	Version     string `json:"version"`
	Notes       string `json:"notes"`
	URL         string `json:"url"`
	Prerelease  bool   `json:"prerelease"`
	Installable bool   `json:"installable"`
	asset       asset
}

type asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type githubRelease struct {
	Tag        string  `json:"tag_name"`
	Body       string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

func versionTag(version string) string {
	tag := "v" + strings.TrimPrefix(version, "v")
	core := tag
	if index := strings.IndexAny(tag, "-+"); index >= 0 {
		core = tag[:index]
	}
	if len(tag) > 120 || strings.Count(core, ".") != 2 || !semver.IsValid(tag) {
		return ""
	}
	return tag
}

func targetAsset(goos, arch string) string {
	switch goos + "/" + arch {
	case "windows/amd64":
		return "ariadne-windows-x64.exe"
	case "linux/amd64", "darwin/amd64":
		return "ariadne-" + goos + "-x64"
	case "linux/arm64", "darwin/arm64":
		return "ariadne-" + goos + "-arm64"
	default:
		return ""
	}
}

func (item asset) valid() bool {
	hash, ok := strings.CutPrefix(item.Digest, "sha256:")
	_, err := hex.DecodeString(hash)
	return ok && len(hash) == 64 && err == nil && item.Size > 0 && item.Size <= maxAssetSize
}

func (item githubRelease) release(stable bool) *Release {
	tag := versionTag(item.Tag)
	if tag == "" || tag != item.Tag || item.Draft {
		return nil
	}
	prerelease := item.Prerelease || semver.Prerelease(tag) != ""
	if stable && prerelease {
		return nil
	}
	value := &Release{
		Version: strings.TrimPrefix(tag, "v"), URL: releaseWeb + "/tag/" + url.PathEscape(tag),
		Notes: strings.ToValidUTF8(item.Body[:min(len(item.Body), 64<<10)], ""), Prerelease: prerelease,
	}
	name := targetAsset(runtime.GOOS, runtime.GOARCH)
	for _, item := range item.Assets {
		if item.Name == name && item.valid() {
			value.asset, value.Installable = item, true
			break
		}
	}
	return value
}

func releaseRequest(ctx context.Context, client *http.Client, address string, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "Ariadne")
	response, err := client.Do(req)
	if err != nil {
		// Redirected asset URLs may contain temporary access parameters.
		var failure *url.Error
		if errors.As(err, &failure) {
			return nil, failure.Err
		}
	}
	return response, err
}

func readReleases(ctx context.Context, client *http.Client, channel string) (*Release, error) {
	if channel == "stable" {
		var item githubRelease
		found, _, err := readReleasePage(ctx, client, releaseAPI+"/latest", &item, true)
		if err != nil || !found {
			return nil, err
		}
		return item.release(true), nil
	}
	var newest *Release
	for page := 1; ; page++ {
		var items []githubRelease
		_, more, err := readReleasePage(ctx, client, fmt.Sprintf("%s?per_page=30&page=%d", releaseAPI, page), &items, false)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			current := item.release(false)
			if current != nil && (newest == nil || semver.Compare(versionTag(current.Version), versionTag(newest.Version)) > 0) {
				newest = current
			}
		}
		// The newest semantic version among recent releases is the update
		// candidate. Continue through older pages only if there was no valid tag.
		if newest != nil || !more {
			return newest, nil
		}
	}
}

func readReleasePage(ctx context.Context, client *http.Client, address string, result any, allowMissing bool) (bool, bool, error) {
	response, err := releaseRequest(ctx, client, address, "application/vnd.github+json")
	if err != nil {
		return false, false, err
	}
	defer response.Body.Close()
	if allowMissing && response.StatusCode == http.StatusNotFound {
		return false, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, false, fmt.Errorf("GitHub release metadata returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil {
		return false, false, err
	}
	if len(body) > 4<<20 {
		return false, false, errors.New("GitHub release metadata exceeds the size limit")
	}
	err = json.Unmarshal(body, result)
	return err == nil, strings.Contains(response.Header.Get("Link"), `rel="next"`), err
}

func releaseClient(base *http.Client, download bool) *http.Client {
	client := &http.Client{}
	if base != nil {
		client.Transport = base.Transport
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		host := req.URL.Host
		if download && len(via) <= 4 && req.URL.Scheme == "https" && req.URL.User == nil &&
			(host == "release-assets.githubusercontent.com" || host == "objects.githubusercontent.com") {
			return nil
		}
		return errors.New("unexpected GitHub release redirect")
	}
	return client
}
