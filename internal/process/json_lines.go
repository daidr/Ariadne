package process

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/textutil"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const MaxJSONLineBytes = 32 * 1024 * 1024

type JSONOptions struct {
	Argv         []string
	Directory    string
	Env          map[string]string
	ReplaceEnv   bool
	MaxLineBytes int
}
type JSONLines struct {
	options                 JSONOptions
	mu                      sync.Mutex
	started, closing, ended bool
	ctx                     context.Context
	cancel                  context.CancelFunc
	input, output, stderr   *os.File
	child                   *Command
	tail                    string
	reason                  error
	done                    chan struct{}
	writeGate               chan struct{}
	closeOnce               sync.Once
}

func NewJSONLines(options JSONOptions) *JSONLines {
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = MaxJSONLineBytes
	}
	return &JSONLines{options: options, done: make(chan struct{}), writeGate: make(chan struct{}, 1)}
}

// Start calls onLine synchronously with borrowed bytes; it must decode or copy
// them before returning. Returning an error ends the process and its descendants.
func (lines *JSONLines) Start(ctx context.Context, onLine func([]byte) error, onExit func(error)) (err error) {
	lines.mu.Lock()
	defer lines.mu.Unlock()
	if lines.started || lines.closing || lines.ended {
		return errors.New("JSON process has already started or closed")
	}
	lines.started = true
	lines.ctx, lines.cancel = context.WithCancel(ctx)
	opened := []*os.File{}
	defer func() {
		if err != nil {
			for _, file := range opened {
				file.Close()
			}
			lines.cancel()
			lines.ended = true
			lines.reason = err
			close(lines.done)
		}
	}()
	pipe := func() (*os.File, *os.File, error) {
		read, write, err := os.Pipe()
		if err == nil {
			opened = append(opened, read, write)
		}
		return read, write, err
	}
	input, writer, err := pipe()
	if err != nil {
		return err
	}
	reader, output, err := pipe()
	if err != nil {
		return err
	}
	stderr, errorWriter, err := pipe()
	if err != nil {
		return err
	}
	lines.input, lines.output, lines.stderr = writer, reader, stderr
	child, err := Start(lines.ctx, lines.options.Argv, Options{Directory: lines.options.Directory, Env: lines.options.Env, ReplaceEnv: lines.options.ReplaceEnv, Stdin: input, Stdout: output, Stderr: errorWriter})
	if err != nil {
		return err
	}
	lines.child = child
	input.Close()
	output.Close()
	errorWriter.Close()
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		if err := readJSONLines(reader, lines.options.MaxLineBytes, onLine); err != nil {
			lines.fail(err)
		}
	}()
	go func() {
		defer readers.Done()
		writer := transform.NewWriter(tailWriter{lines}, unicode.UTF8BOM.NewDecoder())
		_, _ = io.Copy(writer, stderr)
		_ = writer.Close()
	}()
	go func() {
		_ = child.Wait()
		readers.Wait()
		writer.Close()
		reader.Close()
		stderr.Close()
		lines.cancel()
		lines.mu.Lock()
		if lines.reason == nil {
			program := "agent"
			if len(lines.options.Argv) > 0 {
				program = lines.options.Argv[0]
			}
			tail := strings.Split(textutil.Trim(lines.tail), "\n")
			if len(tail) > 5 {
				tail = tail[len(tail)-5:]
			}
			message := fmt.Sprintf("%s exited with code %d", program, child.ExitCode())
			if joined := strings.Join(tail, "\n"); joined != "" {
				message += ": " + joined
			}
			lines.reason = errors.New(message)
		}
		lines.ended = true
		reason := lines.reason
		closing := lines.closing
		close(lines.done)
		lines.mu.Unlock()
		if !closing && onExit != nil {
			onExit(reason)
		}
	}()
	return nil
}
func readJSONLines(reader io.Reader, maxBytes int, take func([]byte) error) error {
	buffer := bufio.NewReaderSize(reader, 64*1024)
	if first, _ := buffer.Peek(3); bytes.Equal(first, []byte{0xef, 0xbb, 0xbf}) {
		buffer.Discard(3)
	}
	var pending []byte
	takeLine := func(line []byte) error {
		line = bytes.TrimFunc(line, textutil.IsSpace)
		if len(line) > 0 && json.Valid(line) && take != nil {
			return func() (err error) {
				defer func() {
					if cause := recover(); cause != nil {
						err = fmt.Errorf("agent message handler panicked: %v", cause)
					}
				}()
				return take(line)
			}()
		}
		return nil
	}
	chunk := make([]byte, 32*1024)
	for {
		n, err := buffer.Read(chunk)
		remaining := chunk[:n]
		for {
			newline := bytes.IndexByte(remaining, '\n')
			if newline < 0 {
				break
			}
			piece := remaining[:newline]
			if len(pending)+len(piece) > maxBytes {
				return fmt.Errorf("the agent wrote a line over %d MB", maxBytes/1024/1024)
			}
			line := piece
			if len(pending) > 0 {
				pending = append(pending, piece...)
				line = pending
			}
			if err := takeLine(line); err != nil {
				return err
			}
			// Do not retain a large buffer for an idle session.
			pending = nil
			remaining = remaining[newline+1:]
		}
		if len(pending)+len(remaining) > maxBytes {
			return fmt.Errorf("the agent wrote a line over %d MB", maxBytes/1024/1024)
		}
		pending = append(pending, remaining...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return takeLine(pending)
			}
			return err
		}
	}
}

type tailWriter struct{ lines *JSONLines }

func (writer tailWriter) Write(data []byte) (int, error) {
	lines := writer.lines
	lines.mu.Lock()
	defer lines.mu.Unlock()
	units := utf16.Encode([]rune(lines.tail + string(data)))
	if len(units) > 4000 {
		units = units[len(units)-4000:]
	}
	lines.tail = string(utf16.Decode(units))
	return len(data), nil
}
func (lines *JSONLines) fail(err error) {
	lines.mu.Lock()
	if lines.reason == nil {
		lines.reason = err
	}
	cancel, input := lines.cancel, lines.input
	lines.mu.Unlock()
	if input != nil {
		_ = input.Close()
	}
	if cancel != nil {
		cancel()
	}
}
func (lines *JSONLines) SendJSON(ctx context.Context, data []byte) error {
	if len(data) > lines.options.MaxLineBytes {
		return errors.New("agent request exceeds message size limit")
	}
	if !json.Valid(data) {
		return errors.New("agent request is not valid JSON")
	}
	lines.mu.Lock()
	started, closed := lines.started, lines.closing || lines.ended
	life, input := lines.ctx, lines.input
	lines.mu.Unlock()
	if !started || closed || input == nil {
		return errors.New("the agent is not running")
	}
	active, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(life, cancel)
	defer stop()
	select {
	case <-active.Done():
		return active.Err()
	case lines.writeGate <- struct{}{}:
	}
	defer func() { <-lines.writeGate }()
	// Cancelling a partial frame invalidates the connection; close its input so
	// even a non-pollable pipe cannot retain a blocked writer.
	stopWrite := context.AfterFunc(active, func() { input.Close() })
	defer stopWrite()
	deadline := time.Now().Add(30 * time.Second)
	if until, ok := active.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	_ = input.SetWriteDeadline(deadline)
	_, err := input.Write(data)
	if err == nil {
		_, err = input.Write([]byte{'\n'})
	}
	if active.Err() != nil {
		err = active.Err()
	}
	if err != nil {
		lines.fail(fmt.Errorf("could not write to the agent: %w", err))
	}
	return err
}
func (lines *JSONLines) Send(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return lines.SendJSON(ctx, data)
}
func (lines *JSONLines) StderrTail() string {
	lines.mu.Lock()
	defer lines.mu.Unlock()
	return textutil.Trim(lines.tail)
}
func (lines *JSONLines) Done() <-chan struct{} { return lines.done }
func (lines *JSONLines) Close(ctx context.Context) error {
	lines.closeOnce.Do(func() {
		lines.mu.Lock()
		lines.closing = true
		started, ended := lines.started, lines.ended
		input, child := lines.input, lines.child
		if !started {
			lines.ended = true
			close(lines.done)
		}
		lines.mu.Unlock()
		if ended || !started {
			return
		}
		if input != nil {
			input.Close()
		}
		go func() {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-child.Done():
				return
			case <-timer.C:
				lines.cancel()
				_ = child.Kill()
			}
		}()
	})
	select {
	case <-ctx.Done():
		lines.mu.Lock()
		cancel := lines.cancel
		lines.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return ctx.Err()
	case <-lines.done:
		return nil
	}
}
func AgentEnv(extra map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range Environ() {
		if !strings.HasPrefix(strings.ToUpper(key), "ARIADNE_") {
			result[key] = value
		}
	}
	for key, value := range extra {
		if strings.HasPrefix(strings.ToUpper(key), "ARIADNE_") {
			continue
		}
		if runtime.GOOS == "windows" {
			for existing := range result {
				if strings.EqualFold(existing, key) {
					delete(result, existing)
				}
			}
		}
		result[key] = value
	}
	return result
}
