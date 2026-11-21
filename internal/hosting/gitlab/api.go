package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/hostinghttp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type APIOptions struct {
	Base, Path, WebURL string
	Token              func(context.Context) (*string, error)
	Client             *http.Client
}
type API struct {
	rest                  *hostinghttp.Client
	path, repository, web string
}
type Project struct {
	ID            int     `json:"id"`
	DefaultBranch *string `json:"default_branch"`
	WebURL        string  `json:"web_url"`
	Path          string  `json:"path_with_namespace"`
	Visibility    *string `json:"visibility,omitempty"`
}
type rawMerge struct {
	IID            int     `json:"iid"`
	URL            string  `json:"web_url"`
	Title          string  `json:"title"`
	State          string  `json:"state"`
	Draft          *bool   `json:"draft"`
	WorkInProgress bool    `json:"work_in_progress"`
	SHA            *string `json:"sha"`
	DiffRefs       *struct {
		HeadSHA *string `json:"head_sha"`
	} `json:"diff_refs"`
	SourceBranch    string `json:"source_branch"`
	SourceProjectID int    `json:"source_project_id"`
	TargetBranch    string `json:"target_branch"`
	CreatedAt       string `json:"created_at"`
	Author          *struct {
		Username string `json:"username"`
	} `json:"author"`
	ChangesCount *string `json:"changes_count"`
	MergedAt     *string `json:"merged_at"`
	ClosedAt     *string `json:"closed_at"`
}

func (raw rawMerge) info() sdk.PullRequestInfo {
	state := "open"
	if raw.State == "merged" || raw.State == "closed" {
		state = raw.State
	}
	draft := raw.WorkInProgress
	if raw.Draft != nil {
		draft = *raw.Draft
	}
	sha := ""
	if raw.SHA != nil {
		sha = *raw.SHA
	} else if raw.DiffRefs != nil && raw.DiffRefs.HeadSHA != nil {
		sha = *raw.DiffRefs.HeadSHA
	}
	return sdk.PullRequestInfo{Number: raw.IID, Reference: "!" + strconv.Itoa(raw.IID), URL: raw.URL, Title: raw.Title, State: state, Draft: draft, HeadSHA: sha, Base: raw.TargetBranch}
}

type rawIssue struct {
	IID    int    `json:"iid"`
	URL    string `json:"web_url"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Author *struct {
		Username string `json:"username"`
	} `json:"author"`
	Labels      []json.RawMessage `json:"labels"`
	Comments    int               `json:"user_notes_count"`
	Description string            `json:"description"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

func (raw rawIssue) info() sdk.IssueSummary {
	var author *string
	if raw.Author != nil {
		author = &raw.Author.Username
	}
	labels := []string{}
	for _, label := range raw.Labels {
		var name string
		if json.Unmarshal(label, &name) != nil {
			var value struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(label, &value) == nil {
				name = value.Name
			}
		}
		if name != "" {
			labels = append(labels, name)
		}
	}
	state := "closed"
	if raw.State == "opened" {
		state = "open"
	}
	return sdk.IssueSummary{Number: raw.IID, URL: raw.URL, Title: raw.Title, State: state, Author: author, Labels: labels, Comments: raw.Comments, UpdatedAt: raw.UpdatedAt}
}

type rawNote struct {
	ID     json.Number `json:"id"`
	Author *struct {
		Username string `json:"username"`
	} `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	System    bool   `json:"system"`
}

func (raw rawNote) info(issueURL string) sdk.IssueComment {
	var author *string
	if raw.Author != nil {
		author = &raw.Author.Username
	}
	return sdk.IssueComment{ID: string(raw.ID), Author: author, Body: raw.Body, CreatedAt: raw.CreatedAt, URL: issueURL + "#note_" + string(raw.ID)}
}

type rawStatus struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	AllowFailure bool    `json:"allow_failure"`
	URL          *string `json:"target_url"`
}

func (raw rawStatus) info() sdk.CheckInfo {
	info := sdk.CheckInfo{ID: strconv.FormatInt(raw.ID, 10), Name: raw.Name, URL: raw.URL, Status: "queued"}
	conclusion := ""
	switch raw.Status {
	case "running", "canceling":
		info.Status = "in_progress"
	case "success":
		conclusion = "success"
	case "failed":
		conclusion = "failure"
		if raw.AllowFailure {
			conclusion = "neutral"
		}
	case "canceled":
		conclusion = "cancelled"
	case "skipped", "manual":
		conclusion = "skipped"
	}
	if conclusion != "" {
		info.Status = "completed"
		info.Conclusion = &conclusion
	}
	return info
}
func NewAPI(options APIOptions) (*API, error) {
	if _, err := checkPath(options.Path, options.Path); err != nil {
		return nil, err
	}
	rest, err := hostinghttp.New(hostinghttp.Options{Name: "GitLab", Base: options.Base, Client: options.Client, Token: options.Token, RejectRedirect: true, Headers: http.Header{"Accept": {"application/json"}, "User-Agent": {"ariadne"}}, Authorize: func(header http.Header, token string) { header.Set("Authorization", "Bearer "+token) }, ErrorMessage: func(data []byte) string {
		var parsed map[string]json.RawMessage
		if json.Unmarshal(data, &parsed) != nil {
			return textutil.Trim(string(data))
		}
		for _, key := range []string{"message", "error_description", "error"} {
			if value, ok := parsed[key]; ok && string(value) != "null" {
				var text string
				if json.Unmarshal(value, &text) == nil {
					return textutil.Trim(text)
				}
				var compact strings.Builder
				var decoded any
				if json.Unmarshal(value, &decoded) == nil {
					encoded, _ := json.Marshal(decoded)
					compact.Write(encoded)
				}
				return textutil.Trim(compact.String())
			}
		}
		return textutil.Trim(string(data))
	}})
	if err != nil {
		return nil, err
	}
	return &API{rest: rest, path: "/projects/" + hostinghttp.Component(options.Path), repository: options.Path, web: options.WebURL}, nil
}
func (api *API) Close() error { return api.rest.Close() }
func (api *API) Project(ctx context.Context) (Project, error) {
	var raw Project
	err := api.rest.Request(ctx, "GET", api.path, nil, &raw)
	return raw, err
}
func (api *API) MergeRequestFor(ctx context.Context, branch string, projectID int) (*sdk.PullRequestInfo, error) {
	params := "source_branch=" + hostinghttp.FormComponent(branch) + "&state=all&order_by=created_at&sort=desc&per_page=20"
	var found []rawMerge
	if err := api.rest.Request(ctx, "GET", api.path+"/merge_requests?"+params, nil, &found); err != nil {
		return nil, err
	}
	found = slices.DeleteFunc(found, func(mr rawMerge) bool { return mr.SourceBranch != branch || mr.SourceProjectID != projectID })
	slices.SortStableFunc(found, func(a, b rawMerge) int {
		if (a.State == "opened") != (b.State == "opened") {
			if a.State == "opened" {
				return -1
			}
			return 1
		}
		return strings.Compare(b.CreatedAt, a.CreatedAt)
	})
	if len(found) == 0 {
		return nil, nil
	}
	info := found[0].info()
	return &info, nil
}
func (api *API) MergeRequest(ctx context.Context, number int) (sdk.PullRequestInfo, error) {
	var raw rawMerge
	err := api.rest.Request(ctx, "GET", api.path+"/merge_requests/"+strconv.Itoa(number), nil, &raw)
	return raw.info(), err
}

var numeric = regexp.MustCompile(`^\d+$`)

func (api *API) Preview(ctx context.Context, number int) (*sdk.PullRequestPreview, error) {
	var raw rawMerge
	if err := api.rest.Request(ctx, "GET", api.path+"/merge_requests/"+strconv.Itoa(number), nil, &raw); err != nil {
		return nil, err
	}
	info := raw.info()
	var author *string
	if raw.Author != nil {
		author = &raw.Author.Username
	}
	var files *int
	if raw.ChangesCount != nil && numeric.MatchString(*raw.ChangesCount) {
		n, err := strconv.Atoi(*raw.ChangesCount)
		if err == nil {
			files = &n
		}
	}
	at := raw.CreatedAt
	if info.State == "merged" && raw.MergedAt != nil {
		at = *raw.MergedAt
	} else if info.State == "closed" && raw.ClosedAt != nil {
		at = *raw.ClosedAt
	}
	return &sdk.PullRequestPreview{Kind: "pull-request", Repository: api.repository, Number: raw.IID, Reference: info.Reference, URL: raw.URL, Title: raw.Title, State: info.State, Draft: info.Draft, Author: author, ChangedFiles: files, At: at}, nil
}
func (api *API) OpenMergeRequest(ctx context.Context, input sdk.OpenPullRequest) (sdk.PullRequestInfo, error) {
	title := input.Title
	if input.Draft && !strings.HasPrefix(strings.ToLower(title), "draft:") {
		title = "Draft: " + title
	}
	base := ""
	if input.Base != nil {
		base = *input.Base
	}
	var raw rawMerge
	err := api.rest.Request(ctx, "POST", api.path+"/merge_requests", map[string]string{"source_branch": input.Branch, "target_branch": base, "title": title, "description": input.Body}, &raw)
	return raw.info(), err
}
func (api *API) MergeMergeRequest(ctx context.Context, number int, method, sha string) (sdk.PullRequestMergeOutcome, error) {
	var result struct {
		MergeSHA  *string `json:"merge_commit_sha"`
		SquashSHA *string `json:"squash_commit_sha"`
	}
	path := api.path + "/merge_requests/" + strconv.Itoa(number)
	err := api.rest.Request(ctx, "PUT", path+"/merge", map[string]any{"sha": sha, "squash": method == "squash"}, &result)
	if err == nil {
		if result.MergeSHA == nil {
			result.MergeSHA = result.SquashSHA
		}
		return sdk.PullRequestMergeOutcome{Kind: "merged", SHA: result.MergeSHA}, nil
	}
	var failure *hostinghttp.Error
	if !errors.As(err, &failure) {
		return sdk.PullRequestMergeOutcome{}, err
	}
	if failure.Status == 409 {
		return sdk.PullRequestMergeOutcome{Kind: "moved"}, nil
	}
	if !slices.Contains([]int{400, 405, 406, 422}, failure.Status) {
		return sdk.PullRequestMergeOutcome{}, err
	}
	var mr struct {
		Conflicts bool `json:"has_conflicts"`
	}
	if err := api.rest.Request(ctx, "GET", path, nil, &mr); err != nil {
		return sdk.PullRequestMergeOutcome{}, err
	}
	if mr.Conflicts {
		return sdk.PullRequestMergeOutcome{Kind: "conflict"}, nil
	}
	return sdk.PullRequestMergeOutcome{Kind: "refused", Detail: failure.Reason}, nil
}
func (api *API) Checks(ctx context.Context, sha string) ([]sdk.CheckInfo, error) {
	path := api.path + "/repository/commits/" + hostinghttp.Component(sha) + "/statuses?per_page=100"
	newest := map[string]rawStatus{}
	for page := 1; ; page++ {
		address := path
		if page > 1 {
			address += "&page=" + strconv.Itoa(page)
		}
		var raw []rawStatus
		if err := api.rest.Request(ctx, "GET", address, nil, &raw); err != nil {
			return nil, err
		}
		for _, status := range raw {
			if seen, exists := newest[status.Name]; !exists || status.ID > seen.ID {
				newest[status.Name] = status
			}
		}
		if len(raw) < 100 {
			break
		}
	}
	ordered := make([]rawStatus, 0, len(newest))
	for _, status := range newest {
		ordered = append(ordered, status)
	}
	slices.SortFunc(ordered, func(a, b rawStatus) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	checks := []sdk.CheckInfo{}
	for _, status := range ordered {
		checks = append(checks, status.info())
	}
	return checks, nil
}
func (api *API) CheckLog(ctx context.Context, id string) (string, error) {
	if !numeric.MatchString(id) {
		message := fmt.Sprintf("no check %s", id)
		return "", &hostinghttp.Error{Name: "GitLab", Status: 400, Reason: message, Message: message}
	}
	var text string
	err := api.rest.Read(ctx, "GET", api.path+"/jobs/"+id+"/trace", nil, false, func(reader io.Reader) error {
		var err error
		text, err = hostinghttp.Tail(plainStream(reader), 20_000)
		return err
	})
	var failure *hostinghttp.Error
	if errors.As(err, &failure) && failure.Status == 404 {
		return "This check was reported from outside GitLab CI; it has no log here. Open its link for details.", nil
	}
	return text, err
}
func (api *API) Issues(ctx context.Context, query sdk.IssueQuery) ([]sdk.IssueSummary, error) {
	limit := query.Limit
	if limit == 0 {
		limit = 30
	}
	limit = min(max(limit, 1), 100)
	params := "order_by=updated_at&sort=desc&per_page=" + strconv.Itoa(limit)
	state := query.State
	if state == "" {
		state = "open"
	}
	if state != "all" {
		if state == "open" {
			state = "opened"
		} else {
			state = "closed"
		}
		params += "&state=" + state
	}
	if len(query.Labels) != 0 {
		params += "&labels=" + hostinghttp.FormComponent(strings.Join(query.Labels, ","))
	}
	if search := textutil.Trim(query.Search); search != "" {
		params += "&search=" + hostinghttp.FormComponent(search)
	}
	var raw []rawIssue
	if err := api.rest.Request(ctx, "GET", api.path+"/issues?"+params, nil, &raw); err != nil {
		return nil, err
	}
	issues := []sdk.IssueSummary{}
	for _, item := range raw {
		issues = append(issues, item.info())
	}
	return issues, nil
}
func (api *API) readIssue(ctx context.Context, number int) (rawIssue, error) {
	var raw rawIssue
	err := api.rest.Request(ctx, "GET", api.path+"/issues/"+strconv.Itoa(number), nil, &raw)
	return raw, err
}

func (api *API) PreviewIssue(ctx context.Context, number int) (*sdk.LinkPreview, error) {
	raw, err := api.readIssue(ctx, number)
	if err != nil {
		return nil, err
	}
	return raw.info().LinkPreview(api.repository, ""), nil
}

func (api *API) Issue(ctx context.Context, number int) (sdk.IssueDetail, error) {
	raw, err := api.readIssue(ctx, number)
	if err != nil {
		return sdk.IssueDetail{}, err
	}
	path := api.path + "/issues/" + strconv.Itoa(number)
	notes := []rawNote{}
	if raw.Comments != 0 {
		if err := api.rest.Request(ctx, "GET", path+"/notes?sort=desc&order_by=created_at&per_page=100", nil, &notes); err != nil {
			return sdk.IssueDetail{}, err
		}
	}
	recentNotes := []rawNote{}
	for _, note := range notes {
		if !note.System {
			recentNotes = append(recentNotes, note)
			if len(recentNotes) == 20 {
				break
			}
		}
	}
	slices.Reverse(recentNotes)
	recent := []sdk.IssueComment{}
	for _, note := range recentNotes {
		recent = append(recent, note.info(api.issueURL(number)))
	}
	return sdk.IssueDetail{IssueSummary: raw.info(), Body: raw.Description, CreatedAt: raw.CreatedAt, Recent: recent}, nil
}
func (api *API) issueURL(number int) string { return api.web + "/-/issues/" + strconv.Itoa(number) }
func (api *API) Comment(ctx context.Context, number int, body string) (sdk.IssueComment, error) {
	var raw rawNote
	err := api.rest.Request(ctx, "POST", api.path+"/issues/"+strconv.Itoa(number)+"/notes", map[string]string{"body": body}, &raw)
	return raw.info(api.issueURL(number)), err
}
func (api *API) SetIssueState(ctx context.Context, number int, state, _ string) (sdk.IssueSummary, error) {
	action := "reopen"
	if state == "closed" {
		action = "close"
	}
	var raw rawIssue
	err := api.rest.Request(ctx, "PUT", api.path+"/issues/"+strconv.Itoa(number), map[string]string{"state_event": action}, &raw)
	return raw.info(), err
}
