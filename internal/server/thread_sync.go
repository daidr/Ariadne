package server

import (
	"encoding/json"
	"hash/maphash"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/cache"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/google/uuid"
)

type threadHash struct {
	hash uint64
	rev  int64
}
type knownThreads struct {
	rev, idsRev int64
	ids         string
	threads     map[string]threadHash
}
type ThreadDelta struct {
	Cursor  string            `json:"cursor"`
	Partial bool              `json:"partial"`
	Threads []core.ThreadView `json:"threads"`
	IDs     *[]string         `json:"ids,omitempty"`
}
type ThreadSync struct {
	mu       sync.Mutex
	epoch    string
	rev      int64
	seed     maphash.Seed
	projects *cache.Cache[string, *knownThreads]
}

func NewThreadSync() *ThreadSync {
	projects, err := cache.New[string, *knownThreads](cache.Options{MaxEntries: 128, MaxWeight: 16 * 1024 * 1024, TTL: 30 * time.Minute})
	if err != nil {
		panic(err)
	}
	return &ThreadSync{epoch: uuid.NewString()[:8], seed: maphash.MakeSeed(), projects: projects}
}

var cursorNumber = regexp.MustCompile(`^\d+$`)

func (syncer *ThreadSync) Delta(projectID string, threads []core.ThreadView, since string) ThreadDelta {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	known, ok := syncer.projects.Get(projectID)
	if !ok {
		known = &knownThreads{threads: map[string]threadHash{}}
	}
	ids := make([]string, 0, len(threads))
	hashes := make([]uint64, 0, len(threads))
	changed := false
	weight := int64(128 + len(projectID))
	for _, thread := range threads {
		data, _ := json.Marshal(thread)
		hash := maphash.Bytes(syncer.seed, data)
		ids = append(ids, thread.ID)
		hashes = append(hashes, hash)
		if previous, ok := known.threads[thread.ID]; !ok || previous.hash != hash {
			changed = true
		}
		weight += int64(96 + 2*len(thread.ID))
	}
	joined := strings.Join(ids, "\n")
	moved := joined != known.ids
	if changed || moved {
		syncer.rev++
		known.rev = syncer.rev
		before := known.threads
		known.threads = make(map[string]threadHash, len(threads))
		for i, thread := range threads {
			previous, ok := before[thread.ID]
			if ok && previous.hash == hashes[i] {
				known.threads[thread.ID] = previous
			} else {
				known.threads[thread.ID] = threadHash{hashes[i], known.rev}
			}
		}
		if moved {
			known.ids, known.idsRev = joined, known.rev
		}
	}
	stored := syncer.projects.Put(projectID, known, weight)
	result := ThreadDelta{Cursor: syncer.epoch + "." + strconv.FormatInt(known.rev, 10), Threads: threads}
	parts := strings.Split(since, ".")
	if !stored || len(parts) < 2 || parts[0] != syncer.epoch || !cursorNumber.MatchString(parts[1]) {
		return result
	}
	from, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || from > known.rev {
		return result
	}
	result.Partial = true
	result.Threads = []core.ThreadView{}
	for _, thread := range threads {
		if known.threads[thread.ID].rev > from {
			result.Threads = append(result.Threads, thread)
		}
	}
	if known.idsRev > from {
		result.IDs = &ids
	}
	return result
}
func (syncer *ThreadSync) Forget(id string) {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	syncer.projects.Remove(id)
}
func (syncer *ThreadSync) Close() {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	syncer.projects.Close()
}
