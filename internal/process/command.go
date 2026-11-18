// Package process owns native child processes and their descendants. Every
// invocation is cancellable and bounds the time spent waiting for inherited pipes.
package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Directory      string
	Env            map[string]string
	ForThread      bool
	ReplaceEnv     bool
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Ledger         *Ledger
	Label          string
}
type Command struct {
	cmd  *exec.Cmd
	tree *processTree
	done chan struct{}
	mu   sync.Mutex
	err  error
}

var hiddenEnv = map[string]bool{"GH_TOKEN": true, "GITHUB_TOKEN": true, "GH_ENTERPRISE_TOKEN": true, "GITHUB_ENTERPRISE_TOKEN": true, "GITLAB_TOKEN": true, "GITLAB_ACCESS_TOKEN": true, "GL_TOKEN": true, "CI_JOB_TOKEN": true, "OAUTH_TOKEN": true}

func ThreadEnv(base map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range base {
		checked := key
		if runtime.GOOS == "windows" {
			checked = strings.ToUpper(key)
		}
		if hiddenEnv[checked] || strings.HasPrefix(checked, "ARIADNE_") || strings.HasPrefix(checked, "GIT_CONFIG_") {
			continue
		}
		result[key] = value
	}
	return result
}
func Environ() map[string]string {
	result := map[string]string{}
	for _, item := range os.Environ() {
		start := 0
		if strings.HasPrefix(item, "=") {
			start = 1
		}
		if relative := strings.IndexByte(item[start:], '='); relative >= 0 {
			at := start + relative
			result[item[:at]] = item[at+1:]
		}
	}
	return result
}
func environment(options Options) []string {
	base := Environ()
	if options.ReplaceEnv {
		base = map[string]string{}
	}
	if options.ForThread {
		base = ThreadEnv(base)
	}
	set := func(key, value string) {
		if runtime.GOOS == "windows" {
			for existing := range base {
				if strings.EqualFold(existing, key) {
					delete(base, existing)
				}
			}
		}
		base[key] = value
	}
	set("GIT_TERMINAL_PROMPT", "0")
	for key, value := range options.Env {
		set(key, value)
	}
	keys := make([]string, 0, len(base))
	for key := range base {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+base[key])
	}
	return result
}
func Start(ctx context.Context, argv []string, options Options) (*Command, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("command program is required")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = options.Directory
	command.Env = environment(options)
	resolved, err := resolveProgram(argv[0], command.Env, options.Directory)
	if err != nil {
		return nil, err
	}
	command.Path = resolved
	command.Err = nil
	if err := prepareInvocation(command); err != nil {
		return nil, err
	}
	command.Stdin, command.Stdout, command.Stderr = options.Stdin, options.Stdout, options.Stderr
	command.WaitDelay = time.Second
	tree, err := prepareTree(command)
	if err != nil {
		return nil, err
	}
	command.Cancel = func() error { return tree.kill(command) }
	if err := command.Start(); err != nil {
		tree.close(command)
		return nil, err
	}
	if err := tree.attach(command); err != nil {
		_ = tree.kill(command)
		_ = command.Wait()
		tree.close(command)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	ledger := options.Ledger
	if ledger == nil {
		ledger = DefaultLedger
	}
	label := options.Label
	if label == "" {
		label = filepath.Base(argv[0])
		if options.ForThread {
			label = "command"
		}
	}
	release, err := ledger.Track(command.Process.Pid, runtime.GOOS != "windows", label)
	if err != nil {
		tree.kill(command)
		command.Wait()
		tree.close(command)
		return nil, ledgerError("track", err)
	}
	child := &Command{cmd: command, tree: tree, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		tree.close(command)
		release()
		child.mu.Lock()
		child.err = err
		child.mu.Unlock()
		close(child.done)
	}()
	return child, nil
}
func (command *Command) PID() int    { return command.cmd.Process.Pid }
func (command *Command) Kill() error { return command.tree.kill(command.cmd) }
func (command *Command) Wait() error {
	<-command.done
	command.mu.Lock()
	defer command.mu.Unlock()
	return command.err
}
func (command *Command) Done() <-chan struct{} { return command.done }
func (command *Command) ExitCode() int {
	<-command.done
	if command.cmd.ProcessState == nil {
		return -1
	}
	return command.cmd.ProcessState.ExitCode()
}
