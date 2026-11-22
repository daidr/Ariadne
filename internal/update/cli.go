package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

// DefaultChannel selects "stable" for release versions and "preview" for dev or prerelease versions.
func DefaultChannel(currentVersion string) string {
	tag := versionTag(currentVersion)
	if tag == "" || semver.Prerelease(tag) != "" {
		return "preview"
	}
	return "stable"
}

// CompareVersions compares two Ariadne version strings using semantic versioning.
// Non-semver strings (such as "dev") sort older than any valid semantic version.
func CompareVersions(a, b string) int {
	ta, tb := versionTag(a), versionTag(b)
	switch {
	case ta == "" && tb == "":
		return strings.Compare(a, b)
	case ta == "":
		return -1
	case tb == "":
		return 1
	default:
		return semver.Compare(ta, tb)
	}
}

// CheckLatest fetches the newest release on the requested channel ("stable" or "preview").
func CheckLatest(ctx context.Context, base *http.Client, channel string) (*Release, error) {
	if channel != "stable" && channel != "preview" {
		return nil, fmt.Errorf("unknown release channel %q (expected stable or preview)", channel)
	}
	return readReleases(ctx, releaseClient(base, false), channel)
}

// CheckLatestWithFallback checks "stable" first when channel is "" or "stable",
// falling back to "preview" if no stable release exists yet.
func CheckLatestWithFallback(ctx context.Context, base *http.Client, channel string) (*Release, string, error) {
	if channel == "preview" {
		rel, err := CheckLatest(ctx, base, "preview")
		return rel, "preview", err
	}
	rel, err := CheckLatest(ctx, base, "stable")
	if err != nil {
		return nil, "stable", err
	}
	if rel != nil {
		return rel, "stable", nil
	}
	rel, err = CheckLatest(ctx, base, "preview")
	return rel, "preview", err
}

// FindRelease fetches metadata for a specific release tag (with or without leading "v").
func FindRelease(ctx context.Context, base *http.Client, targetVersion string) (*Release, error) {
	tag := versionTag(targetVersion)
	if tag == "" {
		return nil, fmt.Errorf("invalid version %q", targetVersion)
	}
	var item githubRelease
	found, _, err := readReleasePage(ctx, releaseClient(base, false), releaseAPI+"/tags/"+url.PathEscape(tag), &item, true)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("release %s was not found", tag)
	}
	rel := item.release(false)
	if rel == nil {
		return nil, fmt.Errorf("release %s is a draft or has an invalid tag", tag)
	}
	return rel, nil
}

// InstallExecutable downloads, verifies (SHA-256 + --version + self-check), and
// atomically replaces the given Ariadne executable on disk.
func InstallExecutable(ctx context.Context, base *http.Client, executable string, release Release, progress func(int)) error {
	if executable == "" {
		return errors.New("executable path is required")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	stat, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", resolved)
	}
	cleanPrevious(resolved)
	return install(ctx, releaseClient(base, true), resolved, stat, release, progress)
}
