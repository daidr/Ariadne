// Package files stores attachments in the existing SQLite files table.
package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/textutil"
)

const MaxFileBytes = 50 * 1024 * 1024
const MaxImageBytes = 10 * 1024 * 1024
const MaxPreviewBytes = 10 * 1024 * 1024
const MaxTextBytes = 1024 * 1024
const SpreadsheetMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

const Schema = `CREATE TABLE IF NOT EXISTS files (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, mime TEXT NOT NULL,
 size INTEGER NOT NULL, sha256 TEXT NOT NULL, data BLOB NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS files_hash ON files (project_id, sha256);`

type Stored struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}
type Info struct {
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}
type Content struct {
	MIME string
	Data []byte
}
type Store struct {
	Events *eventstore.Store
	Now    func() time.Time
}

func IsImage(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		return true
	}
	return false
}

func IsText(data []byte) bool { return utf8.Valid(data) && !bytes.ContainsRune(data, 0) }

func isSafeZipDocument(mime string) bool {
	switch mime {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.oasis.opendocument.text",
		"application/vnd.oasis.opendocument.spreadsheet",
		"application/vnd.oasis.opendocument.presentation",
		"application/epub+zip",
		"application/java-archive":
		return true
	}
	return false
}

func isSafeAudio(mime string) bool {
	switch mime {
	case "audio/mpeg", "audio/wave", "audio/ogg", "audio/flac", "audio/aiff", "application/ogg":
		return true
	}
	return false
}

func isInlineServed(mime string) bool {
	return IsImage(mime) || mime == "text/plain" || mime == "video/mp4" || mime == "video/webm"
}

func binaryAttachmentMIME(given, detected string, data []byte) string {
	switch {
	case detected == "application/zip" || bytes.HasPrefix(data, []byte("PK\x05\x06")):
		if isSafeZipDocument(given) {
			return given
		}
		return "application/zip"
	case detected == "application/pdf":
		return "application/pdf"
	case detected == "application/x-gzip":
		return "application/gzip"
	case isSafeAudio(detected):
		return detected
	case bytes.HasPrefix(data, []byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c}):
		return "application/x-7z-compressed"
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07")):
		return "application/vnd.rar"
	case len(data) >= 4 && data[0] == 'B' && data[1] == 'Z' && data[2] == 'h' && data[3] >= '1' && data[3] <= '9':
		return "application/x-bzip2"
	case bytes.HasPrefix(data, []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}):
		return "application/x-xz"
	case bytes.HasPrefix(data, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "application/zstd"
	case len(data) >= 263 && bytes.Equal(data[257:262], []byte("ustar")):
		return "application/x-tar"
	default:
		return "application/octet-stream"
	}
}

func (store Store) Save(ctx context.Context, project, mime string, data []byte, allowAttachment bool) (stored Stored, err error) {
	err = store.Events.Within(ctx, func(tx *eventstore.Transaction) error {
		stored, err = store.SaveTx(ctx, tx, project, mime, data, allowAttachment)
		return err
	})
	if err != nil {
		return Stored{}, err
	}
	return stored, nil
}

// SaveTx lets an artifact and its inline images commit or roll back together.
func (store Store) SaveTx(ctx context.Context, tx *eventstore.Transaction, project, mime string, data []byte, allowAttachment bool) (Stored, error) {
	limit := MaxImageBytes
	if allowAttachment {
		limit = MaxFileBytes
	}
	if len(data) > limit {
		return Stored{}, domainerr.New("error.fileTooLarge", map[string]any{"max": limit / 1024 / 1024})
	}
	var exists int
	if err := tx.SQL().QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", project).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Stored{}, domainerr.NotFound(project)
		}
		return Stored{}, err
	}
	given := textutil.Lower(textutil.Trim(strings.SplitN(mime, ";", 2)[0]))
	detected := textutil.Lower(textutil.Trim(strings.SplitN(http.DetectContentType(data), ";", 2)[0]))
	if IsImage(given) {
		mime = given
	} else if !allowAttachment {
		if given == "" {
			given = "?"
		}
		return Stored{}, domainerr.New("error.fileType", map[string]any{"type": given})
	} else if (given == "" || given == "application/octet-stream") && IsImage(detected) {
		mime = detected
	} else if (given == "" || given == "application/octet-stream") && len(data) >= 12 && string(data[4:12]) == "ftypavif" {
		mime = "image/avif"
	} else if detected == "video/mp4" || detected == "video/webm" {
		mime = detected
	} else if isSpreadsheet(data) {
		mime = SpreadsheetMIME
	} else if detected == "application/zip" || bytes.HasPrefix(data, []byte("PK\x05\x06")) {
		if isSafeZipDocument(given) {
			mime = given
		} else {
			mime = "application/zip"
		}
	} else if detected == "application/pdf" {
		mime = "application/pdf"
	} else if IsText(data) && (len(data) > 0 || given == "" || given == "application/octet-stream" || strings.HasPrefix(given, "text/")) {
		mime = "text/plain"
	} else {
		mime = binaryAttachmentMIME(given, detected, data)
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	var id string
	err := tx.SQL().QueryRowContext(ctx, "SELECT id FROM files WHERE project_id = ? AND sha256 = ? AND mime = ?", project, digest, mime).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Stored{}, err
	}
	if id == "" {
		random := make([]byte, 18)
		if _, err := rand.Read(random); err != nil {
			return Stored{}, err
		}
		id = "fil_" + base64.RawURLEncoding.EncodeToString(random)
		now := time.Now()
		if store.Now != nil {
			now = store.Now()
		}
		if _, err := tx.SQL().ExecContext(ctx, "INSERT INTO files (id, project_id, mime, size, sha256, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", id, project, mime, len(data), digest, data, now.UnixMilli()); err != nil {
			return Stored{}, err
		}
	}
	return Stored{ID: id, URL: "/api/files/" + id, MIME: mime, Size: int64(len(data))}, nil
}

func (store Store) Info(ctx context.Context, project, id string) (*Info, error) {
	var info Info
	err := store.Events.DB().QueryRowContext(ctx, "SELECT mime, size FROM files WHERE id = ? AND project_id = ?", id, project).Scan(&info.MIME, &info.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func (store Store) Read(ctx context.Context, id string) (Content, error) {
	var content Content
	err := store.Events.DB().QueryRowContext(ctx, "SELECT mime, data FROM files WHERE id = ?", id).Scan(&content.MIME, &content.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return Content{}, domainerr.NotFound(id)
	}
	return content, err
}

const fileBlobChunkBytes = 1 << 20 // 1 MiB per SQLite substr read

type sqliteBlobReader struct {
	ctx    context.Context
	db     *sql.DB
	id     string
	size   int64
	offset int64
	buf    []byte
	bufPos int
	reads  int
}

func (r *sqliteBlobReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.offset + offset
	case io.SeekEnd:
		next = r.size + offset
	default:
		return 0, errors.New("files: invalid seek whence")
	}
	if next < 0 {
		return 0, errors.New("files: negative seek offset")
	}
	if next != r.offset {
		r.buf = nil
		r.bufPos = 0
		r.offset = next
	}
	return r.offset, nil
}

func (r *sqliteBlobReader) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.bufPos < len(r.buf) {
		n := copy(p, r.buf[r.bufPos:])
		r.bufPos += n
		r.offset += int64(n)
		if r.bufPos >= len(r.buf) {
			r.buf = nil
			r.bufPos = 0
		}
		return n, nil
	}

	remaining := r.size - r.offset
	fetchSize := int64(len(p))
	if fetchSize >= 32*1024 && fetchSize < fileBlobChunkBytes {
		fetchSize = fileBlobChunkBytes
	}
	if fetchSize > remaining {
		fetchSize = remaining
	}

	var chunk []byte
	err := r.db.QueryRowContext(
		r.ctx,
		"SELECT substr(data, ?, ?) FROM files WHERE id = ?",
		r.offset+1,
		fetchSize,
		r.id,
	).Scan(&chunk)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, io.ErrUnexpectedEOF
	}
	if err != nil {
		return 0, err
	}
	if len(chunk) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	r.reads++

	n := copy(p, chunk)
	r.offset += int64(n)
	if n < len(chunk) {
		r.buf = chunk[n:]
		r.bufPos = 0
	}
	return n, nil
}

// Serve must be registered behind httpauth.Middleware. It has no public fallback
// path, checks sha256/size metadata before touching the BLOB, and streams data
// in bounded chunks with ETag/304 and HTTP Range support.
func (store Store) Serve(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("fileId")
	var (
		mime   string
		size   int64
		digest string
	)
	err := store.Events.DB().QueryRowContext(
		request.Context(),
		"SELECT mime, size, sha256 FROM files WHERE id = ?",
		id,
	).Scan(&mime, &size, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		err = domainerr.NotFound(id)
	}
	if err != nil {
		domainerr.Write(writer, err)
		return
	}
	if size < 0 {
		size = 0
	}

	contentType := mime
	switch contentType {
	case "":
		contentType = "application/octet-stream"
	case "text/plain":
		contentType = "text/plain; charset=utf-8"
	}
	header := writer.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "private, no-cache")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'")
	if !isInlineServed(mime) {
		header.Set("Content-Disposition", "attachment")
	}
	if digest != "" {
		header.Set("ETag", fmt.Sprintf("%q", digest))
	}

	reader := &sqliteBlobReader{
		ctx:  request.Context(),
		db:   store.Events.DB(),
		id:   id,
		size: size,
	}
	http.ServeContent(writer, request, "", time.Time{}, reader)
}
