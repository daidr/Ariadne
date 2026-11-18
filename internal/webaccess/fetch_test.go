package webaccess

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicDialChecksEveryAddressAndDialsCheckedIP(t *testing.T) {
	public := netip.MustParseAddr("93.184.216.34")
	private := netip.MustParseAddr("192.168.0.1")
	for _, addresses := range [][]netip.Addr{{public, private}, {public}} {
		var resolved, dialed []string
		dial := publicDial(func(_ context.Context, _, host string) ([]netip.Addr, error) {
			resolved = append(resolved, host)
			return addresses, nil
		}, func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("no connection in test")
		})
		_, _ = dial(t.Context(), "tcp", "example.com:443")
		if strings.Join(resolved, ",") != "example.com" {
			t.Fatalf("DNS calls = %v", resolved)
		}
		if len(addresses) == 2 && len(dialed) != 0 {
			t.Fatalf("mixed private/public DNS answer was used: %v", dialed)
		}
		if len(addresses) == 1 && strings.Join(dialed, ",") != "93.184.216.34:443" {
			t.Fatalf("dial used %v", dialed)
		}
	}
}

type fetchTransport func(*http.Request) (*http.Response, error)

func (transport fetchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestFetchStopsBeforePrivateRedirect(t *testing.T) {
	var visited []string
	transport := fetchTransport(func(request *http.Request) (*http.Response, error) {
		visited = append(visited, request.URL.Hostname())
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://127.0.0.1/"}},
			Request: request, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	_, err := fetch(t.Context(), "https://example.com", transport)
	if err == nil || !strings.Contains(err.Error(), "public network") || strings.Join(visited, ",") != "example.com" {
		t.Fatalf("redirect error = %v, requests = %v", err, visited)
	}
}
