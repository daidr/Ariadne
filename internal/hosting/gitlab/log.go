package gitlab

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
)

var sectionMarker = regexp.MustCompile("section_(?:start|end):[0-9]+:[^\r\n]*?\r?\x1b\\[0K")
var ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

func PlainLog(text string) string {
	text = sectionMarker.ReplaceAllString(text, "")
	text = ansi.ReplaceAllString(text, "")
	// A regexp with a captured next character would skip adjacent CRs.
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\r' && (i+1 == len(text) || text[i+1] != '\n') {
			out.WriteByte('\n')
		} else {
			out.WriteByte(text[i])
		}
	}
	return out.String()
}

type plainReader struct {
	input   *bufio.Reader
	pending []byte
	err     error
}

func plainStream(reader io.Reader) io.Reader {
	return &plainReader{input: bufio.NewReaderSize(reader, 4096)}
}
func (reader *plainReader) Read(out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	for len(reader.pending) == 0 && reader.err == nil {
		reader.pending, reader.err = reader.next()
	}
	if len(reader.pending) != 0 {
		n := copy(out, reader.pending)
		reader.pending = reader.pending[n:]
		return n, nil
	}
	return 0, reader.err
}
func (reader *plainReader) next() ([]byte, error) {
	b, err := reader.input.ReadByte()
	if err != nil {
		return nil, err
	}
	if b == '\r' {
		next, _ := reader.input.Peek(1)
		if len(next) == 0 || next[0] != '\n' {
			return []byte{'\n'}, nil
		}
		return []byte{'\r'}, nil
	}
	if b == 0x1b {
		peek, _ := reader.input.Peek(1)
		if len(peek) == 0 || peek[0] != '[' {
			return []byte{b}, nil
		}
		reader.input.Discard(1)
		token := []byte{b, '['}
		for len(token) <= 64*1024 {
			c, err := reader.input.ReadByte()
			if err != nil {
				return token, err
			}
			token = append(token, c)
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
				return nil, nil
			}
			if !(c >= '0' && c <= '9' || c == ';' || c == '?') {
				reader.input.UnreadByte()
				return token[:len(token)-1], nil
			}
		}
		return nil, errors.New("repository log control sequence exceeds 64 KiB")
	}
	if b != 's' {
		return []byte{b}, nil
	}
	prefix := ""
	for _, candidate := range []string{"ection_start:", "ection_end:"} {
		peek, _ := reader.input.Peek(len(candidate))
		if string(peek) == candidate {
			prefix = candidate
			break
		}
	}
	if prefix == "" {
		return []byte{'s'}, nil
	}
	reader.input.Discard(len(prefix))
	token := append([]byte{'s'}, []byte(prefix)...)
	digits := 0
	for {
		c, err := reader.input.ReadByte()
		if err != nil {
			return token, err
		}
		token = append(token, c)
		if c >= '0' && c <= '9' {
			digits++
			if len(token) > 64*1024 {
				return nil, errors.New("repository log section marker exceeds 64 KiB")
			}
			continue
		}
		if c != ':' || digits == 0 {
			reader.input.UnreadByte()
			return token[:len(token)-1], nil
		}
		break
	}
	for len(token) <= 64*1024 {
		c, err := reader.input.ReadByte()
		if err != nil {
			return []byte(PlainLog(string(token))), err
		}
		token = append(token, c)
		if c == '\n' {
			return []byte(PlainLog(string(token))), nil
		}
		if c == '\r' {
			peek, _ := reader.input.Peek(4)
			if !bytes.Equal(peek, []byte{0x1b, '[', '0', 'K'}) {
				next, _ := reader.input.Peek(1)
				if len(next) > 0 && next[0] == '\n' {
					token = append(token, '\n')
					reader.input.Discard(1)
				}
				return []byte(PlainLog(string(token))), nil
			}
		}
		if len(token) >= 4 && bytes.Equal(token[len(token)-4:], []byte{0x1b, '[', '0', 'K'}) {
			return nil, nil
		}
	}
	return nil, errors.New("repository log section marker exceeds 64 KiB")
}
