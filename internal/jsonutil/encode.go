// Package jsonutil encodes wire JSON without HTML escaping. Fingerprints also
// preserve literal Unicode line separators, as JSON.stringify does.
package jsonutil

import (
	"bytes"
	"encoding/json"
)

func Encode(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(output.Bytes(), []byte{'\n'})
	if !bytes.Contains(data, []byte(`\u2028`)) && !bytes.Contains(data, []byte(`\u2029`)) {
		return data, nil
	}
	var result bytes.Buffer
	for i := 0; i < len(data); {
		if data[i] != '\\' {
			result.WriteByte(data[i])
			i++
			continue
		}
		start := i
		for i < len(data) && data[i] == '\\' {
			i++
		}
		count := i - start
		if count%2 == 1 && i+5 <= len(data) && (string(data[i:i+5]) == "u2028" || string(data[i:i+5]) == "u2029") {
			result.Write(data[start : i-1])
			if data[i+4] == '8' {
				result.WriteRune('\u2028')
			} else {
				result.WriteRune('\u2029')
			}
			i += 5
		} else {
			result.Write(data[start:i])
		}
	}
	return result.Bytes(), nil
}
