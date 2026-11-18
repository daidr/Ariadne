package runner

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// An unfinished string can be displayed but must never be used as an ID or edit anchor.
type growingString string

func previewString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case growingString:
		return string(value)
	default:
		return ""
	}
}

type jsonPrefix struct {
	source string
	*json.Decoder
}

func prefixObject(source string) args {
	parser := jsonPrefix{source: source, Decoder: json.NewDecoder(strings.NewReader(source))}
	value, _, err := parser.value(0)
	if err != nil {
		return nil
	}
	object, _ := value.(args)
	return object
}

// Retain the decoded prefix of the last open object/array. Keys and scalars
// other than strings only appear after the decoder has read them.
func (parser jsonPrefix) value(depth int) (any, bool, error) {
	if depth > 24 {
		return nil, false, errors.New("JSON preview nesting limit exceeded")
	}
	offset := parser.InputOffset()
	token, err := parser.Token()
	if err != nil {
		if !prefixEOF(err) {
			return nil, false, err
		}
		source := strings.TrimLeft(parser.source[offset:], " \t\r\n")
		if strings.HasPrefix(source, ":") || strings.HasPrefix(source, ",") {
			source = strings.TrimLeft(source[1:], " \t\r\n")
		}
		if strings.HasPrefix(source, "\"") {
			value, err := openJSONString(source)
			return growingString(value), false, err
		}
		return nil, false, nil
	}
	switch token {
	case json.Delim('{'):
		object := args{}
		for {
			key, err := parser.Token()
			if err != nil {
				if prefixEOF(err) {
					return object, false, nil
				}
				return nil, false, err
			}
			if key == json.Delim('}') {
				return object, true, nil
			}
			value, complete, err := parser.value(depth + 1)
			if err != nil {
				return nil, false, err
			}
			if complete || value != nil {
				object[key.(string)] = value
			}
			if !complete {
				return object, false, nil
			}
		}
	case json.Delim('['):
		array := []any{}
		for {
			value, complete, err := parser.value(depth + 1)
			if err != nil {
				return nil, false, err
			}
			if delim, ok := value.(json.Delim); ok && delim == ']' {
				return array, true, nil
			}
			if complete || value != nil {
				array = append(array, value)
			}
			if !complete {
				return array, false, nil
			}
		}
	default:
		return token, true, nil
	}
}

func prefixEOF(err error) bool { return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) }

// The decoder recognized the start of a string but ran out of input. Add a
// closing quote after its last complete character or escape and decode normally.
func openJSONString(source string) (string, error) {
	end := 1
	for start := 1; start < len(source); {
		next := start + 1
		if source[start] == '\\' {
			next++
			if start+1 < len(source) && source[start+1] == 'u' {
				next += 4
				// A high surrogate at the end might still get its low surrogate.
				if next <= len(source) && (source[start+2] == 'd' || source[start+2] == 'D') &&
					strings.ContainsRune("89aAbB", rune(source[start+3])) && len(source)-next < 6 {
					tail := source[next:]
					if strings.HasPrefix("\\u", tail) || strings.HasPrefix(tail, "\\u") {
						break
					}
				}
			}
		} else {
			if !utf8.FullRuneInString(source[start:]) {
				break
			}
			_, size := utf8.DecodeRuneInString(source[start:])
			next = start + size
		}
		if next > len(source) {
			break
		}
		end, start = next, next
	}
	var result string
	err := json.Unmarshal([]byte(source[:end]+"\""), &result)
	return result, err
}
