package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultMaxLogBytes = 10 << 20 // 10 MiB
	defaultMaxBackups  = 3
)

// RotatingFile writes append-only logs and rotates once maxBytes is reached.
type RotatingFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

func OpenRotatingLog(path string) (*RotatingFile, error) {
	return openRotatingLog(path, defaultMaxLogBytes, defaultMaxBackups)
}

func openRotatingLog(path string, maxBytes int64, maxBackups int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, err := file.Stat(); err == nil {
		size = info.Size()
	}
	return &RotatingFile{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		file:       file,
		size:       size,
	}, nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, os.ErrClosed
	}
	if r.maxBytes > 0 && r.size+int64(len(p)) > r.maxBytes && r.size > 0 {
		_ = r.rotateLocked()
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotateLocked() error {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	for i := r.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", r.path, i)
		dst := fmt.Sprintf("%s.%d", r.path, i+1)
		_ = os.Remove(dst)
		_ = os.Rename(src, dst)
	}
	if r.maxBackups > 0 {
		dst := fmt.Sprintf("%s.1", r.path)
		_ = os.Remove(dst)
		_ = os.Rename(r.path, dst)
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	r.file = file
	r.size = 0
	return nil
}

func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// TailLog writes the last n lines of path to out and, if follow is true,
// continues streaming appended lines until ctx is cancelled.
func TailLog(ctx context.Context, path string, lines int, follow bool, out io.Writer) error {
	if lines <= 0 {
		lines = 100
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("log file does not exist yet: %s", path)
		}
		return err
	}
	defer file.Close()

	ring := make([]string, 0, lines)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if len(ring) < lines {
			ring = append(ring, line)
		} else {
			copy(ring, ring[1:])
			ring[len(ring)-1] = line
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, line := range ring {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	buf := make([]byte, 32<<10)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			info, statErr := os.Stat(path)
			if statErr != nil {
				continue
			}
			// Reopen if rotated or truncated.
			if info.Size() < offset {
				_ = file.Close()
				reopened, openErr := os.Open(path)
				if openErr != nil {
					continue
				}
				file = reopened
				offset = 0
			}
			for {
				n, readErr := file.Read(buf)
				if n > 0 {
					offset += int64(n)
					if _, writeErr := out.Write(buf[:n]); writeErr != nil {
						return writeErr
					}
				}
				if errors.Is(readErr, io.EOF) || n == 0 {
					break
				}
				if readErr != nil {
					return readErr
				}
			}
		}
	}
}
