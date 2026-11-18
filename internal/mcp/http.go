package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/modelhttp"
)

type HTTPOptions struct {
	URL     string
	Headers map[string]string
	Client  *http.Client
}
type HTTPTransport struct {
	mu                                               sync.Mutex
	options                                          HTTPOptions
	client                                           http.Client
	ctx                                              context.Context
	cancel                                           context.CancelFunc
	take                                             func([]byte) error
	end                                              func(error)
	version, session                                 string
	started, closing, ended, sessionEnded, listening bool
	requests                                         map[string]context.CancelFunc
	active                                           chan struct{}
	bytes                                            int
	workers                                          sync.WaitGroup
	closeOnce                                        sync.Once
	closeDone                                        chan struct{}
}

func NewHTTP(options HTTPOptions) *HTTPTransport {
	headers := map[string]string{}
	for key, value := range options.Headers {
		headers[key] = value
	}
	options.Headers = headers
	client := http.Client{}
	if options.Client != nil {
		client = *options.Client
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPTransport{options: options, client: client, requests: map[string]context.CancelFunc{}, active: make(chan struct{}, 128), closeDone: make(chan struct{})}
}
func (transport *HTTPTransport) Start(ctx context.Context, take func([]byte) error, end func(error)) error {
	address, err := url.Parse(transport.options.URL)
	if err != nil || address.Host == "" || address.Scheme != "http" && address.Scheme != "https" || address.User != nil {
		return &ConnectionError{Reason: "invalid MCP server URL"}
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.started || transport.closing {
		return &ConnectionError{Reason: "the connection was already started or closed"}
	}
	transport.started = true
	transport.ctx, transport.cancel = context.WithCancel(ctx)
	transport.take, transport.end = take, end
	return nil
}
func (transport *HTTPTransport) headers() http.Header {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	headers := http.Header{}
	for key, value := range transport.options.Headers {
		headers.Set(key, value)
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json, text/event-stream")
	if transport.session != "" {
		headers.Set("Mcp-Session-Id", transport.session)
	}
	if transport.version != "" {
		headers.Set("Mcp-Protocol-Version", transport.version)
	}
	return headers
}
func (transport *HTTPTransport) SessionEnded() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.sessionEnded
}
func (transport *HTTPTransport) fail(err error) {
	transport.mu.Lock()
	if transport.closing || transport.ended {
		transport.mu.Unlock()
		return
	}
	transport.ended = true
	if errors.Is(err, ErrSessionEnded) {
		transport.sessionEnded = true
	}
	callback := transport.end
	transport.end = nil
	transport.cancel()
	transport.mu.Unlock()
	if callback != nil {
		callback(err)
	}
}
func (transport *HTTPTransport) Send(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return &ConnectionError{Reason: "MCP request exceeded its size limit"}
	}
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(data, &message) != nil {
		return &ConnectionError{Reason: "invalid outgoing MCP message"}
	}
	key := ""
	if message.Method != "" && len(message.ID) > 0 && string(message.ID) != "null" {
		key = string(message.ID)
	}
	transport.mu.Lock()
	if !transport.started || transport.closing || transport.ended || transport.ctx.Err() != nil {
		transport.mu.Unlock()
		return &ConnectionError{Reason: "the connection is closed"}
	}
	if len(data) > 64<<20-transport.bytes {
		transport.mu.Unlock()
		return ErrTransportBusy
	}
	select {
	case transport.active <- struct{}{}:
	default:
		transport.mu.Unlock()
		return ErrTransportBusy
	}
	life, cancel := context.WithCancel(transport.ctx)
	if key == "" {
		cancel()
		life, cancel = context.WithTimeout(transport.ctx, 30*time.Second)
	} else {
		transport.requests[key] = cancel
	}
	transport.bytes += len(data)
	transport.workers.Add(1)
	transport.mu.Unlock()
	owned := append([]byte{}, data...)
	work := func() error {
		err := transport.post(life, owned)
		cancel()
		transport.mu.Lock()
		transport.bytes -= len(owned)
		if key != "" {
			delete(transport.requests, key)
		}
		transport.mu.Unlock()
		<-transport.active
		transport.workers.Done()
		if err != nil {
			transport.fail(err)
		}
		return err
	}
	if key == "" {
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		return work()
	}
	go func() { _ = work() }()
	return nil
}
func (transport *HTTPTransport) EndRequest(id int64) {
	transport.mu.Lock()
	cancel := transport.requests[strconv.FormatInt(id, 10)]
	delete(transport.requests, strconv.FormatInt(id, 10))
	transport.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (transport *HTTPTransport) receive(data []byte) error {
	if !validMessage(data) {
		return nil
	}
	return transport.take(data)
}
func (transport *HTTPTransport) readJSON(body io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(body, MaxMessageBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return &ConnectionError{Reason: "the server answered with too much data"}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	if data[0] == '[' {
		var messages []json.RawMessage
		if json.Unmarshal(data, &messages) != nil {
			return &ConnectionError{Reason: "the server answered with something that is not JSON"}
		}
		for _, message := range messages {
			if err := transport.receive(message); err != nil {
				return err
			}
		}
		return nil
	}
	if !json.Valid(data) {
		return &ConnectionError{Reason: "the server answered with something that is not JSON"}
	}
	return transport.receive(data)
}
func (transport *HTTPTransport) readSSE(body io.Reader) error {
	var receivedErr error
	err := modelhttp.ReadSSELimit(body, MaxMessageBytes, func(event modelhttp.Event) bool {
		if event.Comment || event.Data == "" {
			return true
		}
		receivedErr = transport.receive([]byte(event.Data))
		return receivedErr == nil
	})
	if receivedErr != nil {
		return receivedErr
	}
	if errors.Is(err, modelhttp.ErrSSELineLimit) || errors.Is(err, modelhttp.ErrSSEEventLimit) {
		return &ConnectionError{Reason: "the server answered with too much data"}
	}
	return err
}
func (transport *HTTPTransport) post(ctx context.Context, data []byte) error {
	request, err := http.NewRequestWithContext(ctx, "POST", transport.options.URL, bytes.NewReader(data))
	if err != nil {
		return &ConnectionError{Reason: "invalid MCP request"}
	}
	request.Header = transport.headers()
	response, err := transport.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		address, _ := url.Parse(transport.options.URL)
		return &ConnectionError{Reason: fmt.Sprintf("could not reach %s: %v", address.Host, err)}
	}
	defer response.Body.Close()
	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		if len(session) > 4096 {
			return &ConnectionError{Reason: "invalid MCP session header"}
		}
		for _, char := range session {
			if char < 0x21 || char > 0x7e {
				return &ConnectionError{Reason: "invalid MCP session header"}
			}
		}
		transport.mu.Lock()
		if transport.session == "" {
			transport.session = session
		}
		transport.mu.Unlock()
	}
	if response.StatusCode == 202 {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		transport.mu.Lock()
		hasSession := transport.session != ""
		transport.mu.Unlock()
		if response.StatusCode == 404 && hasSession {
			return ErrSessionEnded
		}
		return &ConnectionError{Reason: fmt.Sprintf("the server answered HTTP %d", response.StatusCode)}
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		err = transport.readSSE(response.Body)
	} else {
		err = transport.readJSON(response.Body)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		var connection *ConnectionError
		if errors.As(err, &connection) {
			return err
		}
		return &ConnectionError{Reason: "the server stream could not be read"}
	}
	return nil
}
func (transport *HTTPTransport) SetProtocolVersion(version string) {
	transport.mu.Lock()
	transport.version = version
	transport.mu.Unlock()
}
func (transport *HTTPTransport) StartNotifications() {
	transport.mu.Lock()
	if !transport.started || transport.closing || transport.ended || transport.listening {
		transport.mu.Unlock()
		return
	}
	transport.listening = true
	transport.workers.Add(1)
	transport.mu.Unlock()
	go func() {
		err := transport.listen()
		transport.workers.Done()
		if err != nil {
			transport.fail(err)
		}
	}()
}
func (transport *HTTPTransport) listen() error {
	delay := time.Second
	for {
		request, err := http.NewRequestWithContext(transport.ctx, "GET", transport.options.URL, nil)
		if err != nil {
			return nil
		}
		request.Header = transport.headers()
		request.Header.Set("Accept", "text/event-stream")
		response, err := transport.client.Do(request)
		if err == nil {
			if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
				response.Body.Close()
				return nil
			}
			readErr := transport.readSSE(response.Body)
			response.Body.Close()
			var protocolError *ConnectionError
			if errors.As(readErr, &protocolError) {
				return readErr
			}
		}
		if transport.ctx.Err() != nil {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-transport.ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}
func (transport *HTTPTransport) Close(ctx context.Context) error {
	transport.closeOnce.Do(func() {
		transport.mu.Lock()
		transport.closing = true
		transport.end = nil
		if transport.cancel != nil {
			transport.cancel()
		}
		session := transport.session
		transport.mu.Unlock()
		go func() {
			defer close(transport.closeDone)
			transport.workers.Wait()
			if session == "" {
				return
			}
			deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(deadline, "DELETE", transport.options.URL, nil)
			if err != nil {
				return
			}
			request.Header = transport.headers()
			if response, err := transport.client.Do(request); err == nil {
				response.Body.Close()
			}
		}()
	})
	select {
	case <-transport.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
