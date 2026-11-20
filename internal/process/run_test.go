package process

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestRunBoundsOutputAndDecodesSplitUTF8(t *testing.T) {
	options := RunOptions{Env: map[string]string{"ARIADNE_PROCESS_HELPER": "1"}, ForThread: true}
	result := Run(t.Context(), helper(t, "unicode"), options)
	if result.ExitCode != 0 || result.Stdout != "中文😀" {
		t.Fatal(result)
	}
	result = Run(t.Context(), helper(t, "output"), options)
	if result.ExitCode != 0 || len(result.Stdout) > MaxOutput+100 || !strings.HasPrefix(result.Stdout, "BEGIN") || !strings.HasSuffix(result.Stdout, "END") || !strings.Contains(result.Stdout, "1500008 characters cut") {
		t.Fatal(result.ExitCode, len(result.Stdout), result.Stderr)
	}
	options.Whole = true
	result = Run(t.Context(), helper(t, "output"), options)
	if result.ExitCode != 0 || len(result.Stdout) != 2_500_008 {
		t.Fatal(result.ExitCode, len(result.Stdout))
	}
}
func TestRunTimeoutAndPreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := Run(ctx, helper(t, "echo"), RunOptions{}); result.ExitCode != 130 || result.Stderr != "cancelled" {
		t.Fatal(result)
	}
	root := t.TempDir()
	result := Run(t.Context(), helper(t, "hold", root+"/ready", root), RunOptions{Env: map[string]string{"ARIADNE_PROCESS_HELPER": "1"}, ForThread: true, Timeout: 100 * time.Millisecond})
	if result.ExitCode != 124 || !strings.Contains(result.Stderr, "command timed out") {
		t.Fatal(result)
	}
}

func TestRunPlainOutputUsesShellDefaultsAndKeepsBothStreams(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	options := RunOptions{Env: map[string]string{"ARIADNE_PROCESS_HELPER": "1"}, ForThread: true, PlainOutput: true}
	var streamed = map[string]string{}
	options.OnOutput = func(text, stream string) { streamed[stream] += text }
	result := Run(t.Context(), helper(t, "colors"), options)
	if result.ExitCode != 7 || result.Stdout != "1|dumb|cat\n" || result.Stderr != "失败\n" {
		t.Fatal(result)
	}
	if streamed["stdout"] != result.Stdout || streamed["stderr"] != result.Stderr {
		t.Fatal(streamed)
	}
	options.PlainOutput = false
	result = Run(t.Context(), helper(t, "colors"), options)
	if !strings.Contains(result.Stdout, "xterm-256color") || result.Stderr != "\x1b[31m失败\x1b[0m\n" {
		t.Fatal(result)
	}
}

func TestPlainOutputFiltersSplitTerminalEscapesBeforeBuffering(t *testing.T) {
	var live strings.Builder
	buffer := outputBuffer{plain: true, notify: func(chunk, _ string) { live.WriteString(chunk) }}
	for _, chunk := range []string{"A\x1b[3", "1m中文", "\x1b[0m\x1b]8;;https://example.test\x1b", "\\link\x1b]8;;\x07", "\tend\n"} {
		if n, err := buffer.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("write %q: %d, %v", chunk, n, err)
		}
	}
	const want = "A中文link\tend\n"
	if buffer.String() != want || live.String() != want {
		t.Fatalf("buffered %q, streamed %q", buffer.String(), live.String())
	}
}

func TestWholeOutputLimitAndLiteralShellEncoding(t *testing.T) {
	stopped := false
	buffer := &outputBuffer{whole: true, stop: func() { stopped = true }}
	piece := []byte(strings.Repeat("x", 4096))
	for i := 0; i < MaxWholeOutput/len(piece); i++ {
		if _, err := buffer.Write(piece); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buffer.Write(piece); !errors.Is(err, ErrOutputLimit) || !stopped || buffer.full.Len() > MaxWholeOutput {
		t.Fatal("whole output exceeded its bound", err, buffer.full.Len())
	}
	command := `Write-Output '中文 $HOME "quotes" & < >'`
	argv := ShellArgv([]string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoProfile", "-NonInteractive", "-Command"}, command)
	if len(argv) != 5 || argv[3] != "-EncodedCommand" {
		t.Fatal(argv)
	}
	data, err := base64.StdEncoding.DecodeString(argv[4])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	if decoded := string(utf16.Decode(units)); !strings.HasSuffix(decoded, "\n"+command) || !strings.Contains(decoded, "OutputEncoding") {
		t.Fatal(decoded)
	}
}
