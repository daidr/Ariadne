package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/sdk"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const MaxOutput = 1_000_000
const MaxWholeOutput = 64 * 1024 * 1024

var ErrOutputLimit = errors.New("command output exceeds 64 MiB")

type RunOptions struct {
	Directory string
	Env       map[string]string
	ForThread bool
	Whole     bool
	Timeout   time.Duration
	OnOutput  func(text, stream string)
}
type outputBuffer struct {
	head, tail []uint16
	next       int
	total      int64
	whole      bool
	full       strings.Builder
	stream     string
	notify     func(string, string)
	stop       context.CancelFunc
	tooLarge   bool
}

func (buffer *outputBuffer) Write(data []byte) (int, error) {
	if buffer.whole {
		if buffer.full.Len() > MaxWholeOutput-len(data) {
			buffer.tooLarge = true
			buffer.stop()
			return 0, ErrOutputLimit
		}
		buffer.full.Write(data)
	} else {
		units := utf16.Encode([]rune(string(data)))
		buffer.total += int64(len(units))
		if len(buffer.head) < MaxOutput/4 {
			count := min(MaxOutput/4-len(buffer.head), len(units))
			buffer.head = append(buffer.head, units[:count]...)
			units = units[count:]
		}
		limit := MaxOutput - MaxOutput/4
		if len(buffer.tail) < limit {
			count := min(limit-len(buffer.tail), len(units))
			buffer.tail = append(buffer.tail, units[:count]...)
			units = units[count:]
		}
		for len(units) > 0 {
			count := min(limit-buffer.next, len(units))
			copy(buffer.tail[buffer.next:], units[:count])
			buffer.next = (buffer.next + count) % limit
			units = units[count:]
		}
	}
	if len(data) > 0 && buffer.notify != nil {
		buffer.notify(string(data), buffer.stream)
	}
	return len(data), nil
}
func (buffer *outputBuffer) String() string {
	if buffer.whole {
		return buffer.full.String()
	}
	var out strings.Builder
	out.WriteString(string(utf16.Decode(buffer.head)))
	if buffer.total > MaxOutput {
		fmt.Fprintf(&out, "\n[… %d characters cut …]\n", buffer.total-int64(len(buffer.head)+len(buffer.tail)))
	}
	if len(buffer.tail) > 0 {
		out.WriteString(string(utf16.Decode(buffer.tail[buffer.next:])))
		out.WriteString(string(utf16.Decode(buffer.tail[:buffer.next])))
	}
	return out.String()
}
func Run(ctx context.Context, argv []string, options RunOptions) sdk.ExecResult {
	if ctx.Err() != nil {
		return sdk.ExecResult{ExitCode: 130, Stderr: "cancelled"}
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	timed, cancelTimer := context.WithTimeout(ctx, timeout)
	defer cancelTimer()
	invocation, cancel := context.WithCancel(timed)
	defer cancel()
	var callbackMu sync.Mutex
	var notify func(string, string)
	if options.OnOutput != nil {
		notify = func(text, stream string) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			options.OnOutput(text, stream)
		}
	}
	out := &outputBuffer{whole: options.Whole, stream: "stdout", notify: notify, stop: cancel}
	errOut := &outputBuffer{stream: "stderr", notify: notify, stop: cancel}
	stdout := transform.NewWriter(out, unicode.UTF8BOM.NewDecoder())
	stderr := transform.NewWriter(errOut, unicode.UTF8BOM.NewDecoder())
	command, err := Start(invocation, argv, Options{Directory: options.Directory, Env: options.Env, ForThread: options.ForThread, Stdout: stdout, Stderr: stderr})
	if err != nil {
		if ctx.Err() != nil {
			return sdk.ExecResult{ExitCode: 130, Stderr: "cancelled"}
		}
		if timed.Err() != nil {
			return sdk.ExecResult{ExitCode: 124, Stderr: "command timed out"}
		}
		program := "command"
		if len(argv) > 0 {
			program = argv[0]
		}
		return sdk.ExecResult{ExitCode: 127, Stderr: program + ": " + err.Error()}
	}
	err = command.Wait()
	_ = stdout.Close()
	_ = stderr.Close()
	result := sdk.ExecResult{ExitCode: command.ExitCode(), Stdout: out.String(), Stderr: errOut.String()}
	if out.tooLarge {
		result.ExitCode = 1
		result.Stderr = joinError(result.Stderr, ErrOutputLimit.Error())
	} else if ctx.Err() != nil {
		result.ExitCode = 130
		result.Stderr = joinError(result.Stderr, "cancelled")
	} else if timed.Err() != nil {
		result.ExitCode = 124
		result.Stderr = joinError(result.Stderr, "command timed out")
	} else if err != nil && result.ExitCode == 0 {
		// WaitDelay reports an inherited pipe that was closed after the leader
		// exited. The process exit code remains authoritative in that case.
		if !errors.Is(err, exec.ErrWaitDelay) {
			result.ExitCode = 1
			result.Stderr = joinError(result.Stderr, err.Error())
		}
	}
	return result
}
func joinError(text, message string) string {
	if text == "" {
		return message
	}
	return text + "\n" + message
}
