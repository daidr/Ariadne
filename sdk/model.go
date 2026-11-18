// Package sdk defines the native backend's provider contracts. Context
// cancellation belongs to each invocation, while Conversation identifies a
// persistent model session independently of the currently running turn.
package sdk

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Image struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

type Reasoning struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

type Message struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	Images     []Image     `json:"images,omitempty"`
	ToolCalls  []ToolCall  `json:"toolCalls,omitempty"`
	Reasoning  []Reasoning `json:"reasoning,omitempty"`
	ToolCallID string      `json:"toolCallId,omitempty"`
	IsError    bool        `json:"isError,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	Model        string            `json:"model"`
	Messages     []Message         `json:"messages"`
	Tools        []Tool            `json:"tools"`
	Effort       string            `json:"effort,omitempty"`
	Cache        *bool             `json:"cache,omitempty"`
	MaxTokens    int64             `json:"maxTokens,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	ExtraBody    map[string]any    `json:"extraBody,omitempty"`
	Conversation string            `json:"conversation,omitempty"`
}

type Part struct {
	Type         string   `json:"type"`
	Text         string   `json:"text,omitempty"`
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name,omitempty"`
	Arguments    string   `json:"arguments,omitempty"`
	Call         *int     `json:"call,omitempty"`
	Signature    string   `json:"signature,omitempty"`
	Redacted     bool     `json:"redacted,omitempty"`
	StartedAt    int64    `json:"startedAt,omitempty"`
	Input        *int64   `json:"input,omitempty"`
	Output       *int64   `json:"output,omitempty"`
	ContextInput *int64   `json:"contextInput,omitempty"`
	Cached       *int64   `json:"cached,omitempty"`
	CacheWrite   *int64   `json:"cacheWrite,omitempty"`
	Reasoning    *int64   `json:"reasoning,omitempty"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Error        string   `json:"error,omitempty"`
	Retryable    bool     `json:"retryable,omitempty"`
	RetryAfterMS *int64   `json:"retryAfterMs,omitempty"`
	Busy         bool     `json:"busy,omitempty"`
}

func WithExtraBody(body, extra map[string]any) map[string]any {
	for key, value := range extra {
		switch key {
		case "model", "messages", "tools", "stream", "system":
			continue
		}
		body[key] = value
	}
	return body
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type SearchRequest struct {
	Model   string            `json:"model"`
	Query   string            `json:"query"`
	Limit   int               `json:"limit"`
	Headers map[string]string `json:"headers,omitempty"`
}

type SearchUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Cached     int64 `json:"cached,omitempty"`
	CacheWrite int64 `json:"cacheWrite,omitempty"`
}

type SearchAnswer struct {
	Text    string         `json:"text"`
	Sources []SearchResult `json:"sources"`
	Usage   *SearchUsage   `json:"usage,omitempty"`
}

func SearchAsk(query string) string {
	return "Search the web for the query below, once or twice, and report what the pages say that bears on it: the facts, with names, numbers, versions and dates, each cited to its page. At most 300 words, in the language of the query. No advice and no questions back; where the results do not answer it, say so.\n\nQuery: " + query
}
