package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	js "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/daidr/Ariadne/internal/textutil"
)

type Action struct {
	Name, URL, ColorScheme, Text, Key, Option, Script string
	Width, Height, Ref, By, Millis                    int
	Modifiers                                         []string
	Append, FullPage, All                             bool
}

type Result struct {
	Text string
	MIME string
	Data []byte
}

func (pages *Pages) Do(ctx context.Context, id string, action Action) (result Result, err error) {
	if action.Name == "close" {
		if pages.ClosePage(id) {
			return Result{Text: "Closed."}, nil
		}
		return Result{Text: "No page was open."}, nil
	}
	err = pages.Use(ctx, id, action.Name == "open", func(ctx context.Context, p *Page) error {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, err = p.act(ctx, action)
		if err != nil {
			return err
		}
		address, _ := chromedp.Run(ctx, chromedp.Location())
		if address != "" {
			result.Text += "\nPage: " + textutil.HeadUnits(address, 4096)
		}
		p.logMu.Lock()
		if action.Name != "console" && p.console.unread > 0 {
			result.Text += fmt.Sprintf("\n%d new console messages.", p.console.unread)
		}
		if action.Name != "network" && p.network.unread > 0 {
			result.Text += fmt.Sprintf("\n%d new failed requests.", p.network.unread)
		}
		if dialogs := p.dialogs.read(false); dialogs != "" {
			result.Text += "\nAccepted browser dialogs:\n" + dialogs
		}
		p.logMu.Unlock()
		result.Text = strings.TrimSpace(result.Text)
		return nil
	})
	return result, err
}

func (p *Page) act(ctx context.Context, action Action) (Result, error) {
	run := func(work chromedp.Action[chromedp.Void]) (Result, error) {
		_, err := chromedp.Run(ctx, work)
		return Result{Text: "Done."}, err
	}
	switch action.Name {
	case "open":
		if action.Width < 320 || action.Width > 3840 || action.Height < 240 || action.Height > 2160 {
			return Result{}, errors.New("viewport must be between 320×240 and 3840×2160")
		}
		p.refs = nil
		_, err := chromium(ctx, emulation.SetDeviceMetricsOverride,
			emulation.SetDeviceMetricsOverrideParams{Width: int64(action.Width), Height: int64(action.Height), DeviceScaleFactor: 1})
		if err != nil {
			return Result{}, err
		}
		media := emulation.SetEmulatedMediaParams{}
		if action.ColorScheme != "" {
			media.Features = []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: action.ColorScheme}}
		}
		if _, err := chromium(ctx, emulation.SetEmulatedMedia, media); err != nil {
			return Result{}, err
		}
		return run(chromedp.Navigate(action.URL))
	case "back":
		p.refs = nil
		return run(chromedp.NavigateBack())
	case "reload":
		p.refs = nil
		return run(chromedp.Reload())
	case "snapshot":
		text, err := p.snapshot(ctx)
		return Result{Text: text}, err
	case "click", "hover":
		clip, err := p.box(ctx, action.Ref)
		if err != nil {
			return Result{}, err
		}
		x, y := clip.X+clip.Width/2, clip.Y+clip.Height/2
		if action.Name == "click" {
			return run(chromedp.MouseClickXY(x, y))
		}
		_, err = chromium(ctx, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: "mouseMoved", X: x, Y: y})
		return Result{Text: "Hovering."}, err
	case "type":
		id, err := p.ref(action.Ref)
		if err != nil {
			return Result{}, err
		}
		if _, err := chromium(ctx, dom.Focus, dom.FocusParams{BackendNodeID: id}); err != nil {
			return Result{}, errors.New("the element is gone or cannot be focused. Take another snapshot")
		}
		if action.Append {
			_, err = chromedp.Run(ctx, chromedp.KeyEvent(kb.End))
		} else {
			key := chromedp.ModifierCtrl
			if runtime.GOOS == "darwin" {
				key = chromedp.ModifierMeta
			}
			_, err = chromedp.Run(ctx, chromedp.KeyEvent("a", chromedp.KeyModifiers(key)))
		}
		if err != nil {
			return Result{}, err
		}
		_, err = chromium(ctx, input.InsertText, input.InsertTextParams{Text: action.Text})
		return Result{Text: "Typed."}, err
	case "press":
		return p.press(ctx, action)
	case "scroll":
		if action.Ref != 0 {
			id, err := p.ref(action.Ref)
			if err != nil {
				return Result{}, err
			}
			_, err = chromium(ctx, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: id})
			return Result{Text: "Scrolled to the element."}, err
		}
		return run(chromedp.Evaluate[chromedp.Void](fmt.Sprintf("scrollBy(0,%d)", action.By)))
	case "wait":
		wait, cancel := context.WithTimeout(ctx, time.Duration(action.Millis)*time.Millisecond)
		defer cancel()
		if action.Text == "" {
			<-wait.Done()
			return Result{Text: "Waited."}, ctx.Err()
		}
		text, _ := json.Marshal(action.Text)
		_, err := chromedp.Run(wait, chromedp.Poll[bool]("document.body?.innerText.includes("+string(text)+")"))
		return Result{Text: "Text found."}, err
	case "select":
		return p.selectOption(ctx, action.Ref, action.Option)
	case "evaluate":
		source, _ := json.Marshal(action.Script)
		out, err := chromedp.Run(ctx, chromedp.Evaluate[string]("(async()=>{const v=await(0,eval)("+string(source)+");return (JSON.stringify(v)??'null').slice(0,20000)})()", chromedp.EvalAwaitPromise))
		return Result{Text: out}, err
	case "console", "network":
		p.logMu.Lock()
		defer p.logMu.Unlock()
		log := &p.console
		if action.Name == "network" {
			log = &p.network
		}
		result := log.read(action.All)
		if result == "" {
			result = "No messages."
		}
		return Result{Text: textutil.HeadUnits(result, 20000)}, nil
	case "screenshot":
		return p.screenshot(ctx, action)
	}
	return Result{}, fmt.Errorf("unknown browser action %q", action.Name)
}

func (p *Page) snapshot(ctx context.Context) (string, error) {
	p.refs = nil
	tree, err := chromium(ctx, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{Depth: new(int64(48))})
	if err != nil {
		return "", err
	}
	p.refs = map[int]cdp.BackendNodeID{}
	var out strings.Builder
	lines := 0
	for _, node := range tree.Nodes {
		if node == nil || node.Ignored {
			continue
		}
		role, name, value := axValue(node.Role), axValue(node.Name), axValue(node.Value)
		var ref string
		switch role {
		case "button", "link", "textbox", "searchbox", "checkbox", "radio", "combobox", "listbox", "option", "tab", "switch",
			"menuitem", "menuitemcheckbox", "menuitemradio", "slider", "spinbutton", "treeitem":
			if node.BackendDOMNodeID != 0 {
				index := len(p.refs) + 1
				p.refs[index] = node.BackendDOMNodeID
				ref = fmt.Sprintf("[%d] ", index)
			}
		case "InlineTextBox":
			continue
		}
		if ref == "" && name == "" && value == "" {
			continue
		}
		fmt.Fprintf(&out, "%s%s %s", ref, role, textutil.HeadUnits(strings.Join(strings.Fields(name), " "), 500))
		if value != "" && value != name {
			fmt.Fprintf(&out, " = %s", textutil.HeadUnits(strings.Join(strings.Fields(value), " "), 500))
		}
		out.WriteByte('\n')
		lines++
		if lines == 400 || out.Len() > 20000 {
			out.WriteString("[Snapshot truncated]\n")
			break
		}
	}
	return out.String(), nil
}

func axValue(value *accessibility.Value) string {
	if value == nil {
		return ""
	}
	var text string
	_ = json.Unmarshal(value.Value, &text)
	return text
}

func (p *Page) ref(ref int) (cdp.BackendNodeID, error) {
	if id := p.refs[ref]; id != 0 {
		return id, nil
	}
	return 0, errors.New("use an element number from the last snapshot")
}

func (p *Page) box(ctx context.Context, ref int) (page.Viewport, error) {
	id, err := p.ref(ref)
	if err != nil {
		return page.Viewport{}, err
	}
	if _, err := chromium(ctx, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: id}); err != nil {
		return page.Viewport{}, errors.New("the element is gone or hidden. Take another snapshot")
	}
	data, err := chromium(ctx, dom.GetBoxModel, dom.GetBoxModelParams{BackendNodeID: id})
	if err != nil || data.Model == nil || len(data.Model.Border) < 8 {
		return page.Viewport{}, errors.New("the element has no visible bounds")
	}
	q := data.Model.Border
	x, y := min(q[0], q[2], q[4], q[6]), min(q[1], q[3], q[5], q[7])
	return page.Viewport{X: x, Y: y, Width: max(q[0], q[2], q[4], q[6]) - x, Height: max(q[1], q[3], q[5], q[7]) - y, Scale: 1}, nil
}

func (p *Page) press(ctx context.Context, action Action) (Result, error) {
	keys := map[string]string{
		"Enter": kb.Enter, "Escape": kb.Escape, "Tab": kb.Tab, "Backspace": kb.Backspace, "Delete": kb.Delete,
		"ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown, "ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight,
		"Home": kb.Home, "End": kb.End, "PageUp": kb.PageUp, "PageDown": kb.PageDown, "Space": " ",
	}
	key := keys[action.Key]
	if key == "" && utf8.RuneCountInString(action.Key) == 1 {
		key = action.Key
	}
	if key == "" {
		return Result{}, errors.New("unknown key")
	}
	var modifiers []chromedp.Modifier
	known := map[string]chromedp.Modifier{"Alt": chromedp.ModifierAlt, "Control": chromedp.ModifierCtrl, "Meta": chromedp.ModifierMeta, "Shift": chromedp.ModifierShift}
	for _, modifier := range action.Modifiers {
		if value, ok := known[modifier]; ok {
			modifiers = append(modifiers, value)
		} else {
			return Result{}, fmt.Errorf("unknown key modifier %q", modifier)
		}
	}
	_, err := chromedp.Run(ctx, chromedp.KeyEvent(key, chromedp.KeyModifiers(modifiers...)))
	return Result{Text: "Pressed."}, err
}

func (p *Page) selectOption(ctx context.Context, ref int, option string) (Result, error) {
	id, err := p.ref(ref)
	if err != nil {
		return Result{}, err
	}
	node, err := chromium(ctx, dom.ResolveNode, dom.ResolveNodeParams{BackendNodeID: id})
	if err != nil || node.Object == nil {
		return Result{}, errors.New("the element is gone. Take another snapshot")
	}
	defer chromium(ctx, js.ReleaseObject, js.ReleaseObjectParams{ObjectID: node.Object.ObjectID})
	value, _ := json.Marshal(option)
	changed, err := chromium(ctx, js.CallFunctionOn, js.CallFunctionOnParams{
		ObjectID: node.Object.ObjectID, ReturnByValue: new(true), Arguments: []*js.CallArgument{{Value: value}},
		FunctionDeclaration: `function(value) {
			if (!this.options) return false;
			const option = [...this.options].find(o => !o.disabled && (o.value === value || o.text.trim() === value));
			if (!option) return false;
			this.value = option.value;
			this.dispatchEvent(new Event('input', {bubbles:true}));
			this.dispatchEvent(new Event('change', {bubbles:true}));
			return true;
		}`,
	})
	if err != nil {
		return Result{}, err
	}
	if changed.Result == nil || string(changed.Result.Value) != "true" {
		return Result{}, errors.New("the requested option is unavailable")
	}
	return Result{Text: "Selected."}, nil
}

func (p *Page) screenshot(ctx context.Context, action Action) (Result, error) {
	params := page.CaptureScreenshotParams{FromSurface: new(true)}
	if action.Ref != 0 {
		clip, err := p.box(ctx, action.Ref)
		if err != nil {
			return Result{}, err
		}
		layout, err := chromium(ctx, page.GetLayoutMetrics, cdp.Empty{})
		if err != nil {
			return Result{}, err
		}
		if visible := layout.CSSVisualViewport; visible != nil {
			clip.X, clip.Y = clip.X+visible.PageX, clip.Y+visible.PageY
		}
		params.Clip = &clip
		params.CaptureBeyondViewport = new(true)
	} else if action.FullPage {
		layout, err := chromium(ctx, page.GetLayoutMetrics, cdp.Empty{})
		if err != nil {
			return Result{}, err
		}
		if size := layout.CSSContentSize; size != nil && size.Width > 0 && size.Height > 0 {
			params.Clip = &page.Viewport{X: size.X, Y: size.Y, Width: size.Width, Height: size.Height, Scale: 1}
		}
		params.CaptureBeyondViewport = new(true)
	}
	if params.Clip != nil {
		params.Clip.Width = min(3840, params.Clip.Width)
		params.Clip.Height = min(8000, params.Clip.Height)
	}
	image, err := chromium(ctx, page.CaptureScreenshot, params)
	mime := "image/png"
	if err == nil && len(image.Data) > 5<<20 {
		params.Format, params.Quality = "jpeg", new(int64(72))
		image, err = chromium(ctx, page.CaptureScreenshot, params)
		mime = "image/jpeg"
	}
	if err != nil {
		return Result{}, err
	}
	if len(image.Data) == 0 || len(image.Data) > 5<<20 {
		return Result{}, errors.New("the screenshot is too large. Capture a smaller area")
	}
	return Result{Text: "Screenshot attached.", MIME: mime, Data: image.Data}, nil
}
