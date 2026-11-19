package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/google/uuid"
)

const maxCheckpointBytes = 4 * 1024 * 1024
const checkpointTTL = 30 * 24 * time.Hour

type Checkpoint struct {
	Config    string          `json:"config"`
	System    string          `json:"system"`
	FPS       []string        `json:"fps"`
	Driver    json.RawMessage `json:"driver"`
	Directory string          `json:"directory,omitempty"`
}
type checkpointFile struct {
	at    time.Time
	order uint64
}
type Checkpoints struct {
	mu    sync.Mutex
	root  *os.Root
	files map[string]checkpointFile
	limit int
	next  uint64
	now   func() time.Time
}

var providerKind = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var checkpointName = regexp.MustCompile(`^[a-f0-9]{64}\.json$`)
var checkpointTemporary = regexp.MustCompile(`^[a-f0-9]{64}\.json\.[a-f0-9-]{36}\.tmp$`)

func NewCheckpoints(dataDir, kind string, limit int) (*Checkpoints, error) {
	if dataDir == "" {
		return nil, errors.New("agent checkpoint data directory is required")
	}
	if !providerKind.MatchString(kind) {
		return nil, errors.New("invalid agent provider kind")
	}
	if limit <= 0 {
		limit = 256
	}
	dir := filepath.Join(dataDir, "checkpoints", kind)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	store := &Checkpoints{root: root, files: map[string]checkpointFile{}, limit: limit, now: time.Now}
	directory, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	defer directory.Close()
	for {
		entries, err := directory.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			root.Close()
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !checkpointName.MatchString(name) && !checkpointTemporary.MatchString(name) {
				continue
			}
			info, err := root.Stat(name)
			if err != nil {
				continue
			}
			if checkpointTemporary.MatchString(name) {
				if store.now().Sub(info.ModTime()) > 24*time.Hour {
					_ = root.Remove(name)
				}
				continue
			}
			if info.Size() > maxCheckpointBytes || store.now().Sub(info.ModTime()) > checkpointTTL {
				_ = root.Remove(name)
				continue
			}
			store.next++
			store.files[name] = checkpointFile{at: info.ModTime(), order: store.next}
			if err := store.prune(); err != nil {
				root.Close()
				return nil, err
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return store, nil
}
func checkpointPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) + ".json"
}
func driverPresent(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) || bytes.Equal(value, []byte("false")) {
		return false
	}
	if value[0] == '"' {
		var text string
		return json.Unmarshal(value, &text) == nil && text != ""
	}
	if value[0] == '-' || value[0] >= '0' && value[0] <= '9' {
		number, _ := strconv.ParseFloat(string(value), 64)
		return number != 0
	}
	return true
}
func (store *Checkpoints) Read(key string) *Checkpoint {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root == nil {
		return nil
	}
	name := checkpointPath(key)
	file, err := store.root.Open(name)
	if err != nil {
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil
	}
	if info.Size() > maxCheckpointBytes || store.now().Sub(info.ModTime()) > checkpointTTL {
		file.Close()
		_ = store.remove(name)
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
	file.Close()
	if len(data) > maxCheckpointBytes {
		_ = store.remove(name)
		return nil
	}
	if err != nil {
		return nil
	}
	var value struct {
		Config    *string         `json:"config"`
		System    *string         `json:"system"`
		FPS       []string        `json:"fps"`
		Driver    json.RawMessage `json:"driver"`
		Directory string          `json:"directory"`
	}
	if json.Unmarshal(data, &value) != nil || value.Config == nil || value.System == nil || value.FPS == nil || !driverPresent(value.Driver) {
		return nil
	}
	return &Checkpoint{Config: *value.Config, System: *value.System, FPS: value.FPS, Driver: value.Driver, Directory: value.Directory}
}
func (store *Checkpoints) Write(key string, value Checkpoint) error {
	data, err := jsonutil.Encode(value)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root == nil {
		return errors.New("agent checkpoint store is closed")
	}
	name := checkpointPath(key)
	if len(data) > maxCheckpointBytes {
		return store.remove(name)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	temporary := name + "." + id.String() + ".tmp"
	file, err := store.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer store.root.Remove(temporary)
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := store.root.Rename(temporary, name); err != nil {
		return err
	}
	store.next++
	store.files[name] = checkpointFile{at: store.now(), order: store.next}
	return store.prune()
}
func (store *Checkpoints) remove(name string) error {
	delete(store.files, name)
	err := store.root.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (store *Checkpoints) Remove(key string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root == nil {
		return nil
	}
	return store.remove(checkpointPath(key))
}
func (store *Checkpoints) prune() error {
	for name, file := range store.files {
		if store.now().Sub(file.at) > checkpointTTL {
			if err := store.remove(name); err != nil {
				return err
			}
		}
	}
	for len(store.files) > store.limit {
		oldest := ""
		var before checkpointFile
		for name, file := range store.files {
			if oldest == "" || file.at.Before(before.at) || file.at.Equal(before.at) && file.order < before.order {
				oldest, before = name, file
			}
		}
		if err := store.remove(oldest); err != nil {
			return err
		}
	}
	return nil
}
func (store *Checkpoints) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	clear(store.files)
	if store.root == nil {
		return nil
	}
	err := store.root.Close()
	store.root = nil
	return err
}
