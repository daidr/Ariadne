package local

import (
	"os"
	"path/filepath"
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
