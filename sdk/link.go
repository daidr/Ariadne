package sdk

import (
	"context"
	"net/url"
)

type LinkMatch struct {
	Kind          string `json:"kind"`
	Label         string `json:"label"`
	Reference     string `json:"reference,omitempty"`
	RepositoryURL string `json:"repositoryUrl,omitempty"`
}
type LinkPreview struct {
	Kind         string   `json:"kind"`
	Repository   string   `json:"repository"`
	Number       int      `json:"number"`
	Reference    string   `json:"reference,omitempty"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	State        string   `json:"state"`
	Draft        bool     `json:"draft"`
	Author       *string  `json:"author"`
	Additions    *int     `json:"additions"`
	Deletions    *int     `json:"deletions"`
	ChangedFiles *int     `json:"changedFiles"`
	At           string   `json:"at"`
	UpdatedAt    string   `json:"updatedAt,omitempty"`
	StateReason  string   `json:"stateReason,omitempty"`
	Labels       []string `json:"labels,omitempty"`
}

type PullRequestPreview = LinkPreview

func (issue IssueSummary) LinkPreview(repository, reason string) *LinkPreview {
	return &LinkPreview{Kind: "issue", Repository: repository, Number: issue.Number,
		URL: issue.URL, Title: issue.Title, State: issue.State, StateReason: reason,
		Author: issue.Author, Labels: issue.Labels, UpdatedAt: issue.UpdatedAt}
}

type LinkProvider struct {
	Kind    string
	Title   LocalizedText
	Match   func(*url.URL) *LinkMatch
	Preview func(context.Context, *url.URL) (*LinkPreview, error)
	Close   func() error
}
