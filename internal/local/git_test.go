package local

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUnwrapWindowsGitForArch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDummy := func(parts ...string) string {
		t.Helper()
		full := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("exe"), 0o755); err != nil {
			t.Fatal(err)
		}
		return full
	}

	cmdGit := writeDummy("Git", "cmd", "git.exe")
	binGit := writeDummy("Git", "bin", "git.exe")
	mingwGit := writeDummy("Git", "mingw64", "bin", "git.exe")
	armGit := writeDummy("Git", "clangarm64", "bin", "git.exe")
	shimGit := writeDummy("Git", "shims", "git.exe")
	cmdScript := writeDummy("Git", "cmd", "git.cmd")
	aloneGit := writeDummy("Alone", "cmd", "git.exe")

	if got := unwrapWindowsGitForArch(cmdGit, "windows", "amd64"); got != mingwGit {
		t.Fatalf("amd64 cmd/git.exe = %q, want %q", got, mingwGit)
	}
	if got := unwrapWindowsGitForArch(binGit, "windows", "arm64"); got != armGit {
		t.Fatalf("arm64 bin/git.exe = %q, want %q", got, armGit)
	}
	if got := unwrapWindowsGitForArch(cmdGit, "linux", "amd64"); got != cmdGit {
		t.Fatalf("non-windows should not unwrap: got %q, want %q", got, cmdGit)
	}
	if got := unwrapWindowsGitForArch(shimGit, "windows", "amd64"); got != shimGit {
		t.Fatalf("non-cmd/bin parent should not unwrap: got %q, want %q", got, shimGit)
	}
	if got := unwrapWindowsGitForArch(cmdScript, "windows", "amd64"); got != cmdScript {
		t.Fatalf("non-git.exe should not unwrap: got %q, want %q", got, cmdScript)
	}
	if got := unwrapWindowsGitForArch(aloneGit, "windows", "amd64"); got != aloneGit {
		t.Fatalf("missing mingw sibling should keep wrapper: got %q, want %q", got, aloneGit)
	}
}

func TestGitExecutableRespectsPathOverride(t *testing.T) {
	t.Parallel()
	if got := gitExecutable(map[string]string{"PATH": "/custom/bin"}); got != "git" {
		t.Fatalf("PATH override = %q, want git", got)
	}
	if got := gitExecutable(map[string]string{"Path": `C:\custom\bin`}); got != "git" {
		t.Fatalf("case-insensitive Path override = %q, want git", got)
	}
}

func TestParseEmbeddedInspectStatus(t *testing.T) {
	t.Parallel()
	const sha = "0123456789abcdef0123456789abcdef01234567"

	cases := []struct {
		name         string
		output       string
		wantBranch   string
		wantHead     string
		wantDirty    bool
		wantFallback bool
	}{
		{
			name:       "clean branch",
			output:     "# branch.oid " + sha + "\x00# branch.head main\x00",
			wantBranch: "refs/heads/main",
			wantHead:   sha,
		},
		{
			name:       "unborn branch",
			output:     "# branch.oid (initial)\x00# branch.head main\x00",
			wantBranch: "refs/heads/main",
			wantHead:   "",
		},
		{
			name:       "detached head",
			output:     "# branch.oid " + sha + "\x00# branch.head (detached)\x00",
			wantBranch: "",
			wantHead:   sha,
		},
		{
			name:         "non-heads symbolic ref uppercase null",
			output:       "# branch.oid " + sha + "\x00# branch.head (NULL)\x00",
			wantBranch:   "",
			wantHead:     sha,
			wantFallback: true,
		},
		{
			name:         "non-heads symbolic ref lowercase null",
			output:       "# branch.oid " + sha + "\x00# branch.head (null)\x00",
			wantBranch:   "",
			wantHead:     sha,
			wantFallback: true,
		},
		{
			name:         "unknown branch head",
			output:       "# branch.oid " + sha + "\x00# branch.head (unknown)\x00",
			wantBranch:   "",
			wantHead:     sha,
			wantFallback: true,
		},
		{
			name:         "full ref prefix falls back to symbolic-ref",
			output:       "# branch.oid " + sha + "\x00# branch.head refs/custom/foo\x00",
			wantBranch:   "",
			wantHead:     sha,
			wantFallback: true,
		},
		{
			name:       "dirty rename with hash orig path",
			output:     "# branch.oid " + sha + "\x00# branch.head feat/work\x002 R. N... 100644 100644 100644 " + sha + " " + sha + " R100 new.txt\x00# branch.head wrong\x00",
			wantBranch: "refs/heads/feat/work",
			wantHead:   sha,
			wantDirty:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			branch, head, dirty, fallback := parseEmbeddedInspectStatus(tc.output)
			if branch != tc.wantBranch || head != tc.wantHead || dirty != tc.wantDirty || fallback != tc.wantFallback {
				t.Fatalf("parseEmbeddedInspectStatus() = (%q, %q, %v, %v), want (%q, %q, %v, %v)",
					branch, head, dirty, fallback, tc.wantBranch, tc.wantHead, tc.wantDirty, tc.wantFallback)
			}
		})
	}
}

func TestWorktreeConfigArgsRespectsGlobalIncludeIf(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	// Resolve symlinks (e.g. macOS /var -> /private/var) so gitdir: matches git's canonical cwd.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	repoA := filepath.Join(root, "alpha")
	repoB := filepath.Join(root, "beta")
	copyTree(t, ensureBaseGitTemplate(t), repoA)
	copyTree(t, ensureBaseGitTemplate(t), repoB)

	cfgA := filepath.Join(root, "alpha.gitconfig")
	cfgB := filepath.Join(root, "beta.gitconfig")
	homeInclude := filepath.Join(root, "home-include")
	homePlain := filepath.Join(root, "home-plain")
	if err := os.MkdirAll(homeInclude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homePlain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgA, []byte("[core]\n\tautocrlf = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgB, []byte("[core]\n\tautocrlf = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	globalBody := strings.Join([]string{
		"[includeIf \"gitdir:" + filepath.ToSlash(repoA) + "/\"]",
		"\tpath = " + filepath.ToSlash(cfgA),
		"[includeIf \"gitdir:" + filepath.ToSlash(repoB) + "/\"]",
		"\tpath = " + filepath.ToSlash(cfgB),
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(homeInclude, ".gitconfig"), []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePlain, ".gitconfig"), []byte("[core]\n\tautocrlf = input\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_CONFIG_NOSYSTEM", "")
	t.Setenv("GIT_CONFIG_SYSTEM", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "")

	// Plain global config without include is cacheable (unless host system config itself uses include).
	t.Setenv("HOME", homePlain)
	t.Setenv("USERPROFILE", homePlain)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homePlain, ".config"))
	if _, ok := inspectConfigFileForGlobalCache(filepath.Join(homePlain, ".gitconfig")); !ok {
		t.Fatal("expected plain global config without include to be cacheable")
	}

	// Unreadable/non-regular path or relative path must bypass cache.
	t.Setenv("GIT_CONFIG_GLOBAL", root)
	if _, ok := globalWorktreeConfigKey(); ok {
		t.Fatal("expected directory GIT_CONFIG_GLOBAL to bypass global cache")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "relative.gitconfig")
	if _, ok := globalWorktreeConfigKey(); ok {
		t.Fatal("expected relative GIT_CONFIG_GLOBAL to bypass global cache")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "")

	// Global config with includeIf must bypass shared cache and resolve per-repo settings.
	t.Setenv("HOME", homeInclude)
	t.Setenv("USERPROFILE", homeInclude)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeInclude, ".config"))
	if _, ok := globalWorktreeConfigKey(); ok {
		t.Fatal("expected global config with includeIf to bypass global cache")
	}
	gotA := worktreeConfigArgs(ctx, repoA)
	if want := []string{"-c", "core.autocrlf=true"}; !slices.Equal(gotA, want) {
		t.Fatalf("worktreeConfigArgs(repoA) = %v, want %v", gotA, want)
	}
	gotB := worktreeConfigArgs(ctx, repoB)
	if want := []string{"-c", "core.autocrlf=false"}; !slices.Equal(gotB, want) {
		t.Fatalf("worktreeConfigArgs(repoB) = %v, want %v", gotB, want)
	}
}

func TestHostedOriginConfigMatchesFailClosed(t *testing.T) {
	t.Parallel()
	const remoteURL = "https://github.com/owner/repo.git"
	valid := "[core]\n\trepositoryformatversion = 0\n\tbare = true\n[remote \"origin\"]\n\turl = " + remoteURL + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n\tpushurl = " + noPushURL + "\n"

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "exact valid bare config",
			body: valid,
			want: true,
		},
		{
			name: "case insensitive remote section name with exact origin subsection",
			body: strings.Replace(valid, `[remote "origin"]`, `[REMOTE "origin"]`, 1),
			want: true,
		},
		{
			name: "rejects include section",
			body: valid + "[include]\n\tpath = /tmp/override.gitconfig\n",
			want: false,
		},
		{
			name: "rejects includeIf section",
			body: valid + "[includeIf \"gitdir:/\"]\n\tpath = /tmp/override.gitconfig\n",
			want: false,
		},
		{
			name: "rejects backslash escape or line continuation",
			body: valid + "# trailing \\\n",
			want: false,
		},
		{
			name: "rejects header with trailing comment",
			body: valid + "[remote \"origin\"] # note\n\tpushurl = https://evil.example/repo.git\n",
			want: false,
		},
		{
			name: "rejects header with inline key value",
			body: valid + "[remote \"origin\"] pushurl = https://evil.example/repo.git\n",
			want: false,
		},
		{
			name: "rejects extra spaces in remote origin header",
			body: valid + "[remote  \"origin\"]\n\tpushurl = https://evil.example/repo.git\n",
			want: false,
		},
		{
			name: "rejects legacy dot remote.origin syntax",
			body: valid + "[remote.origin]\n\tpushurl = https://evil.example/repo.git\n",
			want: false,
		},
		{
			name: "rejects different case subsection name",
			body: valid + "[remote \"Origin\"]\n\tpushurl = https://evil.example/repo.git\n",
			want: false,
		},
		{
			name: "rejects padded brackets around remote header",
			body: strings.Replace(valid, `[remote "origin"]`, `[ remote "origin" ]`, 1),
			want: false,
		},
		{
			name: "rejects multiple headers on one line",
			body: "[core] [remote \"origin\"]\n\turl = " + remoteURL + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n\tpushurl = " + noPushURL + "\n",
			want: false,
		},
		{
			name: "rejects quoted multiline value hiding fake origin section",
			body: "[core]\n\tnote = \"start\n" + valid + "\"\n",
			want: false,
		},
		{
			name: "rejects inline comment on pushurl value",
			body: strings.Replace(valid, noPushURL, noPushURL+" # comment", 1),
			want: false,
		},
		{
			name: "rejects duplicate pushurl",
			body: valid + "\tpushurl = https://evil.example/repo.git\n",
			want: false,
		},
	}

	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+"_"+string(rune('a'+i)))
			if err := os.WriteFile(cfgPath, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := hostedOriginConfigMatches(cfgPath, remoteURL); got != tc.want {
				t.Fatalf("hostedOriginConfigMatches() = %v, want %v\nconfig:\n%s", got, tc.want, tc.body)
			}
		})
	}
}
