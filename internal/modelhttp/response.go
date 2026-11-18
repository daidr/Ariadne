package modelhttp

import (
	"errors"
	"fmt"
	"io"
)

func ReadResponse(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 16*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16*1024*1024 {
		return nil, errors.New("model response exceeds size limit")
	}
	return data, nil
}

type SearchRefused struct{ Status int }

func (err *SearchRefused) Error() string { return fmt.Sprintf("HTTP %d", err.Status) }
func SearchHTTPError(status int) error {
	if status >= 400 && status < 500 && status != 408 && status != 429 {
		return &SearchRefused{Status: status}
	}
	return fmt.Errorf("HTTP %d", status)
}
