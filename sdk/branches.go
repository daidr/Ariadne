package sdk

import (
	"encoding/json"
	"fmt"
)

type ChangedFile struct {
	Path         string  `json:"path"`
	PreviousPath *string `json:"previousPath"`
	Status       string  `json:"status"`
	Added        *int64  `json:"added"`
	Removed      *int64  `json:"removed"`
}
type BranchCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type BranchRevisions struct {
	Head     string
	BaseHead string
}

type BranchChanges struct {
	Branch      string         `json:"branch"`
	Base        string         `json:"base"`
	From        string         `json:"from"`
	Head        string         `json:"head"`
	BaseHead    string         `json:"baseHead,omitempty"`
	Commits     []BranchCommit `json:"commits"`
	Files       []ChangedFile  `json:"files"`
	Uncommitted []string       `json:"uncommitted"`
}
type FileVersions struct {
	Before  *string `json:"before"`
	After   *string `json:"after"`
	Skipped *string `json:"skipped"`
}
type MergeOutcome struct {
	Kind  string   `json:"kind"`
	SHA   string   `json:"sha,omitempty"`
	Head  string   `json:"head,omitempty"`
	Files []string `json:"files,omitempty"`
}

func (outcome MergeOutcome) MarshalJSON() ([]byte, error) {
	switch outcome.Kind {
	case "empty":
		return json.Marshal(struct {
			Kind string `json:"kind"`
		}{outcome.Kind})
	case "conflict":
		files := outcome.Files
		if files == nil {
			files = []string{}
		}
		return json.Marshal(struct {
			Kind  string   `json:"kind"`
			Files []string `json:"files"`
		}{outcome.Kind, files})
	default:
		type plain MergeOutcome
		return json.Marshal(plain(outcome))
	}
}

type MergeRefused struct{ Reason, Detail string }

func (err *MergeRefused) Error() string {
	return fmt.Sprintf("merge refused (%s): %s", err.Reason, err.Detail)
}

type WorktreeInfo struct {
	ThreadID string `json:"threadId"`
	Path     string `json:"path"`
	Bytes    *int64 `json:"bytes"`
	Orphaned bool   `json:"orphaned,omitempty"`
}
