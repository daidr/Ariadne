package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
)

type Store struct {
	mu     sync.RWMutex
	path   string
	config Object
}

var errNoChange = errors.New("settings unchanged")

func Open(path string) (*Store, error) {
	config, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, config: config}, nil
}
func (store *Store) Current() Object {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, _ := clone(store.config)
	return value
}

// Published configurations are immutable: updates clone before changing them.
// Internal readers can retain this version after releasing the lock, copying
// only the small values they return to callers.
func (store *Store) snapshot() Object {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.config
}
func (store *Store) update(change func(Object) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	next, err := clone(store.config)
	if err != nil {
		return err
	}
	if err := change(next); err != nil {
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > MaxConfigBytes {
		return bad("settings")
	}
	owned, err := clone(next)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(store.path), ".config-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, store.path); err != nil {
		return err
	}
	store.config = owned
	return nil
}
func (store *Store) UpdateConcurrency(input any) (Object, error) {
	body := object(input)
	slots, slotsOK := number(body["slots"])
	reserved, reservedOK := number(body["reserved"])
	if !slotsOK || slots != float64(int(slots)) || slots < 1 || slots > 16 {
		return nil, bad("slots")
	}
	if !reservedOK || reserved != float64(int(reserved)) || reserved < 0 || reserved > 4 {
		return nil, bad("reserved")
	}
	value := Object{"slots": slots, "reserved": reserved}
	err := store.update(func(config Object) error { config["concurrency"] = value; return nil })
	return value, err
}
func (store *Store) UpdateInput(input any) (Object, error) {
	body := object(input)
	submit := text(body["submitOn"])
	if submit != "enter" && submit != "mod-enter" {
		return nil, bad("submitOn")
	}
	items, ok := body["snippets"].([]any)
	if !ok || len(items) > 200 {
		return nil, bad("snippets")
	}
	snippets := []any{}
	ids := map[string]bool{}
	for index, raw := range items {
		item := object(raw)
		at := "snippets[" + strconv.Itoa(index) + "]"
		id := text(item["id"])
		if !snippetID.MatchString(id) || ids[id] {
			return nil, bad(at + ".id")
		}
		ids[id] = true
		name := textutil.Trim(text(item["name"]))
		if name == "" || length(name) > 40 {
			return nil, bad(at + ".name")
		}
		content, ok := item["text"].(string)
		if !ok || textutil.Trim(content) == "" || length(content) > 20000 {
			return nil, bad(at + ".text")
		}
		snippets = append(snippets, Object{"id": id, "name": name, "text": content})
	}
	keys, err := parseKeys(fallback(body["keys"], defaultKeys), true)
	if err != nil {
		return nil, err
	}
	value := Object{"submitOn": submit, "snippets": snippets, "keys": keys}
	err = store.update(func(config Object) error { config["input"] = value; return nil })
	return value, err
}
func work(config Object) Object {
	return Object{"instructions": config["instructions"], "rules": object(config["commands"])["rules"], "limits": config["limits"], "defaults": cloneMap(defaultLimits)}
}
func (store *Store) Work() Object { value, _ := clone(work(store.snapshot())); return value }
func (store *Store) Input() Object {
	value, _ := clone(object(store.snapshot()["input"]))
	return value
}
func (store *Store) Concurrency() Object { return cloneMap(object(store.snapshot()["concurrency"])) }
func (store *Store) Locale() string      { return text(store.snapshot()["locale"]) }
func (store *Store) UpdateWork(input any) (Object, error) {
	body := object(input)
	var result Object
	err := store.update(func(config Object) error {
		if raw, exists := body["instructions"]; exists {
			value, ok := raw.(string)
			if !ok || length(value) > 16000 {
				return bad("instructions")
			}
			config["instructions"] = textutil.Trim(value)
		}
		if raw, exists := body["rules"]; exists {
			rules, err := parseRules(raw, true)
			if err != nil {
				return err
			}
			config["commands"] = Object{"rules": rules}
		}
		if raw, exists := body["limits"]; exists {
			limits, err := parseLimits(raw, true)
			if err != nil {
				return err
			}
			config["limits"] = limits
		}
		result = work(config)
		return nil
	})
	return result, err
}
func instructionLine(line string, includeNEL bool) string {
	return textutil.Trim(strings.Join(strings.FieldsFunc(line, func(char rune) bool { return textutil.IsSpace(char) || includeNEL && char == '\u0085' }), " "))
}
func (store *Store) AddInstruction(line string) (bool, error) {
	value := instructionLine(line, true)
	if value == "" {
		return false, bad("text")
	}
	added := false
	err := store.update(func(config Object) error {
		before := text(config["instructions"])
		for _, existing := range strings.Split(before, "\n") {
			if textutil.Trim(existing) == value {
				return errNoChange
			}
		}
		next := value
		if before != "" {
			next = before + "\n" + value
		}
		if length(next) > 16000 {
			return domainerr.New("error.instructionsFull", nil)
		}
		config["instructions"] = next
		added = true
		return nil
	})
	return added, err
}
func (store *Store) RemoveInstruction(line string) (bool, error) {
	value := instructionLine(line, true)
	removed := false
	err := store.update(func(config Object) error {
		if value == "" {
			return errNoChange
		}
		lines := strings.Split(text(config["instructions"]), "\n")
		for index, existing := range lines {
			if instructionLine(existing, false) == value {
				lines = append(lines[:index], lines[index+1:]...)
				config["instructions"] = textutil.Trim(strings.Join(lines, "\n"))
				removed = true
				break
			}
		}
		if !removed {
			return errNoChange
		}
		return nil
	})
	return removed, err
}
func (store *Store) ProjectEnvNames(project string) []string {
	return names(object(object(store.snapshot()["projectEnv"])[project]))
}
func names(given Object) []string {
	result := []string{}
	for key := range given {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
func (store *Store) UpdateProjectEnv(project string, input any) ([]string, error) {
	var result []string
	err := store.update(func(config Object) error {
		all := object(config["projectEnv"])
		env, err := secretPairs(input, validProjectEnv, object(all[project]), "env")
		if err != nil {
			return err
		}
		if len(env) > 0 {
			all[project] = env
		} else {
			delete(all, project)
		}
		config["projectEnv"] = all
		result = names(env)
		return nil
	})
	return result, err
}
func (store *Store) ForgetProject(project string) error {
	return store.update(func(config Object) error {
		delete(object(config["projectEnv"]), project)
		delete(object(config["sourceSecrets"]), project)
		return nil
	})
}
func (store *Store) TurnSettings(project string) Object {
	config := store.snapshot()
	env := Object{}
	for name, value := range object(object(config["projectEnv"])[project]) {
		if validProjectEnv(name) {
			env[name] = value
		}
	}
	limits, _ := clone(object(config["limits"]))
	commands, _ := clone(object(config["commands"]))
	return Object{"limits": limits, "instructions": config["instructions"], "commandRules": commands["rules"], "env": env}
}
func containsWhitespace(value string) bool {
	for _, char := range value {
		if textutil.IsSpace(char) {
			return true
		}
	}
	return false
}
