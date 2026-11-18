// Package hostinghttp provides bounded, cancellable REST calls for repository
// hosts. Connections own their client and must close it when unloaded.
package hostinghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/secrets"
)

const MaxResponse = 16 * 1024 * 1024

type Error struct {
	Status                     int
	Reason, Name, Method, Path string
	Message                    string
}

func (err *Error) Error() string {
	if err.Message != "" {
		return err.Message
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s: %d %s", err.Name, err.Method, err.Path, err.Status, err.Reason))
}

type Options struct {
	Name, Base     string
	Client         *http.Client
	Token          func(context.Context) (*string, error)
	Headers        http.Header
	Authorize      func(http.Header, string)
	ErrorMessage   func([]byte) string
	RejectRedirect bool
}
type Client struct {
	options Options
	base    *url.URL
	http    http.Client
	mu      sync.Mutex
	life    context.Context
	cancel  context.CancelFunc
	jobs    sync.WaitGroup
	active  int
	closed  bool
	done    chan struct{}
}

func New(options Options) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(options.Base, "/"))
	if err != nil || base == nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("repository API requires an http or https URL without credentials, query or fragment")
	}
	life, cancel := context.WithCancel(context.Background())
	client := &Client{options: options, base: base, life: life, cancel: cancel, done: make(chan struct{})}
	if options.Client != nil {
		client.http = *options.Client
	}
	client.http.Jar = nil
	client.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.http.Timeout <= 0 || client.http.Timeout > 30*time.Second {
		client.http.Timeout = 30 * time.Second
	}
	client.options.Headers = options.Headers.Clone()
	return client, nil
}
func (client *Client) begin(ctx context.Context) (context.Context, func(), error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed {
		return nil, nil, errors.New("repository API client is closed")
	}
	if client.active >= 128 {
		return nil, nil, errors.New("too many repository API requests")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	client.active++
	client.jobs.Add(1)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(client.life, cancel)
	return ctx, func() {
		stop()
		cancel()
		client.mu.Lock()
		client.active--
		client.mu.Unlock()
		client.jobs.Done()
	}, nil
}
func (client *Client) Close() error {
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		<-client.done
		return nil
	}
	client.closed = true
	client.cancel()
	client.mu.Unlock()
	client.jobs.Wait()
	client.options = Options{Name: client.options.Name}
	client.http = http.Client{}
	close(client.done)
	return nil
}
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
func (client *Client) Request(ctx context.Context, method, path string, body, out any) error {
	return client.Read(ctx, method, path, body, false, func(reader io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(reader, MaxResponse+1))
		if err != nil {
			return err
		}
		if len(data) > MaxResponse {
			return errors.New("repository API response exceeds 16 MiB")
		}
		if out == nil {
			return nil
		}
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("invalid repository API JSON response")
		}
		return nil
	})
}

// Read allows external redirects only for a log download. Once a redirect
// leaves the API origin, none of its later requests receive credentials.
func (client *Client) Read(ctx context.Context, method, path string, body any, download bool, consume func(io.Reader) error) error {
	ctx, done, err := client.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("invalid repository API path")
	}
	address, err := url.Parse(client.base.String() + path)
	if err != nil || address.Fragment != "" || !sameOrigin(address, client.base) {
		return errors.New("invalid repository API path")
	}
	var encoded []byte
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
		if len(encoded) > MaxResponse {
			return errors.New("repository API request exceeds 16 MiB")
		}
	}
	var private []string
	if client.options.Token != nil {
		token, err := client.options.Token(ctx)
		if err != nil {
			return err
		}
		if token != nil && *token != "" {
			private = []string{*token}
		}
	}
	credentialed := true
	for redirects := 0; ; redirects++ {
		request, err := http.NewRequestWithContext(ctx, method, address.String(), bytes.NewReader(encoded))
		if err != nil {
			return errors.New("invalid repository API request")
		}
		request.Header = client.options.Headers.Clone()
		if request.Header == nil {
			request.Header = make(http.Header)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if credentialed && len(private) != 0 && client.options.Authorize != nil {
			client.options.Authorize(request.Header, private[0])
		}
		if !credentialed {
			request.Header.Del("Authorization")
			request.Header.Del("Private-Token")
			request.Header.Del("Cookie")
		}
		response, err := client.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				err = urlErr.Err
			}
			return fmt.Errorf("%s request failed: %s", client.options.Name, secrets.Redact(err.Error(), private))
		}
		if response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308 {
			if client.options.RejectRedirect {
				response.Body.Close()
				return &Error{Name: client.options.Name, Method: method, Path: path, Status: response.StatusCode, Reason: "redirect refused; check the instance and repository settings"}
			}
			location, err := response.Location()
			response.Body.Close()
			if err != nil || redirects >= 5 {
				return errors.New("invalid or excessive repository API redirects")
			}
			if location.User != nil || location.Scheme != "http" && location.Scheme != "https" || address.Scheme == "https" && location.Scheme != "https" {
				return errors.New("unsafe repository API redirect")
			}
			if !sameOrigin(location, client.base) {
				if !download || method != http.MethodGet {
					return errors.New("repository API redirect leaves its origin")
				}
				credentialed = false
			}
			// A redirected write may be repeated only when the server preserves its
			// method. Do not turn an unexecuted POST into a successful page GET.
			if method != http.MethodGet && method != http.MethodHead && response.StatusCode != 307 && response.StatusCode != 308 {
				return errors.New("repository API write redirect does not preserve its method")
			}
			address = location
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024))
			response.Body.Close()
			if readErr != nil {
				return readErr
			}
			reason := strings.TrimSpace(string(data))
			if client.options.ErrorMessage != nil {
				reason = client.options.ErrorMessage(data)
			}
			if reason == "" {
				reason = http.StatusText(response.StatusCode)
			}
			return &Error{Status: response.StatusCode, Reason: secrets.Redact(reason, private), Name: client.options.Name, Method: method, Path: path}
		}
		err = consume(response.Body)
		response.Body.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}
