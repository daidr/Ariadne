package modelhttp

import "testing"

func TestValidateHTTPSettings(t *testing.T) {
	valid := []map[string]any{
		{},
		{"baseUrl": "", "apiKey": ""},
		{"baseUrl": "https://api.openai.com/v1", "apiKey": "sk-123"},
		{"baseUrl": "http://127.0.0.1:8080/v1", "apiKey": "local"},
		{"baseUrl": "  https://proxy.example.com/custom/path  "},
	}
	for _, values := range valid {
		if err := ValidateHTTPSettings(values); err != nil {
			t.Fatalf("expected valid settings %v, got error: %v", values, err)
		}
	}

	invalid := []map[string]any{
		{"baseUrl": 123},
		{"baseUrl": "file:///etc/passwd"},
		{"baseUrl": "ftp://example.com"},
		{"baseUrl": "https://user:pass@example.com/v1"},
		{"baseUrl": "https://example.com/v1?foo=bar"},
		{"baseUrl": "https://example.com/v1#frag"},
		{"baseUrl": "https://example.com/v1\r\nX-Injected: 1"},
		{"baseUrl": "/relative/path"},
		{"apiKey": 42},
		{"apiKey": "bad\nkey"},
		{"apiKey": "bad\x00key"},
	}
	for _, values := range invalid {
		if err := ValidateHTTPSettings(values); err == nil {
			t.Fatalf("expected error for invalid settings %v", values)
		}
	}
}
