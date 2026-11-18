package hostinghttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Tail consumes a bounded stream while retaining only the last UTF-16 units,
// matching JavaScript log truncation without buffering a complete job log.
func Tail(input io.Reader, limit int) (string, error) {
	if limit <= 0 || limit > 1_000_000 {
		return "", errors.New("invalid repository log limit")
	}
	const maxBytes = 256 * 1024 * 1024
	limited := &io.LimitedReader{R: input, N: maxBytes + 1}
	reader := bufio.NewReader(transform.NewReader(limited, unicode.UTF8.NewDecoder()))
	units := make([]uint16, limit)
	total, next := 0, 0
	put := func(unit uint16) { units[next] = unit; next = (next + 1) % limit; total++ }
	first := true
	for {
		r, _, err := reader.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if limited.N == 0 {
			return "", errors.New("repository log exceeds 256 MiB")
		}
		if first {
			first = false
			if r == '\ufeff' {
				continue
			}
		}
		if r > 0xffff {
			high, low := utf16.EncodeRune(r)
			put(uint16(high))
			put(uint16(low))
		} else {
			put(uint16(r))
		}
	}
	if total <= limit {
		return string(utf16.Decode(units[:total])), nil
	}
	ordered := append(append(make([]uint16, 0, limit), units[next:]...), units[:next]...)
	return fmt.Sprintf("[… %d characters cut …]\n%s", total-limit, string(utf16.Decode(ordered))), nil
}
