package gitlab

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
)

const GitLabCom = "https://gitlab.com"

var segment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var httpPrefix = regexp.MustCompile(`^https?://`)
var sshRepository = regexp.MustCompile(`^(?:ssh://)?[\w.-]+@([\w.-]+)(?::\d+)?[:/](.+)$`)
var sshAnonymous = regexp.MustCompile(`^ssh://([\w.-]+)(?::\d+)?/(.+)$`)
var subPage = regexp.MustCompile(`/-(?:/.*)?$`)

// WebURL normalizes the HTTP URL features used by JavaScript's URL: host casing,
// default ports and dot path segments. Credentials are validated by the caller.
func webURL(input string) (*url.URL, error) {
	address, err := url.Parse(strings.ReplaceAll(textutil.Trim(input), "\\", "/"))
	if err != nil || address.Host == "" || address.Scheme == "" {
		return nil, errors.New("invalid URL")
	}
	address.Scheme = strings.ToLower(address.Scheme)
	host := strings.ToLower(address.Hostname())
	port := address.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n > 65535 {
			return nil, errors.New("invalid URL port")
		}
	}
	if address.Scheme == "https" && port == "443" || address.Scheme == "http" && port == "80" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	address.Host = host
	if port != "" {
		address.Host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	pathname := address.EscapedPath()
	if pathname == "" {
		pathname = "/"
	}
	trailing := strings.HasSuffix(pathname, "/")
	parts := strings.Split(pathname, "/")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		decoded, _ := url.PathUnescape(part)
		if decoded == "." {
			trailing = true
			continue
		}
		if decoded == ".." {
			if len(normalized) > 1 {
				normalized = normalized[:len(normalized)-1]
			}
			trailing = true
			continue
		}
		normalized = append(normalized, part)
		trailing = strings.HasSuffix(pathname, "/")
	}
	pathname = strings.Join(normalized, "/")
	if trailing && !strings.HasSuffix(pathname, "/") {
		pathname += "/"
	}
	if pathname == "" {
		pathname = "/"
	}
	address.Path, _ = url.PathUnescape(pathname)
	address.RawPath = pathname
	return address, nil
}
func ParseInstance(input string) (string, error) {
	address, err := webURL(input)
	if err != nil {
		return "", fmt.Errorf("\"%s\" is not an address (https://gitlab.example.com)", input)
	}
	host := address.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	if address.Scheme != "https" && !(address.Scheme == "http" && loopback) {
		return "", fmt.Errorf("\"%s\" is not https: the token would cross the network unencrypted (http is taken only for localhost)", input)
	}
	if address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" || strings.Contains(input, "#") {
		return "", fmt.Errorf("\"%s\" holds more than an address; give only https://host[/path]", input)
	}
	return address.Scheme + "://" + address.Host + strings.TrimRight(address.EscapedPath(), "/"), nil
}
func checkPath(value, input string) (string, error) {
	parts := strings.Split(value, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("\"%s\" is not a GitLab project (group/project)", input)
	}
	for _, part := range parts {
		if !segment.MatchString(part) || strings.HasSuffix(part, ".git") {
			return "", fmt.Errorf("\"%s\" is not a GitLab project (group/project)", input)
		}
	}
	return value, nil
}

type RepositoryPath struct {
	Instance string `json:"instance"`
	Path     string `json:"path"`
}

func ParseRepository(input, instance string) (RepositoryPath, error) {
	given := ""
	if textutil.Trim(instance) != "" {
		var err error
		given, err = ParseInstance(instance)
		if err != nil {
			return RepositoryPath{}, err
		}
	}
	text := strings.TrimSuffix(strings.TrimRight(textutil.Trim(input), "/"), ".git")
	var repository string
	if httpPrefix.MatchString(text) {
		address, err := webURL(text)
		if err != nil {
			return RepositoryPath{}, errors.New("Invalid URL")
		}
		pathname := strings.TrimRight(subPage.ReplaceAllString(address.EscapedPath(), ""), "/")
		origin := address.Scheme + "://" + address.Host
		full := origin + pathname
		if given != "" {
			if !strings.HasPrefix(full, given+"/") {
				return RepositoryPath{}, fmt.Errorf("\"%s\" is not on the instance %s", input, given)
			}
			repository = full[len(given)+1:]
		} else {
			given, err = ParseInstance(origin)
			if err != nil {
				return RepositoryPath{}, err
			}
			repository = strings.TrimPrefix(pathname, "/")
		}
	} else if match := sshRepository.FindStringSubmatch(text); match != nil {
		if given != "" {
			address, _ := url.Parse(given)
			if address.Hostname() != match[1] {
				return RepositoryPath{}, fmt.Errorf("\"%s\" is not on the instance %s", input, given)
			}
		} else {
			given = "https://" + match[1]
		}
		repository = match[2]
	} else if match := sshAnonymous.FindStringSubmatch(text); match != nil {
		if given != "" {
			address, _ := url.Parse(given)
			if address.Hostname() != match[1] {
				return RepositoryPath{}, fmt.Errorf("\"%s\" is not on the instance %s", input, given)
			}
		} else {
			given = "https://" + match[1]
		}
		repository = match[2]
	} else {
		repository = text
	}
	if given == "" {
		given = GitLabCom
	}
	repository, err := checkPath(repository, input)
	if err != nil {
		return RepositoryPath{}, err
	}
	return RepositoryPath{Instance: given, Path: repository}, nil
}
func InstanceDirs(instance string) []string {
	address, err := url.Parse(instance)
	if err != nil {
		return nil
	}
	// A colon is invalid in a Windows path component. Keep host/port distinct
	// without colliding with a host whose name contains the chosen separator.
	host := address.Host
	host = strings.ReplaceAll(host, "%", "%25")
	host = strings.NewReplacer(":", "%3A", "[", "%5B", "]", "%5D").Replace(host)
	parts := []string{host}
	for _, part := range strings.Split(address.EscapedPath(), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

type MergeURL struct {
	Path string `json:"path"`
	IID  int    `json:"iid"`
}

var mergePath = regexp.MustCompile(`^/(.+?)/-/merge_requests/(\d{1,9})(?:/[\w/.-]*)?$`)

func ParseMergeRequestURL(address *url.URL, instance string) *MergeURL {
	home, err := url.Parse(instance)
	if address == nil || err != nil || address.Scheme != home.Scheme || !strings.EqualFold(address.Host, home.Host) {
		return nil
	}
	prefix := strings.TrimRight(home.EscapedPath(), "/")
	pathname := address.EscapedPath()
	if prefix != "" && !strings.HasPrefix(pathname, prefix+"/") {
		return nil
	}
	match := mergePath.FindStringSubmatch(strings.TrimPrefix(pathname, prefix))
	if match == nil {
		return nil
	}
	if _, err := checkPath(match[1], address.String()); err != nil {
		return nil
	}
	number, _ := strconv.Atoi(match[2])
	return &MergeURL{Path: match[1], IID: number}
}
