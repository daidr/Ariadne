package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

func (schema SettingsSchema) MarshalJSON() ([]byte, error) {
	var properties json.RawMessage
	if schema.Properties == nil {
		properties = json.RawMessage("null")
	} else {
		var buffer bytes.Buffer
		buffer.WriteByte('{')
		seen := map[string]bool{}
		keys := []string{}
		for _, key := range schema.FieldOrder {
			if _, exists := schema.Properties[key]; exists && !seen[key] {
				keys = append(keys, key)
				seen[key] = true
			}
		}
		rest := []string{}
		for key := range schema.Properties {
			if !seen[key] {
				rest = append(rest, key)
			}
		}
		slices.Sort(rest)
		keys = append(keys, rest...)
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			name, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			value, err := json.Marshal(schema.Properties[key])
			if err != nil {
				return nil, err
			}
			buffer.Write(name)
			buffer.WriteByte(':')
			buffer.Write(value)
		}
		buffer.WriteByte('}')
		properties = buffer.Bytes()
	}
	return json.Marshal(struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required,omitempty"`
	}{schema.Type, properties, schema.Required})
}
func (schema *SettingsSchema) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	result := SettingsSchema{Type: raw.Type, Required: raw.Required}
	if len(raw.Properties) == 0 || bytes.Equal(bytes.TrimSpace(raw.Properties), []byte("null")) {
		*schema = result
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw.Properties))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("settings properties must be an object")
	}
	result.Properties = map[string]SettingField{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("settings property name must be a string")
		}
		var field SettingField
		if err := decoder.Decode(&field); err != nil {
			return err
		}
		if _, exists := result.Properties[key]; !exists {
			result.FieldOrder = append(result.FieldOrder, key)
		}
		result.Properties[key] = field
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	*schema = result
	return nil
}
