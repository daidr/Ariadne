package modelhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func RetryAfter(response *http.Response, now time.Time) *int64 {
	header := response.Header.Get("Retry-After")
	if header == "" {
		return nil
	}
	var delay int64
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		if seconds >= math.MaxInt64/1000 {
			delay = math.MaxInt64
		} else {
			delay = int64(math.Max(0, seconds*1000))
		}
	} else if at, err := http.ParseTime(header); err == nil {
		delay = max(0, at.Sub(now).Milliseconds())
	} else {
		return nil
	}
	return &delay
}

func HTTPError(response *http.Response, now time.Time) sdk.Part {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	text := ""
	if json.Unmarshal(data, &body) == nil && body.Error.Message != "" {
		text = body.Error.Message
	} else {
		text = string(data)
		if len(text) > 300 {
			text = text[:300]
		}
	}
	message := fmt.Sprintf("HTTP %d", response.StatusCode)
	if text != "" {
		message += ": " + text
	}
	part := sdk.Part{Type: "finish", Reason: "error", Error: message}
	switch response.StatusCode {
	case 429, 500, 502, 503, 504, 529:
		part.Busy = true
	}
	after := RetryAfter(response, now)
	if after != nil && *after > 60000 {
		part.Error += fmt.Sprintf(" (the service asks to wait %.0f min before trying again)", math.Ceil(float64(*after)/60000))
		return part
	}
	switch response.StatusCode {
	case 408, 409, 429, 500, 502, 503, 504, 529:
		part.Retryable = true
	}
	part.RetryAfterMS = after
	return part
}

func TransportError(ctx context.Context, err error, midStream bool) *sdk.Part {
	if ctx.Err() != nil {
		return nil
	}
	var timeout net.Error
	passing := midStream || (errors.As(err, &timeout) && timeout.Timeout()) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNABORTED)
	var address *url.Error
	if errors.As(err, &address) {
		err = address.Err
	}
	return &sdk.Part{Type: "finish", Reason: "error", Error: err.Error(), Retryable: passing}
}

func Incomplete() sdk.Part {
	return sdk.Part{Type: "finish", Reason: "error", Error: "the response stream ended before it was complete", Retryable: true}
}

func RefusalText(status int, data []byte) string {
	detail := strings.TrimSpace(string(data))
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) == nil {
		var message string
		if json.Unmarshal(body["error"], &message) != nil {
			var failure struct {
				Message *string `json:"message"`
			}
			if json.Unmarshal(body["error"], &failure) == nil && failure.Message != nil {
				message = *failure.Message
			} else {
				_ = json.Unmarshal(body["message"], &message)
			}
		}
		if strings.TrimSpace(message) != "" {
			detail = strings.TrimSpace(message)
		}
	}
	runes := []rune(detail)
	if len(runes) > 300 {
		detail = string(runes[:300])
	}
	if detail == "" {
		return fmt.Sprint(status)
	}
	return fmt.Sprintf("%d %s", status, detail)
}
