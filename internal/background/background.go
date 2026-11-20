// Package background owns commands that outlive an agent turn, with bounded
// retained output and cancellation when their thread, workspace or server ends.
package background

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const MaxRunning = 4
const Timeout = 8 * time.Hour
const keptOutput = 2_000_000
const maxOutputUnits = 32 * 1024 * 1024

type Result struct {
	ExitCode int   `json:"exitCode"`
	EndedAt  int64 `json:"endedAt"`
	Stopped  bool  `json:"stopped"`
}
type Command struct {
	ID             string  `json:"id"`
	ThreadID       string  `json:"threadId"`
	Command        string  `json:"command"`
	CWD            string  `json:"cwd"`
	SourceIdentity string  `json:"-"`
	StartedAt      int64   `json:"startedAt"`
	URL            *string `json:"url"`
	Result         *Result `json:"result"`
	Keep           bool    `json:"keep"`
}
type Output struct {
	Command Command `json:"command"`
	Text    string  `json:"text"`
	Missed  int64   `json:"missed"`
	More    bool    `json:"more,omitempty"`
}
type Peek struct {
	Command Command `json:"command"`
	Tail    string  `json:"tail"`
}
type StartOptions struct {
	Local, Keep    bool
	CWD            string
	SourceIdentity string
	Timeout        time.Duration
}
type Options struct {
	Now func() time.Time
	// OnChange may read List and must return promptly.
	OnChange    func(string)
	FinishedTTL time.Duration
}
type Executor func(context.Context, sdk.ExecOptions) (sdk.ExecResult, error)
type Execution struct {
	Result sdk.ExecResult
	Err    error
}
type job struct {
	view          Command
	output        outputBuffer
	ctx           context.Context
	cancel        context.CancelFunc
	done, changed chan struct{}
	seeking, kept bool
	discard       bool
}
type Commands struct {
	mu                     sync.Mutex
	jobs                   map[string]*job
	order                  []string
	next                   uint64
	units, active, waiters int
	options                Options
	life                   context.Context
	cancel                 context.CancelFunc
	workers                sync.WaitGroup
	closed                 bool
	done                   chan struct{}
	stopSweep              chan struct{}
	sweepDone              chan struct{}
	dirty                  map[string]bool
}

func New(options Options) *Commands {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.FinishedTTL <= 0 {
		options.FinishedTTL = 30 * time.Minute
	}
	life, cancel := context.WithCancel(context.Background())
	commands := &Commands{jobs: map[string]*job{}, dirty: map[string]bool{}, options: options, life: life, cancel: cancel, done: make(chan struct{}), stopSweep: make(chan struct{}), sweepDone: make(chan struct{})}
	go func() {
		defer close(commands.sweepDone)
		ticker := time.NewTicker(min(options.FinishedTTL, time.Minute))
		defer ticker.Stop()
		for {
			select {
			case <-commands.stopSweep:
				return
			case <-ticker.C:
				commands.Purge()
			}
		}
	}()
	return commands
}
func (commands *Commands) notify(thread string) {
	commands.mu.Lock()
	if thread != "" {
		commands.dirty[thread] = true
	}
	pending := commands.dirty
	commands.dirty = map[string]bool{}
	callback := commands.options.OnChange
	commands.mu.Unlock()
	if callback != nil {
		for thread := range pending {
			func() { defer func() { recover() }(); callback(thread) }()
		}
	}
}
func copyView(value Command) Command {
	if value.URL != nil {
		url := *value.URL
		value.URL = &url
	}
	if value.Result != nil {
		result := *value.Result
		value.Result = &result
	}
	return value
}
func (commands *Commands) running(thread string) int {
	count := 0
	for _, job := range commands.jobs {
		if job.view.ThreadID == thread && job.view.Result == nil {
			count++
		}
	}
	return count
}
func (commands *Commands) List(thread string) []Command {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	out := []Command{}
	for _, id := range commands.order {
		job := commands.jobs[id]
		if job != nil && job.view.ThreadID == thread {
			out = append(out, copyView(job.view))
		}
	}
	return out
}
func (commands *Commands) Running(thread string) int {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	return commands.running(thread)
}
func (commands *Commands) HasRoom(thread string) bool { return commands.Running(thread) < MaxRunning }
func (commands *Commands) forget(job *job) {
	if commands.jobs[job.view.ID] != job {
		return
	}
	delete(commands.jobs, job.view.ID)
	commands.dirty[job.view.ThreadID] = true
	commands.units -= job.output.length()
	job.output.clear()
	job.view.Command = ""
	job.view.CWD = ""
	job.view.SourceIdentity = ""
	job.view.URL = nil
	close(job.changed)
	job.changed = make(chan struct{})
	for i, id := range commands.order {
		if id == job.view.ID {
			copy(commands.order[i:], commands.order[i+1:])
			commands.order[len(commands.order)-1] = ""
			commands.order = commands.order[:len(commands.order)-1]
			break
		}
	}
}
func (commands *Commands) room(thread string) error {
	if commands.closed {
		return errors.New("background commands are closed")
	}
	if commands.running(thread) >= MaxRunning {
		return fmt.Errorf("%d commands of yours already run in the background; stop one first (stop_command)", MaxRunning)
	}
	if commands.active >= 256 {
		return errors.New("too many active background commands")
	}
	finished := []*job{}
	for _, id := range commands.order {
		job := commands.jobs[id]
		if job.view.ThreadID == thread && job.view.Result != nil {
			finished = append(finished, job)
		}
	}
	for _, job := range finished[:max(0, len(finished)-7)] {
		commands.forget(job)
	}
	for len(commands.jobs) >= 1024 {
		var oldest *job
		for _, id := range commands.order {
			if job := commands.jobs[id]; job.view.Result != nil {
				oldest = job
				break
			}
		}
		if oldest == nil {
			return errors.New("too many background commands")
		}
		commands.forget(oldest)
	}
	return nil
}
func (commands *Commands) track(thread, command string, ctx context.Context, cancel context.CancelFunc, options StartOptions) (*job, error) {
	if len(thread) > 512 || len(command) > 32*1024 || len(options.CWD) > 4096 || len(options.SourceIdentity) > 128 || thread == "" {
		return nil, errors.New("background command metadata exceeds size limit")
	}
	if err := commands.room(thread); err != nil {
		return nil, err
	}
	commands.next++
	cwd := options.CWD
	if cwd == "" {
		cwd = "."
	}
	job := &job{view: Command{ID: "cmd-" + strconv.FormatUint(commands.next, 10), ThreadID: strings.Clone(thread), Command: strings.Clone(command), CWD: strings.Clone(cwd), SourceIdentity: strings.Clone(options.SourceIdentity), StartedAt: commands.options.Now().UnixMilli(), Keep: options.Keep}, ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}), seeking: options.Local}
	commands.jobs[job.view.ID] = job
	commands.order = append(commands.order, job.view.ID)
	commands.active++
	commands.workers.Add(1)
	return job, nil
}
func (commands *Commands) append(job *job, text string) {
	if text == "" {
		return
	}
	commands.mu.Lock()
	if commands.closed || commands.jobs[job.view.ID] != job {
		commands.mu.Unlock()
		return
	}
	before := job.output.length()
	job.output.append(text)
	commands.units += job.output.length() - before
	commands.limitOutput()
	changed := false
	if job.seeking {
		prefix := job.output.first(min(20_000, job.output.length()))
		job.view.URL = ServedURL(prefix)
		job.seeking = job.view.URL == nil && job.output.dropped+int64(job.output.length()) < 20_000
		changed = job.view.URL != nil
	}
	close(job.changed)
	job.changed = make(chan struct{})
	commands.mu.Unlock()
	if changed {
		commands.notify(job.view.ThreadID)
	}
}
func (commands *Commands) limitOutput() {
	for commands.units > maxOutputUnits {
		var drop *job
		for _, id := range commands.order {
			candidate := commands.jobs[id]
			if candidate.output.length() > 0 {
				drop = candidate
				break
			}
		}
		if drop == nil {
			break
		}
		amount := min(drop.output.length(), commands.units-maxOutputUnits)
		commands.units -= drop.output.drop(amount)
	}
}
func execute(ctx context.Context, exec Executor, options sdk.ExecOptions) (result sdk.ExecResult, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("background command failed")
		}
	}()
	return exec(ctx, options)
}
func (commands *Commands) Start(thread, command string, exec Executor, options StartOptions) (Command, error) {
	ctx, cancel := context.WithCancel(commands.life)
	commands.mu.Lock()
	job, err := commands.track(thread, command, ctx, cancel, options)
	if err != nil {
		commands.mu.Unlock()
		cancel()
		return Command{}, err
	}
	view := copyView(job.view)
	commands.mu.Unlock()
	commands.notify(thread)
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	go func() {
		defer commands.workers.Done()
		result, err := execute(ctx, exec, sdk.ExecOptions{Timeout: timeout, OnOutput: func(text, _ string) { commands.append(job, text) }})
		commands.settle(job, result, err)
	}()
	return view, nil
}
func (commands *Commands) Adopt(thread, command string, ctx context.Context, cancel context.CancelFunc, ended <-chan Execution, output string, options StartOptions) (Command, func(string), error) {
	if ctx == nil || cancel == nil || ended == nil {
		return Command{}, nil, errors.New("running command needs its context, cancellation and result")
	}
	stop := context.AfterFunc(commands.life, cancel)
	ownedCancel := func() { stop(); cancel() }
	commands.mu.Lock()
	entry, err := commands.track(thread, command, ctx, ownedCancel, options)
	if err != nil {
		commands.mu.Unlock()
		stop()
		return Command{}, nil, err
	}
	view := copyView(entry.view)
	commands.mu.Unlock()
	append := func(text string) { commands.append(entry, text) }
	append(output)
	commands.notify(thread)
	go func() {
		defer commands.workers.Done()
		completion, ok := <-ended
		if !ok {
			completion.Err = errors.New("background command ended without a result")
		}
		stop()
		commands.settle(entry, completion.Result, completion.Err)
	}()
	return view, append, nil
}
func (commands *Commands) settle(job *job, result sdk.ExecResult, err error) {
	if err != nil {
		commands.append(job, "\n"+err.Error()+"\n")
		result.ExitCode = -1
	}
	commands.mu.Lock()
	job.view.Result = &Result{ExitCode: result.ExitCode, EndedAt: commands.options.Now().UnixMilli(), Stopped: job.ctx.Err() != nil}
	commands.active--
	close(job.changed)
	job.changed = make(chan struct{})
	close(job.done)
	visible := commands.jobs[job.view.ID] == job
	if job.discard {
		commands.forget(job)
	}
	job.cancel()
	commands.mu.Unlock()
	if visible {
		commands.notify(job.view.ThreadID)
	}
}
func (commands *Commands) own(thread, id string) *job {
	job := commands.jobs[id]
	if job != nil && job.view.ThreadID == thread {
		return job
	}
	return nil
}
func (commands *Commands) Read(ctx context.Context, thread, id string, wait time.Duration, limit int) (*Output, error) {
	commands.mu.Lock()
	job := commands.own(thread, id)
	if job == nil {
		commands.mu.Unlock()
		return nil, nil
	}
	if wait > 0 && job.view.Result == nil && job.output.read >= job.output.dropped+int64(job.output.length()) {
		if commands.waiters >= 1024 {
			commands.mu.Unlock()
			return nil, errors.New("too many background readers")
		}
		commands.waiters++
		changed := job.changed
		commands.mu.Unlock()
		timer := time.NewTimer(min(wait, 30*time.Second))
		select {
		case <-ctx.Done():
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
		commands.mu.Lock()
		commands.waiters--
		if commands.own(thread, id) != job {
			commands.mu.Unlock()
			return nil, nil
		}
	}
	if err := ctx.Err(); err != nil {
		commands.mu.Unlock()
		return nil, err
	}
	text, missed, more := job.output.take(limit)
	view := copyView(job.view)
	commands.mu.Unlock()
	return &Output{Command: view, Text: text, Missed: missed, More: more}, nil
}
func (commands *Commands) Peek(thread, id string, chars int) *Peek {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	job := commands.own(thread, id)
	if job == nil {
		return nil
	}
	if chars <= 0 {
		chars = 20_000
	}
	chars = min(chars, 100_000)
	tail := textutil.TailUnits(textutil.ScreenText(job.output.tail(chars*2)), chars)
	return &Peek{Command: copyView(job.view), Tail: tail}
}
func (commands *Commands) Wait(ctx context.Context, thread, id string) (*Command, error) {
	commands.mu.Lock()
	job := commands.own(thread, id)
	if job == nil {
		commands.mu.Unlock()
		return nil, nil
	}
	if commands.waiters >= 1024 {
		commands.mu.Unlock()
		return nil, errors.New("too many waiting command readers")
	}
	commands.waiters++
	done := job.done
	commands.mu.Unlock()
	select {
	case <-ctx.Done():
		commands.mu.Lock()
		commands.waiters--
		commands.mu.Unlock()
		return nil, ctx.Err()
	case <-done:
	}
	commands.mu.Lock()
	defer commands.mu.Unlock()
	commands.waiters--
	if commands.own(thread, id) != job {
		return nil, nil
	}
	view := copyView(job.view)
	return &view, nil
}
func (commands *Commands) Stop(ctx context.Context, thread, id string) (*Command, error) {
	commands.mu.Lock()
	job := commands.own(thread, id)
	if job == nil {
		commands.mu.Unlock()
		return nil, nil
	}
	job.cancel()
	commands.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return commands.Wait(ctx, thread, id)
}
func (commands *Commands) StopThread(ctx context.Context, thread string, keepAsked bool) error {
	commands.mu.Lock()
	jobs := []*job{}
	for _, id := range commands.order {
		job := commands.jobs[id]
		if job.view.ThreadID != thread {
			continue
		}
		job.kept = keepAsked && job.view.Keep && job.view.Result == nil
		if !job.kept {
			job.cancel()
			jobs = append(jobs, job)
		}
	}
	commands.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-job.done:
		}
	}
	commands.mu.Lock()
	for _, job := range jobs {
		commands.forget(job)
	}
	commands.mu.Unlock()
	if len(jobs) > 0 {
		commands.notify(thread)
	}
	return nil
}

// CancelThread releases output when the children have exited. It lets the event
// scheduler request cleanup without waiting for processes or holding a turn slot.
func (commands *Commands) CancelThread(thread string, keepAsked bool) {
	commands.mu.Lock()
	var finished []*job
	for _, id := range commands.order {
		job := commands.jobs[id]
		if job.view.ThreadID != thread || job.discard {
			continue
		}
		job.kept = keepAsked && job.view.Keep && job.view.Result == nil
		if job.kept {
			continue
		}
		job.discard = true
		if job.view.Result == nil {
			job.cancel()
		} else {
			finished = append(finished, job)
		}
	}
	for _, job := range finished {
		commands.forget(job)
	}
	commands.mu.Unlock()
	if len(finished) != 0 {
		commands.notify(thread)
	}
}
func (commands *Commands) TakeKept(thread string) []Command {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	out := []Command{}
	for _, id := range commands.order {
		job := commands.jobs[id]
		if job.view.ThreadID == thread && job.kept {
			job.kept = false
			if job.view.Result == nil {
				out = append(out, copyView(job.view))
			}
		}
	}
	return out
}
func (commands *Commands) Purge() {
	commands.mu.Lock()
	if commands.closed {
		commands.mu.Unlock()
		return
	}
	changed := map[string]bool{}
	for _, job := range commands.jobs {
		if job.view.Result != nil && commands.options.Now().UnixMilli()-job.view.Result.EndedAt >= commands.options.FinishedTTL.Milliseconds() {
			changed[job.view.ThreadID] = true
			commands.forget(job)
		}
	}
	commands.mu.Unlock()
	for thread := range changed {
		commands.notify(thread)
	}
}
func (commands *Commands) Close(ctx context.Context) error {
	commands.mu.Lock()
	if !commands.closed {
		commands.closed = true
		commands.cancel()
		close(commands.stopSweep)
		go func() {
			commands.workers.Wait()
			<-commands.sweepDone
			commands.mu.Lock()
			for _, job := range commands.jobs {
				commands.forget(job)
			}
			commands.mu.Unlock()
			commands.notify("")
			commands.mu.Lock()
			commands.options.OnChange = nil
			commands.options.Now = time.Now
			commands.mu.Unlock()
			close(commands.done)
		}()
	}
	commands.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-commands.done:
		return nil
	}
}
