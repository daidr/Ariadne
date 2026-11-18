package sdk

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSettingsSchemaPreservesFormFieldOrderThroughRPC(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"host":{"type":"string","title":{"zh-CN":"主机"}},"port":{"type":"number","title":{"zh-CN":"端口"}},"path":{"type":"string","title":{"zh-CN":"目录"}}},"required":["host","path"]}`)
	var schema SettingsSchema
	if err := json.Unmarshal(input, &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.FieldOrder, []string{"host", "port", "path"}) {
		t.Fatal(schema.FieldOrder)
	}
	data, err := json.Marshal(schema)
	if err != nil || string(data) != string(input) {
		t.Fatal(string(data), err)
	}
}
