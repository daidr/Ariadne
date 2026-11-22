package sdk

import (
	"context"
	"iter"
)

type ModelListOptions struct{ Headers map[string]string }
type ListedModel struct {
	ID       string    `json:"id"`
	Title    string    `json:"title,omitempty"`
	Resolves string    `json:"resolves,omitempty"`
	Efforts  *[]string `json:"efforts,omitempty"`
}
type FindContext struct {
	Env   map[string]string
	Home  string
	Which func(string) bool
}
type FoundProvider struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	From     map[string]string `json:"from"`
	Settings map[string]any    `json:"settings"`
}

// ModelProvider is the contract used by built-in and external model providers.
type ModelProvider struct {
	Info             ModelProviderInfo
	ValidateSettings func(map[string]any) error
	Stream           func(context.Context, map[string]any, Request) iter.Seq[Part]
	Models           func(context.Context, map[string]any, ModelListOptions) ([]ListedModel, error)
	Describe         func(map[string]any, Request) any
	Check            func(context.Context, map[string]any) (LocalizedText, error)
	Find             func(FindContext) []FoundProvider
	Forget           func(context.Context, string) error
	SearchAvailable  func(map[string]any) bool
	Search           func(context.Context, map[string]any, SearchRequest) (SearchAnswer, error)
}
