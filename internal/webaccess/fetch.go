// Package webaccess reads public web pages for model tools.
package webaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxBody = 5 << 20

var slots = make(chan struct{}, 16)

var privatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func PublicIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.Zone() != "" {
		return false
	}
	if netip.MustParsePrefix("64:ff9b::/96").Contains(address) {
		octets := address.As16()
		return PublicIP(netip.AddrFrom4([4]byte(octets[12:])))
	}
	for _, reserved := range privatePrefixes {
		if reserved.Contains(address) {
			return false
		}
	}
	return true
}

func publicURL(address *url.URL) error {
	if (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" || address.User != nil || address.Opaque != "" {
		return errors.New("an HTTP or HTTPS URL without credentials is required")
	}
	if ip, err := netip.ParseAddr(address.Hostname()); err == nil && !PublicIP(ip) {
		return errors.New("only public network addresses can be fetched")
	}
	return nil
}

type lookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

func publicDial(lookup lookupFunc, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var addresses []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			addresses = []netip.Addr{ip}
		} else {
			addresses, err = lookup(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("the host has no addresses")
		}
		for _, ip := range addresses {
			if !PublicIP(ip) {
				return nil, errors.New("the host resolves to a local or private network")
			}
		}
		// Use the checked addresses directly. Keep the original URL for Host,
		// TLS server name and certificate verification.
		var last error
		for _, ip := range addresses[:min(len(addresses), 16)] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
}

type Page struct {
	URL, Type, Charset string
	Body               []byte
	Status             int
	Truncated          bool
}

func Fetch(ctx context.Context, address string) (Page, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext:       publicDial(net.DefaultResolver.LookupNetIP, dialer.DialContext),
		ForceAttemptHTTP2: true, TLSHandshakeTimeout: 8 * time.Second,
		DisableKeepAlives: true, MaxResponseHeaderBytes: 1 << 20,
	}
	defer transport.CloseIdleConnections()
	return fetch(ctx, address, transport)
}

func fetch(ctx context.Context, address string, transport http.RoundTripper) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return Page{}, ctx.Err()
	}
	target, err := url.Parse(address)
	if err != nil {
		return Page{}, errors.New("invalid URL")
	}
	if err := publicURL(target); err != nil {
		return Page{}, err
	}
	target.Fragment = ""
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		req.Header.Del("Referer")
		return publicURL(req.URL)
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Page{}, err
	}
	request.Header.Set("User-Agent", "Ariadne")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.8")
	response, err := client.Do(request)
	if err != nil {
		return Page{}, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return Page{}, fmt.Errorf("read web response: %w", err)
	}
	media, params, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if media == "" {
		media, _, _ = mime.ParseMediaType(http.DetectContentType(content))
	}
	return Page{URL: response.Request.URL.String(), Type: strings.ToLower(media), Charset: params["charset"],
		Status: response.StatusCode, Body: content[:min(len(content), maxBody)], Truncated: len(content) > maxBody}, nil
}
