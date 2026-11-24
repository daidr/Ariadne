package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/files"
)

const (
	maxPrototypeBytes        = 512 * 1024
	inlinePrototypeThreshold = 32 * 1024
	maxPrototypeTweaks       = 32
	prototypeHeaderHeight    = 36.0
)

var (
	prototypeTweakKeyRe     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	prototypeTweaksTagRe    = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	prototypeTweaksAttrRe   = regexp.MustCompile(`(?i)\bdata-ariadne-tweaks\b`)
	prototypeJSONTypeAttrRe = regexp.MustCompile(`(?i)\btype\s*=\s*["']?application/json["']?`)
)

type boardPrototype struct {
	title          string
	note           string
	hasNote        bool
	html           string
	src            string
	viewportWidth  int
	viewportHeight int
	preview        string
	tweaks         []args
}

func (turn *turn) resolvePrototype(ctx context.Context, input args) (boardPrototype, error) {
	proto := boardPrototype{
		title:          strings.TrimSpace(text(input, "title")),
		viewportWidth:  1280,
		viewportHeight: 800,
	}
	if note, ok := input["note"].(string); ok {
		proto.note = note
		proto.hasNote = true
	}
	if rawW, has := input["viewportWidth"]; has && rawW != nil {
		w, err := artifactInteger(rawW, "viewportWidth")
		if err != nil || w < 240 || w > 3840 {
			return proto, errors.New("viewportWidth must be an integer between 240 and 3840")
		}
		proto.viewportWidth = int(w)
	}
	if rawH, has := input["viewportHeight"]; has && rawH != nil {
		h, err := artifactInteger(rawH, "viewportHeight")
		if err != nil || h < 240 || h > 3840 {
			return proto, errors.New("viewportHeight must be an integer between 240 and 3840")
		}
		proto.viewportHeight = int(h)
	}

	rawHTML, hasHTML := input["html"].(string)
	fileID := strings.TrimPrefix(strings.TrimSpace(text(input, "file")), "file:")
	filePath := strings.TrimSpace(text(input, "path"))

	var htmlBytes []byte
	var existingSrc string

	switch {
	case hasHTML && strings.TrimSpace(rawHTML) != "":
		htmlBytes = []byte(rawHTML)
	case fileID != "":
		store := files.Store{Events: turn.Repository.Store}
		info, err := store.Info(ctx, turn.conversation.ProjectID, fileID)
		if err != nil {
			return proto, err
		}
		if info == nil || info.Size > maxPrototypeBytes {
			return proto, errors.New("the prototype file must exist in this project and be under 512 KiB")
		}
		content, err := store.Read(ctx, fileID)
		if err != nil {
			return proto, err
		}
		if !files.IsText(content.Data) {
			return proto, errors.New("the prototype file must be UTF-8 text")
		}
		htmlBytes = content.Data
		existingSrc = "/api/files/" + fileID
	case filePath != "":
		if turn.conversation.Role != "thread" || turn.Workspaces == nil {
			return proto, errors.New("prototype path requires a thread workspace")
		}
		ext := strings.ToLower(filepath.Ext(filePath))
		if ext != ".html" && ext != ".htm" {
			return proto, errors.New("prototype workspace path must be an .html or .htm file")
		}
		view, err := turn.source(ctx, args{})
		if err != nil {
			return proto, err
		}
		data, err := view.ReadBytes(ctx, filePath, maxPrototypeBytes)
		if err != nil {
			return proto, err
		}
		if !files.IsText(data) {
			return proto, errors.New("prototype file must be UTF-8 text")
		}
		htmlBytes = data
	default:
		return proto, errors.New("prototype requires html, file, or path")
	}

	if len(htmlBytes) == 0 || len(htmlBytes) > maxPrototypeBytes || !utf8.Valid(htmlBytes) {
		return proto, errors.New("prototype HTML must be valid UTF-8 under 512 KiB")
	}

	htmlText := string(htmlBytes)
	if len(htmlBytes) > inlinePrototypeThreshold {
		if existingSrc != "" {
			proto.src = existingSrc
		} else {
			store := files.Store{Events: turn.Repository.Store}
			stored, err := store.Save(ctx, turn.conversation.ProjectID, "text/plain", htmlBytes, true)
			if err != nil {
				return proto, err
			}
			proto.src = stored.URL
		}
	} else {
		proto.html = htmlText
	}

	if previewRaw, ok := input["preview"].(args); ok && previewRaw != nil {
		pic, err := turn.boardPicture(ctx, previewRaw)
		if err != nil {
			return proto, fmt.Errorf("preview: %w", err)
		}
		proto.preview = pic.src
	}

	var rawTweaks any
	if input["tweaks"] != nil {
		rawTweaks = input["tweaks"]
	} else if extracted := extractTweaksFromHTML(htmlText); extracted != nil {
		rawTweaks = extracted
	}
	if rawTweaks != nil {
		tweaks, err := normalizePrototypeTweaks(rawTweaks)
		if err != nil {
			return proto, fmt.Errorf("tweaks: %w", err)
		}
		proto.tweaks = tweaks
	}

	return proto, nil
}

func extractTweaksFromHTML(htmlText string) any {
	matches := prototypeTweaksTagRe.FindAllStringSubmatch(htmlText, -1)
	for _, m := range matches {
		attrs, body := m[1], strings.TrimSpace(m[2])
		if !prototypeTweaksAttrRe.MatchString(attrs) || !prototypeJSONTypeAttrRe.MatchString(attrs) {
			continue
		}
		if body == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			continue
		}
		if obj, ok := decoded.(map[string]any); ok {
			if list, ok := obj["tweaks"]; ok {
				return list
			}
		}
		return decoded
	}
	return nil
}

func normalizePrototypeTweaks(raw any) ([]args, error) {
	items, err := artifactObjects(raw)
	if err != nil {
		return nil, errors.New("tweaks must be an array of parameter definitions")
	}
	if len(items) > maxPrototypeTweaks {
		return nil, fmt.Errorf("at most %d tweaks are allowed per prototype", maxPrototypeTweaks)
	}
	out := make([]args, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		key := strings.TrimSpace(text(item, "key"))
		if !prototypeTweakKeyRe.MatchString(key) {
			return nil, fmt.Errorf("tweak key %q must match [a-zA-Z0-9_-]{1,64}", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate tweak key %q", key)
		}
		seen[key] = true

		label := strings.TrimSpace(text(item, "label"))
		if label == "" {
			label = key
		}
		kind := strings.ToLower(strings.TrimSpace(text(item, "kind")))
		if kind == "" {
			switch {
			case item["options"] != nil:
				kind = "enum"
			case item["min"] != nil || item["max"] != nil:
				kind = "range"
			case item["default"] != nil:
				if _, isBool := item["default"].(bool); isBool {
					kind = "boolean"
				} else if _, isNum := item["default"].(float64); isNum {
					kind = "range"
				} else if s, isStr := item["default"].(string); isStr && isHexColor(strings.TrimSpace(s)) {
					kind = "color"
				} else {
					kind = "text"
				}
			default:
				return nil, fmt.Errorf("tweak %q needs kind (color, enum, range, boolean or text)", key)
			}
		}
		if !slices.Contains([]string{"color", "enum", "range", "boolean", "text"}, kind) {
			return nil, fmt.Errorf("tweak %q has invalid kind %q", key, kind)
		}

		spec := args{
			"key":   key,
			"label": label,
			"kind":  kind,
		}
		if sec := strings.TrimSpace(text(item, "section")); sec != "" {
			spec["section"] = sec
		}

		switch kind {
		case "boolean":
			def, ok := item["default"].(bool)
			if !ok {
				def = false
			}
			spec["default"] = def
		case "color":
			def := strings.TrimSpace(text(item, "default"))
			if def == "" {
				def = "#2563eb"
			}
			spec["default"] = def
			if rawSwatches, ok := item["swatches"].([]any); ok && len(rawSwatches) > 0 {
				swatches := make([]string, 0, min(len(rawSwatches), 16))
				for _, sw := range rawSwatches {
					if s, ok := sw.(string); ok && strings.TrimSpace(s) != "" {
						swatches = append(swatches, strings.TrimSpace(s))
						if len(swatches) >= 16 {
							break
						}
					}
				}
				if len(swatches) > 0 {
					spec["swatches"] = swatches
				}
			}
		case "range":
			minVal, hasMin := item["min"].(float64)
			maxVal, hasMax := item["max"].(float64)
			if !hasMin {
				minVal = 0
			}
			if !hasMax {
				maxVal = 100
			}
			if maxVal <= minVal {
				return nil, fmt.Errorf("tweak %q max must be greater than min", key)
			}
			step := 1.0
			if s, ok := item["step"].(float64); ok && s > 0 {
				step = s
			}
			def := minVal
			if d, ok := item["default"].(float64); ok && !math.IsNaN(d) && !math.IsInf(d, 0) {
				def = min(max(d, minVal), maxVal)
			}
			spec["min"] = minVal
			spec["max"] = maxVal
			spec["step"] = step
			spec["default"] = def
			if unit := strings.TrimSpace(text(item, "unit")); unit != "" {
				spec["unit"] = unit
			}
		case "enum":
			rawOpts, ok := item["options"].([]any)
			if !ok || len(rawOpts) == 0 {
				return nil, fmt.Errorf("enum tweak %q needs non-empty options", key)
			}
			opts := make([]args, 0, min(len(rawOpts), 20))
			values := make([]string, 0, len(rawOpts))
			for _, rawOpt := range rawOpts {
				switch v := rawOpt.(type) {
				case string:
					s := strings.TrimSpace(v)
					if s != "" {
						opts = append(opts, args{"value": s, "label": s})
						values = append(values, s)
					}
				case map[string]any:
					val := strings.TrimSpace(text(v, "value"))
					if val == "" {
						continue
					}
					lbl := strings.TrimSpace(text(v, "label"))
					if lbl == "" {
						lbl = val
					}
					opts = append(opts, args{"value": val, "label": lbl})
					values = append(values, val)
				}
				if len(opts) >= 20 {
					break
				}
			}
			if len(opts) == 0 {
				return nil, fmt.Errorf("enum tweak %q needs at least one valid option", key)
			}
			def := strings.TrimSpace(text(item, "default"))
			if def == "" || !slices.Contains(values, def) {
				def = values[0]
			}
			spec["options"] = opts
			spec["default"] = def
		case "text":
			def := strings.TrimSpace(text(item, "default"))
			if runes := []rune(def); len(runes) > 200 {
				def = string(runes[:200])
			}
			spec["default"] = def
		}
		out = append(out, spec)
	}
	return out, nil
}

func isHexColor(s string) bool {
	if !strings.HasPrefix(s, "#") {
		return false
	}
	n := len(s) - 1
	if n != 3 && n != 4 && n != 6 && n != 8 {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func prototypeCardSize(viewportWidth, viewportHeight int, targetWidth float64) (float64, float64) {
	vw := float64(max(viewportWidth, 240))
	vh := float64(max(viewportHeight, 240))
	if targetWidth > 0 {
		bodyH := math.Round(targetWidth * vh / vw)
		bodyH = min(max(bodyH, 180.0), 620.0)
		return targetWidth, bodyH + prototypeHeaderHeight
	}
	if vw >= vh {
		w := 480.0
		bodyH := min(max(math.Round(w*vh/vw), 220.0), 420.0)
		return w, bodyH + prototypeHeaderHeight
	}
	w := 280.0
	bodyH := min(max(math.Round(w*vh/vw), 320.0), 560.0)
	return w, bodyH + prototypeHeaderHeight
}

func prototypeNodeData(proto boardPrototype, existing args) args {
	data := objectField(existing, "data")
	delete(data, "shape")
	delete(data, "tone")
	if proto.title != "" {
		data["title"] = proto.title
	}
	if proto.hasNote {
		if proto.note == "" {
			delete(data, "note")
		} else {
			data["note"] = proto.note
		}
	}
	if proto.html != "" {
		data["html"] = proto.html
		delete(data, "src")
	} else if proto.src != "" {
		data["src"] = proto.src
		delete(data, "html")
	}
	data["viewportWidth"] = proto.viewportWidth
	data["viewportHeight"] = proto.viewportHeight
	if proto.preview != "" {
		data["preview"] = proto.preview
	}
	if len(proto.tweaks) > 0 {
		data["tweaks"] = proto.tweaks
	} else {
		delete(data, "tweaks")
	}
	return data
}
