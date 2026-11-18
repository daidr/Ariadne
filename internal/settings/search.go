package settings

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/searchservice"
	"github.com/daidr/Ariadne/internal/textutil"
)

func searchDraft(before Object, input any) (Object, error) {
	body := object(input)
	provider := fallback(body["provider"], before["provider"])
	if provider != "tavily" && provider != "deepseek" {
		return nil, bad("provider")
	}
	if raw, exists := body["services"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return nil, bad("services")
		}
	}
	services, err := clone(object(before["services"]))
	if err != nil {
		return nil, err
	}
	edits := cloneMap(object(body["services"]))
	if key, exists := body["apiKey"]; exists {
		edits["tavily"] = Object{"apiKey": key}
	}
	for _, kind := range names(edits) {
		raw := edits[kind]
		if kind != "tavily" && kind != "deepseek" {
			return nil, bad("provider")
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, bad("services")
		}
		service := object(services[kind])
		if raw, exists := entry["apiKey"]; exists {
			if raw == nil {
				service["apiKey"] = ""
			} else {
				key, ok := raw.(string)
				if !ok {
					return nil, bad("apiKey")
				}
				key = textutil.Trim(key)
				if length(key) > 200 || containsWhitespace(key) {
					return nil, bad("apiKey")
				}
				if key != "" {
					service["apiKey"] = key
				}
			}
		}
		if raw, exists := entry["baseUrl"]; exists {
			address, ok := raw.(string)
			if !ok {
				return nil, bad("baseUrl")
			}
			address = strings.TrimRight(textutil.Trim(address), "/")
			if address != "" {
				parsed, err := url.Parse(address)
				if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
					return nil, bad("baseUrl")
				}
			}
			service["baseUrl"] = address
		}
		services[kind] = service
	}
	return Object{"provider": provider, "services": services}, nil
}

func (store *Store) CheckSearch(ctx context.Context, input any, client *http.Client) (Object, error) {
	draft, err := store.SearchDraft(input)
	if err != nil {
		return nil, err
	}
	provider := text(draft["provider"])
	service := object(object(draft["services"])[provider])
	key := text(service["apiKey"])
	if key == "" {
		return nil, bad("apiKey")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = (searchservice.Provider{Kind: provider, APIKey: key, BaseURL: text(service["baseUrl"]), Client: client}).Search(ctx, "Ariadne", 1)
	result := Object{"ok": err == nil, "status": nil}
	var httpError *searchservice.HTTPError
	if errors.As(err, &httpError) {
		result["status"] = httpError.Status
	}
	return result, nil
}
func (store *Store) SearchDraft(input any) (Object, error) {
	config := store.snapshot()
	return searchDraft(object(config["search"]), input)
}

func (store *Store) SearchProvider(client *http.Client) *searchservice.Provider {
	config := object(store.snapshot()["search"])
	kind := text(config["provider"])
	service := object(object(config["services"])[kind])
	key := text(service["apiKey"])
	if key == "" {
		return nil
	}
	return &searchservice.Provider{Kind: kind, APIKey: key, BaseURL: text(service["baseUrl"]), Client: client}
}
func (store *Store) UpdateSearch(input any) (Object, error) {
	var result Object
	err := store.update(func(config Object) error {
		value, err := searchDraft(object(config["search"]), input)
		if err != nil {
			return err
		}
		config["search"] = value
		result = value
		return nil
	})
	return result, err
}

// PublicSearch keeps API keys in the private configuration. The host supplies
// labels of models and plugins whose search implementations are available.
func (store *Store) PublicSearch(models []string, plugins []any) Object {
	config := store.snapshot()
	search := object(config["search"])
	services := object(search["services"])
	provider := text(search["provider"])
	if models == nil {
		models = []string{}
	}
	if plugins == nil {
		plugins = []any{}
	}
	listed := []any{}
	for _, entry := range []struct{ kind, title, address string }{{"tavily", "Tavily", "https://api.tavily.com"}, {"deepseek", "DeepSeek Search API", "https://api.deepseek.com/v1/tools"}} {
		service := object(services[entry.kind])
		listed = append(listed, Object{"kind": entry.kind, "title": entry.title, "defaultBaseUrl": entry.address, "baseUrl": service["baseUrl"], "keySet": text(service["apiKey"]) != ""})
	}
	return Object{"provider": provider, "keySet": text(object(services[provider])["apiKey"]) != "", "needed": len(list(object(config["models"])["models"])) > 0 && len(models) == 0 && len(plugins) == 0, "models": models, "plugins": plugins, "services": listed}
}
