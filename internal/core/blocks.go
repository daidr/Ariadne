package core

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

var blockTypes = []string{"choice", "confirm", "form", "table", "chart", "steps", "thread", "connector"}
var fieldKinds = []string{"text", "textarea", "number", "select", "multiselect", "switch"}
var blockKey = regexp.MustCompile(`^[A-Za-z_][\w-]{0,39}$`)
var secretName = regexp.MustCompile(`^[A-Za-z_][\w-]{0,63}$`)
var connectorURL = regexp.MustCompile(`(?i)^https?://[^` + jsSpace[1:] + `+$`)

type blockValidator struct{ err error }

func (v *blockValidator) invalid(reason string) {
	if v.err == nil {
		v.err = domainerr.New("error.blockInvalid", map[string]any{"reason": reason})
	}
}
func (v *blockValidator) only(value object, allowed []string, at string) {
	extras := []string{}
	for key := range value {
		if !slices.Contains(allowed, key) {
			extras = append(extras, key)
		}
	}
	jsSortKeys(extras)
	if len(extras) > 0 {
		v.invalid(at + " has unknown fields: " + strings.Join(extras, ", "))
	}
}
func (v *blockValidator) text(raw any, at string, max int) string {
	value, ok := raw.(string)
	if !ok || projectsettings.Trim(value) == "" {
		v.invalid(at + " must be a non-empty string")
		return ""
	}
	if utf16Length(value) > max {
		v.invalid(fmt.Sprintf("%s is longer than %d characters", at, max))
		return ""
	}
	return projectsettings.Trim(value)
}
func (v *blockValidator) optionalText(raw any, at string, max int) any {
	if raw == nil || raw == "" {
		return nil
	}
	return v.text(raw, at, max)
}
func (v *blockValidator) optionalBool(raw any, at string) bool {
	if raw == nil {
		return false
	}
	value, ok := raw.(bool)
	if !ok {
		v.invalid(at + " must be true or false")
	}
	return value
}
func (v *blockValidator) optionalNumber(raw any, at string) any {
	if raw == nil {
		return nil
	}
	if _, ok := number(raw); !ok {
		v.invalid(at + " must be a number")
		return nil
	}
	return raw
}
func (v *blockValidator) list(raw any, at string, min, max int) []any {
	if v.err != nil {
		return nil
	}
	value, ok := listValue(raw)
	if !ok {
		v.invalid(at + " must be an array")
		return nil
	}
	if len(value) < min {
		plural := "s"
		if min == 1 {
			plural = ""
		}
		v.invalid(fmt.Sprintf("%s needs at least %d item%s", at, min, plural))
		return nil
	}
	if len(value) > max {
		v.invalid(fmt.Sprintf("%s has more than %d items", at, max))
		return nil
	}
	return value
}
func (v *blockValidator) key(raw any, at string) string {
	value, ok := raw.(string)
	if !ok || !blockKey.MatchString(value) {
		v.invalid(at + " must be a short key of letters, digits, _ or -")
		return ""
	}
	return value
}
func (v *blockValidator) oneOf(raw any, allowed []string, at string, defaults ...string) string {
	if raw == nil && len(defaults) > 0 {
		return defaults[0]
	}
	value, ok := raw.(string)
	if !ok || !slices.Contains(allowed, value) {
		v.invalid(at + " must be one of " + strings.Join(allowed, ", "))
		return ""
	}
	return value
}
func (v *blockValidator) requireObject(raw any, reason string) object {
	value := asObject(raw)
	if value == nil {
		v.invalid(reason)
		return object{}
	}
	return value
}
func IsAskBlock(block map[string]any) bool {
	return slices.Contains([]string{"choice", "confirm", "form"}, object(block).str("type"))
}
func ParseBlock(raw any, paths ...string) (object, error) {
	at := "block"
	if len(paths) > 0 {
		at = paths[0]
	}
	v := &blockValidator{}
	value := v.requireObject(raw, at+" must be an object")
	kind := v.oneOf(value["type"], blockTypes, at+".type")
	var block object
	switch kind {
	case "choice":
		block = v.choice(value, at)
	case "confirm":
		block = v.confirm(value, at)
	case "form":
		block = v.form(value, at)
	case "table":
		block = v.table(value, at)
	case "chart":
		block = v.chart(value, at)
	case "steps":
		block = v.steps(value, at)
	case "thread":
		v.only(value, []string{"type", "threadId"}, at)
		block = object{"type": "thread", "threadId": v.text(value["threadId"], at+".threadId", 120)}
	case "connector":
		block = v.connector(value, at)
	}
	if v.err != nil {
		return nil, v.err
	}
	return block, nil
}
func (v *blockValidator) choice(value object, at string) object {
	v.only(value, []string{"type", "question", "options", "recommended"}, at)
	options := []object{}
	for i, raw := range v.list(value["options"], at+".options", 2, 6) {
		where := fmt.Sprintf("%s.options[%d]", at, i)
		option := v.requireObject(raw, where+" must be { label, consequence }")
		v.only(option, []string{"label", "consequence"}, where)
		options = append(options, object{"label": v.text(option["label"], where+".label", 120), "consequence": v.text(option["consequence"], where+".consequence", 2000)})
	}
	recommended := v.optionalNumber(value["recommended"], at+".recommended")
	if n, ok := number(recommended); ok && (n != math.Trunc(n) || n < 0 || n >= float64(len(options))) {
		v.invalid(at + ".recommended must be the index of an option")
	}
	return object{"type": "choice", "question": v.text(value["question"], at+".question", 2000), "options": options, "recommended": recommended}
}
func (v *blockValidator) confirm(value object, at string) object {
	v.only(value, []string{"type", "title", "description", "detail", "tone", "approveText", "denyText"}, at)
	return object{"type": "confirm", "title": v.text(value["title"], at+".title", 2000), "description": v.optionalText(value["description"], at+".description", 2000), "detail": v.optionalText(value["detail"], at+".detail", 10000), "tone": v.oneOf(value["tone"], []string{"neutral", "warning", "danger"}, at+".tone", "neutral"), "approveText": v.optionalText(value["approveText"], at+".approveText", 120), "denyText": v.optionalText(value["denyText"], at+".denyText", 120)}
}
func (v *blockValidator) field(raw any, at string) object {
	value := v.requireObject(raw, at+" must be an object")
	v.only(value, []string{"key", "label", "kind", "required", "placeholder", "options", "default", "min", "max"}, at)
	kind := v.oneOf(value["kind"], fieldKinds, at+".kind")
	choosing := kind == "select" || kind == "multiselect"
	options := []object{}
	if choosing {
		for i, raw := range v.list(value["options"], at+".options", 1, 50) {
			where := fmt.Sprintf("%s.options[%d]", at, i)
			if text, ok := raw.(string); ok {
				options = append(options, object{"label": v.text(text, where, 120), "value": text})
			} else {
				option := v.requireObject(raw, where+" must be a string or { label, value }")
				v.only(option, []string{"label", "value"}, where)
				options = append(options, object{"label": v.text(option["label"], where+".label", 120), "value": v.text(option["value"], where+".value", 120)})
			}
		}
	} else if raw := value["options"]; raw != nil {
		if list, ok := listValue(raw); !ok || len(list) > 0 {
			v.invalid(at + ".options is only for select fields")
		}
	}
	seen := map[string]bool{}
	for _, option := range options {
		key := option.str("value")
		if seen[key] {
			v.invalid(at + ".options has the same value twice")
		}
		seen[key] = true
	}
	bounded := kind == "number"
	if !bounded && (value["min"] != nil || value["max"] != nil) {
		v.invalid(at + ".min and max are only for number fields")
	}
	field := object{"key": v.key(value["key"], at+".key"), "label": v.text(value["label"], at+".label", 120), "kind": kind}
	required := false
	if kind != "switch" {
		required = v.optionalBool(value["required"], at+".required")
	}
	field["required"] = required
	field["placeholder"] = v.optionalText(value["placeholder"], at+".placeholder", 120)
	field["options"] = options
	field["default"] = nil
	field["min"] = nil
	field["max"] = nil
	if bounded {
		field["min"] = v.optionalNumber(value["min"], at+".min")
		field["max"] = v.optionalNumber(value["max"], at+".max")
	}
	minimum, hasMin := number(field["min"])
	maximum, hasMax := number(field["max"])
	if hasMin && hasMax && minimum > maximum {
		v.invalid(at + ".min is greater than max")
	}
	if value["default"] == nil {
		field["default"] = emptyField(field)
	} else {
		field["default"] = v.fieldValue(field, value["default"], at+".default", false)
	}
	return field
}
func (v *blockValidator) form(value object, at string) object {
	v.only(value, []string{"type", "title", "description", "fields", "submitText"}, at)
	fields := []object{}
	for i, raw := range v.list(value["fields"], at+".fields", 1, 20) {
		fields = append(fields, v.field(raw, fmt.Sprintf("%s.fields[%d]", at, i)))
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if seen[field.str("key")] {
			v.invalid(fmt.Sprintf("%s.fields uses the key \"%s\" twice", at, field.str("key")))
		}
		seen[field.str("key")] = true
	}
	return object{"type": "form", "title": v.text(value["title"], at+".title", 2000), "description": v.optionalText(value["description"], at+".description", 2000), "fields": fields, "submitText": v.optionalText(value["submitText"], at+".submitText", 120)}
}
func (v *blockValidator) table(value object, at string) object {
	v.only(value, []string{"type", "title", "columns", "rows"}, at)
	columns := []object{}
	keys := []string{}
	for i, raw := range v.list(value["columns"], at+".columns", 1, 12) {
		where := fmt.Sprintf("%s.columns[%d]", at, i)
		column := v.requireObject(raw, where+" must be { key, title }")
		v.only(column, []string{"key", "title", "align"}, where)
		key := v.key(column["key"], where+".key")
		columns = append(columns, object{"key": key, "title": v.text(column["title"], where+".title", 120), "align": v.oneOf(column["align"], []string{"start", "end"}, where+".align", "start")})
		keys = append(keys, key)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			v.invalid(at + ".columns has the same key twice")
		}
		seen[key] = true
	}
	rows := []object{}
	for i, raw := range v.list(value["rows"], at+".rows", 0, 200) {
		where := fmt.Sprintf("%s.rows[%d]", at, i)
		cells := v.requireObject(raw, where+" must be an object of cells by column key")
		v.only(cells, keys, where)
		row := object{}
		for _, key := range keys {
			cell := cells[key]
			switch value := cell.(type) {
			case nil, bool:
			case string:
				if utf16Length(value) > 2000 {
					v.invalid(where + "." + key + " is longer than 2000 characters")
				}
			default:
				if _, ok := number(cell); !ok {
					v.invalid(where + "." + key + " must be text, a number or a boolean")
				}
			}
			row[key] = cell
		}
		rows = append(rows, row)
	}
	return object{"type": "table", "title": v.optionalText(value["title"], at+".title", 2000), "columns": columns, "rows": rows}
}
func (v *blockValidator) chart(value object, at string) object {
	v.only(value, []string{"type", "title", "kind", "x", "y", "labels", "rows", "stacked", "horizontal"}, at)
	kind := v.oneOf(value["kind"], []string{"line", "area", "bar", "pie"}, at+".kind")
	x := v.key(value["x"], at+".x")
	maximum := 8
	if kind == "pie" {
		maximum = 1
	}
	y := []string{}
	seen := map[string]bool{}
	for i, raw := range v.list(value["y"], at+".y", 1, maximum) {
		name := v.key(raw, fmt.Sprintf("%s.y[%d]", at, i))
		y = append(y, name)
	}
	for _, name := range y {
		if seen[name] {
			v.invalid(at + ".y names the same key twice")
		}
		seen[name] = true
	}
	if slices.Contains(y, x) {
		v.invalid(at + ".y must not include x")
	}
	labels := object{}
	if value["labels"] != nil {
		given := v.requireObject(value["labels"], at+".labels must be an object of names by y key")
		keys := []string{}
		for key := range given {
			keys = append(keys, key)
		}
		jsSortKeys(keys)
		for _, name := range keys {
			if !slices.Contains(y, name) {
				v.invalid(at + ".labels." + name + " is not one of y")
			}
			labels[name] = v.text(given[name], at+".labels."+name, 120)
		}
	}
	rows := []object{}
	for i, raw := range v.list(value["rows"], at+".rows", 1, 500) {
		where := fmt.Sprintf("%s.rows[%d]", at, i)
		given := v.requireObject(raw, where+" must be an object")
		v.only(given, append([]string{x}, y...), where)
		xValue := given[x]
		if text, ok := xValue.(string); ok {
			if utf16Length(text) > 120 {
				v.invalid(where + "." + x + " is longer than 120 characters")
			}
		} else if _, ok := number(xValue); !ok {
			v.invalid(where + "." + x + " must be text or a number")
		}
		row := object{x: xValue}
		for _, name := range y {
			value := given[name]
			if value != nil {
				if _, ok := number(value); !ok {
					v.invalid(where + "." + name + " must be a number or null")
				}
			}
			row[name] = value
		}
		rows = append(rows, row)
	}
	title := v.optionalText(value["title"], at+".title", 2000)
	stacked, horizontal := false, false
	if kind != "pie" {
		stacked = v.optionalBool(value["stacked"], at+".stacked")
	}
	if kind == "bar" {
		horizontal = v.optionalBool(value["horizontal"], at+".horizontal")
	}
	return object{"type": "chart", "title": title, "kind": kind, "x": x, "y": y, "labels": labels, "rows": rows, "stacked": stacked, "horizontal": horizontal}
}
func (v *blockValidator) steps(value object, at string) object {
	v.only(value, []string{"type", "title", "items"}, at)
	items := []object{}
	for i, raw := range v.list(value["items"], at+".items", 1, 20) {
		where := fmt.Sprintf("%s.items[%d]", at, i)
		item := v.requireObject(raw, where+" must be { title, status }")
		v.only(item, []string{"title", "description", "status"}, where)
		items = append(items, object{"title": v.text(item["title"], where+".title", 120), "description": v.optionalText(item["description"], where+".description", 2000), "status": v.oneOf(item["status"], []string{"done", "current", "pending", "failed"}, where+".status", "pending")})
	}
	return object{"type": "steps", "title": v.optionalText(value["title"], at+".title", 2000), "items": items}
}
func (v *blockValidator) strings(raw any, at string, max int) []string {
	if raw == nil {
		return []string{}
	}
	values, ok := listValue(raw)
	if !ok || len(values) > max {
		v.invalid(fmt.Sprintf("%s must be a list of at most %d", at, max))
		return nil
	}
	result := []string{}
	for i, value := range values {
		result = append(result, v.text(value, fmt.Sprintf("%s[%d]", at, i), 500))
	}
	return result
}
func (v *blockValidator) connector(value object, at string) object {
	v.only(value, []string{"type", "name", "why", "transport", "command", "args", "url", "secrets"}, at)
	transport := v.oneOf(value["transport"], []string{"stdio", "http"}, at+".transport")
	secrets := v.strings(value["secrets"], at+".secrets", 10)
	for i, name := range secrets {
		if !secretName.MatchString(name) {
			v.invalid(fmt.Sprintf("%s.secrets[%d] must be a variable or header name", at, i))
		}
	}
	block := object{"type": "connector", "name": v.text(value["name"], at+".name", 120), "why": v.optionalText(value["why"], at+".why", 300), "transport": transport, "command": nil, "args": []string{}, "url": nil, "secrets": secrets}
	if transport == "stdio" {
		block["command"] = v.text(value["command"], at+".command", 500)
		block["args"] = v.strings(value["args"], at+".args", 30)
	} else {
		url := v.text(value["url"], at+".url", 2000)
		if !connectorURL.MatchString(url) {
			v.invalid(at + ".url must be an http(s) address")
		}
		block["url"] = url
	}
	return block
}
func emptyField(field object) any {
	if field.str("kind") == "switch" {
		return false
	}
	if field.str("kind") == "multiselect" {
		return []any{}
	}
	return nil
}
func fieldOptions(field object) []object {
	switch options := field["options"].(type) {
	case []object:
		return options
	case []any:
		return objects(options)
	}
	return nil
}
func (v *blockValidator) fieldValue(field object, raw any, at string, strict bool) any {
	missing := func() any {
		if strict && field.yes("required") {
			v.invalid(at + " is required")
		}
		return emptyField(field)
	}
	if raw == nil {
		return missing()
	}
	switch field.str("kind") {
	case "text", "textarea":
		value, ok := raw.(string)
		if !ok {
			v.invalid(at + " must be text")
			return nil
		}
		if utf16Length(value) > 10000 {
			v.invalid(at + " is longer than 10000 characters")
		}
		if projectsettings.Trim(value) == "" {
			return missing()
		}
		return value
	case "number":
		n, ok := number(raw)
		if !ok {
			v.invalid(at + " must be a number")
			return nil
		}
		if minimum, ok := number(field["min"]); ok && n < minimum {
			v.invalid(at + " must be at least " + encoded(minimum))
		}
		if maximum, ok := number(field["max"]); ok && n > maximum {
			v.invalid(at + " must be at most " + encoded(maximum))
		}
		return raw
	case "select":
		for _, option := range fieldOptions(field) {
			if option["value"] == raw {
				return raw
			}
		}
		v.invalid(at + " must be one of the field's option values")
		return nil
	case "multiselect":
		values, ok := listValue(raw)
		if !ok {
			v.invalid(at + " must be a list of option values")
			return nil
		}
		allowed := map[string]bool{}
		for _, option := range fieldOptions(field) {
			allowed[option.str("value")] = true
		}
		seen := map[string]bool{}
		for _, raw := range values {
			value, ok := raw.(string)
			if !ok || !allowed[value] {
				v.invalid(at + " must hold only the field's option values")
			}
		}
		for _, raw := range values {
			value, _ := raw.(string)
			if seen[value] {
				v.invalid(at + " names a value twice")
			}
			seen[value] = true
		}
		if len(values) == 0 {
			return missing()
		}
		return values
	case "switch":
		value, ok := raw.(bool)
		if !ok {
			v.invalid(at + " must be true or false")
		}
		return value
	}
	return nil
}
func ParseAnswer(block map[string]any, raw any) (object, error) {
	question := object(block)
	value := asObject(raw)
	v := &blockValidator{}
	if value == nil || value.str("type") != question.str("type") {
		v.invalid("the answer must be a " + question.str("type"))
		return nil, v.err
	}
	var answer object
	switch question.str("type") {
	case "choice":
		choice, ok := number(value["choice"])
		if !ok || choice != math.Trunc(choice) || choice < 0 || choice >= float64(len(array(question, "options"))) {
			v.invalid("choice must be the index of an option")
		}
		answer = object{"type": "choice", "choice": value["choice"]}
	case "confirm":
		approved, ok := value["approved"].(bool)
		if !ok {
			v.invalid("approved must be true or false")
		}
		answer = object{"type": "confirm", "approved": approved}
		if approved && question.yes("allowProjectReference") {
			answer["scope"] = v.oneOf(value["scope"], []string{"thread", "project"}, "scope", "thread")
		} else if value["scope"] != nil {
			v.invalid("scope is only available when allowing a reference with a choice of scope")
		}
	case "form":
		given := v.requireObject(value["values"], "values must be an object by field key")
		fields := []object{}
		switch raw := question["fields"].(type) {
		case []object:
			fields = raw
		case []any:
			fields = objects(raw)
		}
		keys := []string{}
		for _, field := range fields {
			keys = append(keys, field.str("key"))
		}
		v.only(given, keys, "values")
		values := object{}
		for _, field := range fields {
			key := field.str("key")
			values[key] = v.fieldValue(field, given[key], "values."+key, true)
		}
		answer = object{"type": "form", "values": values}
	}
	if v.err != nil {
		return nil, v.err
	}
	return answer, nil
}
func ConfirmBlock(input map[string]any) object {
	return object{"type": "confirm", "title": input["title"], "description": input["description"], "detail": input["detail"], "tone": fallback(input["tone"], "neutral"), "approveText": input["approveText"], "denyText": input["denyText"]}
}

func jsSortKeys(keys []string) {
	index := func(key string) (uint64, bool) {
		n, err := strconv.ParseUint(key, 10, 32)
		return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
	}
	slices.SortFunc(keys, func(a, b string) int {
		x, xOK := index(a)
		y, yOK := index(b)
		if xOK && yOK {
			if x < y {
				return -1
			}
			if x > y {
				return 1
			}
			return 0
		}
		if xOK {
			return -1
		}
		if yOK {
			return 1
		}
		return strings.Compare(a, b)
	})
}
