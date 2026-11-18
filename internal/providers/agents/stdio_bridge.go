package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/modelhttp"
)

const StdioBridgeCommand = "mcp-stdio"

// RunStdioBridge is the native CLI's mcp-stdio mode. Calls run concurrently so
// a tool awaiting approval never blocks a ping. Input is owned for this call;
// cancellation closes it when it implements io.Closer (including CLI stdin).
func RunStdioBridge(ctx context.Context, address string, input io.Reader, output io.Writer) error {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !strings.HasPrefix(endpoint.Path, "/mcp/") {
		return errors.New("stdio bridge requires a local agent tool URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxResponseHeaderBytes = 64 << 10
	transport.MaxIdleConnsPerHost = 128
	transport.DialContext = (&net.Dialer{Timeout: 5e9}).DialContext
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if closer, ok := input.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
	var writes sync.Mutex
	write := func(data []byte) error {
		if len(data) > maxBridgeResult {
			return errors.New("stdio bridge response exceeded its size limit")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return errors.New("tool bridge returned invalid JSON")
		}
		data = compact.Bytes()
		writes.Lock()
		defer writes.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := output.Write(append(data, '\n')); err != nil {
			cancel()
			return err
		}
		return nil
	}
	refuse := func(id json.RawMessage, err error) {
		if len(id) == 0 || string(id) == "null" {
			return
		}
		data, _ := jsonutil.Encode(bridgeError(id, -32000, err.Error()))
		_ = write(data)
	}
	forward := func(line []byte, id json.RawMessage) {
		request, err := http.NewRequestWithContext(ctx, "POST", address, bytes.NewReader(line))
		if err != nil {
			refuse(id, err)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response, err := client.Do(request)
		if err != nil {
			refuse(id, err)
			return
		}
		defer response.Body.Close()
		if response.StatusCode == 202 {
			return
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			refuse(id, fmt.Errorf("tool bridge returned HTTP %d", response.StatusCode))
			return
		}
		if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
			var writeErr error
			err = modelhttp.ReadSSELimit(response.Body, maxBridgeResult, func(event modelhttp.Event) bool {
				if event.Comment || event.Data == "" {
					return true
				}
				writeErr = write([]byte(event.Data))
				return writeErr == nil
			})
			if err != nil {
				refuse(id, err)
			}
			return
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxBridgeResult+1))
		if err != nil {
			refuse(id, err)
			return
		}
		data = bytes.TrimSpace(data)
		if len(data) > maxBridgeResult {
			refuse(id, errors.New("tool bridge response exceeded its size limit"))
			return
		}
		if len(data) > 0 {
			_ = write(data)
		}
	}
	active := make(chan struct{}, 128)
	var calls sync.WaitGroup
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxBridgeResult)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &message) != nil {
			continue
		}
		select {
		case active <- struct{}{}:
		case <-ctx.Done():
			calls.Wait()
			return ctx.Err()
		default:
			refuse(message.ID, errors.New("too many active stdio bridge requests"))
			continue
		}
		owned := append([]byte{}, line...)
		calls.Add(1)
		go func() { defer calls.Done(); defer func() { <-active }(); forward(owned, message.ID) }()
	}
	readErr := scanner.Err()
	if readErr != nil {
		cancel()
	}
	calls.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return readErr
}
