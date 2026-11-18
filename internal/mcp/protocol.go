// Package mcp implements native stdio and streamable HTTP tool clients.
package mcp

import (
	"encoding/json"
	"errors"
)

const MaxMessageBytes = 20 << 20

var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type ConnectionError struct{ Reason string }

func (err *ConnectionError) Error() string { return err.Reason }

var ErrSessionEnded = &ConnectionError{Reason: "the server ended the session"}
var ErrTransportBusy = errors.New("too many active MCP requests")

type ServerInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocolVersion"`
}
type ToolInfo struct {
	Name        string          `json:"name"`
	Title       *string         `json:"title,omitempty"`
	Description *string         `json:"description,omitempty"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations *map[string]any `json:"annotations,omitempty"`
}
type ToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

func validMessage(data []byte) bool {
	var value struct {
		Version string `json:"jsonrpc"`
	}
	return len(data) <= MaxMessageBytes && json.Unmarshal(data, &value) == nil && value.Version == "2.0"
}
func ToolsOf(raw json.RawMessage) ([]ToolInfo, string, error) {
	var page map[string]json.RawMessage
	if json.Unmarshal(raw, &page) != nil || page == nil {
		return nil, "", &ConnectionError{Reason: "tools/list answered without a list of tools"}
	}
	var items []json.RawMessage
	if len(page["tools"]) == 0 || page["tools"][0] != '[' || json.Unmarshal(page["tools"], &items) != nil {
		return nil, "", &ConnectionError{Reason: "tools/list answered without a list of tools"}
	}
	tools := []ToolInfo{}
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var name string
		if json.Unmarshal(item["name"], &name) != nil || name == "" {
			continue
		}
		schema := map[string]any{}
		_ = json.Unmarshal(item["inputSchema"], &schema)
		if schema == nil {
			schema = map[string]any{}
		}
		schema["type"] = "object"
		tool := ToolInfo{Name: name, InputSchema: schema}
		if json.Unmarshal(item["title"], &tool.Title) != nil {
			tool.Title = nil
		}
		if json.Unmarshal(item["description"], &tool.Description) != nil {
			tool.Description = nil
		}
		var annotations map[string]any
		if json.Unmarshal(item["annotations"], &annotations) == nil && annotations != nil {
			tool.Annotations = &annotations
		}
		tools = append(tools, tool)
	}
	var cursor string
	_ = json.Unmarshal(page["nextCursor"], &cursor)
	return tools, cursor, nil
}
func ToolResultOf(raw json.RawMessage) (ToolResult, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return ToolResult{}, &ConnectionError{Reason: "tools/call answered without a result"}
	}
	content := []json.RawMessage{}
	var items []json.RawMessage
	_ = json.Unmarshal(value["content"], &items)
	for _, raw := range items {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) != nil || part == nil {
			continue
		}
		var kind string
		if len(part["type"]) > 0 && part["type"][0] == '"' && json.Unmarshal(part["type"], &kind) == nil {
			content = append(content, raw)
		}
	}
	var failed bool
	_ = json.Unmarshal(value["isError"], &failed)
	return ToolResult{Content: content, StructuredContent: value["structuredContent"], IsError: failed}, nil
}
