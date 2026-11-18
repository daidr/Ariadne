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
