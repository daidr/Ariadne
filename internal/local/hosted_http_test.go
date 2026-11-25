package local

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

func gitHTTPBackend(t *testing.T, repository string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "server" || password != "private-test-token" {
			t.Error("git credentials missing")
			w.WriteHeader(401)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		env := process.ThreadEnv(process.Environ())
		for key, value := range ServerGitEnv("file") {
			env[key] = value
		}
		for key, value := range map[string]string{"GIT_PROJECT_ROOT": filepath.Dir(repository), "GIT_HTTP_EXPORT_ALL": "1", "PATH_INFO": r.URL.Path, "QUERY_STRING": r.URL.RawQuery, "REQUEST_METHOD": r.Method, "CONTENT_TYPE": r.Header.Get("Content-Type"), "CONTENT_LENGTH": strconv.FormatInt(r.ContentLength, 10), "REMOTE_USER": "server", "REMOTE_ADDR": "127.0.0.1", "SERVER_PROTOCOL": "HTTP/1.1", "HTTP_GIT_PROTOCOL": r.Header.Get("Git-Protocol")} {
			env[key] = value
		}
		stdout := &limitedBytes{max: 8 << 20, cancel: cancel}
		stderr := &limitedBytes{max: 64 << 10}
		command, err := process.Start(ctx, []string{gitExecutable(nil), "http-backend"}, process.Options{Directory: filepath.Dir(repository), Env: env, ReplaceEnv: true, ForThread: true, Stdin: r.Body, Stdout: stdout, Stderr: stderr})
		if err == nil {
			err = command.Wait()
		}
		if err != nil {
			t.Errorf("git http-backend: %v %s", err, stderr.data.String())
			w.WriteHeader(500)
			return
		}
		reader := bufio.NewReader(bytes.NewReader(stdout.data.Bytes()))
		headers, err := textproto.NewReader(reader).ReadMIMEHeader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		status := 200
		for key, values := range headers {
			if key == "Status" {
				parts := strings.Fields(values[0])
				if len(parts) > 0 {
					status, _ = strconv.Atoi(parts[0])
				}
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		if _, err := io.Copy(w, reader); err != nil && r.Context().Err() == nil {
			t.Error(err)
		}
	})
}
func TestHostedGitHTTPFetchPushAndRedirectCredentialBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP git backend credential boundary covered by full suite")
	}
	t.Parallel()
	remote, _ := initGit(t)
	server := httptest.NewServer(gitHTTPBackend(t, remote))
	defer server.Close()
	options := HostedOptions{DataDir: t.TempDir(), Path: "owner/repository", RemoteURL: server.URL + "/" + filepath.Base(remote), User: "server", Token: func(context.Context) (*string, error) { return new("private-test-token"), nil }, DefaultBranch: func(context.Context) (string, error) { return "main", nil }, DisableClone: true}
	host, err := NewHosted(options)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	workspace, err := host.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "http_thread"})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteFile(t.Context(), "file.txt", "http branch\n"); err != nil {
		t.Fatal(err)
	}
	git := NewGit(workspace.Info().Label, "file")
	gitCommitTracked(t, git, "http branch")
	head := readHeadSHA(t, workspace.Info().Label)
	branch := *workspace.Info().Branch
	if sha, err := host.Push(t.Context(), branch); err != nil || sha != head {
		t.Fatal(sha, err)
	}
	if got := readRefSHA(t, filepath.Join(remote, ".git"), "refs/heads/"+branch); got != head {
		t.Fatal(got)
	}
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(500) }))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirect.Close()
	options.DataDir = t.TempDir()
	options.RemoteURL = redirect.URL + "/repository"
	redirected, err := NewHosted(options)
	if err != nil {
		t.Fatal(err)
	}
	defer redirected.Close()
	if err := redirected.Fetch(t.Context()); err == nil {
		t.Fatal("git followed a redirect")
	}
	if leaked.Load() != 0 {
		t.Fatal("git sent a request to the redirect target")
	}
}

func TestHostedGitErrorsDoNotExposeCredentials(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(403)
		_, _ = io.WriteString(w, "private-test-token "+r.Header.Get("Authorization"))
	}))
	defer server.Close()
	host, err := NewHosted(HostedOptions{DataDir: t.TempDir(), Path: "owner/repository", RemoteURL: server.URL + "/repository", User: "server", Token: func(context.Context) (*string, error) { return new("private-test-token"), nil }, DefaultBranch: func(context.Context) (string, error) { return "main", nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	err = host.Fetch(t.Context())
	if err == nil || strings.Contains(err.Error(), "private-test-token") || strings.Contains(err.Error(), "c2VydmVyOnByaXZhdGUtdGVzdC10b2tlbg==") {
		t.Fatal("git error exposed credential or accepted refusal")
	}
}
