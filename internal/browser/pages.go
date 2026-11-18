// Package browser owns the private Chromium profiles used by discussion threads.
package browser

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/daidr/Ariadne/internal/textutil"
)

const maxPages = 8
const idle = 10 * time.Minute

type Pages struct {
	ctx    context.Context
	stop   context.CancelFunc
	bin    *locator
	track  func(pid int, group bool, label string) (func(), error)
	mu     sync.Mutex
	pages  map[string]*Page
	closed bool
}

type Page struct {
	guard   chan struct{}
	life    context.Context
	ctx     context.Context
	stop    context.CancelFunc
	cleanup func()
	idle    *time.Timer
	expires time.Time
	refs    map[int]cdp.BackendNodeID

	logMu   sync.Mutex
	console messages
	network messages
	dialogs messages
}

type messages struct {
	lines  []string
	unread int
}

func (m *messages) add(value string) {
	if len(m.lines) >= 200 {
		copy(m.lines, m.lines[1:])
		m.lines = m.lines[:len(m.lines)-1]
	}
	m.lines = append(m.lines, textutil.HeadUnits(value, 2000))
	m.unread = min(m.unread+1, len(m.lines))
}

func (m *messages) read(all bool) string {
	start := len(m.lines) - m.unread
	if all {
		start = 0
	}
	result := strings.Join(m.lines[start:], "\n")
	m.unread = 0
	return result
}

func New(ctx context.Context, home string, track func(int, bool, string) (func(), error)) *Pages {
	life, stop := context.WithCancel(ctx)
	return &Pages{ctx: life, stop: stop, track: track, bin: &locator{home: home}, pages: map[string]*Page{}}
}

func (pages *Pages) Use(ctx context.Context, id string, open bool, work func(context.Context, *Page) error) error {
	pages.mu.Lock()
	if pages.closed || pages.ctx.Err() != nil {
		pages.mu.Unlock()
		return errors.New("browser service is closed")
	}
	p := pages.pages[id]
	if p == nil && !open {
		pages.mu.Unlock()
		return errors.New("open a page before using the browser")
	}
	if p == nil {
		if len(pages.pages) >= maxPages {
			pages.mu.Unlock()
			return errors.New("there are too many open browsers")
		}
		life, stop := context.WithCancel(pages.ctx)
		p = &Page{life: life, stop: stop, guard: make(chan struct{}, 1)}
		pages.pages[id] = p
	}
	pages.mu.Unlock()
	select {
	case p.guard <- struct{}{}:
		defer func() { <-p.guard }()
	case <-p.life.Done():
		return errors.New("the page closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	pages.mu.Lock()
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	active := pages.pages[id] == p
	pages.mu.Unlock()
	if !active {
		return errors.New("the page closed")
	}
	if p.cleanup == nil {
		if err := p.start(ctx, pages); err != nil {
			pages.remove(id, p)
			return err
		}
	}
	call, cancel := context.WithCancel(p.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	// This operation borrows the browser context without inheriting its caller's
	// cancellation for all future operations.
	err := work(call, p)
	pages.mu.Lock()
	if pages.pages[id] == p {
		p.expires = time.Now().Add(idle)
		p.idle = time.AfterFunc(idle, func() { pages.expire(id, p) })
	}
	pages.mu.Unlock()
	return err
}

func (p *Page) start(ctx context.Context, pages *Pages) error {
	boot, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	drop := context.AfterFunc(p.life, cancel)
	defer drop()
	executable, err := pages.bin.find(boot)
	if err != nil {
		return err
	}
	opts := slices.Clone(chromedp.DefaultExecAllocatorOptions[:])
	opts = append(opts, chromedp.ExecPath(executable), chromedp.Flag("disable-extensions", true))
	processLife, stopAllocator := chromedp.NewExecAllocator(p.life, opts...)
	tab, stopTab := chromedp.NewContext(processLife, chromedp.WithErrorf(func(string, ...any) {}))
	stop := context.AfterFunc(boot, p.stop)
	_, err = chromedp.Run(tab, chromedp.Location())
	inTime := stop() && boot.Err() == nil
	if err != nil || !inTime {
		stopTab()
		stopAllocator()
		if err != nil {
			return err
		}
		return context.Canceled
	}
	var forget func()
	if process := chromedp.FromContext(tab).Browser.Process(); pages.track != nil && process != nil {
		forget, err = pages.track(process.Pid, false, "browser")
		if err != nil {
			stopTab()
			stopAllocator()
			return err
		}
	}
	p.ctx = tab
	p.cleanup = func() {
		stopTab()
		stopAllocator()
		if forget != nil {
			forget()
		}
	}
	p.monitor(tab)
	return nil
}

func receive[T any](events iter.Seq2[T, error], handle func(T)) {
	for event, err := range events {
		if err != nil {
			return
		}
		handle(event)
	}
}

func (p *Page) monitor(ctx context.Context) {
	console := chromedp.Events(ctx, runtime.ConsoleAPICalled)
	go receive(console, func(e runtime.EventConsoleAPICalled) {
		words := make([]string, 0, min(len(e.Args), 16))
		for _, value := range e.Args[:min(len(e.Args), 16)] {
			if value == nil {
				continue
			}
			part := value.Description
			if len(value.Value) > 0 {
				part = string(value.Value)
			}
			words = append(words, textutil.HeadUnits(part, 500))
		}
		p.logMu.Lock()
		p.console.add(string(e.Type) + " " + strings.Join(words, " "))
		p.logMu.Unlock()
	})
	exceptions := chromedp.Events(ctx, runtime.ExceptionThrown)
	go receive(exceptions, func(e runtime.EventExceptionThrown) {
		p.logMu.Lock()
		p.console.add("error " + e.ExceptionDetails.Text)
		p.logMu.Unlock()
	})
	responses := chromedp.Events(ctx, network.ResponseReceived)
	go receive(responses, func(e network.EventResponseReceived) {
		if e.Response != nil && e.Response.Status >= 400 {
			p.logMu.Lock()
			p.network.add(e.Response.StatusText + " " + e.Response.URL)
			p.logMu.Unlock()
		}
	})
	failed := chromedp.Events(ctx, network.LoadingFailed)
	go receive(failed, func(e network.EventLoadingFailed) {
		if !e.Canceled {
			p.logMu.Lock()
			p.network.add(e.ErrorText + " (" + string(e.Type) + ")")
			p.logMu.Unlock()
		}
	})
	dialogs := chromedp.Events(ctx, page.JavascriptDialogOpening)
	go receive(dialogs, func(e page.EventJavascriptDialogOpening) {
		p.logMu.Lock()
		p.dialogs.add(string(e.Type) + ": " + e.Message)
		p.logMu.Unlock()
		req, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, _ = chromium(req, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: true})
	})
}

func (pages *Pages) remove(id string, p *Page) {
	p.stop()
	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
	pages.mu.Lock()
	if pages.pages[id] == p {
		delete(pages.pages, id)
	}
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	pages.mu.Unlock()
}

func (pages *Pages) expire(id string, p *Page) {
	select {
	case p.guard <- struct{}{}:
		defer func() { <-p.guard }()
	default:
		return
	}
	pages.mu.Lock()
	armed := pages.pages[id] == p && p.idle != nil && !time.Now().Before(p.expires)
	pages.mu.Unlock()
	if armed {
		pages.remove(id, p)
	}
}

func (pages *Pages) ClosePage(id string) bool {
	pages.mu.Lock()
	p := pages.pages[id]
	pages.mu.Unlock()
	if p == nil {
		return false
	}
	p.stop()
	p.guard <- struct{}{}
	defer func() { <-p.guard }()
	pages.remove(id, p)
	return true
}

func (pages *Pages) DropThread(id string) {
	pages.dropMatching(func(key string) bool { return strings.HasSuffix(key, "/"+id) })
}

func (pages *Pages) DropProject(id string) {
	pages.dropMatching(func(key string) bool { return strings.HasPrefix(key, id+"/") })
}

func (pages *Pages) dropMatching(match func(string) bool) {
	pages.mu.Lock()
	defer pages.mu.Unlock()
	for id, p := range pages.pages {
		if match(id) && p.life.Err() == nil {
			p.stop()
			go pages.ClosePage(id)
		}
	}
}

func (pages *Pages) Close(ctx context.Context) error {
	pages.mu.Lock()
	pages.closed = true
	all := make([]string, 0, len(pages.pages))
	for id := range pages.pages {
		all = append(all, id)
	}
	pages.mu.Unlock()
	pages.stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range all {
			pages.ClosePage(id)
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func chromium[P, R any](ctx context.Context, cmd cdp.Command[P, R], params P) (R, error) {
	return chromedp.Run(ctx, func(ctx context.Context, tab *chromedp.Target) (R, error) {
		return cdp.Call(ctx, tab, cmd, params)
	})
}
