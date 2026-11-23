package runner

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/sdk"
)

const maxTrackedConversations = 200

type researchContextKey struct{}

func withResearchContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, researchContextKey{}, true)
}

func inResearchContext(ctx context.Context) bool {
	active, _ := ctx.Value(researchContextKey{}).(bool)
	return active
}

type lruEntry[V any] struct {
	key   string
	value V
}

type lruMap[V any] struct {
	limit int
	order *list.List
	items map[string]*list.Element
}

func (cache *lruMap[V]) init() {
	if cache.items == nil {
		if cache.limit <= 0 {
			cache.limit = maxTrackedConversations
		}
		cache.order = list.New()
		cache.items = make(map[string]*list.Element)
	}
}

func (cache *lruMap[V]) get(key string) (V, bool) {
	var zero V
	if cache.items == nil {
		return zero, false
	}
	element, ok := cache.items[key]
	if !ok {
		return zero, false
	}
	return element.Value.(*lruEntry[V]).value, true
}

func (cache *lruMap[V]) touch(key string, create func() V) V {
	cache.init()
	if element, ok := cache.items[key]; ok {
		cache.order.MoveToFront(element)
		return element.Value.(*lruEntry[V]).value
	}
	entry := &lruEntry[V]{key: key, value: create()}
	cache.items[key] = cache.order.PushFront(entry)
	cache.evict()
	return entry.value
}

func (cache *lruMap[V]) set(key string, value V) {
	cache.init()
	if element, ok := cache.items[key]; ok {
		element.Value.(*lruEntry[V]).value = value
		cache.order.MoveToFront(element)
		return
	}
	entry := &lruEntry[V]{key: key, value: value}
	cache.items[key] = cache.order.PushFront(entry)
	cache.evict()
}

func (cache *lruMap[V]) delete(key string) {
	if cache.items == nil {
		return
	}
	if element, ok := cache.items[key]; ok {
		cache.order.Remove(element)
		delete(cache.items, key)
	}
}

func (cache *lruMap[V]) evict() {
	for len(cache.items) > cache.limit {
		oldest := cache.order.Back()
		if oldest == nil {
			break
		}
		cache.order.Remove(oldest)
		delete(cache.items, oldest.Value.(*lruEntry[V]).key)
	}
}

// fileState tracks per-conversation file hashes (for external modification detection),
// read ranges currently present in model context (for duplicate read deduplication),
// and the previous failing post-edit check output (for folding repeated failures).
// All methods are safe for concurrent use.
type fileState struct {
	mu       sync.Mutex
	seen     lruMap[map[string][32]byte]
	reads    lruMap[map[string][32]byte]
	failures lruMap[string]
}

func normalizeFilePath(raw string) string {
	cleaned := path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	return strings.TrimPrefix(cleaned, "./")
}

func fileStateKey(sourceIdentity, filePath string) string {
	return sourceIdentity + "\x00" + normalizeFilePath(filePath)
}

func fileReadKey(sourceIdentity, filePath string, offset, limit int) string {
	return sourceIdentity + "\x00" + normalizeFilePath(filePath) + "\x00" + strconv.Itoa(offset) + "\x00" + strconv.Itoa(limit)
}

func fileSourceIdentity(view any, input args) string {
	if identified, ok := view.(interface{ SourceIdentity() string }); ok {
		if id := identified.SourceIdentity(); id != "" {
			return id
		}
	}
	if name := text(input, "source"); name != "" {
		return "source:" + name
	}
	if ref := text(input, "reference"); ref != "" {
		return "reference:" + ref
	}
	return ""
}

func (state *fileState) noteFile(conversationID, sourceIdentity, filePath string, data []byte) {
	if state == nil || conversationID == "" {
		return
	}
	sum := sha256.Sum256(data)
	key := fileStateKey(sourceIdentity, filePath)
	state.mu.Lock()
	defer state.mu.Unlock()
	files := state.seen.touch(conversationID, func() map[string][32]byte {
		return make(map[string][32]byte)
	})
	files[key] = sum
}

func (state *fileState) hasSeen(conversationID, sourceIdentity, filePath string) bool {
	if state == nil || conversationID == "" {
		return false
	}
	key := fileStateKey(sourceIdentity, filePath)
	state.mu.Lock()
	defer state.mu.Unlock()
	files, ok := state.seen.get(conversationID)
	if !ok {
		return false
	}
	_, known := files[key]
	return known
}

func (state *fileState) changedSince(conversationID, sourceIdentity, filePath string, data []byte) bool {
	if state == nil || conversationID == "" {
		return false
	}
	sum := sha256.Sum256(data)
	key := fileStateKey(sourceIdentity, filePath)
	state.mu.Lock()
	defer state.mu.Unlock()
	files, ok := state.seen.get(conversationID)
	if !ok {
		return false
	}
	known, exists := files[key]
	return exists && known != sum
}

func (state *fileState) noteRead(conversationID, sourceIdentity, filePath string, offset, limit int, data []byte) bool {
	if state == nil || conversationID == "" {
		return false
	}
	sum := sha256.Sum256(data)
	key := fileReadKey(sourceIdentity, filePath, offset, limit)
	state.mu.Lock()
	defer state.mu.Unlock()
	reads := state.reads.touch(conversationID, func() map[string][32]byte {
		return make(map[string][32]byte)
	})
	previous, exists := reads[key]
	reads[key] = sum
	return exists && previous == sum
}

func (state *fileState) noteCheckFailure(conversationID, output string, failed bool) bool {
	if state == nil || conversationID == "" {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !failed {
		state.failures.delete(conversationID)
		return false
	}
	previous, exists := state.failures.get(conversationID)
	state.failures.set(conversationID, output)
	return exists && previous == output
}

func (state *fileState) forgetReads(conversationID string) {
	if state == nil || conversationID == "" {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.reads.delete(conversationID)
	state.failures.delete(conversationID)
}

func (turn *turn) fileTracker() *fileState {
	if turn == nil || turn.Runner == nil {
		return nil
	}
	return &turn.Runner.files
}

func (turn *turn) keepsResults() bool {
	if turn == nil {
		return false
	}
	return !turn.statefulSession
}

func changedSinceError(filePath string) error {
	return fmt.Errorf("%s changed since you last read or wrote it (the user, a command or a tool changed it). Read it again and work from what it holds now; nothing was written", filePath)
}

func readExistingBytes(ctx context.Context, workspace sdk.Workspace, filePath string) ([]byte, bool, error) {
	data, err := workspace.ReadBytes(ctx, filePath, maxEditBytes)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	var tooLarge *sdk.FileTooLargeError
	if errors.As(err, &tooLarge) {
		return nil, false, err
	}
	if files, ok := workspace.(sdk.WorkspaceFiles); ok {
		if exists, existsErr := files.FileExists(ctx, filePath); existsErr == nil && !exists {
			return nil, false, nil
		}
	}
	return nil, false, err
}
