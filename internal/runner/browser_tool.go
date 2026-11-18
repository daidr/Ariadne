package runner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"

	"github.com/daidr/Ariadne/internal/browser"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/transcript"
)

func (turn *turn) browserTool() tool {
	actions := []string{"open", "snapshot", "click", "type", "press", "select", "hover", "scroll", "wait", "screenshot", "console", "network", "evaluate", "back", "reload", "close"}
	entry := specification("browser", "Use an isolated headless browser, including a local web app you started. Open a URL, then take a snapshot for numbered elements. Screenshots can be attached to a message.",
		args{"action": args{"type": "string", "enum": actions}, "url": field("string"), "width": field("integer"), "height": field("integer"),
			"colorScheme": args{"type": "string", "enum": []string{"light", "dark"}}, "ref": field("integer"), "text": field("string"),
			"append": field("boolean"), "option": field("string"), "key": field("string"), "modifiers": arrayOf(field("string")),
			"by": field("integer"), "ms": field("integer"), "fullPage": field("boolean"), "all": field("boolean"), "script": field("string")},
		[]string{"action"}, nil)
	entry.call = func(ctx context.Context, input args) (string, []any, error) {
		action, err := browserAction(input, actions)
		if err != nil {
			return "", nil, err
		}
		conversation := turn.conversation
		if action.Name != "close" {
			project, err := turn.Repository.GetProject(ctx, conversation.ProjectID)
			if err != nil {
				return "", nil, err
			}
			if slices.Contains(project.Settings.Tools, "browser") {
				return "", nil, errors.New("the browser is disabled for this project")
			}
		}
		result, err := turn.Browser.Do(ctx, conversation.ProjectID+"/"+conversation.ID, action)
		if err != nil {
			return "", nil, err
		}
		output := transcript.Fenced("browser_output", transcript.Defang(result.Text), "").Text
		if len(result.Data) == 0 {
			return output, nil, nil
		}
		store := files.Store{Events: turn.Repository.Store}
		saved, err := store.Save(ctx, conversation.ProjectID, result.MIME, result.Data, false)
		if err != nil {
			return "", nil, err
		}
		name := "screenshot.png"
		if saved.MIME == "image/jpeg" {
			name = "screenshot.jpg"
		}
		return output + "\nScreenshot file ID: " + saved.ID, []any{args{"fileId": saved.ID, "name": name, "mime": saved.MIME, "size": saved.Size}}, nil
	}
	return entry
}

func browserAction(input args, actions []string) (browser.Action, error) {
	name := text(input, "action")
	action := browser.Action{Name: name, URL: text(input, "url"), ColorScheme: text(input, "colorScheme"), Key: text(input, "key"),
		Option: text(input, "option"), Script: text(input, "script"), Width: 1280, Height: 800, By: 600, Millis: 5000}
	if !slices.Contains(actions, name) {
		return action, fmt.Errorf("unknown browser action %q", name)
	}
	for key, target := range map[string]*int{"width": &action.Width, "height": &action.Height, "by": &action.By, "ms": &action.Millis, "ref": &action.Ref} {
		if value := input[key]; value != nil {
			number, ok := value.(float64)
			if !ok || number != math.Trunc(number) || math.Abs(number) > 30000 {
				return action, fmt.Errorf("%s must be a whole number within range", key)
			}
			*target = int(number)
		}
	}
	action.Append, _ = input["append"].(bool)
	action.FullPage, _ = input["fullPage"].(bool)
	action.All, _ = input["all"].(bool)
	action.Text, _ = input["text"].(string)
	var err error
	action.Modifiers, err = stringsOf(input["modifiers"])
	if err != nil {
		return action, err
	}
	if action.Millis < 0 || len(action.Text) > 32<<10 || len(action.Script) > 32<<10 {
		return action, errors.New("browser text or script is too long, or wait is out of range")
	}
	if name == "open" && action.URL != "about:blank" {
		address, err := url.Parse(action.URL)
		if err != nil || address.Hostname() == "" || address.Scheme != "http" && address.Scheme != "https" || len(action.URL) > 8192 {
			return action, errors.New("an HTTP or HTTPS URL is required")
		}
	}
	if action.ColorScheme != "" && action.ColorScheme != "light" && action.ColorScheme != "dark" {
		return action, errors.New("colorScheme must be light or dark")
	}
	return action, nil
}
