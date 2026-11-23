package update

import (
	"fmt"
	"net/http"
	"testing"
)

func TestDefaultChannelAndCompareVersions(t *testing.T) {
	if got := DefaultChannel("dev"); got != "preview" {
		t.Fatalf("expected preview for dev, got %q", got)
	}
	if got := DefaultChannel("0.1.0-alpha.12"); got != "preview" {
		t.Fatalf("expected preview for alpha, got %q", got)
	}
	if got := DefaultChannel("1.0.0"); got != "stable" {
		t.Fatalf("expected stable for 1.0.0, got %q", got)
	}
	if CompareVersions("dev", "0.1.0-alpha.1") >= 0 {
		t.Fatal("dev should sort older than semver")
	}
	if CompareVersions("0.1.0-alpha.10", "0.1.0-alpha.2") <= 0 {
		t.Fatal("0.1.0-alpha.10 should sort newer than 0.1.0-alpha.2")
	}
	if CompareVersions("v1.2.0", "1.2.0") != 0 {
		t.Fatal("v1.2.0 and 1.2.0 should compare equal")
	}
}

func TestCheckLatestWithFallbackAndFindRelease(t *testing.T) {
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case releaseAPI + "/latest":
			return response(http.StatusNotFound, nil), nil
		case releaseAPI + "?per_page=30&page=1":
			return response(http.StatusOK, encodedRelease([]githubRelease{
				{Tag: "v0.1.0-alpha.14", Prerelease: true},
				{Tag: "v0.1.0-alpha.15", Prerelease: true, Body: "Latest preview"},
			})), nil
		case releaseAPI + "/tags/v0.1.0-alpha.14":
			return response(http.StatusOK, encodedRelease(githubRelease{
				Tag:        "v0.1.0-alpha.14",
				Prerelease: true,
				Body:       "Pinned preview",
			})), nil
		case releaseAPI + "/tags/v9.9.9":
			return response(http.StatusNotFound, nil), nil
		default:
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
	})}

	rel, usedChannel, err := CheckLatestWithFallback(t.Context(), client, "stable")
	if err != nil {
		t.Fatalf("CheckLatestWithFallback: %v", err)
	}
	if usedChannel != "preview" || rel == nil || rel.Version != "0.1.0-alpha.15" {
		t.Fatalf("expected fallback to preview 0.1.0-alpha.15, got channel=%q rel=%+v", usedChannel, rel)
	}

	pinned, err := FindRelease(t.Context(), client, "0.1.0-alpha.14")
	if err != nil {
		t.Fatalf("FindRelease: %v", err)
	}
	if pinned.Version != "0.1.0-alpha.14" || pinned.Notes != "Pinned preview" {
		t.Fatalf("unexpected pinned release: %+v", pinned)
	}

	if _, err := FindRelease(t.Context(), client, "9.9.9"); err == nil {
		t.Fatal("expected missing release to return an error")
	}
}
