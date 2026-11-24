package runner

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

func TestTruncatedCommandOutputPersistedAndReadableViaControlledSource(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	threadA := &turn{
		Runner:       &Runner{Home: home},
		conversation: core.ConversationView{ID: "thr_alpha", ProjectID: "prj_1", Role: "thread"},
	}
	threadB := &turn{
		Runner:       &Runner{Home: home},
		conversation: core.ConversationView{ID: "thr_beta", ProjectID: "prj_1", Role: "thread"},
	}

	var stdout strings.Builder
	stdout.WriteString("HEADER_START\n")
	for i := 1; i <= 400; i++ {
		if i == 200 {
			stdout.WriteString("MIDDLE_NEEDLE_MARKER_42\n")
		} else {
			stdout.WriteString("repetitive verbose log line padding payload 0123456789\n")
		}
	}
	stdout.WriteString("FOOTER_END\n")

	out, err := threadA.formatCommandResult(background.Execution{Result: sdk.ExecResult{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   "stderr diagnostic line\n",
	}}, 2000, "")
	if err != nil {
		t.Fatalf("formatCommandResult failed: %v", err)
	}
	if len(out) > 2020 {
		t.Fatalf("formatted output exceeded budget: %d bytes", len(out))
	}
	if strings.Contains(out, "MIDDLE_NEEDLE_MARKER_42") {
		t.Fatalf("expected middle marker to be omitted from truncated excerpt")
	}
	for _, want := range []string{"HEADER_START", "FOOTER_END", "output truncated:", "Full output saved:", `source="tool-output"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("formatted output missing %q:\n%s", want, out)
		}
	}

	re := regexp.MustCompile(`path="(cmd-[^"]+\.txt)"`)
	m := re.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("could not extract saved filename from output:\n%s", out)
	}
	savedName := m[1]

	// Verify file exists in session data dir and is NOT in cwd.
	savedFull := filepath.Join(home, toolOutputDirName, "thr_alpha", savedName)
	if _, err := os.Stat(savedFull); err != nil {
		t.Fatalf("saved output file missing at %s: %v", savedFull, err)
	}

	// Verify read_file reads saved output cleanly via source="tool-output".
	content, _, err := threadA.readFile(ctx, args{"source": toolOutputSourceName, "path": savedName, "offset": float64(1), "limit": float64(500)})
	if err != nil {
		t.Fatalf("read_file(source=tool-output) failed: %v", err)
	}
	if !strings.Contains(content, "MIDDLE_NEEDLE_MARKER_42") || !strings.Contains(content, "stderr diagnostic line") {
		t.Fatalf("read_file missed expected full output content:\n%s", content)
	}

	// Verify search finds truncated middle lines via source="tool-output".
	found, err := threadA.searchFiles(ctx, args{"source": toolOutputSourceName, "path": savedName, "pattern": "MIDDLE_NEEDLE_MARKER_42"})
	if err != nil {
		t.Fatalf("search(source=tool-output) failed: %v", err)
	}
	if !strings.Contains(found, savedName+":") || !strings.Contains(found, "MIDDLE_NEEDLE_MARKER_42") {
		t.Fatalf("unexpected search result: %q", found)
	}

	// Verify list_dir and find_files work on source="tool-output".
	listed, err := threadA.listDirectory(ctx, args{"source": toolOutputSourceName})
	if err != nil || !strings.Contains(listed, savedName) {
		t.Fatalf("list_dir(source=tool-output) = %q, %v", listed, err)
	}
	foundFiles, err := threadA.findFiles(ctx, args{"source": toolOutputSourceName, "pattern": "cmd-*.txt"})
	if err != nil || !strings.Contains(foundFiles, savedName) {
		t.Fatalf("find_files(source=tool-output) = %q, %v", foundFiles, err)
	}

	// Cross-conversation isolation: threadB cannot read or search threadA's saved output.
	if _, _, err := threadB.readFile(ctx, args{"source": toolOutputSourceName, "path": savedName}); err == nil {
		t.Fatal("threadB was able to read threadA's saved tool output")
	}

	// Path traversal & invalid filenames must be rejected.
	for _, badPath := range []string{"../thr_alpha/" + savedName, "..\\thr_alpha\\" + savedName, "/etc/passwd", `C:\Windows\win.ini`, "secret.txt", "sub/cmd-1.txt"} {
		if _, _, err := threadA.readFile(ctx, args{"source": toolOutputSourceName, "path": badPath}); err == nil {
			t.Fatalf("accepted unsafe tool-output path %q", badPath)
		}
		if _, err := threadA.searchFiles(ctx, args{"source": toolOutputSourceName, "path": badPath, "pattern": "HEADER"}); err == nil {
			t.Fatalf("search accepted unsafe tool-output path %q", badPath)
		}
	}

	// Outputs larger than 1 MiB (e.g. 1.2 MiB) are still readable and searchable via source="tool-output".
	bigBody := strings.Repeat("0123456789abcdef\n", 75_000) + "LARGE_OUTPUT_TAIL_MARKER\n"
	bigOut, err := threadA.formatCommandResult(background.Execution{Result: sdk.ExecResult{ExitCode: 0, Stdout: bigBody}}, 3000, "")
	if err != nil {
		t.Fatalf("large formatCommandResult failed: %v", err)
	}
	bigMatch := re.FindStringSubmatch(bigOut)
	if len(bigMatch) != 2 {
		t.Fatalf("missing saved filename for large output:\n%s", bigOut)
	}
	bigRead, _, err := threadA.readFile(ctx, args{"source": toolOutputSourceName, "path": bigMatch[1], "offset": float64(1), "limit": float64(5)})
	if err != nil || !strings.Contains(bigRead, "0123456789abcdef") {
		t.Fatalf("read_file on >1MiB saved output failed: %q, %v", bigRead, err)
	}
	bigSearch, err := threadA.searchFiles(ctx, args{"source": toolOutputSourceName, "path": bigMatch[1], "pattern": "LARGE_OUTPUT_TAIL_MARKER"})
	if err != nil || !strings.Contains(bigSearch, "LARGE_OUTPUT_TAIL_MARKER") {
		t.Fatalf("search on >1MiB saved output failed: %q, %v", bigSearch, err)
	}
}

func TestPruneToolOutputDirRemovesExpiredFilesAndEmptyDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), toolOutputDirName)
	oldDir := filepath.Join(root, "thr_old")
	mixedDir := filepath.Join(root, "thr_mixed")
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mixedDir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expired := now.Add(-8 * 24 * time.Hour)
	fresh := now.Add(-2 * time.Hour)

	oldOnly := filepath.Join(oldDir, "cmd-100-aaaa1111.txt")
	mixedOld := filepath.Join(mixedDir, "cmd-200-bbbb2222.txt")
	mixedFresh := filepath.Join(mixedDir, "cmd-300-cccc3333.txt")
	for _, p := range []string{oldOnly, mixedOld, mixedFresh} {
		if err := os.WriteFile(p, []byte("log\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldOnly, expired, expired); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(mixedOld, expired, expired); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(mixedFresh, fresh, fresh); err != nil {
		t.Fatal(err)
	}

	pruneToolOutputDir(root, now, toolOutputMaxAge)

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("expected empty expired dir to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(mixedOld); !os.IsNotExist(err) {
		t.Fatalf("expected expired file in mixed dir to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(mixedFresh); err != nil {
		t.Fatalf("expected fresh file to remain, stat err = %v", err)
	}
}
