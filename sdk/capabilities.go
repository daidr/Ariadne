package sdk

import (
	"context"
	"encoding/json"
	"io"
	"net"
)

type WorkspaceRequest struct {
	ThreadID string   `json:"threadId"`
	Ref      *string  `json:"ref,omitempty"`
	Branch   *string  `json:"branch,omitempty"`
	Base     **string `json:"base,omitempty"`
}

func (request *WorkspaceRequest) UnmarshalJSON(data []byte) error {
	type plain WorkspaceRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["base"]; exists {
		var base *string
		if err := json.Unmarshal(raw, &base); err != nil {
			return err
		}
		decoded.Base = &base
	}
	*request = WorkspaceRequest(decoded)
	return nil
}

type ThreadBranch struct {
	ThreadID string `json:"threadId"`
	Branch   string `json:"branch"`
}
type FileVersionRequest struct {
	From         string  `json:"from"`
	Head         string  `json:"head"`
	Path         string  `json:"path"`
	PreviousPath *string `json:"previousPath"`
}
type MergeRequest struct {
	ThreadID     string `json:"threadId"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	Method       string `json:"method"`
	Message      string `json:"message"`
	ExpectedHead string `json:"expectedHead,omitempty"`
}
type UndoMergeRequest struct {
	Branch string `json:"branch"`
	Base   string `json:"base"`
	SHA    string `json:"sha"`
}
type WorktreeRemoveOptions struct {
	Discard bool `json:"discard,omitempty"`
	Spare   bool `json:"spare,omitempty"`
	Merged  bool `json:"merged,omitempty"`
}
type BranchCapability struct {
	Changes func(context.Context, ThreadBranch, string) (*BranchChanges, error)
	// Revisions can check the refs without computing a diff or scanning the worktree.
	Revisions func(context.Context, ThreadBranch, string) (BranchRevisions, error)
	File      func(context.Context, FileVersionRequest) (FileVersions, error)
	Files     func(context.Context, []FileVersionRequest) ([]FileVersions, error)
	Merged    func(context.Context, ThreadBranch, string) (bool, error)
	Merge     func(context.Context, MergeRequest) (MergeOutcome, error)
	UndoMerge func(context.Context, UndoMergeRequest) error
	Remove    func(context.Context, string, WorktreeRemoveOptions) (bool, error)
	// RemoveOrphaned checks the recorded workspace label before removing a checkout left on disk without its Git registration.
	RemoveOrphaned func(ctx context.Context, threadID, label string) (bool, error)
	// Worktrees can list paths without measuring them when measure is false.
	Worktrees     func(ctx context.Context, measure bool) ([]WorktreeInfo, error)
	SharesStorage func(context.Context) (bool, error)
	Snapshot      func(context.Context, string) (*string, error)
	Diff          func(context.Context, string, string) ([]ChangedFile, error)
}
type WorkspaceCapability struct {
	Isolated bool
	ViewOnly bool
	Open     func(context.Context, WorkspaceRequest) (Workspace, error)
	View     func(context.Context) (SourceView, error)
	Branches *BranchCapability
	Checkout *HostedCheckout
}

// Remote workspaces can expose a loopback port without giving tools access to
// the transport or its credentials. The caller owns the returned connection.
type WorkspaceConnector interface {
	Connect(context.Context, int) (net.Conn, error)
}
type GitObjects struct {
	Refs func(context.Context, []string) (map[string]string, error)
	Has  func(context.Context, []string) ([]string, error)
	// Bundles are streamed with backpressure; large repositories must not be
	// buffered in the host or retained in a plugin's RPC response.
	Bundle func(context.Context, []string, []string) (io.ReadCloser, error)
	// Receive consumes and closes a non-nil stream, including on cancellation
	// and validation failure. Ref updates happen only after its bundle succeeds.
	Receive func(context.Context, io.ReadCloser, map[string]*string) error
}
type HostedCheckout struct {
	OriginURL func(context.Context) (*string, error)
	Objects   GitObjects
	Tracking  func() *WorkspaceCapability
}
type Capabilities struct {
	Workspace *WorkspaceCapability
	Repo      *RepoCapability
	Tracker   *TrackerCapability
	Close     func() error
}
type PullRequestInfo struct {
	Number     int    `json:"number"`
	Reference  string `json:"reference,omitempty"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Draft      bool   `json:"draft"`
	Conflicted bool   `json:"conflicted,omitempty"`
	HeadSHA    string `json:"headSha"`
	Base       string `json:"base"`
}
type PullRequestMergeOutcome struct {
	Kind   string  `json:"kind"`
	SHA    *string `json:"sha,omitempty"`
	Detail string  `json:"detail,omitempty"`
}

func (outcome PullRequestMergeOutcome) MarshalJSON() ([]byte, error) {
	switch outcome.Kind {
	case "merged":
		return json.Marshal(struct {
			Kind string  `json:"kind"`
			SHA  *string `json:"sha"`
		}{outcome.Kind, outcome.SHA})
	case "refused":
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			Detail string `json:"detail"`
		}{outcome.Kind, outcome.Detail})
	default:
		return json.Marshal(struct {
			Kind string `json:"kind"`
		}{outcome.Kind})
	}
}

type CheckInfo struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	URL        *string `json:"url"`
}
type OpenPullRequest struct {
	Branch string  `json:"branch"`
	Base   *string `json:"base,omitempty"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Draft  bool    `json:"draft,omitempty"`
}
type RepoCapability struct {
	Remote, WebURL   string
	DefaultBranch    func(context.Context) (string, error)
	IsPublic         func(context.Context) (bool, error)
	Fetch            func(context.Context) error
	Push             func(context.Context, string) (string, error)
	PullRequestFor   func(context.Context, string) (*PullRequestInfo, error)
	PullRequest      func(context.Context, int) (PullRequestInfo, error)
	OpenPullRequest  func(context.Context, OpenPullRequest) (PullRequestInfo, error)
	Checks           func(context.Context, string) ([]CheckInfo, error)
	MergePullRequest func(context.Context, int, string, string) (PullRequestMergeOutcome, error)
	CheckLog         func(context.Context, string) (string, error)
	Objects          *GitObjects
}
type IssueSummary struct {
	Number    int      `json:"number"`
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Author    *string  `json:"author"`
	Labels    []string `json:"labels"`
	Comments  int      `json:"comments"`
	UpdatedAt string   `json:"updatedAt"`
}
type IssueComment struct {
	ID        string  `json:"id"`
	Author    *string `json:"author"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"createdAt"`
	URL       string  `json:"url"`
}
type IssueDetail struct {
	IssueSummary
	Body      string         `json:"body"`
	CreatedAt string         `json:"createdAt"`
	Recent    []IssueComment `json:"recent"`
}
type IssueQuery struct {
	State  string   `json:"state,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Search string   `json:"search,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}
type TrackerCapability struct {
	Name, WebURL string
	List         func(context.Context, IssueQuery) ([]IssueSummary, error)
	Issue        func(context.Context, int) (IssueDetail, error)
	Comment      func(context.Context, int, string) (IssueComment, error)
	SetState     func(context.Context, int, string, string) (IssueSummary, error)
}
