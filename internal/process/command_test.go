package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("ARIADNE_PROCESS_HELPER") != "1" {
		return
	}
	at := 0
	for at < len(os.Args) && os.Args[at] != "--" {
		at++
	}
	args := os.Args[at+1:]
	switch args[0] {
	case "echo":
		data, _ := json.Marshal(args[1:])
		fmt.Print(string(data))
		fmt.Fprint(os.Stderr, "error output")
		os.Exit(7)
	case "unicode":
		for _, value := range []byte("\ufeff中文😀") {
			os.Stdout.Write([]byte{value})
		}
		os.Exit(0)
	case "colors":
		fmt.Printf("\x1b[32m%s|%s|%s\x1b[0m\n", os.Getenv("NO_COLOR"), os.Getenv("TERM"), os.Getenv("GIT_PAGER"))
		fmt.Fprint(os.Stderr, "\x1b[31m失败\x1b[0m\n")
		os.Exit(7)
	case "output":
		fmt.Print("BEGIN")
		for i := 0; i < 2500; i++ {
			fmt.Print(strings.Repeat("x", 1000))
		}
		fmt.Print("END")
		os.Exit(0)
	case "hold":
		if err := os.Chdir(args[2]); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(4)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "tree", "orphan":
		program, _ := os.Executable()
		child := exec.Command(program, "-test.run=TestHelperProcess", "--", "hold", args[1], args[2])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		if args[0] == "orphan" {
			for i := 0; i < 1000; i++ {
				if _, err := os.Stat(args[1]); err == nil {
					os.Exit(0)
				}
				time.Sleep(time.Millisecond)
			}
			os.Exit(6)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(2)
}
func helper(t *testing.T, args ...string) []string {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{program, "-test.run=TestHelperProcess", "--"}, args...)
}
func waitReady(t *testing.T, path string) int {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				return pid
			}
		}
		select {
		case <-timer.C:
			t.Fatal("helper did not become ready")
		case <-ticker.C:
		}
	}
}
func waitCommand(t *testing.T, command *Command) {
	t.Helper()
	select {
	case <-command.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process or inherited output did not finish")
	}
}
func TestNativeArgumentsExitAndCredentialFiltering(t *testing.T) {
	var stdout, stderr bytes.Buffer
	arguments := []string{"中文 😀", "two words", `quotes"and'backslash\`, "$HOME; & echo wrong", "%PATH%"}
	command, err := Start(t.Context(), helper(t, append([]string{"echo"}, arguments...)...), Options{Env: map[string]string{"ARIADNE_PROCESS_HELPER": "1"}, ForThread: true, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	waitCommand(t, command)
	if command.Wait() == nil || command.ExitCode() != 7 {
		t.Fatal(command.Wait(), command.ExitCode())
	}
	var got []string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if fmt.Sprint(got) != fmt.Sprint(arguments) || stderr.String() != "error output" {
		t.Fatal(got, stderr.String())
	}
	clean := ThreadEnv(map[string]string{"GH_TOKEN": "private", "ARIADNE_TOKEN": "private", "GIT_CONFIG_KEY_0": "private", "PATH": "tools", "NORMAL": "kept"})
	if len(clean) != 2 || clean["PATH"] != "tools" || clean["NORMAL"] != "kept" {
		t.Fatal("thread environment leaked credentials")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Start(ctx, helper(t, "echo"), Options{}); err == nil {
		t.Fatal("cancelled command started")
	}
}

func TestCommandUsesWorkspacePATH(t *testing.T) {
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "workspace-tool"
	if filepath.Ext(program) == ".exe" {
		name += ".exe"
	}
	if err := os.Link(program, filepath.Join(tools, name)); err != nil {
		t.Fatal(err)
	}
	result := Run(t.Context(), []string{name, "-test.run=TestHelperProcess", "--", "echo", "local PATH"}, RunOptions{Directory: root, Env: map[string]string{"PATH": "tools", "ARIADNE_PROCESS_HELPER": "1"}, ForThread: true})
	if result.ExitCode != 7 || result.Stdout != `["local PATH"]` {
		t.Fatal(result)
	}
}
func TestCancellationKillsDescendantsAndReleasesWorkspace(t *testing.T) {
	for _, mode := range []string{"tree", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "workspace")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(root, "ready")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var output bytes.Buffer
			command, err := Start(ctx, helper(t, mode, ready, directory), Options{ForThread: true, Env: map[string]string{"ARIADNE_PROCESS_HELPER": "1"}, Stdout: &output, Stderr: &output})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = command.Kill(); command.Wait() })
			pid := waitReady(t, ready)
			if mode == "tree" {
				cancel()
			}
			waitCommand(t, command)
			if !processHasExited(pid) {
				t.Fatalf("descendant %d outlived its command", pid)
			}
			if err := fsutil.RemoveTree(t.Context(), root, directory); err != nil {
				t.Fatalf("process prevented bounded workspace cleanup after exit: %v", err)
			}
		})
	}
}
