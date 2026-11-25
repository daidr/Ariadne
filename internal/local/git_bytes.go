package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/process"
)

type GitBytes func(context.Context, []string, int64) ([]byte, error)
type limitedBytes struct {
	data     bytes.Buffer
	max      int64
	overflow bool
	cancel   context.CancelFunc
}

func (out *limitedBytes) Write(data []byte) (int, error) {
	if int64(len(data)) > out.max-int64(out.data.Len()) {
		out.overflow = true
		if out.cancel != nil {
			out.cancel()
			return 0, errors.New("git output exceeds limit")
		}
		count := max(0, int(out.max)-out.data.Len())
		out.data.Write(data[:count])
		return len(data), nil
	}
	return out.data.Write(data)
}
func NewGitBytes(directory string, protocols ...string) GitBytes {
	return func(ctx context.Context, args []string, limit int64) ([]byte, error) {
		if limit < 0 || limit > MaxTextBytes {
			return nil, errors.New("git byte limit must be between 0 and 64 MiB")
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		stdout := &limitedBytes{max: limit, cancel: cancel}
		stderr := &limitedBytes{max: 64 << 10}
		command, err := process.Start(ctx, append([]string{gitExecutable(nil)}, args...), process.Options{Directory: directory, ForThread: true, Env: ServerGitEnv(protocols...), Stdout: stdout, Stderr: stderr})
		if err != nil {
			return nil, err
		}
		err = command.Wait()
		if stdout.overflow {
			return nil, errors.New("git output exceeds limit")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("git failed: %s", strings.TrimSpace(stderr.data.String()))
		}
		return stdout.data.Bytes(), nil
	}
}
