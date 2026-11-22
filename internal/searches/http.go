package searches

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type HTTPError struct{ Status int }

func (err *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", err.Status) }

func stringField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}

func settingString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func containsWhitespace(value string) bool {
	for _, ch := range value {
		if unicode.IsSpace(ch) {
			return true
		}
	}
	return false
}

func searchSchema(defaultBaseURL string) sdk.SettingsSchema {
	return sdk.SettingsSchema{
		Type: "object",
		Properties: map[string]sdk.SettingField{
			"baseUrl": {
				Type: "string",
				Title: sdk.LocalizedText{
					"zh-CN": "接口地址",
					"en":    "Base URL",
				},
				Description: sdk.LocalizedText{
					"zh-CN": "留空使用默认地址；包含路径前缀，程序追加 /search",
					"en":    "Leave blank for the default; include the path prefix. /search is appended",
				},
				Default: defaultBaseURL,
			},
			"apiKey": {
				Type: "string",
				Title: sdk.LocalizedText{
					"zh-CN": "API Key",
					"en":    "API key",
				},
				Secret: true,
			},
		},
		Required:   []string{"apiKey"},
		FieldOrder: []string{"baseUrl", "apiKey"},
	}
}

func validateHTTPSettings(values map[string]any) error {
	if raw, exists := values["apiKey"]; exists && raw != nil {
		key, ok := raw.(string)
		if !ok {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "apiKey"})
		}
		key = textutil.Trim(key)
		if len(utf16.Encode([]rune(key))) > 200 || containsWhitespace(key) {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "apiKey"})
		}
		values["apiKey"] = key
	}
	if raw, exists := values["baseUrl"]; exists && raw != nil {
		address, ok := raw.(string)
		if !ok {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "baseUrl"})
		}
		address = strings.TrimRight(textutil.Trim(address), "/")
		if address != "" {
			parsed, err := url.Parse(address)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
				parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return domainerr.New("error.invalidBody", map[string]any{"reason": "baseUrl"})
			}
		}
		values["baseUrl"] = address
	}
	return nil
}

func postSearch(ctx context.Context, baseClient *http.Client, baseURL, defaultBaseURL, apiKey string, body map[string]any) ([]json.RawMessage, error) {
	address := strings.TrimRight(textutil.Trim(baseURL), "/")
	if address == "" {
		address = defaultBaseURL
	}
	data, err := jsonutil.Encode(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address+"/search", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	client := http.Client{}
	if baseClient != nil {
		client = *baseClient
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPError{Status: response.StatusCode}
	}
	data, err = modelhttp.ReadResponse(response.Body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results *[]json.RawMessage `json:"results"`
	}
	if json.Unmarshal(data, &result) != nil || result.Results == nil {
		return nil, errors.New("Invalid search response")
	}
	return *result.Results, nil
}

func checkSearchError(err error) sdk.SearchCheckResult {
	result := sdk.SearchCheckResult{OK: err == nil}
	var httpError *HTTPError
	if errors.As(err, &httpError) {
		status := httpError.Status
		result.Status = &status
	}
	return result
}
