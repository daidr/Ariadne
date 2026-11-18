package github

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
	Base, Owner, Name string
	Token             func(context.Context) (*string, error)
	Client            *http.Client
}
type API struct {
	rest              *hostinghttp.Client
	owner, name, path string
}
type Repository struct {
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	Private       bool   `json:"private"`
}
type rawPull struct {
	Number   int     `json:"number"`
	URL      string  `json:"html_url"`
	Title    string  `json:"title"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	ClosedAt *string `json:"closed_at"`
	Draft    bool    `json:"draft"`
	Head     struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	CreatedAt string `json:"created_at"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
	ChangedFiles int `json:"changed_files"`
}

func (raw rawPull) info() sdk.PullRequestInfo {
	state := raw.State
	if raw.MergedAt != nil && *raw.MergedAt != "" {
		state = "merged"
	}
	return sdk.PullRequestInfo{Number: raw.Number, URL: raw.URL, Title: raw.Title, State: state, Draft: raw.Draft, HeadSHA: raw.Head.SHA, Base: raw.Base.Ref}
}

type rawIssue struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Title  string `json:"title"`
	State  string `json:"state"`
	User   *struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels        []json.RawMessage `json:"labels"`
	Comments      int               `json:"comments"`
	Body          *string           `json:"body"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	PullRequest   json.RawMessage   `json:"pull_request"`
	RepositoryURL string            `json:"repository_url"`
}

func labelNames(labels []json.RawMessage) []string {
	names := []string{}
	for _, label := range labels {
		var name string
		if json.Unmarshal(label, &name) != nil {
			var object struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(label, &object) == nil {
				name = object.Name
			}
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
func (raw rawIssue) info() sdk.IssueSummary {
	var author *string
	if raw.User != nil {
		author = &raw.User.Login
	}
	return sdk.IssueSummary{Number: raw.Number, URL: raw.URL, Title: raw.Title, State: raw.State, Author: author, Labels: labelNames(raw.Labels), Comments: raw.Comments, UpdatedAt: raw.UpdatedAt}
}
func (raw rawIssue) isPull() bool {
	return len(raw.PullRequest) != 0 && string(raw.PullRequest) != "null" && string(raw.PullRequest) != "false"
}

type rawComment struct {
	ID   json.Number `json:"id"`
	User *struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	URL       string `json:"html_url"`
}

func (raw rawComment) info() sdk.IssueComment {
	var author *string
	if raw.User != nil {
		author = &raw.User.Login
	}
	return sdk.IssueComment{ID: string(raw.ID), Author: author, Body: raw.Body, CreatedAt: raw.CreatedAt, URL: raw.URL}
}
func NewAPI(options APIOptions) (*API, error) {
	if options.Base == "" {
		options.Base = "https://api.github.com"
	}
	if !ownerPattern.MatchString(options.Owner) || !namePattern.MatchString(options.Name) {
		return nil, errors.New("invalid GitHub repository")
	}
	rest, err := hostinghttp.New(hostinghttp.Options{Name: "GitHub", Base: options.Base, Client: options.Client, Token: options.Token,
		Headers:   http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {"2022-11-28"}, "User-Agent": {"ariadne"}},
		Authorize: func(headers http.Header, token string) { headers.Set("Authorization", "Bearer "+token) },
		ErrorMessage: func(data []byte) string {
			var parsed struct {
				Message string            `json:"message"`
				Errors  []json.RawMessage `json:"errors"`
			}
			if json.Unmarshal(data, &parsed) != nil {
				return textutil.Trim(string(data))
			}
			messages := []string{}
			if parsed.Message != "" {
				messages = append(messages, parsed.Message)
			}
			for _, item := range parsed.Errors {
				var detail struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(item, &detail) == nil && detail.Message != "" {
					messages = append(messages, detail.Message)
				}
			}
			return textutil.Trim(strings.Join(messages, ": "))
		},
	})
	if err != nil {
		return nil, err
	}
	return &API{rest: rest, owner: options.Owner, name: options.Name, path: "/repos/" + options.Owner + "/" + options.Name}, nil
}
func (api *API) Close() error { return api.rest.Close() }
func (api *API) Repository(ctx context.Context) (Repository, error) {
	var result Repository
	err := api.rest.Request(ctx, "GET", api.path, nil, &result)
	return result, err
}
func (api *API) PullRequest(ctx context.Context, number int) (sdk.PullRequestInfo, error) {
	var raw rawPull
	err := api.rest.Request(ctx, "GET", api.path+"/pulls/"+strconv.Itoa(number), nil, &raw)
	return raw.info(), err
}
func (api *API) PullRequestFor(ctx context.Context, branch string) (*sdk.PullRequestInfo, error) {
	var found []rawPull
	if err := api.rest.Request(ctx, "GET", api.path+"/pulls?head="+hostinghttp.Component(api.owner+":"+branch)+"&state=all&per_page=10", nil, &found); err != nil {
		return nil, err
	}
	found = slices.DeleteFunc(found, func(raw rawPull) bool { return raw.Head.Ref != branch })
	slices.SortStableFunc(found, func(a, b rawPull) int {
		if (a.State == "open") != (b.State == "open") {
			if a.State == "open" {
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
func (api *API) Preview(ctx context.Context, number int) (*sdk.PullRequestPreview, error) {
	var raw rawPull
	if err := api.rest.Request(ctx, "GET", api.path+"/pulls/"+strconv.Itoa(number), nil, &raw); err != nil {
		return nil, err
	}
	info := raw.info()
	var author *string
	if raw.User != nil {
		author = &raw.User.Login
	}
	at := raw.CreatedAt
	if raw.ClosedAt != nil {
		at = *raw.ClosedAt
	}
	if raw.MergedAt != nil {
		at = *raw.MergedAt
	}
	return &sdk.PullRequestPreview{Kind: "pull-request", Repository: api.owner + "/" + api.name, Number: raw.Number, URL: raw.URL, Title: raw.Title, State: info.State, Draft: info.Draft, Author: author, Additions: &raw.Additions, Deletions: &raw.Deletions, ChangedFiles: &raw.ChangedFiles, At: at}, nil
}
func (api *API) OpenPullRequest(ctx context.Context, input sdk.OpenPullRequest) (sdk.PullRequestInfo, error) {
	base := ""
	if input.Base != nil {
		base = *input.Base
	}
	var raw rawPull
	err := api.rest.Request(ctx, "POST", api.path+"/pulls", map[string]any{"head": input.Branch, "base": base, "title": input.Title, "body": input.Body, "draft": input.Draft}, &raw)
	return raw.info(), err
}
func (api *API) MergePullRequest(ctx context.Context, number int, method, sha string) (sdk.PullRequestMergeOutcome, error) {
	var merged struct {
		SHA *string `json:"sha"`
	}
	path := api.path + "/pulls/" + strconv.Itoa(number)
	err := api.rest.Request(ctx, "PUT", path+"/merge", map[string]string{"merge_method": method, "sha": sha}, &merged)
	if err == nil {
		return sdk.PullRequestMergeOutcome{Kind: "merged", SHA: merged.SHA}, nil
	}
	var failure *hostinghttp.Error
	if !errors.As(err, &failure) {
		return sdk.PullRequestMergeOutcome{}, err
	}
	if failure.Status == 409 {
		return sdk.PullRequestMergeOutcome{Kind: "moved"}, nil
	}
	if failure.Status != 405 && failure.Status != 422 {
		return sdk.PullRequestMergeOutcome{}, err
	}
	var pull struct {
		Mergeable *bool  `json:"mergeable"`
		State     string `json:"mergeable_state"`
	}
	if err := api.rest.Request(ctx, "GET", path, nil, &pull); err != nil {
		return sdk.PullRequestMergeOutcome{}, err
	}
	if pull.Mergeable != nil && !*pull.Mergeable && pull.State == "dirty" {
		return sdk.PullRequestMergeOutcome{Kind: "conflict"}, nil
	}
	return sdk.PullRequestMergeOutcome{Kind: "refused", Detail: failure.Reason}, nil
}
func (api *API) Checks(ctx context.Context, sha string) ([]sdk.CheckInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var runs struct {
		Runs []struct {
			ID         json.Number `json:"id"`
			Name       string      `json:"name"`
			Status     string      `json:"status"`
			Conclusion *string     `json:"conclusion"`
			URL        *string     `json:"html_url"`
		} `json:"check_runs"`
	}
	var combined struct {
		Statuses []struct {
			Context string  `json:"context"`
			State   string  `json:"state"`
			URL     *string `json:"target_url"`
		} `json:"statuses"`
	}
	finished := make(chan error, 2)
	go func() {
		finished <- api.rest.Request(ctx, "GET", api.path+"/commits/"+hostinghttp.Component(sha)+"/check-runs?per_page=100", nil, &runs)
	}()
	go func() {
		finished <- api.rest.Request(ctx, "GET", api.path+"/commits/"+hostinghttp.Component(sha)+"/status", nil, &combined)
	}()
	first := <-finished
	if first != nil {
		cancel()
	}
	second := <-finished
	if first != nil {
		return nil, first
	}
	if second != nil {
		return nil, second
	}
	checks := []sdk.CheckInfo{}
	for _, run := range runs.Runs {
		status, conclusion := "queued", (*string)(nil)
		if run.Status == "in_progress" {
			status = run.Status
		}
		if run.Status == "completed" {
			status, conclusion = run.Status, run.Conclusion
		}
		checks = append(checks, sdk.CheckInfo{ID: string(run.ID), Name: run.Name, Status: status, Conclusion: conclusion, URL: run.URL})
	}
	for _, raw := range combined.Statuses {
		status, conclusion := "completed", &raw.State
		if raw.State == "pending" {
			status, conclusion = "in_progress", nil
		}
		checks = append(checks, sdk.CheckInfo{ID: "status:" + raw.Context, Name: raw.Context, Status: status, Conclusion: conclusion, URL: raw.URL})
	}
	return checks, nil
}

var qualifier = regexp.MustCompile(`^-?[\w.-]*:`)

func (api *API) Issues(ctx context.Context, query sdk.IssueQuery) ([]sdk.IssueSummary, error) {
	limit := query.Limit
	if limit == 0 {
		limit = 30
	}
	limit = min(max(limit, 1), 100)
	state := query.State
	if state == "" {
		state = "open"
	}
	var found []rawIssue
	if textutil.Trim(query.Search) != "" {
		terms := []string{}
		for _, word := range strings.FieldsFunc(query.Search, textutil.IsSpace) {
			if qualifier.MatchString(word) || word == "AND" || word == "OR" || word == "NOT" {
				continue
			}
			if word = strings.TrimLeft(word, "-"); word != "" {
				terms = append(terms, word)
			}
		}
		terms = append(terms, "repo:"+api.owner+"/"+api.name, "is:issue")
		if state != "all" {
			terms = append(terms, "is:"+state)
		}
		for _, label := range query.Labels {
			terms = append(terms, `label:"`+strings.ReplaceAll(label, `"`, "")+`"`)
		}
		var response struct {
			Items []rawIssue `json:"items"`
		}
		if err := api.rest.Request(ctx, "GET", "/search/issues?q="+hostinghttp.Component(strings.Join(terms, " "))+"&sort=updated&per_page="+strconv.Itoa(limit), nil, &response); err != nil {
			return nil, err
		}
		own := textutil.Lower(api.path)
		for _, item := range response.Items {
			if strings.HasSuffix(textutil.Lower(item.RepositoryURL), own) {
				found = append(found, item)
			}
		}
	} else {
		params := "state=" + hostinghttp.FormComponent(state) + "&sort=updated&direction=desc&per_page=" + strconv.Itoa(limit)
		if len(query.Labels) != 0 {
			params += "&labels=" + hostinghttp.FormComponent(strings.Join(query.Labels, ","))
		}
		if err := api.rest.Request(ctx, "GET", api.path+"/issues?"+params, nil, &found); err != nil {
			return nil, err
		}
	}
	issues := []sdk.IssueSummary{}
	for _, item := range found {
		if !item.isPull() {
			issues = append(issues, item.info())
		}
	}
	return issues, nil
}
func (api *API) Issue(ctx context.Context, number int) (sdk.IssueDetail, error) {
	path := api.path + "/issues/" + strconv.Itoa(number)
	var raw rawIssue
	if err := api.rest.Request(ctx, "GET", path, nil, &raw); err != nil {
		return sdk.IssueDetail{}, err
	}
	if raw.isPull() {
		message := fmt.Sprintf("#%d is a pull request, not an issue", number)
		return sdk.IssueDetail{}, &hostinghttp.Error{Name: "GitHub", Status: 404, Reason: message, Message: message}
	}
	pages := max(1, (raw.Comments+19)/20)
	read := func(page int) ([]rawComment, error) {
		var result []rawComment
		err := api.rest.Request(ctx, "GET", path+"/comments?per_page=20&page="+strconv.Itoa(page), nil, &result)
		return result, err
	}
	comments := []rawComment{}
	if raw.Comments > 0 {
		var err error
		comments, err = read(pages)
		if err != nil {
			return sdk.IssueDetail{}, err
		}
	}
	if pages > 1 && len(comments) < 20 {
		before, err := read(pages - 1)
		if err != nil {
			return sdk.IssueDetail{}, err
		}
		comments = append(before, comments...)
		comments = comments[max(0, len(comments)-20):]
	}
	recent := []sdk.IssueComment{}
	for _, comment := range comments {
		recent = append(recent, comment.info())
	}
	body := ""
	if raw.Body != nil {
		body = *raw.Body
	}
	return sdk.IssueDetail{IssueSummary: raw.info(), Body: body, CreatedAt: raw.CreatedAt, Recent: recent}, nil
}
func (api *API) Comment(ctx context.Context, number int, body string) (sdk.IssueComment, error) {
	var raw rawComment
	err := api.rest.Request(ctx, "POST", api.path+"/issues/"+strconv.Itoa(number)+"/comments", map[string]string{"body": body}, &raw)
	return raw.info(), err
}
func (api *API) SetIssueState(ctx context.Context, number int, state, reason string) (sdk.IssueSummary, error) {
	body := map[string]string{"state": state}
	if state == "open" {
		body["state_reason"] = "reopened"
	} else if state == "closed" && reason != "" {
		body["state_reason"] = reason
	}
	var raw rawIssue
	err := api.rest.Request(ctx, "PATCH", api.path+"/issues/"+strconv.Itoa(number), body, &raw)
	return raw.info(), err
}

var checkID = regexp.MustCompile(`^\d+$`)

func (api *API) CheckLog(ctx context.Context, id string) (string, error) {
	if strings.HasPrefix(id, "status:") {
		return "This check is a commit status; it has no log here. Open its link for details.", nil
	}
	if !checkID.MatchString(id) {
		message := fmt.Sprintf("no check %s", id)
		return "", &hostinghttp.Error{Name: "GitHub", Status: 400, Reason: message, Message: message}
	}
	var text string
	err := api.rest.Read(ctx, "GET", api.path+"/actions/jobs/"+id+"/logs", nil, true, func(reader io.Reader) error { var err error; text, err = hostinghttp.Tail(reader, 20_000); return err })
	if err == nil {
		return text, nil
	}
	var failure *hostinghttp.Error
	if !errors.As(err, &failure) || failure.Status != 404 {
		return "", err
	}
	var run struct {
		Output struct{ Title, Summary, Text *string } `json:"output"`
	}
	if err := api.rest.Request(ctx, "GET", api.path+"/check-runs/"+id, nil, &run); err != nil {
		return "", err
	}
	parts := []string{}
	for _, item := range []*string{run.Output.Title, run.Output.Summary, run.Output.Text} {
		if item != nil && *item != "" {
			parts = append(parts, *item)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}
