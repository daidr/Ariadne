package server

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/google/uuid"
)

type ThreadDelta struct {
	Cursor  string            `json:"cursor"`
	Partial bool              `json:"partial"`
	Threads []core.ThreadView `json:"threads"`
	IDs     *[]string         `json:"ids,omitempty"`
}

// ThreadSync encodes stateless cursors over threads.changed_seq so incremental
// project reads avoid in-memory snapshots, global locks and full JSON hashing.
type ThreadSync struct {
	epoch string
}

func NewThreadSync() *ThreadSync {
	return &ThreadSync{epoch: uuid.NewString()[:8]}
}

var (
	cursorNumber = regexp.MustCompile(`^\d+$`)
	cursorHex    = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func hashThreadIDs(ids []string) string {
	h := fnv.New64a()
	for i, id := range ids {
		if i > 0 {
			_, _ = h.Write([]byte{'\n'})
		}
		_, _ = h.Write([]byte(id))
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

func (syncer *ThreadSync) parseCursor(since string) (seq int64, idsHash string, at int64, ok bool) {
	parts := strings.Split(since, ".")
	if len(parts) != 4 || parts[0] != syncer.epoch || !cursorNumber.MatchString(parts[1]) || !cursorHex.MatchString(parts[2]) || !cursorNumber.MatchString(parts[3]) {
		return 0, "", 0, false
	}
	parsedSeq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, "", 0, false
	}
	parsedAt, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0, "", 0, false
	}
	return parsedSeq, parts[2], parsedAt, true
}

func (syncer *ThreadSync) ProjectDetail(ctx context.Context, reader core.Reader, projectID, since string) (core.ProjectDetailDelta, ThreadDelta, error) {
	sinceSeq, sinceIDsHash, sinceAt, canPartial := syncer.parseCursor(since)
	detail, err := reader.GetProjectDetailSince(ctx, projectID, sinceSeq, sinceAt, canPartial)
	if err != nil {
		return core.ProjectDetailDelta{}, ThreadDelta{}, err
	}
	idsHash := hashThreadIDs(detail.ThreadIDs)
	cursor := syncer.epoch + "." + strconv.FormatInt(detail.Seq, 10) + "." + idsHash + "." + strconv.FormatInt(detail.At, 10)
	delta := ThreadDelta{
		Cursor:  cursor,
		Partial: detail.Partial,
		Threads: detail.Threads,
	}
	if detail.Partial && idsHash != sinceIDsHash {
		ids := make([]string, len(detail.ThreadIDs))
		copy(ids, detail.ThreadIDs)
		delta.IDs = &ids
	}
	return detail, delta, nil
}

func (syncer *ThreadSync) Forget(string) {}

func (syncer *ThreadSync) Close() {}
