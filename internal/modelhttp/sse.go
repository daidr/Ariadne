// Package modelhttp contains bounded transport primitives shared by model APIs.
package modelhttp

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const MaxEventBytes = 16 * 1024 * 1024

var ErrSSELineLimit = errors.New("model SSE line exceeds size limit")
var ErrSSEEventLimit = errors.New("model SSE event exceeds size limit")

type Event struct {
	Name    string
	Data    string
	Comment bool
}

// ReadSSE accepts CRLF, comments, split UTF-8 sequences and a final data field
// without a trailing newline. It bounds both individual lines and whole events.
func ReadSSE(body io.Reader, yield func(Event) bool) error {
	return ReadSSELimit(body, MaxEventBytes, yield)
}

// ReadSSELimit lets non-model protocols apply their own frame limit while
// preserving the default model API's smaller bound.
func ReadSSELimit(body io.Reader, limit int, yield func(Event) bool) error {
	if limit <= 0 {
		limit = MaxEventBytes
	}
	reader := bufio.NewReader(body)
	name := ""
	data := make([]string, 0)
	size := 0
	flush := func() bool {
		keep := true
		if len(data) > 0 {
			keep = yield(Event{Name: name, Data: strings.Join(data, "\n")})
		}
		name = ""
		data = data[:0]
		size = 0
		return keep
	}
	for {
		line := make([]byte, 0)
		var err error
		for {
			var piece []byte
			piece, err = reader.ReadSlice('\n')
			if len(line)+len(piece) > limit {
				return ErrSSELineLimit
			}
			line = append(line, piece...)
			if !errors.Is(err, bufio.ErrBufferFull) {
				break
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) > 0 {
			text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
			switch {
			case text == "":
				if !flush() {
					return nil
				}
			case strings.HasPrefix(text, ":"):
				if !yield(Event{Comment: true}) {
					return nil
				}
			case strings.HasPrefix(text, "event:"):
				name = strings.TrimSpace(strings.TrimPrefix(text, "event:"))
			case strings.HasPrefix(text, "data:"):
				value := strings.TrimPrefix(strings.TrimPrefix(text, "data:"), " ")
				size += len(value) + 1
				if size > limit {
					return ErrSSEEventLimit
				}
				data = append(data, value)
			}
		}
		if errors.Is(err, io.EOF) {
			flush()
			return nil
		}
	}
}
