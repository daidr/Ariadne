package sources

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/sdk"
)

func gitOK(t *testing.T, git local.GitRun, args ...string) string {
	t.Helper()
	result := git(t.Context(), args, local.GitOptions{Whole: true})
	if result.ExitCode != 0 {
		t.Fatalf("git %v: %s", args, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}
func objectStore(t *testing.T, root string) *local.ObjectStore {
	t.Helper()
	store, err := local.NewObjectStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func writeLooseRef(t *testing.T, gitDir, ref, content string) {
	t.Helper()
	path := filepath.Join(gitDir, filepath.FromSlash(ref))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readLooseRef(t *testing.T, gitDir, ref string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func TestHostingMovesCommitsBetweenIndependentRepositories(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	hostRoot := t.TempDir()
	hostGitDir := filepath.Join(hostRoot, ".git")
	hostGit := local.NewGit(hostRoot, "file")
	gitOK(t, hostGit, "init", "-qb", "main")
	if err := os.WriteFile(filepath.Join(hostRoot, "file.txt"), []byte("host\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitOK(t, hostGit, "add", "--all")
	gitOK(t, hostGit, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "host")
	head := readLooseRef(t, hostGitDir, "refs/heads/main")
	writeLooseRef(t, hostGitDir, "refs/remotes/origin/main", head)
	writeLooseRef(t, hostGitDir, "refs/remotes/origin/obsolete", head)
	hostObjects := objectStore(t, hostRoot)
	checkoutRoot := t.TempDir()
	checkoutGit := local.NewGit(checkoutRoot, "file")
	gitOK(t, checkoutGit, "init", "--bare", "-q")
	checkoutObjects := objectStore(t, checkoutRoot)
	var fetched, pushed atomic.Int32
	var opened sdk.WorkspaceRequest
	tracked := &sdk.WorkspaceCapability{Isolated: true, Open: func(_ context.Context, input sdk.WorkspaceRequest) (sdk.Workspace, error) {
		opened = input
		return nil, nil
	}}
	origin := "ssh://git@git.example.test/owner/repository.git"
	source := sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Checkout: &sdk.HostedCheckout{OriginURL: func(context.Context) (*string, error) { return &origin, nil }, Objects: *checkoutObjects.Capability(), Tracking: func() *sdk.WorkspaceCapability { return tracked }}}, Close: checkoutObjects.Close}
	hosting := sdk.Capabilities{Repo: &sdk.RepoCapability{Remote: "owner/repository", WebURL: "https://git.example.test/owner/repository", Objects: hostObjects.Capability(), DefaultBranch: func(context.Context) (string, error) { return "main", nil }, Fetch: func(context.Context) error { fetched.Add(1); return nil }, Push: func(ctx context.Context, branch string) (string, error) {
		pushed.Add(1)
		refs, err := hostObjects.Refs(ctx, []string{"refs/heads/" + branch})
		return refs["refs/heads/"+branch], err
	}}, Close: hostObjects.Close}
	combined, err := WithHosting(t.Context(), source, hosting)
	if err != nil {
		t.Fatal(err)
	}
	defer combined.Close()
	if _, err := combined.Workspace.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "one"}); err != nil {
		t.Fatal(err)
	}
	if fetched.Load() != 1 || opened.Ref == nil || *opened.Ref != "refs/remotes/origin/main" || opened.Base == nil || *opened.Base == nil || **opened.Base != "main" {
		t.Fatal(opened, fetched.Load())
	}
	if value := gitOK(t, checkoutGit, "show", "refs/remotes/origin/main:file.txt"); value != "host" {
		t.Fatal(value)
	}
	base := new("main")
	if _, err := combined.Workspace.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "one", Base: &base}); err != nil {
		t.Fatal(err)
	}
	if fetched.Load() != 1 || opened.Ref != nil {
		t.Fatal("resume fetched or changed ref", opened, fetched.Load())
	}
	writeLooseRef(t, checkoutRoot, "refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main")
	if err := os.Remove(filepath.Join(hostGitDir, "refs", "remotes", "origin", "obsolete")); err != nil {
		t.Fatal(err)
	}
	if err := combined.Repo.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	refs, err := checkoutObjects.Refs(t.Context(), []string{originRefs})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := refs[originRefs+"obsolete"]; exists || refs[originRefs+"HEAD"] != head || refs[originRefs+"main"] != head {
		t.Fatal(refs)
	}
	tree := gitOK(t, checkoutGit, "rev-parse", head+"^{tree}")
	threadSHA := gitOK(t, checkoutGit, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit-tree", tree, "-p", head, "-m", "thread")
	writeLooseRef(t, checkoutRoot, "refs/heads/ariadne/thread", threadSHA)
	sha, err := combined.Repo.Push(t.Context(), "ariadne/thread")
	if err != nil || sha != threadSHA || pushed.Load() != 1 {
		t.Fatal(sha, err, pushed.Load())
	}
	if actual := readLooseRef(t, hostGitDir, "refs/heads/ariadne/thread"); actual != threadSHA {
		t.Fatal(actual)
	}
	if actual := readLooseRef(t, checkoutRoot, "refs/remotes/origin/ariadne/thread"); actual != threadSHA {
		t.Fatal(actual)
	}
	if err := combined.Close(); err != nil {
		t.Fatal(err)
	}
	if err := combined.Repo.Fetch(t.Context()); err == nil {
		t.Fatal("closed hosting connected again")
	}
}
func TestRepositoryURLNormalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		address string
		want    string
	}{
		{"https://GitHub.com/Owner/Repo.git/", "github.com/owner/repo"},
		{"git@github.com:Owner/Repo.git", "github.com/owner/repo"},
		{"ssh://git@git.example.test:2222/group/sub/project.git", "git.example.test/group/sub/project"},
		{"https://git.example.test/group/project/", "git.example.test/group/project"},
		{"https://user:password@git.example.test/a/b.git", "git.example.test/a/b"},
		{"https://git.example.test/a%20b/repo.git", "git.example.test/a%20b/repo"},
		{"https://[2001:db8::1]:8443/a/b.git", "[2001:db8::1]/a/b"},
		{"not a repository", ""},
		{"https://git.example.test/", ""},
	}
	for _, tc := range cases {
		if got := RepositoryOf(tc.address); got != tc.want {
			t.Errorf("RepositoryOf(%q) = %q, want %q", tc.address, got, tc.want)
		}
	}
}
