package modelcatalog

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type Info struct {
	ContextTokens *float64        `json:"contextTokens,omitempty"`
	MaxTokens     *float64        `json:"maxTokens,omitempty"`
	Images        *bool           `json:"images,omitempty"`
	Price         *sdk.ModelPrice `json:"price,omitempty"`
	Tools         *bool           `json:"tools,omitempty"`
	Released      string          `json:"released,omitempty"`
	Efforts       *[]string       `json:"efforts,omitempty"`
	reasoning     *bool
}
type entry struct {
	provider, id string
	info         Info
}
type snapshot struct {
	index  map[string][]*entry
	weight int
}

var releaseDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}`)
var makers = []string{"anthropic", "openai", "deepseek", "google", "xai", "mistral", "moonshotai", "alibaba", "zhipuai", "minimax"}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value
}
func rawNumber(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}
func rawBool(raw json.RawMessage) *bool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}
func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
func whole(raw json.RawMessage, least float64) *float64 {
	value := rawNumber(raw)
	if value == nil || *value < least || math.Trunc(*value) != *value {
		return nil
	}
	return value
}
func modelInfo(raw json.RawMessage) Info {
	model := rawObject(raw)
	limits, cost, modalities := rawObject(model["limit"]), rawObject(model["cost"]), rawObject(model["modalities"])
	info := Info{ContextTokens: whole(limits["context"], 4096), MaxTokens: whole(limits["output"], 256), Tools: rawBool(model["tool_call"])}
	input, output := rawNumber(cost["input"]), rawNumber(cost["output"])
	if input != nil && output != nil {
		info.Price = &sdk.ModelPrice{Currency: "USD", Input: *input, Output: *output, CacheRead: rawNumber(cost["cache_read"]), CacheWrite: rawNumber(cost["cache_write"])}
	}
	var inputs []any
	if raw := modalities["input"]; len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &inputs) == nil {
		images := false
		for _, input := range inputs {
			if value, ok := input.(string); ok && value == "image" {
				images = true
			}
		}
		info.Images = &images
	}
	released := rawString(model["release_date"])
	if releaseDate.MatchString(released) {
		info.Released = released
	}
	info.reasoning = rawBool(model["reasoning"])
	return info
}

type parseLimits struct {
	body          int64
	models, index int
}

func parseCatalog(reader io.Reader) (*snapshot, error) {
	return parseBounded(reader, parseLimits{body: 64 << 20, models: 500000, index: 128 << 20})
}
func parseBounded(reader io.Reader, limits parseLimits) (*snapshot, error) {
	limited := &io.LimitedReader{R: reader, N: limits.body + 1}
	decoder := json.NewDecoder(limited)
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if start != json.Delim('{') {
		return nil, errors.New("the price catalog is not a catalog")
	}
	providers := [][]*entry{}
	positions := map[string]int{}
	count := 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		provider, ok := key.(string)
		if !ok {
			return nil, errors.New("invalid price catalog provider")
		}
		start, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if start != json.Delim('{') {
			if err := skipValue(decoder, start); err != nil {
				return nil, err
			}
			continue
		}
		models := []*entry{}
		for decoder.More() {
			field, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			if field != "models" {
				var discard json.RawMessage
				if err := decoder.Decode(&discard); err != nil {
					return nil, err
				}
				continue
			}
			models = []*entry{}
			start, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			if start != json.Delim('{') {
				if err := skipValue(decoder, start); err != nil {
					return nil, err
				}
				continue
			}
			ids := map[string]int{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				id := key.(string)
				var raw json.RawMessage
				if err := decoder.Decode(&raw); err != nil {
					return nil, err
				}
				count++
				if count > limits.models {
					return nil, errors.New("price catalog model count exceeded its limit")
				}
				model := &entry{provider: provider, id: id, info: modelInfo(raw)}
				if index, exists := ids[id]; exists {
					models[index] = model
				} else {
					ids[id] = len(models)
					models = append(models, model)
				}
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		if index, exists := positions[provider]; exists {
			providers[index] = models
		} else {
			positions[provider] = len(providers)
			providers = append(providers, models)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("invalid price catalog JSON")
	}
	if limited.N <= 0 {
		return nil, errors.New("price catalog exceeded 64 MiB")
	}
	catalog := &snapshot{index: map[string][]*entry{}}
	for _, models := range providers {
		for _, model := range models {
			catalog.weight += len(model.provider) + len(model.id) + len(model.info.Released) + 256
			key := textutil.Lower(model.id)
			for {
				catalog.index[key] = append(catalog.index[key], model)
				catalog.weight += len(key) + 32
				if catalog.weight > limits.index {
					return nil, errors.New("price catalog index exceeded its memory limit")
				}
				at := strings.IndexByte(key, '/')
				if at < 0 {
					break
				}
				key = key[at+1:]
			}
		}
	}
	return catalog, nil
}
func skipValue(decoder *json.Decoder, start json.Token) error {
	delimiter, ok := start.(json.Delim)
	if !ok || delimiter != '{' && delimiter != '[' {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
func (catalog *snapshot) matches(model, hint string) []*entry {
	name := textutil.Lower(textutil.Trim(model))
	if name == "" {
		return nil
	}
	found := append([]*entry{}, catalog.index[name]...)
	lowerHint := textutil.Lower(hint)
	rank := func(provider string) int {
		if lowerHint != "" && strings.Contains(lowerHint, textutil.Lower(provider)) {
			return -1
		}
		for index, maker := range makers {
			if provider == maker {
				return index
			}
		}
		return len(makers)
	}
	sort.SliceStable(found, func(i, j int) bool { return rank(found[i].provider) < rank(found[j].provider) })
	return found
}
func copyInfo(info Info) Info {
	if info.ContextTokens != nil {
		info.ContextTokens = new(*info.ContextTokens)
	}
	if info.MaxTokens != nil {
		info.MaxTokens = new(*info.MaxTokens)
	}
	if info.Images != nil {
		info.Images = new(*info.Images)
	}
	if info.Tools != nil {
		info.Tools = new(*info.Tools)
	}
	if info.Price != nil {
		price := *info.Price
		if price.CacheRead != nil {
			price.CacheRead = new(*price.CacheRead)
		}
		if price.CacheWrite != nil {
			price.CacheWrite = new(*price.CacheWrite)
		}
		info.Price = &price
	}
	if info.Efforts != nil {
		values := append([]string{}, *info.Efforts...)
		info.Efforts = &values
	}
	return info
}
func describe(catalog *snapshot, models []string, hint string) map[string]Info {
	out := map[string]Info{}
	for _, name := range models {
		found := catalog.matches(name, hint)
		if len(found) == 0 {
			continue
		}
		info := Info{}
		var reasoning *bool
		for _, entry := range found {
			next := entry.info
			if info.ContextTokens == nil {
				info.ContextTokens = next.ContextTokens
			}
			if info.MaxTokens == nil {
				info.MaxTokens = next.MaxTokens
			}
			if info.Images == nil {
				info.Images = next.Images
			}
			if info.Price == nil {
				info.Price = next.Price
			}
			if info.Tools == nil {
				info.Tools = next.Tools
			}
			if info.Released == "" {
				info.Released = next.Released
			}
			if reasoning == nil {
				reasoning = next.reasoning
			}
		}
		if info.MaxTokens != nil && *info.MaxTokens > 1000000 {
			info.MaxTokens = nil
		}
		if reasoning != nil && !*reasoning {
			info.Efforts = new([]string{})
		}
		out[name] = copyInfo(info)
	}
	return out
}
