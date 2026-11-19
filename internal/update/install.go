package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errDownload = errors.New("update download failed")
var errVerification = errors.New("update verification failed")

func install(ctx context.Context, client *http.Client, executable string, original os.FileInfo, release Release, progress func(int)) error {
	if original == nil || !release.asset.valid() {
		return errors.New("a verified program for this installation is unavailable")
	}
	if err := sameExecutable(executable, original); err != nil {
		return err
	}
	// Keep staging on the destination filesystem so the final rename never copies.
	staged, err := os.CreateTemp(filepath.Dir(executable), ".ariadne-update-*"+filepath.Ext(executable))
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	stageErr := func() error {
		if err := download(ctx, client, staged, release, progress); err != nil {
			return err
		}
		if progress != nil {
			progress(100)
		}
		if err := staged.Chmod(original.Mode().Perm()); err != nil {
			return err
		}
		return staged.Sync()
	}()
	if err := errors.Join(stageErr, staged.Close()); err != nil {
		return err
	}
	if err := verifyProgram(ctx, staged.Name(), release.Version); err != nil {
		return fmt.Errorf("%w: %w", errVerification, err)
	}

	lock, err := os.OpenFile(executable+".ariadne-update.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockExecutable(lock); err != nil {
		return fmt.Errorf("lock the program for update: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sameExecutable(executable, original); err != nil {
		return err
	}
	return replaceExecutable(staged.Name(), executable)
}

func sameExecutable(executable string, original os.FileInfo) error {
	current, err := os.Lstat(executable)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(current, original) || current.Size() != original.Size() || !current.ModTime().Equal(original.ModTime()) {
		return errors.New("the installed program changed since Ariadne started")
	}
	return nil
}

type downloadWriter struct {
	writer   io.Writer
	total    int64
	written  int64
	percent  int
	progress func(int)
	err      error
}

func (output *downloadWriter) Write(chunk []byte) (int, error) {
	n, err := output.writer.Write(chunk)
	if err == nil && n != len(chunk) {
		err = io.ErrShortWrite
	}
	output.err = err
	output.written += int64(n)
	next := min(99, int(output.written*100/output.total))
	if output.progress != nil && next > output.percent {
		output.percent = next
		output.progress(next)
	}
	return n, err
}

func download(ctx context.Context, client *http.Client, staged io.Writer, release Release, progress func(int)) error {
	address := releaseWeb + "/download/" + url.PathEscape(versionTag(release.Version)) + "/" + release.asset.Name
	response, err := releaseRequest(ctx, client, address, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("%w: %w", errDownload, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GitHub returned HTTP %d", errDownload, response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != release.asset.Size {
		return fmt.Errorf("%w: content length does not match the release asset", errDownload)
	}
	hash := sha256.New()
	output := &downloadWriter{writer: io.MultiWriter(staged, hash), total: release.asset.Size, progress: progress}
	written, err := io.Copy(output, io.LimitReader(response.Body, release.asset.Size+1))
	if err != nil {
		if output.err != nil {
			return output.err
		}
		return fmt.Errorf("%w: %w", errDownload, err)
	}
	if written != release.asset.Size {
		return fmt.Errorf("%w: received %d of %d bytes", errDownload, written, release.asset.Size)
	}
	if !strings.EqualFold(release.asset.Digest, "sha256:"+hex.EncodeToString(hash.Sum(nil))) {
		return fmt.Errorf("%w: SHA-256 mismatch", errVerification)
	}
	return nil
}

func verifyProgram(ctx context.Context, path, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, path, "--version")
	prepareCommand(version)
	result, err := version.Output()
	if err != nil {
		return fmt.Errorf("read the downloaded program version: %w", err)
	}
	if strings.TrimSpace(string(result)) != expected {
		return errors.New("downloaded program version does not match the release")
	}
	check := exec.CommandContext(ctx, path, "self-check")
	prepareCommand(check)
	if err := check.Run(); err != nil {
		return fmt.Errorf("downloaded program self-check: %w", err)
	}
	return nil
}
