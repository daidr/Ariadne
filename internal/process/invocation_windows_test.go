//go:build windows

package process

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBatchShimForwardsArgumentsLiterally(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "space & (中文)")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(directory, "agent.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n\""+program+"\" -test.run=TestHelperProcess -- echo %*\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"中文 😀", "two words", `quotes"and'backslash\`, "$HOME; & echo wrong", "%PATH%", "!PATH!", "^caret", "|pipe <in >out", "(paren)", "", `ending\\`}
	var stdout, stderr bytes.Buffer
	child, err := Start(t.Context(), append([]string{shim}, arguments...), Options{Env: map[string]string{"ARIADNE_PROCESS_HELPER": "1"}, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	var got []string
	if json.Unmarshal(stdout.Bytes(), &got) != nil || !reflect.DeepEqual(got, arguments) || child.ExitCode() != 7 {
		t.Fatalf("args %#v\nstdout %s\nstderr %s\nexit %d", got, stdout.Bytes(), stderr.Bytes(), child.ExitCode())
	}
	if strings.Contains(stdout.String(), "wrong\r\n") {
		t.Fatal("batch interpreted an argument as code")
	}
}
