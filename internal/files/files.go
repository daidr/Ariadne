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
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/textutil"
)

const MaxFileBytes = 10 * 1024 * 1024
const MaxTextBytes = 1024 * 1024

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

func (store Store) Save(ctx context.Context, project, mime string, data []byte, allowText bool) (stored Stored, err error) {
	err = store.Events.Within(ctx, func(tx *eventstore.Transaction) error {
		stored, err = store.SaveTx(ctx, tx, project, mime, data, allowText)
		return err
	})
	if err != nil {
		return Stored{}, err
	}
	return stored, nil
}

// SaveTx lets an artifact and its inline images commit or roll back together.
func (store Store) SaveTx(ctx context.Context, tx *eventstore.Transaction, project, mime string, data []byte, allowText bool) (Stored, error) {
	var exists int
	if err := tx.SQL().QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", project).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Stored{}, domainerr.NotFound(project)
		}
		return Stored{}, err
	}
	given := textutil.Lower(textutil.Trim(strings.SplitN(mime, ";", 2)[0]))
	if IsImage(given) {
		mime = given
	} else if allowText && !strings.HasPrefix(given, "image/") && IsText(data) {
		mime = "text/plain"
	} else {
		key := "error.fileType"
		if allowText {
			key = "error.attachmentType"
		}
		if given == "" {
			given = "?"
		}
		return Stored{}, domainerr.New(key, map[string]any{"type": given})
	}
	limit := MaxFileBytes
	if mime == "text/plain" {
		limit = MaxTextBytes
	}
	if len(data) > limit {
		return Stored{}, domainerr.New("error.fileTooLarge", map[string]any{"max": limit / 1024 / 1024})
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

// Serve must be registered behind httpauth.Middleware. It has no public fallback
// path and uses SQLite length(data) so HEAD does not load a large BLOB into Go.
func (store Store) Serve(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("fileId")
	var mime string
	var size int64
	var data []byte
	var err error
	if request.Method == http.MethodHead {
		err = store.Events.DB().QueryRowContext(request.Context(), "SELECT mime, length(data) FROM files WHERE id = ?", id).Scan(&mime, &size)
	} else {
		var content Content
		content, err = store.Read(request.Context(), id)
		mime, data = content.MIME, content.Data
		size = int64(len(data))
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = domainerr.NotFound(id)
	}
	if err != nil {
		domainerr.Write(writer, err)
		return
	}
	if mime == "text/plain" {
		mime = "text/plain; charset=utf-8"
	}
	for key, value := range map[string]string{
		"Content-Type": mime, "Cache-Control": "private, no-store", "Content-Length": fmt.Sprint(size),
		"Cross-Origin-Resource-Policy": "same-origin", "X-Content-Type-Options": "nosniff", "Content-Security-Policy": "default-src 'none'",
	} {
		writer.Header().Set(key, value)
	}
	if request.Method != http.MethodHead {
		_, _ = writer.Write(data)
	}
}
