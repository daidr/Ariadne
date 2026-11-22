package local

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

const catFileIdleTTL = 30 * time.Second

// catFileBatch keeps one `git cat-file --batch-command --buffer` process alive
// for a repository so multi-file diffs read blob sizes and contents in bulk
// without spawning four git processes per file.
type catFileBatch struct {
	directory string
	mu        sync.Mutex
	closed    bool
	cmd       *process.Command
	stdin     *os.File
	stdout    *os.File
	reader    *bufio.Reader
	idleTimer *time.Timer
}

func newCatFileBatch(directory string) *catFileBatch {
	return &catFileBatch{directory: directory}
}

func (b *catFileBatch) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	b.resetLocked()
	b.mu.Unlock()
	return nil
}

func (b *catFileBatch) resetLocked() {
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
	if b.stdin != nil {
		_ = b.stdin.Close()
		b.stdin = nil
	}
	if b.stdout != nil {
		_ = b.stdout.Close()
		b.stdout = nil
	}
	if b.cmd != nil {
		_ = b.cmd.Kill()
		_ = b.cmd.Wait()
		b.cmd = nil
	}
	b.reader = nil
}

func (b *catFileBatch) ensureStartedLocked() error {
	if b.closed {
		return errors.New("cat-file reader is closed")
	}
	if b.cmd != nil {
		select {
		case <-b.cmd.Done():
			b.resetLocked()
		default:
			if b.idleTimer != nil {
				b.idleTimer.Stop()
			}
			return nil
		}
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return err
	}
	cmd, err := process.Start(context.Background(), []string{"git", "cat-file", "--batch-command", "--buffer"}, process.Options{
		Directory: b.directory,
		Env:       ServerGitEnv("file"),
		ForThread: true,
		Stdin:     inR,
		Stdout:    outW,
		Stderr:    io.Discard,
		Label:     "git-cat-file",
	})
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return err
	}
	b.cmd = cmd
	b.stdin = inW
	b.stdout = outR
	b.reader = bufio.NewReaderSize(outR, 64*1024)
	return nil
}

func (b *catFileBatch) scheduleIdleLocked() {
	if b.closed || b.cmd == nil {
		return
	}
	current := b.cmd
	if b.idleTimer != nil {
		b.idleTimer.Stop()
	}
	b.idleTimer = time.AfterFunc(catFileIdleTTL, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.cmd == current {
			b.resetLocked()
		}
	})
}

type blobMeta struct {
	oid   string
	size  int64
	found bool
	large bool
}

func parseBatchInfoLine(line string) (blobMeta, error) {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if strings.HasSuffix(line, " missing") || strings.HasSuffix(line, " ambiguous") {
		return blobMeta{}, nil
	}
	fields := strings.Fields(line)
	if len(fields) != 3 || !isObjectID(fields[0]) {
		return blobMeta{}, fmt.Errorf("unexpected git cat-file info line: %q", line)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return blobMeta{}, fmt.Errorf("invalid git blob size in %q", line)
	}
	if fields[1] != "blob" {
		return blobMeta{}, nil
	}
	return blobMeta{
		oid:   fields[0],
		size:  size,
		found: true,
		large: size > MaxShownBytes,
	}, nil
}

type preparedVersion struct {
	beforeSpec string
	afterSpec  string
	fallback   bool
}

func (b *catFileBatch) ReadVersions(ctx context.Context, git GitRun, inputs []VersionInput) ([]sdk.FileVersions, error) {
	if len(inputs) == 0 {
		return []sdk.FileVersions{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	preps := make([]preparedVersion, len(inputs))
	resolvedTrees := map[string]string{}
	resolveTree := func(revision string) (string, bool) {
		if isObjectID(revision) {
			return revision + "^{tree}", true
		}
		if cached, ok := resolvedTrees[revision]; ok {
			return cached, cached != ""
		}
		res := git(ctx, []string{"rev-parse", "--verify", "--end-of-options", revision + "^{tree}"}, GitOptions{})
		if res.ExitCode != 0 {
			resolvedTrees[revision] = ""
			return "", false
		}
		tree := strings.TrimSpace(res.Stdout)
		resolvedTrees[revision] = tree
		return tree, tree != ""
	}

	batchCount := 0
	for i, input := range inputs {
		oldPath := input.Path
		if input.PreviousPath != nil {
			oldPath = *input.PreviousPath
		}
		cleanOld, err := TreePath(oldPath)
		if err != nil {
			return nil, err
		}
		cleanNew, err := TreePath(input.Path)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(cleanOld, "\r\n") || strings.ContainsAny(cleanNew, "\r\n") {
			preps[i].fallback = true
			continue
		}
		if fromTree, ok := resolveTree(input.From); ok {
			preps[i].beforeSpec = fromTree + ":" + cleanOld
		}
		if headTree, ok := resolveTree(input.Head); ok {
			preps[i].afterSpec = headTree + ":" + cleanNew
		}
		batchCount++
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	results := make([]sdk.FileVersions, len(inputs))
	if batchCount > 0 {
		if err := b.readPrepared(ctx, preps, results); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Fall back to standalone cat-file calls if batch mode fails on an older git or transient pipe error.
			for i := range inputs {
				preps[i].fallback = true
			}
		}
	}
	for i, prep := range preps {
		if !prep.fallback {
			continue
		}
		file, err := FileVersions(ctx, git, inputs[i])
		if err != nil {
			return nil, err
		}
		results[i] = file
	}
	return results, nil
}

func (b *catFileBatch) writeAndReadLocked(payload string, read func() error) error {
	stdin, stdout := b.stdin, b.stdout
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, payload)
		if err != nil {
			_ = stdout.Close()
		}
		writeDone <- err
	}()
	readErr := read()
	if readErr != nil {
		b.resetLocked()
		<-writeDone
		return readErr
	}
	if writeErr := <-writeDone; writeErr != nil {
		b.resetLocked()
		return writeErr
	}
	return nil
}

func (b *catFileBatch) readPrepared(ctx context.Context, preps []preparedVersion, results []sdk.FileVersions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.ensureStartedLocked(); err != nil {
		return err
	}

	cmd, stdin, stdout := b.cmd, b.stdin, b.stdout
	stopCancel := context.AfterFunc(ctx, func() {
		_ = cmd.Kill()
		_ = stdin.Close()
		_ = stdout.Close()
	})
	defer func() {
		stopCancel()
		if ctx.Err() != nil {
			b.resetLocked()
			return
		}
		b.scheduleIdleLocked()
	}()

	type infoSlot struct {
		index  int
		before bool
	}
	var slots []infoSlot
	var query strings.Builder
	for i, prep := range preps {
		if prep.fallback {
			continue
		}
		if prep.beforeSpec != "" {
			slots = append(slots, infoSlot{index: i, before: true})
			query.WriteString("info ")
			query.WriteString(prep.beforeSpec)
			query.WriteByte('\n')
		}
		if prep.afterSpec != "" {
			slots = append(slots, infoSlot{index: i, before: false})
			query.WriteString("info ")
			query.WriteString(prep.afterSpec)
			query.WriteByte('\n')
		}
	}
	if len(slots) == 0 {
		return nil
	}
	query.WriteString("flush\n")
	beforeMeta := make([]blobMeta, len(preps))
	afterMeta := make([]blobMeta, len(preps))
	if err := b.writeAndReadLocked(query.String(), func() error {
		for _, slot := range slots {
			line, err := b.reader.ReadString('\n')
			if err != nil {
				return err
			}
			meta, err := parseBatchInfoLine(line)
			if err != nil {
				return err
			}
			if slot.before {
				beforeMeta[slot.index] = meta
			} else {
				afterMeta[slot.index] = meta
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// Collect unique blob OIDs for files that are not skipped as large.
	var oidOrder []string
	seenOID := map[string]bool{}
	var contentsQuery strings.Builder
	for i, prep := range preps {
		if prep.fallback {
			continue
		}
		if beforeMeta[i].large || afterMeta[i].large {
			results[i] = sdk.FileVersions{Skipped: new("large")}
			continue
		}
		for _, meta := range []blobMeta{beforeMeta[i], afterMeta[i]} {
			if meta.found && !seenOID[meta.oid] {
				seenOID[meta.oid] = true
				oidOrder = append(oidOrder, meta.oid)
				contentsQuery.WriteString("contents ")
				contentsQuery.WriteString(meta.oid)
				contentsQuery.WriteByte('\n')
			}
		}
	}

	blobs := make(map[string]string, len(oidOrder))
	if len(oidOrder) > 0 {
		contentsQuery.WriteString("flush\n")
		if err := b.writeAndReadLocked(contentsQuery.String(), func() error {
			for _, oid := range oidOrder {
				header, err := b.reader.ReadString('\n')
				if err != nil {
					return err
				}
				meta, err := parseBatchInfoLine(header)
				if err != nil || !meta.found {
					return fmt.Errorf("missing expected blob %s", oid)
				}
				if meta.size > MaxShownBytes {
					return fmt.Errorf("blob %s exceeded max size", oid)
				}
				buf := make([]byte, meta.size+1)
				if _, err := io.ReadFull(b.reader, buf); err != nil {
					return err
				}
				blobs[oid] = string(buf[:meta.size])
			}
			return nil
		}); err != nil {
			return err
		}
	}

	for i, prep := range preps {
		if prep.fallback || results[i].Skipped != nil {
			continue
		}
		var before, after *string
		if beforeMeta[i].found {
			val := blobs[beforeMeta[i].oid]
			before = &val
		}
		if afterMeta[i].found {
			val := blobs[afterMeta[i].oid]
			after = &val
		}
		if (before != nil && binaryText(*before)) || (after != nil && binaryText(*after)) {
			results[i] = sdk.FileVersions{Skipped: new("binary")}
			continue
		}
		results[i] = sdk.FileVersions{Before: before, After: after}
	}
	return nil
}
