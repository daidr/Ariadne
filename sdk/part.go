package sdk

import "encoding/json"

// MarshalJSON keeps required string fields even when a service returns an empty
// thinking block or an empty tool-argument delta.
func (part Part) MarshalJSON() ([]byte, error) {
	value := map[string]any{"type": part.Type}
	switch part.Type {
	case "text":
		value["text"] = part.Text
	case "reasoning":
		value["text"] = part.Text
		if part.StartedAt != 0 {
			value["startedAt"] = part.StartedAt
		}
		if part.Signature != "" {
			value["signature"] = part.Signature
		}
		if part.Redacted {
			value["redacted"] = true
		}
	case "tool-input":
		value["call"] = part.Call
		value["name"] = part.Name
		value["arguments"] = part.Arguments
	case "tool-call":
		value["id"] = part.ID
		value["name"] = part.Name
		value["arguments"] = part.Arguments
	case "usage":
		value["input"], value["output"] = part.Input, part.Output
		if part.ContextInput != nil {
			value["contextInput"] = part.ContextInput
		}
		if part.Cached != nil {
			value["cached"] = part.Cached
		}
		if part.CacheWrite != nil {
			value["cacheWrite"] = part.CacheWrite
		}
		if part.Reasoning != nil {
			value["reasoning"] = part.Reasoning
		}
		if part.CostUSD != nil {
			value["costUsd"] = part.CostUSD
		}
	case "finish":
		value["reason"] = part.Reason
		if part.Reason == "error" || part.Error != "" {
			value["error"] = part.Error
		}
		if part.Retryable {
			value["retryable"] = true
		}
		if part.RetryAfterMS != nil {
			value["retryAfterMs"] = part.RetryAfterMS
		}
		if part.Busy {
			value["busy"] = true
		}
	}
	return json.Marshal(value)
}
