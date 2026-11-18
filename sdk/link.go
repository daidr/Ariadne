package sdk

import (
	"context"
	"net/url"
)

type LinkMatch struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}
type PullRequestPreview struct {
	Kind         string  `json:"kind"`
	Repository   string  `json:"repository"`
	Number       int     `json:"number"`
	Reference    string  `json:"reference,omitempty"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	State        string  `json:"state"`
	Draft        bool    `json:"draft"`
	Author       *string `json:"author"`
	Additions    *int    `json:"additions"`
	Deletions    *int    `json:"deletions"`
	ChangedFiles *int    `json:"changedFiles"`
	At           string  `json:"at"`
}
type LinkProvider struct {
	Kind    string
	Title   LocalizedText
	Match   func(*url.URL) *LinkMatch
	Preview func(context.Context, *url.URL) (*PullRequestPreview, error)
	Close   func() error
}
